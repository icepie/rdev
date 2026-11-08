export interface ExecutableSlashCommand {
  name: string;
  aliases?: readonly string[];
  runningOnly?: boolean;
  allowArgs?: boolean;
  run(argument: string): boolean;
}

/** Shared recognition keeps menu dispatch and attachment validation in sync. */
export function matchSlashCommand<T extends ExecutableSlashCommand>(
  text: string,
  commands: readonly T[],
): { command: T; argument: string } | null {
  const match = text.match(/^\/([a-z]+)(?:\s+([\s\S]*))?$/i);
  if (!match) return null;
  const command = commands.find(command => command.name === match[1].toLowerCase() || command.aliases?.includes(match[1].toLowerCase()));
  return command ? { command, argument: (match[2] ?? "").trim() } : null;
}

/** A recognized command never falls through to chat, even if sending fails.
 * Keep its draft until the transport accepts it (or validation succeeds). */
export function dispatchSlashCommand(
  text: string,
  commands: readonly ExecutableSlashCommand[],
  running: boolean,
  clearDraft: () => void,
  onRejected?: (name: string, reason: "inactive" | "arguments") => void,
): boolean {
  const match = matchSlashCommand(text, commands);
  if (!match) return false;
  const { command, argument } = match;
  if (argument && !command.allowArgs) {
    onRejected?.(command.name, "arguments");
    return true;
  }
  if (command.runningOnly && !running) {
    onRejected?.(command.name, "inactive");
    return true;
  }
  if (command.run(argument)) clearDraft();
  return true;
}
