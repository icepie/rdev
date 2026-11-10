import { useState } from "react";
import { Popover, PopoverContent, PopoverTrigger } from "../popover";
import { Button } from "../button";
import { Input } from "../input";
import { DeviceIcon } from "../device-icon";
import { Icon } from "../icon";
import { useDevices } from "../../lib/devices";
import { getDevicePassword } from "../../lib/api";
import type { Translator } from "./types";

export interface Participant {
  device: string;
  cwd: string;
  online: boolean;
  authorized: boolean;
  primary: boolean;
}
export function parseParticipants(value: unknown): Participant[] {
  if (!Array.isArray(value)) return [];
  return value
    .filter((v) => v && typeof v.device === "string")
    .map((v) => ({
      device: v.device,
      cwd: typeof v.cwd === "string" ? v.cwd : "",
      online: v.online === true,
      authorized: v.authorized === true,
      primary: v.primary === true,
    }));
}
export function Participants({
  primary,
  rows,
  ready,
  error,
  onUpdate,
  onRefresh,
  t,
}: {
  primary: string;
  rows: Participant[];
  ready: boolean;
  error: string;
  onUpdate: (
    mode: string,
    device: string,
    password?: string,
    cwd?: string,
  ) => void;
  onRefresh: () => void;
  t: Translator;
}) {
  const { devices } = useDevices();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [editing, setEditing] = useState("");
  const [password, setPassword] = useState("");
  const [cwd, setCwd] = useState("");
  const members = rows.length
    ? rows
    : [
        {
          device: primary,
          cwd: "",
          online: true,
          authorized: true,
          primary: true,
        },
      ];
  const edit = (id: string) => {
    setEditing(id);
    setPassword(getDevicePassword(id));
    setCwd(rows.find((r) => r.device === id)?.cwd ?? "");
  };
  return (
    <Popover
      open={open}
      onOpenChange={(value) => {
        setOpen(value);
        if (value) onRefresh();
        else {
          setEditing("");
          setPassword("");
        }
      }}
    >
      <PopoverTrigger asChild>
        <button
          type="button"
          disabled={!ready}
          aria-label={t("agent.participants")}
          className="inline-flex h-6 max-w-40 min-w-0 shrink-0 items-center gap-1 rounded-full border border-border-seam bg-surface-recessed px-2 text-xs text-muted-foreground hover:bg-state-hover focus-visible:outline focus-visible:outline-ring"
        >
          <DeviceIcon
            device={primary}
            os={devices.find((d) => d.id === primary)?.os}
            className="size-3"
          />
          <span className="truncate">{primary}</span>
          <span className="shrink-0">
            {members.length > 1 ? `+${members.length - 1}` : "+"}
          </span>
        </button>
      </PopoverTrigger>
      <PopoverContent
        align="start"
        className="w-80 max-w-[calc(100vw-2rem)] p-2"
      >
        <div className="px-2 py-1.5 text-xs font-medium">
          {t("agent.participants")}
        </div>
        {error && (
          <p role="alert" className="px-2 py-1 text-xs text-destructive-text">
            {error}
          </p>
        )}
        <div className="max-h-64 overflow-y-auto">
          {members.map((row) => (
            <div
              key={row.device}
              className="flex min-w-0 items-center gap-2 rounded px-2 py-2 text-xs hover:bg-state-hover"
            >
              <DeviceIcon
                device={row.device}
                os={devices.find((d) => d.id === row.device)?.os}
              />
              <div className="min-w-0 flex-1">
                <div className="truncate">
                  {row.device}
                  {row.primary && (
                    <span className="ml-1 text-muted-foreground">
                      {t("agent.primaryDevice")}
                    </span>
                  )}
                </div>
                <div className="truncate text-muted-foreground">
                  {!devices.some((d) => d.id === row.device)
                    ? t("common.offline")
                    : !row.authorized
                      ? t("agent.deviceReauthorize")
                      : row.cwd || t("agent.deviceDefaultDirectory")}
                </div>
              </div>
              {!row.primary && (
                <>
                  <Button
                    size="icon"
                    variant="ghost"
                    className="size-6"
                    aria-label={t("agent.deviceSettings")}
                    onClick={() => edit(row.device)}
                  >
                    <Icon name="Settings" className="size-3.5" />
                  </Button>
                  <Button
                    size="icon"
                    variant="ghost"
                    className="size-6"
                    aria-label={t("agent.removeDevice")}
                    onClick={() => onUpdate("remove", row.device)}
                  >
                    <Icon name="X" className="size-3.5" />
                  </Button>
                </>
              )}
            </div>
          ))}
        </div>
        <div className="mt-1 border-t border-border-seam pt-2">
          <Input
            aria-label={t("agent.addDevice")}
            placeholder={t("agent.addDevice")}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="h-8 text-xs"
          />
          <div className="max-h-36 overflow-y-auto py-1">
            {devices
              .filter(
                (d) =>
                  !members.some((m) => m.device === d.id) &&
                  d.id.toLowerCase().includes(query.toLowerCase()),
              )
              .map((d) => (
                <button
                  type="button"
                  key={d.id}
                  onClick={() => edit(d.id)}
                  className="flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-xs hover:bg-state-hover"
                >
                  <DeviceIcon device={d.id} os={d.os} />
                  <span className="truncate">{d.id}</span>
                  <Icon name="Plus" className="ml-auto size-3.5" />
                </button>
              ))}
          </div>
        </div>
        {editing && (
          <form
            className="space-y-2 border-t border-border-seam p-2"
            onSubmit={(e) => {
              e.preventDefault();
              onUpdate("add", editing, password, cwd);
              setPassword("");
            }}
          >
            <p className="truncate text-xs font-medium">{editing}</p>
            {devices.find((d) => d.id === editing)?.hasPassword && (
              <Input
                type="password"
                autoComplete="off"
                aria-label={t("browser.password")}
                placeholder={t("browser.password")}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            )}
            <Input
              aria-label={t("agent.deviceDirectory")}
              placeholder={t("agent.deviceDirectory")}
              value={cwd}
              onChange={(e) => setCwd(e.target.value)}
            />
            <div className="flex justify-end gap-1">
              <Button
                type="button"
                size="sm"
                variant="ghost"
                onClick={() => {
                  setEditing("");
                  setPassword("");
                }}
              >
                {t("common.cancel")}
              </Button>
              <Button type="submit" size="sm" disabled={!ready}>
                {t("common.save")}
              </Button>
            </div>
          </form>
        )}
      </PopoverContent>
    </Popover>
  );
}
