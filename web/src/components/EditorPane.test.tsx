import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { useStore } from "../store";
import type { EditorTab } from "../store/editor";
import EditorPane from "./EditorPane";
import { writeFileContent } from "../api/files";

vi.mock("../api/files", async (importActual) => ({
  ...(await importActual<typeof import("../api/files")>()),
  writeFileContent: vi.fn(async () => {}),
}));

const tab = (over: Partial<EditorTab> = {}): EditorTab => ({
  path: "src/main.ts",
  language: "typescript",
  dirty: false,
  loaded: true,
  deletedOnDisk: false,
  changedOnDisk: false,
  openError: null,
  initialDoc: "const a = 1;\n",
  ...over,
});

function seed(tabs: EditorTab[], activePath = tabs[0]?.path ?? null) {
  useStore.setState({ editor: { workspaceId: "ws1", tabs, activePath } });
}

// Mount and let the lazy import, the editor effect, the `onReady` store write,
// and the 150 ms dirty debounce all settle inside `act` so output stays clean.
async function mountWithEditor() {
  // Wrapping `render` in an async `act` keeps the lazy-import resolution, the
  // editor mount effect, and the `onReady` store write inside `act`.
  await act(async () => {
    render(<EditorPane />);
  });
  await waitFor(() =>
    expect(document.querySelector(".cm-content")).not.toBeNull(),
  );
  // Let the 150 ms dirty debounce elapse so no timer fires after the test.
  await act(async () => {
    await new Promise((r) => setTimeout(r, 200));
  });
}

beforeEach(() => {
  useStore.setState({
    editor: { workspaceId: null, tabs: [], activePath: null },
  });
});
afterEach(() => vi.clearAllMocks());

test("empty editor invites opening a file", () => {
  seed([]);
  render(<EditorPane />);
  expect(screen.getByText("Open a file from the tree.")).toBeInTheDocument();
});

test("an open error shows an alert with a Retry button instead of an editor", () => {
  seed([
    tab({
      loaded: false,
      initialDoc: null,
      openError: "file is 20000000 bytes, over the 10485760 byte limit",
    }),
  ]);
  render(<EditorPane />);
  expect(screen.getByRole("alert")).toHaveTextContent("over the 10485760");
  expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  expect(document.querySelector(".cm-editor")).toBeNull();
});

test("an open tab shows the basename and mounts CodeMirror", async () => {
  seed([tab()]);
  await mountWithEditor();
  expect(screen.getByRole("tab", { name: /main\.ts/ })).toBeInTheDocument();
  expect(document.querySelector(".cm-content")?.textContent).toContain(
    "const a = 1;",
  );
  // The store releases the initial content once the editor has taken it.
  expect(useStore.getState().editor.tabs[0].initialDoc).toBeNull();
});

test("a dirty tab shows the unsaved dot", async () => {
  seed([tab({ dirty: true })]);
  await mountWithEditor();
  expect(document.querySelector(".et-dot")).not.toBeNull();
});

test("edit -> dirty -> Ctrl-S -> write -> clean is the full cycle", async () => {
  seed([tab()]);
  await mountWithEditor();
  const content = document.querySelector(".cm-content") as HTMLElement;
  content.focus();

  await userEvent.type(content, "x");
  await waitFor(() =>
    expect(useStore.getState().editor.tabs[0].dirty).toBe(true),
  );
  expect(document.querySelector(".et-dot")).not.toBeNull();

  await userEvent.keyboard("{Control>}s{/Control}");
  await waitFor(() => expect(writeFileContent).toHaveBeenCalledTimes(1));
  // The edited buffer, not the original, is what gets written.
  expect(writeFileContent).toHaveBeenCalledWith(
    "ws1",
    "src/main.ts",
    expect.stringMatching(/^xconst a = 1;/),
  );

  await waitFor(() =>
    expect(useStore.getState().editor.tabs[0].dirty).toBe(false),
  );
  await waitFor(() =>
    expect(document.querySelector(".et-dot")).toBeNull(),
  );
});

test("a failed save keeps the tab dirty and warns the user", async () => {
  const alertSpy = vi.spyOn(window, "alert").mockImplementation(() => {});
  vi.mocked(writeFileContent).mockRejectedValueOnce(new Error("disk full"));
  seed([tab()]);
  await mountWithEditor();
  const content = document.querySelector(".cm-content") as HTMLElement;
  content.focus();

  await userEvent.type(content, "x");
  await waitFor(() =>
    expect(useStore.getState().editor.tabs[0].dirty).toBe(true),
  );

  await userEvent.keyboard("{Control>}s{/Control}");
  await waitFor(() => expect(writeFileContent).toHaveBeenCalledTimes(1));
  await waitFor(() =>
    expect(alertSpy).toHaveBeenCalledWith(
      expect.stringContaining("Could not save main.ts"),
    ),
  );

  // The write rejected: the dirty marker and the dot must survive so the
  // edits are not silently dropped on close.
  await act(async () => {
    await new Promise((r) => setTimeout(r, 200));
  });
  expect(useStore.getState().editor.tabs[0].dirty).toBe(true);
  expect(document.querySelector(".et-dot")).not.toBeNull();
  alertSpy.mockRestore();
});

test("the close button removes the tab", async () => {
  seed([tab({ path: "a.txt", initialDoc: "a", language: "plaintext" })]);
  await mountWithEditor();
  await userEvent.click(screen.getByRole("button", { name: "Close a.txt" }));
  expect(useStore.getState().editor.tabs).toHaveLength(0);
});
