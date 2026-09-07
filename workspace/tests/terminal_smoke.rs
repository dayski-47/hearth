//! Gated smoke test: drive a real shell over `PodmanExec::start_terminal`.
//!
//! Needs a container engine. Set `HEARTH_PODMAN_IT=1` to run it and
//! `HEARTH_PODMAN_SOCKET` to point at the socket (the host also runs Docker on
//! the default socket, so the podman path must be given explicitly).

use std::time::Duration;

use bollard::container::{Config, CreateContainerOptions, RemoveContainerOptions};
use bollard::image::CreateImageOptions;
use futures_util::StreamExt;
use hearth_workspace::engine::PodmanExec;
use tokio::io::AsyncWriteExt;
use tokio::time::timeout;

const IMAGE: &str = "busybox:stable";

#[tokio::test]
async fn shell_round_trip_over_exec() {
    if std::env::var("HEARTH_PODMAN_IT").as_deref() != Ok("1") {
        eprintln!("skipped: set HEARTH_PODMAN_IT=1 (and HEARTH_PODMAN_SOCKET) to run");
        return;
    }

    let socket = std::env::var("HEARTH_PODMAN_SOCKET").ok();
    let exec = PodmanExec::connect(socket.as_deref()).expect("connect to podman");
    let name = format!("hearth-ws-smoke-{}", std::process::id());

    // Make sure a stale container from a crashed run does not get in the way.
    remove(&exec, &name).await;

    let result: Result<(), String> = async {
        let mut pull = exec.docker().create_image(
            Some(CreateImageOptions {
                from_image: IMAGE.to_string(),
                ..Default::default()
            }),
            None,
            None,
        );
        while let Some(step) = pull.next().await {
            step.map_err(|e| format!("pull image: {e}"))?;
        }

        exec.docker()
            .create_container(
                Some(CreateContainerOptions {
                    name: name.clone(),
                    platform: None,
                }),
                Config {
                    image: Some(IMAGE.to_string()),
                    cmd: Some(vec!["sleep".to_string(), "300".to_string()]),
                    ..Default::default()
                },
            )
            .await
            .map_err(|e| format!("create container: {e}"))?;
        exec.docker()
            .start_container(
                &name,
                None::<bollard::container::StartContainerOptions<String>>,
            )
            .await
            .map_err(|e| format!("start container: {e}"))?;

        let mut handle = exec
            .start_terminal(&name, "/bin/sh", 80, 24)
            .await
            .map_err(|e| format!("start terminal: {e:#}"))?;

        handle
            .input
            .write_all(b"echo hearthhi\n")
            .await
            .map_err(|e| format!("write stdin: {e}"))?;
        handle.input.flush().await.ok();

        let mut seen = Vec::new();
        loop {
            let chunk = timeout(Duration::from_secs(10), handle.output.next())
                .await
                .map_err(|_| "timed out waiting for echo output".to_string())?;
            match chunk {
                Some(Ok(bytes)) => seen.extend_from_slice(&bytes),
                Some(Err(e)) => return Err(format!("exec output: {e:#}")),
                None => return Err("exec stream ended before echo output".to_string()),
            }
            if String::from_utf8_lossy(&seen).contains("hearthhi") {
                break;
            }
        }

        handle
            .input
            .write_all(b"exit\n")
            .await
            .map_err(|e| format!("write exit: {e}"))?;
        handle.input.flush().await.ok();

        // The shell has exited: the output stream must drain to its end.
        loop {
            let chunk = timeout(Duration::from_secs(10), handle.output.next())
                .await
                .map_err(|_| "timed out waiting for stream end".to_string())?;
            match chunk {
                Some(Ok(_)) => continue,
                Some(Err(e)) => return Err(format!("exec output after exit: {e:#}")),
                None => break,
            }
        }

        let code = exec
            .terminal_exit_code(&handle.id)
            .await
            .map_err(|e| format!("exit code: {e:#}"))?;
        if code != Some(0) {
            return Err(format!("shell exited with {code:?}, expected Some(0)"));
        }
        Ok(())
    }
    .await;

    remove(&exec, &name).await;
    result.expect("terminal smoke");
}

/// A browser tab closed at an idle prompt: nothing is ever written to stdin and
/// the shell never emits a byte. Dropping the handle must let the container be
/// torn down promptly rather than hang on the still-open exec.
#[tokio::test]
async fn idle_terminal_tears_down_on_drop() {
    if std::env::var("HEARTH_PODMAN_IT").as_deref() != Ok("1") {
        eprintln!("skipped: set HEARTH_PODMAN_IT=1 (and HEARTH_PODMAN_SOCKET) to run");
        return;
    }

    let socket = std::env::var("HEARTH_PODMAN_SOCKET").ok();
    let exec = PodmanExec::connect(socket.as_deref()).expect("connect to podman");
    let name = format!("hearth-ws-smoke-idle-{}", std::process::id());

    remove(&exec, &name).await;

    let result: Result<(), String> = async {
        let mut pull = exec.docker().create_image(
            Some(CreateImageOptions {
                from_image: IMAGE.to_string(),
                ..Default::default()
            }),
            None,
            None,
        );
        while let Some(step) = pull.next().await {
            step.map_err(|e| format!("pull image: {e}"))?;
        }

        exec.docker()
            .create_container(
                Some(CreateContainerOptions {
                    name: name.clone(),
                    platform: None,
                }),
                Config {
                    image: Some(IMAGE.to_string()),
                    cmd: Some(vec!["sleep".to_string(), "300".to_string()]),
                    ..Default::default()
                },
            )
            .await
            .map_err(|e| format!("create container: {e}"))?;
        exec.docker()
            .start_container(
                &name,
                None::<bollard::container::StartContainerOptions<String>>,
            )
            .await
            .map_err(|e| format!("start container: {e}"))?;

        let handle = exec
            .start_terminal(&name, "/bin/sh", 80, 24)
            .await
            .map_err(|e| format!("start terminal: {e:#}"))?;

        // The client goes away without ever touching stdin or reading stdout.
        drop(handle);

        // Both exec halves are gone now, so the shell can be reaped and the
        // container removed without waiting on a dead connection.
        timeout(
            Duration::from_secs(3),
            exec.docker().remove_container(
                &name,
                Some(RemoveContainerOptions {
                    force: true,
                    v: true,
                    ..Default::default()
                }),
            ),
        )
        .await
        .map_err(|_| "remove_container hung after dropping an idle terminal".to_string())?
        .map_err(|e| format!("remove container: {e}"))?;
        Ok(())
    }
    .await;

    remove(&exec, &name).await;
    result.expect("idle terminal teardown");
}

async fn remove(exec: &PodmanExec, name: &str) {
    let _ = exec
        .docker()
        .remove_container(
            name,
            Some(RemoveContainerOptions {
                force: true,
                v: true,
                ..Default::default()
            }),
        )
        .await;
}
