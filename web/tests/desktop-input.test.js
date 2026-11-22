import { expect, it } from "vitest";
import { DesktopHeldInput } from "../ui/lib/desktop-input";

it("releases only held physical keys across repeat and modifier changes", () => {
  const held = new DesktopHeldInput();
  held.sent({ inputType: "key_down", code: "KeyA", key: "A" });
  held.sent({ inputType: "key_down", code: "KeyA", key: "a" });
  held.sent({ inputType: "key_up", code: "KeyA", key: "a" });
  held.sent({ inputType: "key_down", code: "ControlLeft", key: "Control", ctrlKey: true });
  const events = [];
  held.release(event => events.push(event));
  held.release(event => events.push(event));
  expect(events).toEqual([{ inputType: "key_up", code: "ControlLeft", key: "Control", ctrlKey: false, altKey: false, shiftKey: false, metaKey: false }]);
});

it("releases a cancelled pointer's original button at the last delivered position", () => {
  const held = new DesktopHeldInput();
  for (const [pointerId, button] of [[1, 2], [2, 0]]) {
    held.sent({ inputType: "mouse_down", x: 10, y: 20, pointerId, button });
  }
  held.sent({ inputType: "mouse_move", x: 30, y: 40, pointerId: 1, button: -1 });
  const events = [];
  held.releasePointer(1, event => events.push(event));
  held.releasePointer(1, event => events.push(event));
  expect(events).toEqual([{ inputType: "mouse_up", x: 30, y: 40, pointerId: 1, button: 2, pressure: 0 }]);
  held.clear();
  held.release(event => events.push(event));
  expect(events).toHaveLength(1);
});
