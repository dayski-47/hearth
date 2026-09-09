import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { expect, test, vi } from "vitest";
import PaneSwitcher from "./PaneSwitcher";

test("marks the active tab and reports a change", async () => {
  const onChange = vi.fn();
  render(<PaneSwitcher pane="editor" onChange={onChange} />);
  expect(screen.getByRole("tab", { name: "Editor" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await userEvent.click(screen.getByRole("tab", { name: "Terminal" }));
  expect(onChange).toHaveBeenCalledWith("terminal");
});
