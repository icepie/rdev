import { useEffect, useState, type ReactNode } from "react";
import { Skeleton } from "./skeleton";
/** BB f51e6b0 timeline/secondary-panel shapes. Delay prevents fast navigation flashes. */
function Delayed({ children, label }: { children: ReactNode; label: string }) {
  const [shown, setShown] = useState(false);
  useEffect(() => {
    const timer = setTimeout(() => setShown(true), 160);
    return () => clearTimeout(timer);
  }, []);
  return (
    <div
      role="status"
      aria-label={label}
      aria-busy="true"
      className={shown ? "" : "invisible"}
    >
      {children}
    </div>
  );
}
export function ThreadTimelineLoadingSkeleton({ label }: { label: string }) {
  return (
    <Delayed label={label}>
      <div className="mt-6 space-y-5">
        <div className="flex justify-end px-2">
          <Skeleton className="h-12 w-3/5" />
        </div>
        <div className="space-y-2 px-2">
          <Skeleton className="h-3.5 w-11/12" />
          <Skeleton className="h-3.5 w-full" />
          <Skeleton className="h-3.5 w-3/4" />
        </div>
        <div className="space-y-2.5 px-2">
          {["w-2/5", "w-1/2", "w-1/3"].map((w) => (
            <div key={w} className="flex items-center gap-2">
              <Skeleton className="size-3.5 shrink-0 rounded" />
              <Skeleton className={`h-3 ${w}`} />
            </div>
          ))}
        </div>
        <div className="space-y-2 px-2">
          <Skeleton className="h-3.5 w-5/6" />
          <Skeleton className="h-3.5 w-2/3" />
        </div>
      </div>
    </Delayed>
  );
}
export function SecondaryPanelContentSkeleton({ label }: { label: string }) {
  return (
    <Delayed label={label}>
      <div className="space-y-2 px-4 py-4">
        {["w-3/4", "w-full", "w-5/6", "w-2/3"].map((w) => (
          <Skeleton key={w} className={`h-3 ${w} rounded-sm`} />
        ))}
      </div>
    </Delayed>
  );
}
export function SourceLoadingSkeleton({ label }: { label: string }) {
  return (
    <Delayed label={label}>
      <div className="space-y-2 px-4 pt-4">
        {["w-3/4", "w-full", "w-5/6", "w-2/3", "w-full", "w-3/5"].map(
          (w, i) => (
            <Skeleton key={i} className={`h-3 ${w} rounded-sm`} />
          ),
        )}
      </div>
    </Delayed>
  );
}
export function RouteLoadingSkeleton({
  label,
  chat = false,
}: {
  label: string;
  chat?: boolean;
}) {
  return (
    <div
      role="status"
      aria-label={label}
      aria-busy="true"
      className="flex h-full min-h-0 flex-1 flex-col overflow-hidden"
    >
      <div className="flex h-12 shrink-0 items-center border-b border-border-seam px-3">
        <Skeleton className="h-4 w-40 max-w-[50%]" />
      </div>
      <div className="mx-auto min-h-0 w-full max-w-[760px] flex-1 overflow-hidden px-4 pt-4">
        {chat ? (
          <ThreadTimelineLoadingSkeleton label={label} />
        ) : (
          <SecondaryPanelContentSkeleton label={label} />
        )}
      </div>
      {chat && (
        <div className="mx-auto w-full max-w-[760px] shrink-0 px-4 pb-4">
          <Skeleton className="h-[151px] w-full rounded-xl max-md:h-[50px]" />
        </div>
      )}
    </div>
  );
}
