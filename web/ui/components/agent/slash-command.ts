export interface ExecutableSlashCommand {
  name: string;
  runningOnly?: boolean;
  run(argument: string): boolean;
}

/** A recognized command never falls through to chat, even if sending fails.
 * Keep its draft until the transport accepts it (or validation succeeds). */
export function dispatchSlashCommand(
  text: string,
  commands: readonly ExecutableSlashCommand[],
  running: boolean,
  clearDraft: () => void,
): boolean {
  const match = text.match(/^\/([a-z]+)(?:\s+([\s\S]*))?$/i);
  if (!match) return false;
  const command = commands.find(command => command.name === match[1].toLowerCase());
  if (!command) return false;
  if (command.runningOnly && !running) return true;
  if (command.run((match[2] ?? "").trim())) clearDraft();
  return true;
}
