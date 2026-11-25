/* Session bar under the desktop stage: control ownership, negotiated stream
 * details and live frame metrics. Kept out of the toolbar so state changes do
 * not reflow the device/connection controls. */
type DesktopSessionBarProps = {
  controlling: boolean;
  onReleaseControl: () => void;
  info?: string;
  frame?: string;
  t: (key: string) => string;
};

export function DesktopSessionBar({ controlling, onReleaseControl, info, frame, t }: DesktopSessionBarProps) {
  if (!controlling && !info && !frame) return null;
  return (
    <div className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1 border-t border-border-seam bg-sidebar px-3 py-1.5 text-xs text-muted-foreground">
      {controlling && (
        <span className="inline-flex items-center gap-1.5 rounded-full border border-timeline-accent/40 bg-timeline-accent/10 px-2 py-0.5 text-timeline-accent">
          <span aria-hidden="true" className="size-1.5 rounded-full bg-timeline-accent" />
          {t("desktop.controlling")}
          <button
            type="button"
            onClick={onReleaseControl}
            className="cursor-pointer rounded underline-offset-2 hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
          >
            {t("desktop.releaseControl")}
          </button>
        </span>
      )}
      {info && <span className="min-w-0 truncate" title={info}>{info}</span>}
      {frame && <span className="ml-auto shrink-0 tabular-nums">{frame}</span>}
    </div>
  );
}
