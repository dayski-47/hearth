//! Direct filesystem access to a workspace's volume, confined to that volume
//! by `openat2(RESOLVE_BENEATH)` on every open. `hearth-workspace` runs as the
//! host podman user, so this confinement is the only thing between a caller and
//! that user's whole home directory — it is done by the kernel, not by
//! comparing strings.

use std::collections::HashMap;
use std::io::Read;
use std::os::fd::OwnedFd;
use std::path::{Path, PathBuf};
use std::sync::Mutex;

use bollard::Docker;
use hearth_proto::hearth::v1::{FileChunk, Node};
use rustix::fs::{Mode, OFlags, ResolveFlags};
use tokio::sync::mpsc;
use tokio_stream::wrappers::ReceiverStream;
use tonic::Status;

/// Filesystem operations for one host's workspaces. Cheap to clone the docker
/// handle into; the mount-point cache is shared behind a mutex.
pub struct Files {
    docker: Docker,
    /// workspace id -> the volume's mount point on disk, from `podman volume
    /// inspect`. Filled on first use, never invalidated except on a NotFound.
    roots: Mutex<HashMap<String, PathBuf>>,
    read_cap: u64,
}

// Wired into the gRPC surface in the next task. Every fallible fn here answers
// the caller in `tonic::Status`, which is large enough to trip clippy's
// `result_large_err`; the type is fixed by the gRPC surface, so silence it.
#[allow(dead_code)]
#[allow(clippy::result_large_err)]
impl Files {
    pub fn new(docker: Docker, read_cap_bytes: u64) -> Self {
        Self {
            docker,
            roots: Mutex::new(HashMap::new()),
            read_cap: read_cap_bytes,
        }
    }

    async fn root(&self, workspace_id: &str) -> Result<PathBuf, Status> {
        if let Some(p) = self.roots.lock().unwrap().get(workspace_id).cloned() {
            return Ok(p);
        }
        let name = hearth_common::workspace_container_name(workspace_id);
        let vol = self.docker.inspect_volume(&name).await.map_err(|e| {
            if matches!(&e, bollard::errors::Error::DockerResponseServerError { status_code, .. } if *status_code == 404)
            {
                Status::not_found("workspace volume not found")
            } else {
                Status::internal(format!("inspect volume: {e}"))
            }
        })?;
        let p = PathBuf::from(vol.mountpoint);
        self.roots
            .lock()
            .unwrap()
            .insert(workspace_id.to_string(), p.clone());
        Ok(p)
    }

    pub async fn list_dir(&self, workspace_id: &str, path: &str) -> Result<Vec<Node>, Status> {
        let root = self.root(workspace_id).await?;
        let rel = clean_rel(path)?;
        tokio::task::spawn_blocking(move || list_dir_blocking(&root, &rel))
            .await
            .map_err(|e| Status::internal(format!("join: {e}")))?
    }

    pub async fn read_file(
        &self,
        workspace_id: &str,
        path: &str,
    ) -> Result<ReceiverStream<Result<FileChunk, Status>>, Status> {
        let root = self.root(workspace_id).await?;
        let rel = clean_rel(path)?;
        let cap = self.read_cap;
        let file = tokio::task::spawn_blocking(move || open_file_capped(&root, &rel, cap))
            .await
            .map_err(|e| Status::internal(format!("join: {e}")))??;

        let (tx, rx) = mpsc::channel(4);
        tokio::task::spawn_blocking(move || {
            let mut f = file;
            let mut buf = vec![0u8; 64 * 1024];
            loop {
                match f.read(&mut buf) {
                    Ok(0) => break,
                    Ok(n) => {
                        if tx
                            .blocking_send(Ok(FileChunk {
                                data: buf[..n].to_vec(),
                            }))
                            .is_err()
                        {
                            break;
                        }
                    }
                    Err(e) => {
                        let _ = tx.blocking_send(Err(Status::internal(format!("read: {e}"))));
                        break;
                    }
                }
            }
        });
        Ok(ReceiverStream::new(rx))
    }
}

/// Strip a leading `/`, drop `.` components, and reject any `..` — a fast,
/// friendly `400` before the kernel would reject it anyway. Returns `.` for
/// the volume root.
#[allow(clippy::result_large_err)]
fn clean_rel(path: &str) -> Result<PathBuf, Status> {
    let mut out = PathBuf::new();
    for part in path.trim_start_matches('/').split('/') {
        match part {
            "" | "." => {}
            ".." => return Err(Status::invalid_argument("path may not contain ..")),
            p => out.push(p),
        }
    }
    if out.as_os_str().is_empty() {
        out.push(".");
    }
    Ok(out)
}

/// Open `rel` beneath `root`. `root` is a trusted path from `podman volume
/// inspect`; `rel` is caller-supplied and only ever reached through
/// `RESOLVE_BENEATH`, so `..`, absolute paths, and outward symlinks all fail
/// at the syscall.
#[allow(clippy::result_large_err)]
fn open_beneath(root: &Path, rel: &Path, oflags: OFlags) -> Result<OwnedFd, Status> {
    let root_fd = rustix::fs::open(
        root,
        OFlags::DIRECTORY | OFlags::PATH | OFlags::CLOEXEC,
        Mode::empty(),
    )
    .map_err(|e| Status::internal(format!("open volume root: {e}")))?;

    if rel == Path::new(".") {
        return rustix::fs::openat(&root_fd, ".", oflags | OFlags::CLOEXEC, Mode::empty())
            .map_err(map_open_err);
    }
    rustix::fs::openat2(
        &root_fd,
        rel,
        oflags | OFlags::CLOEXEC,
        Mode::empty(),
        ResolveFlags::BENEATH | ResolveFlags::NO_MAGICLINKS,
    )
    .map_err(map_open_err)
}

fn map_open_err(e: rustix::io::Errno) -> Status {
    use rustix::io::Errno;
    match e {
        Errno::NOENT => Status::not_found("no such file or directory"),
        Errno::XDEV | Errno::LOOP => Status::invalid_argument("path escapes the workspace"),
        Errno::ACCESS | Errno::PERM => Status::permission_denied("permission denied"),
        Errno::ISDIR => Status::invalid_argument("path is a directory"),
        Errno::NOTDIR => Status::invalid_argument("a path component is not a directory"),
        other => Status::internal(format!("open: {other}")),
    }
}

/// Open a regular file for reading, rejecting anything larger than `cap`.
#[allow(clippy::result_large_err)]
fn open_file_capped(root: &Path, rel: &Path, cap: u64) -> Result<std::fs::File, Status> {
    let fd = open_beneath(root, rel, OFlags::RDONLY)?;
    let st = rustix::fs::fstat(&fd).map_err(|e| Status::internal(format!("stat: {e}")))?;
    if (st.st_size as u64) > cap {
        return Err(Status::invalid_argument(format!(
            "file is {} bytes, over the {cap} byte limit",
            st.st_size
        )));
    }
    Ok(std::fs::File::from(fd))
}

#[allow(clippy::result_large_err)]
fn list_dir_blocking(root: &Path, rel: &Path) -> Result<Vec<Node>, Status> {
    let dir_fd = open_beneath(root, rel, OFlags::RDONLY | OFlags::DIRECTORY)?;
    let dir = rustix::fs::Dir::read_from(&dir_fd)
        .map_err(|e| Status::internal(format!("readdir: {e}")))?;

    let mut out = Vec::new();
    for entry in dir {
        let entry = entry.map_err(|e| Status::internal(format!("readdir: {e}")))?;
        let name = entry.file_name().to_string_lossy().into_owned();
        if name == "." || name == ".." {
            continue;
        }
        let st = rustix::fs::statat(
            &dir_fd,
            entry.file_name(),
            rustix::fs::AtFlags::SYMLINK_NOFOLLOW,
        )
        .map_err(|e| Status::internal(format!("stat {name}: {e}")))?;
        let is_dir =
            rustix::fs::FileType::from_raw_mode(st.st_mode) == rustix::fs::FileType::Directory;
        let child = if rel == Path::new(".") {
            PathBuf::from(&name)
        } else {
            rel.join(&name)
        };
        out.push(Node {
            path: child.to_string_lossy().into_owned(),
            name,
            is_dir,
            size: st.st_size as u64,
            modified_unix: st.st_mtime as i64,
        });
    }
    out.sort_by(|a, b| (b.is_dir, &a.name).cmp(&(a.is_dir, &b.name)));
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;

    #[test]
    fn clean_rel_normalises_and_rejects() {
        assert_eq!(clean_rel("/").unwrap(), PathBuf::from("."));
        assert_eq!(clean_rel("").unwrap(), PathBuf::from("."));
        assert_eq!(clean_rel("/a/b.txt").unwrap(), PathBuf::from("a/b.txt"));
        assert_eq!(clean_rel("a/./b").unwrap(), PathBuf::from("a/b"));
        assert!(clean_rel("../etc/passwd").is_err());
        assert!(clean_rel("a/../../b").is_err());
        assert!(clean_rel("a/../b").is_err()); // any `..` component is rejected
    }

    #[test]
    fn open_beneath_blocks_escape() {
        let root = tempfile::tempdir().unwrap();
        std::fs::create_dir(root.path().join("sub")).unwrap();
        std::fs::write(root.path().join("sub/ok.txt"), b"hi").unwrap();

        // a normal nested path opens
        assert!(open_beneath(root.path(), Path::new("sub/ok.txt"), OFlags::RDONLY).is_ok());

        // a path to something that does not exist under the root fails
        assert!(open_beneath(root.path(), Path::new("etc/passwd"), OFlags::RDONLY).is_err());

        // a symlink pointing outside the root is refused by the kernel
        std::os::unix::fs::symlink("/", root.path().join("esc")).unwrap();
        let escaped = open_beneath(root.path(), Path::new("esc/etc/hostname"), OFlags::RDONLY);
        assert!(
            escaped.is_err(),
            "RESOLVE_BENEATH must refuse a symlink escape"
        );
    }

    #[tokio::test]
    async fn read_file_caps_size() {
        let root = tempfile::tempdir().unwrap();
        let mut big = std::fs::File::create(root.path().join("big")).unwrap();
        big.write_all(&vec![0u8; 2048]).unwrap();

        // open_file_capped is the size-checked open; cap of 1024 rejects it
        let err = open_file_capped(root.path(), Path::new("big"), 1024).unwrap_err();
        assert_eq!(err.code(), tonic::Code::InvalidArgument);
        assert!(open_file_capped(root.path(), Path::new("big"), 4096).is_ok());
    }
}
