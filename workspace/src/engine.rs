//! Container-engine access for exec sessions inside a workspace.

use anyhow::{Context, Result};
use bollard::Docker;

pub struct PodmanExec {
    docker: Docker,
}

impl PodmanExec {
    pub fn connect(socket: Option<&str>) -> Result<Self> {
        let docker = match socket {
            Some(path) => Docker::connect_with_socket(path, 120, bollard::API_DEFAULT_VERSION)
                .context("connect to podman socket")?,
            None => Docker::connect_with_socket_defaults().context("connect to podman")?,
        };
        Ok(Self { docker })
    }

    pub async fn ping(&self) -> Result<()> {
        self.docker.ping().await.context("podman ping")?;
        Ok(())
    }

    // The terminal handler reaches for the raw client to open exec sessions;
    // that lands in the next commit, so nothing calls this yet.
    #[allow(dead_code)]
    pub(crate) fn docker(&self) -> &Docker {
        &self.docker
    }
}
