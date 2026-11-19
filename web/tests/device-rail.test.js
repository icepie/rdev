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

it("supports keyboard navigation across filtered device rows", async () => {
  state = { devices: [{ id: "alpha", os: "linux", hasPassword: false }, { id: "beta", os: "macos", hasPassword: false }, { id: "gamma", os: "windows", hasPassword: false }], error: "", loading: false };
  const picked = vi.fn();
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost" });
  Object.assign(globalThis, { window: dom.window, document: dom.window.document, IS_REACT_ACT_ENVIRONMENT: true });
  const root = createRoot(document.getElementById("root"));
  await act(() => root.render(createElement(DeviceRail, { onPick: picked })));
  const rows = [...document.querySelectorAll("button[draggable]")];
  rows[0].focus();
  await act(() => rows[0].dispatchEvent(new dom.window.KeyboardEvent("keydown", { key: "End", bubbles: true })));
  expect(document.activeElement).toBe(rows[2]);
  await act(() => rows[2].dispatchEvent(new dom.window.KeyboardEvent("keydown", { key: "ArrowUp", bubbles: true })));
  expect(document.activeElement).toBe(rows[1]);
});

it("keeps the selected device visible as offline after a settled refresh", async () => {
  state = { devices: [], error: "", loading: false };
  const { root, render } = setup();
  await act(() => root.render(createElement(DeviceRail, { selectedDevice: "laptop", onPick: () => {} })));
  const status = document.querySelector('[role="status"]');
  expect(status?.textContent).toContain("laptop");
  expect(status?.textContent).toContain("rail.selectedOffline");
  expect(document.querySelectorAll("button[draggable]")).toHaveLength(0);
});

it("shows names and IDs while preserving selection, drag identity, and keyed rows across renames", async () => {
  state = { devices: [{ id: "host/one", displayName: "Build host" }, { id: "host/two", displayName: "Build host" }, { id: "legacy" }], error: "", loading: false };
  const picked = vi.fn();
  const { root } = setup();
  const render = () => root.render(createElement(DeviceRail, { selectedDevice: "host/one", onPick: picked }));
  try {
    await act(render);
    const rows = [...document.querySelectorAll("button[draggable]")];
    expect(rows.map(row => row.textContent)).toEqual(["Build host (host/one)", "Build host (host/two)", "legacy"]);
    expect(rows[0].getAttribute("aria-pressed")).toBe("true");
    await act(() => rows[1].click());
    expect(picked).toHaveBeenCalledWith("host/two");
    const dataTransfer = { setData: vi.fn(), effectAllowed: "" };
    const event = new window.Event("dragstart", { bubbles: true });
    Object.defineProperty(event, "dataTransfer", { value: dataTransfer });
    await act(() => rows[0].dispatchEvent(event));
    expect(dataTransfer.setData).toHaveBeenCalledWith("application/x-rdev-device", "host/one");
    state = { ...state, devices: state.devices.map(d => d.id === "host/one" ? { ...d, displayName: "Renamed host" } : d) };
    await act(render);
    expect(document.querySelector("button[draggable]")).toBe(rows[0]);
    expect(rows[0].textContent).toBe("Renamed host (host/one)");
    expect(rows[0].getAttribute("aria-pressed")).toBe("true");
    await act(() => document.querySelector('[aria-label="rail.search"]').click());
    const input = document.querySelector("input");
    const propsKey = Object.keys(input).find(key => key.startsWith("__reactProps$"));
    for (const query of ["RENAMED", "host/one"]) {
      await act(() => input[propsKey].onChange({ target: { value: query } }));
      expect([...document.querySelectorAll("button[draggable]")].map(row => row.textContent)).toEqual(["Renamed host (host/one)"]);
    }
  } finally {
    await act(() => root.unmount());
    window.close();
  }
});
