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
  vi.restoreAllMocks();
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
  // Pin the jitter so the scheduled delay is exactly raw/2 and the doubling,
  // the 5000 cap on raw, and the reset-on-open stay observable.
  vi.spyOn(Math, "random").mockReturnValue(0);
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

  step(500); // raw 1000
  step(1000); // raw 2000
  step(2000); // raw 4000
  step(2500); // raw min(8000, 5000)
  step(2500); // raw stays capped

  FakeWS.last!.open(); // a successful open resets the backoff
  step(500);
});

test("reconnect delay is jittered inside [raw/2, raw]", () => {
  const rnd = vi.spyOn(Math, "random");
  const setT = vi.spyOn(globalThis, "setTimeout");

  for (const r of [0, 0.999999]) {
    rnd.mockReturnValue(r);
    const { bs } = make();
    bs.open();
    FakeWS.last!.open();

    // raw follows min(1000 * 2^n, 5000) with no open in between.
    for (const raw of [1000, 2000, 4000, 5000, 5000]) {
      setT.mockClear();
      const prev = FakeWS.last!;
      prev.close();
      const delay = Number(setT.mock.calls.at(-1)![1]);
      expect(delay).toBeGreaterThanOrEqual(raw / 2);
      expect(delay).toBeLessThanOrEqual(raw);
      if (r === 0) expect(delay).toBe(raw / 2);
      vi.advanceTimersByTime(raw);
      expect(FakeWS.last).not.toBe(prev);
    }
  }
});

test("onGiveUp still fires after 8 closes with jitter on", () => {
  vi.spyOn(Math, "random").mockReturnValue(0.5);
  const { bs, onGiveUp } = make();
  bs.open();
  for (let i = 0; i < 8; i++) {
    FakeWS.last!.close();
    vi.advanceTimersByTime(5000);
  }
  expect(onGiveUp).toHaveBeenCalledTimes(1);
});

test("retryNow revives the socket after it has given up", () => {
  vi.spyOn(Math, "random").mockReturnValue(0);
  const { bs, onState, onGiveUp } = make();
  bs.open();
  for (let i = 0; i < 8; i++) {
    FakeWS.last!.close();
    vi.advanceTimersByTime(5000);
  }
  expect(onGiveUp).toHaveBeenCalledTimes(1);
  const dead = FakeWS.last;

  bs.retryNow();
  expect(FakeWS.last).not.toBe(dead);
  FakeWS.last!.open();
  expect(onState).toHaveBeenLastCalledWith("open");

  // The attempt counter was reset: a single further close schedules a
  // fresh [500, 1000] retry rather than giving up again.
  onGiveUp.mockClear();
  const prev = FakeWS.last!;
  prev.close();
  expect(onGiveUp).not.toHaveBeenCalled();
  vi.advanceTimersByTime(500);
  expect(FakeWS.last).not.toBe(prev);
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
