import { LitElement, html } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { appStyles } from './styles';
import { Device, authFetch, authUrl } from './api';

@customElement('rdev-dashboard')
export class RdevDashboard extends LitElement {
  static styles = appStyles;
  @state() devices: Device[] = [];
  @state() cfg = { sshPort: '2222', httpHost: location.host, authRequired: 'false' };

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

  get host() { return location.host; }
  get ws() { return location.protocol === 'https:' ? 'wss://' : 'ws://'; }
  get http() { return location.protocol === 'https:' ? 'https://' : 'http://'; }
  get sshHost() { return (this.cfg.httpHost || this.host).split(':')[0]; }

  copy(text: string) { navigator.clipboard?.writeText(text); }

  renderCommand(label: string, text: string) {
    return html`<div class="cmd-group"><div class="cmd-label">${label}</div><div class="cmd-row"><div class="cmd-code">${text}</div><button @click=${() => this.copy(text)}>复制</button></div></div>`;
  }

  render() {
    const sessions = this.devices.reduce((n, d) => n + (d.sessions || 0), 0);
    const forwards = this.devices.reduce((n, d) => n + (d.forwards || 0), 0);
    return html`
      <main class="shell">
        <header class="topbar">
          <div><div class="brand"><span class="accent">RDev</span> Remote Debug</div><div class="subtitle">SSH 远程调试已连接设备 · Shell / SCP / SFTP / 端口转发 / 免密</div></div>
          <div class="nav"><a href="https://github.com/icepie/rdev" target="_blank">⭐ GitHub</a><a href=${authUrl('/terminal.html')}>⚡ Terminal</a><a href=${authUrl('/batch.html')}>📦 Batch</a></div>
        </header>

        <section class="stats">
          <div class="stat-card"><div class="label">在线设备</div><div class="value online">${this.devices.length}</div></div>
          <div class="stat-card"><div class="label">活跃会话</div><div class="value accent">${sessions}</div></div>
          <div class="stat-card"><div class="label">端口转发</div><div class="value yellow">${forwards}</div></div>
          <div class="stat-card"><div class="label">SSH 端口</div><div class="value">${this.cfg.sshPort || '—'}</div></div>
        </section>

        <table>
          <thead><tr><th>状态</th><th>设备 ID</th><th>连接时间</th><th>会话</th><th>转发</th><th>认证</th><th></th></tr></thead>
          <tbody>
            ${this.devices.length ? this.devices.map(d => html`<tr>
              <td><span class="dot"></span>在线</td>
              <td><b>${d.id}</b></td>
              <td class="muted">${new Date(d.connectedAt).toLocaleString()}</td>
              <td><span class="badge accent">${d.sessions || 0}</span></td>
              <td><span class="badge yellow">${d.forwards || 0}</span></td>
              <td>${d.hasPassword ? html`<span class="badge yellow">密码</span>` : html`<span class="badge green">开放</span>`}</td>
              <td><a class="term-btn" href=${authUrl(`/terminal.html?device=${encodeURIComponent(d.id)}`)}>⚡ Terminal</a></td>
            </tr>`) : html`<tr><td colspan="7" class="empty"><div style="font-size:3rem;opacity:.3">📡</div>等待设备连接...</td></tr>`}
          </tbody>
        </table>

        <section class="install">
          <h3>🚀 一键启动客户端</h3>
          ${this.renderCommand('📦 Linux / macOS curl 一键启动（替换密码）', `curl -sL ${this.http}${this.host}/install.sh | sh -s -- ${this.ws}${this.host} -p <自定义密码>`)}
          ${this.renderCommand('wget 版', `wget -qO- ${this.http}${this.host}/install.sh | sh -s -- ${this.ws}${this.host} -p <自定义密码>`)}
          ${this.renderCommand('🔓 开放模式（无密码，仅限内网）', `curl -sL ${this.http}${this.host}/install.sh | sh -s -- ${this.ws}${this.host}`)}
          ${this.renderCommand('Windows PowerShell', `powershell -Command "iwr -useb ${this.http}${this.host}/install.ps1 | iex; RDev ${this.ws}${this.host} -Password <自定义密码>"`)}
          ${this.renderCommand('Win7/8 PowerShell', `$wc=New-Object Net.WebClient; $wc.DownloadString('${this.http}${this.host}/install.ps1') | iex; RDev ${this.ws}${this.host} -Password <自定义密码>`)}
        </section>

        <section class="hint">
          <h3>⚡ SSH 连接方式</h3>
          <code>ssh &lt;deviceID&gt;@${this.sshHost} -p ${this.cfg.sshPort}</code>
          <code>scp file &lt;deviceID&gt;@${this.sshHost}:/tmp/ -P ${this.cfg.sshPort}</code>
          <code>sftp -P ${this.cfg.sshPort} &lt;deviceID&gt;@${this.sshHost}</code>
          <h3 style="margin-top:.8rem">🔀 端口转发</h3>
          <code>ssh -L 8080:localhost:80 &lt;deviceID&gt;@${this.sshHost} -p ${this.cfg.sshPort}</code>
          <code>ssh -R 3000:localhost:3000 &lt;deviceID&gt;@${this.sshHost} -p ${this.cfg.sshPort}</code>
        </section>
      </main>`;
  }
}
