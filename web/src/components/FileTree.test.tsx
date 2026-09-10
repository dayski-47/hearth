import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { useStore } from "../store";
import FileTree from "./FileTree";
import { retryFileEvents } from "../store/fileEvents";
import type { FileNode } from "../store/tree";

vi.mock("../store/fileEvents", () => ({ retryFileEvents: vi.fn() }));

const n = (p: string, isDir = false): FileNode => ({
  path: p,
  name: p.split("/").pop() ?? p,
  is_dir: isDir,
  size: 0,
  modified_unix: 0,
});
afterEach(() => vi.restoreAllMocks());

function seed(
  partial: Partial<ReturnType<typeof useStore.getState>["tree"]>,
) {
  useStore.setState({
    tree: {
      workspaceId: "ws",
      expanded: new Set([""]),
      children: { "": [] },
      loading: new Set(),
      error: null,
      eventsPaused: false,
      ...partial,
    },
    toggleDir: vi.fn(),
    createNode: vi.fn(),
    renameNode: vi.fn(),
    deleteNode: vi.fn(),
  });
}

test("expanding a directory calls toggleDir once", async () => {
  const toggleDir = vi.fn();
  seed({ children: { "": [n("src", true)] } });
  useStore.setState({ toggleDir });
  render(<FileTree onOpen={() => {}} />);
  await userEvent.click(screen.getByText("src"));
  expect(toggleDir).toHaveBeenCalledWith("src");
  expect(toggleDir).toHaveBeenCalledTimes(1);
});

test("clicking a file calls onOpen with its path", async () => {
  const onOpen = vi.fn();
  seed({ children: { "": [n("a.txt")] } });
  render(<FileTree onOpen={onOpen} />);
  await userEvent.click(screen.getByText("a.txt"));
  expect(onOpen).toHaveBeenCalledWith("a.txt");
});

test("the root pane shows a loading line during the first tree load", () => {
  seed({ children: {}, loading: new Set([""]) });
  render(<FileTree onOpen={() => {}} />);
  expect(screen.getByText("...")).toBeInTheDocument();
});

test("a tree error renders an alert", () => {
  seed({ error: "file is 20000000 bytes, over the 10485760 byte limit" });
  render(<FileTree onOpen={() => {}} />);
  expect(screen.getByRole("alert")).toHaveTextContent(
    "over the 10485760 byte limit",
  );
});

test("renaming a directory passes is_dir through to renameNode", async () => {
  const renameNode = vi.fn();
  seed({ children: { "": [n("src", true)] } });
  useStore.setState({ renameNode });
  render(<FileTree onOpen={() => {}} />);
  await userEvent.click(screen.getByTitle("Rename"));
  const input = screen.getByLabelText("new name");
  await userEvent.clear(input);
  await userEvent.type(input, "lib{Enter}");
  expect(renameNode).toHaveBeenCalledWith("src", "lib", true);
});

test("deleting a row confirms then calls deleteNode", async () => {
  const deleteNode = vi.fn();
  seed({ children: { "": [n("a.txt")] } });
  useStore.setState({ deleteNode });
  vi.spyOn(window, "confirm").mockReturnValue(true);
  render(<FileTree onOpen={() => {}} />);
  await userEvent.click(screen.getByTitle("Delete"));
  expect(deleteNode).toHaveBeenCalledWith("a.txt");
});

test("a paused events socket renders the bar and Reconnect calls the retry", async () => {
  seed({ eventsPaused: true });
  render(<FileTree onOpen={() => {}} />);
  expect(screen.getByText("Live updates paused.")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Reconnect" }));
  expect(retryFileEvents).toHaveBeenCalledTimes(1);
});

test("the toolbar new file button creates under the root", async () => {
  const createNode = vi.fn();
  seed({ children: { "": [] } });
  useStore.setState({ createNode });
  vi.spyOn(window, "prompt").mockReturnValue("notes.md");
  render(<FileTree onOpen={() => {}} />);
  await userEvent.click(screen.getByText("+ file"));
  expect(createNode).toHaveBeenCalledWith("", "notes.md", false);
});
