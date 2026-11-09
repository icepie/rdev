import type { ReactNode } from "react";
import { useDevices } from "../../lib/devices";
import { useI18n } from "../../lib/i18n";
import { DeviceIcon } from "../device-icon";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../select";

/** Tool tabs choose a client independently of the Agent conversation host. */
export function PanelDevice({ device, onChange, children }: {
  device: string;
  onChange: (device: string) => void;
  children: (device: string) => ReactNode;
}) {
  const { devices } = useDevices();
  const { t } = useI18n();
  const target = device === "@server" ? "" : device;
  const available = devices.filter(d => d.id !== "@server");
  return <>
    <div className="flex shrink-0 items-center gap-2 border-b border-border-seam px-3 py-2">
      <Select value={target} onValueChange={onChange}>
        <SelectTrigger aria-label={t("agent.selectDeviceShort")} className="h-8 min-w-0 w-full text-xs">
          <SelectValue placeholder={t("agent.selectDeviceShort")} />
        </SelectTrigger>
        <SelectContent>
          {target && !available.some(d => d.id === target) && <SelectItem value={target} disabled>{target} · {t("common.disconnected")}</SelectItem>}
          {available.map(d => <SelectItem key={d.id} value={d.id}><DeviceIcon device={d.id} os={d.os} /><span className="truncate">{d.id}</span></SelectItem>)}
        </SelectContent>
      </Select>
    </div>
    {target ? children(target) : <div className="m-auto p-4 text-center text-sm text-muted-foreground">{t("agent.selectDeviceShort")}</div>}
  </>;
}
