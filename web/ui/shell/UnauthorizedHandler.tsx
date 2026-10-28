import { useEffect, useRef, useState, type ReactNode } from "react";
import { setUnauthorizedHandler } from "../lib/api";
import { useI18n } from "../lib/i18n";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../components/dialog";
import { Input } from "../components/input";
import { Button } from "../components/button";

/** Bridges apiFetch 401s to a modal admin-token prompt. */
export function UnauthorizedHandler({ children }: { children: ReactNode }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [value, setValue] = useState("");
  const pending = useRef<Promise<string | null> | null>(null);
  const resolver = useRef<((token: string | null) => void) | null>(null);

  useEffect(() => {
    setUnauthorizedHandler(() => {
      // A 401 storm (devices poll + page fetches) must not reset the dialog
      // or wipe a half-typed token: all callers share one pending prompt.
      if (!pending.current) {
        pending.current = new Promise<string | null>((resolve) => {
          resolver.current = resolve;
          setValue("");
          setOpen(true);
        });
      }
      return pending.current;
    });
  }, []);

  const finish = (token: string | null) => {
    resolver.current?.(token);
    resolver.current = null;
    pending.current = null;
    setOpen(false);
  };

  return (
    <>
      {children}
      <Dialog
        open={open}
        onOpenChange={(o) => {
          if (!o) finish(null);
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("common.adminTokenRequired")}</DialogTitle>
            <DialogDescription>{t("common.adminTokenRequired")}</DialogDescription>
          </DialogHeader>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              finish(value || null);
            }}
          >
            <Input
              type="password"
              autoFocus
              value={value}
              onChange={(e) => setValue(e.target.value)}
            />
            <DialogFooter className="mt-4">
              <Button type="submit">{t("common.connect")}</Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}
