import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, expect, test, vi } from "vitest";
import { useStore } from "./store";
import App from "./App";

const future = { v7_startTransition: true, v7_relativeSplatPath: true } as const;

beforeEach(() => {
  useStore.setState({
    auth: { status: "authed", username: "admin" },
    checkMe: vi.fn().mockResolvedValue(undefined),
    startPolling: vi.fn(),
    stopPolling: vi.fn(),
  });
});

test("renders the dashboard route when authed", () => {
  render(
    <MemoryRouter initialEntries={["/"]} future={future}>
      <App />
    </MemoryRouter>,
  );
  expect(screen.getByRole("button", { name: "Log out" })).toBeInTheDocument();
});

test("shows the login overlay when anon", () => {
  useStore.setState({ auth: { status: "anon", username: null } });
  render(
    <MemoryRouter initialEntries={["/"]} future={future}>
      <App />
    </MemoryRouter>,
  );
  expect(screen.getByRole("button", { name: "Log in" })).toBeInTheDocument();
});
