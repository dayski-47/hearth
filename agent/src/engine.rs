use anyhow::{Context, Result};
use async_trait::async_trait;
use bollard::Docker;

#[derive(Clone, Debug)]
pub struct ContainerSpec {
    pub name: String,
    pub image: String,
    pub cmd: Vec<String>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ContainerRunState {
    Running,
    Stopped,
    Missing,
}

#[async_trait]
pub trait ContainerEngine: Send + Sync + 'static {
    async fn ping(&self) -> Result<()>;
    async fn create_container(&self, spec: ContainerSpec) -> Result<String>;
    async fn start(&self, id: &str) -> Result<()>;
    async fn stop(&self, id: &str) -> Result<()>;
    async fn remove(&self, id: &str) -> Result<()>;
    async fn inspect_state(&self, id: &str) -> Result<ContainerRunState>;
}

pub struct PodmanEngine {
    docker: Docker,
}

impl PodmanEngine {
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

    async fn create_container(&self, spec: ContainerSpec) -> Result<String> {
        use bollard::container::{Config, CreateContainerOptions};
        let cfg = Config {
            image: Some(spec.image.clone()),
            cmd: if spec.cmd.is_empty() {
                None
            } else {
                Some(spec.cmd.clone())
            },
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
        self.docker
            .remove_container(
                id,
                Some(RemoveContainerOptions {
                    force: true,
                    v: true,
                    ..Default::default()
                }),
            )
            .await
            .context("remove container")?;
        Ok(())
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
