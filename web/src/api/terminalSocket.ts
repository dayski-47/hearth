import { encodeResize, encodeStdin } from "./wireCodec";

export type ConnState = "connecting" | "open" | "closed";

interface Handlers {
  onData: (bytes: Uint8Array) => void;
  onState: (s: ConnState) => void;
  // Fired once when the reconnect budget is spent: no more attempts will be
  // made and the caller has to build a fresh socket (a page reload) to retry.
  onGiveUp?: () => void;
}

// Consecutive failed reconnects, with no successful open in between, before the
// socket stops retrying. A stopped or deleted workspace makes the gateway
// refuse the upgrade indefinitely; without a cap that is an endless 5s loop of
// authenticated requests.
const MAX_ATTEMPTS = 8;

export class TerminalSocket {
  private ws: WebSocket | undefined;
  private closedByUs = false;
  private attempts = 0;
  private backoff = 1000;
  private retry: ReturnType<typeof setTimeout> | undefined;
  private cols = 80;
  private rows = 24;

  constructor(
    private workspaceId: string,
    private h: Handlers,
  ) {}

  connect(cols: number, rows: number) {
    this.cols = cols;
    this.rows = rows;
    this.closedByUs = false;
    this.attempts = 0;
    this.backoff = 1000;
    this.spawn();
  }

  private detach(ws: WebSocket) {
    ws.onopen = null;
    ws.onmessage = null;
    ws.onclose = null;
    ws.onerror = null;
  }

  private spawn() {
    // Drop any socket still lingering from a previous attempt so its close
    // event cannot drive a second reconnect chain.
    if (this.ws) {
      this.detach(this.ws);
      try {
        this.ws.close();
      } catch {
        // already closing or closed
      }
    }

    this.h.onState("connecting");
    const proto = location.protocol === "https:" ? "wss" : "ws";
    const url = `${proto}://${location.host}/api/workspaces/${this.workspaceId}/terminal?cols=${this.cols}&rows=${this.rows}`;
    const ws = new WebSocket(url);
    ws.binaryType = "arraybuffer";
    this.ws = ws;

    ws.onopen = () => {
      this.attempts = 0;
      this.backoff = 1000;
      this.h.onState("open");
    };
    ws.onmessage = (e: MessageEvent) => {
      this.h.onData(new Uint8Array(e.data as ArrayBuffer));
    };
    ws.onclose = () => {
      this.detach(ws);
      this.h.onState("closed");
      if (this.closedByUs) return;
      this.attempts += 1;
      if (this.attempts >= MAX_ATTEMPTS) {
        this.h.onGiveUp?.();
        return;
      }
      this.retry = setTimeout(() => this.spawn(), this.backoff);
      this.backoff = Math.min(this.backoff * 2, 5000);
    };
    ws.onerror = () => ws.close();
  }

  send(data: string) {
    if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(encodeStdin(data));
  }

  resize(cols: number, rows: number) {
    this.cols = cols;
    this.rows = rows;
    if (this.ws?.readyState === WebSocket.OPEN)
      this.ws.send(encodeResize(cols, rows));
  }

  close() {
    this.closedByUs = true;
    clearTimeout(this.retry);
    if (this.ws) {
      this.detach(this.ws);
      this.ws.close();
    }
  }
}
