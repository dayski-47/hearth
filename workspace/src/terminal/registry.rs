//! The terminal session registry: one held session per workspace, so a
//! browser reconnect re-attaches to the same shell instead of starting a
//! new one.

use std::collections::HashMap;
use std::sync::Arc;

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

/// A shell held open across browser reconnects. The fields past `exec_id`
/// are wired by the reconnect handler (task 3): the ring is the scrollback
/// replayed on re-attach, `grace` is the reap timer that runs while no
/// client is attached, `boot` boots a superseded client, and `pump` is the
/// forever-running PTY -> ring copy.
#[allow(dead_code)] // fields past exec_id are filled in by the reconnect handler (task 3)
pub struct TerminalSession {
    pub exec_id: String,
    pub ring: Arc<Mutex<Ring>>,
    pub size: (u16, u16),
    // Set while the client is gone; aborting it cancels the reap.
    pub grace: Option<tokio::task::JoinHandle<()>>,
    // Fired to boot the currently attached client (last-attach-wins).
    pub boot: Arc<tokio::sync::Notify>,
    // Aborts the forever-running PTY -> ring pump when the session ends.
    pub pump: tokio::task::JoinHandle<()>,
}

#[derive(Default)]
pub struct TerminalRegistry {
    sessions: Mutex<HashMap<String, Arc<Mutex<TerminalSession>>>>,
}

impl TerminalRegistry {
    pub fn new() -> Arc<Self> {
        Arc::new(Self::default())
    }

    #[allow(dead_code)] // consumed by the reconnect handler (task 3)
    pub async fn get(&self, workspace_id: &str) -> Option<Arc<Mutex<TerminalSession>>> {
        self.sessions.lock().await.get(workspace_id).cloned()
    }

    #[allow(dead_code)] // consumed by the reconnect handler (task 3)
    pub async fn remove(&self, workspace_id: &str) {
        self.sessions.lock().await.remove(workspace_id);
    }

    #[allow(dead_code)] // consumed by the reconnect handler (task 3)
    pub async fn insert(&self, workspace_id: String, s: Arc<Mutex<TerminalSession>>) {
        self.sessions.lock().await.insert(workspace_id, s);
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

    fn dummy_session() -> Arc<Mutex<TerminalSession>> {
        Arc::new(Mutex::new(TerminalSession {
            exec_id: "x".into(),
            ring: Arc::new(Mutex::new(Ring::new())),
            size: (80, 24),
            grace: None,
            boot: Arc::new(tokio::sync::Notify::new()),
            pump: tokio::spawn(async {}),
        }))
    }

    #[tokio::test]
    async fn insert_get_remove() {
        let reg = TerminalRegistry::new();
        assert_eq!(reg.count().await, 0);
        reg.insert("w1".into(), dummy_session()).await;
        assert!(reg.get("w1").await.is_some());
        assert_eq!(reg.count().await, 1);
        reg.remove("w1").await;
        assert!(reg.get("w1").await.is_none());
    }

    #[tokio::test]
    async fn boot_notify_wakes_a_waiter() {
        let s = dummy_session();
        let boot = s.lock().await.boot.clone();
        let waiter = tokio::spawn(async move { boot.notified().await });
        tokio::time::sleep(Duration::from_millis(10)).await;
        s.lock().await.boot.notify_waiters();
        tokio::time::timeout(Duration::from_secs(1), waiter)
            .await
            .expect("boot notify did not wake the waiter")
            .unwrap();
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
