import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { decodeFrame, encodeFrame } from '../ui/lib/file-transfer-frame';

// The Go, Rust and Java tests read the same fixture set (see
// docs/protocol-framing.md). The web relay only speaks the layer-C file
// transfer frame, so this suite covers the offset rows and the frames every
// layer-C decoder must reject.
const root = resolve(dirname(fileURLToPath(import.meta.url)), '../../internal/transport/testdata/framing');

function bytes(file) {
  const raw = readFileSync(resolve(root, file));
  return raw.buffer.slice(raw.byteOffset, raw.byteOffset + raw.byteLength);
}

function hex(text) {
  return Buffer.from(text, 'hex');
}

function innerRows() {
  const rows = [];
  let section = '';
  for (const line of readFileSync(resolve(root, 'fixtures.tsv'), 'utf8').split('\n')) {
    if (!line || line.startsWith('#')) continue;
    if (line.startsWith('[')) {
      section = line;
      continue;
    }
    if (!section.startsWith('[inner]')) continue;
    const fields = line.split('\t');
    rows.push({
      name: fields[0],
      file: fields[1],
      expect: fields[2],
      typ: Number(fields[3]),
      id: fields[4],
      offseted: fields[5] === '1',
      offset: fields[6] === '' ? 0 : Number(fields[6]),
      payload: hex(fields[7] ?? ''),
    });
  }
  return rows;
}

const rows = innerRows();
const webRows = rows.filter((row) => row.expect !== 'ok' || row.offseted);

describe('file transfer frame fixtures shared with the Go, Rust and Java clients', () => {
  it('parses the shared manifest', () => {
    expect(rows.length).toBeGreaterThan(10);
    expect(webRows.length).toBeGreaterThan(5);
  });

  it('rejects every frame that must be rejected', () => {
    const rejected = rows.filter((row) => row.expect.startsWith('error:'));
    expect(rejected.length).toBeGreaterThan(0);
    for (const row of rejected) {
      expect(decodeFrame(bytes(row.file)), row.name).toBeNull();
    }
  });

  it('decodes the offset frames as the other runtimes do', () => {
    for (const row of webRows.filter((entry) => entry.expect === 'ok')) {
      const frame = decodeFrame(bytes(row.file));
      if (!row.id) {
        // Documented difference: the web relay requires a task id, direct
        // client transports accept an empty one.
        expect(frame, row.name).toBeNull();
        continue;
      }
      expect(frame, row.name).not.toBeNull();
      expect(frame.typ, row.name).toBe(row.typ);
      expect(frame.taskId, row.name).toBe(row.id);
      expect(frame.offset, row.name).toBe(row.offset);
      expect(Buffer.from(frame.payload), row.name).toEqual(row.payload);
    }
  });

  it('encodes back to the same bytes', () => {
    for (const row of webRows.filter((entry) => entry.expect === 'ok' && entry.id)) {
      const payload = row.payload.length
        ? row.payload.buffer.slice(row.payload.byteOffset, row.payload.byteOffset + row.payload.byteLength)
        : undefined;
      const encoded = encodeFrame(row.typ, row.id, row.offset, payload);
      expect(Buffer.from(encoded), row.name).toEqual(readFileSync(resolve(root, row.file)));
    }
  });
});
