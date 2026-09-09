const enc = new TextEncoder();

export function encodeStdin(s: string): Uint8Array {
  const body = enc.encode(s);
  const out = new Uint8Array(body.length + 1);
  out[0] = 0x00;
  out.set(body, 1);
  return out;
}

function clampU16(n: number): number {
  if (!Number.isFinite(n)) return 0;
  return Math.max(0, Math.min(65535, Math.floor(n)));
}

export function encodeResize(cols: number, rows: number): Uint8Array {
  const out = new Uint8Array(5);
  out[0] = 0x01;
  const dv = new DataView(out.buffer);
  dv.setUint16(1, clampU16(cols));
  dv.setUint16(3, clampU16(rows));
  return out;
}
