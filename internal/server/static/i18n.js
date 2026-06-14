(function () {
  const dictionaries = {
    en: {
      'theme.toggle': 'Theme',
      'theme.auto': 'Auto',
      'theme.light': 'Light',
      'theme.dark': 'Dark',
      'nav.dashboard': 'Dashboard',
      'nav.terminal': 'Terminal',
      'nav.batch': 'Batch',
      'nav.sessions': 'Sessions',
      'lang.label': 'Language',
      'common.copy': 'Copy',
      'common.copied': 'Copied',
      'common.connect': 'Connect',
      'common.connecting': 'Verifying...',
      'common.passwordWrong': 'Wrong password',
      'common.adminTokenRequired': 'Admin token required',
      'common.noDevices': 'No devices connected',
      'common.online': 'Online',
      'common.password': 'Password',
      'common.open': 'Open',
      'common.justNow': 'just now',
      'common.minutesAgo': '{n} min ago',
      'common.hoursAgo': '{n} h ago',
      'common.daysAgo': '{n} d ago',
      'index.subtitle': 'SSH remote debugging for connected devices · Shell / SCP / SFTP / Port Forwarding / Key auth',
      'index.onlineDevices': 'Online devices',
      'index.activeSessions': 'Active sessions',
      'index.portForwards': 'Port forwards',
      'index.sshPort': 'SSH port',
      'index.status': 'Status',
      'index.deviceId': 'Device ID',
      'index.connectedAt': 'Connected at',
      'index.sessions': 'Sessions',
      'index.forwards': 'Forwards',
      'index.auth': 'Auth',
      'index.waitingDevices': 'Waiting for devices...',
      'index.installTitle': 'One-click client start',
      'index.curlLabel': 'Start with curl (replace {password} with your password)',
      'index.wgetLabel': 'wget version',
      'index.openMode': 'Open mode (no password, LAN only)',
      'index.psLabel': 'Start with PowerShell (replace {password} with your password)',
      'index.win7Step1': '1️⃣ Start PowerShell from cmd',
      'index.win7Step2': '2️⃣ Paste this command (replace {password})',
      'index.sshTitle': 'SSH connection',
      'index.forwardTitle': 'Port forwarding',
      'index.localForwardComment': '# -L access services on the device',
      'index.remoteForwardComment': '# -R expose local services through the server',
      'index.keyAuthTitle': 'Key authentication',
      'index.keyAuthComment': '# Add your public key to server ~/.rdev/authorized_keys',
      'index.noPasswordComment': '# no password needed!',
      'terminal.devicePlaceholder': '-- device --',
      'terminal.newTab': '+ New Tab',
      'terminal.authTitle': 'Device authentication',
      'terminal.authDesc': '{id} requires password verification',
      'terminal.passwordPlaceholder': 'Enter password',
      'sessions.title': 'Session Manager',
      'sessions.active': 'Active sessions',
      'sessions.searchPlaceholder': 'Search device ID / session ID / command...',
      'sessions.detach': '✕ Detach',
      'sessions.emptyHint': 'Select a session on the left to attach',
      'sessions.noActive': 'No active sessions',
      'sessions.authTitle': 'Session authentication',
      'sessions.authDesc': '{id} requires device password',
      'sessions.disconnected': '[Disconnected]',
      'batch.devices': 'Devices',
      'batch.selectAll': 'Select all',
      'batch.commandTab': 'Batch Command',
      'batch.uploadTab': 'File Distribution',
      'batch.dropHint': 'Click or drag file here',
      'batch.distribute': 'Distribute',
      'batch.commandPlaceholder': 'Command to run on selected devices...',
      'batch.run': 'Run',
      'batch.noSelection': 'Select at least one device',
      'batch.noCommand': 'Enter a command',
      'batch.passwordRequired': 'Password required for {ids}',
      'batch.wrongPassword': 'Wrong password for {id}',
      'batch.uploading': 'uploading...'
    },
    zh: {
      'theme.toggle': '主题',
      'theme.auto': '自动',
      'theme.light': '浅色',
      'theme.dark': '深色',
      'nav.dashboard': '仪表盘',
      'nav.terminal': '终端',
      'nav.batch': '批量',
      'nav.sessions': '会话',
      'lang.label': '语言',
      'common.copy': '复制',
      'common.copied': '已复制',
      'common.connect': '连接',
      'common.connecting': '验证中...',
      'common.passwordWrong': '密码错误',
      'common.adminTokenRequired': '需要管理员 token',
      'common.noDevices': '暂无设备连接',
      'common.online': '在线',
      'common.password': '密码',
      'common.open': '开放',
      'common.justNow': '刚刚',
      'common.minutesAgo': '{n} 分钟前',
      'common.hoursAgo': '{n} 小时前',
      'common.daysAgo': '{n} 天前',
      'index.subtitle': 'SSH 远程调试已连接设备 · Shell / SCP / SFTP / 端口转发 / 免密',
      'index.onlineDevices': '在线设备',
      'index.activeSessions': '活跃会话',
      'index.portForwards': '端口转发',
      'index.sshPort': 'SSH 端口',
      'index.status': '状态',
      'index.deviceId': '设备 ID',
      'index.connectedAt': '连接时间',
      'index.sessions': '会话',
      'index.forwards': '转发',
      'index.auth': '认证',
      'index.waitingDevices': '等待设备连接...',
      'index.installTitle': '一键启动客户端',
      'index.curlLabel': 'curl 一键启动（替换 {password} 为你的密码）',
      'index.wgetLabel': 'wget 版',
      'index.openMode': '开放模式（无密码，仅限内网）',
      'index.psLabel': 'PowerShell 一键启动（替换 {password} 为你的密码）',
      'index.win7Step1': '1️⃣ 在 cmd 中启动 PowerShell',
      'index.win7Step2': '2️⃣ 粘贴以下命令（替换 {password}）',
      'index.sshTitle': 'SSH 连接方式',
      'index.forwardTitle': '端口转发',
      'index.localForwardComment': '# -L 访问设备上的服务',
      'index.remoteForwardComment': '# -R 暴露本地服务到服务器',
      'index.keyAuthTitle': '免密登录',
      'index.keyAuthComment': '# 将公钥加入服务端 ~/.rdev/authorized_keys',
      'index.noPasswordComment': '# 无需密码!',
      'terminal.devicePlaceholder': '-- device --',
      'terminal.newTab': '+ 新建标签',
      'terminal.authTitle': '设备认证',
      'terminal.authDesc': '{id} 需要密码验证',
      'terminal.passwordPlaceholder': '输入密码',
      'sessions.title': '会话管理',
      'sessions.active': '活跃会话',
      'sessions.searchPlaceholder': '搜索设备ID / 会话ID / 命令...',
      'sessions.detach': '✕ 断开',
      'sessions.emptyHint': '选择左侧会话以附加',
      'sessions.noActive': '无活跃会话',
      'sessions.authTitle': '会话认证',
      'sessions.authDesc': '{id} 需要设备密码',
      'sessions.disconnected': '[已断开]',
      'batch.devices': '设备',
      'batch.selectAll': '全选',
      'batch.commandTab': '批量命令',
      'batch.uploadTab': '文件分发',
      'batch.dropHint': '点击或拖拽文件到这里',
      'batch.distribute': '分发',
      'batch.commandPlaceholder': '在所选设备上运行的命令...',
      'batch.run': '执行',
      'batch.noSelection': '请至少选择一个设备',
      'batch.noCommand': '请输入命令',
      'batch.passwordRequired': '{ids} 需要密码',
      'batch.wrongPassword': '{id} 密码错误',
      'batch.uploading': '上传中...'
    }
  };

  const supported = Object.keys(dictionaries);
  const browserLang = (navigator.language || 'en').toLowerCase().startsWith('zh') ? 'zh' : 'en';
  let currentLang = localStorage.getItem('rdevLang') || browserLang;
  if (!supported.includes(currentLang)) currentLang = 'en';

  function format(template, vars) {
    return String(template).replace(/\{(\w+)\}/g, (_, key) => vars && vars[key] !== undefined ? vars[key] : '');
  }

  function t(key, vars) {
    const dict = dictionaries[currentLang] || dictionaries.en;
    return format(dict[key] || dictionaries.en[key] || key, vars);
  }

  function setLang(lang) {
    if (!supported.includes(lang)) return;
    currentLang = lang;
    localStorage.setItem('rdevLang', lang);
    applyI18n();
    window.dispatchEvent(new CustomEvent('rdev:langchange', { detail: { lang } }));
  }

  function applyI18n(root) {
    const scope = root || document;
    document.documentElement.lang = currentLang === 'zh' ? 'zh-CN' : 'en';
    scope.querySelectorAll('[data-i18n]').forEach(el => { el.textContent = t(el.dataset.i18n); });
    scope.querySelectorAll('[data-i18n-title]').forEach(el => { el.title = t(el.dataset.i18nTitle); });
    scope.querySelectorAll('[data-i18n-placeholder]').forEach(el => { el.placeholder = t(el.dataset.i18nPlaceholder); });
    scope.querySelectorAll('[data-i18n-html]').forEach(el => { el.innerHTML = t(el.dataset.i18nHtml); });
    scope.querySelectorAll('[data-lang-select]').forEach(el => { el.value = currentLang; });
  }

  function langSelector() {
    return '<label class="lang-switch"><span data-i18n="lang.label">Language</span><select data-lang-select aria-label="Language"><option value="en">EN</option><option value="zh">中文</option></select></label>';
  }

  document.addEventListener('change', event => {
    const select = event.target.closest && event.target.closest('[data-lang-select]');
    if (select) setLang(select.value);
  });
  document.addEventListener('DOMContentLoaded', () => applyI18n());

  window.RDevI18n = { t, setLang, apply: applyI18n, langSelector, get lang() { return currentLang; } };
  window.t = t;
})();
