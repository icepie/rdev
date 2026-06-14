import { css } from 'lit';

export const appStyles = css`
  :host { display:block; min-height:100vh; background:#0f1117; color:#e4e4e7; font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif; }
  * { box-sizing:border-box; }
  .shell { max-width:1040px; margin:0 auto; padding:2rem; }
  .topbar { margin-bottom:2rem; display:flex; justify-content:space-between; align-items:flex-start; flex-wrap:wrap; gap:1rem; }
  .brand { font-size:1.8rem; font-weight:700; letter-spacing:-.02em; display:block; }
  .brand .accent, .accent { color:#22d3ee; }
  .subtitle { color:#71717a; margin-top:.5rem; font-size:.9rem; }
  .nav { display:flex; gap:.5rem; flex-wrap:wrap; align-items:center; }
  .nav a, .btn, button, .pill { display:inline-flex; align-items:center; gap:6px; padding:8px 16px; border-radius:8px; font-size:.85rem; font-weight:500; cursor:pointer; border:1px solid #2a2d3a; background:#1a1d27; color:#e4e4e7; text-decoration:none; transition:all .15s; }
  .nav a:hover, .btn:hover, button:hover, .pill:hover, .nav a.active, button.primary { border-color:#22d3ee; color:#22d3ee; }
  button.primary { background:rgba(34,211,238,.08); }
  button:disabled { opacity:.35; cursor:not-allowed; }
  .stats { display:grid; grid-template-columns:repeat(4,1fr); gap:1rem; margin-bottom:2rem; }
  .card, .stat-card { background:#1a1d27; border:1px solid #2a2d3a; border-radius:12px; padding:1.2rem; }
  .stat-card .label { font-size:.75rem; color:#71717a; text-transform:uppercase; letter-spacing:.05em; }
  .stat-card .value { font-size:1.8rem; font-weight:700; margin-top:.3rem; }
  .online { color:#4ade80; } .yellow { color:#facc15; } .muted { color:#71717a; }
  table { width:100%; border-collapse:collapse; background:#1a1d27; border:1px solid #2a2d3a; border-radius:12px; overflow:hidden; }
  thead th { text-align:left; padding:.8rem 1rem; font-size:.75rem; text-transform:uppercase; letter-spacing:.05em; color:#71717a; border-bottom:1px solid #2a2d3a; }
  tbody td { padding:.8rem 1rem; border-bottom:1px solid #2a2d3a; font-size:.9rem; }
  tbody tr:last-child td { border-bottom:none; }
  tbody tr:hover { background:rgba(255,255,255,.02); }
  .dot { display:inline-block; width:8px; height:8px; border-radius:50%; margin-right:6px; background:#4ade80; box-shadow:0 0 6px #4ade80; }
  .badge { display:inline-block; padding:2px 8px; border-radius:6px; font-size:.75rem; font-weight:500; margin-right:4px; }
  .badge.green { background:rgba(74,222,128,.15); color:#4ade80; }
  .badge.accent { background:rgba(34,211,238,.15); color:#22d3ee; }
  .badge.yellow { background:rgba(250,204,21,.15); color:#facc15; }
  .empty { text-align:center; padding:3rem; color:#71717a; }
  .term-btn { display:inline-flex; align-items:center; gap:4px; padding:4px 10px; border-radius:6px; font-size:.8rem; cursor:pointer; border:1px solid rgba(34,211,238,.3); background:rgba(34,211,238,.08); color:#22d3ee; text-decoration:none; transition:all .15s; }
  .term-btn:hover { background:rgba(34,211,238,.18); border-color:#22d3ee; }
  h2, h3 { margin:0 0 1rem; font-size:1rem; color:#22d3ee; }
  p { color:#71717a; line-height:1.65; }
  .install, .hint { background:#1a1d27; border:1px solid #2a2d3a; border-radius:12px; padding:1.5rem; margin-top:2rem; }
  .cmd-group { margin-bottom:1rem; }
  .cmd-label { font-size:.8rem; color:#71717a; margin-bottom:.4rem; }
  .cmd-row { display:flex; align-items:center; gap:8px; }
  .cmd-code, code, pre { background:#000; padding:.6rem 1rem; border-radius:6px; font-family:'SF Mono','Fira Code','Cascadia Code',Consolas,monospace; font-size:.82rem; color:#e4e4e7; overflow:auto; white-space:pre-wrap; line-height:1.5; }
  .cmd-code { flex:1; user-select:all; white-space:pre; }
  input, textarea { width:100%; border:1px solid #2a2d3a; background:#0f1117; color:#e4e4e7; border-radius:8px; padding:10px 14px; outline:none; font:inherit; }
  textarea { min-height:120px; font-family:'SF Mono','Fira Code',monospace; resize:vertical; }
  .grid { display:grid; grid-template-columns:repeat(2,1fr); gap:1rem; }
  .split { display:grid; grid-template-columns:280px 1fr; gap:1rem; }
  .devices, .list, .results { display:grid; gap:8px; }
  .device, .selectable { border:1px solid #2a2d3a; background:#1a1d27; border-radius:10px; padding:12px; }
  .selectable { cursor:pointer; }
  .selectable.active { border-color:#22d3ee; background:rgba(34,211,238,.06); }
  .row { display:flex; align-items:center; justify-content:space-between; gap:12px; }
  .terminal-wrap { overflow:hidden; height:calc(100vh - 170px); min-height:420px; border:1px solid #2a2d3a; background:#000; border-radius:12px; }
  #terminal { height:100%; padding:10px; }
  .drop { display:block; border:2px dashed #2a2d3a; border-radius:10px; padding:2rem; text-align:center; cursor:pointer; color:#71717a; }
  .drop:hover { border-color:#22d3ee; color:#22d3ee; background:rgba(34,211,238,.04); }
  @media (max-width:800px) { .shell{padding:1rem}.stats,.grid,.split{grid-template-columns:1fr}.cmd-row{align-items:stretch;flex-direction:column}.terminal-wrap{height:65vh} table{display:block;overflow:auto} }
`;

export const icons = { zap:'⚡', terminal:'⚡', batch:'📦', shield:'🔐', device:'📡', upload:'📁', file:'📄' };
