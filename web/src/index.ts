import { LitElement, html } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { appStyles, icons } from './styles';
import { Device, authFetch } from './api';

@customElement('rdev-dashboard')
export class RdevDashboard extends LitElement {
  static styles = appStyles;
  @state() devices: Device[] = [];
  @state() cfg = { sshPort: '', httpHost: '', authRequired: 'false' };

  connectedCallback() {
    super.connectedCallback();
    this.refresh();
    window.setInterval(() => this.refresh(), 4000);
  }

  async refresh() {
    try {
      this.cfg = await (await fetch('/api/config')).json();
      this.devices = await (await authFetch('/api/clients')).json();
    } catch {}
  }

  render() {
    const sessions = this.devices.reduce((n, d) => n + (d.sessions || 0), 0);
    const forwards = this.devices.reduce((n, d) => n + (d.forwards || 0), 0);
    return html`
      <main class="shell">
        <header class="topbar">
          <div class="brand"><span class="logo">${icons.zap}</span><span>RDev</span></div>
          <nav class="nav"><a class="active" href="/">Dashboard</a><a href="/terminal.html">Terminal</a><a href="/batch.html">Batch</a></nav>
        </header>
        <section class="hero">
          <div>
            <span class="pill">${this.cfg.authRequired === 'true' ? 'Web auth enabled' : 'Open web mode'}</span>
            <h1>Remote devices, one control plane.</h1>
            <p>High-throughput SSH, terminal, TCP forwarding and binary batch distribution for connected machines.</p>
          </div>
          <div class="card">
            <h2>${icons.shield} Connect</h2>
            <p><b>Client</b><br><code>rdev-client -s ws://${this.cfg.httpHost || 'server:8080'} -i &lt;id&gt;</code></p>
            <p><b>SSH</b><br><code>ssh &lt;id&gt;@${(this.cfg.httpHost || 'server:8080').split(':')[0]} -p ${this.cfg.sshPort || '2222'}</code></p>
          </div>
        </section>
        <section class="grid">
          <div class="card"><div class="metric">${this.devices.length}</div><div class="muted">online devices</div></div>
          <div class="card"><div class="metric">${sessions}</div><div class="muted">active sessions</div></div>
          <div class="card"><div class="metric">${forwards}</div><div class="muted">tcp forwards</div></div>
        </section>
        <section class="card" style="margin-top:14px">
          <div class="row"><h2>${icons.device} Devices</h2><button @click=${this.refresh}>Refresh</button></div>
          <div class="devices">
            ${this.devices.length ? this.devices.map(d => html`
              <article class="device">
                <div class="row"><b>${d.id}</b><span class="dot"></span></div>
                <div class="muted">${new Date(d.connectedAt).toLocaleString()}</div>
                <div class="row"><span>${d.hasPassword ? 'Password protected' : 'Open mode'}</span><span>${d.sessions || 0} sessions</span></div>
                <div class="actions"><a class="pill" href=${`/terminal.html?device=${encodeURIComponent(d.id)}`}>Terminal</a><a class="pill" href="/batch.html">Batch</a></div>
              </article>`) : html`<p>No devices connected.</p>`}
          </div>
        </section>
      </main>`;
  }
}
