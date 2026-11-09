import { useEffect, useId, useRef, useState } from "react";
import { FilePreview } from "./agent/file-preview";
import { resolveFileIconName } from "./agent/file-visuals";
import type { Translator } from "./agent/types";
import type { PreviewTab } from "../lib/file-preview-tabs";
import { Icon } from "./icon";
import { Button } from "./button";

export function FileEditorTabs({ tabs, active, onSelect, onClose, onRefresh, onDownload, connected, t }: {
  tabs: PreviewTab[]; active: string; onSelect: (key: string) => void; onClose: (key: string) => void;
  onRefresh: (path: string) => void; onDownload: (tab: PreviewTab) => void; connected: boolean; t: Translator;
}) {
  const prefix = useId();
  const container = useRef<HTMLDivElement>(null);
  const [visible, setVisible] = useState(true);
  useEffect(() => {
    if (!container.current || typeof IntersectionObserver === "undefined") return;
    const observer = new IntersectionObserver(([entry]) => setVisible(entry.isIntersecting));
    observer.observe(container.current);
    return () => observer.disconnect();
  }, []);
  const current = tabs.find(tab => tab.key === active);
  return <div ref={container} className="flex h-full min-h-0 min-w-0 flex-col bg-background">
    <div role="tablist" aria-label={t("files.openFiles")} className="flex min-h-9 shrink-0 overflow-x-auto border-b border-border-seam bg-sidebar">
      {tabs.map((tab, index) => <div key={tab.key} className={`flex shrink-0 items-center border-r border-border-seam ${tab.key === active ? "bg-background text-foreground" : "text-muted-foreground"}`}>
        <button id={`${prefix}-tab-${index}`} aria-controls={`${prefix}-panel-${index}`} role="tab" aria-selected={tab.key === active} tabIndex={tab.key === active ? 0 : -1} title={tab.path}
          onClick={() => onSelect(tab.key)} onKeyDown={event => {
            const delta = event.key === "ArrowRight" ? 1 : event.key === "ArrowLeft" ? -1 : 0;
            if (!delta) return;
            event.preventDefault(); const next = (index + delta + tabs.length) % tabs.length;
            onSelect(tabs[next].key);
            const buttons = event.currentTarget.closest('[role="tablist"]')?.querySelectorAll<HTMLElement>('[role="tab"]'); buttons?.[next]?.focus();
          }} className="flex h-9 max-w-56 items-center gap-2 border-t-2 border-transparent px-3 text-xs aria-selected:border-t-timeline-accent">
          <Icon name={resolveFileIconName(tab.path)} className="size-3.5 shrink-0" /><span className="truncate">{tab.path.split(/[\\/]/).pop()}</span>
        </button>
        <button aria-label={`${t("common.close")} ${tab.path}`} className="mr-1 rounded p-1 hover:bg-state-hover" onClick={() => onClose(tab.key)}><Icon name="X" className="size-3" /></button>
      </div>)}
    </div>
    {!current ? <div className="m-auto p-6 text-center text-sm text-muted-foreground"><Icon name="Files" className="mx-auto mb-3 size-8" />{t("files.selectPreview")}</div> : <>
      <div className="min-h-0 flex-1 overflow-hidden">
        {tabs.map((tab, index) => <div key={tab.key} id={`${prefix}-panel-${index}`} aria-labelledby={`${prefix}-tab-${index}`} role="tabpanel" hidden={tab.key !== active} className="h-full min-h-0">
          <FilePreview path={tab.path} state={tab.state} connected={connected} visible={visible && tab.key === active} onRefresh={() => onRefresh(tab.path)} t={t} />
        </div>)}
      </div>
      <div className="flex shrink-0 items-center justify-between gap-2 border-t border-border-seam px-3 py-1 text-xs text-muted-foreground">
        <span className="truncate" title={current.path}>{current.path}</span>
        <Button variant="ghost" size="sm" disabled={!connected} onClick={() => onDownload(current)}>{t("files.download")}</Button>
      </div>
    </>}
  </div>;
}
