import { useContext, type ReactNode } from "react";
import { MobileNavContext } from "../shell/AppShell";
import { useI18n } from "../lib/i18n";
import { Button } from "../components/button";
import { Badge } from "../components/badge";
import { Icon } from "../components/icon";
import { cn } from "../lib/utils";

export { fmt, localDateInput, Status, Shell };
function fmt(value?: string) { if (!value) return "—"; const date = new Date(value); return Number.isNaN(date.valueOf()) || date.getFullYear() < 2000 ? "—" : date.toLocaleString(); }
function localDateInput(value: string) { if (!value) return ""; const date = new Date(value); if (!Number.isFinite(date.valueOf())) return ""; return new Date(date.valueOf() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16); }
function statusClass(state: string) { return state === "succeeded" || state === "enabled" ? "bg-success/15 text-success-foreground" : state === "failed" || state === "paused" ? "bg-destructive/15 text-destructive" : "bg-secondary text-secondary-foreground"; }
function Status({ state }: { state: string }) { const { t } = useI18n(); return <Badge variant="outline" className={cn("border-transparent", statusClass(state))}>{t(`tasks.status.${state}`)}</Badge>; }

function Shell({ children }: { children: ReactNode }) {
  const { open } = useContext(MobileNavContext);
  const { t } = useI18n();
  return <main className="flex min-h-0 flex-1 flex-col overflow-auto"><header className="flex min-h-14 items-center gap-3 border-b border-border-seam px-4"><Button variant="ghost" size="icon" className="md:hidden" onClick={() => open()} aria-label={t("nav.label")}><Icon name="PanelLeft" /></Button><h1 className="text-lg font-semibold">{t("tasks.title")}</h1></header><div className="mx-auto w-full max-w-6xl flex-1 p-4 md:p-6">{children}</div></main>;
}
