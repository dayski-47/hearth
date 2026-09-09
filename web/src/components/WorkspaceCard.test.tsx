import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { expect, test, vi } from "vitest";
import { useStore } from "../store";
import WorkspaceCard from "./WorkspaceCard";

const future = { v7_startTransition: true, v7_relativeSplatPath: true } as const;

const base = {
  id: "a",
  name: "scratch",
  image: "busybox:stable",
  host_id: "local",
  created_at: new Date().toISOString(),
};

test("running workspace shows an Open link to its route", () => {
  render(
    <MemoryRouter future={future}>
      <WorkspaceCard ws={{ ...base, state: "running" }} />
    </MemoryRouter>,
  );
  expect(screen.getByRole("link", { name: "Open" })).toHaveAttribute(
    "href",
    "/w/a",
  );
});

test("stopped workspace shows no Open link", () => {
  render(
    <MemoryRouter future={future}>
      <WorkspaceCard ws={{ ...base, state: "stopped" }} />
    </MemoryRouter>,
  );
  expect(screen.queryByRole("link", { name: "Open" })).toBeNull();
});

test("stopped workspace: Start calls the action, Destroy is present", async () => {
  const start = vi.fn();
  useStore.setState({
    startWorkspace: start,
    stopWorkspace: vi.fn(),
    destroyWorkspace: vi.fn(),
  });
  render(
    <MemoryRouter future={future}>
      <WorkspaceCard ws={{ ...base, state: "stopped" }} />
    </MemoryRouter>,
  );
  await userEvent.click(screen.getByRole("button", { name: "Start" }));
  expect(start).toHaveBeenCalledWith("a");
  expect(
    screen.getByRole("button", { name: "Destroy" }),
  ).toBeInTheDocument();
});

test("creating workspace: actions are disabled, no Destroy", () => {
  useStore.setState({
    startWorkspace: vi.fn(),
    stopWorkspace: vi.fn(),
    destroyWorkspace: vi.fn(),
  });
  render(
    <MemoryRouter future={future}>
      <WorkspaceCard ws={{ ...base, state: "creating" }} />
    </MemoryRouter>,
  );
  expect(screen.getByRole("button", { name: /creating/ })).toBeDisabled();
  expect(screen.queryByRole("button", { name: "Destroy" })).toBeNull();
});
