import{d as e,f as t,h as n,m as r,n as i,p as a,t as o,u as s}from"./decorate-DNlucyPE.js";var c=class extends r{constructor(...e){super(...e),this.devices=[],this.cfg={sshPort:``,httpHost:``,authRequired:`false`}}static{this.styles=s}connectedCallback(){super.connectedCallback(),this.refresh(),window.setInterval(()=>this.refresh(),4e3)}async refresh(){try{this.cfg=await(await fetch(`/api/config`)).json(),this.devices=await(await i(`/api/clients`)).json()}catch{}}render(){let t=this.devices.reduce((e,t)=>e+(t.sessions||0),0),r=this.devices.reduce((e,t)=>e+(t.forwards||0),0);return n`
      <main class="shell">
        <header class="topbar">
          <div class="brand"><span class="logo">${e.zap}</span><span>RDev</span></div>
          <nav class="nav"><a class="active" href="/">Dashboard</a><a href="/terminal.html">Terminal</a><a href="/batch.html">Batch</a></nav>
        </header>
        <section class="hero">
          <div>
            <span class="pill">${this.cfg.authRequired===`true`?`Web auth enabled`:`Open web mode`}</span>
            <h1>Remote devices, one control plane.</h1>
            <p>High-throughput SSH, terminal, TCP forwarding and binary batch distribution for connected machines.</p>
          </div>
          <div class="card">
            <h2>${e.shield} Connect</h2>
            <p><b>Client</b><br><code>rdev-client -s ws://${this.cfg.httpHost||`server:8080`} -i &lt;id&gt;</code></p>
            <p><b>SSH</b><br><code>ssh &lt;id&gt;@${(this.cfg.httpHost||`server:8080`).split(`:`)[0]} -p ${this.cfg.sshPort||`2222`}</code></p>
          </div>
        </section>
        <section class="grid">
          <div class="card"><div class="metric">${this.devices.length}</div><div class="muted">online devices</div></div>
          <div class="card"><div class="metric">${t}</div><div class="muted">active sessions</div></div>
          <div class="card"><div class="metric">${r}</div><div class="muted">tcp forwards</div></div>
        </section>
        <section class="card" style="margin-top:14px">
          <div class="row"><h2>${e.device} Devices</h2><button @click=${this.refresh}>Refresh</button></div>
          <div class="devices">
            ${this.devices.length?this.devices.map(e=>n`
              <article class="device">
                <div class="row"><b>${e.id}</b><span class="dot"></span></div>
                <div class="muted">${new Date(e.connectedAt).toLocaleString()}</div>
                <div class="row"><span>${e.hasPassword?`Password protected`:`Open mode`}</span><span>${e.sessions||0} sessions</span></div>
                <div class="actions"><a class="pill" href=${`/terminal.html?device=${encodeURIComponent(e.id)}`}>Terminal</a><a class="pill" href="/batch.html">Batch</a></div>
              </article>`):n`<p>No devices connected.</p>`}
          </div>
        </section>
      </main>`}};o([t()],c.prototype,`devices`,void 0),o([t()],c.prototype,`cfg`,void 0),c=o([a(`rdev-dashboard`)],c);