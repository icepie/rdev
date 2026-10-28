import { useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { MobileNavContext } from "../shell/AppShell";
import { useI18n } from "../lib/i18n";
import {
  apiJSON,
  wsURL,
  deviceAuthBypassed,
  getDevicePassword,
  rememberDevicePassword,
  forgetDevicePassword,
} from "../lib/api";
import { cn } from "../lib/utils";
import { Button } from "../components/button";
import { Input } from "../components/input";
import { Badge } from "../components/badge";
import { Card, CardContent, CardHeader, CardTitle } from "../components/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../components/tabs";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "../components/alert-dialog";
import { Icon } from "../components/icon";
import { OsIcon } from "../components/os-icon";

/* ── /api/batch/devices shape (distinct from /api/clients) ── */

interface BatchDevice {
  id: string;
  connectedAt: string;
  os?: string;
  hasPassword: boolean;
}

type ResultStatus = "running" | "ok" | "fail";

interface ExecResult {
  id: string;
  status: ResultStatus;
  label: string;
  output: string;
}

interface UploadResult {
  id: string;
  status: ResultStatus;
  label: string;
  path: string;
}

/* ── Batch WS binary framing (ported 1:1 from web/batch.html) ── */

function decodeBinFrame(buf: ArrayBuffer) {
  const view = new DataView(buf);
  const typ = view.getUint8(0);
  const idLen = view.getUint8(1);
  const devId = new TextDecoder().decode(new Uint8Array(buf, 2, idLen));
  const payload = new Uint8Array(buf, 2 + idLen);
  return { typ, devId, payload };
}

function encodeFileStart(id: string, meta: unknown, mode: number): ArrayBuffer {
  const enc = new TextEncoder();
  const idBytes = enc.encode(id);
  const metaBytes = enc.encode(JSON.stringify(meta));
  const buf = new ArrayBuffer(2 + idBytes.length + 2 + metaBytes.length + 4);
  const view = new DataView(buf);
  let pos = 0;
  view.setUint8(pos++, 0x05);
  view.setUint8(pos++, idBytes.length);
  new Uint8Array(buf, pos, idBytes.length).set(idBytes);
  pos += idBytes.length;
  view.setUint16(pos, metaBytes.length);
  pos += 2;
  new Uint8Array(buf, pos, metaBytes.length).set(metaBytes);
  pos += metaBytes.length;
  view.setUint32(pos, mode);
  return buf;
}

function encodeChunk(id: string, data: ArrayBuffer): ArrayBuffer {
  const idBytes = new TextEncoder().encode(id);
  const chunk = new Uint8Array(data);
  const buf = new ArrayBuffer(2 + idBytes.length + chunk.length);
  const out = new Uint8Array(buf);
  out[0] = 0x06;
  out[1] = idBytes.length;
  out.set(idBytes, 2);
  out.set(chunk, 2 + idBytes.length);
  return buf;
}

function encodeEnd(id: string): ArrayBuffer {
  const idBytes = new TextEncoder().encode(id);
  const buf = new ArrayBuffer(2 + idBytes.length);
  const out = new Uint8Array(buf);
  out[0] = 0x07;
  out[1] = idBytes.length;
  out.set(idBytes, 2);
  return buf;
}

function authDevice(
  ws: WebSocket,
  device: BatchDevice,
  password: string,
): Promise<boolean> {
  if (!device.hasPassword) return Promise.resolve(true);
  const { promise, resolve } = Promise.withResolvers<boolean>();
  const onMessage = (evt: MessageEvent) => {
    if (evt.data instanceof ArrayBuffer) return;
    let msg: { op?: string; deviceId?: string };
    try {
      msg = JSON.parse(evt.data as string);
    } catch {
      return;
    }
    if (
      (msg.op === "auth_ok" || msg.op === "auth_fail") &&
      msg.deviceId === device.id
    ) {
      ws.removeEventListener("message", onMessage);
      resolve(msg.op === "auth_ok");
    }
  };
  ws.addEventListener("message", onMessage);
  ws.send(JSON.stringify({ op: "auth", deviceId: device.id, password }));
  return promise;
}

/* ── Status badge ── */

function StatusBadge({ status, label }: { status: ResultStatus; label: string }) {
  if (status === "ok") {
    return (
      <Badge
        variant="outline"
        className="border-transparent bg-success/15 text-success-foreground"
      >
        {label}
      </Badge>
    );
  }
  if (status === "fail") {
    return <Badge variant="destructive">{label}</Badge>;
  }
  return <Badge variant="secondary">{label}</Badge>;
}

function ResultCard({
  id,
  status,
  label,
  children,
}: {
  id: string;
  status: ResultStatus;
  label: string;
  children: ReactNode;
}) {
  return (
    <div className="overflow-hidden rounded-lg border border-border bg-surface-recessed-solid">
      <div className="flex items-center justify-between gap-2 border-b border-border px-3 py-2 text-xs">
        <span className="truncate font-semibold">{id}</span>
        <StatusBadge status={status} label={label} />
      </div>
      <div className="max-h-[200px] overflow-y-auto px-3 py-2 font-mono text-xs leading-relaxed whitespace-pre-wrap break-words text-muted-foreground">
        {children}
      </div>
    </div>
  );
}

/* ── Page ── */

export function BatchPage() {
  const { t } = useI18n();
  const { open: openMobileNav } = useContext(MobileNavContext);
  const tRef = useRef(t);
  tRef.current = t;

  const [devices, setDevices] = useState<BatchDevice[]>([]);
  const devicesRef = useRef<BatchDevice[]>([]);
  devicesRef.current = devices;

  const [selected, setSelected] = useState<Set<string>>(new Set());

  const [cmd, setCmd] = useState("");
  const [execResults, setExecResults] = useState<ExecResult[]>([]);
  const [execErrors, setExecErrors] = useState<string[]>([]);

  const [file, setFile] = useState<File | null>(null);
  const [destPath, setDestPath] = useState("");
  const [dragOver, setDragOver] = useState(false);
  const [uploadResults, setUploadResults] = useState<UploadResult[]>([]);

  const fileInputRef = useRef<HTMLInputElement>(null);
  const uploadAcks = useRef(new Map<string, () => void>());
  const sockets = useRef(new Set<WebSocket>());

  /* Password prompt + alert dialogs (promise-based, like RDevUI.prompt/alert) */
  const [pwPrompt, setPwPrompt] = useState<{ ids: string[] } | null>(null);
  const [pwValue, setPwValue] = useState("");
  const pwResolver = useRef<((v: string | null) => void) | null>(null);
  const [alertBox, setAlertBox] = useState<{ title: string; message: string } | null>(null);
  const alertResolver = useRef<(() => void) | null>(null);

  const promptPassword = (ids: string[]) => {
    const { promise, resolve } = Promise.withResolvers<string | null>();
    pwResolver.current = resolve;
    setPwValue("");
    setPwPrompt({ ids });
    return promise;
  };
  const settlePw = (v: string | null) => {
    pwResolver.current?.(v);
    pwResolver.current = null;
    setPwPrompt(null);
  };

  const showAlert = (title: string, message: string) => {
    const { promise, resolve } = Promise.withResolvers<void>();
    alertResolver.current = resolve;
    setAlertBox({ title, message });
    return promise;
  };
  const settleAlert = () => {
    alertResolver.current?.();
    alertResolver.current = null;
    setAlertBox(null);
  };

  /* ── Device polling: /api/batch/devices every 5s, errors ignored ── */
  useEffect(() => {
    let dead = false;
    const load = async () => {
      try {
        const list = await apiJSON<BatchDevice[]>("/api/batch/devices");
        if (!dead) setDevices(list);
      } catch {
        /* old page ignored fetch errors */
      }
    };
    load();
    const iv = setInterval(load, 5000);
    return () => {
      dead = true;
      clearInterval(iv);
    };
  }, []);

  /* Close any open batch sockets on unmount */
  useEffect(() => {
    const set = sockets.current;
    return () => {
      set.forEach((ws) => ws.close());
      set.clear();
    };
  }, []);

  const toggleDevice = (id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const toggleAll = () => {
    setSelected((prev) =>
      prev.size === devices.length ? new Set() : new Set(devices.map((d) => d.id)),
    );
  };

  const openBatchWS = () => {
    const ws = new WebSocket(wsURL("/batch"));
    ws.binaryType = "arraybuffer"; // for binary output frames
    sockets.current.add(ws);
    return ws;
  };
  const closeWS = (ws: WebSocket) => {
    sockets.current.delete(ws);
    ws.close();
  };

  /* Auth all selected locked devices over the batch socket. */
  const authorizeSelectedDevices = async (
    ws: WebSocket,
    deviceList: string[],
  ): Promise<boolean> => {
    const locked = deviceList
      .map((id) => devicesRef.current.find((d) => d.id === id))
      .filter((d): d is BatchDevice => !!d && d.hasPassword);
    if (!locked.length) return true;
    if (await deviceAuthBypassed()) return true;
    const saved = locked.map((d) => getDevicePassword(d.id)).find(Boolean);
    if (saved) {
      let allSavedOk = true;
      for (const device of locked) {
        const ok = await authDevice(ws, device, saved);
        if (!ok) {
          allSavedOk = false;
          forgetDevicePassword(device.id);
        }
      }
      if (allSavedOk) return true;
      await showAlert(tRef.current("common.error"), tRef.current("common.passwordWrong"));
    }
    const password = await promptPassword(locked.map((d) => d.id));
    if (password === null) return false;
    for (const device of locked) {
      const ok = await authDevice(ws, device, password);
      if (!ok) {
        forgetDevicePassword(device.id);
        await showAlert(
          tRef.current("common.error"),
          tRef.current("batch.wrongPassword", { id: device.id }),
        );
        return false;
      }
      rememberDevicePassword(device.id, password);
    }
    return true;
  };

  /* ── Batch exec ── */

  const runBatchExec = () => {
    const command = cmd.trim();
    if (!command || selected.size === 0) return;
    const deviceList = [...selected];
    setExecErrors([]);
    setExecResults(
      deviceList.map((id) => ({
        id,
        status: "running",
        label: t("batch.running"),
        output: "",
      })),
    );

    const ws = openBatchWS();

    ws.onopen = async () => {
      if (!(await authorizeSelectedDevices(ws, deviceList))) {
        closeWS(ws);
        return;
      }
      ws.send(JSON.stringify({ op: "exec", devices: deviceList, command }));
    };

    ws.onmessage = (evt: MessageEvent) => {
      if (evt.data instanceof ArrayBuffer) {
        // Binary frame = exec output [0x01] [devIdLen] [devId] [raw output]
        const { devId, payload } = decodeBinFrame(evt.data);
        const text = new TextDecoder().decode(payload);
        setExecResults((prev) =>
          prev.map((r) => (r.id === devId ? { ...r, output: r.output + text } : r)),
        );
        return;
      }
      // Text frame = JSON control
      const msg = JSON.parse(evt.data as string) as {
        op?: string;
        deviceId?: string;
        code?: number;
        message?: string;
      };
      if (msg.deviceId && msg.op === "exec_exit") {
        const code = msg.code ?? -1;
        setExecResults((prev) => {
          const next = prev.map((r): ExecResult =>
            r.id === msg.deviceId
              ? {
                  ...r,
                  status: code === 0 ? "ok" : "fail",
                  label: tRef.current("batch.exitCode", { code }),
                }
              : r,
          );
          if (next.length > 0 && next.every((r) => r.status !== "running")) {
            closeWS(ws);
          }
          return next;
        });
      } else if (msg.op === "error" || msg.op === "auth_required") {
        if (msg.deviceId) {
          const line = "\n[" + tRef.current("common.error") + ": " + msg.message + "]";
          setExecResults((prev) =>
            prev.map((r) =>
              r.id === msg.deviceId
                ? {
                    ...r,
                    output: r.output + line,
                    ...(r.status === "running"
                      ? {
                          status: "fail" as const,
                          label:
                            msg.op === "auth_required"
                              ? tRef.current("batch.authRequired")
                              : tRef.current("common.error"),
                        }
                      : {}),
                  }
                : r,
            ),
          );
        } else {
          setExecErrors((prev) => [
            ...prev,
            tRef.current("common.error") + ": " + msg.message,
          ]);
        }
      }
    };

    ws.onerror = () => {};
    ws.onclose = () => {
      sockets.current.delete(ws);
    };
  };

  /* ── File distribution ── */

  const handleFile = (f: File) => {
    setFile(f);
    setDestPath((prev) => prev || "/tmp/" + f.name);
  };

  const runBatchUpload = () => {
    if (!file || selected.size === 0) return;
    const dest = destPath.trim();
    if (!dest) return;
    const deviceList = [...selected];
    setUploadResults(
      deviceList.map((id) => ({
        id,
        status: "running",
        label: t("batch.uploading"),
        path: dest,
      })),
    );

    const ws = openBatchWS();
    const uploadId = `upload-${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
    const blob = file;

    ws.onopen = async () => {
      if (!(await authorizeSelectedDevices(ws, deviceList))) {
        closeWS(ws);
        return;
      }
      ws.send(encodeFileStart(uploadId, { devices: deviceList, path: dest, mode: 420 }, 420));
      for (let offset = 0; offset < blob.size; offset += 512 * 1024) {
        const chunk = await blob.slice(offset, offset + 512 * 1024).arrayBuffer();
        ws.send(encodeChunk(uploadId, chunk));
        const ack = Promise.withResolvers<void>();
        uploadAcks.current.set(uploadId, ack.resolve);
        await ack.promise;
      }
      ws.send(encodeEnd(uploadId));
    };

    ws.onmessage = (evt: MessageEvent) => {
      if (evt.data instanceof ArrayBuffer) {
        const { typ, devId } = decodeBinFrame(evt.data);
        if (typ === 0x08 && uploadAcks.current.has(devId)) {
          uploadAcks.current.get(devId)!();
          uploadAcks.current.delete(devId);
        }
        return;
      }
      const msg = JSON.parse(evt.data as string) as {
        op?: string;
        deviceId?: string;
        success?: boolean;
        message?: string;
      };
      if (msg.deviceId && msg.op === "upload_result") {
        setUploadResults((prev) => {
          const next = prev.map((r): UploadResult =>
            r.id === msg.deviceId
              ? {
                  ...r,
                  status: msg.success ? "ok" : "fail",
                  label: msg.success
                    ? tRef.current("common.ok")
                    : msg.message || tRef.current("common.failed"),
                }
              : r,
          );
          if (next.length > 0 && next.every((r) => r.status !== "running")) {
            closeWS(ws);
          }
          return next;
        });
      } else if ((msg.op === "error" || msg.op === "auth_required") && msg.deviceId) {
        setUploadResults((prev) => {
          const next = prev.map((r): UploadResult =>
            r.id === msg.deviceId
              ? {
                  ...r,
                  status: "fail",
                  label:
                    msg.message ||
                    (msg.op === "auth_required"
                      ? tRef.current("batch.authRequired")
                      : tRef.current("common.error")),
                }
              : r,
          );
          if (next.length > 0 && next.every((r) => r.status !== "running")) {
            closeWS(ws);
          }
          return next;
        });
      }
    };

    ws.onerror = () => {};
    ws.onclose = () => {
      sockets.current.delete(ws);
    };
  };

  const hasSelection = selected.size > 0;

  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto flex w-full max-w-6xl flex-col gap-3 p-4">
        <button
          type="button"
          aria-label={t("nav.label")}
          onClick={() => openMobileNav()}
          className="flex size-8 cursor-pointer items-center justify-center self-start rounded-md text-muted-foreground transition-colors hover:bg-state-hover hover:text-foreground md:hidden"
        >
          <Icon name="AlignLeft" className="size-5" />
        </button>
        {/* Device selector */}
        <Card>
          <CardHeader className="p-4 pb-2">
            <CardTitle className="flex items-center gap-2 text-sm text-primary">
              <Icon name="Radio" className="size-4" />
              {t("batch.devices")}
            </CardTitle>
          </CardHeader>
          <CardContent className="p-4 pt-0">
            <Button
              variant="ghost"
              size="sm"
              className="mb-1 h-7 px-2 text-xs text-muted-foreground"
              onClick={toggleAll}
            >
              {t("batch.selectAll")}
            </Button>
            {devices.length === 0 ? (
              <div className="text-sm text-muted-foreground">
                {t("common.noDevices")}
              </div>
            ) : (
              <div className="grid grid-cols-[repeat(auto-fill,minmax(220px,1fr))] gap-1.5">
                {devices.map((d) => {
                  const sel = selected.has(d.id);
                  return (
                    <label
                      key={d.id}
                      className={cn(
                        "flex cursor-pointer items-center gap-2 rounded-md border px-2.5 py-1.5 text-sm transition-colors select-none",
                        sel
                          ? "border-primary bg-surface-selected"
                          : "border-transparent hover:bg-state-hover",
                      )}
                    >
                      <input
                        type="checkbox"
                        checked={sel}
                        onChange={() => toggleDevice(d.id)}
                        className="accent-primary"
                      />
                      <span className="size-1.5 shrink-0 rounded-full bg-success shadow-[0_0_5px_var(--success)]" />
                      <span className="flex min-w-0 items-center gap-1.5">
                        <OsIcon os={d.os} className="text-muted-foreground" />
                        <span className="truncate">{d.id}</span>
                        {d.hasPassword && (
                          <Icon
                            name="Lock"
                            className="size-3 shrink-0 text-muted-foreground"
                          />
                        )}
                      </span>
                    </label>
                  );
                })}
              </div>
            )}
          </CardContent>
        </Card>

        {/* Tabs: exec / upload */}
        <Tabs defaultValue="exec">
          <TabsList>
            <TabsTrigger value="exec" className="gap-1.5">
              <Icon name="Terminal" className="size-3.5" />
              {t("batch.commandTab")}
            </TabsTrigger>
            <TabsTrigger value="upload" className="gap-1.5">
              <Icon name="Folder" className="size-3.5" />
              {t("batch.uploadTab")}
            </TabsTrigger>
          </TabsList>

          <TabsContent value="exec">
            <Card>
              <CardContent className="flex flex-col gap-3 p-4">
                <div className="flex flex-col gap-2 sm:flex-row">
                  <Input
                    value={cmd}
                    onChange={(e) => setCmd(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") runBatchExec();
                    }}
                    placeholder={t("batch.commandPlaceholder")}
                    spellCheck={false}
                    autoComplete="off"
                    className="flex-1 font-mono"
                  />
                  <Button onClick={runBatchExec} disabled={!hasSelection}>
                    {t("batch.run")}
                  </Button>
                </div>
                {execResults.length > 0 && (
                  <div className="grid grid-cols-[repeat(auto-fit,minmax(320px,1fr))] gap-2">
                    {execResults.map((r) => (
                      <ResultCard key={r.id} id={r.id} status={r.status} label={r.label}>
                        {r.output}
                      </ResultCard>
                    ))}
                  </div>
                )}
                {execErrors.map((err, i) => (
                  <div key={i} className="px-2 py-1 text-sm text-destructive-text">
                    {err}
                  </div>
                ))}
              </CardContent>
            </Card>
          </TabsContent>

          <TabsContent value="upload">
            <Card>
              <CardContent className="flex flex-col gap-3 p-4">
                <div
                  onClick={() => fileInputRef.current?.click()}
                  onDragOver={(e) => {
                    e.preventDefault();
                    setDragOver(true);
                  }}
                  onDragLeave={() => setDragOver(false)}
                  onDrop={(e) => {
                    e.preventDefault();
                    setDragOver(false);
                    const f = e.dataTransfer.files[0];
                    if (f) handleFile(f);
                  }}
                  className={cn(
                    "cursor-pointer rounded-lg border-2 border-dashed border-border p-8 text-center transition-colors",
                    dragOver && "border-primary bg-surface-selected",
                  )}
                >
                  <Icon
                    name="Upload"
                    className="mx-auto mb-2 size-8 text-muted-foreground opacity-40"
                  />
                  <div className="text-sm text-muted-foreground">
                    {t("batch.dropHint")}
                  </div>
                  {file && (
                    <div className="mt-1 text-sm font-semibold text-primary">
                      {file.name} ({(file.size / 1024).toFixed(1)} KB)
                    </div>
                  )}
                </div>
                <input
                  ref={fileInputRef}
                  type="file"
                  className="hidden"
                  onChange={(e) => {
                    const f = e.target.files?.[0];
                    if (f) handleFile(f);
                  }}
                />
                <div className="flex flex-col items-stretch gap-2 sm:flex-row sm:items-center">
                  <Icon
                    name="Upload"
                    className="hidden size-4 shrink-0 text-muted-foreground sm:block"
                  />
                  <Input
                    value={destPath}
                    onChange={(e) => setDestPath(e.target.value)}
                    placeholder={t("batch.destPlaceholder")}
                    spellCheck={false}
                    autoComplete="off"
                    className="flex-1 font-mono"
                  />
                  <Button
                    onClick={runBatchUpload}
                    disabled={!hasSelection || !file}
                  >
                    {t("batch.distribute")}
                  </Button>
                </div>
                {uploadResults.length > 0 && (
                  <div className="grid grid-cols-[repeat(auto-fit,minmax(320px,1fr))] gap-2">
                    {uploadResults.map((r) => (
                      <ResultCard key={r.id} id={r.id} status={r.status} label={r.label}>
                        {r.path}
                      </ResultCard>
                    ))}
                  </div>
                )}
              </CardContent>
            </Card>
          </TabsContent>
        </Tabs>
      </div>

      {/* Device password prompt */}
      <AlertDialog open={!!pwPrompt}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("common.password")}</AlertDialogTitle>
            <AlertDialogDescription>
              {pwPrompt &&
                t("batch.passwordRequired", { ids: pwPrompt.ids.join(", ") })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              settlePw(pwValue);
            }}
          >
            <Input
              type="password"
              autoFocus
              autoComplete="off"
              value={pwValue}
              onChange={(e) => setPwValue(e.target.value)}
            />
            <AlertDialogFooter className="mt-4">
              <AlertDialogCancel type="button" onClick={() => settlePw(null)}>
                {t("common.cancel")}
              </AlertDialogCancel>
              <AlertDialogAction type="submit">
                {t("common.ok")}
              </AlertDialogAction>
            </AlertDialogFooter>
          </form>
        </AlertDialogContent>
      </AlertDialog>

      {/* Generic alert */}
      <AlertDialog open={!!alertBox}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{alertBox?.title}</AlertDialogTitle>
            <AlertDialogDescription>{alertBox?.message}</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogAction onClick={settleAlert}>
              {t("common.ok")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
