import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { BackoffSocket } from "./backoffSocket";

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
  sent: unknown[] = [];
  constructor(url: string) {
    this.url = url;
    FakeWS.last = this;
  }
  send(d: unknown) {
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
  vi.useFakeTimers();
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

function make(
  overrides: Partial<ConstructorParameters<typeof BackoffSocket>[1]> = {},
) {
  const onState = vi.fn();
  const onMessage = vi.fn();
  const onGiveUp = vi.fn();
  const bs = new BackoffSocket(() => "wss://h/x", {
    onState,
    onMessage,
    onGiveUp,
    ...overrides,
  });
  return { bs, onState, onMessage, onGiveUp };
}

test("open builds the url and reports connecting then open", () => {
  const { bs, onState } = make();
  bs.open();
  expect(FakeWS.last?.url).toBe("wss://h/x");
  expect(onState).toHaveBeenCalledWith("connecting");
  FakeWS.last!.open();
  expect(onState).toHaveBeenCalledWith("open");
});

test("binaryType is applied when given", () => {
  const { bs } = make({ binaryType: "arraybuffer" });
  bs.open();
  expect(FakeWS.last?.binaryType).toBe("arraybuffer");
});

test("onMessage forwards frames", () => {
  const { bs, onMessage } = make();
  bs.open();
  FakeWS.last!.open();
  FakeWS.last!.onmessage?.({ data: "hi" });
  expect(onMessage).toHaveBeenCalledWith({ data: "hi" });
});

test("an unexpected close reconnects after the backoff delay", () => {
  const { bs } = make();
  bs.open();
  FakeWS.last!.open();
  const first = FakeWS.last;
  FakeWS.last!.close();
  vi.advanceTimersByTime(1000);
  expect(FakeWS.last).not.toBe(first);
});

test("backoff doubles, caps at 5000, and resets after an open", () => {
  const { bs } = make();
  bs.open();
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

test("onGiveUp fires once after 8 failed closes and no more sockets are made", () => {
  const { bs, onGiveUp } = make();
  bs.open();
  for (let i = 0; i < 8; i++) {
    FakeWS.last!.close();
    vi.advanceTimersByTime(5000);
  }
  const settled = FakeWS.last;
  expect(onGiveUp).toHaveBeenCalledTimes(1);
  vi.advanceTimersByTime(60_000);
  expect(FakeWS.last).toBe(settled);
});

test("close stops it reconnecting", () => {
  const { bs } = make();
  bs.open();
  FakeWS.last!.open();
  const first = FakeWS.last;
  bs.close();
  vi.advanceTimersByTime(10_000);
  expect(FakeWS.last).toBe(first);
});
