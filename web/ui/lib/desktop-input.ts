type Input = Record<string, unknown>;

/** Track only events sent to this viewer's current desktop session. */
export class DesktopHeldInput {
  private keys = new Map<unknown, Input>();
  private buttons = new Map<unknown, Map<unknown, Input>>();

  sent(input: Input) {
    const id = input.code || input.key;
    switch (input.inputType) {
      case "key_down":
        this.keys.set(id, input);
        break;
      case "key_up":
        this.keys.delete(id);
        break;
      case "mouse_down": {
        const held = this.buttons.get(input.pointerId) ?? new Map<unknown, Input>();
        held.set(input.button, input);
        this.buttons.set(input.pointerId, held);
        break;
      }
      case "mouse_move": {
        const held = this.buttons.get(input.pointerId);
        if (held) for (const button of held.values()) {
          button.x = input.x;
          button.y = input.y;
        }
        break;
      }
      case "mouse_up": {
        const held = this.buttons.get(input.pointerId);
        held?.delete(input.button);
        if (!held?.size) this.buttons.delete(input.pointerId);
        break;
      }
    }
  }

  releasePointer(id: unknown, send: (input: Input) => void) {
    const held = this.buttons.get(id);
    if (!held) return;
    for (const [button, input] of held) {
      send({ ...input, inputType: "mouse_up", pressure: 0 });
      held.delete(button);
    }
    this.buttons.delete(id);
  }

  release(send: (input: Input) => void) {
    for (const id of this.buttons.keys()) this.releasePointer(id, send);
    for (const [id, input] of this.keys) {
      send({ ...input, inputType: "key_up", ctrlKey: false, altKey: false, shiftKey: false, metaKey: false });
      this.keys.delete(id);
    }
  }

  clear() {
    this.keys.clear();
    this.buttons.clear();
  }
}
