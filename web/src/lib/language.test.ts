import { expect, test } from "vitest";
import { languageForPath } from "./language";

test.each([
  ["a/b.ts", "typescript"],
  ["main.rs", "rust"],
  ["go.mod", "plaintext"],
  ["Cargo.toml", "toml"],
  ["README", "plaintext"],
  ["x.JSON", "json"],
  ["noext.", "plaintext"],
])("%s -> %s", (p, want) => expect(languageForPath(p)).toBe(want));
