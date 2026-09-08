//! Direct filesystem access to a workspace's volume, confined to that volume
//! by `openat2(RESOLVE_BENEATH)` on every open. `hearth-workspace` runs as the
//! host podman user, so this confinement is the only thing between a caller and
//! that user's whole home directory — it is done by the kernel, not by
//! comparing strings.

use std::collections::HashMap;
use std::io::Read;
use std::os::fd::OwnedFd;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};

use bollard::Docker;
use futures_util::StreamExt;
use hearth_proto::hearth::v1::write_file_frame::Msg as WriteMsg;
use hearth_proto::hearth::v1::{FileChunk, Node, WriteFileFrame};
use rustix::fs::{AtFlags, Mode, OFlags, ResolveFlags};
use tokio::sync::mpsc;
use tokio_stream::wrappers::ReceiverStream;
use tonic::{Status, Streaming};

/// Filesystem operations for one host's workspaces. Cheap to clone the docker
/// handle into; the mount-point cache is shared behind a mutex.
pub struct Files {
    docker: Docker,
    /// workspace id -> the volume's mount point on disk, from `podman volume
    /// inspect`. Filled on first use and kept for the lifetime of the process;
    /// a volume's mount point does not move while it exists.
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

    /// Stream filesystem changes under the workspace volume, debounced and
    /// mapped to paths relative to the volume root. The stream ends when the
    /// caller drops it.
    pub async fn watch(
        &self,
        workspace_id: &str,
    ) -> Result<ReceiverStream<Result<hearth_proto::hearth::v1::FileEvent, Status>>, Status> {
        let root = self.root(workspace_id).await?;
        crate::watch::start(root)
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
            // The fstat cap was checked before the first read, but the file can
            // grow underneath us; hold the same limit against what we actually send.
            let mut sent: u64 = 0;
            loop {
                match f.read(&mut buf) {
                    Ok(0) => break,
                    Ok(n) => {
                        sent += n as u64;
                        if sent > cap {
                            let _ = tx.blocking_send(Err(Status::invalid_argument(
                                "file exceeded the size limit while reading",
                            )));
                            break;
                        }
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

    /// Consume a `WriteFile` client stream: the first frame carries the target,
    /// every frame after it carries bytes. The bytes land in a temp file in the
    /// destination directory that is renamed over the destination at the end, so
    /// a crash or a dropped connection leaves either the old file or the new one,
    /// never a half-written mix. The temp is committed only after the client
    /// sends the end frame; a stream that ends any other way — an error, an
    /// abort, a dropped connection, or a client that never sends `End` — leaves
    /// the destination untouched. Returns the number of bytes written.
    pub async fn write_file(&self, mut stream: Streaming<WriteFileFrame>) -> Result<u64, Status> {
        let init = match stream.next().await {
            Some(Ok(WriteFileFrame {
                msg: Some(WriteMsg::Init(i)),
            })) => i,
            Some(Ok(_)) => return Err(Status::invalid_argument("first frame must be init")),
            Some(Err(e)) => return Err(Status::internal(format!("client stream: {e}"))),
            None => return Err(Status::invalid_argument("empty write stream")),
        };
        let root = self.root(&init.workspace_id).await?;
        let rel = clean_rel(&init.path)?;
        if rel == Path::new(".") {
            return Err(Status::invalid_argument("cannot write the volume root"));
        }

        let (tx, rx) = mpsc::channel::<Vec<u8>>(8);
        let rp = root.clone();
        let rrel = rel.clone();
        // Starts set: any early exit — a stream error, or the whole handler
        // future being dropped mid-upload — leaves it set and the writer bins
        // the temp. Only a clean end of the stream clears it.
        let aborted = Arc::new(AtomicBool::new(true));
        let writer_abort = Arc::clone(&aborted);
        let writer = tokio::task::spawn_blocking(move || {
            write_atomic_blocking(&rp, &rrel, rx, writer_abort)
        });

        // tonic surfaces a client that just drops its send side as a plain
        // `None`, indistinguishable from a finished upload, so the client has
        // to say so explicitly. Nothing else clears `aborted`.
        let mut saw_end = false;
        while let Some(frame) = stream.next().await {
            match frame {
                Ok(WriteFileFrame {
                    msg: Some(WriteMsg::Data(d)),
                }) => {
                    if tx.send(d).await.is_err() {
                        break; // the writer died; its Result carries the error
                    }
                }
                Ok(WriteFileFrame {
                    msg: Some(WriteMsg::End(_)),
                }) => {
                    saw_end = true;
                    break;
                }
                Ok(_) => {} // a second init or an empty frame: ignore
                Err(e) => {
                    // A broken client stream mid-upload: leave `aborted` set so
                    // the writer bins the temp instead of renaming a half-written
                    // mix.
                    drop(tx);
                    let _ = writer.await;
                    return Err(Status::internal(format!("client stream: {e}")));
                }
            }
        }
        // Commit only if the client sent `End`; any other way out of the loop
        // leaves `aborted` set and the writer bins the temp.
        if saw_end {
            aborted.store(false, Ordering::SeqCst);
        }
        drop(tx);
        writer
            .await
            .map_err(|e| Status::internal(format!("join: {e}")))?
    }

    /// Create an empty file or a directory at `path`. Fails if something is
    /// already there.
    pub async fn create_node(
        &self,
        workspace_id: &str,
        path: &str,
        is_dir: bool,
    ) -> Result<Node, Status> {
        let root = self.root(workspace_id).await?;
        let rel = clean_rel(path)?;
        let r2 = rel.clone();
        tokio::task::spawn_blocking(move || {
            create_node_blocking(&root, &rel, is_dir)?;
            node_at(&root, &r2)
        })
        .await
        .map_err(|e| Status::internal(format!("join: {e}")))?
    }

    /// Remove `path`. A directory is removed with everything under it.
    pub async fn delete_node(&self, workspace_id: &str, path: &str) -> Result<(), Status> {
        let root = self.root(workspace_id).await?;
        let rel = clean_rel(path)?;
        if rel == Path::new(".") {
            return Err(Status::invalid_argument("cannot delete the volume root"));
        }
        tokio::task::spawn_blocking(move || delete_node_blocking(&root, &rel))
            .await
            .map_err(|e| Status::internal(format!("join: {e}")))?
    }

    /// Move `from` to `to`. Both are resolved beneath the volume root.
    pub async fn rename_node(
        &self,
        workspace_id: &str,
        from: &str,
        to: &str,
    ) -> Result<Node, Status> {
        let root = self.root(workspace_id).await?;
        let f = clean_rel(from)?;
        let t = clean_rel(to)?;
        if f == Path::new(".") || t == Path::new(".") {
            return Err(Status::invalid_argument("cannot rename the volume root"));
        }
        let (root2, t2) = (root.clone(), t.clone());
        tokio::task::spawn_blocking(move || {
            rename_node_blocking(&root, &f, &t)?;
            node_at(&root2, &t2)
        })
        .await
        .map_err(|e| Status::internal(format!("join: {e}")))?
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
    if rustix::fs::FileType::from_raw_mode(st.st_mode) == rustix::fs::FileType::Directory {
        return Err(Status::invalid_argument("path is a directory, not a file"));
    }
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

/// Split `rel` into (parent, file name). `rel` is never `.` here.
#[allow(clippy::result_large_err)]
fn split_parent(rel: &Path) -> Result<(PathBuf, &std::ffi::OsStr), Status> {
    let name = rel
        .file_name()
        .ok_or_else(|| Status::invalid_argument("path has no file name"))?;
    let parent = rel.parent().map(Path::to_path_buf).unwrap_or_default();
    let parent = if parent.as_os_str().is_empty() {
        PathBuf::from(".")
    } else {
        parent
    };
    Ok((parent, name))
}

/// Open the directory that will hold `rel`, and hand back the leaf name to
/// operate on within it. The parent is reached through `RESOLVE_BENEATH`, so the
/// leaf operation cannot land outside the volume.
#[allow(clippy::result_large_err)]
fn open_parent(root: &Path, rel: &Path) -> Result<(OwnedFd, PathBuf), Status> {
    let (parent, name) = split_parent(rel)?;
    let fd = open_beneath(root, &parent, OFlags::RDONLY | OFlags::DIRECTORY)?;
    Ok((fd, PathBuf::from(name)))
}

/// Stream `rx` into a temp file next to `rel`, fsync it, and rename it over
/// `rel`. Any failure unlinks the temp and leaves the destination as it was.
#[allow(clippy::result_large_err)]
fn write_atomic_blocking(
    root: &Path,
    rel: &Path,
    mut rx: mpsc::Receiver<Vec<u8>>,
    aborted: Arc<AtomicBool>,
) -> Result<u64, Status> {
    use std::io::Write;
    let (parent_fd, name) = open_parent(root, rel)?;
    let tmp = format!(
        ".hearth-tmp-{}-{}",
        std::process::id(),
        rand::random::<u64>()
    );

    let tmp_fd = rustix::fs::openat(
        &parent_fd,
        tmp.as_str(),
        OFlags::CREATE | OFlags::EXCL | OFlags::WRONLY | OFlags::CLOEXEC,
        Mode::from_raw_mode(0o644),
    )
    .map_err(map_open_err)?;
    let mut file = std::fs::File::from(tmp_fd);

    let cleanup = |err: Status| -> Status {
        let _ = rustix::fs::unlinkat(&parent_fd, tmp.as_str(), AtFlags::empty());
        err
    };

    let mut total: u64 = 0;
    while let Some(chunk) = rx.blocking_recv() {
        if let Err(e) = file.write_all(&chunk) {
            return Err(cleanup(Status::internal(format!("write: {e}"))));
        }
        total += chunk.len() as u64;
    }
    if aborted.load(Ordering::SeqCst) {
        return Err(cleanup(Status::cancelled(
            "write aborted before completion",
        )));
    }
    if let Err(e) = file.sync_all() {
        return Err(cleanup(Status::internal(format!("fsync: {e}"))));
    }
    drop(file);
    rustix::fs::renameat(&parent_fd, tmp.as_str(), &parent_fd, name.as_os_str())
        .map_err(|e| cleanup(Status::internal(format!("rename into place: {e}"))))?;
    Ok(total)
}

#[allow(clippy::result_large_err)]
fn create_node_blocking(root: &Path, rel: &Path, is_dir: bool) -> Result<(), Status> {
    let (parent_fd, name) = open_parent(root, rel)?;
    if is_dir {
        rustix::fs::mkdirat(&parent_fd, name.as_os_str(), Mode::from_raw_mode(0o755))
            .map_err(map_mk_err)
    } else {
        let fd = rustix::fs::openat(
            &parent_fd,
            name.as_os_str(),
            OFlags::CREATE | OFlags::EXCL | OFlags::WRONLY | OFlags::CLOEXEC,
            Mode::from_raw_mode(0o644),
        )
        .map_err(map_mk_err)?;
        drop(fd);
        Ok(())
    }
}

fn map_mk_err(e: rustix::io::Errno) -> Status {
    match e {
        rustix::io::Errno::EXIST => Status::already_exists("already exists"),
        other => map_open_err(other),
    }
}

#[allow(clippy::result_large_err)]
fn delete_node_blocking(root: &Path, rel: &Path) -> Result<(), Status> {
    let (parent_fd, name) = open_parent(root, rel)?;
    let st = rustix::fs::statat(&parent_fd, name.as_os_str(), AtFlags::SYMLINK_NOFOLLOW)
        .map_err(map_open_err)?;
    if rustix::fs::FileType::from_raw_mode(st.st_mode) == rustix::fs::FileType::Directory {
        // recurse, then remove the now-empty directory
        let dir_fd = rustix::fs::openat(
            &parent_fd,
            name.as_os_str(),
            OFlags::RDONLY | OFlags::DIRECTORY | OFlags::CLOEXEC | OFlags::NOFOLLOW,
            Mode::empty(),
        )
        .map_err(map_open_err)?;
        rmdir_recursive(&dir_fd)?;
        rustix::fs::unlinkat(&parent_fd, name.as_os_str(), AtFlags::REMOVEDIR).map_err(map_open_err)
    } else {
        rustix::fs::unlinkat(&parent_fd, name.as_os_str(), AtFlags::empty()).map_err(map_open_err)
    }
}

#[allow(clippy::result_large_err)]
fn rmdir_recursive(dir_fd: &OwnedFd) -> Result<(), Status> {
    // Drain the whole directory stream before touching anything. Unlinking
    // entries while the `getdents` cursor is still open makes the kernel skip
    // over entries on a refill, so a large tree (a real `.git/objects` or
    // `node_modules`) would be left half-populated and the final rmdir would
    // fail with ENOTEMPTY.
    let mut entries: Vec<(std::ffi::CString, bool)> = Vec::new();
    {
        let dir = rustix::fs::Dir::read_from(dir_fd)
            .map_err(|e| Status::internal(format!("readdir: {e}")))?;
        for entry in dir {
            let entry = entry.map_err(|e| Status::internal(format!("readdir: {e}")))?;
            let name = entry.file_name();
            if name.to_bytes() == b"." || name.to_bytes() == b".." {
                continue;
            }
            let st = rustix::fs::statat(dir_fd, name, AtFlags::SYMLINK_NOFOLLOW)
                .map_err(map_open_err)?;
            let is_dir =
                rustix::fs::FileType::from_raw_mode(st.st_mode) == rustix::fs::FileType::Directory;
            entries.push((name.to_owned(), is_dir));
        }
    }

    for (name, is_dir) in entries {
        if is_dir {
            let child = rustix::fs::openat(
                dir_fd,
                name.as_c_str(),
                OFlags::RDONLY | OFlags::DIRECTORY | OFlags::CLOEXEC | OFlags::NOFOLLOW,
                Mode::empty(),
            )
            .map_err(map_open_err)?;
            rmdir_recursive(&child)?;
            rustix::fs::unlinkat(dir_fd, name.as_c_str(), AtFlags::REMOVEDIR)
                .map_err(map_open_err)?;
        } else {
            rustix::fs::unlinkat(dir_fd, name.as_c_str(), AtFlags::empty())
                .map_err(map_open_err)?;
        }
    }
    Ok(())
}

#[allow(clippy::result_large_err)]
fn rename_node_blocking(root: &Path, from: &Path, to: &Path) -> Result<(), Status> {
    let (from_parent, from_name) = open_parent(root, from)?;
    let (to_parent, to_name) = open_parent(root, to)?;
    rustix::fs::renameat(
        &from_parent,
        from_name.as_os_str(),
        &to_parent,
        to_name.as_os_str(),
    )
    .map_err(map_open_err)
}

#[allow(clippy::result_large_err)]
fn node_at(root: &Path, rel: &Path) -> Result<Node, Status> {
    let (parent_fd, name) = open_parent(root, rel)?;
    let st = rustix::fs::statat(&parent_fd, name.as_os_str(), AtFlags::SYMLINK_NOFOLLOW)
        .map_err(map_open_err)?;
    Ok(Node {
        path: rel.to_string_lossy().into_owned(),
        name: name.to_string_lossy().into_owned(),
        is_dir: rustix::fs::FileType::from_raw_mode(st.st_mode) == rustix::fs::FileType::Directory,
        size: st.st_size as u64,
        modified_unix: st.st_mtime as i64,
    })
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

    #[test]
    fn open_file_capped_rejects_a_directory() {
        let root = tempfile::tempdir().unwrap();
        let err = open_file_capped(root.path(), Path::new("."), 4096).unwrap_err();
        assert_eq!(err.code(), tonic::Code::InvalidArgument);
    }

    #[tokio::test]
    #[allow(clippy::result_large_err)]
    async fn write_replaces_atomically() {
        let root = tempfile::tempdir().unwrap();
        std::fs::write(root.path().join("f"), b"original").unwrap();

        // A clean run replaces "f" in place and leaves no temp file behind.
        let rp = root.path().to_path_buf();
        let (tx, rx) = mpsc::channel::<Vec<u8>>(2);
        let aborted = Arc::new(AtomicBool::new(false));
        let h = tokio::task::spawn_blocking(move || {
            write_atomic_blocking(&rp, Path::new("f"), rx, aborted)
        });
        tx.send(b"new".to_vec()).await.unwrap();
        drop(tx); // clean end -> this one SUCCEEDS; assert it replaced the file
        let n = h.await.unwrap().unwrap();
        assert_eq!(n, 3);
        assert_eq!(std::fs::read(root.path().join("f")).unwrap(), b"new");
        // no leftover temp files
        let leftovers: Vec<_> = std::fs::read_dir(root.path())
            .unwrap()
            .filter_map(|e| e.ok())
            .filter(|e| e.file_name().to_string_lossy().starts_with(".hearth-tmp-"))
            .collect();
        assert!(leftovers.is_empty(), "temp file left behind");
    }

    #[tokio::test]
    #[allow(clippy::result_large_err)]
    async fn write_aborted_leaves_the_original() {
        let root = tempfile::tempdir().unwrap();
        std::fs::write(root.path().join("f"), b"original").unwrap();

        // The flag is already set when the writer's channel closes: it must bin
        // the temp file rather than rename it over "f".
        let rp = root.path().to_path_buf();
        let (tx, rx) = mpsc::channel::<Vec<u8>>(2);
        let aborted = Arc::new(AtomicBool::new(false));
        let flag = Arc::clone(&aborted);
        let h = tokio::task::spawn_blocking(move || {
            write_atomic_blocking(&rp, Path::new("f"), rx, flag)
        });
        tx.send(b"partial".to_vec()).await.unwrap();
        aborted.store(true, Ordering::SeqCst);
        drop(tx);

        let err = h.await.unwrap().unwrap_err();
        assert_eq!(err.code(), tonic::Code::Cancelled);
        assert_eq!(std::fs::read(root.path().join("f")).unwrap(), b"original");
        let leftovers: Vec<_> = std::fs::read_dir(root.path())
            .unwrap()
            .filter_map(|e| e.ok())
            .filter(|e| e.file_name().to_string_lossy().starts_with(".hearth-tmp-"))
            .collect();
        assert!(leftovers.is_empty(), "temp file left behind");
    }

    #[test]
    fn node_ops_stay_beneath_root() {
        let root = tempfile::tempdir().unwrap();
        assert!(create_node_blocking(root.path(), Path::new("d"), true).is_ok());
        assert!(create_node_blocking(root.path(), Path::new("d/x.txt"), false).is_ok());
        assert!(create_node_blocking(root.path(), Path::new("d/x.txt"), false).is_err()); // EEXIST
        assert!(
            rename_node_blocking(root.path(), Path::new("d/x.txt"), Path::new("d/y.txt")).is_ok()
        );
        assert!(delete_node_blocking(root.path(), Path::new("d")).is_ok()); // recursive
        assert!(!root.path().join("d").exists());
    }

    #[test]
    fn delete_node_recurses_a_large_directory() {
        let root = tempfile::tempdir().unwrap();
        create_node_blocking(root.path(), Path::new("d"), true).unwrap();

        // Enough entries to force the readdir buffer to refill mid-walk, which
        // is where deleting while the stream is live used to drop entries.
        let d = root.path().join("d");
        for i in 0..5000 {
            std::fs::write(d.join(format!("f{i}")), b"x").unwrap();
        }

        assert!(delete_node_blocking(root.path(), Path::new("d")).is_ok());
        assert!(!root.path().join("d").exists());
    }

    #[test]
    fn node_ops_reject_escape() {
        let root = tempfile::tempdir().unwrap();
        std::fs::write(root.path().join("a"), b"x").unwrap();
        std::os::unix::fs::symlink("/", root.path().join("esc")).unwrap();

        // Every write op reaches its parent through open_beneath, so a symlink
        // that points out of the root is refused by the kernel.
        assert!(create_node_blocking(root.path(), Path::new("esc/tmp/x"), false).is_err());
        assert!(rename_node_blocking(root.path(), Path::new("a"), Path::new("esc/tmp/x")).is_err());
        assert!(delete_node_blocking(root.path(), Path::new("esc/tmp")).is_err());
    }
}
