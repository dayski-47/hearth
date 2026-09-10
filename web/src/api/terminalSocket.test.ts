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
  onmessage: ((e: { data: unknown }) => void) | null = null;
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
  vi.restoreAllMocks();
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
  // Pin the jitter so the scheduled delay is exactly raw/2.
  vi.spyOn(Math, "random").mockReturnValue(0);
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

  step(500); // raw 1000
  step(1000); // raw 2000
  step(2000); // raw 4000
  step(2500); // raw min(8000, 5000)
  step(2500); // stays capped

  FakeWS.last!.open(); // a successful open resets the backoff
  step(500);
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

test("a ready control frame calls onReady and is not forwarded as data", () => {
  const onData = vi.fn();
  const onReady = vi.fn();
  const s = new TerminalSocket("abc", { onData, onState: () => {}, onReady });
  s.connect(1, 1);
  FakeWS.last!.open();
  FakeWS.last!.onmessage?.({ data: '{"t":"ready","resumed":true}' });
  expect(onReady).toHaveBeenCalledWith(true);
  expect(onData).not.toHaveBeenCalled();
});

test("a ready frame with resumed:false reports false", () => {
  const onReady = vi.fn();
  const s = new TerminalSocket("abc", {
    onData: () => {},
    onState: () => {},
    onReady,
  });
  s.connect(1, 1);
  FakeWS.last!.open();
  FakeWS.last!.onmessage?.({ data: '{"t":"ready","resumed":false}' });
  expect(onReady).toHaveBeenCalledWith(false);
});

test("binary frames still reach onData", () => {
  const onData = vi.fn();
  const s = new TerminalSocket("abc", { onData, onState: () => {} });
  s.connect(1, 1);
  FakeWS.last!.open();
  FakeWS.last!.onmessage?.({ data: new Uint8Array([1, 2, 3]).buffer });
  expect(onData).toHaveBeenCalledTimes(1);
  expect(Array.from(onData.mock.calls[0][0])).toEqual([1, 2, 3]);
});

test("malformed control text is swallowed", () => {
  const onData = vi.fn();
  const onReady = vi.fn();
  const s = new TerminalSocket("abc", { onData, onState: () => {}, onReady });
  s.connect(1, 1);
  FakeWS.last!.open();
  expect(() => FakeWS.last!.onmessage?.({ data: "{not json" })).not.toThrow();
  expect(onData).not.toHaveBeenCalled();
  expect(onReady).not.toHaveBeenCalled();
});

test("a control frame that is not ready is ignored", () => {
  const onData = vi.fn();
  const onReady = vi.fn();
  const s = new TerminalSocket("abc", { onData, onState: () => {}, onReady });
  s.connect(1, 1);
  FakeWS.last!.open();
  FakeWS.last!.onmessage?.({ data: '{"t":"pong"}' });
  expect(onData).not.toHaveBeenCalled();
  expect(onReady).not.toHaveBeenCalled();
});

test("retry revives the socket after it has given up", () => {
  const onGiveUp = vi.fn();
  const onState = vi.fn();
  const s = new TerminalSocket("abc", {
    onData: () => {},
    onState,
    onGiveUp,
  });
  s.connect(1, 1);
  for (let i = 0; i < 8; i++) {
    FakeWS.last!.close();
    vi.advanceTimersByTime(5000);
  }
  expect(onGiveUp).toHaveBeenCalledTimes(1);
  const dead = FakeWS.last;

  s.retry();
  expect(FakeWS.last).not.toBe(dead);
  FakeWS.last!.open();
  expect(onState).toHaveBeenLastCalledWith("open");
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
