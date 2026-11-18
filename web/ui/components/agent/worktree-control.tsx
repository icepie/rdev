import { useEffect, useRef, useState } from "react";
import { Button } from "../button";
import { Input } from "../input";
import { Popover, PopoverContent, PopoverTrigger } from "../popover";
import { nextKey } from "./protocol";
import { requestWorkspace } from "./workspace-history";
import type { ThreadSinkOps } from "./thread-sink";
import type { Translator } from "./types";

export function WorktreeControl({ device, thread, cwd, ready, ops, sendRaw, t }: {
  device: string; thread: string; cwd: string; ready: boolean; ops: ThreadSinkOps;
  sendRaw: (frame: Record<string, unknown>) => boolean; t: Translator;
}) {
  const [open, setOpen] = useState(false);
  const [base, setBase] = useState("");
  const [result, setResult] = useState<Record<string, unknown> | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState<"remove" | "merge" | null>(null);
  const [merged, setMerged] = useState(false);
  const cancel = useRef<(() => void) | null>(null);
  useEffect(() => { setResult(null); setConfirm(null); setMerged(false); setBusy(false); return () => cancel.current?.(); }, [device, thread]);
  const request = (action: "create" | "status" | "remove" | "merge") => {
    cancel.current?.(); setBusy(true); setConfirm(null); setMerged(false);
    cancel.current = requestWorkspace(ops.workspaceReplies.current, sendRaw,
      { op: "worktree", id: `worktree-${nextKey()}`, thread, action, ...(action === "create" && base.trim() ? { base: base.trim() } : {}) }, reply => {
        setBusy(false);
        setResult(reply?.code === "timeout" ? { state: "error", error: t("agent.workspaceTimeout") } : reply || { state: "error", error: t("agent.workspaceDisconnected") });
        if (action === "merge" && reply && !reply.error && reply.state === "ready") setMerged(true);
      }, 315000);
  };
  useEffect(() => {
    if (open && ready) request("status");
    return () => cancel.current?.();
    // A cwd update must refresh ownership, not retain a different project's state.
  }, [open, ready, device, thread, cwd]);
  const owned = result?.state === "ready" || !!result?.path;
  return <Popover open={open} onOpenChange={value => { setOpen(value); setConfirm(null); }}>
    <PopoverTrigger asChild><Button variant="ghost" size="sm" disabled={!ready}>{t("agent.worktree")}</Button></PopoverTrigger>
    <PopoverContent className="w-80" align="start"><div className="flex w-full flex-col gap-2 text-xs">
      <strong>{t("agent.worktree")}</strong>
      {busy && <p role="status">{t("common.loading")}</p>}
      {result?.error ? <p role="alert" className="whitespace-pre-wrap text-destructive-text">{String(result.error)}</p> : null}
      {merged && <p role="status">{t("agent.worktreeMerged")}</p>}
      {result && !busy && <p>{t(`agent.worktreeState.${String(result.state)}`)}</p>}
      {result?.path ? <p className="break-all">{String(result.path)}</p> : null}
      {result?.branch ? <p className="break-all">{String(result.branch)}</p> : null}
      {!owned && <><Input value={base} onChange={event => setBase(event.target.value)} placeholder={t("agent.worktreeBase")} aria-label={t("agent.worktreeBase")} /><Button disabled={!ready || busy || !result || result.state === "error"} onClick={() => request("create")}>{t("agent.worktreeCreate")}</Button></>}
      {owned && !confirm && <><Button disabled={!ready || busy} variant="outline" onClick={() => setConfirm("merge")}>{t("agent.worktreeMerge")}</Button><Button disabled={!ready || busy} variant="outline" onClick={() => setConfirm("remove")}>{t("agent.worktreeRemove")}</Button></>}
      {confirm && <div className="flex flex-col gap-2" role="alert"><p>{t(confirm === "merge" ? "agent.worktreeMergeConfirm" : "agent.worktreeConfirm")}</p><Button disabled={!ready || busy} variant={confirm === "remove" ? "destructive" : "default"} onClick={() => request(confirm)}>{t(confirm === "merge" ? "agent.worktreeMerge" : "agent.worktreeRemove")}</Button><Button variant="ghost" onClick={() => setConfirm(null)}>{t("common.cancel")}</Button></div>}
      <Button variant="ghost" disabled={!ready || busy} onClick={() => request("status")}>{t("agent.worktreeRefresh")}</Button>
    </div></PopoverContent>
  </Popover>;
}
