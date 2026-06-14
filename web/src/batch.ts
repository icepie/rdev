import { LitElement, html } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { appStyles } from './styles';
import { Device, authUrl, decodeBinFrame, encodeChunk, encodeEnd, encodeFileFrame, loadDevices, wsUrl } from './api';

type Result = { output: string; state: 'running' | 'ok' | 'fail'; label: string };

@customElement('rdev-batch')
export class RdevBatch extends LitElement {
  static styles = appStyles;
  @state() devices: Device[] = [];
  @state() selected = new Set<string>();
  @state() command = '';
  @state() file?: File;
  @state() path = '';
  @state() execResults = new Map<string, Result>();
  @state() uploadResults = new Map<string, Result>();
  private uploadAcks = new Map<string, () => void>();

  connectedCallback() { super.connectedCallback(); this.refresh(); }
  async refresh() { try { this.devices = await loadDevices(); } catch {} }
  toggle(id: string) { this.selected.has(id) ? this.selected.delete(id) : this.selected.add(id); this.requestUpdate(); }
  selectAll() { this.selected.size === this.devices.length ? this.selected.clear() : this.devices.forEach(d => this.selected.add(d.id)); this.requestUpdate(); }

  runExec() {
    const devices = [...this.selected];
    if (!devices.length || !this.command.trim()) return;
    this.execResults = new Map(devices.map(id => [id, { output: '', state: 'running', label: 'running' }]));
    const ws = new WebSocket(wsUrl('/batch'));
    ws.binaryType = 'arraybuffer';
    ws.onopen = () => ws.send(JSON.stringify({ op: 'exec', devices, command: this.command }));
    ws.onmessage = evt => {
      if (evt.data instanceof ArrayBuffer) {
        const { id, payload } = decodeBinFrame(evt.data);
        const r = this.execResults.get(id); if (r) r.output += new TextDecoder().decode(payload);
        this.requestUpdate(); return;
      }
      const msg = JSON.parse(evt.data);
      if (msg.deviceId && msg.op === 'exec_exit') this.execResults.set(msg.deviceId, { ...(this.execResults.get(msg.deviceId) || { output: '' }), state: msg.code === 0 ? 'ok' : 'fail', label: `exit ${msg.code}` });
      if (msg.deviceId && msg.op === 'error') this.execResults.set(msg.deviceId, { output: msg.message, state: 'fail', label: 'error' });
      this.requestUpdate();
      if (devices.every(id => this.execResults.get(id)?.state !== 'running')) ws.close();
    };
  }

  async upload() {
    const devices = [...this.selected];
    if (!this.file || !this.path || !devices.length) return;
    this.uploadResults = new Map(devices.map(id => [id, { output: this.path, state: 'running', label: 'uploading' }]));
    const ws = new WebSocket(wsUrl('/batch'));
    ws.binaryType = 'arraybuffer';
    ws.onopen = async () => {
      const uploadId = `upload-${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
      const meta = { devices, path: this.path, mode: 420 };
      ws.send(encodeFileFrame(0x05, uploadId, meta, 420, new ArrayBuffer(0)));
      for (let offset = 0; offset < this.file!.size; offset += 512 * 1024) {
        const chunk = await this.file!.slice(offset, offset + 512 * 1024).arrayBuffer();
        ws.send(encodeChunk(uploadId, chunk));
        await new Promise<void>(resolve => this.uploadAcks.set(uploadId, resolve));
      }
      ws.send(encodeEnd(uploadId));
    };
    ws.onmessage = evt => {
      if (evt.data instanceof ArrayBuffer) {
        const { typ, id } = decodeBinFrame(evt.data);
        if (typ === 0x08 && this.uploadAcks.has(id)) { this.uploadAcks.get(id)!(); this.uploadAcks.delete(id); }
        return;
      }
      const msg = JSON.parse(evt.data);
      if (msg.deviceId && msg.op === 'upload_result') this.uploadResults.set(msg.deviceId, { output: msg.message || this.path, state: msg.success ? 'ok' : 'fail', label: msg.success ? 'ok' : 'failed' });
      if (msg.deviceId && msg.op === 'error') this.uploadResults.set(msg.deviceId, { output: msg.message, state: 'fail', label: 'error' });
      this.requestUpdate();
      if (devices.every(id => this.uploadResults.get(id)?.state !== 'running')) ws.close();
    };
  }

  pickFile(e: Event) {
    const input = e.target as HTMLInputElement;
    this.file = input.files?.[0];
    if (this.file && !this.path) this.path = `/tmp/${this.file.name}`;
  }

  renderResults(results: Map<string, Result>) {
    return html`<div class="results">${[...results].map(([id, r]) => html`<article class="device"><div class="row"><b>${id}</b><span class="pill">${r.label}</span></div><pre>${r.output}</pre></article>`)}</div>`;
  }

  render() {
    return html`<main class="shell">
      <header class="topbar"><div class="brand"><span class="logo">⇄</span><span>Batch</span></div><nav class="nav"><a href=${authUrl('/')}>Dashboard</a><a href=${authUrl('/terminal.html')}>Terminal</a><a class="active" href=${authUrl('/batch.html')}>Batch</a></nav></header>
      <section class="grid" style="grid-template-columns:1fr 1fr">
        <div class="card"><div class="row"><h2>Targets</h2><button @click=${this.selectAll}>Select all</button></div><div class="devices">${this.devices.map(d => html`<div class="selectable ${this.selected.has(d.id) ? 'active' : ''}" @click=${() => this.toggle(d.id)}><div class="row"><b>${d.id}</b><span class="dot"></span></div></div>`)}</div></div>
        <div class="card"><h2>Command</h2><textarea .value=${this.command} @input=${(e: Event) => this.command = (e.target as HTMLTextAreaElement).value} placeholder="uname -a && uptime"></textarea><button class="primary" style="margin-top:10px" @click=${this.runExec}>Run command</button></div>
      </section>
      ${this.renderResults(this.execResults)}
      <section class="card" style="margin-top:14px"><h2>Binary File Distribution</h2><label class="drop"><input type="file" hidden @change=${this.pickFile}>${this.file ? this.file.name : 'Drop/click to choose file'}</label><input style="margin-top:10px" .value=${this.path} @input=${(e: Event) => this.path = (e.target as HTMLInputElement).value} placeholder="/tmp/file.bin"><button class="primary" style="margin-top:10px" @click=${this.upload}>Distribute binary chunks</button></section>
      ${this.renderResults(this.uploadResults)}
    </main>`;
  }
}
