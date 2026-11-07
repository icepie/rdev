import type { Translator } from "./types";

export function AgentConnectionError({ message, t }: { message: string; t: Translator }) {
  const needsSetup = message.startsWith("agent not configured:");
  return (
    <div role="alert" className="shrink-0 break-words border-b border-border-seam px-4 py-3 text-xs">
      {needsSetup ? (
        <>
          <p className="font-medium text-foreground">{t("agent.notConfigured")}</p>
          <p className="mt-1 leading-relaxed text-muted-foreground">{t("agent.notConfiguredHint")}</p>
          <p className="mt-1 text-muted-foreground">{t("agent.reconnectAfterSetup")}</p>
        </>
      ) : <p className="text-destructive-text">{message}</p>}
    </div>
  );
}
