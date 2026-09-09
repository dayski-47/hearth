import { afterEach, expect, test, vi } from "vitest";
import { ApiError, api, setUnauthorizedHandler } from "./client";

const realFetch = globalThis.fetch;
afterEach(() => {
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
});

function mockFetch(handler: (url: string, init?: RequestInit) => Response) {
  globalThis.fetch = vi.fn(async (url: string | URL | Request, init?: RequestInit) =>
    handler(String(url), init),
  ) as typeof fetch;
}

test("get parses JSON and sends credentials", async () => {
  mockFetch((_url, init) => {
    expect(init?.credentials).toBe("same-origin");
    return new Response(JSON.stringify({ ok: 1 }), { status: 200 });
  });
  await expect(api.get("/api/thing")).resolves.toEqual({ ok: 1 });
});

test("post sends the CSRF header and body", async () => {
  mockFetch((_url, init) => {
    expect((init?.headers as Record<string, string>)["X-Hearth-CSRF"]).toBe("1");
    expect(init?.body).toBe(JSON.stringify({ a: 1 }));
    return new Response(null, { status: 204 });
  });
  await expect(api.post("/api/thing", { a: 1 })).resolves.toBeNull();
});

test("non-2xx throws ApiError with the server message", async () => {
  mockFetch(() => new Response(JSON.stringify({ error: "nope" }), { status: 400 }));
  await expect(api.get("/api/thing")).rejects.toMatchObject({
    status: 400,
    message: "nope",
  } satisfies Partial<ApiError>);
});

test("401 fires the unauthorized handler once", async () => {
  const spy = vi.fn();
  setUnauthorizedHandler(spy);
  mockFetch(() => new Response(null, { status: 401 }));
  await expect(api.get("/api/me")).rejects.toBeInstanceOf(ApiError);
  expect(spy).toHaveBeenCalledTimes(1);
  setUnauthorizedHandler(() => {});
});

test("concurrent GETs to the same path share one request", async () => {
  let calls = 0;
  mockFetch(() => {
    calls += 1;
    return new Response(JSON.stringify({ n: calls }), { status: 200 });
  });
  const [a, b] = await Promise.all([api.get("/api/x"), api.get("/api/x")]);
  expect(calls).toBe(1);
  expect(a).toEqual(b);
});
