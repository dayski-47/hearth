import { ApiError } from "./types";
export { ApiError } from "./types";

let onUnauthorized: () => void = () => {};
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn;
}

const inflight = new Map<string, Promise<unknown>>();

async function parse<T>(res: Response): Promise<T> {
  if (res.status === 204 || res.headers.get("content-length") === "0") {
    return null as T;
  }
  const text = await res.text();
  if (!text) return null as T;
  return JSON.parse(text) as T;
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {};
  const init: RequestInit = { method, credentials: "same-origin", headers };
  if (method !== "GET" && method !== "HEAD") {
    headers["X-Hearth-CSRF"] = "1";
  }
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }

  const res = await fetch(path, init);
  if (res.status === 401) {
    onUnauthorized();
  }
  if (!res.ok) {
    let message = res.statusText || `HTTP ${res.status}`;
    try {
      const j = (await res.clone().json()) as { error?: string };
      if (j?.error) message = j.error;
    } catch {
      // non-JSON body; keep the status text
    }
    throw new ApiError(res.status, message);
  }
  return parse<T>(res);
}

export const api = {
  get<T>(path: string): Promise<T> {
    const existing = inflight.get(path);
    if (existing) return existing as Promise<T>;
    const p = request<T>("GET", path).finally(() => inflight.delete(path));
    inflight.set(path, p);
    return p;
  },
  post<T>(path: string, body?: unknown): Promise<T> {
    return request<T>("POST", path, body);
  },
  del(path: string): Promise<null> {
    return request<null>("DELETE", path);
  },
};
