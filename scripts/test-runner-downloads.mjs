// Run: node --test scripts/test-runner-downloads.mjs (POSIX, tar).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, writeFileSync, mkdirSync, rmSync, existsSync, readdirSync, symlinkSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync, execFileSync } from 'node:child_process';

const shell = readFileSync(new URL('../web/public/run.sh', import.meta.url), 'utf8');

function fixture(client = 'go') {
  const root = mkdtempSync(join(tmpdir(), 'rdev-download-test-'));
  const bin = join(root, 'bin'); mkdirSync(bin);
  const temp = join(root, 'tmp'); mkdirSync(temp);
  const home = join(root, 'home with spaces'); mkdirSync(home);
  const runner = join(root, 'run.sh'); writeFileSync(runner, shell);
  const payload = join(root, 'payload');
  const program = '#!/bin/sh\n' + (client === 'rs' ? 'test "$(cat "$(dirname "$0")/lib/runtime.txt")" = sidecar || exit 91\n' : '') + 'printf "%s\\n" "$@" > "$RDEV_TEST_OUTPUT"\n';
  if (client === 'rs') {
    const runtime = join(root, 'runtime'); mkdirSync(runtime); mkdirSync(join(runtime, 'lib'));
    writeFileSync(join(runtime, 'rdev-client-rs'), program, { mode: 0o755 });
    writeFileSync(join(runtime, 'lib/runtime.txt'), 'sidecar');
    execFileSync('tar', ['-czf', payload, '-C', root, 'runtime']);
  } else { writeFileSync(payload, program); }
  const curl = `#!${process.execPath}\n` + `const fs=require('fs');const a=process.argv.slice(2);const url=new URL(a[a.indexOf('-o')-1]);const asset=url.searchParams.get('asset')||url.pathname.split('/').pop();const out=a[a.indexOf('-o')+1];fs.appendFileSync(process.env.RDEV_TEST_ROOT+'/requests',url.href+'\\n');if(asset.endsWith('.sig')||fs.existsSync(process.env.RDEV_TEST_ROOT+'/offline'))process.exit(22);try{fs.copyFileSync(process.env.RDEV_TEST_ROOT+'/payload',out)}catch{process.exit(22)}\n`;
  writeFileSync(join(bin, 'curl'), curl, { mode: 0o755 });
  writeFileSync(join(bin, 'uname'), '#!/bin/sh\ncase "$1" in -s) echo Linux;; -m) echo x86_64;; -o) echo GNU/Linux;; *) echo fixture;; esac\n', { mode: 0o755 });
  writeFileSync(join(bin, 'id'), '#!/bin/sh\necho 0\n', { mode: 0o755 });
  // Expose only required utilities: neither OpenSSL nor another verifier exists.
  for (const tool of ['sh', 'head', 'grep', 'cp', 'rm', 'mv', 'mkdir', 'sed', 'tr', 'mktemp', 'chmod', 'tar', 'gzip', 'find', 'dirname', 'cat', 'getconf', 'awk']) {
    const path = execFileSync('/bin/sh', ['-c', 'command -v "$1"', 'sh', tool], { encoding: 'utf8' }).trim();
    symlinkSync(path, join(bin, tool));
  }
  const output = join(root, 'args');
  const run = (env = {}) => {
    rmSync(output, { force: true });
    return spawnSync('sh', [runner, '--server', 'ws://fixture.test:8080', '--version', '1.2.3', '--client', client, '--no-mirror', '--id', 'device with spaces', '--password', 'semi;quote"', '--shell', '/a shell', '--ssh-port', '2200'], {
      env: { ...process.env, ANDROID_ROOT: '', PATH: bin, HOME: home, TMPDIR: temp, RDEV_TEST_ROOT: root, RDEV_TEST_OUTPUT: output, ...env }, encoding: 'utf8', timeout: 30000,
    });
  };
  return { root, home, temp, output, payload, run, close() { rmSync(root, { recursive: true, force: true }); } };
}

for (const client of ['go', 'rs']) {
  test(`${client}: runs without OpenSSL or signatures; package cache retains runtime`, () => {
    const f = fixture(client);
    try {
      let result = f.run(); assert.equal(result.status, 0, result.stderr);
      const expected = ['-s', 'ws://fixture.test:8080', '-i', 'device with spaces', '-p', 'semi;quote"', client === 'rs' ? '--shell' : '-S', '/a shell', ...(client === 'rs' ? [] : ['--ssh-port', '2200'])];
      assert.deepEqual(readFileSync(f.output, 'utf8').trimEnd().split('\n'), expected);
      assert.ok(!readFileSync(join(f.root, 'requests'), 'utf8').includes('.sig'));
      writeFileSync(join(f.root, 'offline'), '');
      result = f.run(); assert.equal(result.status, 0, result.stderr);
      assert.ok(existsSync(f.output));
      // Poison an old extracted executable: it must never be used on a cache hit.
      const cache = join(f.temp, 'rdev-cache', readdirSync(join(f.temp, 'rdev-cache'))[0]);
      writeFileSync(join(cache, 'rdev-client-rs'), '#!/bin/sh\nexit 92\n', { mode: 0o755 });
      result = f.run(); assert.equal(result.status, 0, result.stderr);
      // Reject error-page cache contents when no download source is available.
      writeFileSync(join(cache, 'payload'), '<html>error</html>');
      result = f.run(); assert.notEqual(result.status, 0); assert.equal(existsSync(f.output), false);
    } finally { f.close(); }
  });
}

for (const client of ['go', 'rs']) {
  for (const invalid of ['empty', 'html', 'legacy-cache']) {
    test(`${client}: rejects ${invalid} before execution/publication`, () => {
      const f = fixture(client);
      try {
        if (invalid === 'empty') writeFileSync(f.payload, '');
        if (invalid === 'html') writeFileSync(f.payload, '<html>error</html>');
        if (invalid === 'legacy-cache') {
          const result = f.run(); assert.equal(result.status, 0, result.stderr);
          const cache = join(f.temp, 'rdev-cache', readdirSync(join(f.temp, 'rdev-cache'))[0]);
          rmSync(join(cache, 'payload'));
          writeFileSync(join(cache, '.runtime-complete'), '');
          writeFileSync(join(f.root, 'offline'), '');
        }
        const result = f.run(); assert.notEqual(result.status, 0, result.stderr); assert.equal(existsSync(f.output), false);
        if (invalid !== 'legacy-cache') assert.deepEqual(readdirSync(join(f.temp, 'rdev-cache')), []);
      } finally { f.close(); }
    });
  }
}

for (const client of ['go', 'rs']) {
  test(`${client}: falls back from a real noexec mount and preserves arguments/runtime`, (t) => {
    if (process.platform !== 'linux' || !existsSync('/dev/shm')) return t.skip('Requires Linux /dev/shm');
    const blocked = mkdtempSync('/dev/shm/rdev-noexec-test-');
    const f = fixture(client);
    try {
      const probe = join(blocked, 'probe');
      writeFileSync(probe, '#!/bin/sh\nexit 0\n', { mode: 0o700 });
      const denied = spawnSync(probe);
      if (denied.error?.code !== 'EACCES') return t.skip('/dev/shm is not noexec');
      rmSync(probe);
      let result = f.run({ TMPDIR: blocked });
      assert.equal(result.status, 0, result.stderr);
      assert.deepEqual(readFileSync(f.output, 'utf8').trimEnd().split('\n'), [
        '-s', 'ws://fixture.test:8080', '-i', 'device with spaces', '-p', 'semi;quote"',
        client === 'rs' ? '--shell' : '-S', '/a shell', ...(client === 'rs' ? [] : ['--ssh-port', '2200']),
      ]);
      assert.deepEqual(readdirSync(blocked), [], 'Rejected work directories must be cleaned');
      const fallback = join(f.home, '.cache', 'rdev');
      assert.deepEqual(readdirSync(fallback), ['rdev-cache'], 'Executed runtime must be cleaned');
      writeFileSync(join(f.root, 'offline'), '');
      result = f.run({ TMPDIR: blocked });
      assert.equal(result.status, 0, result.stderr);
      assert.ok(existsSync(f.output), 'Cached package must also execute outside noexec');
    } finally {
      f.close();
      rmSync(blocked, { recursive: true, force: true });
    }
  });
}

test('termux prefix selects the android asset when uname lacks -o', () => {
  const f = fixture('go');
  try {
    // Toybox/coreutils variants may not implement -o; Termux still sets PREFIX.
    writeFileSync(join(f.root, 'bin', 'uname'), '#!/bin/sh\ncase "$1" in -s) echo Linux;; -m) echo aarch64;; -o) exit 1;; esac\n', { mode: 0o755 });
    const result = f.run({ PREFIX: '/data/data/com.termux/files/usr', ANDROID_ROOT: '' });
    assert.equal(result.status, 0, result.stderr);
    const requests = readFileSync(join(f.root, 'requests'), 'utf8');
    assert.match(requests, /rdev-client-android-arm64/);
    assert.doesNotMatch(requests, /rdev-client-linux-arm64/);
  } finally {
    f.close();
  }
});
