import { useEffect, useState } from "react";
import { Button } from "../button";
import { Input } from "../input";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "../dialog";
import type { Translator } from "./types";

export interface RenameTarget {
  id: string;
  device: string;
  title: string;
}

/** Retain one captured target; navigation must not retarget an open edit. */
export function ThreadRenameDialog({ target, open, disabled, onCommit, onClose, t }: {
  target: RenameTarget;
  open: boolean;
  disabled: boolean;
  onCommit: (id: string, title: string, device: string) => boolean;
  onClose: () => void;
  t: Translator;
}) {
  const [value, setValue] = useState(target.title);
  const [error, setError] = useState("");
  useEffect(() => {
    if (open) { setValue(target.title); setError(""); }
  }, [open, target]);
  return (
    <Dialog open={open} onOpenChange={open => { if (!open) onClose(); }}>
      <DialogContent className="max-w-sm">
        <DialogHeader>
          <DialogTitle>{t("agent.renamePrompt")}</DialogTitle>
          <DialogDescription className="sr-only">{t("agent.renamePrompt")}</DialogDescription>
        </DialogHeader>
        <form className="space-y-4" onSubmit={event => {
          event.preventDefault();
          if (!open || disabled || !value.trim()) return;
          if (onCommit(target.id, value.trim(), target.device)) onClose();
          else setError(t("common.disconnected"));
        }}>
          <Input autoFocus aria-label={t("agent.renamePrompt")} value={value}
            onChange={event => { setValue(event.target.value); setError(""); }}
            onKeyDown={event => {
              if (event.key === "Enter" && (event.nativeEvent.isComposing || event.nativeEvent.keyCode === 229)) {
                event.preventDefault();
              }
            }} />
          {error && <p role="alert" className="text-sm text-destructive-text">{error}</p>}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>{t("common.cancel")}</Button>
            <Button type="submit" disabled={disabled || !value.trim()}>{t("common.save")}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
