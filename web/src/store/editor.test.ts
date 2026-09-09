import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { useStore } from "./index";

const realFetch = globalThis.fetch;

beforeEach(() =>
  useStore.setState({ editor: { workspaceId: null, tabs: [], activePath: null } }),
);
afterEach(() => {
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
});

function mockText(body: string, status = 200) {
  globalThis.fetch = vi.fn(
    async () => new Response(body, { status }),
  ) as typeof fetch;
}

test("openFile adds a tab with the initial content", async () => {
  mockText("hello");
  await useStore.getState().openFile("a.txt");
  const t = useStore.getState().editor.tabs[0];
  expect(t).toMatchObject({
    path: "a.txt",
    language: "plaintext",
    dirty: false,
    initialDoc: "hello",
    openError: null,
  });
  expect(useStore.getState().editor.activePath).toBe("a.txt");
});

test("openFile resolves the language from the path", async () => {
  mockText("x");
  await useStore.getState().openFile("src/main.rs");
  expect(useStore.getState().editor.tabs[0].language).toBe("rust");
});

test("openFile marks the tab loaded once the content arrives", async () => {
  mockText("hello");
  const t0 = useStore.getState().editor;
  expect(t0.tabs).toHaveLength(0);
  await useStore.getState().openFile("a.txt");
  expect(useStore.getState().editor.tabs[0].loaded).toBe(true);
});

test("openFile on an oversize file records openError and no editor", async () => {
  globalThis.fetch = vi.fn(
    async () =>
      new Response(
        JSON.stringify({
          error: "file is 20000000 bytes, over the 10485760 byte limit",
        }),
        { status: 400 },
      ),
  ) as typeof fetch;
  await useStore.getState().openFile("big.bin");
  const t = useStore.getState().editor.tabs[0];
  expect(t.openError).toMatch(/over the 10485760/);
  expect(t.initialDoc).toBeNull();
});

test("openFile on an already-open path just activates it, no refetch", async () => {
  mockText("x");
  await useStore.getState().openFile("a.txt");
  await useStore.getState().openFile("b.txt");
  const spy = globalThis.fetch as ReturnType<typeof vi.fn>;
  spy.mockClear();
  await useStore.getState().openFile("a.txt");
  expect(useStore.getState().editor.activePath).toBe("a.txt");
  expect(useStore.getState().editor.tabs).toHaveLength(2);
  expect(spy).not.toHaveBeenCalled();
});

test("retryOpen clears the error and re-fetches the content", async () => {
  globalThis.fetch = vi.fn(
    async () => new Response(JSON.stringify({ error: "boom" }), { status: 500 }),
  ) as typeof fetch;
  await useStore.getState().openFile("a.txt");
  expect(useStore.getState().editor.tabs[0].openError).toBe("boom");
  mockText("recovered");
  await useStore.getState().retryOpen("a.txt");
  const t = useStore.getState().editor.tabs[0];
  expect(t.openError).toBeNull();
  expect(t.initialDoc).toBe("recovered");
  expect(t.loaded).toBe(true);
});

test("closeTab removes it and picks a neighbour as active", async () => {
  mockText("x");
  await useStore.getState().openFile("a.txt");
  await useStore.getState().openFile("b.txt");
  await useStore.getState().openFile("c.txt");
  useStore.getState().setActiveTab("b.txt");
  useStore.getState().closeTab("b.txt");
  expect(useStore.getState().editor.tabs.map((t) => t.path)).toEqual([
    "a.txt",
    "c.txt",
  ]);
  expect(useStore.getState().editor.activePath).toBe("c.txt");
  useStore.getState().closeTab("c.txt");
  expect(useStore.getState().editor.activePath).toBe("a.txt");
  useStore.getState().closeTab("a.txt");
  expect(useStore.getState().editor.activePath).toBeNull();
});

test("closeTab on an inactive tab keeps the active tab", async () => {
  mockText("x");
  await useStore.getState().openFile("a.txt");
  await useStore.getState().openFile("b.txt");
  useStore.getState().closeTab("a.txt");
  expect(useStore.getState().editor.activePath).toBe("b.txt");
});

test("markDirty / markChangedOnDisk / markDeleted flip the flags", async () => {
  mockText("x");
  await useStore.getState().openFile("a.txt");
  useStore.getState().markDirty("a.txt", true);
  expect(useStore.getState().editor.tabs[0].dirty).toBe(true);
  useStore.getState().markChangedOnDisk("a.txt", true);
  expect(useStore.getState().editor.tabs[0].changedOnDisk).toBe(true);
  useStore.getState().markDeleted("a.txt");
  expect(useStore.getState().editor.tabs[0].deletedOnDisk).toBe(true);
});

test("consumeInitialDoc clears the initial content once the editor has it", async () => {
  mockText("hello");
  await useStore.getState().openFile("a.txt");
  useStore.getState().consumeInitialDoc("a.txt");
  expect(useStore.getState().editor.tabs[0].initialDoc).toBeNull();
});

test("recordSaved clears the dirty and deleted flags", async () => {
  mockText("x");
  await useStore.getState().openFile("a.txt");
  useStore.getState().markDirty("a.txt", true);
  useStore.getState().markDeleted("a.txt");
  useStore.getState().recordSaved("a.txt", "new text");
  const t = useStore.getState().editor.tabs[0];
  expect(t.dirty).toBe(false);
  expect(t.deletedOnDisk).toBe(false);
});

test("setActiveWorkspace resets the tabs when the id changes", async () => {
  mockText("x");
  useStore.getState().setActiveWorkspace("ws1");
  await useStore.getState().openFile("a.txt");
  expect(useStore.getState().editor.tabs).toHaveLength(1);
  useStore.getState().setActiveWorkspace("ws1"); // same id keeps the tabs
  expect(useStore.getState().editor.tabs).toHaveLength(1);
  useStore.getState().setActiveWorkspace("ws2"); // new id clears them
  expect(useStore.getState().editor).toMatchObject({
    workspaceId: "ws2",
    tabs: [],
    activePath: null,
  });
});

test("closeWorkspaceEditor clears the whole editor", async () => {
  mockText("x");
  useStore.getState().setActiveWorkspace("ws1");
  await useStore.getState().openFile("a.txt");
  useStore.getState().closeWorkspaceEditor();
  expect(useStore.getState().editor).toEqual({
    workspaceId: null,
    tabs: [],
    activePath: null,
  });
});
