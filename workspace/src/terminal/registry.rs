//! The terminal session registry: one held session per workspace, so a
//! browser reconnect re-attaches to the same shell instead of starting a
//! new one.
//!
//! Every lifecycle transition for a workspace - create, re-attach, detach,
//! reap - runs under that workspace's `SessionCell` slot lock, so those
//! paths never race each other. The pump and the per-client forwarding loop
//! work off channels and `Arc`s handed out under that lock and never take
//! it themselves (except the pump's final reap).

use std::collections::HashMap;
use std::pin::Pin;
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::Arc;
use std::time::Duration;

use futures_util::{Stream, StreamExt};
use tokio::io::{AsyncWrite, AsyncWriteExt};
use tokio::sync::mpsc;
use tokio::sync::Mutex;
use tokio::time::timeout;

use super::ring::Ring;

/// What a session needs from the container engine while it is held.
/// `PodmanExec` implements this in production; tests use a fake. Reaping a
/// dead session is done by dropping its `TerminalHandle` (Podman has no
/// "kill exec"), not through this trait.
#[tonic::async_trait]
pub trait ExecControl: Send + Sync + 'static {
    async fn resize(&self, id: &str, cols: u16, rows: u16);
}

/// The PTY end of a freshly started shell, handed to the registry by the
/// caller's `start` closure. The registry owns these until the session is
/// reaped; dropping them is what sends the shell EOF.
pub struct StartedPty {
    pub exec_id: String,
    pub output: Pin<Box<dyn Stream<Item = anyhow::Result<bytes::Bytes>> + Send>>,
    pub input: Pin<Box<dyn AsyncWrite + Send>>,
}

/// The result of attaching a client to a session, held or fresh. Everything
/// the handler's two client tasks need is lifted out here under the slot
/// lock, so the handler never re-locks the session.
pub struct Attached {
    /// True when this attach re-joined a held session and `replay` carries
    /// its scrollback.
    pub resumed: bool,
    /// Ring snapshot to write to the client before live output; empty when
    /// the session is fresh.
    pub replay: Vec<u8>,
    /// Live PTY output for this client. Closes when the shell exits or a
    /// newer client takes the session.
    pub frames_rx: mpsc::Receiver<Vec<u8>>,
    /// Set true the moment a newer attach supersedes this client. The
    /// forwarding loop reads it to tell "booted" (quiet) from "shell exited"
    /// (sends `Exit`) when its channel closes.
    pub booted: Arc<AtomicBool>,
    /// The PTY stdin writer, shared with the session so a reconnect reuses it.
    pub input: Arc<Mutex<Pin<Box<dyn AsyncWrite + Send>>>>,
    pub exec_id: String,
    /// This session's identity. Stale reap timers and detach calls compare
    /// it against the current entry and no-op on a mismatch.
    pub epoch: u64,
}

/// The currently attached client's live channel plus its boot flag.
pub struct AttachedClient {
    frames_tx: mpsc::Sender<Vec<u8>>,
    booted: Arc<AtomicBool>,
}

/// A shell held open across browser reconnects.
pub struct TerminalSession {
    pub exec_id: String,
    /// Scrollback replayed on re-attach. The pump appends every chunk.
    pub ring: Arc<Mutex<Ring>>,
    pub size: (u16, u16),
    /// Set while no client is attached; aborting it cancels the reap. A
    /// reconnect clears it (`take`) under the slot lock, which is exactly
    /// what the reap timer re-checks before acting.
    pub grace: Option<tokio::task::JoinHandle<()>>,
    /// The forever-running PTY -> ring copy. Lives as long as the session,
    /// not the client; aborted only when the session is reaped.
    pub pump: tokio::task::JoinHandle<()>,
    /// The PTY stdin writer. Held by the session so a re-attach reuses it.
    pub input: Arc<Mutex<Pin<Box<dyn AsyncWrite + Send>>>>,
    /// The attached client, or `None` while detached. The pump takes this
    /// lock to forward a chunk; `attach` takes it to swap the client;
    /// `detach` takes it to clear it.
    pub client: Arc<Mutex<Option<AttachedClient>>>,
}

/// One permanent cell per workspace. `slot` holds the live session or
/// `None`; taking `slot` serialises every lifecycle transition.
struct SessionCell {
    slot: Mutex<Option<Live>>,
}

struct Live {
    epoch: u64,
    session: Arc<Mutex<TerminalSession>>,
}

pub struct TerminalRegistry {
    cells: std::sync::Mutex<HashMap<String, Arc<SessionCell>>>,
    next_epoch: AtomicU64,
}

/// Copy PTY output into the ring forever, and into the attached client's
/// channel when there is one. Ends only when the shell's output stream
/// does; on that end it clears the client slot (so a still-attached client
/// reports the exit) and reaps the registry entry.
async fn run_pump(
    mut output: Pin<Box<dyn Stream<Item = anyhow::Result<bytes::Bytes>> + Send>>,
    ring: Arc<Mutex<Ring>>,
    client: Arc<Mutex<Option<AttachedClient>>>,
    reg: Arc<TerminalRegistry>,
    workspace_id: String,
    epoch: u64,
) {
    loop {
        match output.next().await {
            Some(Ok(bytes)) => {
                let chunk = bytes.to_vec();
                // Ring the chunk and read the current sender under one client
                // lock, the same lock `attach` holds across its snapshot and
                // swap, so a chunk is never both replayed and delivered live.
                let sender = {
                    let guard = client.lock().await;
                    ring.lock().await.push(&chunk);
                    guard.as_ref().map(|c| c.frames_tx.clone())
                };
                if let Some(tx) = sender {
                    let _ = tx.send(chunk).await;
                }
            }
            Some(Err(e)) => {
                tracing::warn!(%workspace_id, error = %e, "terminal pump stopped on exec output error");
                break;
            }
            None => break,
        }
    }
    // The shell exited or the container stopped.
    *client.lock().await = None;
    reg.reap(&workspace_id, epoch).await;
}

impl TerminalRegistry {
    pub fn new() -> Arc<Self> {
        Arc::new(Self {
            cells: std::sync::Mutex::new(HashMap::new()),
            next_epoch: AtomicU64::new(1),
        })
    }

    fn cell(&self, workspace_id: &str) -> Arc<SessionCell> {
        self.cells
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .entry(workspace_id.to_string())
            .or_insert_with(|| {
                Arc::new(SessionCell {
                    slot: Mutex::new(None),
                })
            })
            .clone()
    }

    /// Attach a client. Serialised per workspace by the slot lock: a held
    /// session takes the re-attach path (cancel grace, boot the current
    /// client, replay the ring); an empty slot runs `start()` while still
    /// holding the lock, so a second caller waits and then re-attaches
    /// rather than starting a second shell.
    pub async fn attach<F, Fut>(
        self: &Arc<Self>,
        workspace_id: &str,
        cols: u16,
        rows: u16,
        exec: Arc<dyn ExecControl>,
        start: F,
    ) -> anyhow::Result<Attached>
    where
        F: FnOnce() -> Fut,
        Fut: std::future::Future<Output = anyhow::Result<StartedPty>>,
    {
        let cell = self.cell(workspace_id);
        let mut slot = cell.slot.lock().await;

        if let Some(live) = slot.as_ref() {
            let session = live.session.clone();
            let epoch = live.epoch;
            let (tx, rx) = mpsc::channel::<Vec<u8>>(256);
            let booted = Arc::new(AtomicBool::new(false));

            let (exec_id, input, replay) = {
                let mut s = session.lock().await;
                if let Some(g) = s.grace.take() {
                    g.abort();
                }
                let mut client_guard = s.client.lock().await;
                // Boot the outgoing client: set its flag before its channel
                // closes, so its forwarding loop reads "booted", not "exited".
                if let Some(prev) = client_guard.as_ref() {
                    prev.booted.store(true, Ordering::SeqCst);
                }
                // Snapshot and swap under the same client lock the pump takes.
                let replay = s.ring.lock().await.snapshot();
                *client_guard = Some(AttachedClient {
                    frames_tx: tx,
                    booted: booted.clone(),
                });
                drop(client_guard);
                s.size = (cols, rows);
                (s.exec_id.clone(), s.input.clone(), replay)
            };
            exec.resize(&exec_id, cols, rows).await;
            return Ok(Attached {
                resumed: true,
                replay,
                frames_rx: rx,
                booted,
                input,
                exec_id,
                epoch,
            });
        }

        let started = start().await?;
        let epoch = self.next_epoch.fetch_add(1, Ordering::Relaxed);
        let ring = Arc::new(Mutex::new(Ring::new()));
        let (tx, rx) = mpsc::channel::<Vec<u8>>(256);
        let booted = Arc::new(AtomicBool::new(false));
        let client: Arc<Mutex<Option<AttachedClient>>> =
            Arc::new(Mutex::new(Some(AttachedClient {
                frames_tx: tx,
                booted: booted.clone(),
            })));
        let input = Arc::new(Mutex::new(started.input));

        let pump = tokio::spawn(run_pump(
            started.output,
            ring.clone(),
            client.clone(),
            self.clone(),
            workspace_id.to_string(),
            epoch,
        ));

        let session = Arc::new(Mutex::new(TerminalSession {
            exec_id: started.exec_id.clone(),
            ring,
            size: (cols, rows),
            grace: None,
            pump,
            input: input.clone(),
            client,
        }));
        *slot = Some(Live { epoch, session });

        Ok(Attached {
            resumed: false,
            replay: Vec::new(),
            frames_rx: rx,
            booted,
            input,
            exec_id: started.exec_id,
            epoch,
        })
    }

    /// The client's response stream closed. If this client was already
    /// superseded (`booted`) or the entry has moved on, do nothing;
    /// otherwise clear the client slot and arm the reap timer.
    pub async fn detach(
        self: &Arc<Self>,
        workspace_id: &str,
        epoch: u64,
        booted: Arc<AtomicBool>,
        grace: Duration,
    ) {
        let cell = self.cell(workspace_id);
        let slot = cell.slot.lock().await;
        // Under the slot lock a concurrent re-attach has fully run, so this
        // flag is now exact.
        if booted.load(Ordering::SeqCst) {
            return;
        }
        let Some(live) = slot.as_ref() else {
            return;
        };
        if live.epoch != epoch {
            return;
        }
        let session = live.session.clone();
        let mut s = session.lock().await;
        *s.client.lock().await = None;
        if s.grace.is_some() {
            return;
        }
        let reg = self.clone();
        let id = workspace_id.to_string();
        s.grace = Some(tokio::spawn(async move {
            tokio::time::sleep(grace).await;
            reg.reap_after_grace(&id, epoch).await;
        }));
    }

    /// Reap after the pump ended (shell exited): drop the entry if it is
    /// still ours, cancel any lingering grace timer, and close the writer.
    async fn reap(&self, workspace_id: &str, epoch: u64) {
        let cell = self.cell(workspace_id);
        let mut slot = cell.slot.lock().await;
        let Some(live) = slot.as_ref() else {
            return;
        };
        if live.epoch != epoch {
            return;
        }
        let session = live.session.clone();
        let writer = {
            let mut s = session.lock().await;
            if let Some(g) = s.grace.take() {
                g.abort();
            }
            s.input.clone()
        };
        // The input task can hold this writer lock across a wedged `write_all`
        // (a shell that stopped reading stdin plus a large pending paste).
        // Dropping the session below is what actually ends the exec; the
        // `shutdown()` is best-effort, so bound the wait and proceed either
        // way rather than blocking the slot lock, and every future attach for
        // this workspace, forever.
        if let Ok(mut w) = timeout(Duration::from_secs(2), writer.lock()).await {
            let _ = w.shutdown().await;
        }
        *slot = None;
    }

    /// Reap after the grace window elapsed. Bails if a reconnect cleared
    /// `grace` under the slot lock, or if the entry has moved on.
    async fn reap_after_grace(&self, workspace_id: &str, epoch: u64) {
        let cell = self.cell(workspace_id);
        let mut slot = cell.slot.lock().await;
        let Some(live) = slot.as_ref() else {
            return;
        };
        if live.epoch != epoch {
            return;
        }
        let session = live.session.clone();
        let writer = {
            let mut s = session.lock().await;
            if s.grace.is_none() {
                return;
            }
            s.grace = None;
            s.pump.abort();
            s.input.clone()
        };
        // See `reap`: the input task can wedge holding this lock, so bound the
        // wait. Aborting the pump and dropping the session is what ends the
        // exec; `shutdown()` is best-effort cleanup.
        if let Ok(mut w) = timeout(Duration::from_secs(2), writer.lock()).await {
            let _ = w.shutdown().await;
        }
        *slot = None;
    }

    #[cfg(test)]
    pub async fn count(&self) -> usize {
        let cells: Vec<_> = self
            .cells
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .values()
            .cloned()
            .collect();
        let mut n = 0;
        for cell in cells {
            if cell.slot.lock().await.is_some() {
                n += 1;
            }
        }
        n
    }

    #[cfg(test)]
    async fn live_session(&self, workspace_id: &str) -> Option<Arc<Mutex<TerminalSession>>> {
        let cell = self.cell(workspace_id);
        let slot = cell.slot.lock().await;
        slot.as_ref().map(|l| l.session.clone())
    }
}

#[cfg(test)]
pub(crate) mod fake {
    use std::pin::Pin;
    use std::sync::atomic::{AtomicBool, Ordering};
    use std::sync::Arc;

    use tokio::io::AsyncWrite;
    use tokio::sync::mpsc;

    use super::StartedPty;

    pub struct FakeExec {
        pub resized: AtomicBool,
        pub last_size: std::sync::Mutex<(u16, u16)>,
    }

    impl FakeExec {
        pub fn new() -> Arc<Self> {
            Arc::new(Self {
                resized: AtomicBool::new(false),
                last_size: std::sync::Mutex::new((0, 0)),
            })
        }
    }

    #[tonic::async_trait]
    impl super::ExecControl for FakeExec {
        async fn resize(&self, _id: &str, cols: u16, rows: u16) {
            self.resized.store(true, Ordering::SeqCst);
            *self.last_size.lock().unwrap() = (cols, rows);
        }
    }

    fn sink() -> Pin<Box<dyn AsyncWrite + Send>> {
        Box::pin(tokio::io::sink())
    }

    /// A channel-backed fake PTY: the returned sender feeds output bytes,
    /// and dropping it (or sending an `Err`) ends the stream.
    pub fn pty() -> (StartedPty, mpsc::Sender<anyhow::Result<bytes::Bytes>>) {
        let (feed, rx) = mpsc::channel::<anyhow::Result<bytes::Bytes>>(64);
        (
            StartedPty {
                exec_id: "e1".into(),
                output: Box::pin(tokio_stream::wrappers::ReceiverStream::new(rx)),
                input: sink(),
            },
            feed,
        )
    }

    /// A fake PTY whose output never yields and never ends: the pump parks
    /// on it, so the session stays live until something aborts the pump.
    pub fn pty_idle() -> StartedPty {
        StartedPty {
            exec_id: "e1".into(),
            output: Box::pin(futures_util::stream::pending()),
            input: sink(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicUsize, Ordering};
    use std::time::Duration;

    async fn ring_len(session: &Arc<Mutex<TerminalSession>>) -> usize {
        let ring = { session.lock().await.ring.clone() };
        let snap = ring.lock().await.snapshot();
        snap.len()
    }

    async fn wait_for_ring(session: &Arc<Mutex<TerminalSession>>, want: usize) {
        for _ in 0..2000 {
            if ring_len(session).await >= want {
                return;
            }
            tokio::time::sleep(Duration::from_millis(1)).await;
        }
        panic!("ring never reached {want} bytes");
    }

    #[tokio::test]
    async fn attach_on_an_empty_registry_starts_fresh() {
        let reg = TerminalRegistry::new();
        let exec = fake::FakeExec::new();

        let a = reg
            .attach(
                "w1",
                80,
                24,
                exec,
                move || async move { Ok(fake::pty_idle()) },
            )
            .await
            .unwrap();

        assert!(!a.resumed);
        assert!(a.replay.is_empty());
        assert_eq!(a.epoch, 1);
        assert_eq!(reg.count().await, 1);
    }

    #[tokio::test]
    async fn detach_arms_grace_and_keeps_the_session() {
        let reg = TerminalRegistry::new();
        let exec = fake::FakeExec::new();

        let a = reg
            .attach(
                "w1",
                80,
                24,
                exec,
                move || async move { Ok(fake::pty_idle()) },
            )
            .await
            .unwrap();

        reg.detach("w1", a.epoch, a.booted.clone(), Duration::from_secs(60))
            .await;

        assert_eq!(reg.count().await, 1);
        let session = reg.live_session("w1").await.unwrap();
        assert!(session.lock().await.grace.is_some());
    }

    #[tokio::test]
    async fn reattach_within_grace_resumes_and_replays_the_ring() {
        let reg = TerminalRegistry::new();
        let exec = fake::FakeExec::new();
        let (pty, feed) = fake::pty();

        let a = reg
            .attach("w1", 80, 24, exec.clone(), move || async move { Ok(pty) })
            .await
            .unwrap();
        reg.detach("w1", a.epoch, a.booted.clone(), Duration::from_secs(60))
            .await;

        feed.send(Ok(bytes::Bytes::from_static(b"while-away")))
            .await
            .unwrap();
        let session = reg.live_session("w1").await.unwrap();
        wait_for_ring(&session, b"while-away".len()).await;

        let a2 = reg
            .attach("w1", 100, 40, exec.clone(), || async {
                panic!("re-attach must not start a new pty")
            })
            .await
            .unwrap();

        assert!(a2.resumed);
        assert_eq!(a2.replay, b"while-away");
        assert_eq!(a2.epoch, a.epoch);
        assert!(reg
            .live_session("w1")
            .await
            .unwrap()
            .lock()
            .await
            .grace
            .is_none());
        assert_eq!(reg.count().await, 1);
        assert_eq!(*exec.last_size.lock().unwrap(), (100, 40));

        drop(feed);
    }

    #[tokio::test(start_paused = true)]
    async fn grace_expiry_reaps_the_session_and_aborts_the_pump() {
        let reg = TerminalRegistry::new();
        let exec = fake::FakeExec::new();

        let a = reg
            .attach(
                "w1",
                80,
                24,
                exec,
                move || async move { Ok(fake::pty_idle()) },
            )
            .await
            .unwrap();
        let session = reg.live_session("w1").await.unwrap();
        reg.detach("w1", a.epoch, a.booted.clone(), Duration::from_secs(60))
            .await;
        for _ in 0..10 {
            tokio::task::yield_now().await;
        }
        assert_eq!(reg.count().await, 1);

        tokio::time::advance(Duration::from_secs(61)).await;
        for _ in 0..100 {
            if reg.count().await == 0 {
                break;
            }
            tokio::task::yield_now().await;
        }
        assert_eq!(reg.count().await, 0);

        for _ in 0..100 {
            if session.lock().await.pump.is_finished() {
                break;
            }
            tokio::task::yield_now().await;
        }
        assert!(session.lock().await.pump.is_finished());
    }

    #[tokio::test(start_paused = true)]
    async fn grace_reap_gives_up_on_a_wedged_writer_lock() {
        let reg = TerminalRegistry::new();
        let exec = fake::FakeExec::new();

        let a = reg
            .attach(
                "w1",
                80,
                24,
                exec,
                move || async move { Ok(fake::pty_idle()) },
            )
            .await
            .unwrap();

        // Simulate the input task wedged inside `write_all`: hold the writer
        // lock and never release it.
        let writer = {
            reg.live_session("w1")
                .await
                .unwrap()
                .lock()
                .await
                .input
                .clone()
        };
        let _wedged = writer.lock().await;

        reg.detach("w1", a.epoch, a.booted.clone(), Duration::from_secs(60))
            .await;
        // Let the spawned grace task run once so its sleep timer is registered
        // before time is advanced.
        for _ in 0..10 {
            tokio::task::yield_now().await;
        }

        // Fire the grace timer; the reap task then parks on the wedged writer
        // lock while holding the slot lock.
        tokio::time::advance(Duration::from_secs(61)).await;
        for _ in 0..50 {
            tokio::task::yield_now().await;
        }
        // The reap has now waited ~2s on the wedged lock and given up, dropping
        // the entry even though `shutdown()` never ran.
        tokio::time::advance(Duration::from_secs(3)).await;
        for _ in 0..50 {
            tokio::task::yield_now().await;
        }
        assert_eq!(
            reg.count().await,
            0,
            "a wedged writer must not block the reap"
        );
        drop(_wedged);
    }

    #[tokio::test]
    async fn a_second_attach_sets_the_first_clients_booted_flag() {
        let reg = TerminalRegistry::new();
        let exec = fake::FakeExec::new();

        let a1 = reg
            .attach("w1", 80, 24, exec.clone(), move || async move {
                Ok(fake::pty_idle())
            })
            .await
            .unwrap();
        assert!(!a1.booted.load(Ordering::SeqCst));

        let a2 = reg
            .attach("w1", 80, 24, exec.clone(), || async {
                panic!("second attach must not start a new pty")
            })
            .await
            .unwrap();

        assert!(
            a1.booted.load(Ordering::SeqCst),
            "the first client is booted"
        );
        assert!(!a2.booted.load(Ordering::SeqCst));
        assert!(a2.resumed);
        assert_eq!(reg.count().await, 1);
    }

    #[tokio::test]
    async fn pump_end_reports_the_exit_and_reaps() {
        let reg = TerminalRegistry::new();
        let exec = fake::FakeExec::new();
        let (pty, feed) = fake::pty();

        let a = reg
            .attach("w1", 80, 24, exec, move || async move { Ok(pty) })
            .await
            .unwrap();
        let mut frames_rx = a.frames_rx;

        drop(feed); // the shell exited

        assert!(frames_rx.recv().await.is_none());
        assert!(
            !a.booted.load(Ordering::SeqCst),
            "not booted, so the forwarding loop reports Exit"
        );
        for _ in 0..100 {
            if reg.count().await == 0 {
                break;
            }
            tokio::task::yield_now().await;
        }
        assert_eq!(reg.count().await, 0);
    }

    #[tokio::test]
    async fn concurrent_attach_for_one_workspace_starts_one_shell() {
        let reg = TerminalRegistry::new();
        let exec = fake::FakeExec::new();
        let starts = Arc::new(AtomicUsize::new(0));

        let spawn_attach = || {
            let reg = reg.clone();
            let exec: Arc<dyn ExecControl> = exec.clone();
            let starts = starts.clone();
            tokio::spawn(async move {
                reg.attach("w1", 80, 24, exec, move || async move {
                    starts.fetch_add(1, Ordering::SeqCst);
                    // Widen the window so a second caller is inside `attach`.
                    tokio::time::sleep(Duration::from_millis(20)).await;
                    Ok(fake::pty_idle())
                })
                .await
                .unwrap()
            })
        };

        let (h1, h2) = (spawn_attach(), spawn_attach());
        let (r1, r2) = (h1.await.unwrap(), h2.await.unwrap());

        assert_eq!(
            starts.load(Ordering::SeqCst),
            1,
            "exactly one shell was started"
        );
        assert_eq!(reg.count().await, 1);
        assert_ne!(r1.resumed, r2.resumed, "one fresh, one resumed");
        assert_eq!(r1.epoch, r2.epoch);
    }

    #[tokio::test]
    async fn fake_exec_records_resize() {
        let exec = fake::FakeExec::new();
        let ctl: Arc<dyn ExecControl> = exec.clone();
        ctl.resize("x", 120, 40).await;
        assert!(exec.resized.load(Ordering::SeqCst));
        assert_eq!(*exec.last_size.lock().unwrap(), (120, 40));
    }
}
