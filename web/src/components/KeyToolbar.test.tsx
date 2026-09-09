import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import KeyToolbar from "./KeyToolbar";

test("Esc sends the escape sequence", async () => {
  const onKey = vi.fn();
  render(<KeyToolbar onKey={onKey} />);
  await userEvent.click(screen.getByRole("button", { name: "Esc" }));
  expect(onKey).toHaveBeenCalledWith("\x1b");
});

test("sticky Ctrl then c sends ^C and clears", async () => {
  const onKey = vi.fn();
  render(<KeyToolbar onKey={onKey} />);
  await userEvent.click(screen.getByRole("button", { name: "Ctrl" }));
  await userEvent.click(screen.getByRole("button", { name: "c" }));
  expect(onKey).toHaveBeenCalledWith("\x03");
  expect(screen.getByRole("button", { name: "Ctrl" })).not.toHaveClass("sticky");
});
