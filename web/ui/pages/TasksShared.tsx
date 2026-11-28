import { useContext, type ReactNode } from "react";
import { MobileNavContext } from "../shell/AppShell";
import { useI18n } from "../lib/i18n";
import { Badge } from "../components/badge";
import { Icon } from "../components/icon";
import { COARSE_POINTER_HEADER_ICON_BUTTON_CLASS } from "../components/coarse-pointer-sizing";
import { cn } from "../lib/utils";

export { fmt, localDateInput, Status, Shell };

function fmt(value?: string) {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) || date.getFullYear() < 2000
    ? "—"
    : date.toLocaleString();
}

function localDateInput(value: string) {
  if (!value) return "";
  const date = new Date(value);
  if (!Number.isFinite(date.valueOf())) return "";
  return new Date(date.valueOf() - date.getTimezoneOffset() * 60000)
    .toISOString()
    .slice(0, 16);
}

function statusClass(state: string) {
  if (state === "succeeded" || state === "enabled") {
    return "bg-success/15 text-success-foreground";
  }
  if (state === "failed") {
    return "bg-destructive/15 text-destructive-text";
  }
  if (state === "running") {
    return "bg-timeline-accent/15 text-timeline-accent";
  }
  if (state === "paused" || state === "pending" || state === "dispatching") {
    return "bg-attention/15 text-warning-text";
  }
  return "bg-secondary text-secondary-foreground";
}

function Status({ state }: { state: string }) {
  const { t } = useI18n();
  return (
    <Badge
      variant="outline"
      className={cn("border-transparent", statusClass(state))}
    >
      {t(`tasks.status.${state}`)}
    </Badge>
  );
}

function Shell({ children }: { children: ReactNode }) {
  const { open } = useContext(MobileNavContext);
  const { t } = useI18n();
  return (
    <div className="h-full overflow-y-auto">
      <div className="page-content">
        <header className="flex items-center gap-2">
          <h1 className="page-title">
            <button
              type="button"
              aria-label={t("nav.label")}
              onClick={() => open()}
              className={cn(
                COARSE_POINTER_HEADER_ICON_BUTTON_CLASS,
                "flex shrink-0 cursor-pointer items-center justify-center text-muted-foreground transition-colors hover:bg-state-hover hover:text-foreground md:hidden",
              )}
            >
              <Icon name="PanelLeft" className="size-5" />
            </button>
            <span>{t("tasks.title")}</span>
          </h1>
        </header>
        {children}
      </div>
    </div>
  );
}
