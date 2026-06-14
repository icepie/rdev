export type Device = { id: string; connectedAt: string; sessions?: number; forwards?: number; hasPassword: boolean };

export const tokenParam = () => new URLSearchParams(location.search).get('token');

export function getToken(): string {
  const token = tokenParam() || localStorage.getItem('rdevAdminToken') || '';
  if (token) localStorage.setItem('rdevAdminToken', token);
  return token;
}

export function setToken(token: string) {
  localStorage.setItem('rdevAdminToken', token);
}

export function authUrl(path: string, token = getToken()): string {
  return token ? `${path}${path.includes('?') ? '&' : '?'}token=${encodeURIComponent(token)}` : path;
}

export async function authFetch(path: string, init?: RequestInit): Promise<Response> {
  let res = await fetch(authUrl(path), init);
  if (res.status === 401) {
    const token = prompt('Admin token required') || '';
    if (token) {
      setToken(token);
      res = await fetch(authUrl(path, token), init);
    }
  }
  return res;
}

export async function loadDevices(): Promise<Device[]> {
  const res = await authFetch('/api/devices');
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.json();
}

export function wsUrl(path: string): string {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${proto}//${location.host}${authUrl(path)}`;
}

export function escapeHtml(value: string): string {
  return value.replace(/[&<>'"]/g, ch => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#39;', '"': '&quot;' }[ch]!));
}

export function decodeBinFrame(buf: ArrayBuffer) {
  const view = new DataView(buf);
  const typ = view.getUint8(0);
  const idLen = view.getUint8(1);
  const id = new TextDecoder().decode(new Uint8Array(buf, 2, idLen));
  const payload = new Uint8Array(buf, 2 + idLen);
  return { typ, id, payload };
}

export function encodeFileFrame(type: number, id: string, meta: unknown, mode: number, data: ArrayBuffer): ArrayBuffer {
  const enc = new TextEncoder();
  const idBytes = enc.encode(id);
  const metaBytes = enc.encode(JSON.stringify(meta));
  const fileBytes = new Uint8Array(data);
  const buf = new ArrayBuffer(2 + idBytes.length + 2 + metaBytes.length + 4 + fileBytes.length);
  const view = new DataView(buf);
  let pos = 0;
  view.setUint8(pos++, type);
  view.setUint8(pos++, idBytes.length);
  new Uint8Array(buf, pos, idBytes.length).set(idBytes); pos += idBytes.length;
  view.setUint16(pos, metaBytes.length); pos += 2;
  new Uint8Array(buf, pos, metaBytes.length).set(metaBytes); pos += metaBytes.length;
  view.setUint32(pos, mode); pos += 4;
  new Uint8Array(buf, pos).set(fileBytes);
  return buf;
}

export function encodeChunk(id: string, data: ArrayBuffer): ArrayBuffer {
  const enc = new TextEncoder();
  const idBytes = enc.encode(id);
  const chunk = new Uint8Array(data);
  const buf = new ArrayBuffer(2 + idBytes.length + chunk.length);
  const out = new Uint8Array(buf);
  out[0] = 0x06;
  out[1] = idBytes.length;
  out.set(idBytes, 2);
  out.set(chunk, 2 + idBytes.length);
  return buf;
}

export function encodeEnd(id: string): ArrayBuffer {
  const enc = new TextEncoder();
  const idBytes = enc.encode(id);
  const buf = new ArrayBuffer(2 + idBytes.length);
  const out = new Uint8Array(buf);
  out[0] = 0x07;
  out[1] = idBytes.length;
  out.set(idBytes, 2);
  return buf;
}
