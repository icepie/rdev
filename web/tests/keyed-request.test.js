import { expect, it, vi } from "vitest";
import { createKeyedRequest } from "../ui/lib/keyed-request";

it("shares authentication for one device without blocking another device", async () => {
  const run = createKeyedRequest();
  const pending = Promise.withResolvers();
  const authenticate = vi.fn(() => pending.promise);
  const first = run("device-a", authenticate);
  expect(run("device-a", authenticate)).toBe(first);
  await expect(run("device-b", async () => false)).resolves.toBe(false);
  expect(authenticate).toHaveBeenCalledTimes(1);
  pending.resolve(true);
  await expect(first).resolves.toBe(true);
  await expect(run("device-a", async () => false)).resolves.toBe(false);
});

it("releases rejected requests so reconnect can authenticate again", async () => {
  const run = createKeyedRequest();
  const first = run("device", () => { throw new Error("disconnected"); });
  const second = run("device", async () => true);
  expect(second).toBe(first);
  await expect(first).rejects.toThrow("disconnected");
  await expect(run("device", async () => true)).resolves.toBe(true);
});
