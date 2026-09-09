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
  globalThis.fetch = vi.fn(
    async () =>
      new Response(
        JSON.stringify({
          id: "a",
          name: "scratch",
          image: "x",
          state: "running",
          host_id: "local",
          created_at: "2026-01-01T00:00:00Z",
        }),
        { status: 200 },
      ),
  ) as typeof fetch;
  mount();
  await screen.findByRole("button", { name: "Terminal" });
  expect(document.querySelector(".term-pane")).not.toBeNull();
});

test("stopped workspace disables the terminal with a hint", async () => {
  globalThis.fetch = vi.fn(
    async () =>
      new Response(
        JSON.stringify({
          id: "a",
          name: "s",
          image: "x",
          state: "stopped",
          host_id: "local",
          created_at: "2026-01-01T00:00:00Z",
        }),
        { status: 200 },
      ),
  ) as typeof fetch;
  mount();
  expect(
    await screen.findByText(/Start the workspace to open a terminal/),
  ).toBeInTheDocument();
});

test("a still-creating workspace shows an alert and a link home", async () => {
  globalThis.fetch = vi.fn(
    async () =>
      new Response(
        JSON.stringify({
          id: "a",
          name: "s",
          image: "x",
          state: "creating",
          host_id: "local",
          created_at: "2026-01-01T00:00:00Z",
        }),
        { status: 200 },
      ),
  ) as typeof fetch;
  mount();
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "still being created",
  );
  expect(
    screen.getByRole("link", { name: "Back to workspaces" }),
  ).toBeInTheDocument();
});

test("404 shows a message and a link home", async () => {
  globalThis.fetch = vi.fn(
    async () =>
      new Response(JSON.stringify({ error: "not found" }), { status: 404 }),
  ) as typeof fetch;
  mount("nope");
  expect(await screen.findByRole("alert")).toHaveTextContent("not found");
  expect(
    screen.getByRole("link", { name: "Back to workspaces" }),
  ).toBeInTheDocument();
});
