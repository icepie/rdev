// BB f51e6b0 SecondaryPanelSelectionActions / TimelineSelectionMenu behavior:
// coalesce selection events, preserve selection while activating the action,
// and use a collision-aware floating menu instead of a clipped absolute button.
import { useEffect, useMemo, useState, type RefObject } from "react";
import * as PopoverPrimitive from "@radix-ui/react-popover";
import { Icon } from "../icon";
import { usePointerCoarse } from "../hooks/use-pointer-coarse";
import { usePortalScopeProps } from "../../lib/portal-scope";
import { readPanelSelection, type PanelSelection } from "../../lib/panel-selection";
import type { Translator } from "./types";

export function FileSelectionMenu({ panelRef, content, mode, onAddToChat, t }: {
  panelRef: RefObject<HTMLDivElement | null>;
  content: string;
  mode: string;
  onAddToChat: (text: string) => void;
  t: Translator;
}) {
  const coarse = usePointerCoarse();
  const [selection, setSelection] = useState<PanelSelection | null>(null);
  const portalScope = usePortalScopeProps();
  useEffect(() => {
    setSelection(null);
    const panel = panelRef.current;
    if (!panel || coarse) return;
    const doc = panel.ownerDocument;
    const view = doc.defaultView;
    if (!view) return;
    let frame: number | null = null;
    let pointerDown = false;
    let startedHere = false;
    const inMenu = (event: Event) => event.composedPath().some(target => target instanceof HTMLElement && target.hasAttribute("data-file-selection-menu"));
    const cancelFrame = () => { if (frame !== null) view.cancelAnimationFrame(frame); frame = null; };
    const dismiss = () => { cancelFrame(); setSelection(null); };
    const report = () => {
      cancelFrame();
      frame = view.requestAnimationFrame(() => {
        frame = null;
        setSelection(panel.isConnected ? readPanelSelection(panel) : null);
      });
    };
    const down = (event: PointerEvent) => {
      if (inMenu(event)) return;
      pointerDown = true;
      startedHere = event.composedPath().includes(panel);
      dismiss();
    };
    const release = (event: Event) => {
      if (inMenu(event)) return;
      pointerDown = false;
      if (startedHere) report();
      startedHere = false;
    };
    const changed = () => { if (!pointerDown) report(); };
    const keyup = (event: KeyboardEvent) => { if (event.key === "Escape") dismiss(); else changed(); };
    doc.addEventListener("pointerdown", down);
    doc.addEventListener("pointerup", release);
    doc.addEventListener("pointercancel", release);
    doc.addEventListener("selectionchange", changed);
    doc.addEventListener("keyup", keyup);
    panel.addEventListener("scroll", dismiss, true);
    view.addEventListener("resize", dismiss);
    return () => {
      cancelFrame();
      doc.removeEventListener("pointerdown", down);
      doc.removeEventListener("pointerup", release);
      doc.removeEventListener("pointercancel", release);
      doc.removeEventListener("selectionchange", changed);
      doc.removeEventListener("keyup", keyup);
      panel.removeEventListener("scroll", dismiss, true);
      view.removeEventListener("resize", dismiss);
    };
  }, [panelRef, content, mode, coarse]);
  const anchor = useMemo(() => ({ current: {
    contextElement: panelRef.current ?? undefined,
    getBoundingClientRect: () => selection?.rect ?? new DOMRect(),
  } }), [selection, panelRef]);
  if (!selection || coarse) return null;
  return <PopoverPrimitive.Root open onOpenChange={open => { if (!open) setSelection(null); }}>
    <PopoverPrimitive.Anchor virtualRef={anchor} />
    <PopoverPrimitive.Portal>
      <PopoverPrimitive.Content {...portalScope} data-file-selection-menu
        side="top" align="center" sideOffset={6} collisionPadding={8} collisionBoundary={panelRef.current}
        className="pointer-events-auto z-50 flex w-auto items-center rounded-md border bg-popover p-0.5 text-popover-foreground shadow-md outline-none data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-95 motion-reduce:animate-none"
        onOpenAutoFocus={event => event.preventDefault()} onCloseAutoFocus={event => event.preventDefault()}
        onEscapeKeyDown={() => setSelection(null)}>
        <button type="button" data-add-to-chat
          className="inline-flex cursor-pointer select-none items-center gap-1 rounded px-1.5 py-0.5 text-xs text-foreground transition-colors hover:bg-surface-recessed focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
          onMouseDown={event => event.preventDefault()}
          onClick={() => {
            const live = panelRef.current && readPanelSelection(panelRef.current);
            if (live && live.text === selection.text) {
              onAddToChat(live.text);
              panelRef.current?.ownerDocument.getSelection()?.removeAllRanges();
            }
            setSelection(null);
          }}>
          <Icon name="MessageSquarePlus" className="size-3" />
          {t("agent.addToChat")}
        </button>
      </PopoverPrimitive.Content>
    </PopoverPrimitive.Portal>
  </PopoverPrimitive.Root>;
}
