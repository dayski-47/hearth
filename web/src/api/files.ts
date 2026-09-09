import { ApiError } from "./types";

async function fail(res: Response): Promise<never> {
  let message = res.statusText || `HTTP ${res.status}`;
  try {
    const j = (await res.clone().json()) as { error?: string };
    if (j?.error) message = j.error;
  } catch {
    // non-JSON body
  }
  throw new ApiError(res.status, message);
}

export async function readFileContent(
  wsId: string,
  path: string,
): Promise<string> {
  const res = await fetch(
    `/api/workspaces/${wsId}/files/content?path=${encodeURIComponent(path)}`,
    { credentials: "same-origin" },
  );
  if (!res.ok) return fail(res);
  return res.text();
}

export async function writeFileContent(
  wsId: string,
  path: string,
  text: string,
): Promise<void> {
  const res = await fetch(
    `/api/workspaces/${wsId}/files/content?path=${encodeURIComponent(path)}`,
    {
      method: "PUT",
      credentials: "same-origin",
      headers: { "X-Hearth-CSRF": "1", "Content-Type": "application/octet-stream" },
      body: text,
    },
  );
  if (!res.ok) await fail(res);
}
