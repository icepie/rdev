import { css } from 'lit';

export const appStyles = css`
  :host { display:block; min-height:100vh; color:#e5eefb; background:radial-gradient(circle at top left,#1e3a8a55,transparent 32rem),linear-gradient(135deg,#07111f,#0d1324 55%,#111827); font-family:Inter,ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif; }
  * { box-sizing:border-box; }
  .shell { width:min(1180px,100%); margin:0 auto; padding:24px; }
  .topbar { display:flex; justify-content:space-between; align-items:center; gap:18px; margin-bottom:22px; }
  .brand { display:flex; align-items:center; gap:12px; font-weight:800; letter-spacing:-.03em; font-size:24px; }
  .logo { width:44px; height:44px; display:grid; place-items:center; border-radius:15px; background:linear-gradient(135deg,#22d3ee,#8b5cf6); box-shadow:0 18px 50px #22d3ee30; }
  .nav { display:flex; flex-wrap:wrap; gap:8px; }
  .nav a, button, .pill { border:1px solid #ffffff18; color:#dbeafe; background:#ffffff0d; backdrop-filter:blur(14px); border-radius:999px; padding:9px 14px; text-decoration:none; font-weight:650; font-size:14px; }
  .nav a.active, button.primary { background:linear-gradient(135deg,#22d3ee,#7c3aed); border-color:transparent; color:white; }
  button { cursor:pointer; transition:.18s transform,.18s opacity; }
  button:hover { transform:translateY(-1px); }
  button:disabled { opacity:.45; cursor:not-allowed; transform:none; }
  .hero, .card { border:1px solid #ffffff14; background:#0f172acc; box-shadow:0 24px 70px #0008; border-radius:28px; }
  .hero { padding:28px; display:grid; grid-template-columns:1.35fr .65fr; gap:18px; margin-bottom:18px; }
  h1 { margin:0; font-size:clamp(32px,6vw,64px); line-height:.95; letter-spacing:-.07em; }
  h2 { margin:0 0 14px; font-size:18px; }
  p { color:#94a3b8; line-height:1.7; margin:10px 0 0; }
  .grid { display:grid; grid-template-columns:repeat(3,minmax(0,1fr)); gap:14px; }
  .card { padding:18px; }
  .metric { font-size:34px; font-weight:850; letter-spacing:-.05em; }
  .muted { color:#94a3b8; }
  .devices { display:grid; grid-template-columns:repeat(auto-fill,minmax(240px,1fr)); gap:12px; }
  .device { border:1px solid #ffffff12; background:#ffffff08; border-radius:20px; padding:16px; display:grid; gap:10px; }
  .row { display:flex; align-items:center; justify-content:space-between; gap:12px; }
  .dot { width:10px; height:10px; border-radius:50%; background:#22c55e; box-shadow:0 0 18px #22c55e; }
  .actions { display:flex; flex-wrap:wrap; gap:8px; }
  input, textarea { width:100%; border:1px solid #ffffff18; background:#020617aa; color:#e5eefb; border-radius:16px; padding:12px 14px; outline:none; font:inherit; }
  textarea { min-height:120px; font-family:ui-monospace,SFMono-Regular,Menlo,monospace; resize:vertical; }
  .split { display:grid; grid-template-columns:320px 1fr; gap:14px; }
  .list { display:grid; gap:8px; }
  .selectable { cursor:pointer; border:1px solid #ffffff12; background:#ffffff08; border-radius:16px; padding:12px; }
  .selectable.active { border-color:#22d3ee; background:#22d3ee18; }
  .terminal-wrap { overflow:hidden; height:calc(100vh - 170px); min-height:420px; border:1px solid #ffffff14; background:#020617; border-radius:24px; }
  #terminal { height:100%; padding:10px; }
  .drop { border:1.5px dashed #38bdf855; background:#0ea5e91a; border-radius:22px; padding:28px; text-align:center; cursor:pointer; }
  .results { display:grid; gap:10px; margin-top:14px; }
  pre { margin:0; white-space:pre-wrap; color:#cbd5e1; font:13px/1.55 ui-monospace,SFMono-Regular,Menlo,monospace; max-height:260px; overflow:auto; }
  @media (max-width:800px) { .shell{padding:16px}.topbar,.hero{grid-template-columns:1fr;align-items:flex-start}.hero{padding:22px}.grid,.split{grid-template-columns:1fr}.nav a{padding:8px 11px}.terminal-wrap{height:65vh} }
`;

export const icons = {
  zap: '⚡', terminal: '⌘', batch: '⇄', shield: '◆', device: '◉', upload: '⬆', file: '▣'
};
