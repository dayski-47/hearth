import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { Workspace } from "../api/types";
import { useStore } from "./index";

const realFetch = globalThis.fetch;
beforeEach(() => {
  useStore.setState({ workspaces: { list: [], loading: false, error: null } });
});
afterEach(() => {
  globalThis.fetch = realFetch;
  useStore.getState().stopPolling();
  vi.useRealTimers();
});

function mock(status: number, body: unknown) {
  globalThis.fetch = vi.fn(
    async () => new Response(JSON.stringify(body), { status }),
  ) as typeof fetch;
}

const wsRunning = {
  id: "a",
  name: "scratch",
  image: "busybox",
  state: "running",
  host_id: "local",
  created_at: "2026-06-10T00:00:00Z",
} satisfies Workspace;

test("fetchWorkspaces populates the list", async () => {
  mock(200, { workspaces: [wsRunning] });
  await useStore.getState().fetchWorkspaces();
  expect(useStore.getState().workspaces.list).toHaveLength(1);
  expect(useStore.getState().workspaces.loading).toBe(false);
});

test("fetchWorkspaces records an error", async () => {
  mock(500, { error: "boom" });
  await useStore.getState().fetchWorkspaces();
  expect(useStore.getState().workspaces.error).toBe("boom");
});

test("polling refetches on the interval and stops on stopPolling", async () => {
  vi.useFakeTimers();
  const calls = { n: 0 };
  globalThis.fetch = vi.fn(async () => {
    calls.n += 1;
    return new Response(JSON.stringify({ workspaces: [] }), { status: 200 });
  }) as typeof fetch;

  useStore.getState().startPolling();
  await vi.advanceTimersByTimeAsync(0); // immediate fetch
  expect(calls.n).toBe(1);
  await vi.advanceTimersByTimeAsync(15_000); // slow tick (no transitions)
  expect(calls.n).toBe(2);
  useStore.getState().stopPolling();
  await vi.advanceTimersByTimeAsync(60_000);
  expect(calls.n).toBe(2);
});

test("stopPolling during an in-flight tick fetch does not re-arm", async () => {
  vi.useFakeTimers();
  const calls = { n: 0 };
  let releaseFetch: () => void = () => {};
  globalThis.fetch = vi.fn(async () => {
    calls.n += 1;
    await new Promise<void>((resolve) => {
      releaseFetch = resolve;
    });
    return new Response(JSON.stringify({ workspaces: [] }), { status: 200 });
  }) as typeof fetch;

  useStore.getState().startPolling();
  await vi.advanceTimersByTimeAsync(0); // immediate fetch starts (call #1)
  releaseFetch(); // let it resolve so the initial fetch settles
  await vi.advanceTimersByTimeAsync(15_000); // slow tick fires (call #2), now suspended on fetch
  expect(calls.n).toBe(2);

  useStore.getState().stopPolling(); // clears `timer` while the tick fetch is in flight
  releaseFetch(); // the suspended callback resumes; guard must skip schedule()
  await vi.advanceTimersByTimeAsync(60_000);
  expect(calls.n).toBe(2); // no re-armed tick
});

test("createWorkspace merges the new row", async () => {
  globalThis.fetch = vi.fn(
    async () =>
      new Response(JSON.stringify({ ...wsRunning, id: "new", name: "n" }), {
        status: 201,
      }),
  ) as typeof fetch;
  await useStore.getState().createWorkspace("n");
  expect(useStore.getState().workspaces.list.map((w) => w.id)).toContain("new");
});

test("createWorkspace on 502 still merges the error row, then rethrows nothing", async () => {
  globalThis.fetch = vi.fn(
    async () =>
      new Response(JSON.stringify({ ...wsRunning, id: "e", state: "error" }), {
        status: 502,
      }),
  ) as typeof fetch;
  await useStore.getState().createWorkspace("n");
  const row = useStore.getState().workspaces.list.find((w) => w.id === "e");
  expect(row?.state).toBe("error");
});

test("createWorkspace on 400 throws for the caller", async () => {
  globalThis.fetch = vi.fn(
    async () =>
      new Response(JSON.stringify({ error: "name is required" }), {
        status: 400,
      }),
  ) as typeof fetch;
  await expect(useStore.getState().createWorkspace("")).rejects.toThrow(
    "name is required",
  );
});

test("createWorkspace sends host_mount_path when given", async () => {
  const fetchMock = vi.fn(
    async () =>
      new Response(JSON.stringify({ ...wsRunning, id: "hm" }), {
        status: 201,
      }),
  );
  globalThis.fetch = fetchMock as typeof fetch;
  await useStore
    .getState()
    .createWorkspace("n", undefined, "/home/dayson/homelab");
  const [, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
  const body = JSON.parse(String(init?.body));
  expect(body.host_mount_path).toBe("/home/dayson/homelab");
});

test("startWorkspace merges the returned running row", async () => {
  useStore.setState({
    workspaces: {
      list: [{ ...wsRunning, state: "stopped" }],
      loading: false,
      error: null,
    },
  });
  globalThis.fetch = vi.fn(
    async () => new Response(JSON.stringify({ ...wsRunning }), { status: 200 }),
  ) as typeof fetch;
  await useStore.getState().startWorkspace("a");
  expect(useStore.getState().workspaces.list[0].state).toBe("running");
});

test("destroyWorkspace removes the row on 204 and on 404", async () => {
  useStore.setState({
    workspaces: { list: [wsRunning], loading: false, error: null },
  });
  globalThis.fetch = vi.fn(
    async () => new Response(null, { status: 204 }),
  ) as typeof fetch;
  await useStore.getState().destroyWorkspace("a");
  expect(useStore.getState().workspaces.list).toHaveLength(0);

  useStore.setState({
    workspaces: { list: [wsRunning], loading: false, error: null },
  });
  globalThis.fetch = vi.fn(
    async () =>
      new Response(JSON.stringify({ error: "not found" }), { status: 404 }),
  ) as typeof fetch;
  await useStore.getState().destroyWorkspace("a");
  expect(useStore.getState().workspaces.list).toHaveLength(0);
});
