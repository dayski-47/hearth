use std::collections::HashMap;

use anyhow::{Context, Result};
use async_trait::async_trait;
use bollard::models::{HostConfig, Mount, MountTypeEnum};
use bollard::Docker;
use futures_util::StreamExt;

#[derive(Clone, Debug)]
pub enum NetworkMode {
    Egress,
    None,
}

/// The dedicated outbound-only Podman network every "egress" workspace joins.
pub const EGRESS_NETWORK: &str = "hearth-egress";

#[derive(Clone, Debug)]
pub struct WorkspaceContainerSpec {
    pub name: String,
    pub image: String,
    pub volume: String,
    pub network: NetworkMode,
    pub userns: String,
    pub cpu_millis: u32,
    pub memory_bytes: u64,
    pub pids: u32,
}

/// Translate a workspace spec into the bollard HostConfig that enforces the
/// section 6 security model. Kept pure so it can be tested without Podman.
pub fn workspace_host_config(spec: &WorkspaceContainerSpec) -> HostConfig {
    let mem = spec.memory_bytes as i64;
    let network_mode = match spec.network {
        NetworkMode::Egress => EGRESS_NETWORK.to_string(),
        NetworkMode::None => "none".to_string(),
    };
    // "keep-id" is passed through; anything else ("auto") means the host default.
    let userns = if spec.userns == "keep-id" {
        "keep-id"
    } else {
        ""
    };

    let mut tmpfs = HashMap::new();
    tmpfs.insert("/tmp".to_string(), "size=64m,mode=1777".to_string());

    HostConfig {
        memory: Some(mem),
        memory_swap: Some(mem),
        nano_cpus: Some(spec.cpu_millis as i64 * 1_000_000),
        pids_limit: Some(spec.pids as i64),
        cap_drop: Some(vec!["ALL".to_string()]),
        security_opt: Some(vec!["no-new-privileges".to_string()]),
        readonly_rootfs: Some(true),
        ulimits: Some(vec![bollard::models::ResourcesUlimits {
            name: Some("nofile".to_string()),
            soft: Some(4096),
            hard: Some(8192),
        }]),
        userns_mode: Some(userns.to_string()),
        network_mode: Some(network_mode),
        tmpfs: Some(tmpfs),
        mounts: Some(vec![Mount {
            target: Some("/workspace".to_string()),
            source: Some(spec.volume.clone()),
            typ: Some(MountTypeEnum::VOLUME),
            ..Default::default()
        }]),
        ..Default::default()
    }
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum ContainerRunState {
    Running,
    Stopped,
    #[default]
    Missing,
}

#[async_trait]
pub trait ContainerEngine: Send + Sync + 'static {
    async fn ping(&self) -> Result<()>;
    async fn ensure_image(&self, image: &str) -> Result<()>;
    async fn ensure_network(&self, name: &str) -> Result<()>;
    async fn create_volume(&self, name: &str) -> Result<()>;
    async fn remove_volume(&self, name: &str) -> Result<()>;
    async fn create_container(&self, spec: WorkspaceContainerSpec) -> Result<String>;
    async fn start(&self, id: &str) -> Result<()>;
    async fn stop(&self, id: &str) -> Result<()>;
    async fn remove(&self, id: &str) -> Result<()>;
    async fn inspect_state(&self, id: &str) -> Result<ContainerRunState>;
}

pub struct PodmanEngine {
    docker: Docker,
}

impl PodmanEngine {
    /// The underlying bollard handle, so integration tests can inspect what
    /// actually landed on a live container without opening a second socket.
    pub fn docker(&self) -> &Docker {
        &self.docker
    }

    pub fn connect(socket: Option<&str>) -> Result<Self> {
        let docker = match socket {
            Some(path) => Docker::connect_with_socket(path, 120, bollard::API_DEFAULT_VERSION)
                .context("connect to podman socket")?,
            None => {
                Docker::connect_with_socket_defaults().context("connect to podman (defaults)")?
            }
        };
        Ok(Self { docker })
    }
}

#[async_trait]
impl ContainerEngine for PodmanEngine {
    async fn ping(&self) -> Result<()> {
        self.docker.ping().await.context("podman ping")?;
        Ok(())
    }

    async fn ensure_image(&self, image: &str) -> Result<()> {
        use bollard::image::CreateImageOptions;
        match self.docker.inspect_image(image).await {
            Ok(_) => Ok(()),
            Err(bollard::errors::Error::DockerResponseServerError {
                status_code: 404, ..
            }) => {
                let mut stream = self.docker.create_image(
                    Some(CreateImageOptions {
                        from_image: image.to_string(),
                        ..Default::default()
                    }),
                    None,
                    None,
                );
                while let Some(x) = stream.next().await {
                    // Podman reports a failed pull (manifest unknown, auth
                    // denied) as HTTP 200 with an error frame in the stream, so
                    // the transport-level `?` above is not enough.
                    let info = x.context("pull image")?;
                    if let Some(err) = info.error {
                        anyhow::bail!("pull image {image}: {err}");
                    }
                }
                Ok(())
            }
            Err(e) => Err(e).context("inspect image"),
        }
    }

    async fn ensure_network(&self, name: &str) -> Result<()> {
        use bollard::network::{CreateNetworkOptions, InspectNetworkOptions};
        // Inspect-then-create, like ensure_image: Podman does not answer a
        // duplicate create with a 409, so keying idempotency off the create
        // response would fail every workspace after the first.
        match self
            .docker
            .inspect_network(name, None::<InspectNetworkOptions<String>>)
            .await
        {
            Ok(_) => Ok(()),
            Err(bollard::errors::Error::DockerResponseServerError {
                status_code: 404, ..
            }) => {
                self.docker
                    .create_network(CreateNetworkOptions {
                        name: name.to_string(),
                        check_duplicate: false,
                        ..Default::default()
                    })
                    .await
                    .context("create network")?;
                Ok(())
            }
            Err(e) => Err(e).context("ensure network"),
        }
    }

    async fn create_volume(&self, name: &str) -> Result<()> {
        use bollard::volume::CreateVolumeOptions;
        self.docker
            .create_volume(CreateVolumeOptions {
                name: name.to_string(),
                driver: "local".to_string(),
                ..Default::default()
            })
            .await
            .context("create volume")?;
        Ok(())
    }

    async fn remove_volume(&self, name: &str) -> Result<()> {
        use bollard::volume::RemoveVolumeOptions;
        match self
            .docker
            .remove_volume(name, Some(RemoveVolumeOptions { force: true }))
            .await
        {
            Ok(()) => Ok(()),
            Err(bollard::errors::Error::DockerResponseServerError {
                status_code: 404, ..
            }) => Ok(()),
            Err(e) => Err(e).context("remove volume"),
        }
    }

    async fn create_container(&self, spec: WorkspaceContainerSpec) -> Result<String> {
        use bollard::container::{Config, CreateContainerOptions};
        let mut labels = std::collections::HashMap::new();
        labels.insert("hearth.workspace".to_string(), spec.name.clone());
        let cfg = Config {
            image: Some(spec.image.clone()),
            cmd: Some(vec!["sleep".to_string(), "infinity".to_string()]),
            labels: Some(labels),
            host_config: Some(workspace_host_config(&spec)),
            ..Default::default()
        };
        let res = self
            .docker
            .create_container(
                Some(CreateContainerOptions {
                    name: spec.name.clone(),
                    platform: None,
                }),
                cfg,
            )
            .await
            .context("create container")?;
        Ok(res.id)
    }

    async fn start(&self, id: &str) -> Result<()> {
        self.docker
            .start_container(
                id,
                None::<bollard::container::StartContainerOptions<String>>,
            )
            .await
            .context("start container")?;
        Ok(())
    }

    async fn stop(&self, id: &str) -> Result<()> {
        self.docker
            .stop_container(id, None)
            .await
            .context("stop container")?;
        Ok(())
    }

    async fn remove(&self, id: &str) -> Result<()> {
        use bollard::container::RemoveContainerOptions;
        match self
            .docker
            .remove_container(
                id,
                Some(RemoveContainerOptions {
                    force: true,
                    v: true,
                    ..Default::default()
                }),
            )
            .await
        {
            Ok(()) => Ok(()),
            // Teardown is idempotent: a workspace whose container never came up
            // must still be destroyable.
            Err(bollard::errors::Error::DockerResponseServerError {
                status_code: 404, ..
            }) => Ok(()),
            Err(e) => Err(e).context("remove container"),
        }
    }

    async fn inspect_state(&self, id: &str) -> Result<ContainerRunState> {
        match self.docker.inspect_container(id, None).await {
            Ok(info) => {
                let running = info.state.and_then(|s| s.running).unwrap_or(false);
                Ok(if running {
                    ContainerRunState::Running
                } else {
                    ContainerRunState::Stopped
                })
            }
            Err(bollard::errors::Error::DockerResponseServerError {
                status_code: 404, ..
            }) => Ok(ContainerRunState::Missing),
            Err(e) => Err(e).context("inspect container"),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn host_config_applies_the_security_model() {
        let spec = WorkspaceContainerSpec {
            name: "hearth-ws-abc".into(),
            image: "busybox:stable".into(),
            volume: "hearth-ws-abc".into(),
            network: NetworkMode::Egress,
            userns: "keep-id".into(),
            cpu_millis: 2000,
            memory_bytes: 2 << 30,
            pids: 512,
        };
        let hc = workspace_host_config(&spec);

        assert_eq!(hc.cap_drop.as_deref(), Some(&["ALL".to_string()][..]));
        assert_eq!(
            hc.security_opt.as_deref(),
            Some(&["no-new-privileges".to_string()][..])
        );
        assert_eq!(hc.readonly_rootfs, Some(true));
        assert_eq!(hc.userns_mode.as_deref(), Some("keep-id"));
        assert_eq!(hc.network_mode.as_deref(), Some("hearth-egress"));
        assert_eq!(hc.memory, Some(2i64 << 30));
        assert_eq!(hc.memory_swap, hc.memory); // swap disabled
        assert_eq!(hc.nano_cpus, Some(2_000_000_000)); // 2000 millis -> 2 CPUs
        assert_eq!(hc.pids_limit, Some(512));
        let ulimits = hc.ulimits.as_ref().unwrap();
        assert!(ulimits.iter().any(|u| u.name.as_deref() == Some("nofile")
            && u.soft == Some(4096)
            && u.hard == Some(8192)));
        // /workspace is the volume, /tmp is a capped tmpfs, nothing else writable
        let mounts = hc.mounts.as_ref().unwrap();
        assert!(mounts
            .iter()
            .any(|m| m.target.as_deref() == Some("/workspace")
                && m.source.as_deref() == Some("hearth-ws-abc")
                && m.typ == Some(bollard::models::MountTypeEnum::VOLUME)));
        assert!(hc.tmpfs.as_ref().unwrap().contains_key("/tmp"));
    }

    #[test]
    fn host_config_none_network_and_auto_userns() {
        let spec = WorkspaceContainerSpec {
            name: "x".into(),
            image: "x".into(),
            volume: "x".into(),
            network: NetworkMode::None,
            userns: "auto".into(),
            cpu_millis: 1000,
            memory_bytes: 1 << 30,
            pids: 100,
        };
        let hc = workspace_host_config(&spec);
        assert_eq!(hc.network_mode.as_deref(), Some("none"));
        assert_eq!(hc.userns_mode.as_deref(), Some("")); // "auto" -> host default
    }
}
