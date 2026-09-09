import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, expect, test, vi } from "vitest";
import Workspace from "./Workspace";

const future = { v7_startTransition: true, v7_relativeSplatPath: true } as const;

const realFetch = globalThis.fetch;
afterEach(() => {
  globalThis.fetch = realFetch;
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
  expect(await screen.findByText(/Terminal \(Task 7\)/)).toBeInTheDocument();
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
