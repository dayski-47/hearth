import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { expect, test } from "vitest";
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
