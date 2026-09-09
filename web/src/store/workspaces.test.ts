import { afterEach, beforeEach, expect, test, vi } from "vitest";
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
};

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
