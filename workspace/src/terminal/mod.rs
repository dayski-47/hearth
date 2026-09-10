//! The OpenTerminal handler.

mod registry;
mod ring;

pub use registry::{ExecControl, TerminalRegistry};

use std::sync::Arc;
use std::time::Duration;

use futures_util::StreamExt;
use hearth_proto::hearth::v1::{
    terminal_client_frame::Msg as ClientMsg, terminal_server_frame::Msg as ServerMsg,
    TerminalClientFrame, TerminalExit, TerminalInit, TerminalReady, TerminalServerFrame,
};
use tokio::io::AsyncWriteExt;
use tokio::sync::mpsc;
use tokio::sync::Notify;
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
/// `biased` with the boot arm first is load-bearing: when a re-attach swaps
/// the session's sender, this client's `frames_rx` also closes, which looks
/// exactly like a shell exit. The boot permit is set before that swap, so
/// polling it first tells the two apart.
async fn forward_to_client(
    out_tx: mpsc::Sender<Result<TerminalServerFrame, Status>>,
    mut frames_rx: mpsc::Receiver<Vec<u8>>,
    boot: Arc<Notify>,
) -> ClientEnd {
    loop {
        tokio::select! {
            biased;
            _ = boot.notified() => return ClientEnd::Booted,
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
    tracing::info!(%container, %session_id, resumed = attached.resumed, "terminal attached");

    let (tx, rx) = mpsc::channel::<Result<TerminalServerFrame, Status>>(64);

    // The handshake goes out before any output so a client never sees shell
    // bytes ahead of the ready frame.
    tx.send(frame(ServerMsg::Ready(TerminalReady {
        session_id,
        resumed: attached.resumed,
    })))
    .await
    .map_err(|_| Status::internal("client hung up"))?;

    // Replay the scrollback before live output.
    for chunk in attached.replay.chunks(32 * 1024) {
        let _ = tx.send(frame(ServerMsg::Stdout(chunk.to_vec()))).await;
    }

    // One short lock to lift out everything the two client tasks need, so
    // neither task holds a session `Arc` that could outlive a reap.
    let (boot, input, exec_id) = {
        let s = attached.session.lock().await;
        (s.boot.clone(), s.input.clone(), s.exec_id.clone())
    };

    // Live output -> this client, until it leaves or is superseded.
    let frames_rx = attached.frames_rx;
    let out_tx = tx.clone();
    let reg_serve = reg.clone();
    let ws_serve = workspace_id.clone();
    tokio::spawn(async move {
        match forward_to_client(out_tx, frames_rx, boot).await {
            ClientEnd::Detached => reg_serve.detach(&ws_serve, grace).await,
            ClientEnd::Booted | ClientEnd::ShellExited => {}
        }
    });

    // Client frames -> PTY stdin / resize. Only the PTY handles are needed,
    // not a session `Arc`, so this task never keeps a reaped session alive.
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
        let boot = Arc::new(Notify::new());

        let task = tokio::spawn(forward_to_client(out_tx, frames_rx, boot));

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
        let boot = Arc::new(Notify::new());

        frames_tx.send(b"hi".to_vec()).await.unwrap();
        frames_tx.send(b" there".to_vec()).await.unwrap();
        drop(frames_tx); // the shell exited

        let end = forward_to_client(out_tx, frames_rx, boot).await;
        assert_eq!(end, ClientEnd::ShellExited);

        let (stdout, saw_exit) = drain(&mut out_rx);
        assert_eq!(stdout, b"hi there");
        assert!(saw_exit, "a genuine shell exit sends an Exit frame");
    }

    #[tokio::test]
    async fn forward_to_client_boot_beats_a_closed_channel() {
        let (frames_tx, frames_rx) = mpsc::channel::<Vec<u8>>(4);
        let (out_tx, mut out_rx) = mpsc::channel::<Result<TerminalServerFrame, Status>>(8);
        let boot = Arc::new(Notify::new());

        // A re-attach: the permit is set, then the old sender is dropped.
        boot.notify_one();
        drop(frames_tx);

        let end = forward_to_client(out_tx, frames_rx, boot).await;
        assert_eq!(end, ClientEnd::Booted);
        // A booted client gets no frame, in particular no Exit.
        assert!(out_rx.try_recv().is_err());
    }
}
