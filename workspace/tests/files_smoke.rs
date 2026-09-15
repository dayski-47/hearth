//! Gated smoke test: drive the six file RPCs end to end against a real volume.
//!
//! Needs a container engine whose volumes live on a path this process can also
//! touch directly (podman). Set `HEARTH_PODMAN_IT=1` to run it and
//! `HEARTH_PODMAN_SOCKET` to point at the socket.
//!
//! The service takes a client-streaming `WriteFile`, which can only be fed
//! through a real transport, so the test stands the gRPC service up on a
//! loopback port and talks to it with the generated client. That also means the
//! round trip exercises `grpc.rs` exactly as the gateway would drive it.

use std::sync::Arc;

use bollard::container::{Config, CreateContainerOptions, RemoveContainerOptions};
use bollard::models::{HostConfig, Mount, MountTypeEnum};
use bollard::volume::{CreateVolumeOptions, RemoveVolumeOptions};
use futures_util::StreamExt;
use hearth_proto::hearth::v1::{
    workspace_io_client::WorkspaceIoClient, workspace_io_server::WorkspaceIoServer,
    write_file_frame::Msg, CreateNodeRequest, DeleteNodeRequest, ListDirRequest, ReadFileRequest,
    RenameNodeRequest, WriteFileEnd, WriteFileFrame, WriteFileInit,
};
use hearth_workspace::engine::PodmanExec;
use hearth_workspace::files::Files;
use hearth_workspace::grpc::WorkspaceSvc;
use tokio::net::TcpListener;
use tokio_stream::wrappers::TcpListenerStream;

const READ_CAP: u64 = 10 * 1024 * 1024;

#[tokio::test]
async fn file_rpcs_round_trip_against_a_volume() {
    if std::env::var("HEARTH_PODMAN_IT").as_deref() != Ok("1") {
        eprintln!("skipped: set HEARTH_PODMAN_IT=1 (and HEARTH_PODMAN_SOCKET) to run");
        return;
    }

    let socket = std::env::var("HEARTH_PODMAN_SOCKET").ok();
    let exec = PodmanExec::connect(socket.as_deref()).expect("connect to podman");
    let docker = exec.docker().clone();

    // `workspace_container_name(id)` == `hearth-ws-<id>`, and it names both
    // the volume and the container - `Files::root` resolves the container's
    // own `/workspace` mount, which is why both exist here even though this
    // test never starts the container.
    let id = format!("fs{}", std::process::id());
    let name = format!("hearth-ws-{id}");

    // Clear anything a crashed run left behind.
    let _ = docker
        .remove_container(
            &name,
            Some(RemoveContainerOptions {
                force: true,
                ..Default::default()
            }),
        )
        .await;
    let _ = docker
        .remove_volume(&name, Some(RemoveVolumeOptions { force: true }))
        .await;

    docker
        .create_volume(CreateVolumeOptions {
            name: name.clone(),
            ..Default::default()
        })
        .await
        .expect("create volume");

    docker
        .create_container(
            Some(CreateContainerOptions {
                name: name.clone(),
                platform: None,
            }),
            Config {
                image: Some("docker.io/library/busybox:stable".to_string()),
                host_config: Some(HostConfig {
                    mounts: Some(vec![Mount {
                        target: Some("/workspace".to_string()),
                        source: Some(name.clone()),
                        typ: Some(MountTypeEnum::VOLUME),
                        ..Default::default()
                    }]),
                    ..Default::default()
                }),
                ..Default::default()
            },
        )
        .await
        .expect("create container with a volume mount");

    let result = run(&docker, exec, &id, &name).await;

    let _ = docker
        .remove_container(
            &name,
            Some(RemoveContainerOptions {
                force: true,
                ..Default::default()
            }),
        )
        .await;
    let _ = docker
        .remove_volume(&name, Some(RemoveVolumeOptions { force: true }))
        .await;

    result.expect("file rpc round trip");
}

/// A bind-mounted workspace (persistent host-mounted workspaces feature) has
/// no Podman volume at all - the container mounts a real host directory
/// straight into `/workspace`. `Files::root` must resolve that from the
/// container's own mount config, not from `inspect_volume`.
#[tokio::test]
async fn file_rpcs_round_trip_against_a_bind_mount() {
    if std::env::var("HEARTH_PODMAN_IT").as_deref() != Ok("1") {
        eprintln!("skipped: set HEARTH_PODMAN_IT=1 (and HEARTH_PODMAN_SOCKET) to run");
        return;
    }

    let socket = std::env::var("HEARTH_PODMAN_SOCKET").ok();
    let exec = PodmanExec::connect(socket.as_deref()).expect("connect to podman");
    let docker = exec.docker().clone();

    let id = format!("fsbind{}", std::process::id());
    let name = format!("hearth-ws-{id}");
    let host_dir = tempfile::tempdir().expect("host tempdir");
    std::fs::write(host_dir.path().join("seeded.txt"), b"from the host").unwrap();

    let _ = docker
        .remove_container(
            &name,
            Some(RemoveContainerOptions {
                force: true,
                ..Default::default()
            }),
        )
        .await;

    docker
        .create_container(
            Some(CreateContainerOptions {
                name: name.clone(),
                platform: None,
            }),
            Config {
                image: Some("docker.io/library/busybox:stable".to_string()),
                host_config: Some(HostConfig {
                    mounts: Some(vec![Mount {
                        target: Some("/workspace".to_string()),
                        source: Some(host_dir.path().to_string_lossy().to_string()),
                        typ: Some(MountTypeEnum::BIND),
                        ..Default::default()
                    }]),
                    ..Default::default()
                }),
                ..Default::default()
            },
        )
        .await
        .expect("create container with a bind mount");

    let result = run_against_bind_mount(&docker, exec, &id, host_dir.path()).await;

    let _ = docker
        .remove_container(
            &name,
            Some(RemoveContainerOptions {
                force: true,
                ..Default::default()
            }),
        )
        .await;

    result.expect("file rpc round trip against a bind mount");
}

async fn run_against_bind_mount(
    docker: &bollard::Docker,
    exec: PodmanExec,
    id: &str,
    host_dir: &std::path::Path,
) -> Result<(), String> {
    let (mut client, server) = serve(docker, exec).await?;

    // The pre-existing host file is visible immediately - no create_node or
    // write_file involved, so this exercises `root()` on its own.
    let got = read_all(&mut client, id, "seeded.txt").await;
    if got.as_deref() != Ok(b"from the host".as_slice()) {
        server.abort();
        return Err(format!(
            "read_file seeded.txt = {got:?}, want the seeded content"
        ));
    }

    // Writing through the RPC lands on the real host directory.
    let write_result = client
        .write_file(tokio_stream::iter(vec![
            WriteFileFrame {
                msg: Some(Msg::Init(WriteFileInit {
                    workspace_id: id.to_string(),
                    path: "written.txt".into(),
                })),
            },
            WriteFileFrame {
                msg: Some(Msg::Data(b"from the container".to_vec())),
            },
            WriteFileFrame {
                msg: Some(Msg::End(WriteFileEnd {})),
            },
        ]))
        .await;
    server.abort();
    write_result.map_err(|e| format!("write_file: {e}"))?;

    let on_disk = std::fs::read(host_dir.join("written.txt"))
        .map_err(|e| format!("read back from host disk: {e}"))?;
    if on_disk != b"from the container" {
        return Err(format!(
            "host disk has {on_disk:?}, want b\"from the container\""
        ));
    }
    Ok(())
}

/// Stand the file RPCs up on a loopback port and connect a client to it,
/// exactly as the gateway would. Shared by every test in this file: the
/// `WriteFile` client-streaming RPC can only be driven through a real
/// transport, not called directly on `Files`.
async fn serve(
    docker: &bollard::Docker,
    exec: PodmanExec,
) -> Result<
    (
        WorkspaceIoClient<tonic::transport::Channel>,
        tokio::task::JoinHandle<()>,
    ),
    String,
> {
    let files = Arc::new(Files::new(docker.clone(), READ_CAP));
    let svc = WorkspaceSvc::new(
        Arc::new(exec),
        files,
        hearth_workspace::terminal::TerminalRegistry::new(),
        std::time::Duration::from_secs(60),
    );

    let listener = TcpListener::bind("127.0.0.1:0")
        .await
        .map_err(|e| format!("bind: {e}"))?;
    let addr = listener
        .local_addr()
        .map_err(|e| format!("local addr: {e}"))?;
    let server = tokio::spawn(async move {
        let _ = tonic::transport::Server::builder()
            .add_service(WorkspaceIoServer::new(svc))
            .serve_with_incoming(TcpListenerStream::new(listener))
            .await;
    });

    let channel = tonic::transport::Endpoint::from_shared(format!("http://{addr}"))
        .map_err(|e| format!("endpoint: {e}"))?
        .connect_lazy();
    Ok((WorkspaceIoClient::new(channel), server))
}

async fn run(
    docker: &bollard::Docker,
    exec: PodmanExec,
    id: &str,
    volume: &str,
) -> Result<(), String> {
    let (mut client, server) = serve(docker, exec).await?;
    let outcome = round_trip(&mut client, docker, id, volume).await;
    server.abort();
    outcome
}

async fn round_trip(
    client: &mut WorkspaceIoClient<tonic::transport::Channel>,
    docker: &bollard::Docker,
    id: &str,
    volume: &str,
) -> Result<(), String> {
    // create_node: a directory to hold the file.
    client
        .create_node(CreateNodeRequest {
            workspace_id: id.to_string(),
            path: "dir".into(),
            is_dir: true,
        })
        .await
        .map_err(|e| format!("create_node dir: {e}"))?;

    // write_file: an init frame naming the target, then the bytes in two frames.
    let frames = vec![
        WriteFileFrame {
            msg: Some(Msg::Init(WriteFileInit {
                workspace_id: id.to_string(),
                path: "dir/a.txt".into(),
            })),
        },
        WriteFileFrame {
            msg: Some(Msg::Data(b"hello ".to_vec())),
        },
        WriteFileFrame {
            msg: Some(Msg::Data(b"world".to_vec())),
        },
        WriteFileFrame {
            msg: Some(Msg::End(WriteFileEnd {})),
        },
    ];
    let written = client
        .write_file(tokio_stream::iter(frames))
        .await
        .map_err(|e| format!("write_file: {e}"))?
        .into_inner()
        .bytes_written;
    if written != 11 {
        return Err(format!("write_file reported {written} bytes, expected 11"));
    }

    // read_file: stream it back and reassemble.
    let got = read_all(client, id, "dir/a.txt").await?;
    if got != b"hello world" {
        return Err(format!("read back {got:?}, expected b\"hello world\""));
    }

    // list_dir: one entry, sized to the content.
    let entries = list(client, id, "dir").await?;
    match entries.as_slice() {
        [node] if node.name == "a.txt" && !node.is_dir && node.size == 11 => {}
        other => return Err(format!("list_dir dir = {other:?}, expected a single a.txt")),
    }

    // rename_node: a.txt -> b.txt, and list_dir follows.
    client
        .rename_node(RenameNodeRequest {
            workspace_id: id.to_string(),
            from: "dir/a.txt".into(),
            to: "dir/b.txt".into(),
        })
        .await
        .map_err(|e| format!("rename_node: {e}"))?;
    let entries = list(client, id, "dir").await?;
    match entries.as_slice() {
        [node] if node.name == "b.txt" => {}
        other => return Err(format!("list_dir after rename = {other:?}, expected b.txt")),
    }

    // delete_node: the directory and everything under it.
    client
        .delete_node(DeleteNodeRequest {
            workspace_id: id.to_string(),
            path: "dir".into(),
        })
        .await
        .map_err(|e| format!("delete_node: {e}"))?;
    let entries = list(client, id, "/").await?;
    if !entries.is_empty() {
        return Err(format!("volume root not empty after delete: {entries:?}"));
    }

    // Path safety, live: a `..` in the request is rejected outright.
    let err = client
        .list_dir(ListDirRequest {
            workspace_id: id.to_string(),
            path: "../..".into(),
        })
        .await
        .expect_err("list_dir ../.. must fail");
    if err.code() != tonic::Code::InvalidArgument {
        return Err(format!(
            "list_dir ../.. gave {:?}, want InvalidArgument",
            err.code()
        ));
    }

    // Path safety, live: a symlink out of the volume is refused by the kernel.
    // The test runs as the user that owns the volume, so it can plant one.
    let mountpoint = docker
        .inspect_volume(volume)
        .await
        .map_err(|e| format!("inspect_volume: {e}"))?
        .mountpoint;
    std::os::unix::fs::symlink("/", format!("{mountpoint}/esc"))
        .map_err(|e| format!("plant symlink: {e}"))?;
    let err = client
        .read_file(ReadFileRequest {
            workspace_id: id.to_string(),
            path: "esc/etc/hostname".into(),
        })
        .await
        .expect_err("read_file through a symlink escape must fail");
    if err.code() != tonic::Code::InvalidArgument {
        return Err(format!(
            "symlink escape gave {:?}, want InvalidArgument",
            err.code()
        ));
    }

    Ok(())
}

async fn read_all(
    client: &mut WorkspaceIoClient<tonic::transport::Channel>,
    id: &str,
    path: &str,
) -> Result<Vec<u8>, String> {
    let mut stream = client
        .read_file(ReadFileRequest {
            workspace_id: id.to_string(),
            path: path.to_string(),
        })
        .await
        .map_err(|e| format!("read_file {path}: {e}"))?
        .into_inner();
    let mut out = Vec::new();
    while let Some(chunk) = stream.next().await {
        let chunk = chunk.map_err(|e| format!("read_file chunk: {e}"))?;
        out.extend_from_slice(&chunk.data);
    }
    Ok(out)
}

async fn list(
    client: &mut WorkspaceIoClient<tonic::transport::Channel>,
    id: &str,
    path: &str,
) -> Result<Vec<hearth_proto::hearth::v1::Node>, String> {
    Ok(client
        .list_dir(ListDirRequest {
            workspace_id: id.to_string(),
            path: path.to_string(),
        })
        .await
        .map_err(|e| format!("list_dir {path}: {e}"))?
        .into_inner()
        .entries)
}
