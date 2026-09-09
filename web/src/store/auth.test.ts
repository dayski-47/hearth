import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { useStore } from "./index";

const realFetch = globalThis.fetch;
beforeEach(() => {
  useStore.setState({ auth: { status: "unknown", username: null } });
});
afterEach(() => {
  globalThis.fetch = realFetch;
});

function mock(status: number, body: unknown) {
  globalThis.fetch = vi.fn(
    async () =>
      new Response(body === null ? null : JSON.stringify(body), { status }),
  ) as typeof fetch;
}

test("checkMe sets authed on 200", async () => {
  mock(200, { username: "admin" });
  await useStore.getState().checkMe();
  expect(useStore.getState().auth).toEqual({ status: "authed", username: "admin" });
});

test("checkMe sets anon on 401", async () => {
  mock(401, null);
  await useStore.getState().checkMe();
  expect(useStore.getState().auth.status).toBe("anon");
});

test("login success flips to authed", async () => {
  mock(200, { username: "admin" });
  await useStore.getState().login("admin", "pw");
  expect(useStore.getState().auth).toEqual({ status: "authed", username: "admin" });
});

test("login failure throws and stays anon", async () => {
  mock(401, { error: "bad credentials" });
  await expect(useStore.getState().login("admin", "x")).rejects.toThrow(
    "bad credentials",
  );
  expect(useStore.getState().auth.status).not.toBe("authed");
});

test("logout flips to anon", async () => {
  useStore.setState({ auth: { status: "authed", username: "admin" } });
  mock(204, null);
  await useStore.getState().logout();
  expect(useStore.getState().auth.status).toBe("anon");
});
