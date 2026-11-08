// Own the listeners as one stream lifetime. Browser media events may arrive
// after a socket/decoder has been replaced; never let them bind the new stream.
export function attachDesktopMediaSource(source: MediaSource, {
  current, supported, ready, update, error,
}: {
  current: () => boolean;
  supported: (mime: string) => boolean;
  ready: (buffer: SourceBuffer) => void;
  update: () => void;
  error: (cause: unknown) => void;
}) {
  let disposed = false;
  let buffer: SourceBuffer | null = null;
  const ownsStream = () => !disposed && current();
  const onUpdate = () => { if (ownsStream()) update(); };
  const onOpen = () => {
    if (!ownsStream() || source.readyState !== "open") return;
    try {
      const preferred = 'video/mp4; codecs="avc1.4D403D"';
      buffer = source.addSourceBuffer(supported(preferred) ? preferred : "video/mp4");
      try { buffer.mode = "sequence"; } catch { /* optional browser mode */ }
      buffer.addEventListener("updateend", onUpdate);
      ready(buffer);
      // Packets can arrive before sourceopen; do not require a later packet
      // to kick off the first append.
      onUpdate();
    } catch (cause) { if (ownsStream()) error(cause); }
  };
  source.addEventListener("sourceopen", onOpen, { once: true });
  return () => {
    disposed = true;
    source.removeEventListener("sourceopen", onOpen);
    buffer?.removeEventListener("updateend", onUpdate);
    buffer = null;
  };
}
