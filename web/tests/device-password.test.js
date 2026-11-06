// @vitest-environment jsdom
import { act, createElement, StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
vi.mock("../ui/lib/api", () => ({ getDevicePassword: id => ({ a: "saved-a", b: "saved-b", same: "saved-b" }[id] || "") }));
import { useDevicePassword } from "../ui/hooks/use-device-password";

it.each([false, true])("scopes authentication to the open device and never auto-submits typing (strict=%s)", async strict => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  vi.useFakeTimers();
  const host = document.createElement("div"); document.body.append(host);
  const root = createRoot(host);
  const onSubmit = vi.fn();
  let form;
  const props = { device: "a", open: true, error: "", onSubmit };
  function Harness() { form = useDevicePassword(props); return null; }
  const render = () => act(async () => root.render(strict ? createElement(StrictMode, null, createElement(Harness)) : createElement(Harness)));
  const tick = () => act(async () => vi.advanceTimersByTimeAsync(100));
  try {
    await render();
    props.device = "b";
    await render(); await tick();
    expect(onSubmit.mock.calls).toEqual([["saved-b"]]);
    expect(form.busy).toBe(true);
    await act(async () => form.submit());
    expect(onSubmit).toHaveBeenCalledTimes(1);
    props.error = "wrong";
    await render();
    await act(async () => form.setValue("manual"));
    await tick();
    expect(onSubmit).toHaveBeenCalledTimes(1);
    await act(async () => { form.submit(); form.submit(); });
    expect(onSubmit.mock.calls).toEqual([["saved-b"], ["manual"]]);
    // The caller clears the previous error on send; an identical rejection
    // must release the form again, rather than leaving it busy forever.
    props.error = "";
    await render();
    props.error = "wrong";
    await render();
    expect(form.busy).toBe(false);
    props.error = "";
    props.device = "same";
    await render(); await tick();
    expect(onSubmit).toHaveBeenLastCalledWith("saved-b");
    props.device = "empty";
    await render();
    await act(async () => form.setValue("p"));
    await tick();
    expect(onSubmit).toHaveBeenCalledTimes(3);
    expect(form.busy).toBe(false);
    props.device = "a";
    await render();
    props.open = false;
    await render(); await tick();
    expect(onSubmit).toHaveBeenCalledTimes(3);
    props.open = true;
    await render(); await tick();
    expect(onSubmit).toHaveBeenLastCalledWith("saved-a");
    expect(onSubmit).toHaveBeenCalledTimes(4);
    props.device = "b";
    await render();
    await act(async () => form.setValue("edited-before-auto"));
    await tick();
    expect(onSubmit).toHaveBeenCalledTimes(4);
  } finally {
    await act(async () => root.unmount()); host.remove(); vi.useRealTimers();
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});
