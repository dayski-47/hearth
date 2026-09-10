import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import ReconnectBar from "./ReconnectBar";

test("hidden renders nothing", () => {
  const { container } = render(<ReconnectBar state={{ kind: "hidden" }} />);
  expect(container).toBeEmptyDOMElement();
});

test("reconnecting shows the muted status", () => {
  render(<ReconnectBar state={{ kind: "reconnecting" }} />);
  const bar = screen.getByText("Reconnecting...");
  expect(bar).toHaveClass("muted");
  expect(bar).not.toHaveClass("err");
});

test("reconnected shows the muted status", () => {
  render(<ReconnectBar state={{ kind: "reconnected" }} />);
  expect(screen.getByText("Reconnected")).toHaveClass("muted");
});

test("fresh shows the timeout text and a dismiss control", async () => {
  const onDismiss = vi.fn();
  render(<ReconnectBar state={{ kind: "fresh", onDismiss }} />);
  expect(screen.getByText(/this is a new shell/i)).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "dismiss" }));
  expect(onDismiss).toHaveBeenCalledOnce();
});

test("paused shows the label, a Reconnect button and error styling", async () => {
  const onReconnect = vi.fn();
  render(
    <ReconnectBar
      state={{ kind: "paused", label: "Connection lost.", onReconnect }}
    />,
  );
  expect(screen.getByText("Connection lost.").closest("div")).toHaveClass("err");
  await userEvent.click(screen.getByRole("button", { name: "Reconnect" }));
  expect(onReconnect).toHaveBeenCalledOnce();
});

test("gaveup shows the label, a Reconnect button and error styling", async () => {
  const onReconnect = vi.fn();
  render(
    <ReconnectBar
      state={{ kind: "gaveup", label: "Disconnected.", onReconnect }}
    />,
  );
  expect(screen.getByText("Disconnected.").closest("div")).toHaveClass("err");
  await userEvent.click(screen.getByRole("button", { name: "Reconnect" }));
  expect(onReconnect).toHaveBeenCalledOnce();
});
