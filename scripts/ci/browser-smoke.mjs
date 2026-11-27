#!/usr/bin/env node
/* Real-browser smoke test for the production Web bundle.
 *
 * Boots rdev-server + rdev-client on loopback with a fixed test admin token,
 * then drives headless Chromium through the real terminal page and files page:
 *   1. the device appears in the page's device list,
 *   2. typed input is echoed back through PTY -> server -> browser socket,
 *   3. the shell's `exit` arrives as a {"op":"exit"} control frame,
 *   4. a small file downloaded from the device matches the file on disk.
 *
 * Isolated HOME/XDG/TMP dirs for both Go processes, no secrets, loopback only.
 * On failure the server/client logs and page screenshots stay in --artifacts.
 *
 * Usage:
 *   node scripts/ci/browser-smoke.mjs --server <bin> --client <bin> \
 *     [--playwright-root <dir with node_modules/playwright>] \
 *     [--artifacts <dir>] [--keep]
 */
import { spawn } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { closeSync, existsSync, openSync, readFileSync } from 'node:fs';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import assert from 'node:assert/strict';

const TOKEN = 'rdev-browser-smoke-token';
const DEVICE_ID = 'browser-smoke-device';
const DEVICE_NAME = 'Browser Smoke Device';
const MARKER = `RDEV_SMOKE_${randomBytes(6).toString('hex').toUpperCase()}`;
const DOWNLOAD_NAME = `rdev-smoke-${MARKER}.txt`;
const DOWNLOAD_CONTENT =
  `${MARKER}\n` +
  Array.from({ length: 40 }, (_, i) => `rdev browser smoke line ${i} ${MARKER}`).join('\n') +
  '\n';
const READY_TIMEOUT = 30_000;
const ACTION_TIMEOUT = 30_000;

function arg(name, fallback) {
  const i = process.argv.indexOf(`--${name}`);
  if (i < 0) return fallback;
  if (i + 1 >= process.argv.length || process.argv[i + 1].startsWith('--')) return true;
  return process.argv[i + 1];
}

const serverBin = resolve(String(arg('server', '')));
const clientBin = resolve(String(arg('client', '')));
const keep = arg('keep', false) === true;
if (!arg('server', null) || !arg('client', null)) {
  console.error('usage: browser-smoke.mjs --server <bin> --client <bin> [--playwright-root <dir>] [--artifacts <dir>] [--keep]');
  process.exit(2);
}

const sha = (data) => createHash('sha256').update(data).digest('hex');
const delay = (ms) => new Promise((r) => setTimeout(r, ms));

async function freePorts(count) {
  const servers = [];
  try {
    for (let i = 0; i < count; i++) {
      const server = createServer();
      await new Promise((res, rej) => {
        server.once('error', rej);
        server.listen(0, '127.0.0.1', res);
      });
      servers.push(server);
    }
    return servers.map((s) => s.address().port);
  } finally {
    await Promise.all(servers.map((s) => new Promise((r) => s.close(r))));
  }
}

const temp = await mkdtemp(join(tmpdir(), 'rdev-browser-smoke-'));
const artifacts = resolve(String(arg('artifacts', join(temp, 'artifacts'))));
await mkdir(artifacts, { recursive: true });
const home = join(temp, 'home');
const configHome = join(temp, 'config');
const dataHome = join(temp, 'data');
const cacheHome = join(temp, 'cache');
const tmpDir = join(temp, 'tmp');
const identityDir = join(temp, 'identity');
const downloadsDir = join(temp, 'downloads');
const downloadedPath = join(downloadsDir, DOWNLOAD_NAME);
for (const dir of [home, configHome, dataHome, cacheHome, tmpDir, identityDir, downloadsDir]) {
  await mkdir(dir, { recursive: true });
}
await writeFile(join(home, DOWNLOAD_NAME), DOWNLOAD_CONTENT);

const playwrightRoot = resolve(String(arg('playwright-root', process.cwd())));
const requirePlaywright = createRequire(join(playwrightRoot, 'noop.js'));
let chromium;
try {
  ({ chromium } = requirePlaywright('playwright'));
} catch (error) {
  console.error(`cannot load playwright from ${playwrightRoot}: ${error.message}`);
  process.exit(2);
}

const children = [];
function fixtureEnv(extra) {
  const env = {
    ...process.env,
    HOME: home,
    XDG_CONFIG_HOME: configHome,
    XDG_DATA_HOME: dataHome,
    XDG_CACHE_HOME: cacheHome,
    TMPDIR: tmpDir,
    RDEV_IDENTITY_DIR: identityDir,
    LANG: 'C.UTF-8',
    ...extra,
  };
  for (const key of [
    'RDEV_SERVER', 'RDEV_ID', 'RDEV_DISPLAY_NAME', 'RDEV_ADMIN_TOKEN',
    'RDEV_PUBLIC_SERVER', 'RDEV_TCP_ADDR', 'RDEV_KCP_ADDR',
    'DISPLAY', 'WAYLAND_DISPLAY',
  ]) {
    delete env[key];
  }
  return env;
}

function launch(name, binary, args, env) {
  if (!existsSync(binary)) throw new Error(`${name} binary not found: ${binary}`);
  const logPath = join(artifacts, `${name}.log`);
  const fd = openSync(logPath, 'a');
  const child = spawn(binary, args, { env, stdio: ['ignore', fd, fd], detached: true });
  closeSync(fd);
  child.on('error', (error) => console.error(`${name} spawn error: ${error.message}`));
  const entry = { name, child, logPath };
  children.push(entry);
  return entry;
}

function logTail(entry, limit = 4000) {
  try {
    const text = readFileSync(entry.logPath, 'utf8');
    return text.length > limit ? text.slice(-limit) : text;
  } catch {
    return '';
  }
}

function fixturesAlive() {
  const dead = children.find((c) => c.child.exitCode !== null || c.child.signalCode !== null);
  if (dead) {
    throw new Error(`${dead.name} exited (code ${dead.child.exitCode}, signal ${dead.child.signalCode})\n${logTail(dead)}`);
  }
}

async function waitForValue(label, probe, timeout = READY_TIMEOUT) {
  const deadline = Date.now() + timeout;
  let lastError;
  while (Date.now() < deadline) {
    fixturesAlive();
    try {
      const value = await probe();
      if (value) return value;
    } catch (error) {
      lastError = error;
    }
    await delay(100);
  }
  throw new Error(`${label}: timed out after ${timeout}ms${lastError ? ` (${lastError.message})` : ''}`);
}

async function stopChildren() {
  for (const { child } of children) {
    if (child.exitCode !== null || child.signalCode !== null) continue;
    try { process.kill(-child.pid, 'SIGTERM'); } catch { try { child.kill('SIGTERM'); } catch {} }
  }
  for (const { child } of children) {
    for (let i = 0; i < 40 && child.exitCode === null && child.signalCode === null; i++) await delay(50);
    if (child.exitCode === null && child.signalCode === null) {
      try { process.kill(-child.pid, 'SIGKILL'); } catch { try { child.kill('SIGKILL'); } catch {} }
    }
  }
}

const [httpPort, sshPort] = await freePorts(2);
const origin = `http://127.0.0.1:${httpPort}`;

launch('server', serverBin, [
  '--http', `127.0.0.1:${httpPort}`,
  '--tcp', '',
  '--kcp', '',
  '--ssh', `127.0.0.1:${sshPort}`,
  '--data', join(temp, 'server-data'),
  '--admin-token', TOKEN,
  '--no-auto-update',
], fixtureEnv({ RDEV_AUTO_UPDATE: 'false' }));

launch('client', clientBin, [
  '-s', `ws://127.0.0.1:${httpPort}`,
  '-i', DEVICE_ID,
  '--display-name', DEVICE_NAME,
  '--shell', '/bin/sh',
  '--no-auto-update',
], fixtureEnv({ RDEV_SHELL: '/bin/sh', RDEV_AUTO_UPDATE: 'false' }));

let browser;
const pages = [];
let failed = false;

async function captureFailure(reason) {
  failed = true;
  console.error(`\nFAILED: ${reason}`);
  for (const [index, page] of pages.entries()) {
    try {
      await page.screenshot({ path: join(artifacts, `failure-page-${index}.png`), fullPage: true });
    } catch {}
    try {
      const text = await page.locator('body').innerText({ timeout: 2000 });
      await writeFile(join(artifacts, `failure-page-${index}.txt`), text);
    } catch {}
  }
  for (const entry of children) {
    await writeFile(join(artifacts, `${entry.name}.tail.log`), logTail(entry, 20000));
  }
}

try {
  await waitForValue('server /healthz', async () => (await fetch(`${origin}/healthz`)).ok);

  await waitForValue('device registration', async () => {
    const res = await fetch(`${origin}/api/clients?token=${encodeURIComponent(TOKEN)}`);
    if (!res.ok) return false;
    const devices = await res.json();
    return Array.isArray(devices) && devices.some((device) => device.id === DEVICE_ID);
  });
  console.log(`server ready on ${origin}, device ${DEVICE_ID} connected`);

  browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({
    acceptDownloads: true,
    locale: 'en-US',
    viewport: { width: 1440, height: 900 },
  });
  // Headless Chromium exposes File System Access but has no save dialog. Hide
  // the picker so the pages take their own blob-download fallback path, which
  // is a real browser download with no interactive UI. Nothing else is shimmed.
  await context.addInitScript(() => {
    try {
      Object.defineProperty(window, 'showSaveFilePicker', { value: undefined, configurable: true });
    } catch {}
  });

  /* ── Terminal page ─────────────────────────────────────────── */
  const terminalPage = await context.newPage();
  pages.push(terminalPage);
  const termFrames = [];
  terminalPage.on('websocket', (ws) => {
    if (!ws.url().includes('/terminal')) return;
    ws.on('framereceived', (frame) => termFrames.push(frame.payload));
  });

  await terminalPage.goto(
    `${origin}/terminal?device=${encodeURIComponent(DEVICE_ID)}&token=${encodeURIComponent(TOKEN)}`,
    { waitUntil: 'domcontentloaded' },
  );
  await terminalPage.getByText(DEVICE_ID, { exact: false }).first()
    .waitFor({ state: 'visible', timeout: READY_TIMEOUT });
  console.log('device visible in the page device list');

  const binaryText = () => termFrames
    .filter((payload) => Buffer.isBuffer(payload))
    .map((payload) => payload.toString('utf8'))
    .join('');
  const textFrames = () => termFrames.filter((payload) => typeof payload === 'string');

  await waitForValue('terminal ready frame', () => textFrames().some((frame) => frame.includes('"op":"ready"')));

  await terminalPage.locator('.xterm-screen').first().click({ timeout: ACTION_TIMEOUT });
  await terminalPage.keyboard.type(`printf '%s\\n' ${MARKER}`);
  await terminalPage.keyboard.press('Enter');
  await waitForValue(`terminal echo of ${MARKER}`, () => binaryText().includes(MARKER), ACTION_TIMEOUT);
  console.log('terminal echoed the unique marker');

  await terminalPage.keyboard.type('exit 0');
  await terminalPage.keyboard.press('Enter');
  const exitFrame = await waitForValue(
    'terminal exit frame',
    () => textFrames().find((frame) => /"op"\s*:\s*"exit"/.test(frame)),
    ACTION_TIMEOUT,
  );
  const exitMessage = JSON.parse(exitFrame);
  assert.equal(exitMessage.op, 'exit', `unexpected exit frame: ${exitFrame}`);
  // terminalMsg.Code has `omitempty`, so a clean exit omits the field.
  assert.equal(Number(exitMessage.code ?? 0), 0, `shell exit code: ${exitFrame}`);
  console.log('terminal delivered the exit frame with code 0');
  await terminalPage.screenshot({ path: join(artifacts, 'terminal.png') }).catch(() => {});

  /* ── Files page: download a file from the device ───────────── */
  const filesPage = await context.newPage();
  pages.push(filesPage);
  await filesPage.goto(
    `${origin}/files?device=${encodeURIComponent(DEVICE_ID)}&token=${encodeURIComponent(TOKEN)}`,
    { waitUntil: 'domcontentloaded' },
  );
  const fileRow = filesPage.locator('li[role="treeitem"]').filter({ hasText: DOWNLOAD_NAME }).first();
  await fileRow.waitFor({ state: 'visible', timeout: READY_TIMEOUT });
  const downloadButton = fileRow.locator(`button[aria-label*="${DOWNLOAD_NAME}"]`).first();
  await downloadButton.hover({ timeout: ACTION_TIMEOUT });
  const downloadEvent = filesPage.waitForEvent('download', { timeout: ACTION_TIMEOUT });
  await downloadButton.click({ timeout: ACTION_TIMEOUT });
  const download = await downloadEvent;
  await download.saveAs(downloadedPath);
  const downloaded = await readFile(downloadedPath);
  assert.equal(sha(downloaded), sha(Buffer.from(DOWNLOAD_CONTENT)), 'downloaded bytes differ from the device file');
  console.log(`downloaded ${DOWNLOAD_NAME} (${downloaded.length} bytes, sha256 ${sha(downloaded).slice(0, 12)}…)`);
  await filesPage.screenshot({ path: join(artifacts, 'files.png') }).catch(() => {});

  await writeFile(
    join(artifacts, 'result.txt'),
    `ok\nmarker=${MARKER}\ndevice=${DEVICE_ID}\ndownload=${DOWNLOAD_NAME}\nsha256=${sha(downloaded)}\n`,
  );
  console.log(`\nPASS: ${MARKER}`);
} catch (error) {
  await captureFailure(error && error.stack ? error.stack : String(error));
} finally {
  if (browser) await browser.close().catch(() => {});
  await stopChildren();
  if (!keep && !artifacts.startsWith(temp + '/')) {
    await rm(temp, { recursive: true, force: true }).catch(() => {});
  }
}

if (failed) process.exitCode = 1;
