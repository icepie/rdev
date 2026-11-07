// @vitest-environment jsdom
import { JSDOM } from "jsdom";
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
const theme = vi.hoisted(() => ({ resolved: "light", preset: "default" }));
vi.mock("../ui/lib/theme", () => ({ useTheme: () => theme }));
import { CanvasPreview, canvasDocument } from "../ui/components/agent/canvas-preview";

it("keeps the iframe mounted for source/theme changes and replaces only revised content", async () => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  const tool = { id: "interactive", name: "canvas", args: JSON.stringify({ title: "Counter", format: "html", content: '<button onclick="this.textContent=2">1</button>' }), result: '{"status":"ready"}', done: true };
  const render = () => act(async () => root.render(createElement(CanvasPreview, { tool, t: key => key })));
  try {
    await render();
    const frame = host.querySelector("iframe");
    const original = frame.srcdoc;
    const send = vi.spyOn(frame.contentWindow, "postMessage").mockImplementation(() => {});
    await act(async () => host.querySelector('button[aria-pressed]').click());
    expect(host.querySelector("iframe")).toBe(frame);
    expect(frame.hidden).toBe(true);
    expect(frame.hasAttribute("inert")).toBe(true);
    theme.resolved = "dark";
    theme.preset = "nord";
    await render();
    expect(frame.srcdoc).toBe(original);
    expect(send).toHaveBeenCalledWith(expect.objectContaining({ type: "rdev:canvas-theme", mode: "dark", css: expect.stringContaining("--canvas") }), "*");
    await act(async () => host.querySelector('button[aria-pressed]').click());
    expect(host.querySelector("iframe")).toBe(frame);
    expect(frame.hidden).toBe(false);
    expect(frame.hasAttribute("inert")).toBe(false);
    tool.args = JSON.stringify({ title: "Counter", format: "html", content: "New revision" });
    await render();
    expect(host.querySelector("iframe")).not.toBe(frame);
    expect(host.querySelector("iframe").srcdoc).toContain("New revision");
  } finally {
    await act(async () => root.unmount());
    host.remove();
    theme.resolved = "light";
    theme.preset = "default";
    delete globalThis.IS_REACT_ACT_ENVIRONMENT;
  }
});

it("accepts only parent theme messages without replacing interactive document state", () => {
  // Execute the isolated theme listener in a DOM fixture. Actual origin/CSP
  // isolation is checked separately in the browser and sandbox attribute test.
  const doc = canvasDocument('<input id="draft">');
  const script = doc.match(/<script>([\s\S]*?)<\/script>/)[1];
  const dom = new JSDOM('<html class="light"><head><style id="rdev-canvas-theme"></style></head><body><input id="draft"></body></html>', { runScripts: "outside-only" });
  try {
    const { window } = dom;
    window.eval(script);
    const input = window.document.querySelector("input");
    input.value = "keep interaction";
    const data = { type: "rdev:canvas-theme", mode: "dark", css: ":root{--background:black}" };
    window.dispatchEvent(new window.MessageEvent("message", { data, source: null }));
    expect(window.document.documentElement.className).toBe("light");
    window.dispatchEvent(new window.MessageEvent("message", { data: { ...data, mode: "invalid" }, source: window.parent }));
    expect(window.document.documentElement.className).toBe("light");
    window.dispatchEvent(new window.MessageEvent("message", { data, source: window.parent }));
    expect(window.document.documentElement.className).toBe("dark");
    expect(window.document.querySelector("style").textContent).toBe(data.css);
    expect(window.document.querySelector("input")).toBe(input);
    expect(input.value).toBe("keep interaction");
  } finally { dom.window.close(); }
});
