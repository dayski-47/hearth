import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { useStore } from "../store";
import Dashboard from "./Dashboard";

const future = { v7_startTransition: true, v7_relativeSplatPath: true } as const;

const ws = (id: string, state: "running" | "stopped") => ({
  id,
  name: `ws-${id}`,
  image: "busybox:stable",
  state,
  host_id: "local",
  created_at: new Date().toISOString(),
});

beforeEach(() => {
  useStore.setState({
    auth: { status: "authed", username: "admin" },
    workspaces: { list: [], loading: false, error: null },
    logout: vi.fn(),
    startPolling: vi.fn(),
    stopPolling: vi.fn(),
    fetchWorkspaces: vi.fn().mockResolvedValue(undefined),
    startWorkspace: vi.fn(),
    stopWorkspace: vi.fn(),
    destroyWorkspace: vi.fn(),
  });
});
afterEach(() => vi.restoreAllMocks());

function mount() {
  return render(
    <MemoryRouter future={future}>
      <Dashboard />
    </MemoryRouter>,
  );
}

test("empty list renders the header and the empty hint", () => {
  mount();
  expect(screen.getByText("Hearth")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Log out" })).toBeInTheDocument();
  expect(screen.getByText("No workspaces yet.")).toBeInTheDocument();
});

test("drives polling on mount and stops on unmount", () => {
  const start = vi.fn();
  const stop = vi.fn();
  useStore.setState({ startPolling: start, stopPolling: stop });
  const { unmount } = mount();
  expect(start).toHaveBeenCalled();
  unmount();
  expect(stop).toHaveBeenCalled();
});

test("renders a card per workspace", () => {
  useStore.setState({
    workspaces: {
      list: [ws("a", "running"), ws("b", "stopped")],
      loading: false,
      error: null,
    },
  });
  mount();
  expect(screen.getByText("ws-a")).toBeInTheDocument();
  expect(screen.getByText("ws-b")).toBeInTheDocument();
  expect(screen.queryByText("No workspaces yet.")).toBeNull();
});

test("a visibilitychange event does not crash", () => {
  const start = vi.fn();
  useStore.setState({ startPolling: start });
  mount();
  document.dispatchEvent(new Event("visibilitychange"));
  expect(start).toHaveBeenCalledTimes(2); // once on mount, once on the event
});
