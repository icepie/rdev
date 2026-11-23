import { expect, it } from "vitest";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";

/* Translation keys are looked up by string, so TypeScript cannot see a key
 * that no longer has a caller. This guard scans the UI sources for literal
 * key references and fails when a static key is orphaned.
 *
 * Families built at run time (`t(`agent.status.${x}`)`) are matched by prefix.
 * The explicit list below is the known backlog: keys nothing references yet.
 * Shrink it when the copy is removed; never grow it for new strings. */
const dynamicKeyPrefixes = [
  "agent.autonomy.",
  "agent.jobs.",
  "agent.status.",
  "agent.worktreeState.",
  "agent.",
  "automation.",
  "desktop.gpu.",
  "files.",
  "tasks.status.",
];

const knownUnused = new Set([
  "browser.dragResize",
  "nav.sessions",
  "index.navBatch",
  "index.navSessions",
  "index.androidDownloadDirect",
  "index.vncUnavailable",
  "sessions.title",
  "split.empty",
]);

function sourceFiles(dir) {
  return readdirSync(dir).flatMap((entry) => {
    const path = join(dir, entry);
    if (statSync(path).isDirectory()) return sourceFiles(path);
    return /\.(ts|tsx)$/.test(entry) ? [path] : [];
  });
}

it("keeps every static dictionary key referenced", () => {
  const dictionaryPath = "web/ui/lib/dictionaries.ts";
  const keys = new Set(
    [...readFileSync(dictionaryPath, "utf8").matchAll(/^\s*"([^"]+)":/gm)].map((match) => match[1]),
  );
  const sources = sourceFiles("web/ui")
    .filter((path) => !path.endsWith("dictionaries.ts"))
    .map((path) => readFileSync(path, "utf8"));

  const orphaned = [...keys].filter(
    (key) =>
      !knownUnused.has(key) &&
      !dynamicKeyPrefixes.some((prefix) => key.startsWith(prefix)) &&
      !sources.some((source) => source.includes(`"${key}"`)),
  );

  expect(orphaned).toEqual([]);
});
