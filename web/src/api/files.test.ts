import { afterEach, expect, test, vi } from "vitest";
import { ApiError } from "./types";
import { readFileContent, writeFileContent } from "./files";

const realFetch = globalThis.fetch;
afterEach(() => {
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
});

function mockFetch(handler: (url: string, init?: RequestInit) => Response) {
  globalThis.fetch = vi.fn(
    async (url: string | URL | Request, init?: RequestInit) =>
      handler(String(url), init),
  ) as typeof fetch;
}

test("readFileContent returns the body text on 200", async () => {
  mockFetch((url, init) => {
    expect(url).toBe(
      "/api/workspaces/ws1/files/content?path=src%2Fmain.rs",
    );
    expect(init?.credentials).toBe("same-origin");
    return new Response("fn main() {}", { status: 200 });
  });
  await expect(readFileContent("ws1", "src/main.rs")).resolves.toBe(
    "fn main() {}",
  );
});

test("readFileContent throws ApiError with the {error} message on 400", async () => {
  mockFetch(
    () =>
      new Response(JSON.stringify({ error: "file is 9 bytes, over the limit" }), {
        status: 400,
      }),
  );
  await expect(readFileContent("ws1", "big.bin")).rejects.toMatchObject({
    status: 400,
    message: "file is 9 bytes, over the limit",
  } satisfies Partial<ApiError>);
});

test("writeFileContent PUTs the body with the CSRF and octet-stream headers", async () => {
  mockFetch((url, init) => {
    expect(url).toBe("/api/workspaces/ws1/files/content?path=a.txt");
    expect(init?.method).toBe("PUT");
    expect(init?.credentials).toBe("same-origin");
    const headers = init?.headers as Record<string, string>;
    expect(headers["X-Hearth-CSRF"]).toBe("1");
    expect(headers["Content-Type"]).toBe("application/octet-stream");
    expect(init?.body).toBe("hello");
    return new Response(null, { status: 200 });
  });
  await expect(writeFileContent("ws1", "a.txt", "hello")).resolves.toBeUndefined();
});

test("writeFileContent throws ApiError with the message on 404", async () => {
  mockFetch(
    () => new Response(JSON.stringify({ error: "no such file" }), { status: 404 }),
  );
  await expect(
    writeFileContent("ws1", "gone.txt", "x"),
  ).rejects.toMatchObject({
    status: 404,
    message: "no such file",
  } satisfies Partial<ApiError>);
});

test("fail falls back to statusText for a non-JSON body", async () => {
  mockFetch(
    () => new Response("nope", { status: 500, statusText: "Internal Server Error" }),
  );
  await expect(readFileContent("ws1", "a.txt")).rejects.toMatchObject({
    status: 500,
    message: "Internal Server Error",
  } satisfies Partial<ApiError>);
});
