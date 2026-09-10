//! The terminal session registry: one held session per workspace, so a
//! browser reconnect re-attaches to the same shell instead of starting a
//! new one.

use std::collections::HashMap;
use std::pin::Pin;
use std::sync::Arc;
use std::time::Duration;

use futures_util::{Stream, StreamExt};
use tokio::io::AsyncWrite;
use tokio::sync::mpsc;
use tokio::sync::Mutex;

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

/// The result of attaching a client to a session, held or fresh.
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
    pub session: Arc<Mutex<TerminalSession>>,
}

/// A shell held open across browser reconnects.
pub struct TerminalSession {
    pub exec_id: String,
    /// Scrollback replayed on re-attach. The pump appends every chunk.
    pub ring: Arc<Mutex<Ring>>,
    pub size: (u16, u16),
    /// Set while no client is attached; aborting it cancels the reap.
    pub grace: Option<tokio::task::JoinHandle<()>>,
    /// Boots the currently attached client (last-attach-wins). A re-attach
    /// swaps this for a fresh `Notify` and fires `notify_one` on the old one,
    /// so the permit reaches only the outgoing client's serve loop and
    /// survives until it next polls, even if it is mid-send at the time.
    pub boot: Arc<tokio::sync::Notify>,
    /// The forever-running PTY -> ring copy. Lives as long as the session,
    /// not the client; aborted only when the session is reaped.
    pub pump: tokio::task::JoinHandle<()>,
    /// The PTY stdin writer. Held by the session so a re-attach reuses it.
    pub input: Arc<Mutex<Pin<Box<dyn AsyncWrite + Send>>>>,
    /// The currently attached client's live-output channel, or `None` while
    /// detached. The pump swaps a chunk in per read; `attach` swaps the
    /// whole sender; `detach` clears it.
    pub frames_tx: Arc<Mutex<Option<mpsc::Sender<Vec<u8>>>>>,
}

#[derive(Default)]
pub struct TerminalRegistry {
    sessions: Mutex<HashMap<String, Arc<Mutex<TerminalSession>>>>,
}

/// Copy PTY output into the ring forever, and into the attached client's
/// channel when there is one. Ends only when the shell's output stream
/// does; on that end it clears `frames_tx` (so the attached client's serve
/// loop sees its channel close and reports the exit) and drops the registry
/// entry.
async fn run_pump(
    mut output: Pin<Box<dyn Stream<Item = anyhow::Result<bytes::Bytes>> + Send>>,
    ring: Arc<Mutex<Ring>>,
    frames_tx: Arc<Mutex<Option<mpsc::Sender<Vec<u8>>>>>,
    reg: Arc<TerminalRegistry>,
    workspace_id: String,
) {
    while let Some(item) = output.next().await {
        let chunk = match item {
            Ok(bytes) => bytes.to_vec(),
            Err(_) => break,
        };
        ring.lock().await.push(&chunk);
        // Clone the sender out so a slow client cannot wedge the lock that
        // `attach` needs to swap channels.
        let sender = frames_tx.lock().await.clone();
        if let Some(tx) = sender {
            let _ = tx.send(chunk).await;
        }
    }
    // The shell exited or the container stopped. Drop the sender so the
    // attached client (if any) reports the exit, then forget the session.
    *frames_tx.lock().await = None;
    reg.remove(&workspace_id).await;
}

impl TerminalRegistry {
    pub fn new() -> Arc<Self> {
        Arc::new(Self::default())
    }

    pub async fn get(&self, workspace_id: &str) -> Option<Arc<Mutex<TerminalSession>>> {
        self.sessions.lock().await.get(workspace_id).cloned()
    }

    pub async fn remove(&self, workspace_id: &str) {
        self.sessions.lock().await.remove(workspace_id);
    }

    pub async fn insert(&self, workspace_id: String, s: Arc<Mutex<TerminalSession>>) {
        self.sessions.lock().await.insert(workspace_id, s);
    }

    /// Attach a client. If a held session exists, cancel its grace timer,
    /// boot the current client, resize, and return a replay. Otherwise run
    /// `start`, spawn the pump, and return a fresh session.
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
        if let Some(session) = self.get(workspace_id).await {
            let (tx, rx) = mpsc::channel::<Vec<u8>>(256);
            let (exec_id, replay) = {
                let mut s = session.lock().await;
                if let Some(h) = s.grace.take() {
                    h.abort();
                }
                // Swap in a fresh boot channel and fire the old one. The
                // outgoing client's serve loop holds the old `Notify`, so the
                // permit reaches only it; the incoming client, which reads
                // `s.boot` after this returns, gets a clean one. The permit
                // is set before the sender swap below closes the old client's
                // channel, so a biased serve loop reads "booted", not "shell
                // exited".
                let outgoing = std::mem::replace(&mut s.boot, Arc::new(tokio::sync::Notify::new()));
                outgoing.notify_one();
                *s.frames_tx.lock().await = Some(tx);
                s.size = (cols, rows);
                let replay = s.ring.lock().await.snapshot();
                (s.exec_id.clone(), replay)
            };
            exec.resize(&exec_id, cols, rows).await;
            return Ok(Attached {
                resumed: true,
                replay,
                frames_rx: rx,
                session,
            });
        }

        let started = start().await?;
        let ring = Arc::new(Mutex::new(Ring::new()));
        let (tx, rx) = mpsc::channel::<Vec<u8>>(256);
        let frames_tx: Arc<Mutex<Option<mpsc::Sender<Vec<u8>>>>> = Arc::new(Mutex::new(Some(tx)));

        let pump = tokio::spawn(run_pump(
            started.output,
            ring.clone(),
            frames_tx.clone(),
            self.clone(),
            workspace_id.to_string(),
        ));

        let session = Arc::new(Mutex::new(TerminalSession {
            exec_id: started.exec_id,
            ring,
            size: (cols, rows),
            grace: None,
            boot: Arc::new(tokio::sync::Notify::new()),
            pump,
            input: Arc::new(Mutex::new(started.input)),
            frames_tx,
        }));
        self.insert(workspace_id.to_string(), session.clone()).await;

        Ok(Attached {
            resumed: false,
            replay: Vec::new(),
            frames_rx: rx,
            session,
        })
    }

    /// The client went away. Clear the live channel so the pump only feeds
    /// the ring, and arm the reap timer. The timer task captures the
    /// registry and the id only, never a session `Arc`, so a session that
    /// is not re-attached is fully dropped when the timer fires.
    pub async fn detach(self: &Arc<Self>, workspace_id: &str, grace: Duration) {
        let Some(session) = self.get(workspace_id).await else {
            return;
        };
        let mut s = session.lock().await;
        *s.frames_tx.lock().await = None;
        if s.grace.is_some() {
            return;
        }
        let reg = self.clone();
        let id = workspace_id.to_string();
        s.grace = Some(tokio::spawn(async move {
            tokio::time::sleep(grace).await;
            if let Some(session) = reg.get(&id).await {
                session.lock().await.pump.abort();
                reg.remove(&id).await;
            }
        }));
    }

    #[cfg(test)]
    pub async fn count(&self) -> usize {
        self.sessions.lock().await.len()
    }
}

#[cfg(test)]
pub(crate) mod fake {
    use std::sync::atomic::{AtomicBool, Ordering};
    use std::sync::Arc;

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
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Duration;

    /// A channel-backed fake PTY: the returned sender feeds output bytes,
    /// and dropping it (or sending an `Err`) ends the stream.
    fn fake_pty() -> (StartedPty, mpsc::Sender<anyhow::Result<bytes::Bytes>>) {
        let (feed, rx) = mpsc::channel::<anyhow::Result<bytes::Bytes>>(64);
        let output = Box::pin(tokio_stream::wrappers::ReceiverStream::new(rx));
        let input: Pin<Box<dyn AsyncWrite + Send>> = Box::pin(tokio::io::sink());
        (
            StartedPty {
                exec_id: "e1".into(),
                output,
                input,
            },
            feed,
        )
    }

    async fn ring_len(session: &Arc<Mutex<TerminalSession>>) -> usize {
        let ring = session.lock().await.ring.clone();
        let len = ring.lock().await.len(); // guard must drop before `ring` does
        len
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
        let (pty, _feed) = fake_pty();

        let a = reg
            .attach("w1", 80, 24, exec, move || async move { Ok(pty) })
            .await
            .unwrap();

        assert!(!a.resumed);
        assert!(a.replay.is_empty());
        assert_eq!(reg.count().await, 1);
    }

    #[tokio::test]
    async fn detach_arms_grace_and_keeps_the_entry() {
        let reg = TerminalRegistry::new();
        let exec = fake::FakeExec::new();
        let (pty, _feed) = fake_pty();

        let a = reg
            .attach("w1", 80, 24, exec, move || async move { Ok(pty) })
            .await
            .unwrap();
        drop(a); // the client's receiver is gone

        reg.detach("w1", Duration::from_secs(60)).await;

        assert_eq!(reg.count().await, 1);
        let session = reg.get("w1").await.unwrap();
        assert!(session.lock().await.grace.is_some());
    }

    #[tokio::test]
    async fn reattach_within_grace_resumes_and_replays_the_ring() {
        let reg = TerminalRegistry::new();
        let exec = fake::FakeExec::new();
        let (pty, feed) = fake_pty();

        let a = reg
            .attach("w1", 80, 24, exec.clone(), move || async move { Ok(pty) })
            .await
            .unwrap();
        let session = a.session.clone();
        drop(a);
        reg.detach("w1", Duration::from_secs(60)).await;

        // The pump keeps filling the ring while no client is attached.
        feed.send(Ok(bytes::Bytes::from_static(b"while-away")))
            .await
            .unwrap();
        wait_for_ring(&session, b"while-away".len()).await;

        let a2 = reg
            .attach("w1", 100, 40, exec.clone(), || async {
                panic!("re-attach must not start a new pty")
            })
            .await
            .unwrap();

        assert!(a2.resumed);
        assert_eq!(a2.replay, b"while-away");
        assert!(reg.get("w1").await.unwrap().lock().await.grace.is_none());
        assert_eq!(reg.count().await, 1);
        assert_eq!(*exec.last_size.lock().unwrap(), (100, 40));

        // Keep the feed alive until the assertions are done.
        drop(feed);
    }

    #[tokio::test(start_paused = true)]
    async fn grace_expiry_reaps_the_session_and_aborts_the_pump() {
        let reg = TerminalRegistry::new();
        let exec = fake::FakeExec::new();
        let (pty, _feed) = fake_pty();

        let a = reg
            .attach("w1", 80, 24, exec, move || async move { Ok(pty) })
            .await
            .unwrap();
        let session = a.session.clone();
        drop(a);
        reg.detach("w1", Duration::from_secs(60)).await;
        // Let the grace task run as far as its sleep so the timer is armed
        // before we advance the clock.
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

    #[tokio::test]
    async fn a_second_attach_boots_the_first_client() {
        let reg = TerminalRegistry::new();
        let exec = fake::FakeExec::new();
        let (pty, _feed) = fake_pty();

        let a1 = reg
            .attach("w1", 80, 24, exec.clone(), move || async move { Ok(pty) })
            .await
            .unwrap();
        let boot = a1.session.lock().await.boot.clone();
        let mut frames_rx = a1.frames_rx;

        // Mirror the serve loop's biased select: boot must win even though
        // the sender swap also closes this receiver.
        let watcher = tokio::spawn(async move {
            loop {
                tokio::select! {
                    biased;
                    _ = boot.notified() => return "booted",
                    r = frames_rx.recv() => {
                        if r.is_none() {
                            return "channel-closed";
                        }
                    }
                }
            }
        });
        tokio::time::sleep(Duration::from_millis(10)).await;

        let _a2 = reg
            .attach("w1", 80, 24, exec.clone(), || async {
                panic!("second attach must not start a new pty")
            })
            .await
            .unwrap();

        let how = tokio::time::timeout(Duration::from_secs(1), watcher)
            .await
            .expect("watcher hung")
            .unwrap();
        assert_eq!(how, "booted");
    }

    #[tokio::test]
    async fn pump_end_clears_the_channel_and_drops_the_entry() {
        let reg = TerminalRegistry::new();
        let exec = fake::FakeExec::new();
        let (pty, feed) = fake_pty();

        let a = reg
            .attach("w1", 80, 24, exec, move || async move { Ok(pty) })
            .await
            .unwrap();
        let mut frames_rx = a.frames_rx;

        drop(feed); // the shell exited

        // The attached client observes the channel close, and the entry goes.
        assert!(frames_rx.recv().await.is_none());
        for _ in 0..100 {
            if reg.count().await == 0 {
                break;
            }
            tokio::task::yield_now().await;
        }
        assert_eq!(reg.count().await, 0);
    }

    #[tokio::test]
    async fn fake_exec_records_resize() {
        use std::sync::atomic::Ordering;
        let exec = fake::FakeExec::new();
        let ctl: Arc<dyn ExecControl> = exec.clone();
        ctl.resize("x", 120, 40).await;
        assert!(exec.resized.load(Ordering::SeqCst));
        assert_eq!(*exec.last_size.lock().unwrap(), (120, 40));
    }
}
