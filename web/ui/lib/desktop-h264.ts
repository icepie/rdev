import { acceptDesktopH264Frame } from "./desktop-frame";
import { base64ToBytes } from "./desktop-model";

export type DesktopH264Setup = { codec?: string; description?: string; fps?: number };

/** The legacy /desktop H.264 chain. Access units form one prediction chain, so
 * every frame feeds in arrival order; a replaced decoder must never paint, and
 * a skipped delta resumes only at the next keyframe. */
export class DesktopH264Decoder {
  /** "" while the stream is MJPEG; "h264" while this decoder owns the canvas. */
  format = "";

  private decoder: VideoDecoder | null = null;
  private frameIndex = 0;
  private frameDurationUs = 125000;
  private readonly needKeyframe = { current: false };

  constructor(
    private readonly paint: (image: CanvasImageSource, width: number, height: number) => void,
    private readonly fallback: () => void,
  ) {}

  close() {
    if (this.decoder) {
      try {
        this.decoder.close();
      } catch {
        /* already closed */
      }
      this.decoder = null;
    }
    this.format = "";
    this.frameIndex = 0;
    this.needKeyframe.current = false;
  }

  setup(msg: DesktopH264Setup): boolean {
    this.close();
    if (typeof VideoDecoder !== "function" || !msg.codec || !msg.description) return false;
    try {
      const config: VideoDecoderConfig = {
        codec: msg.codec,
        description: base64ToBytes(msg.description) as BufferSource,
        optimizeForLatency: true,
      };
      const decoder = new VideoDecoder({
        output: (frame) => {
          try {
            if (this.decoder !== decoder) return;
            this.paint(frame, frame.displayWidth || frame.codedWidth, frame.displayHeight || frame.codedHeight);
          } finally {
            frame.close();
          }
        },
        error: (e) => {
          if (this.decoder !== decoder) return;
          console.error("h264 decoder error", e);
          this.fallback();
        },
      });
      decoder.configure(config);
      this.decoder = decoder;
      this.format = "h264";
      this.frameIndex = 0;
      this.needKeyframe.current = true;
      this.frameDurationUs = Math.max(1000, Math.round(1000000 / (msg.fps || 30)));
      return true;
    } catch (e) {
      console.error("h264 configure failed", e);
      return false;
    }
  }

  feed(data: ArrayBuffer): boolean {
    const decoder = this.decoder;
    if (!decoder || decoder.state !== "configured" || data.byteLength < 5) return false;
    const keyframe = (new Uint8Array(data, 0, 1)[0] & 1) !== 0;
    if (!acceptDesktopH264Frame(keyframe, decoder.decodeQueueSize, this.needKeyframe)) return false;
    const chunk = new Uint8Array(data, 1);
    try {
      decoder.decode(
        new EncodedVideoChunk({
          type: keyframe ? "key" : "delta",
          timestamp: this.frameIndex * this.frameDurationUs,
          data: chunk,
        }),
      );
      this.frameIndex++;
      return true;
    } catch (e) {
      console.error("h264 decode feed failed", e);
      this.fallback();
      return false;
    }
  }
}
