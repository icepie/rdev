import { expect, it } from "vitest";
import { encodeFrame, decodeFrame } from "../ui/lib/file-transfer-frame";

it("matches the server big-endian frame layout and preserves binary payloads", () => {
  const frame = encodeFrame(0x20, "a", 258, new Uint8Array([0, 255]).buffer);
  expect([...new Uint8Array(frame)]).toEqual([
    0x20, 1, 97, 0, 0, 0, 0, 0, 0, 1, 2, 0, 255,
  ]);
  const result = decodeFrame(frame);
  expect(result).toMatchObject({ typ: 0x20, taskId: "a", offset: 258 });
  expect([...new Uint8Array(result.payload)]).toEqual([0, 255]);
  expect(
    decodeFrame(encodeFrame(0x21, "任务", Number.MAX_SAFE_INTEGER)),
  ).toMatchObject({ taskId: "任务", offset: Number.MAX_SAFE_INTEGER });
});

it("rejects every truncated header without throwing", () => {
  const frame = encodeFrame(0x21, "job", 0);
  for (let length = 0; length < frame.byteLength; length++)
    expect(decodeFrame(frame.slice(0, length))).toBeNull();
});

it("rejects malformed UTF-8 and offsets that would silently lose precision", () => {
  const frame = encodeFrame(0x21, "x", 0);
  new Uint8Array(frame)[2] = 255;
  expect(decodeFrame(frame)).toBeNull();
  new Uint8Array(frame)[2] = 120;
  new DataView(frame).setBigUint64(3, BigInt(Number.MAX_SAFE_INTEGER) + 1n);
  expect(decodeFrame(frame)).toBeNull();
  for (const offset of [-1, NaN, 0.1, Number.MAX_SAFE_INTEGER + 1])
    expect(() => encodeFrame(0x20, "x", offset)).toThrow();
  expect(() => encodeFrame(0x20, "界".repeat(86), 0)).toThrow();
});
