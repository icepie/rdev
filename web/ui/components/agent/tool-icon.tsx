import { Icon } from "../icon";

const TOOL_ICON_NAMES: Record<string, string> = {
  shell: "ComputerTerminal01",
  read_file: "File",
  write_file: "EditFile",
  edit: "EditFile",
  grep: "Search",
  glob: "FolderOpen",
  list_dir: "FolderOpen",
  todo_write: "ListTodo",
  ask: "CircleQuestion",
};

export function ToolIcon({
  name,
  className,
}: {
  name: string;
  className?: string;
}) {
  return <Icon name={TOOL_ICON_NAMES[name] ?? "Toolbox"} className={className} />;
}
