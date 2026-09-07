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

    // exec stdout -> server frames; when the stream ends, the exit code.
    let out_tx = tx.clone();
    let mut output = handle.output;
    let exec_for_exit = exec.clone();
    let exec_id_for_exit = exec_id.clone();
    tokio::spawn(async move {
        while let Some(chunk) = output.next().await {
            match chunk {
                Ok(bytes) => {
                    if out_tx
                        .send(frame(ServerMsg::Stdout(bytes.to_vec())))
                        .await
                        .is_err()
                    {
                        return;
                    }
                }
                Err(e) => {
                    let _ = out_tx
                        .send(Err(Status::internal(format!("exec output: {e:#}"))))
                        .await;
                    return;
                }
            }
        }
        let code = exec_for_exit
            .terminal_exit_code(&exec_id_for_exit)
            .await
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

    tx.send(frame(ServerMsg::Ready(TerminalReady { session_id })))
        .await
        .map_err(|_| Status::internal("client hung up"))?;

    Ok(Response::new(ReceiverStream::new(rx)))
}
