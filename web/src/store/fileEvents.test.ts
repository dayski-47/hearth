import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { useStore } from "./index";
import {
  applyFileEvent,
  connectFileEvents,
  disconnectFileEvents,
} from "./fileEvents";

type Ev = { path: string; kind: string };

const h = vi.hoisted(() => ({
  onEvent: undefined as undefined | ((e: Ev) => void),
}));

vi.mock("../api/eventsSocket", () => ({
  EventsSocket: class {
    constructor(_id: string, o: { onEvent: (e: Ev) => void }) {
      h.onEvent = o.onEvent;
    }
    connect() {}
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
    },
  });
}

beforeEach(seed);
afterEach(() => {
  disconnectFileEvents();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

test("CREATED inserts a node under an expanded parent", () => {
  applyFileEvent({ path: "a.txt", kind: "CREATED" });
  expect(useStore.getState().tree.children[""].map((n) => n.path)).toEqual([
    "a.txt",
  ]);
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

test("a burst of events within the debounce window is one coalesced flush", () => {
  vi.useFakeTimers();
  const insert = vi.spyOn(useStore.getState(), "treeInsert");
  connectFileEvents("ws1");
  expect(h.onEvent).toBeTypeOf("function");

  h.onEvent!({ path: "a.txt", kind: "CREATED" });
  h.onEvent!({ path: "b.txt", kind: "CREATED" });
  vi.advanceTimersByTime(99);
  expect(insert).not.toHaveBeenCalled(); // timer not yet fired

  h.onEvent!({ path: "c.txt", kind: "CREATED" }); // resets the debounce
  vi.advanceTimersByTime(99);
  expect(insert).not.toHaveBeenCalled();

  vi.advanceTimersByTime(1); // 100 ms since the last event
  expect(insert).toHaveBeenCalledTimes(3);
});
