import { useState, type ReactNode } from "react";
import { SyntaxCode } from "./syntax-code";
import { cn } from "../lib/utils";
import { copyText } from "../lib/clipboard";
import { useI18n } from "../lib/i18n";
import { Icon } from "./icon";

/* Shared mono code surface (markdown fences, command snippets, log tails).
 * With copyValue it renders the bb-style card: header (label + copy button)
 * over a plain pre body; without it, the legacy bare pre. */
export function CodeBlock({
  children,
  className,
  label,
  copyValue,
  language,
}: {
  children?: ReactNode;
  className?: string;
  label?: string;
  copyValue?: string;
  language?: string;
}) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);
  if (copyValue === undefined) {
    return (
      <pre
        className={cn(
          "overflow-x-auto rounded-md border border-border-seam bg-surface-recessed px-3 py-2 font-mono text-xs leading-5 whitespace-pre text-foreground select-all",
          className,
        )}
      >
        {typeof children === "string" ? <SyntaxCode code={children} language={language ?? "json"} /> : children}
      </pre>
    );
  }
  return (
    <div
      className={cn(
        "overflow-hidden rounded-md border border-border-seam bg-surface-recessed",
        className,
      )}
    >
      <div className="flex items-center justify-between pt-1.5 pr-1.5 pl-3">
        <span className="font-mono text-xs text-muted-foreground uppercase">
          {label ?? ""}
        </span>
        <button
          type="button"
          aria-label={t("common.copy")}
          className="inline-flex size-7 max-md:pointer-coarse:size-9 cursor-pointer items-center justify-center text-muted-foreground transition-colors hover:text-foreground"
          onClick={() => {
            copyText(copyValue)
              .then(() => {
                setCopied(true);
                window.setTimeout(() => setCopied(false), 1500);
              })
              .catch(() => {});
          }}
        >
          <Icon name={copied ? "Check" : "Copy"} className="size-3" />
        </button>
      </div>
      <pre className="overflow-x-auto px-3 pt-1 pb-3 font-mono text-xs whitespace-pre select-text">
        {typeof children === "string" ? <SyntaxCode code={children} language={language ?? "json"} /> : children}
      </pre>
    </div>
  );
}
