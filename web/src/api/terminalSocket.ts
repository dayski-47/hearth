import { encodeResize, encodeStdin } from "./wireCodec";

export type ConnState = "connecting" | "open" | "closed";

interface Handlers {
  onData: (bytes: Uint8Array) => void;
  onState: (s: ConnState) => void;
}

export class TerminalSocket {
  private ws: WebSocket | undefined;
  private closedByUs = false;
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
    this.spawn();
  }

  private spawn() {
    this.h.onState("connecting");
    const proto = location.protocol === "https:" ? "wss" : "ws";
    const url = `${proto}://${location.host}/api/workspaces/${this.workspaceId}/terminal?cols=${this.cols}&rows=${this.rows}`;
    const ws = new WebSocket(url);
    ws.binaryType = "arraybuffer";
    this.ws = ws;

    ws.onopen = () => {
      this.backoff = 1000;
      this.h.onState("open");
    };
    ws.onmessage = (e: MessageEvent) => {
      this.h.onData(new Uint8Array(e.data as ArrayBuffer));
    };
    ws.onclose = () => {
      this.h.onState("closed");
      if (this.closedByUs) return;
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
    this.ws?.close();
  }
}
