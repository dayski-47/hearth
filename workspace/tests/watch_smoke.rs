//! Gated smoke test: run a real `notify` watch against a real podman volume and
//! confirm the change stream reports creates and removes at the volume root and
//! stays quiet about the default ignore set.
//!
//! Needs a container engine whose volumes live on a path this process can also
//! touch directly (podman). Set `HEARTH_PODMAN_IT=1` to run it and
//! `HEARTH_PODMAN_SOCKET` to point at the socket.

use std::time::Duration;

use bollard::volume::{CreateVolumeOptions, RemoveVolumeOptions};
use futures_util::StreamExt;
use hearth_proto::hearth::v1::file_event::Kind;
use hearth_workspace::engine::PodmanExec;
use hearth_workspace::files::Files;

const READ_CAP: u64 = 10 * 1024 * 1024;

#[tokio::test]
async fn watch_reports_volume_changes() {
    if std::env::var("HEARTH_PODMAN_IT").as_deref() != Ok("1") {
        eprintln!("skipped: set HEARTH_PODMAN_IT=1 (and HEARTH_PODMAN_SOCKET) to run");
        return;
    }

    let socket = std::env::var("HEARTH_PODMAN_SOCKET").ok();
    let exec = PodmanExec::connect(socket.as_deref()).expect("connect to podman");
    let docker = exec.docker().clone();

    // `workspace_container_name(id)` == `hearth-ws-<id>`, and Files looks the
    // volume up under that name, so the id and the volume name line up.
    let id = format!("wsmoke{}", std::process::id());
    let volume = format!("hearth-ws-{id}");

    let _ = docker
        .remove_volume(&volume, Some(RemoveVolumeOptions { force: true }))
        .await;
    docker
        .create_volume(CreateVolumeOptions {
            name: volume.clone(),
            ..Default::default()
        })
        .await
        .expect("create volume");

    let result = run(&docker, &id, &volume).await;

    let _ = docker
        .remove_volume(&volume, Some(RemoveVolumeOptions { force: true }))
        .await;

    result.expect("watch smoke");
}

async fn run(docker: &bollard::Docker, id: &str, volume: &str) -> Result<(), String> {
    let files = Files::new(docker.clone(), READ_CAP);

    let mountpoint = docker
        .inspect_volume(volume)
        .await
        .map_err(|e| format!("inspect_volume: {e}"))?
        .mountpoint;

    let mut stream = files.watch(id).await.map_err(|e| format!("watch: {e}"))?;

    // Touch the volume from a task once the watch is up: create, modify, remove,
    // spaced out so the 150 ms debounce flushes each step instead of coalescing
    // a create straight into a remove.
    let mp = mountpoint.clone();
    let writer = tokio::spawn(async move {
        tokio::time::sleep(Duration::from_millis(400)).await;
        std::fs::write(format!("{mp}/hi.txt"), b"one").unwrap();
        tokio::time::sleep(Duration::from_millis(400)).await;
        std::fs::write(format!("{mp}/hi.txt"), b"one two three").unwrap();
        tokio::time::sleep(Duration::from_millis(400)).await;
        std::fs::remove_file(format!("{mp}/hi.txt")).unwrap();
    });

    // Collect (path, kind) pairs until both the create and the remove show up,
    // or the deadline passes.
    let mut seen: Vec<(String, i32)> = Vec::new();
    let deadline = tokio::time::Instant::now() + Duration::from_secs(6);
    loop {
        let left = deadline.saturating_duration_since(tokio::time::Instant::now());
        if left.is_zero() {
            break;
        }
        match tokio::time::timeout(left, stream.next()).await {
            Ok(Some(Ok(ev))) => {
                seen.push((ev.path.clone(), ev.kind));
                let have_create = seen
                    .iter()
                    .any(|(p, k)| p == "hi.txt" && *k == Kind::Created as i32);
                let have_remove = seen
                    .iter()
                    .any(|(p, k)| p == "hi.txt" && *k == Kind::Removed as i32);
                if have_create && have_remove {
                    break;
                }
            }
            Ok(Some(Err(e))) => return Err(format!("stream error: {e}")),
            Ok(None) => return Err("stream ended early".into()),
            Err(_) => break,
        }
    }
    let _ = writer.await;

    if !seen
        .iter()
        .any(|(p, k)| p == "hi.txt" && *k == Kind::Created as i32)
    {
        return Err(format!("no (hi.txt, CREATED) in {seen:?}"));
    }
    if !seen
        .iter()
        .any(|(p, k)| p == "hi.txt" && *k == Kind::Removed as i32)
    {
        return Err(format!("no (hi.txt, REMOVED) in {seen:?}"));
    }

    // A write under `.git/` is in the default ignore set: nothing about it
    // should reach the stream in a one-second window.
    std::fs::create_dir_all(format!("{mountpoint}/.git"))
        .map_err(|e| format!("mkdir .git: {e}"))?;
    std::fs::write(format!("{mountpoint}/.git/x"), b"ignored")
        .map_err(|e| format!("write .git/x: {e}"))?;

    let quiet_until = tokio::time::Instant::now() + Duration::from_secs(1);
    loop {
        let left = quiet_until.saturating_duration_since(tokio::time::Instant::now());
        if left.is_zero() {
            break;
        }
        match tokio::time::timeout(left, stream.next()).await {
            Ok(Some(Ok(ev))) => {
                if ev.path.contains(".git") {
                    return Err(format!("ignored path leaked through: {ev:?}"));
                }
            }
            Ok(Some(Err(e))) => return Err(format!("stream error during quiet window: {e}")),
            Ok(None) => break,
            Err(_) => break,
        }
    }

    Ok(())
}
