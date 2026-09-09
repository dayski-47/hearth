import { expect, test } from "vitest";
import { encodeResize, encodeStdin } from "./wireCodec";

test("encodeStdin prefixes 0x00 and UTF-8 encodes", () => {
  const out = encodeStdin("hi");
  expect(Array.from(out)).toEqual([0x00, 0x68, 0x69]);
});

test("encodeStdin handles multibyte", () => {
  const out = encodeStdin("é"); // é -> 0xC3 0xA9
  expect(Array.from(out)).toEqual([0x00, 0xc3, 0xa9]);
});

test("encodeResize is 0x01 then two big-endian uint16", () => {
  const out = encodeResize(80, 24);
  expect(out.length).toBe(5);
  expect(out[0]).toBe(0x01);
  const dv = new DataView(out.buffer);
  expect(dv.getUint16(1)).toBe(80);
  expect(dv.getUint16(3)).toBe(24);
});

test("encodeResize clamps to uint16 range", () => {
  const out = encodeResize(99999, -1);
  const dv = new DataView(out.buffer);
  expect(dv.getUint16(1)).toBe(65535);
  expect(dv.getUint16(3)).toBe(0);
});
