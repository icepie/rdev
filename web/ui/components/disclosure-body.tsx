import { cn } from "../lib/utils";
import { useEffect, useState, type ReactNode } from "react";

/** BB disclosure's grid-height transition, with lazy initial rendering.
 * Retaining visited content preserves copy/wrap and nested disclosure state.
 * Only open/closed changes animate; streamed children keep their natural size.
 */
export function DisclosureBody({
  open,
  children,
  forceMount = false,
  className,
}: {
  open: boolean;
  children: ReactNode;
  forceMount?: boolean;
  className?: string;
}) {
  const [visited, setVisited] = useState(open);
  useEffect(() => {
    if (open) setVisited(true);
  }, [open]);

  return (
    <div
      className={cn("disclosure-body", className)}
      data-open={open}
      aria-hidden={!open}
      inert={!open}
    >
      <div className="min-h-0 overflow-hidden">
        {forceMount || open || visited ? children : null}
      </div>
    </div>
  );
}
