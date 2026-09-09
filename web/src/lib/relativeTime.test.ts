import { expect, test } from "vitest";
import { relativeTime } from "./relativeTime";

const now = new Date("2026-06-10T12:00:00Z");

test.each([
  ["2026-06-10T11:59:40Z", "just now"],
  ["2026-06-10T11:55:00Z", "5m ago"],
  ["2026-06-10T10:00:00Z", "2h ago"],
  ["2026-06-07T12:00:00Z", "3d ago"],
])("%s -> %s", (iso, want) => {
  expect(relativeTime(iso, now)).toBe(want);
});

test("older than a week shows a date", () => {
  expect(relativeTime("2026-05-01T12:00:00Z", now)).toMatch(/May/);
});
