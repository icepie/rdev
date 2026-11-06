// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";

vi.mock("../ui/lib/api", () => ({
  getDevicePassword: (id) => (id === "device-a" ? "password-a" : "password-b"),
}));
vi.mock("../ui/components/icon", () => ({ Icon: () => null }));
import { TabAuthOverlay } from "../ui/pages/Terminal";

it("reloads remembered credentials when the tab changes device", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  vi.useFakeTimers();
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const submit = vi.fn();
  const props = { deviceId: "device-a", error: "", onSubmit: submit, t: (key) => key };
  try {
    await act(async () => root.render(createElement(TabAuthOverlay, props)));
    await act(async () => vi.advanceTimersByTimeAsync(50));
    expect(submit).toHaveBeenLastCalledWith("password-a");
    props.deviceId = "device-b";
    await act(async () => root.render(createElement(TabAuthOverlay, props)));
    expect(host.querySelector("input").value).toBe("password-b");
    await act(async () => vi.advanceTimersByTimeAsync(50));
    expect(submit).toHaveBeenLastCalledWith("password-b");
    expect(submit).toHaveBeenCalledTimes(2);
  } finally {
    await act(async () => root.unmount());
    host.remove();
    vi.useRealTimers();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});

it("clears a rejected password and permits a manual retry", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const submit = vi.fn();
  try {
    await act(async () => root.render(createElement(TabAuthOverlay, { deviceId: "device-a", error: "", onSubmit: submit, t: (key) => key })));
    const input = host.querySelector("input");
    await act(async () => root.render(createElement(TabAuthOverlay, { deviceId: "device-a", error: "wrong", onSubmit: submit, t: (key) => key })));
    expect(input.value).toBe("");
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set.call(input, "manual");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => host.querySelector("form").dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
    expect(submit).toHaveBeenLastCalledWith("manual");
  } finally {
    await act(async () => root.unmount());
    host.remove();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
