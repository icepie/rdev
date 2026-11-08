import type { FilePreviewState } from "../components/agent/types";

/** A preview belongs to one pane request and one device, never just a path. */
export function readFilePreviewReply(id: string, device: string, msg: Record<string, unknown>): FilePreviewState | null {
  if (!id || msg.requestId !== id || msg.deviceId !== device ||
      (msg.op !== "file_content" && msg.op !== "error")) return null;
  const message = typeof msg.message === "string" ? msg.message : typeof msg.error === "string" ? msg.error : "";
  return {
    status: message || msg.op === "error" ? "error" : "ready",
    message,
    content: typeof msg.content === "string" ? msg.content : "",
    truncated: msg.truncated === true,
    encoding: typeof msg.encoding === "string" ? msg.encoding : undefined,
    mime: typeof msg.mime === "string" ? msg.mime : undefined,
    previewUrl: typeof msg.previewUrl === "string" ? msg.previewUrl : undefined,
  };
}
