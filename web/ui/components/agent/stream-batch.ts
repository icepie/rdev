type Frame = Record<string, unknown>;

// Batch only plain adjacent text frames. Unknown metadata remains a boundary,
// so extending the protocol cannot silently discard a new field's semantics.
function isTextFrame(frame: Frame): boolean {
  return (frame.op === "delta" || frame.op === "reasoning") &&
    typeof frame.text === "string" &&
    Object.keys(frame).every(key => key === "op" || key === "thread" || key === "text");
}

export function createStreamBatch(deliver: (frame: Frame) => void) {
  let pending: Frame | null = null;
  let chunks: string[] = [];
  let size = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let disposed = false;
  const clear = () => {
    clearTimeout(timer);
    timer = undefined;
    pending = null;
    chunks = [];
    size = 0;
  };
  const flush = () => {
    const frame = pending && { ...pending, text: chunks.join("") };
    clear();
    if (frame && !disposed) deliver(frame);
  };
  return {
    push(frame: Frame) {
      if (disposed) return;
      if (!isTextFrame(frame)) {
        flush();
        deliver(frame);
        return;
      }
      if (frame.text === "") return;
      if (pending && (pending.op !== frame.op || pending.thread !== frame.thread)) flush();
      // Bound retained text even in a throttled/background browser tab.
      const text = frame.text as string;
      if (size + text.length > 32 * 1024) flush();
      if (text.length >= 32 * 1024) {
        deliver(frame);
        return;
      }
      if (!pending) {
        pending = frame;
        // A timer continues in hidden tabs; requestAnimationFrame can stop.
        timer = setTimeout(flush, 16);
      }
      chunks.push(text);
      size += text.length;
    },
    flush,
    dispose() { disposed = true; clear(); },
  };
}
