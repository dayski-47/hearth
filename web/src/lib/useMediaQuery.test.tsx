import { renderHook } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { useMediaQuery } from "./useMediaQuery";

test("reads the initial match", () => {
  vi.stubGlobal("matchMedia", (q: string) => ({
    matches: true,
    media: q,
    addEventListener: () => {},
    removeEventListener: () => {},
  }));
  const { result } = renderHook(() => useMediaQuery("(max-width: 900px)"));
  expect(result.current).toBe(true);
  vi.unstubAllGlobals();
});
