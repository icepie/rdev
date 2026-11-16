// Run: node --test scripts/test-runner-signatures.mjs (POSIX, OpenSSL 3.0+, tar).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { generateKeyPairSync, sign } from 'node:crypto';
import { mkdtempSync, readFileSync, writeFileSync, mkdirSync, rmSync, existsSync, readdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync, execFileSync } from 'node:child_process';

const shell = readFileSync(new URL('../web/public/run.sh', import.meta.url), 'utf8');
const updater = readFileSync(new URL('../internal/updater/updater.go', import.meta.url), 'utf8');
const publicKey = updater.match(/const updatePublicKeyB64 = "([^"]+)"/)[1];
const spki = Buffer.concat([Buffer.from('302a300506032b6570032100', 'hex'), Buffer.from(publicKey, 'base64')]).toString('base64');


function fixture(client = 'go') {
  const root = mkdtempSync(join(tmpdir(), 'rdev-signature-test-'));
  const bin = join(root, 'bin'); mkdirSync(bin);
  const temp = join(root, 'tmp'); mkdirSync(temp);
  const { privateKey, publicKey: key } = generateKeyPairSync('ed25519');
  const fixtureKey = key.export({ format: 'der', type: 'spki' }).toString('base64');
  const runner = join(root, 'run.sh'); writeFileSync(runner, shell.replace(spki, fixtureKey));
  const payload = join(root, 'payload');
  const program = '#!/bin/sh\n' + (client === 'rs' ? 'test "$(cat "$(dirname "$0")/lib/runtime.txt")" = sidecar || exit 91\n' : '') + 'printf "%s\\n" "$@" > "$RDEV_TEST_OUTPUT"\n';
  if (client === 'rs') {
    const runtime = join(root, 'runtime'); mkdirSync(runtime); mkdirSync(join(runtime, 'lib'));
    writeFileSync(join(runtime, 'rdev-client-gpu'), program, { mode: 0o755 });
    writeFileSync(join(runtime, 'lib/runtime.txt'), 'sidecar');
    execFileSync('tar', ['-czf', payload, '-C', root, 'runtime']);
  } else { writeFileSync(payload, program); }
  writeFileSync(payload + '.sig', sign(null, readFileSync(payload), privateKey).toString('base64'));
  const curl = `#!${process.execPath}\n` + `const fs=require('fs');const a=process.argv.slice(2);const url=new URL(a[a.indexOf('-o')-1]);const asset=url.searchParams.get('asset')||url.pathname.split('/').pop();const out=a[a.indexOf('-o')+1];fs.appendFileSync(process.env.RDEV_TEST_ROOT+'/requests',url.href+'\\n');if(fs.existsSync(process.env.RDEV_TEST_ROOT+'/offline'))process.exit(22);try{fs.copyFileSync(process.env.RDEV_TEST_ROOT+'/payload'+(asset.endsWith('.sig')?'.sig':''),out)}catch{process.exit(22)}\n`;
  writeFileSync(join(bin, 'curl'), curl, { mode: 0o755 });
  writeFileSync(join(bin, 'uname'), '#!/bin/sh\ncase "$1" in -s) echo Linux;; -m) echo x86_64;; -o) echo GNU/Linux;; *) echo fixture;; esac\n', { mode: 0o755 });
  writeFileSync(join(bin, 'id'), '#!/bin/sh\necho 0\n', { mode: 0o755 });
  const output = join(root, 'args');
  const run = () => {
    rmSync(output, { force: true });
    return spawnSync('sh', [runner, '--server', 'ws://fixture.test:8080', '--version', '1.2.3', '--client', client, '--no-mirror', '--id', 'device with spaces', '--password', 'semi;quote"', '--shell', '/a shell', '--ssh-port', '2200'], {
      env: { ...process.env, ANDROID_ROOT: '', PATH: bin + ':' + process.env.PATH, TMPDIR: temp, RDEV_TEST_ROOT: root, RDEV_TEST_OUTPUT: output }, encoding: 'utf8', timeout: 30000,
    });
  };
  return { root, temp, output, payload, privateKey, run, close() { rmSync(root, { recursive: true, force: true }); } };
}

for (const client of ['go', 'cgo', 'rs']) {
  test(`${client}: signed release executes exact arguments; verified cache retains runtime`, () => {
    const f = fixture(client);
    try {
      let result = f.run(); assert.equal(result.status, 0, result.stderr);
      const expected = ['-s', 'ws://fixture.test:8080', '-i', 'device with spaces', '-p', 'semi;quote"', client === 'rs' ? '--shell' : '-S', '/a shell', ...(client === 'rs' ? [] : ['--ssh-port', '2200'])];
      assert.deepEqual(readFileSync(f.output, 'utf8').trimEnd().split('\n'), expected);
      writeFileSync(join(f.root, 'offline'), '');
      result = f.run(); assert.equal(result.status, 0, result.stderr);
      assert.ok(existsSync(f.output));
      // Poison an old extracted executable: it must never be used on a cache hit.
      const cache = join(f.temp, 'rdev-cache', readdirSync(join(f.temp, 'rdev-cache'))[0]);
      writeFileSync(join(cache, 'rdev-client-gpu'), '#!/bin/sh\nexit 92\n', { mode: 0o755 });
      result = f.run(); assert.equal(result.status, 0, result.stderr);
      // Poison signed cached bytes: no downloaded or old cached executable can run.
      writeFileSync(join(cache, 'payload'), 'tampered');
      result = f.run(); assert.notEqual(result.status, 0); assert.equal(existsSync(f.output), false);
    } finally { f.close(); }
  });
}

for (const client of ['go', 'rs']) {
  for (const invalid of ['missing', 'malformed', 'wrong-key', 'tampered', 'legacy-cache']) {
    test(`${client}: rejects ${invalid} before execution/publication`, () => {
      const f = fixture(client);
      try {
        if (invalid === 'missing') rmSync(f.payload + '.sig');
        if (invalid === 'malformed') writeFileSync(f.payload + '.sig', 'not a signature');
        if (invalid === 'wrong-key') writeFileSync(f.payload + '.sig', sign(null, readFileSync(f.payload), generateKeyPairSync('ed25519').privateKey).toString('base64'));
        if (invalid === 'tampered') writeFileSync(f.payload, 'tampered');
        if (invalid === 'legacy-cache') {
          const result = f.run(); assert.equal(result.status, 0, result.stderr);
          const cache = join(f.temp, 'rdev-cache', readdirSync(join(f.temp, 'rdev-cache'))[0]);
          rmSync(join(cache, 'payload.sig'));
          writeFileSync(join(cache, '.runtime-complete'), '');
          writeFileSync(join(f.root, 'offline'), '');
        }
        const result = f.run(); assert.notEqual(result.status, 0, result.stderr); assert.equal(existsSync(f.output), false);
        if (invalid !== 'legacy-cache') assert.deepEqual(readdirSync(join(f.temp, 'rdev-cache')), []);
      } finally { f.close(); }
    });
  }
}
