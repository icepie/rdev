import { LitElement, html } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { appStyles } from './styles';
import { Device, authUrl, loadDevices, wsUrl } from './api';

declare global {
  interface Window { Terminal: any; FitAddon: any; WebLinksAddon: any; AddonWebgl?: any; }
}

@customElement('rdev-terminal')
export class RdevTerminal extends LitElement {
  static styles = appStyles;
  @state() devices: Device[] = [];
  @state() selected = new URLSearchParams(location.search).get('device') || '';
  @state() status = 'Select a device';
  private term?: any;
  private fit?: any;
  private ws?: WebSocket;

  connectedCallback() {
    super.connectedCallback();
    this.refresh();
    window.addEventListener('resize', () => this.fitTerminal());
  }

  async refresh() {
    try {
      this.devices = await loadDevices();
      if (!this.selected && this.devices[0]) this.selected = this.devices[0].id;
    } catch {}
  }

  firstUpdated() { this.initTerm(); }

  initTerm() {
    const host = this.renderRoot.querySelector('#terminal') as HTMLElement;
    this.term = new window.Terminal({ cursorBlink: true, fontSize: 14, theme: { background: '#020617', foreground: '#e2e8f0' }, scrollback: 10000 });
    this.fit = new window.FitAddon.FitAddon();
    this.term.loadAddon(this.fit);
    this.term.loadAddon(new window.WebLinksAddon.WebLinksAddon());
    try { if (window.AddonWebgl) this.term.loadAddon(new window.AddonWebgl.WebglAddon()); } catch {}
    this.term.open(host);
    this.fitTerminal();
    this.term.onData((data: string) => this.ws?.readyState === WebSocket.OPEN && this.ws.send(new TextEncoder().encode(data)));
  }

  fitTerminal() {
    this.fit?.fit();
    if (this.ws?.readyState === WebSocket.OPEN && this.fit) {
      this.ws.send(JSON.stringify({ op: 'resize', rows: this.term.rows, cols: this.term.cols }));
    }
  }

  connect() {
    if (!this.selected || !this.term) return;
    this.ws?.close();
    this.term.reset();
    this.status = `Connecting ${this.selected}...`;
    this.ws = new WebSocket(wsUrl(`/terminal?device=${encodeURIComponent(this.selected)}`));
    this.ws.binaryType = 'arraybuffer';
    this.ws.onopen = () => { this.status = `Connected: ${this.selected}`; this.fitTerminal(); };
    this.ws.onmessage = evt => {
      if (evt.data instanceof ArrayBuffer) { this.term.write(new Uint8Array(evt.data)); return; }
      const msg = JSON.parse(evt.data);
      if (msg.op === 'auth') {
        const password = prompt(msg.message || 'Password required') || '';
        this.ws?.send(JSON.stringify({ op: 'auth', password }));
      } else if (msg.op === 'auth_ok') this.fitTerminal();
      else if (msg.op === 'exit') this.status = `Exited (${msg.code})`;
      else if (msg.op === 'error') this.term.writeln(`\r\n[error] ${msg.message}`);
    };
    this.ws.onclose = () => { this.status = 'Disconnected'; };
  }

  render() {
    return html`<main class="shell">
      <header class="topbar"><div class="brand"><span class="logo">⌘</span><span>Terminal</span></div><nav class="nav"><a href=${authUrl('/')}>Dashboard</a><a class="active" href=${authUrl('/terminal.html')}>Terminal</a><a href=${authUrl('/batch.html')}>Batch</a></nav></header>
      <section class="split">
        <aside class="card"><div class="row"><h2>Devices</h2><button @click=${this.refresh}>↻</button></div><div class="list">
          ${this.devices.map(d => html`<div class="selectable ${this.selected === d.id ? 'active' : ''}" @click=${() => { this.selected = d.id; }}><div class="row"><b>${d.id}</b><span class="dot"></span></div><div class="muted">${d.hasPassword ? 'Password' : 'Open'}</div></div>`)}
        </div><button class="primary" style="width:100%;margin-top:14px" @click=${this.connect}>Connect</button><p>${this.status}</p></aside>
        <section class="terminal-wrap"><div id="terminal"></div></section>
      </section>
    </main>`;
  }
}
