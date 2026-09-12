import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { useStore } from "../store";
import NewWorkspaceForm from "./NewWorkspaceForm";

afterEach(() => vi.restoreAllMocks());

test("opens, validates a blank name, then creates", async () => {
  const create = vi.fn().mockResolvedValue(undefined);
  useStore.setState({ createWorkspace: create });

  render(<NewWorkspaceForm />);
  await userEvent.click(screen.getByRole("button", { name: "+ New workspace" }));
  await userEvent.click(screen.getByRole("button", { name: "Create" }));
  expect(screen.getByRole("alert")).toHaveTextContent("name is required");
  expect(create).not.toHaveBeenCalled();

  await userEvent.type(screen.getByPlaceholderText("name"), "scratch");
  await userEvent.click(screen.getByRole("button", { name: "Create" }));
  expect(create).toHaveBeenCalledWith("scratch", undefined, undefined);
});

test("sends the host path when given", async () => {
  const create = vi.fn().mockResolvedValue(undefined);
  useStore.setState({ createWorkspace: create });

  render(<NewWorkspaceForm />);
  await userEvent.click(screen.getByRole("button", { name: "+ New workspace" }));
  await userEvent.type(screen.getByPlaceholderText("name"), "scratch");
  await userEvent.type(
    screen.getByPlaceholderText("host path (optional)"),
    "/home/dayson/homelab",
  );
  await userEvent.click(screen.getByRole("button", { name: "Create" }));
  expect(create).toHaveBeenCalledWith(
    "scratch",
    undefined,
    "/home/dayson/homelab",
  );
});

test("surfaces a create error", async () => {
  useStore.setState({
    createWorkspace: vi.fn().mockRejectedValue(new Error("no worker available")),
  });
  render(<NewWorkspaceForm />);
  await userEvent.click(screen.getByRole("button", { name: "+ New workspace" }));
  await userEvent.type(screen.getByPlaceholderText("name"), "x");
  await userEvent.click(screen.getByRole("button", { name: "Create" }));
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "no worker available",
  );
});
