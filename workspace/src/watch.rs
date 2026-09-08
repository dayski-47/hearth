//! A debounced recursive `notify` watch on a workspace volume, mapped to
//! `FileEvent`s relative to the volume root. The stream never blocks the
//! watcher: when the channel to the gateway fills, events are dropped and a
//! single resync sentinel is sent instead.

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Duration;

use hearth_proto::hearth::v1::{file_event::Kind, FileEvent};
use notify::{EventKind, RecursiveMode, Watcher};
use notify_debouncer_full::{new_debouncer, DebounceEventResult, DebouncedEvent};
use tokio::sync::mpsc;
use tokio_stream::wrappers::ReceiverStream;
use tonic::Status;

const IGNORE: &[&str] = &[".git", "node_modules", "target", ".venv"];
const CHANNEL_CAP: usize = 256;
const DEBOUNCE: Duration = Duration::from_millis(150);

fn to_file_event(root: &Path, ev: &DebouncedEvent) -> Option<FileEvent> {
    let kind = match ev.kind {
        EventKind::Create(_) => Kind::Created,
        EventKind::Modify(_) => Kind::Modified,
        EventKind::Remove(_) => Kind::Removed,
        _ => return None,
    };
    let raw = ev.paths.first()?;
    let rel = raw.strip_prefix(root).ok()?;
    // Drop anything living inside an ignored directory at any depth: a nested
    // `packages/app/node_modules` counts, but `.gitignore` (a plain file name,
    // not the `.git` directory) does not.
    if rel.components().any(|c| {
        matches!(c, std::path::Component::Normal(name)
            if IGNORE.iter().any(|d| name == std::ffi::OsStr::new(d)))
    }) {
        return None;
    }
    let rel = rel.to_string_lossy();
    if rel.is_empty() {
        return None;
    }
    Some(FileEvent {
        path: rel.into_owned(),
        kind: kind as i32,
    })
}

#[allow(clippy::result_large_err)]
pub fn start(root: PathBuf) -> Result<ReceiverStream<Result<FileEvent, Status>>, Status> {
    let (tx, rx) = mpsc::channel::<Result<FileEvent, Status>>(CHANNEL_CAP);
    let behind = Arc::new(AtomicBool::new(false));

    let root_for_cb = root.clone();
    let tx_for_cb = tx.clone();
    let behind_cb = behind.clone();
    let mut debouncer = new_debouncer(DEBOUNCE, None, move |res: DebounceEventResult| {
        let events = match res {
            Ok(evs) => evs,
            Err(_) => return,
        };
        for ev in &events {
            let Some(fe) = to_file_event(&root_for_cb, ev) else {
                continue;
            };
            match tx_for_cb.try_send(Ok(fe)) {
                Ok(()) => {}
                Err(mpsc::error::TrySendError::Full(_)) => {
                    behind_cb.store(true, Ordering::Relaxed);
                }
                Err(mpsc::error::TrySendError::Closed(_)) => return,
            }
        }
        // Only clear `behind` once the sentinel has genuinely gone out. The
        // channel is usually still full at this point, so retry on every later
        // callback until the client actually receives the resync signal.
        if behind_cb.load(Ordering::Relaxed)
            && tx_for_cb
                .try_send(Ok(FileEvent {
                    path: String::new(),
                    kind: Kind::Unspecified as i32,
                }))
                .is_ok()
        {
            behind_cb.store(false, Ordering::Relaxed);
        }
    })
    .map_err(|e| Status::internal(format!("start watcher: {e}")))?;

    debouncer
        .watcher()
        .watch(&root, RecursiveMode::Recursive)
        .map_err(|e| Status::internal(format!("watch {}: {e}", root.display())))?;

    // The debouncer owns the watcher thread; hold it until the gateway hangs up.
    tokio::spawn(async move {
        tx.closed().await;
        drop(debouncer);
    });

    Ok(ReceiverStream::new(rx))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn maps_and_filters() {
        let root = Path::new("/vol");
        let mk = |kind: EventKind, path: &str| DebouncedEvent {
            event: notify::Event {
                kind,
                paths: vec![PathBuf::from(path)],
                attrs: Default::default(),
            },
            time: std::time::Instant::now(),
        };

        let created = to_file_event(
            root,
            &mk(
                EventKind::Create(notify::event::CreateKind::File),
                "/vol/a/b.txt",
            ),
        );
        assert_eq!(
            created,
            Some(FileEvent {
                path: "a/b.txt".into(),
                kind: Kind::Created as i32,
            })
        );

        // a modify keeps its kind
        assert_eq!(
            to_file_event(
                root,
                &mk(
                    EventKind::Modify(notify::event::ModifyKind::Any),
                    "/vol/a/b.txt",
                ),
            ),
            Some(FileEvent {
                path: "a/b.txt".into(),
                kind: Kind::Modified as i32,
            })
        );
        // so does a remove
        assert_eq!(
            to_file_event(
                root,
                &mk(
                    EventKind::Remove(notify::event::RemoveKind::File),
                    "/vol/a/b.txt",
                ),
            ),
            Some(FileEvent {
                path: "a/b.txt".into(),
                kind: Kind::Removed as i32,
            })
        );

        // ignored directory -> dropped
        assert_eq!(
            to_file_event(
                root,
                &mk(
                    EventKind::Modify(notify::event::ModifyKind::Any),
                    "/vol/.git/HEAD",
                ),
            ),
            None
        );
        // a nested ignored directory -> dropped too
        assert_eq!(
            to_file_event(
                root,
                &mk(
                    EventKind::Create(notify::event::CreateKind::File),
                    "/vol/packages/app/node_modules/x",
                ),
            ),
            None
        );
        // `.gitignore` is a file, not the `.git` directory -> kept
        assert_eq!(
            to_file_event(
                root,
                &mk(
                    EventKind::Modify(notify::event::ModifyKind::Any),
                    "/vol/.gitignore",
                ),
            ),
            Some(FileEvent {
                path: ".gitignore".into(),
                kind: Kind::Modified as i32,
            })
        );
        // a path outside the root -> dropped
        assert_eq!(
            to_file_event(
                root,
                &mk(
                    EventKind::Create(notify::event::CreateKind::File),
                    "/etc/passwd",
                ),
            ),
            None
        );
        // an access event -> dropped
        assert_eq!(
            to_file_event(
                root,
                &mk(EventKind::Access(notify::event::AccessKind::Read), "/vol/a",),
            ),
            None
        );
    }
}
