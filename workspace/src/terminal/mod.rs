//! The OpenTerminal handler.

mod registry;
mod ring;

pub use registry::{ExecControl, TerminalRegistry};

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::Duration;

use futures_util::StreamExt;
use hearth_proto::hearth::v1::{
    terminal_client_frame::Msg as ClientMsg, terminal_server_frame::Msg as ServerMsg,
    TerminalClientFrame, TerminalExit, TerminalInit, TerminalReady, TerminalServerFrame,
};
use tokio::io::AsyncWriteExt;
use tokio::sync::mpsc;
use tokio_stream::wrappers::ReceiverStream;
use tonic::{Response, Status, Streaming};

use crate::engine::PodmanExec;

use registry::StartedPty;

type ClientStream = Streaming<TerminalClientFrame>;

pub type TerminalStream = ReceiverStream<Result<TerminalServerFrame, Status>>;

#[allow(clippy::result_large_err)]
fn frame(msg: ServerMsg) -> Result<TerminalServerFrame, Status> {
    Ok(TerminalServerFrame { msg: Some(msg) })
}

fn now_session_id() -> String {
    use std::time::{SystemTime, UNIX_EPOCH};
    format!(
        "t-{}",
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_nanos()
    )
}

#[allow(clippy::result_large_err)]
async fn read_init(client: &mut ClientStream) -> Result<TerminalInit, Status> {
    match client.next().await {
        Some(Ok(TerminalClientFrame {
            msg: Some(ClientMsg::Init(i)),
        })) => Ok(i),
        Some(Ok(_)) => Err(Status::invalid_argument("first frame must be init")),
        Some(Err(e)) => Err(Status::internal(format!("client stream: {e}"))),
        None => Err(Status::invalid_argument("client stream closed before init")),
    }
}

/// How a client's forwarding loop ended.
#[derive(Debug, PartialEq, Eq)]
enum ClientEnd {
    /// The browser dropped the response stream.
    Detached,
    /// A newer attach took the session (last-attach-wins). Quiet: no frame.
    Booted,
    /// The pump ended, i.e. the shell exited. An `Exit` frame was sent.
    ShellExited,
}

/// Forward live PTY output to one client until it goes away, is booted by a
/// newer attach, or the shell exits.
///
/// When `frames_rx` closes it could mean either "a re-attach swapped this
/// client's sender out" or "the shell exited". `booted` is the authority:
/// `attach` sets it before it drops the old sender, so the check here is
/// order-independent - a spurious `Exit` to a booted client is impossible
/// even if the select resolves the `recv` arm on the same poll the notify
/// would have landed on.
async fn forward_to_client(
    out_tx: mpsc::Sender<Result<TerminalServerFrame, Status>>,
    mut frames_rx: mpsc::Receiver<Vec<u8>>,
    booted: Arc<AtomicBool>,
) -> ClientEnd {
    loop {
        tokio::select! {
            _ = out_tx.closed() => return ClientEnd::Detached,
            chunk = frames_rx.recv() => match chunk {
                Some(bytes) => {
                    if out_tx
                        .send(frame(ServerMsg::Stdout(bytes)))
                        .await
                        .is_err()
                    {
                        return ClientEnd::Detached;
                    }
                }
                None => {
                    if booted.load(Ordering::SeqCst) {
                        return ClientEnd::Booted;
                    }
                    let _ = out_tx
                        .send(frame(ServerMsg::Exit(TerminalExit {
                            exit_code: -1,
                            reason: String::new(),
                        })))
                        .await;
                    return ClientEnd::ShellExited;
                }
            },
        }
    }
}

// `tonic::Status` is a large error type; the generated trait forces this
// signature on us, so match it rather than fight clippy here.
#[allow(clippy::result_large_err)]
pub async fn open(
    reg: Arc<TerminalRegistry>,
    exec: Arc<PodmanExec>,
    grace: Duration,
    mut client: ClientStream,
) -> Result<Response<TerminalStream>, Status> {
    let init = read_init(&mut client).await?;

    let workspace_id = init.workspace_id.clone();
    let container = hearth_common::workspace_container_name(&workspace_id);
    let shell = if init.shell.is_empty() {
        "/bin/sh".to_string()
    } else {
        init.shell.clone()
    };
    let cols = init.cols.clamp(1, 1000) as u16;
    let rows = init.rows.clamp(1, 1000) as u16;

    let exec_ctl: Arc<dyn ExecControl> = exec.clone();
    let start_exec = exec.clone();
    let start_container = container.clone();
    let start_shell = shell.clone();
    let attached = reg
        .attach(&workspace_id, cols, rows, exec_ctl, move || async move {
            let h = start_exec
                .start_terminal(&start_container, &start_shell, cols, rows)
                .await?;
            Ok(StartedPty {
                exec_id: h.id,
                output: h.output,
                input: h.input,
            })
        })
        .await
        .map_err(|e| Status::internal(format!("attach terminal: {e:#}")))?;

    let session_id = now_session_id();
    tracing::info!(
        %container,
        %session_id,
        epoch = attached.epoch,
        resumed = attached.resumed,
        "terminal attached"
    );

    let (tx, rx) = mpsc::channel::<Result<TerminalServerFrame, Status>>(64);

    // The handshake goes out before any output so a client never sees shell
    // bytes ahead of the ready frame.
    tx.send(frame(ServerMsg::Ready(TerminalReady {
        session_id,
        resumed: attached.resumed,
    })))
    .await
    .map_err(|_| Status::internal("client hung up"))?;

    // The replay is written into `tx` (64 slots) before this fn returns and
    // hands the `ReceiverStream` to tonic, so nothing drains it yet. Safe
    // only because `Ring` caps at 256 KiB, i.e. at most 8 of these 32 KiB
    // frames; a ring past ~2 MiB would fill the channel and deadlock here.
    for chunk in attached.replay.chunks(32 * 1024) {
        let _ = tx.send(frame(ServerMsg::Stdout(chunk.to_vec()))).await;
    }

    // Everything the two client tasks need was lifted out of the session
    // under the slot lock inside `attach`; neither task locks or holds a
    // session `Arc`, so neither can keep a reaped session alive.
    let epoch = attached.epoch;
    let booted = attached.booted;
    let booted_detach = booted.clone();
    let input = attached.input;
    let exec_id = attached.exec_id;

    // Live output -> this client, until it leaves or is superseded.
    let frames_rx = attached.frames_rx;
    let out_tx = tx.clone();
    let reg_serve = reg.clone();
    let ws_serve = workspace_id.clone();
    tokio::spawn(async move {
        if forward_to_client(out_tx, frames_rx, booted).await == ClientEnd::Detached {
            reg_serve
                .detach(&ws_serve, epoch, booted_detach, grace)
                .await;
        }
    });

    // Client frames -> PTY stdin / resize.
    let resize_exec = exec.clone();
    tokio::spawn(async move {
        while let Some(f) = client.next().await {
            match f {
                Ok(TerminalClientFrame {
                    msg: Some(ClientMsg::Stdin(b)),
                }) => {
                    let mut w = input.lock().await;
                    if w.write_all(&b).await.is_err() {
                        break;
                    }
                    let _ = w.flush().await;
                }
                Ok(TerminalClientFrame {
                    msg: Some(ClientMsg::Resize(r)),
                }) => {
                    let _ = resize_exec
                        .resize_terminal(
                            &exec_id,
                            r.cols.clamp(1, 1000) as u16,
                            r.rows.clamp(1, 1000) as u16,
                        )
                        .await;
                }
                Ok(_) => {}
                Err(_) => break,
            }
        }
        // The client stream closed. Leave the PTY writer open: the session
        // owns it, and a reconnect within grace will reuse it.
    });

    Ok(Response::new(ReceiverStream::new(rx)))
}

#[cfg(test)]
mod tests {
    use std::time::Duration;

    use tokio::time::timeout;

    use super::registry::fake;
    use super::*;

    fn drain(rx: &mut mpsc::Receiver<Result<TerminalServerFrame, Status>>) -> (Vec<u8>, bool) {
        let mut stdout = Vec::new();
        let mut saw_exit = false;
        while let Ok(frame) = rx.try_recv() {
            match frame.unwrap().msg {
                Some(ServerMsg::Stdout(b)) => stdout.extend_from_slice(&b),
                Some(ServerMsg::Exit(_)) => saw_exit = true,
                _ => {}
            }
        }
        (stdout, saw_exit)
    }

    #[tokio::test]
    async fn forward_to_client_stops_when_the_client_drops_the_stream() {
        // An idle prompt: the pump never sends anything.
        let (_frames_tx, frames_rx) = mpsc::channel::<Vec<u8>>(4);
        let (out_tx, out_rx) = mpsc::channel::<Result<TerminalServerFrame, Status>>(4);
        let booted = Arc::new(AtomicBool::new(false));

        let task = tokio::spawn(forward_to_client(out_tx, frames_rx, booted));

        // Browser tab closes.
        drop(out_rx);

        let end = timeout(Duration::from_secs(2), task)
            .await
            .expect("forward loop hung at an idle prompt after the client left")
            .expect("forward loop panicked");
        assert_eq!(end, ClientEnd::Detached);
    }

    #[tokio::test]
    async fn forward_to_client_forwards_output_then_reports_the_shell_exit() {
        let (frames_tx, frames_rx) = mpsc::channel::<Vec<u8>>(4);
        let (out_tx, mut out_rx) = mpsc::channel::<Result<TerminalServerFrame, Status>>(8);
        let booted = Arc::new(AtomicBool::new(false));

        frames_tx.send(b"hi".to_vec()).await.unwrap();
        frames_tx.send(b" there".to_vec()).await.unwrap();
        drop(frames_tx); // the shell exited

        let end = forward_to_client(out_tx, frames_rx, booted).await;
        assert_eq!(end, ClientEnd::ShellExited);

        let (stdout, saw_exit) = drain(&mut out_rx);
        assert_eq!(stdout, b"hi there");
        assert!(saw_exit, "a genuine shell exit sends an Exit frame");
    }

    #[tokio::test]
    async fn forward_to_client_is_quiet_when_booted() {
        let (frames_tx, frames_rx) = mpsc::channel::<Vec<u8>>(4);
        let (out_tx, mut out_rx) = mpsc::channel::<Result<TerminalServerFrame, Status>>(8);
        let booted = Arc::new(AtomicBool::new(true)); // a newer attach superseded us

        drop(frames_tx);

        let end = forward_to_client(out_tx, frames_rx, booted).await;
        assert_eq!(end, ClientEnd::Booted);
        // A booted client gets no frame, in particular no Exit.
        assert!(out_rx.try_recv().is_err());
    }

    /// The whole boot path composed: attach a session, run `forward_to_client`
    /// against its live channel, then attach again for the same workspace and
    /// assert the first loop returns `Booted` with no `Exit` frame - even
    /// though the re-attach is what closes its `frames_rx`.
    #[tokio::test]
    async fn a_reconnect_boots_the_first_client_without_an_exit_frame() {
        let reg = TerminalRegistry::new();
        let exec = fake::FakeExec::new();

        let a1 = reg
            .attach("w1", 80, 24, exec.clone(), move || async move {
                Ok(fake::pty_idle())
            })
            .await
            .unwrap();

        let (out_tx, mut out_rx) = mpsc::channel::<Result<TerminalServerFrame, Status>>(8);
        let booted = a1.booted.clone();
        let frames_rx = a1.frames_rx;
        let serve = tokio::spawn(forward_to_client(out_tx, frames_rx, booted));

        tokio::time::sleep(Duration::from_millis(10)).await;

        let _a2 = reg
            .attach("w1", 80, 24, exec.clone(), || async {
                panic!("re-attach must not start a new pty")
            })
            .await
            .unwrap();

        let end = timeout(Duration::from_secs(1), serve)
            .await
            .expect("forward loop hung after the reconnect")
            .expect("forward loop panicked");
        assert_eq!(end, ClientEnd::Booted);
        assert!(
            out_rx.try_recv().is_err(),
            "a booted client must not receive any frame"
        );
    }
}
