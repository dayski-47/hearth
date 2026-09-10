//! The OpenTerminal handler.

use std::sync::Arc;

use futures_util::StreamExt;
use hearth_proto::hearth::v1::{
    terminal_client_frame::Msg as ClientMsg, terminal_server_frame::Msg as ServerMsg,
    TerminalClientFrame, TerminalExit, TerminalReady, TerminalServerFrame,
};
use tokio::io::AsyncWriteExt;
use tokio::sync::mpsc;
use tokio_stream::wrappers::ReceiverStream;
use tonic::{Response, Status, Streaming};

use crate::engine::PodmanExec;

pub type TerminalStream = ReceiverStream<Result<TerminalServerFrame, Status>>;

#[allow(clippy::result_large_err)]
fn frame(msg: ServerMsg) -> Result<TerminalServerFrame, Status> {
    Ok(TerminalServerFrame { msg: Some(msg) })
}

/// Pump exec stdout into the client until the output stream ends or the client
/// drops the response stream. Returns `true` when the stream ended on its own
/// (so the caller should still send the exit frame), `false` when the client
/// went away first.
async fn pump_stdout(
    out_tx: &mpsc::Sender<Result<TerminalServerFrame, Status>>,
    output: &mut (impl futures_util::Stream<Item = anyhow::Result<bytes::Bytes>> + Unpin),
) -> bool {
    loop {
        tokio::select! {
            // Stop as soon as the client goes away, even at an idle prompt
            // where `output.next()` would otherwise pend forever.
            _ = out_tx.closed() => return false,
            chunk = output.next() => match chunk {
                Some(Ok(bytes)) => {
                    if out_tx
                        .send(frame(ServerMsg::Stdout(bytes.to_vec())))
                        .await
                        .is_err()
                    {
                        return false;
                    }
                }
                Some(Err(e)) => {
                    let _ = out_tx
                        .send(Err(Status::internal(format!("exec output: {e:#}"))))
                        .await;
                    return false;
                }
                None => return true,
            },
        }
    }
}

// `tonic::Status` is a large error type; the generated trait forces this
// signature on us, so match it rather than fight clippy here.
#[allow(clippy::result_large_err)]
pub async fn open(
    exec: Arc<PodmanExec>,
    mut client: Streaming<TerminalClientFrame>,
) -> Result<Response<TerminalStream>, Status> {
    let init = match client.next().await {
        Some(Ok(TerminalClientFrame {
            msg: Some(ClientMsg::Init(i)),
        })) => i,
        Some(Ok(_)) => return Err(Status::invalid_argument("first frame must be init")),
        Some(Err(e)) => return Err(Status::internal(format!("client stream: {e}"))),
        None => return Err(Status::invalid_argument("client stream closed before init")),
    };

    let container = hearth_common::workspace_container_name(&init.workspace_id);
    let shell = if init.shell.is_empty() {
        "/bin/sh".to_string()
    } else {
        init.shell.clone()
    };
    let cols = init.cols.clamp(1, 1000) as u16;
    let rows = init.rows.clamp(1, 1000) as u16;

    let handle = exec
        .start_terminal(&container, &shell, cols, rows)
        .await
        .map_err(|e| Status::internal(format!("start terminal: {e:#}")))?;
    let exec_id = handle.id.clone();
    let session_id = {
        use std::time::{SystemTime, UNIX_EPOCH};
        format!(
            "t-{}",
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        )
    };
    tracing::info!(%container, session_id = %session_id, "terminal opened");

    let (tx, rx) = mpsc::channel::<Result<TerminalServerFrame, Status>>(64);

    // The handshake goes out before any output so a client never sees shell
    // bytes ahead of the ready frame. Nothing is spawned yet, so an early
    // return here leaks no task.
    tx.send(frame(ServerMsg::Ready(TerminalReady {
        session_id,
        resumed: false,
    })))
    .await
    .map_err(|_| Status::internal("client hung up"))?;

    // exec stdout -> server frames; when the stream ends, the exit code.
    let out_tx = tx.clone();
    let mut output = handle.output;
    let exec_for_exit = exec.clone();
    let exec_id_for_exit = exec_id.clone();
    tokio::spawn(async move {
        if !pump_stdout(&out_tx, &mut output).await {
            return;
        }
        // stream ended: report the exit code
        let code = exec_for_exit
            .terminal_exit_code(&exec_id_for_exit)
            .await
            .ok()
            .flatten()
            .unwrap_or(-1);
        let _ = out_tx
            .send(frame(ServerMsg::Exit(TerminalExit {
                exit_code: code,
                reason: String::new(),
            })))
            .await;
    });

    // client frames -> exec stdin / resize.
    let resize_exec = exec.clone();
    let resize_id = exec_id.clone();
    tokio::spawn(async move {
        let mut input = handle.input;
        while let Some(f) = client.next().await {
            match f {
                Ok(TerminalClientFrame {
                    msg: Some(ClientMsg::Stdin(b)),
                }) => {
                    if input.write_all(&b).await.is_err() {
                        break;
                    }
                    let _ = input.flush().await;
                }
                Ok(TerminalClientFrame {
                    msg: Some(ClientMsg::Resize(r)),
                }) => {
                    let _ = resize_exec
                        .resize_terminal(
                            &resize_id,
                            r.cols.clamp(1, 1000) as u16,
                            r.rows.clamp(1, 1000) as u16,
                        )
                        .await;
                }
                Ok(_) => {}
                Err(_) => break,
            }
        }
        let _ = input.shutdown().await;
    });

    Ok(Response::new(ReceiverStream::new(rx)))
}

#[cfg(test)]
mod tests {
    use std::time::Duration;

    use tokio::time::timeout;

    use super::*;

    #[tokio::test]
    async fn pump_stdout_stops_when_client_drops_the_stream() {
        // An idle shell prompt: no bytes coming, ever.
        let mut idle = futures_util::stream::pending::<anyhow::Result<bytes::Bytes>>();
        let (tx, rx) = mpsc::channel::<Result<TerminalServerFrame, Status>>(4);

        let pump = tokio::spawn(async move { pump_stdout(&tx, &mut idle).await });

        // Browser tab closes.
        drop(rx);

        // Without the `out_tx.closed()` arm the pump would hang here forever.
        let ended = timeout(Duration::from_secs(2), pump)
            .await
            .expect("pump task hung at an idle prompt after the client went away")
            .expect("pump task panicked");
        assert!(
            !ended,
            "client left first, so the stream did not end on its own"
        );
    }

    #[tokio::test]
    async fn pump_stdout_forwards_output_then_reports_the_end() {
        let chunks = vec![
            Ok(bytes::Bytes::from_static(b"hi")),
            Ok(bytes::Bytes::from_static(b" there")),
        ];
        let mut output = futures_util::stream::iter(chunks);
        let (tx, mut rx) = mpsc::channel::<Result<TerminalServerFrame, Status>>(4);

        let ended = pump_stdout(&tx, &mut output).await;
        assert!(ended, "the stream ran to its end");

        let mut seen = Vec::new();
        while let Ok(frame) = rx.try_recv() {
            if let Some(ServerMsg::Stdout(b)) = frame.unwrap().msg {
                seen.extend_from_slice(&b);
            }
        }
        assert_eq!(seen, b"hi there");
    }
}
