import { JSDOM } from "jsdom";
import { act, createElement } from "react";
import { createRoot } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { expect, it, vi } from "vitest";

const fixtures = vi.hoisted(() => ({ devices: [{ id: "device-a", hasPassword: false }, { id: "device-b", hasPassword: false }] }));
vi.mock("../ui/lib/devices", () => ({ useDevices: () => fixtures }));
vi.mock("../ui/lib/api", () => ({
  wsURL: () => "ws://localhost/files", deviceAuthBypassed: async () => false,
  getDevicePassword: () => "fixture-password", rememberDevicePassword: () => {}, forgetDevicePassword: () => {},
}));
vi.mock("../ui/lib/i18n", () => ({ useI18n: () => ({ t: (key) => key }) }));
vi.mock("../ui/components/icon", () => ({ Icon: () => null }));
vi.mock("../ui/components/os-icon", () => ({ OsIcon: () => null }));
vi.mock("../ui/components/split-layout", () => ({ SplitPageShell: ({ children, ...props }) => { fixtures.shell = props; return children; } }));
vi.mock("../ui/components/device-rail", () => ({ DEVICE_DRAG_MIME: "device", DeviceDropTarget: ({ children }) => { fixtures.panes = children.props.panesRef; return children; } }));
import { FilesPage, FilesBrowser } from "../ui/pages/Files";

it.each([false, true])("isolates reconnect attempts and waits for device authentication (protected=%s)", async (protectedDevice) => {
  fixtures.devices[0].hasPassword = protectedDevice;
  const dom = new JSDOM('<div id="root"></div>', { url: "http://localhost/files", pretendToBeVisual: true });
  const sockets = [];
  class Socket {
    static OPEN = 1;
    readyState = 0;
    sent = [];
    constructor() { sockets.push(this); }
    send(raw) { this.sent.push(JSON.parse(raw)); }
    open() { this.readyState = 1; this.onopen?.(); }
    close() { this.readyState = 3; this.onclose?.(); }
    message(frame) { this.onmessage?.({ data: JSON.stringify(frame) }); }
  }
  const replacements = {
    window: dom.window, document: dom.window.document, navigator: dom.window.navigator,
    localStorage: dom.window.localStorage, WebSocket: Socket,
    HTMLElement: dom.window.HTMLElement, MutationObserver: dom.window.MutationObserver,
    IS_REACT_ACT_ENVIRONMENT: true,
  };
  const previous = Object.entries(replacements).map(([key]) => [key, Object.getOwnPropertyDescriptor(globalThis, key)]);
  for (const [key, value] of Object.entries(replacements)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value });
  vi.useFakeTimers();
  localStorage.setItem("rdevFileTasks", JSON.stringify([{
    id: "download-ui", dir: "download", device: "device-a", path: "/test.bin",
    name: "test.bin", size: 10, done: 0, status: "running", created: Date.now(),
  }]));
  const root = createRoot(document.getElementById("root"));
  try {
    await act(async () => root.render(createElement(MemoryRouter, { initialEntries: ["/files?device=device-a"] }, createElement(FilesPage))));
    const first = sockets[0];
    await act(async () => first.open());
    if (protectedDevice) {
      expect(first.sent.filter(m => m.op === "download_start")).toHaveLength(0);
      expect(first.sent.filter(m => m.op === "auth")).toHaveLength(1);
      await act(async () => first.message({ op: "auth_ok", deviceId: "device-a" }));
    }
    expect(first.sent.filter(m => m.op === "download_start")).toHaveLength(1);
    const firstDownload = first.sent.find(m => m.op === "download_start");
    await act(async () => first.close());
    expect(document.body.textContent).toContain("files.connectionLost");
    const offlinePicker = document.querySelector('input[type="file"]');
    Object.defineProperty(offlinePicker, "files", { configurable: true, value: [new dom.window.File(["offline"], "offline.txt")] });
    await act(async () => offlinePicker.dispatchEvent(new dom.window.Event("change", { bubbles: true })));
    expect(JSON.parse(localStorage.getItem("rdevFileTasks")).find(task => task.name === "offline.txt"))
      .toMatchObject({ status: "failed", message: "files.connectionLost" });
    await act(async () => vi.advanceTimersByTimeAsync(20000));
    expect(sockets).toHaveLength(2);
    const second = sockets[1];
    await act(async () => second.open());
    if (protectedDevice) {
      const picker = document.querySelector('input[type="file"]');
      Object.defineProperty(picker, "files", { configurable: true, value: [new dom.window.File(["upload"], "upload.txt")] });
      await act(async () => picker.dispatchEvent(new dom.window.Event("change", { bubbles: true })));
      await act(async () => vi.advanceTimersByTimeAsync(6000));
      expect(second.sent.filter(m => m.op === "download_start")).toHaveLength(0);
      expect(second.sent.filter(m => m.op === "upload_start")).toHaveLength(0);
      expect(second.sent.filter(m => m.op === "auth")).toHaveLength(1);
      await act(async () => second.message({ op: "auth_ok", deviceId: "device-a" }));
      expect(second.sent.filter(m => m.op === "upload_start")).toHaveLength(1);
      expect(second.sent.find(m => m.op === "upload_start")).toMatchObject({ deviceId: "device-a", name: "upload.txt" });
    }
    expect(second.sent.filter(m => m.op === "download_start")).toHaveLength(1);
    expect(second.sent.some(m => m.op === "upload_start" && m.name === "offline.txt")).toBe(false);
    const resumedDownload = second.sent.find(m => m.op === "download_start");
    expect(resumedDownload.taskId).not.toBe(firstDownload.taskId);
    await act(async () => second.message({ op: "error", taskId: firstDownload.taskId, message: "old attempt failed" }));
    const persistedTask = () => JSON.parse(localStorage.getItem("rdevFileTasks")).find(task => task.id === "download-ui");
    expect(persistedTask().status).toBe("running");
    await act(async () => second.message({ op: "error", taskId: resumedDownload.taskId, message: "device not connected" }));
    expect(persistedTask()).toMatchObject({ status: "failed", message: "device not connected", id: "download-ui" });
    const listing = second.sent.filter(m => m.op === "list").at(-1);
    await act(async () => first.message({ op: "list_result", requestId: listing.requestId, path: "/stale", entries: [] }));
    expect(document.querySelector('input').value).not.toBe("/stale");
    await act(async () => second.message({ op: "list_result", requestId: listing.requestId, path: "/current", entries: [] }));
    expect(document.querySelector('input').value).toBe("/current");
    expect(document.querySelector("table").textContent).not.toContain("files.connectionLost");
    await act(async () => second.message({ op: "error", requestId: listing.requestId, message: "directory unavailable" }));
    expect(document.body.textContent).toContain("directory unavailable");

    // The actual page exposes a second pane without replacing the first browser.
    const toggle = [...document.querySelectorAll("button")].find(b => b.textContent === "files.showSecondPane");
    await act(async () => toggle.click());
    expect(sockets).toHaveLength(3);
    const targetSocket = sockets[2];
    await act(async () => targetSocket.open());
    await act(async () => fixtures.shell.onDevicePick("device-b"));
    expect(fixtures.shell.selectedDevice).toBe("device-b");
    const targetList = targetSocket.sent.filter(m => m.op === "list").at(-1);
    expect(targetList.deviceId).toBe("device-b");
    await act(async () => targetSocket.message({ op: "list_result", requestId: targetList.requestId, path: "/destination", entries: [] }));
    expect(fixtures.panes.current.get(0).deviceId()).toBe("device-a");
    expect(fixtures.panes.current.get(1).deviceId()).toBe("device-b");
    let copied;
    await act(async () => {
      copied = fixtures.panes.current.get(0).transferItemsTo(1, [{ name: "empty", path: "/empty", isDir: true, size: 0 }]);
    });
    if (protectedDevice) await act(async () => second.message({ op: "auth_ok", deviceId: "device-a" }));
    const walk = second.sent.filter(m => m.op === "list").at(-1);
    expect(walk.path).toBe("/empty");
    await act(async () => second.message({ op: "list_result", requestId: walk.requestId, path: "/empty", entries: [] }));
    const mkdir = targetSocket.sent.find(m => m.op === "mkdir");
    expect(mkdir).toMatchObject({ deviceId: "device-b", path: "/destination/empty" });
    await act(async () => targetSocket.message({ op: "mkdir_result", requestId: mkdir.requestId, success: true }));
    await copied;
    await act(async () => toggle.click());
    expect(fixtures.shell.selectedDevice).toBe("device-a");
    expect(document.querySelectorAll("section")[1].hidden).toBe(true);
    expect(targetSocket.readyState).toBe(Socket.OPEN);
    await act(async () => toggle.click());
    expect(sockets).toHaveLength(3);
    expect(document.querySelectorAll("section")[1].hidden).toBe(false);
    expect(fixtures.panes.current.get(1).currentPath()).toBe("/destination");

    await act(async () => second.close());
    await act(async () => fixtures.panes.current.get(0).transferItemsTo(1, [
      { name: "offline-file", path: "/offline-file", isDir: false, size: 4 },
    ]));
    expect(JSON.parse(localStorage.getItem("rdevFileTasks")).find(task => task.name === "offline-file"))
      .toMatchObject({ status: "failed", message: "files.connectionLost" });
    await act(async () => fixtures.panes.current.get(0).transferItemsTo(1, [
      { name: "offline-empty", path: "/offline-empty", isDir: true, size: 0 },
    ]));
    expect(document.body.textContent).toContain("files.connectionLost");
    await act(async () => vi.advanceTimersByTimeAsync(2000));
    const sourceReconnected = sockets.at(-1);
    await act(async () => sourceReconnected.open());
    expect(sourceReconnected.sent.some(m => m.path === "/offline-empty" || m.path === "/offline-file")).toBe(false);
    expect(sourceReconnected.sent.filter(m => m.op === "auth")).toHaveLength(protectedDevice ? 1 : 0);

    const panesRef = { current: new Map() };
    await act(async () => root.render(createElement(MemoryRouter, null, createElement(FilesBrowser, {
      primary: false, leafId: 7, panesRef,
    }))));
    const directorySocket = sockets.at(-1);
    await act(async () => directorySocket.open());
    const createDirectory = panesRef.current.get(7).createDirectory;
    const beginDirectory = async () => {
      let result;
      await act(async () => {
        result = createDirectory("/target/empty", "device-a").then(() => "created", error => error.message);
      });
      if (protectedDevice) {
        await act(async () => directorySocket.message({ op: "auth_ok", deviceId: "device-a" }));
      }
      const request = directorySocket.sent.filter(m => m.op === "mkdir").at(-1);
      expect(request).toMatchObject({ deviceId: "device-a", path: "/target/empty" });
      return { result, request };
    };
    let operation = await beginDirectory();
    await act(async () => directorySocket.message({ op: "mkdir_result", requestId: operation.request.requestId, success: true }));
    expect(await operation.result).toBe("created");
    operation = await beginDirectory();
    await act(async () => directorySocket.message({ op: "mkdir_result", requestId: operation.request.requestId, success: false, error: "file exists" }));
    expect(await operation.result).toBe("file exists");
    operation = await beginDirectory();
    await act(async () => directorySocket.message({ op: "error", requestId: operation.request.requestId, message: "device unavailable" }));
    expect(await operation.result).toBe("device unavailable");
    operation = await beginDirectory();
    await act(async () => vi.advanceTimersByTimeAsync(30000));
    expect(await operation.result).toBe("files.directoryTimeout");
    operation = await beginDirectory();
    await act(async () => directorySocket.close());
    expect(await operation.result).toBe("files.connectionLost");
    await act(async () => {
      await expect(createDirectory("/offline-directory", "device-a")).rejects.toThrow("files.connectionLost");
    });
    await act(async () => vi.advanceTimersByTimeAsync(2000));
    const directoryReconnected = sockets.at(-1);
    await act(async () => directoryReconnected.open());
    expect(directoryReconnected.sent.some(m => m.op === "mkdir" || m.op === "auth")).toBe(false);


  } finally {
    await act(async () => root.unmount());
    vi.useRealTimers();
    dom.window.close();
    for (const [key, descriptor] of previous) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor);
      else delete globalThis[key];
    }
  }
});
