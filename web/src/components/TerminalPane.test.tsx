import { render, screen, act, fireEvent } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import TerminalPane from "./TerminalPane";
import { TerminalSocket } from "../api/terminalSocket";

const { resetSpy } = vi.hoisted(() => ({ resetSpy: vi.fn() }));

// xterm needs a real canvas; jsdom has none. Stub it so the pane can mount.
// `reset` is shared across instances so a test can assert on the screen clear.
vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    cols = 80;
    rows = 24;
    buffer = { active: { length: 1 } };
    reset = resetSpy;
    loadAddon() {}
    open() {}
    write() {}
    writeln() {}
    onData() {
      return { dispose() {} };
    }
    dispose() {}
  },
}));
vi.mock("@xterm/addon-fit", () => ({
  FitAddon: class {
    fit() {}
  },
}));

const sockets: FakeWS[] = [];

class FakeWS {
  static OPEN = 1;
  static CONNECTING = 0;
  static CLOSING = 2;
  static CLOSED = 3;
  readyState = 0;
  binaryType = "";
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((e: { data: unknown }) => void) | null = null;
  constructor() {
    sockets.push(this);
  }
  send() {}
  close() {
    this.readyState = 3;
    this.onclose?.();
  }
}

const latest = () => sockets[sockets.length - 1];
const advance = (ms: number) => act(() => void vi.advanceTimersByTime(ms));
const open = () =>
  act(() => {
    const s = latest();
    s.readyState = 1;
    s.onopen?.();
  });
const ready = (resumed: boolean) =>
  act(() =>
    latest().onmessage?.({ data: JSON.stringify({ t: "ready", resumed }) }),
  );
const drop = () => act(() => latest().onclose?.());

beforeEach(() => {
  vi.useFakeTimers();
  vi.spyOn(Math, "random").mockReturnValue(0);
  vi.stubGlobal("WebSocket", FakeWS as unknown as typeof WebSocket);
  sockets.length = 0;
  resetSpy.mockClear();
});

afterEach(() => {
  vi.runOnlyPendingTimers();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

function mount() {
  return render(<TerminalPane workspaceId="a" visible={true} />);
}

test("resets the screen on a reconnect open but not the first open", () => {
  mount();
  open();
  expect(resetSpy).not.toHaveBeenCalled();

  drop();
  advance(600); // backoff timer -> a fresh connecting socket
  open();
  expect(resetSpy).toHaveBeenCalledOnce();
});

test("a reconnect into a fresh shell shows the notice until dismissed", () => {
  mount();
  open();

  drop();
  advance(600);
  open();
  ready(false);

  expect(screen.getByText(/this is a new shell/i)).toBeInTheDocument();
  act(() => fireEvent.click(screen.getByRole("button", { name: "dismiss" })));
  expect(screen.queryByText(/this is a new shell/i)).not.toBeInTheDocument();
});

test("a later reconnect clears a stale fresh-shell notice", () => {
  mount();
  open();

  // First drop times out into a fresh shell; the user does not dismiss it.
  drop();
  advance(600);
  open();
  ready(false);
  expect(screen.getByText(/this is a new shell/i)).toBeInTheDocument();

  // A second blip: the next connecting cycle must drop the stale notice and
  // show the reconnecting bar instead.
  drop();
  advance(600);
  advance(1000);
  expect(screen.queryByText(/this is a new shell/i)).not.toBeInTheDocument();
  expect(screen.getByText("Reconnecting...")).toBeInTheDocument();
});

test("a resumed reconnect shows Reconnected and clears after the timeout", () => {
  mount();
  open();

  drop();
  advance(600);
  open();
  ready(true);

  expect(screen.getByText("Reconnected")).toBeInTheDocument();
  advance(2000);
  expect(screen.queryByText("Reconnected")).not.toBeInTheDocument();
});

test("first-connect ready does not raise the fresh-shell notice", () => {
  mount();
  open();
  ready(false);
  expect(screen.queryByText(/this is a new shell/i)).not.toBeInTheDocument();
});

test("giving up shows Disconnected with a working Reconnect button", () => {
  const retry = vi
    .spyOn(TerminalSocket.prototype, "retry")
    .mockImplementation(() => {});
  mount();
  open();

  // BackoffSocket gives up after MAX_ATTEMPTS closes with no open between.
  for (let i = 0; i < 10; i++) {
    act(() => latest().onclose?.());
    advance(6000);
  }

  const button = screen.getByRole("button", { name: "Reconnect" });
  expect(screen.getByText("Disconnected.")).toBeInTheDocument();
  act(() => fireEvent.click(button));
  expect(retry).toHaveBeenCalled();
});

test("the reconnecting bar waits out a sub-second blip", () => {
  mount();
  open();

  drop();
  advance(600); // spawn a new connecting socket; bar is delayed 1000ms
  expect(screen.queryByText("Reconnecting...")).not.toBeInTheDocument();

  advance(1000);
  expect(screen.getByText("Reconnecting...")).toBeInTheDocument();
});

test("a quick reopen never flashes the reconnecting bar", () => {
  mount();
  open();

  drop();
  advance(600); // connecting again
  open(); // opens before the 1000ms delay elapses
  advance(2000);
  expect(screen.queryByText("Reconnecting...")).not.toBeInTheDocument();
});
