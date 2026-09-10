import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { useStore } from "./index";
import {
  applyFileEvent,
  connectFileEvents,
  disconnectFileEvents,
  retryFileEvents,
} from "./fileEvents";

type Ev = { path: string; kind: string };

const h = vi.hoisted(() => ({
  onEvent: undefined as undefined | ((e: Ev) => void),
  onState: undefined as undefined | ((s: string) => void),
  onGiveUp: undefined as undefined | (() => void),
  retry: vi.fn(),
}));

vi.mock("../api/eventsSocket", () => ({
  EventsSocket: class {
    constructor(
      _id: string,
      o: {
        onEvent: (e: Ev) => void;
        onState?: (s: string) => void;
        onGiveUp?: () => void;
      },
    ) {
      h.onEvent = o.onEvent;
      h.onState = o.onState;
      h.onGiveUp = o.onGiveUp;
    }
    connect() {}
    retry() {
      h.retry();
    }
    close() {}
  },
}));

function seed() {
  useStore.setState({
    tree: {
      workspaceId: "ws1",
      expanded: new Set([""]),
      children: { "": [] },
      loading: new Set(),
      error: null,
      eventsPaused: false,
    },
  });
}

beforeEach(seed);
afterEach(() => {
  disconnectFileEvents();
  h.retry.mockClear();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

test("CREATED refetches the cached parent so the real node type is known", () => {
  useStore.setState({
    tree: {
      ...useStore.getState().tree,
      expanded: new Set(["", "sub"]),
      children: { "": [], sub: [] },
    },
  });
  const spy = vi
    .spyOn(useStore.getState(), "treeRefetchDir")
    .mockResolvedValue();
  applyFileEvent({ path: "sub/foo", kind: "CREATED" });
  expect(spy).toHaveBeenCalledWith("sub");
});

test("CREATED under an uncached parent does not refetch and does not crash", () => {
  const fetchDir = vi.spyOn(useStore.getState(), "treeRefetchDir");
  expect(() =>
    applyFileEvent({ path: "deep/nested/foo", kind: "CREATED" }),
  ).not.toThrow();
  // treeRefetchDir is still called, but its own cache guard makes it a no-op.
  fetchDir.mockRestore();
  expect(useStore.getState().tree.children["deep/nested"]).toBeUndefined();
});

test("REMOVED drops the node from the tree", () => {
  useStore.setState({
    tree: {
      ...useStore.getState().tree,
      children: {
        "": [
          { path: "a.txt", name: "a.txt", is_dir: false, size: 0, modified_unix: 0 },
        ],
      },
    },
  });
  applyFileEvent({ path: "a.txt", kind: "REMOVED" });
  expect(useStore.getState().tree.children[""]).toEqual([]);
});

test("MODIFIED bumps modified_unix", () => {
  useStore.setState({
    tree: {
      ...useStore.getState().tree,
      children: {
        "": [
          { path: "a.txt", name: "a.txt", is_dir: false, size: 0, modified_unix: 0 },
        ],
      },
    },
  });
  applyFileEvent({ path: "a.txt", kind: "MODIFIED" });
  expect(
    useStore.getState().tree.children[""][0].modified_unix,
  ).toBeGreaterThan(0);
});

test("the sentinel frame refetches every expanded directory", () => {
  const spy = vi
    .spyOn(useStore.getState(), "treeRefetchExpanded")
    .mockResolvedValue();
  applyFileEvent({ path: "", kind: "KIND_UNSPECIFIED" });
  expect(spy).toHaveBeenCalledTimes(1);
});

function seedTab(over: Partial<import("./editor").EditorTab> = {}) {
  useStore.setState({
    editor: {
      workspaceId: "ws1",
      activePath: "a.txt",
      tabs: [
        {
          path: "a.txt",
          language: "plaintext",
          dirty: false,
          loaded: true,
          deletedOnDisk: false,
          changedOnDisk: false,
          openError: null,
          initialDoc: null,
          baseline: "",
          reloadNonce: 0,
          ...over,
        },
      ],
    },
  });
}

test("the sentinel frame also rechecks the open editor tabs", () => {
  vi.spyOn(useStore.getState(), "treeRefetchExpanded").mockResolvedValue();
  const spy = vi
    .spyOn(useStore.getState(), "recheckOpenTabs")
    .mockResolvedValue();
  applyFileEvent({ path: "", kind: "KIND_UNSPECIFIED" });
  expect(spy).toHaveBeenCalledTimes(1);
});

test("MODIFIED on a clean open tab reloads it silently", () => {
  seedTab();
  const spy = vi.spyOn(useStore.getState(), "reloadTab").mockResolvedValue();
  applyFileEvent({ path: "a.txt", kind: "MODIFIED" });
  expect(spy).toHaveBeenCalledWith("a.txt");
});

test("the watcher echo of our own save does not remount the editor", async () => {
  const realFetch = globalThis.fetch;
  seedTab({ baseline: "saved body" });
  globalThis.fetch = vi.fn(
    async () => new Response("saved body", { status: 200 }),
  ) as typeof fetch;
  applyFileEvent({ path: "a.txt", kind: "MODIFIED" });
  await new Promise((r) => setTimeout(r));
  expect(useStore.getState().editor.tabs[0].reloadNonce).toBe(0);
  globalThis.fetch = realFetch;
});

test("MODIFIED on a dirty open tab flags it changed on disk", () => {
  seedTab({ dirty: true });
  const reload = vi.spyOn(useStore.getState(), "reloadTab").mockResolvedValue();
  const changed = vi.spyOn(useStore.getState(), "markChangedOnDisk");
  applyFileEvent({ path: "a.txt", kind: "MODIFIED" });
  expect(reload).not.toHaveBeenCalled();
  expect(changed).toHaveBeenCalledWith("a.txt", true);
});

test("REMOVED on a clean open tab closes it", () => {
  seedTab();
  const close = vi.spyOn(useStore.getState(), "closeTab");
  applyFileEvent({ path: "a.txt", kind: "REMOVED" });
  expect(close).toHaveBeenCalledWith("a.txt");
});

test("REMOVED on a dirty open tab marks it deleted, keeping the tab", () => {
  seedTab({ dirty: true });
  const close = vi.spyOn(useStore.getState(), "closeTab");
  const del = vi.spyOn(useStore.getState(), "markDeleted");
  applyFileEvent({ path: "a.txt", kind: "REMOVED" });
  expect(close).not.toHaveBeenCalled();
  expect(del).toHaveBeenCalledWith("a.txt");
});

test("the first events open loads; a later open resyncs the tree and tabs", () => {
  const refetch = vi
    .spyOn(useStore.getState(), "treeRefetchExpanded")
    .mockResolvedValue();
  const recheck = vi
    .spyOn(useStore.getState(), "recheckOpenTabs")
    .mockResolvedValue();
  connectFileEvents("ws1");

  useStore.getState().setEventsPaused(true);
  h.onState!("open");
  expect(refetch).not.toHaveBeenCalled();
  expect(recheck).not.toHaveBeenCalled();
  expect(useStore.getState().tree.eventsPaused).toBe(false);

  useStore.getState().setEventsPaused(true);
  h.onState!("open");
  expect(refetch).toHaveBeenCalledTimes(1);
  expect(recheck).toHaveBeenCalledTimes(1);
  expect(useStore.getState().tree.eventsPaused).toBe(false);
});

test("onGiveUp pauses live updates", () => {
  connectFileEvents("ws1");
  h.onGiveUp!();
  expect(useStore.getState().tree.eventsPaused).toBe(true);
});

test("retryFileEvents asks the socket to retry", () => {
  connectFileEvents("ws1");
  retryFileEvents();
  expect(h.retry).toHaveBeenCalledTimes(1);
});

test("a burst of events within the debounce window is one coalesced flush", () => {
  vi.useFakeTimers();
  const refetch = vi
    .spyOn(useStore.getState(), "treeRefetchDir")
    .mockResolvedValue();
  connectFileEvents("ws1");
  expect(h.onEvent).toBeTypeOf("function");

  h.onEvent!({ path: "a.txt", kind: "CREATED" });
  h.onEvent!({ path: "b.txt", kind: "CREATED" });
  vi.advanceTimersByTime(99);
  expect(refetch).not.toHaveBeenCalled(); // timer not yet fired

  h.onEvent!({ path: "c.txt", kind: "CREATED" }); // resets the debounce
  vi.advanceTimersByTime(99);
  expect(refetch).not.toHaveBeenCalled();

  vi.advanceTimersByTime(1); // 100 ms since the last event
  expect(refetch).toHaveBeenCalledTimes(3);
});
