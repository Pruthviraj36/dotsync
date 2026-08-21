package cmd

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>DotSync</title>
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=IBM+Plex+Mono:wght@400;500;600;700&family=Outfit:wght@400;500;600;700&display=swap" rel="stylesheet">
<style>
:root {
  --bg:      #0a0a0f;
  --s1:      #12121a;
  --s2:      #1a1a24;
  --s3:      #242432;
  --border:  rgba(255,255,255,0.06);
  --border-hover: rgba(255,255,255,0.12);
  --accent:  #22d3ee;
  --accent-dim: rgba(34,211,238,.10);
  --accent-glow: rgba(34,211,238,.25);
  --purple:  #c084fc;
  --purple-dim: rgba(192,132,252,.10);
  --green:   #4ade80;
  --green-dim: rgba(74,222,128,.10);
  --yellow:  #fbbf24;
  --yellow-dim: rgba(251,191,36,.10);
  --red:     #f87171;
  --red-dim: rgba(248,113,113,.10);
  --text:    #f1f5f9;
  --text-dim: #94a3b8;
  --muted:   #64748b;
  --mono:    'IBM Plex Mono', ui-monospace, monospace;
  --sans:    'Outfit', system-ui, sans-serif;
  --radius:  12px;
  --radius-sm: 8px;
  --topbar-h: 56px;
  --shadow-sm: 0 1px 2px rgba(0,0,0,.3);
  --shadow-md: 0 4px 12px rgba(0,0,0,.4);
  --shadow-lg: 0 8px 24px rgba(0,0,0,.5);
}
*{box-sizing:border-box;margin:0;padding:0}
html,body{height:100%;background:var(--bg);color:var(--text);font-family:var(--sans);font-size:14px;line-height:1.6;-webkit-font-smoothing:antialiased}
body{
  background:
    radial-gradient(ellipse 1200px 500px at 10% -5%, var(--accent-glow), transparent 60%),
    radial-gradient(ellipse 900px 400px at 100% 5%, var(--purple-dim), transparent 55%),
    var(--bg);
}

::-webkit-scrollbar{width:7px;height:7px}
::-webkit-scrollbar-track{background:transparent}
::-webkit-scrollbar-thumb{background:rgba(255,255,255,.12);border-radius:4px}
::-webkit-scrollbar-thumb:hover{background:rgba(255,255,255,.18)}

/* ── topbar ── */
.topbar{
  display:flex;align-items:center;gap:12px;
  height:var(--topbar-h);border-bottom:1px solid var(--border);
  background:rgba(18,18,26,.92);backdrop-filter:blur(16px);
  position:fixed;top:0;left:0;right:0;z-index:100;padding:0 18px 0 14px;
}
.menu-btn{
  display:none;align-items:center;justify-content:center;
  width:36px;height:36px;border-radius:var(--radius-sm);border:1px solid var(--border);
  background:var(--s2);color:var(--text);cursor:pointer;font-size:16px;
  transition:all .2s ease;
}
.menu-btn:hover{background:var(--s3);border-color:var(--border-hover)}
.logo{font-family:var(--mono);font-weight:700;font-size:16px;color:var(--accent);letter-spacing:-.5px;margin-right:10px;white-space:nowrap}
.logo em{color:var(--purple);font-style:normal}
.topbar-sep{width:1px;height:24px;background:var(--border);margin:0 8px;flex-shrink:0}
.project-select{
  background:var(--s2);border:1px solid var(--border);color:var(--text);
  font-family:var(--mono);font-size:13px;cursor:pointer;outline:none;
  padding:6px 12px;border-radius:var(--radius-sm);max-width:220px;
  transition:border-color .2s ease;
}
.project-select:focus{border-color:var(--accent)}
.project-select option{background:var(--s2)}
.env-tabs{display:flex;gap:6px;margin-left:6px;overflow-x:auto;max-width:42vw;scrollbar-width:none}
.env-tabs::-webkit-scrollbar{display:none}
.env-tab{
  padding:5px 13px;border-radius:999px;cursor:pointer;
  font-family:var(--mono);font-size:11px;color:var(--muted);
  transition:all .2s ease;border:1px solid transparent;white-space:nowrap;
}
.env-tab:hover{color:var(--text);background:var(--s2);border-color:var(--border-hover)}
.env-tab.active{color:var(--accent);background:var(--accent-dim);border-color:var(--accent-glow)}
.topbar-right{margin-left:auto;display:flex;align-items:center;gap:12px}
.live-pill{
  display:inline-flex;align-items:center;gap:6px;
  font-family:var(--mono);font-size:10px;color:var(--muted);
  padding:5px 10px;border-radius:999px;border:1px solid var(--border);background:var(--s2);
  transition:all .2s ease;
}
.live-dot{width:7px;height:7px;border-radius:50%;background:var(--muted);flex-shrink:0;transition:all .3s ease}
.live-pill.on .live-dot{background:var(--green);box-shadow:0 0 10px rgba(74,222,128,.4)}
.live-pill.on{color:var(--green);border-color:var(--green-dim)}
.user-chip{display:flex;align-items:center;gap:10px;font-family:var(--mono);font-size:13px;color:var(--text-dim)}
.avatar{
  width:30px;height:30px;border-radius:50%;background:linear-gradient(135deg,var(--purple),var(--accent));
  display:flex;align-items:center;justify-content:center;font-size:12px;font-weight:700;color:#fff;flex-shrink:0;
  overflow:hidden;object-fit:cover;border:2px solid var(--border);
}

/* ── layout ── */
.shell{display:flex;height:calc(100vh - var(--topbar-h));margin-top:var(--topbar-h);overflow:hidden}
.sidebar{
  width:210px;flex-shrink:0;border-right:1px solid var(--border);
  background:rgba(18,18,26,.8);padding:16px 12px;overflow-y:auto;
  display:flex;flex-direction:column;
}
.nav-section{margin-bottom:20px}
.nav-section-label{
  font-size:10px;font-weight:600;text-transform:uppercase;letter-spacing:1.2px;
  color:var(--muted);padding:0 12px 8px;
}
.nav-item{
  display:flex;align-items:center;gap:10px;
  padding:9px 12px;border-radius:var(--radius-sm);cursor:pointer;
  color:var(--muted);transition:all .2s ease;font-size:14px;user-select:none;font-weight:500;
}
.nav-item:hover{color:var(--text);background:var(--s2)}
.nav-item.active{color:var(--accent);background:var(--accent-dim);box-shadow:0 0 0 1px var(--accent-glow)}
.nav-icon{font-size:15px;width:20px;text-align:center;flex-shrink:0;opacity:.9}
.nav-badge{
  margin-left:auto;font-family:var(--mono);font-size:10px;
  background:var(--s3);color:var(--muted);padding:2px 7px;border-radius:999px;
}
.sidebar-foot{
  margin-top:auto;padding:14px 12px 6px;border-top:1px solid var(--border);
  font-family:var(--mono);font-size:11px;color:var(--muted);line-height:1.7;
}
.sidebar-foot .srv{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:180px}
.kbd-hint{opacity:.7;margin-top:8px;font-size:10px}

.main{flex:1;overflow-y:auto;padding:24px;display:flex;flex-direction:column;gap:16px}
.page{display:none;flex-direction:column;gap:16px;animation:fade .2s ease}
.page.active{display:flex}
@keyframes fade{from{opacity:0;transform:translateY(8px)}to{opacity:1;transform:none}}
@keyframes pulse{0%,100%{opacity:1}50%{opacity:.7}}
@keyframes shimmer{0%{background-position:-200% 0}100%{background-position:200% 0}}

.stats{display:grid;grid-template-columns:repeat(3,1fr);gap:12px}
.stat{
  background:linear-gradient(180deg, rgba(255,255,255,.04), transparent), var(--s1);
  border:1px solid var(--border);border-radius:var(--radius);padding:16px 18px;
  transition:border-color .2s ease;
}
.stat:hover{border-color:var(--border-hover)}
.stat-label{font-size:10px;text-transform:uppercase;letter-spacing:1px;color:var(--muted);margin-bottom:8px;font-weight:600}
.stat-value{font-family:var(--mono);font-size:24px;font-weight:700;line-height:1.1}
.stat-sub{font-family:var(--mono);font-size:11px;color:var(--muted);margin-top:6px}

.card{background:var(--s1);border:1px solid var(--border);border-radius:var(--radius);overflow:hidden;transition:border-color .2s ease}
.card:hover{border-color:var(--border-hover)}
.card-head{
  display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap;
  padding:14px 18px;border-bottom:1px solid var(--border);
}
.card-title{font-size:14px;font-weight:600;color:var(--text);display:flex;align-items:center;gap:10px}
.card-actions{display:flex;gap:8px;flex-wrap:wrap;align-items:center}
.card-body{padding:18px}

.btn{
  display:inline-flex;align-items:center;gap:6px;
  padding:7px 14px;border-radius:var(--radius-sm);font-size:12px;font-weight:500;
  cursor:pointer;border:none;transition:all .2s ease;
  font-family:var(--mono);white-space:nowrap;line-height:1.4;
}
.btn-primary{background:var(--accent);color:#041018;box-shadow:0 2px 8px rgba(34,211,238,.15)}
.btn-primary:hover{filter:brightness(1.1);transform:translateY(-1px)}
.btn-primary:active{transform:translateY(0)}
.btn-ghost{background:transparent;color:var(--text-dim);border:1px solid var(--border)}
.btn-ghost:hover{border-color:var(--accent);color:var(--accent);background:var(--accent-dim)}
.btn-green{background:var(--green);color:#041810;box-shadow:0 2px 8px rgba(74,222,128,.15)}
.btn-green:hover{filter:brightness(1.08);transform:translateY(-1px)}
.btn-red{background:transparent;color:var(--red);border:1px solid var(--red-dim)}
.btn-red:hover{background:var(--red-dim);border-color:var(--red)}
.btn:disabled{opacity:.4;cursor:not-allowed;transform:none}

.editor-toolbar{display:flex;gap:10px;align-items:center;margin-bottom:12px;flex-wrap:wrap}
.search-input{
  flex:1;min-width:150px;background:var(--s2);border:1px solid var(--border);
  color:var(--text);font-family:var(--mono);font-size:12px;
  padding:8px 12px;border-radius:var(--radius-sm);outline:none;
  transition:border-color .2s ease;
}
.search-input:focus{border-color:var(--accent)}
.dirty-pill{
  display:none;font-family:var(--mono);font-size:10px;font-weight:600;
  padding:4px 9px;border-radius:999px;background:var(--yellow-dim);
  color:var(--yellow);border:1px solid var(--yellow-dim);
}
.dirty-pill.show{display:inline-flex}
.editor-note{font-size:11px;color:var(--muted);margin-bottom:10px;font-family:var(--mono)}
.editor{
  width:100%;min-height:300px;background:var(--s2);border:1px solid var(--border);
  color:var(--text);font-family:var(--mono);font-size:13px;line-height:1.75;
  padding:16px;border-radius:var(--radius-sm);resize:vertical;outline:none;
  transition:border-color .2s ease,box-shadow .2s ease;tab-size:2;
}
.editor:focus{border-color:var(--accent);box-shadow:0 0 0 3px var(--accent-dim)}
.editor.dirty{border-color:var(--yellow);box-shadow:0 0 0 3px var(--yellow-dim)}

.input,.sel{
  background:var(--s2);border:1px solid var(--border);
  color:var(--text);font-family:var(--mono);font-size:12px;
  padding:8px 12px;border-radius:var(--radius-sm);outline:none;transition:border-color .2s ease;
}
.input:focus,.sel:focus{border-color:var(--accent);box-shadow:0 0 0 3px var(--accent-dim)}
.sel{cursor:pointer}
.sel option{background:var(--s2)}
.form-row{display:flex;gap:8px;align-items:center;flex-wrap:wrap}

.list-item{
  display:flex;align-items:center;gap:12px;
  padding:12px 0;border-bottom:1px solid var(--border);
  transition:background .2s ease;
}
.list-item:last-child{border:none}
.list-item:hover{background:var(--s2);margin:0 -12px;padding:12px 12px;border-radius:var(--radius-sm)}

.pill{font-family:var(--mono);font-size:10px;font-weight:600;padding:3px 8px;border-radius:99px}
.pill-accent{background:var(--accent-dim);color:var(--accent);border:1px solid var(--accent-glow)}
.pill-green {background:var(--green-dim); color:var(--green); border:1px solid var(--green-dim)}
.pill-purple{background:var(--purple-dim);color:var(--purple);border:1px solid var(--purple-dim)}
.pill-muted {background:var(--s3);color:var(--muted);border:1px solid var(--border)}

.member-av{
  width:32px;height:32px;border-radius:50%;
  background:var(--s3);border:1px solid var(--border);
  display:flex;align-items:center;justify-content:center;
  font-size:12px;font-weight:700;color:var(--muted);
  font-family:var(--mono);flex-shrink:0;
}

.audit-time{font-family:var(--mono);font-size:11px;color:var(--muted);width:70px;flex-shrink:0}
.audit-action{
  font-family:var(--mono);font-size:10px;font-weight:600;
  padding:3px 8px;border-radius:6px;min-width:58px;text-align:center;flex-shrink:0;
}
.a-push  {background:var(--green-dim);color:var(--green)}
.a-pull  {background:var(--accent-dim);color:var(--accent)}
.a-invite{background:var(--yellow-dim);color:var(--yellow)}
.a-revoke,.a-token_revoke{background:var(--red-dim);color:var(--red)}
.a-token_create{background:var(--purple-dim);color:var(--purple)}
.a-other {background:var(--s3);color:var(--muted)}

.history-item{
  display:flex;align-items:center;gap:12px;
  padding:12px 10px;border-radius:var(--radius-sm);
  border-bottom:1px solid var(--border);transition:all .2s ease;
}
.history-item:last-child{border:none}
.history-item:hover{background:var(--s2)}
.ver-badge{
  font-family:var(--mono);font-size:11px;font-weight:700;
  padding:3px 9px;border-radius:6px;width:44px;text-align:center;flex-shrink:0;
}
.ver-current{background:var(--green-dim);color:var(--green);border:1px solid var(--green-dim)}
.ver-old    {background:var(--s3);color:var(--muted);border:1px solid var(--border)}

.token-reveal{
  font-family:var(--mono);font-size:12px;word-break:break-all;
  background:var(--green-dim);border:1px solid var(--green-dim);
  color:var(--green);padding:14px;border-radius:var(--radius-sm);margin-top:12px;
  position:relative;cursor:pointer;transition:all .2s ease;
}
.token-reveal:hover{background:rgba(74,222,128,.15);border-color:var(--green)}
.token-reveal::after{
  content:'click to copy';position:absolute;right:12px;top:50%;transform:translateY(-50%);
  font-size:10px;color:rgba(74,222,128,.6);
}

.empty{text-align:center;padding:44px 24px;color:var(--muted);font-size:13px}
.empty-ico{font-size:32px;margin-bottom:12px;opacity:.7}

.toasts{position:fixed;bottom:24px;right:24px;display:flex;flex-direction:column;gap:8px;z-index:999}
.toast{
  display:flex;align-items:center;gap:10px;padding:12px 16px;
  border-radius:var(--radius-sm);font-size:12px;font-family:var(--mono);
  background:var(--s2);border:1px solid var(--border);
  box-shadow:var(--shadow-lg);max-width:380px;
  animation:tIn .2s ease;
}
.t-ok  {border-color:var(--green-dim);color:var(--green);background:rgba(74,222,128,.05)}
.t-err {border-color:var(--red-dim); color:var(--red);background:rgba(248,113,113,.05)}
.t-info{border-color:var(--accent-dim); color:var(--accent);background:rgba(34,211,238,.05)}
@keyframes tIn{from{transform:translateX(20px);opacity:0}to{transform:translateX(0);opacity:1}}

.spin{width:14px;height:14px;border:2px solid var(--border);border-top-color:var(--accent);border-radius:50%;animation:rot .6s linear infinite;display:inline-block;flex-shrink:0}
@keyframes rot{to{transform:rotate(360deg)}}

/* ── loading states ── */
.loading-overlay{
  display:none;position:fixed;inset:0;background:rgba(10,10,15,.85);z-index:200;
  backdrop-filter:blur(8px);align-items:center;justify-content:center;flex-direction:column;gap:16px;
}
.loading-overlay.show{display:flex}
.loading-spinner{
  width:40px;height:40px;border:3px solid var(--border);border-top-color:var(--accent);
  border-radius:50%;animation:rot .8s linear infinite;
}
.loading-text{
  font-family:var(--mono);font-size:13px;color:var(--text-dim);
  animation:pulse 1.5s ease-in-out infinite;
}
.loading-skeleton{
  background:linear-gradient(90deg,var(--s2) 25%,var(--s3) 50%,var(--s2) 75%);
  background-size:200% 100%;animation:shimmer 1.5s infinite;
  border-radius:var(--radius-sm);height:20px;opacity:.6;
}
.loading-skeleton.text{height:16px;width:60%}
.loading-skeleton.full{width:100%}
.loading-skeleton.short{width:40%}

.overlay{
  display:none;position:fixed;inset:0;background:rgba(0,0,0,.5);z-index:90;
  backdrop-filter:blur(4px);
}
.overlay.show{display:block}

@media (max-width:820px){
  .menu-btn{display:inline-flex}
  .sidebar{
    position:fixed;top:var(--topbar-h);left:0;bottom:0;z-index:95;
    transform:translateX(-105%);transition:transform .25s ease;
    width:250px;background:var(--s1);padding:18px 14px;
  }
  .sidebar.open{transform:translateX(0)}
  .env-tabs{max-width:28vw}
  .stats{grid-template-columns:1fr;gap:10px}
  .main{padding:16px;gap:12px}
  .live-pill span{display:none}
  .topbar{padding:0 14px 0 12px}
  .card-body{padding:14px}
  .stat{padding:14px 16px}
  .card-head{padding:12px 14px}
  .project-select{max-width:140px;font-size:12px}
  .logo{font-size:14px}
}

@media (max-width:480px){
  .topbar{gap:8px;padding:0 10px 0 8px}
  .logo{font-size:13px;margin-right:6px}
  .project-select{max-width:100px;padding:5px 8px}
  .stats{gap:8px}
  .stat{padding:12px 14px}
  .stat-value{font-size:20px}
  .main{padding:12px;gap:10px}
  .card-body{padding:12px}
  .btn{padding:6px 10px;font-size:11px}
  .toasts{left:12px;right:12px;bottom:12px}
  .toast{max-width:calc(100vw - 24px)}
}
</style>
</head>
<body>

<div class="overlay" id="navOverlay" onclick="closeNav()"></div>

<div class="topbar">
  <button class="menu-btn" id="menuBtn" onclick="toggleNav()" aria-label="Menu">☰</button>
  <div class="logo">dot<em>sync</em></div>
  <div class="topbar-sep"></div>
  <select class="project-select" id="projectSel" onchange="onProjectChange(this.value)">
    <option>loading...</option>
  </select>
  <div class="env-tabs" id="envTabs"></div>
  <div class="topbar-right">
    <div class="live-pill" id="livePill" title="Local UI connection"><span class="live-dot"></span><span>local</span></div>
    <div class="user-chip">
      <div class="avatar" id="avatarEl">?</div>
      <span id="usernameEl">...</span>
    </div>
  </div>
</div>

<div class="shell">
  <nav class="sidebar" id="sidebar">
    <div class="nav-section">
      <div class="nav-section-label">Secrets</div>
      <div class="nav-item active" data-page="secrets" onclick="nav(this,'secrets')">
        <span class="nav-icon">⬡</span>Secrets
      </div>
      <div class="nav-item" data-page="history" onclick="nav(this,'history')">
        <span class="nav-icon">◷</span>History
      </div>
    </div>
    <div class="nav-section">
      <div class="nav-section-label">Project</div>
      <div class="nav-item" data-page="team" onclick="nav(this,'team')">
        <span class="nav-icon">◎</span>Team
        <span class="nav-badge" id="badgeTeam">0</span>
      </div>
      <div class="nav-item" data-page="tokens" onclick="nav(this,'tokens')">
        <span class="nav-icon">⌘</span>Tokens
        <span class="nav-badge" id="badgeTokens">0</span>
      </div>
      <div class="nav-item" data-page="audit" onclick="nav(this,'audit')">
        <span class="nav-icon">☰</span>Audit Log
      </div>
    </div>
    <div class="sidebar-foot">
      <div class="srv" id="serverEl" title="">—</div>
      <div class="kbd-hint">⌘/Ctrl+S push · ⌘/Ctrl+Shift+L pull</div>
    </div>
  </nav>

  <main class="main">

    <div class="page active" id="page-secrets">
      <div class="stats">
        <div class="stat">
          <div class="stat-label">version</div>
          <div class="stat-value" id="sVer" style="color:var(--green)">—</div>
          <div class="stat-sub" id="sBy">—</div>
        </div>
        <div class="stat">
          <div class="stat-label">keys</div>
          <div class="stat-value" id="sKeys">—</div>
          <div class="stat-sub">encrypted locally</div>
        </div>
        <div class="stat">
          <div class="stat-label">environment</div>
          <div class="stat-value" id="sEnv" style="color:var(--accent)">—</div>
          <div class="stat-sub">active</div>
        </div>
      </div>

      <div class="card">
        <div class="card-head">
          <div class="card-title">
            Editor
            <span class="pill pill-muted" id="keyCountBadge">0 keys</span>
            <span class="dirty-pill" id="dirtyPill">unsaved</span>
          </div>
          <div class="card-actions">
            <button class="btn btn-ghost" onclick="copyEditor()" title="Copy all">⎘ Copy</button>
            <button class="btn btn-ghost" onclick="sortKeys()" title="Sort keys A–Z">A↓</button>
            <button class="btn btn-ghost" id="pullBtn" onclick="doPull()">↓ Pull</button>
            <button class="btn btn-green" id="pushBtn" onclick="doPush()">↑ Push</button>
          </div>
        </div>
        <div class="card-body">
          <div class="editor-note">Decrypted on this machine — server never sees values.</div>
          <div class="editor-toolbar">
            <input class="search-input" id="keySearch" placeholder="Find key…" oninput="findKey(false)" onkeydown="if(event.key==='Enter'){event.preventDefault();findKey(true)}" autocomplete="off">
            <button class="btn btn-ghost" onclick="findKey(true)">Next</button>
          </div>
          <textarea class="editor" id="editor"
            placeholder="DATABASE_URL=postgres://...&#10;API_KEY=sk-live-...&#10;NODE_ENV=production"
            spellcheck="false"></textarea>
        </div>
      </div>
    </div>

    <div class="page" id="page-history">
      <div class="card">
        <div class="card-head">
          <div class="card-title">Version History</div>
          <div class="card-actions">
            <button class="btn btn-ghost" onclick="loadHistory()">↻ Refresh</button>
          </div>
        </div>
        <div class="card-body" id="historyBody">
          <div class="empty"><div class="empty-ico">◷</div>Open this page to load history</div>
        </div>
      </div>
    </div>

    <div class="page" id="page-team">
      <div class="card">
        <div class="card-head"><div class="card-title">Team Members</div></div>
        <div class="card-body">
          <div class="form-row" style="margin-bottom:14px">
            <input class="input" id="memberInput" placeholder="github-username" style="flex:1" onkeydown="if(event.key==='Enter')addMember()">
            <select class="sel" id="memberRole" style="width:110px">
              <option value="member">member</option>
              <option value="admin">admin</option>
              <option value="viewer">viewer</option>
            </select>
            <button class="btn btn-primary" onclick="addMember()">+ Add</button>
          </div>
          <div id="memberBody"></div>
        </div>
      </div>
    </div>

    <div class="page" id="page-tokens">
      <div class="card">
        <div class="card-head"><div class="card-title">Service Tokens</div></div>
        <div class="card-body">
          <div class="form-row" style="margin-bottom:14px">
            <input class="input" id="tokenName" placeholder="name (e.g. github-prod)" style="flex:1" onkeydown="if(event.key==='Enter')createToken()">
            <select class="sel" id="tokenEnv" style="width:130px">
              <option value="*">all envs (*)</option>
            </select>
            <button class="btn btn-primary" onclick="createToken()">+ Create</button>
          </div>
          <div id="tokenReveal"></div>
          <div id="tokenBody"></div>
        </div>
      </div>
    </div>

    <div class="page" id="page-audit">
      <div class="card">
        <div class="card-head">
          <div class="card-title">Audit Log</div>
          <div class="card-actions">
            <button class="btn btn-ghost" onclick="refreshAudit()">↻ Refresh</button>
          </div>
        </div>
        <div class="card-body" id="auditBody">
          <div class="empty"><div class="empty-ico">◷</div>Loading...</div>
        </div>
      </div>
    </div>

  </main>
</div>

<div class="loading-overlay" id="loadingOverlay">
  <div class="loading-spinner"></div>
  <div class="loading-text" id="loadingText">Loading...</div>
</div>

<div class="toasts" id="toasts"></div>

<script>
const S = { project: null, env: 'dev', me: null, data: null, baseline: '', findIdx: -1 };

function validSlug(s) {
  return !!(s && s !== 'null' && s !== 'undefined');
}

function q(slug, env) {
  return 'slug=' + encodeURIComponent(slug) + '&env=' + encodeURIComponent(env || 'dev');
}

function isDirty() {
  const ed = document.getElementById('editor');
  return !!(ed && ed.value !== S.baseline);
}

function setDirtyUI() {
  const dirty = isDirty();
  const ed = document.getElementById('editor');
  const pill = document.getElementById('dirtyPill');
  if (ed) ed.classList.toggle('dirty', dirty);
  if (pill) pill.classList.toggle('show', dirty);
}

function confirmDiscard() {
  if (!isDirty()) return true;
  return confirm('You have unsaved edits. Discard them?');
}

function markClean(content) {
  S.baseline = content == null ? document.getElementById('editor').value : content;
  setDirtyUI();
}

function toggleNav() {
  document.getElementById('sidebar').classList.toggle('open');
  document.getElementById('navOverlay').classList.toggle('show');
}
function closeNav() {
  document.getElementById('sidebar').classList.remove('open');
  document.getElementById('navOverlay').classList.remove('show');
}

function showLoading(text = 'Loading...') {
  const overlay = document.getElementById('loadingOverlay');
  const textEl = document.getElementById('loadingText');
  textEl.textContent = text;
  overlay.classList.add('show');
}

function hideLoading() {
  document.getElementById('loadingOverlay').classList.remove('show');
}

(async () => {
  try {
    showLoading('Connecting to server...');
    const me = await api('/api/me');
    S.me = me;
    const u = me.user || {};
    document.getElementById('usernameEl').textContent = '@' + (u.username || '?');
    const av = document.getElementById('avatarEl');
    if (u.avatar_url) {
      av.outerHTML = '<img class="avatar" id="avatarEl" src="' + esc(u.avatar_url) + '" alt="">';
    } else {
      av.textContent = (u.username || '?')[0].toUpperCase();
    }
    const srv = me.server_url || '';
    const srvEl = document.getElementById('serverEl');
    srvEl.textContent = srv || 'local';
    srvEl.title = srv;

    showLoading('Loading projects...');
    const ps = await api('/api/projects');
    const projects = (ps.projects || []).filter(p => validSlug(p && p.slug));
    const sel = document.getElementById('projectSel');

    if (!projects.length) {
      sel.innerHTML = '<option value="">no projects — run dotsync init</option>';
      hideLoading();
      toast('No projects found — run: dotsync init', 'err');
      setLive(true);
      return;
    }

    sel.innerHTML = projects.map(p => '<option value="' + esc(p.slug) + '">' + esc(p.slug) + '</option>').join('');

    const defSlug = me.project && me.project.project_slug;
    const defEnv  = me.project && me.project.default_env;
    if (validSlug(defSlug)) sel.value = defSlug;
    if (defEnv) S.env = defEnv;

    const slug = sel.value;
    if (!validSlug(slug)) {
      hideLoading();
      toast('No project selected — run: dotsync init', 'err');
      return;
    }
    await switchProject(slug);
    hideLoading();

    const hash = (location.hash || '#secrets').slice(1);
    const navEl = document.querySelector('.nav-item[data-page="' + hash + '"]');
    if (navEl) nav(navEl, hash);
  } catch(e) { hideLoading(); toast('Connection failed: ' + e.message, 'err'); }
})();

async function onProjectChange(slug) {
  if (!confirmDiscard()) {
    document.getElementById('projectSel').value = S.project || '';
    return;
  }
  await switchProject(slug);
}

async function switchProject(slug) {
  if (!validSlug(slug)) return;
  showLoading('Switching project...');
  S.project = slug;
  await loadProject();
  connectSSE();
  hideLoading();
}

async function loadProject() {
  if (!validSlug(S.project)) return;
  const d = await api('/api/project/?slug=' + encodeURIComponent(S.project));
  S.data = d;

  const envs = normalizeEnvs(d.envs);
  if (envs.length && envs.indexOf(S.env) < 0) S.env = envs[0];

  document.getElementById('envTabs').innerHTML = envs.map(e =>
    '<div class="env-tab' + (e===S.env?' active':'') + '" onclick="onEnvClick(\'' + e + '\')">' + esc(e) + '</div>'
  ).join('');

  document.getElementById('tokenEnv').innerHTML =
    '<option value="*">all envs (*)</option>' +
    envs.map(e => '<option value="' + esc(e) + '"' + (e===S.env?' selected':'') + '>' + esc(e) + '</option>').join('');

  renderMembers(d.members || []);
  renderTokens(d.tokens || []);
  renderAudit(d.logs || []);
  document.getElementById('sEnv').textContent = S.env;
  document.getElementById('badgeTeam').textContent = (d.members || []).length;
  document.getElementById('badgeTokens').textContent = (d.tokens || []).length;

  await pullSecrets(false);
}

function normalizeEnvs(envs) {
  if (!envs || !envs.length) return ['dev','staging','production'];
  if (typeof envs[0] === 'string') return envs;
  return envs.map(e => e.name || e).filter(Boolean);
}

function onEnvClick(env) {
  if (!confirmDiscard()) return;
  switchEnv(env);
}

function switchEnv(env) {
  if (!validSlug(S.project)) return;
  S.env = env;
  document.querySelectorAll('.env-tab').forEach(t => t.classList.toggle('active', t.textContent===env));
  document.getElementById('sEnv').textContent = env;
  pullSecrets(false);
  if (document.getElementById('page-history').classList.contains('active')) loadHistory();
}

async function pullSecrets(toastOk) {
  if (!validSlug(S.project)) return;
  if (!toastOk) showLoading('Pulling secrets...');
  try {
    const r = await api('/api/pull?' + q(S.project, S.env));
    const content = r.content || '';
    document.getElementById('editor').value = content;
    document.getElementById('sVer').textContent = 'v' + r.version;
    document.getElementById('sBy').textContent = r.by ? '@' + r.by : '';
    markClean(content);
    countKeys();
    if (toastOk) toast('Pulled v' + r.version + ' — ' + r.keys + ' secrets', 'ok');
  } catch(_) {
    document.getElementById('editor').value = '';
    document.getElementById('sVer').textContent = 'none';
    document.getElementById('sBy').textContent = 'no push yet';
    markClean('');
    countKeys();
  } finally {
    if (!toastOk) hideLoading();
  }
}

function countKeys() {
  const lines = (document.getElementById('editor').value || '').split('\n');
  const n = lines.filter(l => l.trim() && !l.trim().startsWith('#') && l.includes('=')).length;
  document.getElementById('keyCountBadge').textContent = n + ' key' + (n!==1?'s':'');
  document.getElementById('sKeys').textContent = n;
  setDirtyUI();
}

async function doPull() {
  if (!validSlug(S.project)) { toast('No project selected', 'err'); return; }
  if (!confirmDiscard()) return;
  const btn = document.getElementById('pullBtn');
  btn.disabled = true; btn.innerHTML = '<span class="spin"></span> Pulling';
  try {
    await pullSecrets(true);
  } catch(e) { toast(e.message, 'err'); }
  finally { btn.disabled=false; btn.innerHTML='↓ Pull'; }
}

async function doPush() {
  if (!validSlug(S.project)) { toast('No project selected', 'err'); return; }
  const content = document.getElementById('editor').value.trim();
  if (!content) { toast('Editor is empty', 'err'); return; }
  const btn = document.getElementById('pushBtn');
  btn.disabled = true; btn.innerHTML = '<span class="spin"></span> Pushing';
  try {
    const r = await post('/api/push', { slug: S.project, env: S.env, content });
    document.getElementById('sVer').textContent = 'v' + r.version;
    markClean(document.getElementById('editor').value);
    countKeys();
    toast('Pushed v' + r.version + ' — ' + r.keys + ' keys encrypted', 'ok');
    // Refresh metadata only (members/tokens/audit) — avoid extra pull
    try {
      const d = await api('/api/project/?slug=' + encodeURIComponent(S.project));
      S.data = d;
      renderMembers(d.members || []);
      renderTokens(d.tokens || []);
      renderAudit(d.logs || []);
      document.getElementById('badgeTeam').textContent = (d.members || []).length;
      document.getElementById('badgeTokens').textContent = (d.tokens || []).length;
    } catch(_) {}
  } catch(e) { toast(e.message, 'err'); }
  finally { btn.disabled=false; btn.innerHTML='↑ Push'; }
}

function copyEditor() {
  const v = document.getElementById('editor').value;
  if (!v) { toast('Editor is empty', 'err'); return; }
  navigator.clipboard.writeText(v).then(() => toast('Copied to clipboard', 'ok'))
    .catch(() => toast('Copy failed', 'err'));
}

function sortKeys() {
  const ed = document.getElementById('editor');
  const lines = ed.value.split('\n');
  const comments = [], keys = [], other = [];
  for (const line of lines) {
    const t = line.trim();
    if (!t) { other.push(line); continue; }
    if (t.startsWith('#')) comments.push(line);
    else if (t.includes('=')) keys.push(line);
    else other.push(line);
  }
  keys.sort((a,b) => a.split('=')[0].localeCompare(b.split('=')[0], undefined, {sensitivity:'base'}));
  ed.value = [...comments, ...keys, ...other.filter(l => l.trim())].join('\n');
  countKeys();
  toast('Keys sorted', 'info');
}

function findKey(next) {
  const qstr = (document.getElementById('keySearch').value || '').trim().toLowerCase();
  const ed = document.getElementById('editor');
  if (!qstr) return;
  const text = ed.value;
  const lower = text.toLowerCase();
  let start = next ? (S.findIdx + 1) : 0;
  let idx = lower.indexOf(qstr, start);
  if (idx < 0 && start > 0) idx = lower.indexOf(qstr, 0);
  if (idx < 0) { toast('No match', 'info'); return; }
  S.findIdx = idx;
  // Only focus editor when explicitly searching (next button)
  if (next) {
    ed.focus();
    ed.setSelectionRange(idx, idx + qstr.length);
    const pre = text.slice(0, idx);
    const line = pre.split('\n').length;
    const lineH = 1.75 * 12.5;
    ed.scrollTop = Math.max(0, (line - 3) * lineH);
  }
}

async function loadHistory() {
  const el = document.getElementById('historyBody');
  if (!validSlug(S.project)) {
    el.innerHTML = '<div class="empty"><div class="empty-ico">◷</div>Select a project first</div>';
    return;
  }
  el.innerHTML = '<div class="empty"><span class="spin"></span></div>';
  try {
    const r = await api('/api/history?' + q(S.project, S.env));
    const h = r.history || [];
    if (!h.length) { el.innerHTML = '<div class="empty"><div class="empty-ico">◷</div>No history yet</div>'; return; }
    el.innerHTML = h.map((e,i) => {
      const cur = i===0;
      return '<div class="history-item">' +
        '<div class="ver-badge ' + (cur?'ver-current':'ver-old') + '">v' + e.version + '</div>' +
        '<div style="flex:1">' +
          '<div style="font-family:var(--mono);font-size:12px">@' + esc(e.pushed_by) + '</div>' +
          '<div style="font-size:11px;color:var(--muted)">' + ago(e.created_at) + '</div>' +
        '</div>' +
        (cur
          ? '<span class="pill pill-green">current</span>'
          : '<button class="btn btn-ghost" style="font-size:11px;padding:4px 9px" onclick="rollback(' + e.version + ')">↩ Restore</button>'
        ) +
      '</div>';
    }).join('');
  } catch(e) { el.innerHTML = '<div class="empty">' + esc(e.message) + '</div>'; }
}

async function rollback(version) {
  if (!validSlug(S.project)) { toast('No project selected', 'err'); return; }
  if (!confirm('Restore v' + version + ' as new current version?')) return;
  showLoading('Rolling back to v' + version + '...');
  try {
    const r = await post('/api/rollback', { slug:S.project, env:S.env, version });
    hideLoading();
    toast('v' + version + ' restored as v' + r.version, 'ok');
    loadHistory();
    await pullSecrets(false);
  } catch(e) { hideLoading(); toast(e.message, 'err'); }
}

function renderMembers(members) {
  const el = document.getElementById('memberBody');
  if (!members.length) { el.innerHTML = '<div class="empty"><div class="empty-ico">◎</div>No members yet</div>'; return; }
  const me = S.me && S.me.user && S.me.user.username;
  const roleClass = { owner:'pill-accent', admin:'pill-purple', member:'pill-green', viewer:'pill-muted' };
  el.innerHTML = members.map(m => {
    const isMe = m.username === me;
    return '<div class="list-item">' +
      '<div class="member-av">' + (m.username||'?')[0].toUpperCase() + '</div>' +
      '<div style="flex:1">' +
        '<div style="font-family:var(--mono);font-size:12px;' + (isMe?'color:var(--accent)':'') + '">@' + esc(m.username) + (isMe?' <span style="color:var(--muted);font-size:10px">(you)</span>':'') + '</div>' +
        '<div style="font-size:11px;color:var(--muted);font-family:var(--mono)">' + (m.joined_at||'').slice(0,10) + '</div>' +
      '</div>' +
      '<span class="pill ' + (roleClass[m.role]||'pill-muted') + '">' + m.role + '</span>' +
      (!isMe ? '<button class="btn btn-red" style="font-size:11px;padding:4px 9px" onclick="removeMember(\'' + esc(m.username) + '\')">Remove</button>' : '') +
    '</div>';
  }).join('');
}

async function addMember() {
  if (!validSlug(S.project)) { toast('No project selected', 'err'); return; }
  const u = document.getElementById('memberInput').value.trim().replace('@','');
  const r = document.getElementById('memberRole').value;
  if (!u) { toast('Enter a username', 'err'); return; }
  showLoading('Adding member...');
  try {
    await post('/api/team/add', { slug:S.project, username:u, role:r });
    document.getElementById('memberInput').value = '';
    hideLoading();
    toast('@' + u + ' added as ' + r, 'ok');
    await loadProject();
  } catch(e) { hideLoading(); toast(e.message, 'err'); }
}

async function removeMember(u) {
  if (!validSlug(S.project)) { toast('No project selected', 'err'); return; }
  if (!confirm('Remove @' + u + '?')) return;
  showLoading('Removing member...');
  try {
    await post('/api/team/remove', { slug:S.project, username:u });
    hideLoading();
    toast('@' + u + ' removed', 'ok');
    await loadProject();
  } catch(e) { hideLoading(); toast(e.message, 'err'); }
}

function renderTokens(tokens) {
  const el = document.getElementById('tokenBody');
  if (!tokens.length) { el.innerHTML = '<div class="empty" style="padding-top:12px"><div class="empty-ico">⌘</div>No tokens. Create one for CI/CD.</div>'; return; }
  el.innerHTML = tokens.map(t =>
    '<div class="list-item">' +
    '<div style="flex:1;font-family:var(--mono);font-size:12px">' + esc(t.name) + '</div>' +
    '<span class="pill pill-accent" style="margin-right:6px">' + esc(t.env) + '</span>' +
    '<div style="font-size:11px;color:var(--muted);font-family:var(--mono);margin-right:10px">used ' + ago(t.last_used_at) + '</div>' +
    '<button class="btn btn-red" style="font-size:11px;padding:4px 9px" onclick="revokeToken(\'' + esc(t.id) + '\',\'' + esc(t.name) + '\')">Revoke</button>' +
    '</div>'
  ).join('');
}

async function createToken() {
  if (!validSlug(S.project)) { toast('No project selected', 'err'); return; }
  const name = document.getElementById('tokenName').value.trim();
  const env  = document.getElementById('tokenEnv').value;
  if (!name) { toast('Enter a token name', 'err'); return; }
  showLoading('Creating token...');
  try {
    const r = await post('/api/tokens/create', { slug:S.project, env, name });
    document.getElementById('tokenName').value = '';
    hideLoading();
    const rev = document.getElementById('tokenReveal');
    rev.innerHTML = '<div style="font-size:11px;color:var(--yellow);margin-bottom:4px;font-family:var(--mono)">⚠ Copy now — shown once only</div>' +
      '<div class="token-reveal" onclick="navigator.clipboard.writeText(\'' + r.token + '\').then(()=>toast(\'Copied!\',\'ok\'))">' + r.token + '</div>';
    toast('Token created — copy it now!', 'ok');
    await loadProject();
  } catch(e) { hideLoading(); toast(e.message, 'err'); }
}

async function revokeToken(id, name) {
  if (!validSlug(S.project)) { toast('No project selected', 'err'); return; }
  if (!confirm('Revoke "' + name + '"? Cannot be undone.')) return;
  showLoading('Revoking token...');
  try {
    await post('/api/tokens/revoke', { slug:S.project, token_id:id });
    hideLoading();
    toast('Token revoked', 'ok');
    await loadProject();
  } catch(e) { hideLoading(); toast(e.message, 'err'); }
}

function renderAudit(logs) {
  const el = document.getElementById('auditBody');
  if (!logs.length) { el.innerHTML = '<div class="empty"><div class="empty-ico">☰</div>No events yet</div>'; return; }
  const cls = a => ({push:'a-push',pull:'a-pull',invite:'a-invite',revoke:'a-revoke',token_create:'a-token_create',token_revoke:'a-token_revoke'}[a]||'a-other');
  el.innerHTML = logs.map(l =>
    '<div class="list-item">' +
    '<div class="audit-time">' + ago(l.created_at) + '</div>' +
    '<span class="audit-action ' + cls(l.action) + '">' + esc(l.action||'?') + '</span>' +
    '<div>' +
      '<div style="font-family:var(--mono);font-size:12px">@' + esc(l.username||'?') + '</div>' +
      (l.env ? '<div style="font-size:11px;color:var(--accent);font-family:var(--mono)">' + esc(l.env) + '</div>' : '') +
    '</div>' +
    '</div>'
  ).join('');
}

async function refreshAudit() {
  if (!validSlug(S.project)) return;
  showLoading('Refreshing audit log...');
  try {
    const d = await api('/api/project/?slug=' + encodeURIComponent(S.project));
    S.data = d;
    renderAudit(d.logs || []);
    hideLoading();
    toast('Audit refreshed', 'ok');
  } catch(e) { hideLoading(); toast(e.message, 'err'); }
}

function nav(el, page) {
  document.querySelectorAll('.nav-item').forEach(n => n.classList.remove('active'));
  el.classList.add('active');
  document.querySelectorAll('.page').forEach(p => p.classList.remove('active'));
  document.getElementById('page-' + page).classList.add('active');
  location.hash = page;
  closeNav();
  if (page === 'history') loadHistory();
  if (page === 'audit' && S.data) renderAudit(S.data.logs || []);
}

async function api(url) {
  const r = await fetch(url);
  const d = await r.json();
  if (d.error) throw new Error(d.error);
  return d;
}

async function post(url, body) {
  const r = await fetch(url, { method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(body) });
  const d = await r.json();
  if (d.error) throw new Error(d.error);
  return d;
}

function toast(msg, type='info') {
  const el = document.createElement('div');
  el.className = 'toast t-' + type;
  el.textContent = (type==='ok'?'✓ ':type==='err'?'✗ ':'→ ') + msg;
  document.getElementById('toasts').appendChild(el);
  setTimeout(()=>el.remove(), 3800);
}

function ago(iso) {
  if (!iso) return 'never';
  const s = (Date.now() - new Date(iso).getTime()) / 1000;
  if (s < 60)    return Math.floor(s) + 's ago';
  if (s < 3600)  return Math.floor(s/60) + 'm ago';
  if (s < 86400) return Math.floor(s/3600) + 'h ago';
  return new Date(iso).toISOString().slice(0,10);
}

function esc(s) {
  return String(s||'').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

function setLive(on) {
  document.getElementById('livePill').classList.toggle('on', !!on);
}

// SSE keepalive only — no remote polling / no auto-refresh side effects
let _sse = null, _sseBackoff = 1000;

function connectSSE() {
  if (_sse) { _sse.close(); _sse = null; }
  if (document.visibilityState === 'hidden') return;

  _sse = new EventSource('/api/events');
  _sse.addEventListener('ping', () => {
    setLive(true);
    _sseBackoff = 1000;
  });
  _sse.onerror = () => {
    setLive(false);
    if (_sse) { _sse.close(); _sse = null; }
    const delay = _sseBackoff;
    _sseBackoff = Math.min(_sseBackoff * 2, 60000);
    setTimeout(() => {
      if (document.visibilityState !== 'hidden') connectSSE();
    }, delay);
  };
}

document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'hidden') {
    if (_sse) { _sse.close(); _sse = null; }
    setLive(false);
  } else {
    connectSSE();
  }
});

document.addEventListener('DOMContentLoaded', () => {
  const ed = document.getElementById('editor');
  if (ed) {
    ed.addEventListener('input', () => { countKeys(); });
    ed.addEventListener('keydown', (e) => {
      if (e.key === 'Tab') {
        e.preventDefault();
        const start = ed.selectionStart, end = ed.selectionEnd;
        ed.value = ed.value.slice(0, start) + '  ' + ed.value.slice(end);
        ed.selectionStart = ed.selectionEnd = start + 2;
        countKeys();
      }
    });
  }
});

document.addEventListener('keydown', (e) => {
  const meta = e.metaKey || e.ctrlKey;
  if (meta && e.key.toLowerCase() === 's') {
    e.preventDefault();
    doPush();
  }
  if (meta && e.shiftKey && e.key.toLowerCase() === 'l') {
    e.preventDefault();
    doPull();
  }
});

window.addEventListener('beforeunload', (e) => {
  if (isDirty()) { e.preventDefault(); e.returnValue = ''; }
});
</script>
</body>
</html>`
