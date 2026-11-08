import { JSDOM } from "jsdom";
import { act, createElement, createRef } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";

let state;
const refresh = vi.fn();
vi.mock("../ui/lib/devices", () => ({ useDevices: () => ({ ...state, refresh }) }));
vi.mock("../ui/lib/i18n", () => ({ useI18n: () => ({ t: (key) => key }) }));
vi.mock("../ui/components/icon", () => ({ Icon: ({ name, className }) => createElement("i", { "data-icon": name, className }) }));
vi.mock("../ui/components/os-icon", () => ({ OsIcon: () => createElement("i") }));
vi.mock("../ui/components/input", () => ({ Input: (props) => createElement("input", props) }));
import { DeviceRail } from "../ui/components/device-rail";

function setup() {
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost" });
  Object.assign(globalThis, { window: dom.window, document: dom.window.document, IS_REACT_ACT_ENVIRONMENT: true });
  const root = createRoot(document.getElementById("root"));
  return { root, render: () => root.render(createElement(DeviceRail, { onPick: () => {} })) };
}

it("keeps the rail in loading state until the shared device request settles", async () => {
  state = { devices: [], error: "", loading: true };
  const { render } = setup();
  await act(render);
  expect(document.body.textContent).toContain("common.loading");
  expect(document.body.textContent).not.toContain("rail.empty");
});

it("offers an inline retry for a failed shared device request", async () => {
  state = { devices: [], error: "network down", loading: false };
  const { render } = setup();
  await act(render);
  const button = [...document.querySelectorAll("button")].find((node) => node.textContent === "common.retry");
  expect(button).toBeTruthy();
  await act(() => button.click());
  expect(refresh).toHaveBeenCalled();
});

it("uses a reduced-motion-safe loading indicator", async () => {
  state = { devices: [], error: "", loading: true };
  const { render } = setup();
  await act(render);
  const spinner = document.querySelector('[data-icon="Spinner"]');
  expect(spinner?.className).toContain("motion-reduce:animate-none");
});
