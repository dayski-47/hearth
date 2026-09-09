import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { EventsSocket } from "./eventsSocket";

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
  constructor(url: string) {
    this.url = url;
    FakeWS.last = this;
  }
  send() {}
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

test("connect opens the wss events url", () => {
  const s = new EventsSocket("x", { onEvent: () => {}, onState: () => {} });
  s.connect();
  expect(FakeWS.last?.url).toBe("wss://h/api/workspaces/x/events");
});

test("a JSON text frame is delivered as a FileEvent", () => {
  const onEvent = vi.fn();
  const s = new EventsSocket("x", { onEvent, onState: () => {} });
  s.connect();
  FakeWS.last!.open();
  FakeWS.last!.onmessage?.({
    data: JSON.stringify({ path: "a.txt", kind: "MODIFIED" }),
  });
  expect(onEvent).toHaveBeenCalledWith({ path: "a.txt", kind: "MODIFIED" });
});

test("a garbage frame is ignored", () => {
  const onEvent = vi.fn();
  const s = new EventsSocket("x", { onEvent, onState: () => {} });
  s.connect();
  FakeWS.last!.open();
  FakeWS.last!.onmessage?.({ data: "not json {" });
  FakeWS.last!.onmessage?.({ data: JSON.stringify({ path: "a.txt" }) });
  expect(onEvent).not.toHaveBeenCalled();
});

test("close stops reconnection", () => {
  const s = new EventsSocket("x", { onEvent: () => {}, onState: () => {} });
  s.connect();
  FakeWS.last!.open();
  const first = FakeWS.last;
  s.close();
  vi.advanceTimersByTime(10_000);
  expect(FakeWS.last).toBe(first);
});
