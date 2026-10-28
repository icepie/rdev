import {
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { Link } from "react-router-dom";
import { MobileNavContext } from "../shell/AppShell";
import { useI18n } from "../lib/i18n";
import { useDevices } from "../lib/devices";
import { apiFetch, apiJSON, authURL, type DeviceInfo } from "../lib/api";
import { cn } from "../lib/utils";
import { Button } from "../components/button";
import { Input } from "../components/input";
import { Card, CardContent, CardHeader, CardTitle } from "../components/card";
import { Badge } from "../components/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../components/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../components/tabs";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "../components/dialog";
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "../components/select";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "../components/tooltip";
import { Icon } from "../components/icon";
import { OsIcon } from "../components/os-icon";
import { CodeBlock } from "../components/markdown";
import { useCopy } from "../lib/clipboard";

/* ── Server endpoint helpers (ported from web/index.html) ── */

const H = location.host;
const isHTTPS = location.protocol === "https:";
const WS = isHTTPS ? "wss://" : "ws://";
const HTTP = isHTTPS ? "https://" : "http://";

interface ServerConfig {
  sshPort?: string;
  tcpPort?: string;
  kcpPort?: string;
  vncAddr?: string;
  proxyAddr?: string;
}

interface DesktopCaps {
  platform?: string;
  displayServer?: string;
  supported?: boolean;
  backends?: string[];
  reason?: string;
}

interface LogDeviceState {
  id: string;
  enabled?: boolean;
  level?: string;
  size?: number;
  latest?: string;
  supported?: boolean;
}

type ClientFlavor = "go" | "cgo" | "rs";

const LOG_LEVELS = ["error", "warn", "info", "debug", "trace"] as const;

function vncDisplayAddr(addr: string): string {
  if (!addr) return "";
  const host = location.hostname || H.split(":")[0];
  if (addr.startsWith("0.0.0.0:") || addr.startsWith(":"))
    return host + ":" + addr.split(":").pop();
  if (addr.startsWith("[::]:")) return host + ":" + addr.slice(5);
  return addr;
}

function formatBytes(n: number | undefined): string {
  let v = Number(n || 0);
  if (v < 1024) return v + " B";
  if (v < 1024 * 1024) return (v / 1024).toFixed(1) + " KB";
  if (v < 1024 * 1024 * 1024) return (v / 1024 / 1024).toFixed(1) + " MB";
  return (v / 1024 / 1024 / 1024).toFixed(1) + " GB";
}

function randomPassword(): string {
  const alphabet =
    "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  return Array.from(bytes, (byte) => alphabet[byte & 63]).join("");
}

function ago(iso: string, t: (k: string, v?: Record<string, string | number>) => string): string {
  const s = Math.floor((Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 60) return t("common.justNow");
  if (s < 3600) return t("common.minutesAgo", { n: Math.floor(s / 60) });
  if (s < 86400) return t("common.hoursAgo", { n: Math.floor(s / 3600) });
  return t("common.daysAgo", { n: Math.floor(s / 86400) });
}

/* ── Copy buttons with transient check feedback ── */

function CopyButton({
  id,
  text,
  label,
  copiedId,
  onCopy,
}: {
  id: string;
  text: string;
  label: string;
  copiedId: string | null;
  onCopy: (id: string, text: string) => void;
}) {
  const ok = copiedId === id;
  return (
    <Button
      variant="outline"
      size="sm"
      className={cn("shrink-0", ok && "border-success text-success")}
      onClick={() => onCopy(id, text)}
    >
      {ok ? <Icon name="Check" /> : label}
    </Button>
  );
}

function CmdGroup({
  label,
  icon,
  cmd,
  cmdId,
  copiedId,
  onCopy,
  onCopyRandom,
  t,
}: {
  label: string;
  icon?: string;
  cmd: string;
  cmdId: string;
  copiedId: string | null;
  onCopy: (id: string, text: string) => void;
  onCopyRandom?: (id: string, text: string) => void;
  t: (k: string) => string;
}) {
  return (
    <div className="mb-4 last:mb-0">
      <div className="mb-1.5 flex items-center gap-1.5 text-xs text-muted-foreground">
        {icon && <Icon name={icon} className="size-3.5" />}
        <span>{label}</span>
      </div>
      <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
        <CodeBlock className="flex-1">{cmd}</CodeBlock>
        <div className="flex shrink-0 gap-2">
          <CopyButton
            id={cmdId}
            text={cmd}
            label={t("common.copy")}
            copiedId={copiedId}
            onCopy={onCopy}
          />
          {onCopyRandom && (
            <CopyButton
              id={cmdId + ":rp"}
              text={cmd}
              label={t("index.copyRandomPassword")}
              copiedId={copiedId}
              onCopy={onCopyRandom}
            />
          )}
        </div>
      </div>
    </div>
  );
}

/* ── Desktop capability badge ── */

function DesktopBadge({
  d,
  t,
}: {
  d: DeviceInfo;
  t: (k: string) => string;
}) {
  const desktop = d.desktop as DesktopCaps | undefined | null;
  let text: string;
  let ready = false;
  if (d.gpuDesktop) {
    text = t("index.desktopReady");
    ready = true;
  } else if (!desktop) {
    text = t("index.desktopUnavailable");
  } else if (desktop.supported) {
    text = t("index.desktopReady");
    ready = true;
  } else if (desktop.backends && desktop.backends.length) {
    text = t("index.desktopLimited");
  } else {
    text = t("index.desktopUnavailable");
  }
  const title = desktop
    ? [
        desktop.platform,
        desktop.displayServer,
        desktop.backends && desktop.backends.join(","),
        desktop.reason,
      ]
        .filter(Boolean)
        .join(" · ")
    : "";
  const badge = (
    <Badge
      variant="outline"
      className={cn(
        "border-transparent",
        ready
          ? "bg-success/15 text-success"
          : "bg-attention/15 text-attention",
      )}
    >
      {text}
    </Badge>
  );
  if (!title) return badge;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="inline-flex">{badge}</span>
      </TooltipTrigger>
      <TooltipContent>{title}</TooltipContent>
    </Tooltip>
  );
}

/* ── Page ── */

export function DashboardPage() {
  const { t } = useI18n();
  const { open: openMobileNav } = useContext(MobileNavContext);
  const { devices, refresh: refreshDevices } = useDevices();

  const [cfg, setCfg] = useState<Required<ServerConfig>>({
    sshPort: "2222",
    tcpPort: "8081",
    kcpPort: "8082",
    vncAddr: "",
    proxyAddr: "",
  });
  const [flavor, setFlavor] = useState<ClientFlavor>("go");
  const [logStates, setLogStates] = useState<Record<string, LogDeviceState>>(
    {},
  );
  const [logDevice, setLogDevice] = useState("");
  const [logLevel, setLogLevel] = useState("info");
  const [logTail, setLogTail] = useState("");
  const [logDate, setLogDate] = useState(() =>
    new Date().toISOString().slice(0, 10),
  );
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [androidTrying, setAndroidTrying] = useState(false);
  const [copiedId, copy] = useCopy();

  /* Server config (ports / VNC address) */
  useEffect(() => {
    apiJSON<ServerConfig>("/api/config")
      .then((c) => {
        setCfg((prev) => ({
          sshPort: c.sshPort || prev.sshPort,
          tcpPort: c.tcpPort || prev.tcpPort,
          kcpPort: c.kcpPort || prev.kcpPort,
          vncAddr: c.vncAddr ? vncDisplayAddr(c.vncAddr) : prev.vncAddr,
          proxyAddr: c.proxyAddr ? vncDisplayAddr(c.proxyAddr) : prev.proxyAddr,
        }));
      })
      .catch(() => {});
  }, []);

  const SP = cfg.sshPort;
  const VP = cfg.vncAddr;
  const PP = cfg.proxyAddr;

  const serverList = useCallback((): string => {
    const host = location.hostname || H.split(":")[0];
    const wsEndpoint = WS + H;
    const tcpEndpoint = cfg.tcpPort ? `tcp://${host}:${cfg.tcpPort}` : "";
    const kcpEndpoint = cfg.kcpPort ? `kcp://${host}:${cfg.kcpPort}` : "";
    return [tcpEndpoint, kcpEndpoint, wsEndpoint].filter(Boolean).join(",");
  }, [cfg.tcpPort, cfg.kcpPort]);

  const pwPlaceholder = t("index.passwordPlaceholder");
  const clientArg = flavor === "go" ? "" : " --client " + flavor;
  const clientPSArg = flavor === "go" ? "" : " -Client " + flavor;

  /* Install commands */
  const cmds = useMemo(() => {
    const servers = serverList();
    return {
      curl: `curl -sL ${HTTP}${H}/run.sh | sh -s -- ${servers} -p ${pwPlaceholder}${clientArg}`,
      wget: `wget -qO- ${HTTP}${H}/run.sh | sh -s -- ${servers} -p ${pwPlaceholder}${clientArg}`,
      openSh: `curl -sL ${HTTP}${H}/run.sh | sh -s -- ${servers}${clientArg}`,
      ps: `powershell -Command "iwr -useb ${HTTP}${H}/run.ps1 | iex; RDev '${servers}' -Password ${pwPlaceholder}${clientPSArg}"`,
      psOpen: `powershell -Command "iwr -useb ${HTTP}${H}/run.ps1 | iex; RDev '${servers}'${clientPSArg}"`,
      w7Path: `%SystemRoot%\\system32\\WindowsPowerShell\\v1.0\\powershell.exe`,
      w7Pw: `[Net.ServicePointManager]::SecurityProtocol=[Enum]::ToObject([Net.SecurityProtocolType],3072); $wc=New-Object Net.WebClient; $wc.DownloadString('${HTTP}${H}/run.ps1') | iex; RDev '${servers}' -Password ${pwPlaceholder}${clientPSArg}`,
      w7Open: `[Net.ServicePointManager]::SecurityProtocol=[Enum]::ToObject([Net.SecurityProtocolType],3072); $wc=New-Object Net.WebClient; $wc.DownloadString('${HTTP}${H}/run.ps1') | iex; RDev '${servers}'${clientPSArg}`,
    };
  }, [serverList, pwPlaceholder, clientArg, clientPSArg]);

  const copyRandom = useCallback(
    (id: string, cmd: string) => {
      copy(id, cmd.replaceAll(pwPlaceholder, randomPassword()));
    },
    [copy, pwPlaceholder],
  );

  /* Client log states (polled alongside the device list) */
  const loadLogStates = useCallback(async (): Promise<
    Record<string, LogDeviceState>
  > => {
    try {
      const list = await apiJSON<LogDeviceState[]>("/api/client-logs/devices");
      const map: Record<string, LogDeviceState> = {};
      for (const x of Array.isArray(list) ? list : []) map[x.id] = x;
      setLogStates(map);
      return map;
    } catch {
      return {};
    }
  }, []);

  useEffect(() => {
    loadLogStates();
  }, [loadLogStates, devices]);

  /* Stats */
  const totalSessions = devices.reduce(
    (s, d) => s + Number(d.sessions ?? 0),
    0,
  );
  const totalForwards = devices.reduce(
    (s, d) => s + Number(d.forwards ?? 0),
    0,
  );

  /* Device connection copy: admin login means we can resolve the real
   * device password (?passwords=1) and substitute it for the <password>
   * placeholders; a failed lookup keeps the placeholder. */
  const copyDeviceConnection = useCallback(
    async (d: DeviceInfo) => {
      let pw = "<password>";
      if (d.hasPassword) {
        try {
          const r = await apiFetch("/api/clients?passwords=1");
          if (r.ok) {
            const list = (await r.json()) as DeviceInfo[];
            const mine = list.find((c) => c.id === d.id);
            if (mine?.password) pw = mine.password;
          }
        } catch {
          /* keep placeholder */
        }
      }
      const sshHost = location.hostname || H.split(":")[0];
      const desktopURL = `${HTTP}${H}/remote-desktop?device=${encodeURIComponent(d.id)}`;
      const servers = serverList();
      const lines = [
        `RDev device: ${d.id}`,
        `Auth: ${d.hasPassword ? t("common.password") : t("common.open")}`,
        "",
        `Client endpoints: ${servers}`,
        `Client command: rdev-client -s ${servers} -i ${d.id}` +
          (d.hasPassword ? ` -p ${pw}` : ""),
        "",
        `SSH: ssh ${d.id}@${sshHost} -p ${SP}`,
        `SFTP: sftp -P ${SP} ${d.id}@${sshHost}`,
        `SCP: scp -P ${SP} file ${d.id}@${sshHost}:~/`,
        `Desktop: ${desktopURL}`,
      ];
      if (VP) lines.push(`VNC: vncviewer ${VP} (username=${d.id})`);
      if (PP)
        lines.push(
          `Proxy: curl -x http://${d.id}:${pw}@${PP} https://example.com`,
          `Proxy (SOCKS5): curl -x socks5://${d.id}:${pw}@${PP} https://example.com`,
        );
      lines.push(
        "",
        `Local forward: ssh -L 8080:localhost:80 ${d.id}@${sshHost} -p ${SP}`,
        `Remote forward: ssh -R 3000:localhost:3000 ${d.id}@${sshHost} -p ${SP}`,
        `Git clone: git clone ssh://${d.id}@${sshHost}:${SP}/home/user/repo.git`,
      );
      copy("conn:" + d.id, lines.join("\n"));
    },
    [copy, serverList, SP, VP, PP, t],
  );

  /* Client log modal */
  const refreshLogTail = useCallback(
    async (device: string) => {
      setLogTail(t("index.logLoading"));
      try {
        const r = await apiFetch(
          "/api/client-logs/devices/" +
            encodeURIComponent(device) +
            "/tail?lines=300",
        );
        if (!r.ok) {
          setLogTail(t("index.logTailError", { status: r.status }));
          return;
        }
        const data = (await r.json()) as { lines?: string[] };
        setLogTail((data.lines || []).join("\n") || t("index.logNoLines"));
      } catch {
        setLogTail(t("index.logNoLines"));
      }
    },
    [t],
  );

  const openLogModal = useCallback(
    async (device: string) => {
      setLogDevice(device);
      setLogTail(t("index.logLoading"));
      const states = await loadLogStates();
      const st = states[device];
      setLogLevel(st?.level || "info");
      await refreshLogTail(device);
    },
    [loadLogStates, refreshLogTail, t],
  );

  const setLogEnabled = useCallback(
    async (enabled: boolean) => {
      if (!logDevice) return;
      const r = await apiFetch(
        "/api/client-logs/devices/" +
          encodeURIComponent(logDevice) +
          "/config",
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ enabled, level: logLevel || "info" }),
        },
      );
      if (!r.ok) return;
      await loadLogStates();
      await refreshLogTail(logDevice);
      refreshDevices();
    },
    [logDevice, logLevel, loadLogStates, refreshLogTail, refreshDevices],
  );

  const downloadLog = useCallback(() => {
    if (!logDevice) return;
    location.href = authURL(
      "/api/client-logs/devices/" +
        encodeURIComponent(logDevice) +
        "/download?date=" +
        logDate,
    );
  }, [logDevice, logDate]);

  const deleteLogs = useCallback(async () => {
    if (!logDevice) return;
    const r = await apiFetch(
      "/api/client-logs/devices/" + encodeURIComponent(logDevice),
      { method: "DELETE" },
    );
    if (r.ok) {
      await loadLogStates();
      await refreshLogTail(logDevice);
      refreshDevices();
    }
  }, [logDevice, loadLogStates, refreshLogTail, refreshDevices]);

  const logState = logDevice ? logStates[logDevice] : undefined;

  const stats = [
    {
      label: t("index.onlineDevices"),
      value: String(devices.length),
      cls: "text-success",
    },
    {
      label: t("index.activeSessions"),
      value: String(totalSessions),
      cls: "text-timeline-accent",
    },
    {
      label: t("index.portForwards"),
      value: String(totalForwards),
      cls: "text-attention",
    },
    { label: t("index.sshPort"), value: SP, cls: "" },
  ];

  const flavorTags: { value: ClientFlavor; key: string }[] = [
    { value: "go", key: "index.clientCompat" },
    { value: "cgo", key: "index.clientFeature" },
    { value: "rs", key: "index.clientPerf" },
  ];

  const cmdProps = { copiedId, onCopy: copy, onCopyRandom: copyRandom, t };

  return (
    <div className="mx-auto flex w-full max-w-6xl flex-col gap-4 px-4 py-6">
      {/* Heading */}
      <div>
        <h1 className="flex items-center gap-2 text-2xl font-bold tracking-tight">
          <button
            type="button"
            aria-label={t("nav.label")}
            onClick={() => openMobileNav()}
            className="flex size-8 shrink-0 cursor-pointer items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-state-hover hover:text-foreground md:hidden"
          >
            <Icon name="AlignLeft" className="size-5" />
          </button>
          <span>
            <span className="text-timeline-accent">RDev</span>{" "}
            {t("index.heading")}
          </span>
        </h1>
        <p className="mt-1 text-sm text-muted-foreground">
          {t("index.subtitle")}
        </p>
      </div>

      {/* Stats */}
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        {stats.map((s) => (
          <Card key={s.label}>
            <CardContent className="p-4">
              <div className="text-xs tracking-wide text-muted-foreground uppercase">
                {s.label}
              </div>
              <div className={cn("mt-1 text-2xl font-bold", s.cls)}>
                {s.value}
              </div>
            </CardContent>
          </Card>
        ))}
      </div>

      {/* Devices */}
      <Card>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t("index.status")}</TableHead>
              <TableHead>{t("index.deviceId")}</TableHead>
              <TableHead className="hidden md:table-cell">{t("index.version")}</TableHead>
              <TableHead className="hidden md:table-cell">{t("index.connectedAt")}</TableHead>
              <TableHead className="hidden md:table-cell">{t("index.sessions")}</TableHead>
              <TableHead className="hidden md:table-cell">{t("index.forwards")}</TableHead>
              <TableHead>{t("index.auth")}</TableHead>
              <TableHead>{t("index.desktop")}</TableHead>
              <TableHead className="text-right">
                {t("files.actions")}
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {devices.length === 0 && (
              <TableRow>
                <TableCell colSpan={9} className="h-32 text-center">
                  <div className="flex flex-col items-center gap-2 text-muted-foreground">
                    <Icon name="Radio" className="size-8 opacity-30" />
                    <span className="text-sm">
                      {t("index.waitingDevices")}
                    </span>
                  </div>
                </TableCell>
              </TableRow>
            )}
            {devices.map((d) => {
              const st = logStates[d.id];
              const logSupported = Boolean(d.logSupported);
              return (
                <TableRow key={d.id}>
                  <TableCell>
                    <span className="inline-flex items-center gap-1.5">
                      <span className="size-2 rounded-full bg-success shadow-[0_0_6px_var(--success)]" />
                      {t("common.online")}
                    </span>
                  </TableCell>
                  <TableCell className="font-semibold">
                    <span className="inline-flex items-center gap-1.5">
                      <OsIcon os={d.os} className="size-4 text-muted-foreground" />
                      {d.id}
                    </span>
                  </TableCell>
                  <TableCell className="hidden text-muted-foreground md:table-cell">
                    {d.version || "-"}
                  </TableCell>
                  <TableCell className="hidden text-muted-foreground md:table-cell">
                    {ago(d.connectedAt, t)}
                  </TableCell>
                  <TableCell className="hidden md:table-cell">{Number(d.sessions ?? 0)}</TableCell>
                  <TableCell className="hidden md:table-cell">{Number(d.forwards ?? 0)}</TableCell>
                  <TableCell>
                    <Badge
                      variant="outline"
                      className={cn(
                        "border-transparent",
                        d.hasPassword
                          ? "bg-success/15 text-success"
                          : "bg-attention/15 text-attention",
                      )}
                    >
                      {d.hasPassword
                        ? t("common.password")
                        : t("common.open")}
                    </Badge>
                  </TableCell>
                  <TableCell>
                    <DesktopBadge d={d} t={t} />
                  </TableCell>
                  <TableCell>
                    <div className="flex flex-wrap items-center justify-end gap-1.5">
                      <Button variant="outline" size="sm" asChild>
                        <Link
                          to={`/terminal?device=${encodeURIComponent(d.id)}`}
                        >
                          <Icon name="Terminal" />
                          {t("index.navTerminal")}
                        </Link>
                      </Button>
                      <Button variant="outline" size="sm" asChild>
                        <Link
                          to={`/remote-desktop?device=${encodeURIComponent(d.id)}`}
                        >
                          <Icon name="Monitor" />
                          {t("index.navDesktop")}
                        </Link>
                      </Button>
                      <Button variant="outline" size="sm" asChild>
                        <Link to={`/files?device=${encodeURIComponent(d.id)}`}>
                          <Icon name="Folder" />
                          {t("index.navFiles")}
                        </Link>
                      </Button>
                      <Button variant="outline" size="sm" asChild>
                        <Link to={`/agent?device=${encodeURIComponent(d.id)}`}>
                          <Icon name="Bot" />
                          {t("index.agent")}
                        </Link>
                      </Button>
                      <Button
                        variant="outline"
                        size="sm"
                        className={cn(
                          copiedId === "conn:" + d.id &&
                            "border-success text-success",
                        )}
                        onClick={() => copyDeviceConnection(d)}
                      >
                        {copiedId === "conn:" + d.id ? (
                          <>
                            <Icon name="Check" />
                            {t("common.copied")}
                          </>
                        ) : (
                          t("index.copyConnection")
                        )}
                      </Button>
                      {logSupported ? (
                        <Button
                          variant="outline"
                          size="sm"
                          className={cn(
                            st?.enabled && "border-success/50 text-success",
                          )}
                          onClick={() => openLogModal(d.id)}
                        >
                          <Icon name="FileText" />
                          {st?.enabled ? t("index.logOn") : t("index.logs")}
                        </Button>
                      ) : (
                        <Tooltip>
                          <TooltipTrigger asChild>
                            <span>
                              <Button variant="outline" size="sm" disabled>
                                <Icon name="FileText" />
                                {t("index.logUnsupported")}
                              </Button>
                            </span>
                          </TooltipTrigger>
                          <TooltipContent>
                            {t("index.logUnsupportedTitle")}
                          </TooltipContent>
                        </Tooltip>
                      )}
                    </div>
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </Card>

      {/* One-click install */}
      <Card>
        <CardHeader className="flex-row flex-wrap items-center justify-between gap-3 space-y-0">
          <CardTitle className="flex items-center gap-2 text-base">
            <Icon name="Rocket" className="size-4 text-timeline-accent" />
            {t("index.installTitle")}
          </CardTitle>
          <div className="flex gap-1.5" aria-label="Client flavor">
            {flavorTags.map((f) => (
              <button
                key={f.value}
                type="button"
                onClick={() => setFlavor(f.value)}
                className={cn(
                  "cursor-pointer rounded-full border px-3 py-1 text-xs transition-colors",
                  flavor === f.value
                    ? "border-timeline-accent/50 bg-timeline-accent/10 text-timeline-accent"
                    : "border-border text-muted-foreground hover:text-foreground",
                )}
              >
                {t(f.key)}
              </button>
            ))}
          </div>
        </CardHeader>
        <CardContent>
          <Tabs defaultValue="linux">
            <TabsList className="scrollbar-none max-w-full justify-start overflow-x-auto">
              <TabsTrigger value="linux" className="gap-1.5">
                <OsIcon os="linux" />
                <OsIcon os="macos" />
                {t("index.tabLinux")}
              </TabsTrigger>
              <TabsTrigger value="win" className="gap-1.5">
                <OsIcon os="windows" />
                {t("index.tabWindows")}
              </TabsTrigger>
              <TabsTrigger value="win7" className="gap-1.5">
                <OsIcon os="windows" />
                {t("index.tabWinLegacy")}
              </TabsTrigger>
              <TabsTrigger value="android" className="gap-1.5">
                <OsIcon os="android" />
                {t("index.tabAndroid")}
              </TabsTrigger>
            </TabsList>

            <TabsContent value="linux" className="pt-2">
              <CmdGroup
                label={t("index.curlLabel")}
                icon="Package"
                cmd={cmds.curl}
                cmdId="curl"
                {...cmdProps}
              />
              <CmdGroup
                label={t("index.wgetLabel")}
                cmd={cmds.wget}
                cmdId="wget"
                {...cmdProps}
              />
              <CmdGroup
                label={t("index.openMode")}
                icon="LockOpen"
                cmd={cmds.openSh}
                cmdId="openSh"
                copiedId={copiedId}
                onCopy={copy}
                t={t}
              />
            </TabsContent>

            <TabsContent value="win" className="pt-2">
              <CmdGroup
                label={t("index.psLabel")}
                icon="Package"
                cmd={cmds.ps}
                cmdId="ps"
                {...cmdProps}
              />
              <CmdGroup
                label={t("index.openMode")}
                icon="LockOpen"
                cmd={cmds.psOpen}
                cmdId="psOpen"
                copiedId={copiedId}
                onCopy={copy}
                t={t}
              />
            </TabsContent>

            <TabsContent value="win7" className="pt-2">
              <CmdGroup
                label={t("index.win7Step1")}
                cmd={cmds.w7Path}
                cmdId="w7Path"
                copiedId={copiedId}
                onCopy={copy}
                t={t}
              />
              <CmdGroup
                label={t("index.win7Step2")}
                cmd={cmds.w7Pw}
                cmdId="w7Pw"
                {...cmdProps}
              />
              <CmdGroup
                label={t("index.openMode")}
                icon="LockOpen"
                cmd={cmds.w7Open}
                cmdId="w7Open"
                copiedId={copiedId}
                onCopy={copy}
                t={t}
              />
            </TabsContent>

            <TabsContent value="android" className="pt-2">
              <div className="mb-1.5 flex items-center gap-1.5 text-xs text-muted-foreground">
                <Icon name="Download" className="size-3.5" />
                <span>{t("index.androidDownloadLabel")}</span>
              </div>
              <div>
                <Button
                  variant="outline"
                  size="sm"
                  className="border-timeline-accent/40 bg-timeline-accent/10 text-timeline-accent hover:bg-timeline-accent/20"
                  onClick={() => {
                    setAndroidTrying(true);
                    location.href =
                      "/download-release-proxy?asset=" +
                      encodeURIComponent("rdev-android-apk-debug.apk") +
                      "&tag=latest";
                  }}
                >
                  {t("index.androidDownloadApk")}
                </Button>
              </div>
              <div className="mt-2 text-xs text-muted-foreground">
                {androidTrying
                  ? t("index.androidDownloadTrying")
                  : t("index.androidDownloadNote")}
              </div>
            </TabsContent>
          </Tabs>
        </CardContent>
      </Card>

      {/* Connection hints */}
      <Card>
        <CardContent className="flex flex-col gap-4 p-5">
          <HintSection icon="Terminal" title={t("index.sshTitle")}>
            <HintCode cmd={`ssh <deviceID>@${H} -p ${SP}`} />
            <HintCode cmd={`scp file <deviceID>@${H}:/tmp/ -P ${SP}`} />
            <HintCode cmd={`sftp -P ${SP} <deviceID>@${H}`} />
            <HintCode
              cmd={`rsync -az --delete -e "ssh -p ${SP}" ./local-dir/ <deviceID>@${H}:/tmp/remote-dir/`}
            />
          </HintSection>

          <HintSection icon="Monitor" title={t("index.remoteDesktopTitle")}>
            <HintCode
              cmd={`open ${HTTP}${H}/remote-desktop?device=<deviceID>`}
            />
            {VP && (
              <>
                <HintCode cmd={`vncviewer ${VP}`} />
                <HintComment text={t("index.vncAuthComment")} />
              </>
            )}
          </HintSection>

          {PP && (
            <HintSection icon="Globe" title={t("index.proxyTitle")}>
              <HintCode
                cmd={`curl -x http://<deviceID>:<password>@${PP} https://example.com`}
              />
              <HintCode
                cmd={`curl -x socks5://<deviceID>:<password>@${PP} https://example.com`}
              />
              <HintComment text={t("index.proxyAuthComment")} />
            </HintSection>
          )}

          <HintSection icon="Monitor" title={t("index.forwardTitle")}>
            <HintComment text={t("index.localForwardComment")} />
            <HintCode cmd={`ssh -L 8080:localhost:80 <deviceID>@${H} -p ${SP}`} />
            <HintComment text={t("index.remoteForwardComment")} />
            <HintCode
              cmd={`ssh -R 3000:localhost:3000 <deviceID>@${H} -p ${SP}`}
            />
          </HintSection>

          <HintSection icon="Github" title={t("index.gitTitle")}>
            <HintComment text={t("index.gitCloneComment")} />
            <HintCode
              cmd={`git clone ssh://<deviceID>@${H}:${SP}/home/user/repo.git`}
            />
            <HintComment text={t("index.gitPushComment")} />
            <HintCode
              cmd={`git remote add device ssh://<deviceID>@${H}:${SP}/home/user/repo.git`}
            />
          </HintSection>

          <HintSection icon="Key" title={t("index.keyAuthTitle")}>
            <HintComment text={t("index.keyAuthComment")} />
            <HintCode cmd="cat ~/.ssh/id_ed25519.pub >> ~/.rdev/authorized_keys" />
            <HintCode
              cmd={`ssh <deviceID>@${H} -p ${SP}`}
              suffix={t("index.noPasswordComment")}
            />
          </HintSection>
        </CardContent>
      </Card>

      {/* Client log modal */}
      <Dialog
        open={!!logDevice}
        onOpenChange={(o) => {
          if (!o) setLogDevice("");
        }}
      >
        <DialogContent className="flex max-h-[86vh] flex-col overflow-hidden sm:max-w-4xl">
          <DialogHeader>
            <DialogTitle className="text-timeline-accent">
              {t("index.logTitle")} · {logDevice}
            </DialogTitle>
          </DialogHeader>
          <div className="flex flex-wrap items-center gap-2">
            <Select value={logLevel} onValueChange={setLogLevel}>
              <SelectTrigger className="h-8 w-28 text-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {LOG_LEVELS.map((lv) => (
                  <SelectItem key={lv} value={lv}>
                    {lv}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button
              variant="outline"
              size="sm"
              onClick={() => setLogEnabled(true)}
            >
              {t("index.logEnable")}
            </Button>
            <Button
              variant="outline"
              size="sm"
              className="border-attention/50 text-attention"
              onClick={() => setLogEnabled(false)}
            >
              {t("index.logDisable")}
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() => refreshLogTail(logDevice)}
            >
              <Icon name="Refresh" />
              {t("index.logRefresh")}
            </Button>
            <Input
              type="date"
              value={logDate}
              onChange={(e) => setLogDate(e.target.value)}
              className="h-8 w-36 text-xs"
            />
            <Button variant="outline" size="sm" onClick={downloadLog}>
              <Icon name="Download" />
              {t("index.logDownload")}
            </Button>
            <Button
              variant="outline"
              size="sm"
              className="border-destructive/50 text-destructive-text"
              onClick={() => setDeleteOpen(true)}
            >
              <Icon name="Trash2" />
              {t("index.logDelete")}
            </Button>
          </div>
          <div className="text-xs text-muted-foreground">
            {logState
              ? t("index.logMeta", {
                  status: logState.enabled
                    ? t("index.logStatusOn")
                    : t("index.logStatusOff"),
                  size: formatBytes(logState.size),
                  latest: logState.latest || "-",
                  supported:
                    logState.supported === false
                      ? t("index.logNo")
                      : t("index.logYes"),
                })
              : "—"}
          </div>
          <CodeBlock className="max-h-[58vh] min-h-[360px] overflow-auto p-3 whitespace-pre-wrap break-words select-text">
            {logTail || t("index.logEmpty")}
          </CodeBlock>
        </DialogContent>
      </Dialog>

      {/* Delete logs confirm */}
      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("index.logDeleteTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("index.logDeleteDesc", { id: logDevice })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                void deleteLogs();
              }}
            >
              {t("common.ok")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

/* ── Hint building blocks ── */

function HintSection({
  icon,
  title,
  children,
}: {
  icon: string;
  title: string;
  children: ReactNode;
}) {
  return (
    <section>
      <h3 className="mb-2 flex items-center gap-1.5 text-sm font-semibold text-timeline-accent">
        <Icon name={icon} className="size-4" />
        {title}
      </h3>
      <div className="flex flex-col gap-1">{children}</div>
    </section>
  );
}

function HintCode({ cmd, suffix }: { cmd: string; suffix?: string }) {
  const space = cmd.indexOf(" ");
  const head = space === -1 ? cmd : cmd.slice(0, space);
  const rest = space === -1 ? "" : cmd.slice(space);
  return (
    <CodeBlock className="py-1.5 text-muted-foreground">
      <span className="font-semibold text-foreground">{head}</span>
      {rest}
      {suffix && <span className="text-success">{"  " + suffix}</span>}
    </CodeBlock>
  );
}

function HintComment({ text }: { text: string }) {
  return (
    <CodeBlock className="py-1.5 text-attention">{text}</CodeBlock>
  );
}
