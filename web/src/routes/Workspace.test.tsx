import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, expect, test, vi } from "vitest";
import Workspace from "./Workspace";

const future = { v7_startTransition: true, v7_relativeSplatPath: true } as const;

// xterm needs a real canvas; jsdom has none. Stub it so the pane can mount.
vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    cols = 80;
    rows = 24;
    buffer = { active: { length: 1 } };
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

// The tree + events lifecycle now runs on mount; keep it inert in these tests.
vi.mock("../store/fileEvents", () => ({
  connectFileEvents: vi.fn(),
  disconnectFileEvents: vi.fn(),
  retryFileEvents: vi.fn(),
}));

// Every mount also fires GET .../files?path=; answer it with an empty listing
// and route the workspace GET to `body`.
function stubFetch(body: unknown, status = 200) {
  globalThis.fetch = vi.fn(async (url: string) => {
    if (String(url).includes("/files")) {
      return new Response(JSON.stringify({ entries: [] }), { status: 200 });
    }
    return new Response(JSON.stringify(body), { status });
  }) as typeof fetch;
}

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
  onmessage: ((e: { data: ArrayBuffer }) => void) | null = null;
  send() {}
  close() {
    this.readyState = 3;
    this.onclose?.();
  }
}

const realFetch = globalThis.fetch;
afterEach(() => {
  globalThis.fetch = realFetch;
  vi.unstubAllGlobals();
});

function mount(id = "a") {
  return render(
    <MemoryRouter initialEntries={[`/w/${id}`]} future={future}>
      <Routes>
        <Route path="/w/:id" element={<Workspace />} />
      </Routes>
    </MemoryRouter>,
  );
}

test("running workspace renders the terminal pane", async () => {
  vi.stubGlobal("WebSocket", FakeWS as unknown as typeof WebSocket);
  stubFetch({
    id: "a",
    name: "scratch",
    image: "x",
    state: "running",
    host_id: "local",
    created_at: "2026-01-01T00:00:00Z",
  });
  mount();
  await screen.findByRole("button", { name: "Terminal" });
  expect(document.querySelector(".term-pane")).not.toBeNull();
});

test("stopped workspace disables the terminal with a hint", async () => {
  stubFetch({
    id: "a",
    name: "s",
    image: "x",
    state: "stopped",
    host_id: "local",
    created_at: "2026-01-01T00:00:00Z",
  });
  mount();
  expect(
    await screen.findByText(/Start the workspace to open a terminal/),
  ).toBeInTheDocument();
});

test("a still-creating workspace shows an alert and a link home", async () => {
  stubFetch({
    id: "a",
    name: "s",
    image: "x",
    state: "creating",
    host_id: "local",
    created_at: "2026-01-01T00:00:00Z",
  });
  mount();
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "still being created",
  );
  expect(
    screen.getByRole("link", { name: "Back to workspaces" }),
  ).toBeInTheDocument();
});

test("404 shows a message and a link home", async () => {
  stubFetch({ error: "not found" }, 404);
  mount("nope");
  expect(await screen.findByRole("alert")).toHaveTextContent("not found");
  expect(
    screen.getByRole("link", { name: "Back to workspaces" }),
  ).toBeInTheDocument();
});
