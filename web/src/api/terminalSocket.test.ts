import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { TerminalSocket } from "./terminalSocket";

class FakeWS {
  static last: FakeWS | undefined;
  static OPEN = 1;
  static CONNECTING = 0;
  static CLOSING = 2;
  static CLOSED = 3;
  url: string;
  readyState = 0;
  binaryType = "";
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((e: { data: ArrayBuffer }) => void) | null = null;
  sent: ArrayBuffer[] = [];
  constructor(url: string) {
    this.url = url;
    FakeWS.last = this;
  }
  send(d: ArrayBuffer) {
    this.sent.push(d);
  }
  close() {
    this.readyState = 3;
    this.onclose?.();
  }
  open() {
    this.readyState = 1;
    this.onopen?.();
  }
}

beforeEach(() => {
  vi.stubGlobal("WebSocket", FakeWS as unknown as typeof WebSocket);
  vi.stubGlobal("location", {
    host: "h",
    protocol: "https:",
  } as unknown as Location);
  vi.useFakeTimers();
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

test("connect opens a wss url with cols/rows and reports state", () => {
  const onState = vi.fn();
  const s = new TerminalSocket("abc", { onData: () => {}, onState });
  s.connect(80, 24);
  expect(FakeWS.last?.url).toBe(
    "wss://h/api/workspaces/abc/terminal?cols=80&rows=24",
  );
  expect(onState).toHaveBeenCalledWith("connecting");
  FakeWS.last!.open();
  expect(onState).toHaveBeenCalledWith("open");
});

test("send frames stdin with the 0x00 tag", () => {
  const s = new TerminalSocket("abc", { onData: () => {}, onState: () => {} });
  s.connect(1, 1);
  FakeWS.last!.open();
  s.send("x");
  expect(new Uint8Array(FakeWS.last!.sent[0])[0]).toBe(0x00);
});

test("resize frames with the 0x01 tag", () => {
  const s = new TerminalSocket("abc", { onData: () => {}, onState: () => {} });
  s.connect(1, 1);
  FakeWS.last!.open();
  s.resize(120, 40);
  expect(new Uint8Array(FakeWS.last!.sent[0])[0]).toBe(0x01);
});

test("an unexpected close triggers a backoff reconnect", () => {
  const s = new TerminalSocket("abc", { onData: () => {}, onState: () => {} });
  s.connect(1, 1);
  FakeWS.last!.open();
  const first = FakeWS.last;
  FakeWS.last!.close();
  vi.advanceTimersByTime(1000);
  expect(FakeWS.last).not.toBe(first);
});

test("backoff doubles per close, caps at 5000, and resets after an open", () => {
  const s = new TerminalSocket("abc", { onData: () => {}, onState: () => {} });
  s.connect(1, 1);
  FakeWS.last!.open();

  const step = (delay: number) => {
    const prev = FakeWS.last!;
    prev.close();
    vi.advanceTimersByTime(delay - 1);
    expect(FakeWS.last).toBe(prev);
    vi.advanceTimersByTime(1);
    expect(FakeWS.last).not.toBe(prev);
  };

  step(1000);
  step(2000);
  step(4000);
  step(5000); // min(8000, 5000)
  step(5000); // stays capped

  FakeWS.last!.open(); // a successful open resets the backoff
  step(1000);
});

test("gives up reconnecting after the retry cap", () => {
  const onGiveUp = vi.fn();
  const s = new TerminalSocket("abc", {
    onData: () => {},
    onState: () => {},
    onGiveUp,
  });
  s.connect(1, 1);

  for (let i = 0; i < 8; i++) {
    FakeWS.last!.close();
    vi.advanceTimersByTime(5000);
  }
  const settled = FakeWS.last;
  expect(onGiveUp).toHaveBeenCalledTimes(1);

  vi.advanceTimersByTime(60_000);
  expect(FakeWS.last).toBe(settled);
});

test("close() prevents reconnect", () => {
  const s = new TerminalSocket("abc", { onData: () => {}, onState: () => {} });
  s.connect(1, 1);
  FakeWS.last!.open();
  const first = FakeWS.last;
  s.close();
  vi.advanceTimersByTime(10000);
  expect(FakeWS.last).toBe(first);
});
