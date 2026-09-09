import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { useStore } from "./index";

const realFetch = globalThis.fetch;
const empty = {
  workspaceId: null,
  expanded: new Set<string>(),
  children: {},
  loading: new Set<string>(),
  error: null,
};
beforeEach(() =>
  useStore.setState({
    tree: { ...empty, expanded: new Set(), loading: new Set() },
  }),
);
afterEach(() => {
  globalThis.fetch = realFetch;
});

function mockList(entriesByPath: Record<string, unknown[]>) {
  globalThis.fetch = vi.fn(async (url: string) => {
    const u = new URL(String(url), "http://x");
    const path = u.searchParams.get("path") ?? "";
    return new Response(JSON.stringify({ entries: entriesByPath[path] ?? [] }), {
      status: 200,
    });
  }) as typeof fetch;
}
const node = (p: string, isDir = false) => ({
  path: p,
  name: p.split("/").pop() ?? p,
  is_dir: isDir,
  size: 0,
  modified_unix: 0,
});

test("openWorkspaceTree fetches the root and resets", async () => {
  mockList({ "": [node("a.txt"), node("sub", true)] });
  await useStore.getState().openWorkspaceTree("ws1");
  const t = useStore.getState().tree;
  expect(t.workspaceId).toBe("ws1");
  expect(t.children[""].map((n) => n.path)).toEqual(["sub", "a.txt"]);
});

test("toggleDir fetches children once, caches, keeps on collapse", async () => {
  mockList({ "": [node("sub", true)], sub: [node("sub/x.ts")] });
  await useStore.getState().openWorkspaceTree("ws1");
  const spy = globalThis.fetch as ReturnType<typeof vi.fn>;
  spy.mockClear();
  await useStore.getState().toggleDir("sub");
  expect(useStore.getState().tree.expanded.has("sub")).toBe(true);
  expect(useStore.getState().tree.children["sub"].map((n) => n.path)).toEqual([
    "sub/x.ts",
  ]);
  await useStore.getState().toggleDir("sub"); // collapse
  expect(useStore.getState().tree.expanded.has("sub")).toBe(false);
  await useStore.getState().toggleDir("sub"); // re-expand, no refetch
  expect(spy).toHaveBeenCalledTimes(1);
});

test("treeInsert only lands under an expanded parent", () => {
  useStore.setState({
    tree: {
      ...empty,
      expanded: new Set([""]),
      children: { "": [] },
      loading: new Set(),
    },
  });
  useStore.getState().treeInsert(node("a.txt"));
  expect(useStore.getState().tree.children[""].map((n) => n.path)).toEqual([
    "a.txt",
  ]);
  useStore.getState().treeInsert(node("hidden/b.txt")); // parent "hidden" not expanded
  expect(useStore.getState().tree.children["hidden"]).toBeUndefined();
});

test("treeRemove drops the node, its cached children, and collapses it", () => {
  useStore.setState({
    tree: {
      ...empty,
      expanded: new Set(["", "sub"]),
      children: { "": [node("sub", true)], sub: [node("sub/x.ts")] },
      loading: new Set(),
    },
  });
  useStore.getState().treeRemove("sub");
  const t = useStore.getState().tree;
  expect(t.children[""]).toEqual([]);
  expect(t.children["sub"]).toBeUndefined();
  expect(t.expanded.has("sub")).toBe(false);
});

test("treeTouch bumps modified_unix for a listed node", () => {
  useStore.setState({
    tree: {
      ...empty,
      expanded: new Set([""]),
      children: { "": [node("a.txt")] },
      loading: new Set(),
    },
  });
  useStore.getState().treeTouch("a.txt");
  expect(useStore.getState().tree.children[""][0].modified_unix).toBeGreaterThan(
    0,
  );
});

test("deleteNode calls the API then patches the tree", async () => {
  useStore.setState({
    tree: {
      workspaceId: "ws1",
      expanded: new Set([""]),
      children: { "": [node("a.txt")] },
      loading: new Set(),
      error: null,
    },
  });
  const spy = vi.fn(async () => new Response(null, { status: 204 }));
  globalThis.fetch = spy as unknown as typeof fetch;
  await useStore.getState().deleteNode("a.txt");
  expect(spy).toHaveBeenCalledTimes(1);
  expect(useStore.getState().tree.children[""]).toEqual([]);
});
