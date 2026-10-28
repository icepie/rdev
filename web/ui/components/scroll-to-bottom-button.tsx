import { cn } from "../lib/utils";
import { Icon } from "./icon";

/* Floating round button over the bottom of the transcript; slides/fades
 * in when the view is scrolled away from the bottom. `active` marks
 * unread events below by tinting the icon. */
export function ScrollToBottomButton({
  visible,
  active = false,
  onClick,
  label,
}: {
  visible: boolean;
  active?: boolean;
  onClick: () => void;
  label: string;
}) {
  return (
    <div className="flex h-0 items-center justify-center">
      <button
        onClick={onClick}
        className={cn(
          "z-20 -mt-20 flex size-8 cursor-pointer items-center justify-center rounded-full border border-border bg-background transition-all duration-200 hover:bg-accent",
          visible
            ? "translate-y-0 opacity-100"
            : "pointer-events-none invisible translate-y-2 opacity-0",
        )}
        aria-label={label}
        type="button"
      >
        <Icon
          name="ArrowDown"
          className={cn("size-4", active && visible && "text-primary")}
        />
      </button>
    </div>
  );
}
