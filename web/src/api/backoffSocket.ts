import type { ConnState } from "./terminalSocket";

// Consecutive failed reconnects, with no successful open in between, before the
// socket stops retrying. A stopped or deleted workspace makes the gateway
// refuse the upgrade indefinitely; without a cap that is an endless 5s loop of
// authenticated requests.
const MAX_ATTEMPTS = 8;

interface Opts {
  onMessage: (ev: MessageEvent) => void;
  onState: (s: ConnState) => void;
  // Fired once when the reconnect budget is spent: no more attempts will be
  // made and the caller has to build a fresh socket (a page reload) to retry.
  onGiveUp?: () => void;
  binaryType?: BinaryType;
}

// The reconnect core shared by every Hearth WebSocket: backoff 1000 -> 5000 ms,
// a give-up cap, and a detach-before-close so a deliberate close never drives a
// reconnect. Callers layer their own framing on top of `socket`.
export class BackoffSocket {
  private ws: WebSocket | undefined;
  private closedByUs = false;
  private attempts = 0;
  private backoff = 1000;
  private retry: ReturnType<typeof setTimeout> | undefined;

  constructor(
    private makeUrl: () => string,
    private o: Opts,
  ) {}

  get socket(): WebSocket | undefined {
    return this.ws;
  }

  open() {
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

    this.o.onState("connecting");
    const ws = new WebSocket(this.makeUrl());
    if (this.o.binaryType) ws.binaryType = this.o.binaryType;
    this.ws = ws;

    ws.onopen = () => {
      this.attempts = 0;
      this.backoff = 1000;
      this.o.onState("open");
    };
    ws.onmessage = (ev: MessageEvent) => this.o.onMessage(ev);
    ws.onclose = () => {
      this.detach(ws);
      this.o.onState("closed");
      if (this.closedByUs) return;
      this.attempts += 1;
      if (this.attempts >= MAX_ATTEMPTS) {
        this.o.onGiveUp?.();
        return;
      }
      // Spread the retry over [raw/2, raw] so a gateway restart does not get a
      // thundering herd of reconnects landing in the same millisecond.
      const raw = Math.min(this.backoff, 5000);
      const delay = raw / 2 + Math.random() * (raw / 2);
      this.retry = setTimeout(() => this.spawn(), delay);
      this.backoff = Math.min(this.backoff * 2, 5000);
    };
    ws.onerror = () => ws.close();
  }

  // Restart from a clean slate after the reconnect budget was spent. The UI
  // wires this to a "retry" button shown once onGiveUp has fired.
  retryNow() {
    this.closedByUs = false;
    this.attempts = 0;
    this.backoff = 1000;
    clearTimeout(this.retry);
    this.spawn();
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
