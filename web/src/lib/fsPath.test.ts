import { expect, test } from "vitest";
import { basename, dirname } from "./fsPath";

test.each([
  ["a", ""],
  ["a/b", "a"],
  ["a/b/c.txt", "a/b"],
  ["/x", ""],
])("dirname(%s) -> %s", (p, want) => expect(dirname(p)).toBe(want));

test.each([
  ["a/b.txt", "b.txt"],
  ["x", "x"],
  ["a/b/", ""],
])("basename(%s) -> %s", (p, want) => expect(basename(p)).toBe(want));
