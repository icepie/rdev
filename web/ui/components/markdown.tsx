import { memo, useMemo, useState, type ReactNode } from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import remarkBreaks from "remark-breaks";
import { highlight } from "sugar-high";
import { lang } from "sugar-high/lang";
import { cn } from "../lib/utils";
import { copyText } from "../lib/clipboard";
import { useI18n } from "../lib/i18n";
import { Icon } from "./icon";
import {
  parseLocalFileHref,
  type LocalFileLink,
} from "./markdown-local-file-link";

/* bb-aligned markdown rendering for agent conversation text:
 * GFM + hard breaks, sugar-high syntax highlighting, accent links. */

/* Fenced code block: language header + copy + sugar-high highlighted body. */
function FencedCodeBlock({
  code,
  language,
}: {
  code: string;
  language: string | null;
}) {
  const [copied, setCopied] = useState(false);
  const [softWrap, setSoftWrap] = useState(false);
  const html = useMemo(() => {
    try {
      return highlight(code, { lang: language ? lang(language) : undefined });
    } catch {
      return null;
    }
  }, [code, language]);
  return (
    <div className="my-2 overflow-hidden rounded-md border border-border-seam bg-surface-recessed">
      <div className="flex items-center justify-between pt-1.5 pr-1.5 pl-3">
        <span className="font-mono text-xs text-muted-foreground uppercase">
          {language ?? ""}
        </span>
        <div className="flex items-center gap-0.5">
          <button
            type="button"
            aria-pressed={softWrap}
            className="inline-flex size-5 cursor-pointer items-center justify-center text-muted-foreground transition-colors hover:text-foreground aria-pressed:text-foreground"
            onClick={() => setSoftWrap((v) => !v)}
          >
            <Icon name="TextWrap" className="size-3" />
          </button>
          <button
            type="button"
            className="inline-flex size-5 items-center justify-center text-muted-foreground transition-colors hover:text-foreground"
            onClick={() => {
              copyText(code)
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
      </div>
      <pre
        className={cn(
          "code-highlight px-3 pt-1 pb-3 select-text",
          softWrap
            ? "whitespace-pre-wrap [overflow-wrap:anywhere]"
            : "overflow-x-auto",
        )}
      >
        {html === null ? (
          <code className="font-mono text-xs">{code}</code>
        ) : (
          <code
            className="font-mono text-xs"
            dangerouslySetInnerHTML={{ __html: html }}
          />
        )}
      </pre>
    </div>
  );
}

/* Shared mono code surface (markdown fences, command snippets, log tails).
 * With copyValue it renders the bb-style card: header (label + copy button)
 * over a plain pre body; without it, the legacy bare pre. */
export function CodeBlock({
  children,
  className,
  label,
  copyValue,
}: {
  children?: ReactNode;
  className?: string;
  label?: string;
  copyValue?: string;
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
        {children}
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
          className="inline-flex size-5 cursor-pointer items-center justify-center text-muted-foreground transition-colors hover:text-foreground"
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
        {children}
      </pre>
    </div>
  );
}

/* Default anchor: external links open in a new tab. Reused by the
 * onOpenLocalFile branch for hrefs that are not local files. */
const externalAnchor: NonNullable<Components["a"]> = (props) => (
  <a
    className="text-timeline-accent underline underline-offset-2 hover:opacity-80"
    target="_blank"
    rel="noreferrer"
    {...props}
  />
);

const markdownComponents: Components = {
  p: (props) => <p className="my-2 first:mt-0 last:mb-0" {...props} />,
  a: externalAnchor,
  pre: ({ children }) => <>{children}</>,
  code: ({ className, children }) => {
    const text = String(children ?? "").replace(/\n$/, "");
    const language = /language-([\w-]+)/.exec(className ?? "")?.[1] ?? null;
    if (language || text.includes("\n"))
      return <FencedCodeBlock code={text} language={language} />;
    return (
      <code className="rounded bg-surface-recessed px-1 py-0.5 font-mono text-[0.85em]">
        {children}
      </code>
    );
  },
  ul: (props) => <ul className="my-2 list-disc pl-5" {...props} />,
  ol: (props) => <ol className="my-2 list-decimal pl-5" {...props} />,
  li: (props) => <li className="my-0.5" {...props} />,
  blockquote: (props) => (
    <blockquote
      className="my-2 border-l-2 border-border-seam pl-3 text-muted-foreground"
      {...props}
    />
  ),
  h1: (props) => <h1 className="mt-3 mb-1.5 text-base font-semibold first:mt-0" {...props} />,
  h2: (props) => <h2 className="mt-3 mb-1.5 text-base font-semibold first:mt-0" {...props} />,
  h3: (props) => <h3 className="mt-3 mb-1.5 text-sm font-semibold first:mt-0" {...props} />,
  h4: (props) => <h4 className="mt-3 mb-1 text-sm font-semibold first:mt-0" {...props} />,
  hr: () => <hr className="my-3 border-border-seam" />,
  table: (props) => (
    <div className="my-2 overflow-x-auto">
      <table className="w-full border-collapse text-xs" {...props} />
    </div>
  ),
  th: (props) => (
    <th className="border border-border-seam bg-surface-recessed px-2 py-1 text-left font-semibold" {...props} />
  ),
  td: (props) => (
    <td className="border border-border-seam px-2 py-1 text-left" {...props} />
  ),
  img: (props) => <img className="max-w-full rounded-md" {...props} />,
};

export const Markdown = memo(function Markdown({
  text,
  className,
  onOpenLocalFile,
}: {
  text: string;
  className?: string;
  /* When set, anchors whose href parses as a local file (bb suffixes
   * `#L12` / `:12:5` etc. or file:// URLs) render as dotted-underline
   * buttons invoking this callback instead of navigating. */
  onOpenLocalFile?: (link: LocalFileLink) => void;
}) {
  const components = useMemo<Components>(() => {
    if (!onOpenLocalFile) {
      return markdownComponents;
    }
    return {
      ...markdownComponents,
      a: (props) => {
        const link = parseLocalFileHref(props.href ?? "");
        if (link === null) {
          return externalAnchor(props);
        }
        return (
          <button
            type="button"
            className="inline-flex cursor-pointer items-baseline gap-1 text-timeline-accent underline decoration-dotted underline-offset-2 hover:decoration-solid"
            onClick={() => {
              onOpenLocalFile(link);
            }}
          >
            <Icon name="FileText" className="size-3.5 shrink-0 self-center" />
            {props.children}
          </button>
        );
      },
    };
  }, [onOpenLocalFile]);
  return (
    <div className={cn("text-sm leading-6 break-words text-foreground", className)}>
      <ReactMarkdown
        remarkPlugins={[remarkGfm, remarkBreaks]}
        components={components}
      >
        {text}
      </ReactMarkdown>
    </div>
  );
});
