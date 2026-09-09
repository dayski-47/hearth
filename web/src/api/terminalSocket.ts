import { BackoffSocket } from "./backoffSocket";
import { encodeResize, encodeStdin } from "./wireCodec";
import { wsProto } from "./wsUrl";

export type ConnState = "connecting" | "open" | "closed";

interface Handlers {
  onData: (bytes: Uint8Array) => void;
  onState: (s: ConnState) => void;
  // Fired once when the reconnect budget is spent: no more attempts will be
  // made and the caller has to build a fresh socket (a page reload) to retry.
  onGiveUp?: () => void;
}

// The workspace terminal stream: a binary WebSocket carrying the tagged stdin
// and resize frames from wireCodec. The reconnect lifecycle lives in
// BackoffSocket; this class only owns the url and the framing.
export class TerminalSocket {
  private bs: BackoffSocket;
  private cols = 80;
  private rows = 24;

  constructor(workspaceId: string, h: Handlers) {
    this.bs = new BackoffSocket(
      () => {
        const proto = wsProto();
        return `${proto}://${location.host}/api/workspaces/${workspaceId}/terminal?cols=${this.cols}&rows=${this.rows}`;
      },
      {
        binaryType: "arraybuffer",
        onState: h.onState,
        onGiveUp: h.onGiveUp,
        onMessage: (e) => h.onData(new Uint8Array(e.data as ArrayBuffer)),
      },
    );
  }

  connect(cols: number, rows: number) {
    this.cols = cols;
    this.rows = rows;
    this.bs.open();
  }

  send(data: string) {
    const ws = this.bs.socket;
    if (ws?.readyState === WebSocket.OPEN) ws.send(encodeStdin(data));
  }

  resize(cols: number, rows: number) {
    this.cols = cols;
    this.rows = rows;
    const ws = this.bs.socket;
    if (ws?.readyState === WebSocket.OPEN) ws.send(encodeResize(cols, rows));
  }

  close() {
    this.bs.close();
  }
}
