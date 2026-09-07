//! The OpenTerminal handler.

use std::sync::Arc;

use hearth_proto::hearth::v1::{TerminalClientFrame, TerminalServerFrame};
use tokio_stream::wrappers::ReceiverStream;
use tonic::{Response, Status, Streaming};

use crate::engine::PodmanExec;

pub type TerminalStream = ReceiverStream<Result<TerminalServerFrame, Status>>;

// `tonic::Status` is a large error type; the generated trait forces this
// signature on us, so match it rather than fight clippy here.
#[allow(clippy::result_large_err)]
pub async fn open(
    _exec: Arc<PodmanExec>,
    _client: Streaming<TerminalClientFrame>,
) -> Result<Response<TerminalStream>, Status> {
    Err(Status::unimplemented("terminal: Task 5"))
}
