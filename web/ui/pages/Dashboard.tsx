import { legacyWindowsBootstrap } from "../lib/windows-bootstrap";
import {
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { Link } from "react-router-dom";
import { MobileNavContext } from "../shell/AppShell";
import { useI18n } from "../lib/i18n";
import { useDevices } from "../lib/devices";
import { apiFetch, apiJSON, type DeviceInfo } from "../lib/api";
import { cn } from "../lib/utils";
import { Button } from "../components/button";
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
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "../components/dropdown-menu";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "../components/tooltip";
import { Icon } from "../components/icon";
import { OsIcon } from "../components/os-icon";
import { CodeBlock } from "../components/markdown";
import { useCopy } from "../lib/clipboard";
import { COARSE_POINTER_HEADER_ICON_BUTTON_CLASS } from "../components/coarse-pointer-sizing";

/* ── Server endpoint helpers (ported from web/index.html) ── */

const H = location.host;
const SSH_HOST = location.hostname;
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

type ClientFlavor = "go" | "cgo" | "rs";

function shellQuote(value: string): string {
  return `'${value.replaceAll("'", "'\\''")}'`;
}

function vncDisplayAddr(addr: string): string {
  if (!addr) return "";
  const host = location.hostname || H.split(":")[0];
  if (addr.startsWith("0.0.0.0:") || addr.startsWith(":"))
    return host + ":" + addr.split(":").pop();
  if (addr.startsWith("[::]:")) return host + ":" + addr.slice(5);
  return addr;
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
      aria-label={label}
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
        <CodeBlock className="min-w-0 flex-1">{cmd}</CodeBlock>
        <div className="flex shrink-0 flex-wrap gap-2">
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
        <span className="inline-flex" tabIndex={0}>{badge}</span>
      </TooltipTrigger>
      <TooltipContent>{title}</TooltipContent>
    </Tooltip>
  );
}

/* ── Page ── */

export function DashboardPage() {
  const { t } = useI18n();
  const { open: openMobileNav } = useContext(MobileNavContext);
  const { devices, error: devicesError, refresh: refreshDevices } = useDevices();

  const [cfg, setCfg] = useState<Required<ServerConfig>>({
    sshPort: "2222",
    tcpPort: "8081",
    kcpPort: "8082",
    vncAddr: "",
    proxyAddr: "",
  });
  const [flavor, setFlavor] = useState<ClientFlavor>("go");
  const [androidTrying, setAndroidTrying] = useState(false);
  const [configError, setConfigError] = useState("");
  const [copiedId, copy] = useCopy();

  /* Server config (ports / VNC address) */
  useEffect(() => {
    apiJSON<ServerConfig>("/api/config")
      .then((c) => {
        setCfg((prev) => ({
          sshPort: c.sshPort ?? prev.sshPort,
          tcpPort: c.tcpPort ?? prev.tcpPort,
          kcpPort: c.kcpPort ?? prev.kcpPort,
          vncAddr: c.vncAddr === undefined ? prev.vncAddr : vncDisplayAddr(c.vncAddr),
          proxyAddr: c.proxyAddr === undefined ? prev.proxyAddr : vncDisplayAddr(c.proxyAddr),
        }));
      })
      .catch((error: unknown) => setConfigError(String(error)));
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
    return {
      curl: `curl -sL '${HTTP}${H}/run.sh' | sh -s -- -p '${pwPlaceholder}'${clientArg}`,
      wget: `wget -qO- '${HTTP}${H}/run.sh' | sh -s -- -p '${pwPlaceholder}'${clientArg}`,
      openSh: `curl -sL '${HTTP}${H}/run.sh' | sh${clientArg ? ' -s --' + clientArg : ''}`,
      ps: `powershell -Command "iwr -useb '${HTTP}${H}/run.ps1' | iex; RDev -Password '${pwPlaceholder}'${clientPSArg}"`,
      psOpen: `powershell -Command "iwr -useb '${HTTP}${H}/run.ps1' | iex; RDev${clientPSArg}"`,
      w7Path: `%SystemRoot%\\system32\\WindowsPowerShell\\v1.0\\powershell.exe`,
      w7Pw: `${legacyWindowsBootstrap(`${HTTP}${H}/run.ps1`)}; RDev -Password '${pwPlaceholder}'${clientPSArg}`,
      w7Open: `${legacyWindowsBootstrap(`${HTTP}${H}/run.ps1`)}; RDev${clientPSArg}`,
    };
  }, [pwPlaceholder, clientArg, clientPSArg]);

  const copyRandom = useCallback(
    (id: string, cmd: string) => {
      copy(id, cmd.replaceAll(pwPlaceholder, randomPassword()));
    },
    [copy, pwPlaceholder],
  );

  /* Stats */
  const totalSessions = devices.reduce(
    (s, d) => s + Number(d.sessions ?? 0),
    0,
  );
  const totalForwards = devices.reduce(
    (s, d) => s + Number(d.forwards ?? 0),
    0,
  );

  /* Device connection strings: admin login means we can resolve the real
   * device password (?passwords=1) and substitute it for the <password>
   * placeholders; a failed lookup keeps the placeholder. */
  type ConnStrings = {
    ssh: string;
    client: string;
    desktop: string;
    full: string;
  };
  const deviceConnStrings = useCallback(
    async (d: DeviceInfo, includePassword: boolean): Promise<ConnStrings> => {
      let pw = "<password>";
      if (d.hasPassword && includePassword) {
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
      const ssh = `ssh -p ${SP} ${shellQuote(`${d.id}@${sshHost}`)}`;
      const client =
        `rdev-client -s ${shellQuote(servers)} -i ${shellQuote(d.id)}` +
        (d.hasPassword ? ` -p ${shellQuote(pw)}` : "");
      const lines = [
        `RDev device: ${d.id}`,
        `Auth: ${d.hasPassword ? t("common.password") : t("common.open")}`,
        "",
        `Client endpoints: ${servers}`,
        `Client command: ${client}`,
        "",
        `SSH: ${ssh}`,
        `SFTP: sftp -P ${SP} ${shellQuote(`${d.id}@${sshHost}`)}`,
        `SCP: scp -P ${SP} file ${shellQuote(`${d.id}@${sshHost}:~/`)}`,
        `Desktop: ${desktopURL}`,
      ];
      if (VP) lines.push(`VNC: vncviewer ${VP} (username=${d.id})`);
      if (PP)
        lines.push(
          `Proxy: curl -x ${shellQuote(`http://${encodeURIComponent(d.id)}:${encodeURIComponent(d.hasPassword ? pw : "")}@${PP}`)} https://example.com`,
          `Proxy (SOCKS5): curl -x ${shellQuote(`socks5://${encodeURIComponent(d.id)}:${encodeURIComponent(d.hasPassword ? pw : "")}@${PP}`)} https://example.com`,
        );
      lines.push(
        "",
        `Local forward: ssh -L 8080:localhost:80 -p ${SP} ${shellQuote(`${d.id}@${sshHost}`)}`,
        `Remote forward: ssh -R 3000:localhost:3000 -p ${SP} ${shellQuote(`${d.id}@${sshHost}`)}`,
        `Git clone: git clone ${shellQuote(`ssh://${encodeURIComponent(d.id)}@${sshHost}:${SP}/home/user/repo.git`)}`,
      );
      return { ssh, client, desktop: desktopURL, full: lines.join("\n") };
    },
    [serverList, SP, VP, PP, t],
  );

  const copyDeviceConnection = useCallback(
    async (d: DeviceInfo, kind: keyof ConnStrings) => {
      const s = await deviceConnStrings(d, kind === "client" || kind === "full");
      copy(`conn:${kind}:${d.id}`, s[kind]);
    },
    [copy, deviceConnStrings],
  );

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
  const hintCopy = (items: { label: string; cmd: string }[]) => ({
    items,
    copiedId,
    onCopy: copy,
    t,
  });

  return (
    <div className="page-content">
      {/* Heading */}
      <div>
        <h1 className="page-title">
          <button
            type="button"
            aria-label={t("nav.label")}
            onClick={() => openMobileNav()}
            className={cn(COARSE_POINTER_HEADER_ICON_BUTTON_CLASS, "flex shrink-0 cursor-pointer items-center justify-center text-muted-foreground transition-colors hover:bg-state-hover hover:text-foreground md:hidden")}
          >
            <Icon name="PanelLeft" className="size-5" />
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
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        {stats.map((s) => (
          <Card key={s.label}>
            <CardContent className="p-3 sm:p-4">
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

      {devicesError && (
        <div role="alert" className="flex flex-wrap items-center gap-2 rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive-text">
          <span className="min-w-0 flex-1 break-words">{t("common.error")}: {devicesError}</span>
          <Button variant="outline" size="sm" onClick={refreshDevices}>
            <Icon name="Refresh" />{t("files.refresh")}
          </Button>
        </div>
      )}
      {configError && (
        <p role="alert" className="break-words text-sm text-destructive-text">{t("index.installTitle")} · {t("common.error")}: {configError}</p>
      )}

      {/* Devices */}
      <Card className="min-w-0 overflow-hidden">
        <Table aria-label={t("index.onlineDevices")} className="block md:table">
          <TableHeader className="hidden md:table-header-group">
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
          <TableBody className="block md:table-row-group">
            {devices.length === 0 && (
              <TableRow className="block md:table-row">
                <TableCell colSpan={9} className="flex h-32 items-center justify-center text-center md:table-cell">
                  <div className="flex flex-col items-center gap-2 text-muted-foreground">
                    <Icon name="Radio" className="size-8 opacity-30" />
                    <span className="text-sm">
                      {devicesError ? t("common.error") : t("index.waitingDevices")}
                    </span>
                  </div>
                </TableCell>
              </TableRow>
            )}
            {devices.map((d) => {
              return (
                <TableRow key={d.id} className="grid grid-cols-2 p-2 md:table-row md:p-0">
                  <TableCell className="col-start-2 row-start-1 block text-right md:table-cell md:text-left" aria-label={t("index.status")}>
                    <span className="inline-flex items-center gap-1.5 whitespace-nowrap">
                      <span className="size-2 rounded-full bg-success shadow-[0_0_6px_var(--success)]" />
                      {t("common.online")}
                    </span>
                  </TableCell>
                  <TableCell className="col-start-1 row-start-1 block min-w-0 font-semibold md:table-cell" aria-label={t("index.deviceId")}>
                    <span className="inline-flex max-w-full items-center gap-1.5 md:max-w-48" title={d.id}>
                      <OsIcon os={d.os} className="size-4 shrink-0 text-muted-foreground" />
                      <span className="truncate">{d.id}</span>
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
                  <TableCell className="block md:table-cell" aria-label={t("index.auth")}>
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
                  <TableCell className="block text-right md:table-cell md:text-left" aria-label={t("index.desktop")}>
                    <DesktopBadge d={d} t={t} />
                  </TableCell>
                  <TableCell className="col-span-2 block md:table-cell">
                    <div className="flex flex-wrap items-center gap-1.5 md:min-w-60 md:justify-end">
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
                      <DropdownMenu>
                        <DropdownMenuTrigger asChild>
                          <Button
                            variant="outline"
                            size="sm"
                            className={cn(
                              copiedId?.startsWith("conn:") &&
                                copiedId.endsWith(":" + d.id) &&
                                "border-success text-success",
                            )}
                          >
                            {copiedId?.startsWith("conn:") &&
                            copiedId.endsWith(":" + d.id) ? (
                              <>
                                <Icon name="Check" />
                                {t("common.copied")}
                              </>
                            ) : (
                              <>
                                {t("index.copyConnection")}
                                <Icon name="ChevronDown" />
                              </>
                            )}
                          </Button>
                        </DropdownMenuTrigger>
                        <DropdownMenuContent align="end">
                          <DropdownMenuItem
                            onSelect={() => copyDeviceConnection(d, "ssh")}
                          >
                            <Icon name="Terminal" />
                            {t("index.copyConnSsh")}
                          </DropdownMenuItem>
                          <DropdownMenuItem
                            onSelect={() => copyDeviceConnection(d, "client")}
                          >
                            <Icon name="Rocket" />
                            {t("index.copyConnClient")}
                          </DropdownMenuItem>
                          <DropdownMenuItem
                            onSelect={() => copyDeviceConnection(d, "desktop")}
                          >
                            <Icon name="Monitor" />
                            {t("index.copyConnDesktop")}
                          </DropdownMenuItem>
                          <DropdownMenuItem
                            onSelect={() => copyDeviceConnection(d, "full")}
                          >
                            <Icon name="AlignLeft" />
                            {t("index.copyConnFull")}
                          </DropdownMenuItem>
                        </DropdownMenuContent>
                      </DropdownMenu>
                    </div>
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </Card>

      {/* One-click install */}
      <Card className="min-w-0">
        <CardHeader className="flex-row flex-wrap items-center justify-between gap-3 space-y-0">
          <CardTitle className="flex items-center gap-2 text-base">
            <Icon name="Rocket" className="size-4 text-timeline-accent" />
            {t("index.installTitle")}
          </CardTitle>
          <div className="flex flex-wrap gap-1.5" role="group" aria-label={t("index.installTitle")}>
            {flavorTags.map((f) => (
              <button
                key={f.value}
                type="button"
                onClick={() => setFlavor(f.value)}
                aria-pressed={flavor === f.value}
                className={cn(
                  "cursor-pointer rounded-full border px-3 py-1 text-xs transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
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
            <TabsList className="max-w-full justify-start overflow-x-auto [&>button]:shrink-0">
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
                label={t("index.curlLabel", { password: pwPlaceholder })}
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
                label={t("index.psLabel", { password: pwPlaceholder })}
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
              <p className="mb-3 text-xs text-muted-foreground">{t("index.win7TLSHint")}</p>
              <CmdGroup
                label={t("index.win7Step1")}
                cmd={cmds.w7Path}
                cmdId="w7Path"
                copiedId={copiedId}
                onCopy={copy}
                t={t}
              />
              <CmdGroup
                label={t("index.win7Step2", { password: pwPlaceholder })}
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
      <Card className="min-w-0">
        <CardContent className="grid min-w-0 gap-5 p-4 lg:grid-cols-2 lg:p-5">
          <HintSection
            icon="Terminal"
            title={t("index.sshTitle")}
            copy={hintCopy([
              { label: "ssh", cmd: `ssh -p ${SP} '<deviceID>@${SSH_HOST}'` },
              { label: "scp", cmd: `scp -P ${SP} file '<deviceID>@${SSH_HOST}:/tmp/'` },
              { label: "sftp", cmd: `sftp -P ${SP} '<deviceID>@${SSH_HOST}'` },
              {
                label: "rsync",
                cmd: `rsync -az --delete -e "ssh -p ${SP}" ./local-dir/ '<deviceID>@${SSH_HOST}:/tmp/remote-dir/'`,
              },
            ])}
          >
            <HintCode cmd={`ssh -p ${SP} '<deviceID>@${SSH_HOST}'`} />
            <HintCode cmd={`scp -P ${SP} file '<deviceID>@${SSH_HOST}:/tmp/'`} />
            <HintCode cmd={`sftp -P ${SP} '<deviceID>@${SSH_HOST}'`} />
            <HintCode
              cmd={`rsync -az --delete -e "ssh -p ${SP}" ./local-dir/ '<deviceID>@${SSH_HOST}:/tmp/remote-dir/'`}
            />
          </HintSection>

          <HintSection
            icon="Monitor"
            title={t("index.remoteDesktopTitle")}
            copy={hintCopy(
              [
                {
                  label: "URL",
                  cmd: `open '${HTTP}${H}/remote-desktop?device=<deviceID>'`,
                },
                VP ? { label: "VNC", cmd: `vncviewer ${VP}` } : null,
              ].filter((x): x is { label: string; cmd: string } => x !== null),
            )}
          >
            <HintCode
              cmd={`open '${HTTP}${H}/remote-desktop?device=<deviceID>'`}
            />
            {VP && (
              <>
                <HintCode cmd={`vncviewer ${VP}`} />
                <HintComment text={t("index.vncAuthComment")} />
              </>
            )}
          </HintSection>

          {PP && (
            <HintSection
              icon="Globe"
              title={t("index.proxyTitle")}
              copy={hintCopy([
                {
                  label: "HTTP",
                  cmd: `curl -x 'http://<deviceID>:<password>@${PP}' https://example.com`,
                },
                {
                  label: "SOCKS5",
                  cmd: `curl -x 'socks5://<deviceID>:<password>@${PP}' https://example.com`,
                },
              ])}
            >
              <HintCode
                cmd={`curl -x 'http://<deviceID>:<password>@${PP}' https://example.com`}
              />
              <HintCode
                cmd={`curl -x 'socks5://<deviceID>:<password>@${PP}' https://example.com`}
              />
              <HintComment text={t("index.proxyAuthComment")} />
            </HintSection>
          )}

          <HintSection
            icon="Monitor"
            title={t("index.forwardTitle")}
            copy={hintCopy([
              {
                label: "-L",
                cmd: `ssh -L 8080:localhost:80 -p ${SP} '<deviceID>@${SSH_HOST}'`,
              },
              {
                label: "-R",
                cmd: `ssh -R 3000:localhost:3000 -p ${SP} '<deviceID>@${SSH_HOST}'`,
              },
            ])}
          >
            <HintComment text={t("index.localForwardComment")} />
            <HintCode cmd={`ssh -L 8080:localhost:80 -p ${SP} '<deviceID>@${SSH_HOST}'`} />
            <HintComment text={t("index.remoteForwardComment")} />
            <HintCode
              cmd={`ssh -R 3000:localhost:3000 -p ${SP} '<deviceID>@${SSH_HOST}'`}
            />
          </HintSection>

          <HintSection
            icon="Github"
            title={t("index.gitTitle")}
            copy={hintCopy([
              {
                label: "clone",
                cmd: `git clone 'ssh://<deviceID>@${SSH_HOST}:${SP}/home/user/repo.git'`,
              },
              {
                label: "push",
                cmd: `git remote add device 'ssh://<deviceID>@${SSH_HOST}:${SP}/home/user/repo.git'`,
              },
            ])}
          >
            <HintComment text={t("index.gitCloneComment")} />
            <HintCode
              cmd={`git clone 'ssh://<deviceID>@${SSH_HOST}:${SP}/home/user/repo.git'`}
            />
            <HintComment text={t("index.gitPushComment")} />
            <HintCode
              cmd={`git remote add device 'ssh://<deviceID>@${SSH_HOST}:${SP}/home/user/repo.git'`}
            />
          </HintSection>

          <HintSection
            icon="Key"
            title={t("index.keyAuthTitle")}
            copy={hintCopy([
              {
                label: "pubkey",
                cmd: "cat ~/.ssh/id_ed25519.pub >> ~/.rdev/authorized_keys",
              },
              { label: "ssh", cmd: `ssh -p ${SP} '<deviceID>@${SSH_HOST}'` },
            ])}
          >
            <HintComment text={t("index.keyAuthComment")} />
            <HintCode cmd="cat ~/.ssh/id_ed25519.pub >> ~/.rdev/authorized_keys" />
            <HintCode
              cmd={`ssh -p ${SP} '<deviceID>@${SSH_HOST}'`}
              suffix={t("index.noPasswordComment")}
            />
          </HintSection>
        </CardContent>
      </Card>

    </div>
  );
}

/* ── Hint building blocks ── */

/* ── Per-section copy menu: icon button + dropdown of individual commands ── */

function HintCopyMenu({
  section,
  items,
  copiedId,
  onCopy,
  t,
}: {
  section: string;
  items: { label: string; cmd: string }[];
  copiedId: string | null;
  onCopy: (id: string, text: string) => void;
  t: (k: string) => string;
}) {
  const itemId = (i: number) => `hint:${section}:${i}`;
  const anyCopied = items.some((_, i) => copiedId === itemId(i));
  return (
    <DropdownMenu>
      <Tooltip>
        <TooltipTrigger asChild>
          <DropdownMenuTrigger asChild>
            <Button
              variant="ghost"
              size="icon"
              aria-label={`${t("common.copy")}: ${section}`}
              className={cn(
                COARSE_POINTER_HEADER_ICON_BUTTON_CLASS,
                "shrink-0 text-muted-foreground hover:text-foreground",
                anyCopied && "text-success hover:text-success",
              )}
            >
              <Icon name={anyCopied ? "Check" : "Copy"} className="size-3.5" />
            </Button>
          </DropdownMenuTrigger>
        </TooltipTrigger>
        <TooltipContent side="top">{t("common.copy")}</TooltipContent>
      </Tooltip>
      <DropdownMenuContent align="end" className="max-w-[min(28rem,90vw)]">
        {items.map((it, i) => (
          <DropdownMenuItem
            key={i}
            onSelect={() => onCopy(itemId(i), it.cmd)}
            className="gap-2"
          >
            <span className="shrink-0 font-medium">{it.label}</span>
            <span className="min-w-0 flex-1 truncate font-mono text-xs text-muted-foreground">
              {it.cmd}
            </span>
            {copiedId === itemId(i) && (
              <Icon name="Check" className="size-4 text-success" />
            )}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function HintSection({
  icon,
  title,
  children,
  copy,
}: {
  icon: string;
  title: string;
  children: ReactNode;
  copy?: {
    items: { label: string; cmd: string }[];
    copiedId: string | null;
    onCopy: (id: string, text: string) => void;
    t: (k: string) => string;
  };
}) {
  return (
    <section className="min-w-0">
      <h3 className="mb-2 flex items-center gap-1.5 text-sm font-semibold text-timeline-accent">
        <Icon name={icon} className="size-4" />
        {title}
        {copy && copy.items.length > 0 && (
          <span className="ml-auto">
            <HintCopyMenu
              section={title}
              items={copy.items}
              copiedId={copy.copiedId}
              onCopy={copy.onCopy}
              t={copy.t}
            />
          </span>
        )}
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
