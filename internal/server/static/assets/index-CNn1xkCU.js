import{d as e,f as t,m as n,n as r,p as i,r as a,t as o,u as s}from"./decorate-BSIJnHrU.js";var c=class extends i{constructor(...e){super(...e),this.devices=[],this.cfg={sshPort:`2222`,httpHost:location.host,authRequired:`false`}}static{this.styles=s}connectedCallback(){super.connectedCallback(),this.refresh(),window.setInterval(()=>this.refresh(),4e3)}async refresh(){try{this.cfg=await(await fetch(`/api/config`)).json(),this.devices=await(await r(`/api/clients`)).json()}catch{}}get host(){return location.host}get ws(){return location.protocol===`https:`?`wss://`:`ws://`}get http(){return location.protocol===`https:`?`https://`:`http://`}get sshHost(){return(this.cfg.httpHost||this.host).split(`:`)[0]}copy(e){navigator.clipboard?.writeText(e)}renderCommand(e,t){return n`<div class="cmd-group"><div class="cmd-label">${e}</div><div class="cmd-row"><div class="cmd-code">${t}</div><button @click=${()=>this.copy(t)}>复制</button></div></div>`}render(){let e=this.devices.reduce((e,t)=>e+(t.sessions||0),0),t=this.devices.reduce((e,t)=>e+(t.forwards||0),0);return n`
      <main class="shell">
        <header class="topbar">
          <div><div class="brand"><span class="accent">RDev</span> Remote Debug</div><div class="subtitle">SSH 远程调试已连接设备 · Shell / SCP / SFTP / 端口转发 / 免密</div></div>
          <div class="nav"><a href="https://github.com/icepie/rdev" target="_blank">⭐ GitHub</a><a href=${a(`/terminal.html`)}>⚡ Terminal</a><a href=${a(`/batch.html`)}>📦 Batch</a></div>
        </header>

        <section class="stats">
          <div class="stat-card"><div class="label">在线设备</div><div class="value online">${this.devices.length}</div></div>
          <div class="stat-card"><div class="label">活跃会话</div><div class="value accent">${e}</div></div>
          <div class="stat-card"><div class="label">端口转发</div><div class="value yellow">${t}</div></div>
          <div class="stat-card"><div class="label">SSH 端口</div><div class="value">${this.cfg.sshPort||`—`}</div></div>
        </section>

        <table>
          <thead><tr><th>状态</th><th>设备 ID</th><th>连接时间</th><th>会话</th><th>转发</th><th>认证</th><th></th></tr></thead>
          <tbody>
            ${this.devices.length?this.devices.map(e=>n`<tr>
              <td><span class="dot"></span>在线</td>
              <td><b>${e.id}</b></td>
              <td class="muted">${new Date(e.connectedAt).toLocaleString()}</td>
              <td><span class="badge accent">${e.sessions||0}</span></td>
              <td><span class="badge yellow">${e.forwards||0}</span></td>
              <td>${e.hasPassword?n`<span class="badge yellow">密码</span>`:n`<span class="badge green">开放</span>`}</td>
              <td><a class="term-btn" href=${a(`/terminal.html?device=${encodeURIComponent(e.id)}`)}>⚡ Terminal</a></td>
            </tr>`):n`<tr><td colspan="7" class="empty"><div style="font-size:3rem;opacity:.3">📡</div>等待设备连接...</td></tr>`}
          </tbody>
        </table>

        <section class="install">
          <h3>🚀 一键启动客户端</h3>
          ${this.renderCommand(`📦 Linux / macOS curl 一键启动（替换密码）`,`curl -sL ${this.http}${this.host}/install.sh | sh -s -- ${this.ws}${this.host} -p <自定义密码>`)}
          ${this.renderCommand(`wget 版`,`wget -qO- ${this.http}${this.host}/install.sh | sh -s -- ${this.ws}${this.host} -p <自定义密码>`)}
          ${this.renderCommand(`🔓 开放模式（无密码，仅限内网）`,`curl -sL ${this.http}${this.host}/install.sh | sh -s -- ${this.ws}${this.host}`)}
          ${this.renderCommand(`Windows PowerShell`,`powershell -Command "iwr -useb ${this.http}${this.host}/install.ps1 | iex; RDev ${this.ws}${this.host} -Password <自定义密码>"`)}
          ${this.renderCommand(`Win7/8 PowerShell`,`$wc=New-Object Net.WebClient; $wc.DownloadString('${this.http}${this.host}/install.ps1') | iex; RDev ${this.ws}${this.host} -Password <自定义密码>`)}
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
      </main>`}};o([e()],c.prototype,`devices`,void 0),o([e()],c.prototype,`cfg`,void 0),c=o([t(`rdev-dashboard`)],c);