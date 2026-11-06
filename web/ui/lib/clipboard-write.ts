/** Browser clipboard writes cannot be canceled once submitted. Only their
 * current owner may commit fingerprints or display completion/error state. */
export async function writeOwnedClipboard(
  current: () => boolean,
  write: () => Promise<void>,
  commit: () => void,
  failed: (error: unknown) => void,
): Promise<void> {
  if (!current()) return;
  try {
    await write();
    if (current()) commit();
  } catch (error) {
    if (current()) failed(error);
  }
}
