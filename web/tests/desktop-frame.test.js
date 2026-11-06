import { expect, it, vi } from "vitest";
import { paintDesktopImage } from "../ui/lib/desktop-frame";

it("drops a decoded bitmap from a replaced stream and still releases it", async () => {
  const previous = Object.getOwnPropertyDescriptor(globalThis, "window");
  const decode = Promise.withResolvers();
  Object.defineProperty(globalThis, "window", { configurable: true, value: { createImageBitmap: () => decode.promise } });
  try {
    let current = true;
    const bitmap = { width: 800, height: 600, close: vi.fn() };
    const paint = vi.fn(), fallback = vi.fn();
    const result = paintDesktopImage(new Blob(), () => current, paint, fallback);
    current = false;
    decode.resolve(bitmap);
    await expect(result).resolves.toBe(false);
    expect(paint).not.toHaveBeenCalled();
    expect(fallback).not.toHaveBeenCalled();
    expect(bitmap.close).toHaveBeenCalledTimes(1);
    current = true;
    paint.mockImplementation(() => { throw new Error("canvas unavailable"); });
    await expect(paintDesktopImage(new Blob(), () => current, paint, fallback)).rejects.toThrow("canvas unavailable");
    expect(bitmap.close).toHaveBeenCalledTimes(2);
    expect(fallback).not.toHaveBeenCalled();
  } finally {
    if (previous) Object.defineProperty(globalThis, "window", previous);
    else delete globalThis.window;
  }
});

it("checks ownership again after asynchronous fallback decoding", async () => {
  const previous = Object.getOwnPropertyDescriptor(globalThis, "window");
  Object.defineProperty(globalThis, "window", { configurable: true, value: {} });
  try {
    let current = true;
    const decoded = Promise.withResolvers(), paint = vi.fn();
    const fallback = () => decoded.promise;
    const result = paintDesktopImage(new Blob(), () => current, paint, fallback);
    current = false;
    decoded.resolve({ naturalWidth: 320, naturalHeight: 240 });
    await expect(result).resolves.toBe(false);
    expect(paint).not.toHaveBeenCalled();
    current = true;
    await expect(paintDesktopImage(new Blob(), () => current, paint, fallback)).resolves.toBe(true);
    expect(paint).toHaveBeenCalledWith(expect.any(Object), 320, 240);
  } finally {
    if (previous) Object.defineProperty(globalThis, "window", previous);
    else delete globalThis.window;
  }
});
