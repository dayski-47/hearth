import { afterEach, expect, test, vi } from "vitest";
import { wsProto } from "./wsUrl";

afterEach(() => {
  vi.unstubAllGlobals();
});

test("wsProto follows the page protocol", () => {
  vi.stubGlobal("location", { protocol: "https:" } as unknown as Location);
  expect(wsProto()).toBe("wss");
  vi.stubGlobal("location", { protocol: "http:" } as unknown as Location);
  expect(wsProto()).toBe("ws");
});
