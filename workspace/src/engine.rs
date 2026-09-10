//! Container-engine access for exec sessions inside a workspace.

use anyhow::{Context, Result};
use bollard::exec::{CreateExecOptions, ResizeExecOptions, StartExecResults};
use bollard::Docker;
use futures_util::StreamExt;

pub struct PodmanExec {
    docker: Docker,
}

/// A live exec session: the shell's TTY output, a writer for its stdin, and the
/// exec id needed to resize or reap it.
pub struct TerminalHandle {
    pub id: String,
    pub output: std::pin::Pin<Box<dyn futures_util::Stream<Item = Result<bytes::Bytes>> + Send>>,
    pub input: std::pin::Pin<Box<dyn tokio::io::AsyncWrite + Send>>,
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

    /// The underlying bollard handle, so integration tests can stand up a
    /// throwaway container to exec into without opening a second socket.
    pub fn docker(&self) -> &Docker {
        &self.docker
    }

    /// Exec the given shell in a running container with a PTY attached, sized to
    /// `cols` x `rows` before the shell paints its first prompt.
    pub async fn start_terminal(
        &self,
        container: &str,
        shell: &str,
        cols: u16,
        rows: u16,
    ) -> Result<TerminalHandle> {
        let exec = self
            .docker
            .create_exec(
                container,
                CreateExecOptions {
                    attach_stdin: Some(true),
                    attach_stdout: Some(true),
                    attach_stderr: Some(true),
                    tty: Some(true),
                    cmd: Some(vec![shell.to_string()]),
                    ..Default::default()
                },
            )
            .await
            .context("create exec")?;

        // Size the PTY before the shell starts so its prompt wraps correctly.
        let _ = self
            .docker
            .resize_exec(
                &exec.id,
                ResizeExecOptions {
                    height: rows,
                    width: cols,
                },
            )
            .await;

        match self
            .docker
            .start_exec(&exec.id, None)
            .await
            .context("start exec")?
        {
            StartExecResults::Attached { output, input } => Ok(TerminalHandle {
                id: exec.id,
                output: Box::pin(
                    output.map(|r| r.map(|log| log.into_bytes()).context("exec output")),
                ),
                input,
            }),
            StartExecResults::Detached => anyhow::bail!("exec started detached"),
        }
    }

    /// Resize a live exec's PTY in response to a client resize frame.
    pub async fn resize_terminal(&self, id: &str, cols: u16, rows: u16) -> Result<()> {
        self.docker
            .resize_exec(
                id,
                ResizeExecOptions {
                    height: rows,
                    width: cols,
                },
            )
            .await
            .context("resize exec")?;
        Ok(())
    }

    /// The exit code of a finished exec, or `None` while it is still running.
    pub async fn terminal_exit_code(&self, id: &str) -> Result<Option<i32>> {
        let inspect = self.docker.inspect_exec(id).await.context("inspect exec")?;
        Ok(inspect.exit_code.map(|c| c as i32))
    }
}

#[tonic::async_trait]
impl crate::terminal::ExecControl for PodmanExec {
    async fn resize(&self, id: &str, cols: u16, rows: u16) {
        let _ = self.resize_terminal(id, cols, rows).await;
    }
}
