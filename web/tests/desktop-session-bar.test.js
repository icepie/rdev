// @vitest-environment jsdom
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { DesktopSessionBar } from "../ui/components/desktop-session-bar";

let host;
let root;
const t = key => ({ "desktop.controlling": "Controlling", "desktop.releaseControl": "Release" })[key] ?? key;

beforeEach(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
});

const render = props => act(async () => root.render(createElement(DesktopSessionBar, { t, onReleaseControl: vi.fn(), ...props })));

it("stays out of the layout when there is nothing to report", async () => {
  await render({ controlling: false });
  expect(host.textContent).toBe("");
});

it("shows control ownership and releasing it delegates to the viewer", async () => {
  const onReleaseControl = vi.fn();
  await render({ controlling: true, info: "800×600 · screen:all", frame: "12 fps", onReleaseControl });
  expect(host.textContent).toContain("Controlling");
  expect(host.textContent).toContain("800×600 · screen:all");
  expect(host.textContent).toContain("12 fps");
  const release = [...host.querySelectorAll("button")].find(b => b.textContent === "Release");
  await act(async () => release.click());
  expect(onReleaseControl).toHaveBeenCalledTimes(1);
});

it("keeps stream details visible when control is off", async () => {
  await render({ controlling: false, info: "1280×720 · display:1", frame: "30 fps" });
  expect(host.textContent).not.toContain("Controlling");
  expect(host.textContent).toContain("1280×720 · display:1");
});
