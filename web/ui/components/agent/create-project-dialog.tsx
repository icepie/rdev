import { useEffect, useState, type FormEvent } from "react";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../dialog";
import { Button } from "../button";
import {
  deriveNameFromPath,
  normalizePathInput,
  validateProjectPath,
  type ProjectPathError,
} from "./path-utils";
import { RemotePathBrowser, type DirListing } from "./remote-path-browser";
import type { Translator } from "./types";

/* Port of bb apps/app/src/components/dialogs/ProjectPathDialog.tsx (create
 * mode). rdev deviations: no machine picker (the dialog always browses the
 * current device — cwd applies to the current thread on that device), no
 * native OS folder-picker bypass, no virtualized list. Submitting sets the
 * thread cwd (bb creates a project entity; rdev has none). ClassNames and
 * copy structure follow bb verbatim. */

const VALIDATION_KEYS: Record<ProjectPathError, string> = {
  absolute: "agent.pathMustBeAbsolute",
  root: "agent.pathMustNotBeRoot",
  unc: "agent.uncPathsUnsupported",
};

export function CreateProjectDialog({
  open,
  onOpenChange,
  initialPath,
  listDir,
  mkdir,
  onSubmit,
  t,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /* Seed path: the thread's current cwd ("" → server falls back to the
   * thread's effective working directory). */
  initialPath: string;
  listDir: (path: string) => Promise<DirListing>;
  mkdir: (path: string) => Promise<void>;
  onSubmit: (path: string) => void;
  t: Translator;
}) {
  const [selectedPath, setSelectedPath] = useState<string | null>(null);
  const [validation, setValidation] = useState("");

  useEffect(() => {
    if (open) {
      setSelectedPath(null);
      setValidation("");
    }
  }, [open]);

  /* bb clears the validation message whenever the selection changes. */
  useEffect(() => {
    setValidation("");
  }, [selectedPath]);

  const derivedName = selectedPath ? deriveNameFromPath(selectedPath) : "";

  const handleSubmit = (e: FormEvent) => {
    e.preventDefault();
    if (!selectedPath) {
      setValidation(t("agent.chooseProjectFolder"));
      return;
    }
    const normalized = normalizePathInput(selectedPath);
    const err = validateProjectPath(normalized);
    if (err) {
      setValidation(t(VALIDATION_KEYS[err]));
      return;
    }
    if (!deriveNameFromPath(normalized)) {
      setValidation(t("agent.couldNotDeriveName"));
      return;
    }
    onSubmit(normalized);
    onOpenChange(false);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("agent.addProject")}</DialogTitle>
          <DialogDescription>
            {t("agent.browseProjectFolder")}
          </DialogDescription>
        </DialogHeader>
        <form className="min-w-0 space-y-4" onSubmit={handleSubmit}>
          {open && (
            <RemotePathBrowser
              key={initialPath}
              initialPath={initialPath}
              allowCreateFolder
              onDirectoryChange={setSelectedPath}
              listDir={listDir}
              mkdir={mkdir}
              t={t}
            />
          )}
          {derivedName || validation ? (
            <div className="space-y-1">
              {derivedName ? (
                <p className="flex min-w-0 gap-1 text-sm text-muted-foreground">
                  <span className="shrink-0">{t("agent.projectNameLabel")}</span>
                  <span className="min-w-0 truncate font-medium text-foreground">
                    {derivedName}
                  </span>
                </p>
              ) : null}
              {validation ? (
                <p className="text-sm text-destructive-text">{validation}</p>
              ) : null}
            </div>
          ) : null}
          <DialogFooter>
            <Button type="submit" disabled={!selectedPath}>
              {t("agent.addProject")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
