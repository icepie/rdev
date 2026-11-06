import { useRef } from "react";

/** Keep failed edits retryable; composition keys must not submit a title. */
export function ThreadRenameInput({ title, label, onCommit, onFinish }: {
  title: string;
  label: string;
  onCommit(title: string): boolean;
  onFinish(): void;
}) {
  const settled = useRef(false);
  const commit = (value: string) => {
    if (settled.current || !value.trim() || !onCommit(value.trim())) return;
    settled.current = true;
    onFinish();
  };
  return <input
    autoFocus defaultValue={title} aria-label={label}
    className="h-5 min-w-0 flex-1 rounded-sm border border-timeline-accent bg-background px-1 text-sm outline-none"
    onClick={event => event.stopPropagation()}
    onPointerDown={event => event.stopPropagation()}
    onKeyDown={event => {
      event.stopPropagation();
      if (event.nativeEvent.isComposing || event.nativeEvent.keyCode === 229) return;
      if (event.key === "Enter") {
        event.preventDefault();
        commit(event.currentTarget.value);
      } else if (event.key === "Escape") {
        event.preventDefault();
        settled.current = true;
        onFinish();
      }
    }}
    onBlur={event => commit(event.currentTarget.value)}
  />;
}
