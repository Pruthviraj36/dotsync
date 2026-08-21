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
  --bg:      #09090d;
  --s1:      #101016;
  --s2:      #17171f;
  --s3:      #22222c;
  --border:  rgba(255,255,255,0.08);
  --accent:  #2ee6ff;
  --accent-dim: rgba(46,230,255,.12);
  --purple:  #a78bfa;
  --green:   #34f5a0;
  --yellow:  #f5c84c;
  --red:     #ff5c5c;
  --text:    #ececf4;
  --muted:   #6b6b7b;
  --mono:    'IBM Plex Mono', ui-monospace, monospace;
  --sans:    'Outfit', system-ui, sans-serif;
  --radius:  10px;
  --topbar-h: 52px;
}
*{box-sizing:border-box;margin:0;padding:0}
html,body{height:100%;background:var(--bg);color:var(--text);font-family:var(--sans);font-size:13.5px;line-height:1.5}
body{
  background:
    radial-gradient(1200px 600px at 10% -10%, rgba(46,230,255,.07), transparent 55%),
    radial-gradient(900px 500px at 100% 0%, rgba(167,139,250,.06), transparent 50%),
    var(--bg);
}

::-webkit-scrollbar{width:6px;height:6px}
::-webkit-scrollbar-track{background:transparent}
::-webkit-scrollbar-thumb{background:rgba(255,255,255,.1);border-radius:4px}

/* ── topbar ── */
.topbar{
  display:flex;align-items:center;gap:10px;
  height:var(--topbar-h);border-bottom:1px solid var(--border);
  background:rgba(16,16,22,.88);backdrop-filter:blur(12px);
  position:fixed;top:0;left:0;right:0;z-index:100;padding:0 16px 0 12px;
}
.menu-btn{
  display:none;align-items:center;justify-content:center;
  width:34px;height:34px;border-radius:8px;border:1px solid var(--border);
  background:var(--s2);color:var(--text);cursor:pointer;font-size:16px;
}
.logo{font-family:var(--mono);font-weight:700;font-size:15px;color:var(--accent);letter-spacing:-.4px;margin-right:8px;white-space:nowrap}
.logo em{color:var(--purple);font-style:normal}
.topbar-sep{width:1px;height:22px;background:var(--border);margin:0 6px;flex-shrink:0}
.project-select{
  background:var(--s2);border:1px solid var(--border);color:var(--text);
  font-family:var(--mono);font-size:12.5px;cursor:pointer;outline:none;
  padding:5px 10px;border-radius:8px;max-width:200px;
}
.project-select:focus{border-color:var(--accent)}
.project-select option{background:var(--s2)}
.env-tabs{display:flex;gap:4px;margin-left:4px;overflow-x:auto;max-width:42vw}
.env-tab{
  padding:4px 11px;border-radius:999px;cursor:pointer;
  font-family:var(--mono);font-size:11px;color:var(--muted);
  transition:.15s;border:1px solid transparent;white-space:nowrap;
}
.env-tab:hover{color:var(--text);background:var(--s2)}
.env-tab.active{color:var(--accent);background:var(--accent-dim);border-color:rgba(46,230,255,.28)}
.topbar-right{margin-left:auto;display:flex;align-items:center;gap:10px}
.live-pill{
  display:inline-flex;align-items:center;gap:6px;
  font-family:var(--mono);font-size:10px;color:var(--muted);
  padding:4px 9px;border-radius:999px;border:1px solid var(--border);background:var(--s2);
}
.live-dot{width:7px;height:7px;border-radius:50%;background:var(--muted);flex-shrink:0}
.live-pill.on .live-dot{background:var(--green);box-shadow:0 0 8px rgba(52,245,160,.55)}
.live-pill.on{color:var(--green);border-color:rgba(52,245,160,.25)}
.user-chip{display:flex;align-items:center;gap:8px;font-family:var(--mono);font-size:12px;color:var(--muted)}
.avatar{
  width:28px;height:28px;border-radius:50%;background:linear-gradient(135deg,var(--purple),var(--accent));
  display:flex;align-items:center;justify-content:center;font-size:11px;font-weight:700;color:#fff;flex-shrink:0;
  overflow:hidden;object-fit:cover;
}

/* ── layout ── */
.shell{display:flex;height:calc(100vh - var(--topbar-h));margin-top:var(--topbar-h);overflow:hidden}
.sidebar{
  width:200px;flex-shrink:0;border-right:1px solid var(--border);
  background:rgba(16,16,22,.75);padding:14px 10px;overflow-y:auto;
  display:flex;flex-direction:column;
}
.nav-section{margin-bottom:18px}
.nav-section-label{
  font-size:10px;font-weight:600;text-transform:uppercase;letter-spacing:1.1px;
  color:var(--muted);padding:0 10px 7px;
}
.nav-item{
  display:flex;align-items:center;gap:9px;
  padding:8px 10px;border-radius:8px;cursor:pointer;
  color:var(--muted);transition:.12s;font-size:13.5px;user-select:none;font-weight:500;
}
.nav-item:hover{color:var(--text);background:var(--s2)}
.nav-item.active{color:var(--accent);background:var(--accent-dim)}
.nav-icon{font-size:14px;width:18px;text-align:center;flex-shrink:0;opacity:.9}
.nav-badge{
  margin-left:auto;font-family:var(--mono);font-size:10px;
  background:var(--s3);color:var(--muted);padding:1px 6px;border-radius:999px;
}
.sidebar-foot{
  margin-top:auto;padding:12px 10px 4px;border-top:1px solid var(--border);
  font-family:var(--mono);font-size:10px;color:var(--muted);line-height:1.6;
}
.sidebar-foot .srv{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:170px}
.kbd-hint{opacity:.75;margin-top:6px}

.main{flex:1;overflow-y:auto;padding:22px;display:flex;flex-direction:column;gap:14px}
.page{display:none;flex-direction:column;gap:14px;animation:fade .18s ease}
.page.active{display:flex}
@keyframes fade{from{opacity:0;transform:translateY(4px)}to{opacity:1;transform:none}}

.stats{display:grid;grid-template-columns:repeat(3,1fr);gap:10px}
.stat{
  background:linear-gradient(180deg, rgba(255,255,255,.03), transparent), var(--s1);
  border:1px solid var(--border);border-radius:var(--radius);padding:14px 16px;
}
.stat-label{font-size:10px;text-transform:uppercase;letter-spacing:.9px;color:var(--muted);margin-bottom:6px;font-weight:600}
.stat-value{font-family:var(--mono);font-size:22px;font-weight:700;line-height:1}
.stat-sub{font-family:var(--mono);font-size:11px;color:var(--muted);margin-top:5px}

.card{background:var(--s1);border:1px solid var(--border);border-radius:var(--radius);overflow:hidden}
.card-head{
  display:flex;align-items:center;justify-content:space-between;gap:10px;flex-wrap:wrap;
  padding:12px 16px;border-bottom:1px solid var(--border);
}
.card-title{font-size:13px;font-weight:600;color:var(--text);display:flex;align-items:center;gap:8px}
.card-actions{display:flex;gap:6px;flex-wrap:wrap;align-items:center}
.card-body{padding:16px}

.btn{
  display:inline-flex;align-items:center;gap:5px;
  padding:6px 12px;border-radius:7px;font-size:12px;font-weight:500;
  cursor:pointer;border:none;transition:.12s;
  font-family:var(--mono);white-space:nowrap;line-height:1.4;
}
.btn-primary{background:var(--accent);color:#041018}
.btn-primary:hover{filter:brightness(1.08)}
.btn-ghost{background:transparent;color:var(--muted);border:1px solid var(--border)}
.btn-ghost:hover{border-color:var(--accent);color:var(--accent)}
.btn-green{background:var(--green);color:#041810}
.btn-green:hover{filter:brightness(1.05)}
.btn-red{background:transparent;color:var(--red);border:1px solid rgba(255,92,92,.28)}
.btn-red:hover{background:rgba(255,92,92,.08)}
.btn:disabled{opacity:.35;cursor:not-allowed}

.editor-toolbar{display:flex;gap:8px;align-items:center;margin-bottom:10px;flex-wrap:wrap}
.search-input{
  flex:1;min-width:140px;background:var(--s2);border:1px solid var(--border);
  color:var(--text);font-family:var(--mono);font-size:12px;
  padding:7px 11px;border-radius:7px;outline:none;
}
.search-input:focus{border-color:var(--accent)}
.dirty-pill{
  display:none;font-family:var(--mono);font-size:10px;font-weight:600;
  padding:3px 8px;border-radius:999px;background:rgba(245,200,76,.12);
  color:var(--yellow);border:1px solid rgba(245,200,76,.3);
}
.dirty-pill.show{display:inline-flex}
.editor-note{font-size:11px;color:var(--muted);margin-bottom:8px;font-family:var(--mono)}
.editor{
  width:100%;min-height:280px;background:var(--s2);border:1px solid var(--border);
  color:var(--text);font-family:var(--mono);font-size:12.5px;line-height:1.75;
  padding:14px;border-radius:8px;resize:vertical;outline:none;
  transition:border-color .15s;tab-size:2;
}
.editor:focus{border-color:var(--accent)}
.editor.dirty{border-color:rgba(245,200,76,.45)}

.input,.sel{
  background:var(--s2);border:1px solid var(--border);
  color:var(--text);font-family:var(--mono);font-size:12px;
  padding:7px 10px;border-radius:7px;outline:none;transition:border-color .15s;
}
.input:focus,.sel:focus{border-color:var(--accent)}
.sel{cursor:pointer}
.sel option{background:var(--s2)}
.form-row{display:flex;gap:7px;align-items:center;flex-wrap:wrap}

.list-item{
  display:flex;align-items:center;gap:11px;
  padding:10px 0;border-bottom:1px solid var(--border);
}
.list-item:last-child{border:none}

.pill{font-family:var(--mono);font-size:10px;font-weight:600;padding:2px 7px;border-radius:99px}
.pill-accent{background:var(--accent-dim);color:var(--accent);border:1px solid rgba(46,230,255,.22)}
.pill-green {background:rgba(52,245,160,.1); color:var(--green); border:1px solid rgba(52,245,160,.22)}
.pill-purple{background:rgba(167,139,250,.12);color:var(--purple);border:1px solid rgba(167,139,250,.22)}
.pill-muted {background:var(--s3);color:var(--muted);border:1px solid var(--border)}

.member-av{
  width:30px;height:30px;border-radius:50%;
  background:var(--s3);border:1px solid var(--border);
  display:flex;align-items:center;justify-content:center;
  font-size:11px;font-weight:700;color:var(--muted);
  font-family:var(--mono);flex-shrink:0;
}

.audit-time{font-family:var(--mono);font-size:11px;color:var(--muted);width:68px;flex-shrink:0}
.audit-action{
  font-family:var(--mono);font-size:10px;font-weight:600;
  padding:2px 7px;border-radius:4px;min-width:56px;text-align:center;flex-shrink:0;
}
.a-push  {background:rgba(52,245,160,.1);color:var(--green)}
.a-pull  {background:var(--accent-dim);color:var(--accent)}
.a-invite{background:rgba(245,200,76,.1);color:var(--yellow)}
.a-revoke,.a-token_revoke{background:rgba(255,92,92,.1);color:var(--red)}
.a-token_create{background:rgba(167,139,250,.1);color:var(--purple)}
.a-other {background:var(--s3);color:var(--muted)}

.history-item{
  display:flex;align-items:center;gap:11px;
  padding:10px 8px;border-radius:8px;
  border-bottom:1px solid var(--border);transition:.1s;
}
.history-item:last-child{border:none}
.history-item:hover{background:var(--s2)}
.ver-badge{
  font-family:var(--mono);font-size:11px;font-weight:700;
  padding:2px 8px;border-radius:4px;width:42px;text-align:center;flex-shrink:0;
}
.ver-current{background:rgba(52,245,160,.12);color:var(--green);border:1px solid rgba(52,245,160,.25)}
.ver-old    {background:var(--s3);color:var(--muted);border:1px solid var(--border)}

.token-reveal{
  font-family:var(--mono);font-size:11.5px;word-break:break-all;
  background:rgba(52,245,160,.05);border:1px solid rgba(52,245,160,.22);
  color:var(--green);padding:12px;border-radius:8px;margin-top:10px;
  position:relative;cursor:pointer;
}
.token-reveal::after{
  content:'click to copy';position:absolute;right:10px;top:50%;transform:translateY(-50%);
  font-size:10px;color:rgba(52,245,160,.5);
}

.empty{text-align:center;padding:40px 20px;color:var(--muted);font-size:12.5px}
.empty-ico{font-size:28px;margin-bottom:10px;opacity:.7}

.toasts{position:fixed;bottom:20px;right:20px;display:flex;flex-direction:column;gap:7px;z-index:999}
.toast{
  display:flex;align-items:center;gap:9px;padding:10px 14px;
  border-radius:8px;font-size:12px;font-family:var(--mono);
  background:var(--s2);border:1px solid var(--border);
  box-shadow:0 10px 28px rgba(0,0,0,.45);max-width:360px;
  animation:tIn .18s ease;
}
.t-ok  {border-color:rgba(52,245,160,.3);color:var(--green)}
.t-err {border-color:rgba(255,92,92,.3); color:var(--red)}
.t-info{border-color:rgba(46,230,255,.3); color:var(--accent)}
@keyframes tIn{from{transform:translateX(16px);opacity:0}to{transform:translateX(0);opacity:1}}

.spin{width:12px;height:12px;border:2px solid var(--border);border-top-color:var(--accent);border-radius:50%;animation:rot .65s linear infinite;display:inline-block;flex-shrink:0}
@keyframes rot{to{transform:rotate(360deg)}}

.overlay{
  display:none;position:fixed;inset:0;background:rgba(0,0,0,.45);z-index:90;
}
.overlay.show{display:block}

@media (max-width:820px){
  .menu-btn{display:inline-flex}
  .sidebar{
    position:fixed;top:var(--topbar-h);left:0;bottom:0;z-index:95;
    transform:translateX(-105%);transition:transform .2s ease;
    width:240px;background:var(--s1);
  }
  .sidebar.open{transform:translateX(0)}
  .env-tabs{max-width:30vw}
  .stats{grid-template-columns:1fr}
  .main{padding:14px}
  .live-pill span{display:none}
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
            <input class="search-input" id="keySearch" placeholder="Find key…" oninput="findKey()" autocomplete="off">
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

(async () => {
  try {
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

    const ps = await api('/api/projects');
    const projects = (ps.projects || []).filter(p => validSlug(p && p.slug));
    const sel = document.getElementById('projectSel');

    if (!projects.length) {
      sel.innerHTML = '<option value="">no projects — run dotsync init</option>';
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
      toast('No project selected — run: dotsync init', 'err');
      return;
    }
    await switchProject(slug);

    const hash = (location.hash || '#secrets').slice(1);
    const navEl = document.querySelector('.nav-item[data-page="' + hash + '"]');
    if (navEl) nav(navEl, hash);
  } catch(e) { toast('Connection failed: ' + e.message, 'err'); }
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
  S.project = slug;
  await loadProject();
  connectSSE();
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
  ed.focus();
  ed.setSelectionRange(idx, idx + qstr.length);
  const pre = text.slice(0, idx);
  const line = pre.split('\n').length;
  const lineH = 1.75 * 12.5;
  ed.scrollTop = Math.max(0, (line - 3) * lineH);
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
  try {
    const r = await post('/api/rollback', { slug:S.project, env:S.env, version });
    toast('v' + version + ' restored as v' + r.version, 'ok');
    loadHistory();
    await pullSecrets(false);
  } catch(e) { toast(e.message, 'err'); }
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
  try {
    await post('/api/team/add', { slug:S.project, username:u, role:r });
    document.getElementById('memberInput').value = '';
    toast('@' + u + ' added as ' + r, 'ok');
    await loadProject();
  } catch(e) { toast(e.message, 'err'); }
}

async function removeMember(u) {
  if (!validSlug(S.project)) { toast('No project selected', 'err'); return; }
  if (!confirm('Remove @' + u + '?')) return;
  try {
    await post('/api/team/remove', { slug:S.project, username:u });
    toast('@' + u + ' removed', 'ok');
    await loadProject();
  } catch(e) { toast(e.message, 'err'); }
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
  try {
    const r = await post('/api/tokens/create', { slug:S.project, env, name });
    document.getElementById('tokenName').value = '';
    const rev = document.getElementById('tokenReveal');
    rev.innerHTML = '<div style="font-size:11px;color:var(--yellow);margin-bottom:4px;font-family:var(--mono)">⚠ Copy now — shown once only</div>' +
      '<div class="token-reveal" onclick="navigator.clipboard.writeText(\'' + r.token + '\').then(()=>toast(\'Copied!\',\'ok\'))">' + r.token + '</div>';
    toast('Token created — copy it now!', 'ok');
    await loadProject();
  } catch(e) { toast(e.message, 'err'); }
}

async function revokeToken(id, name) {
  if (!validSlug(S.project)) { toast('No project selected', 'err'); return; }
  if (!confirm('Revoke "' + name + '"? Cannot be undone.')) return;
  try {
    await post('/api/tokens/revoke', { slug:S.project, token_id:id });
    toast('Token revoked', 'ok');
    await loadProject();
  } catch(e) { toast(e.message, 'err'); }
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
  try {
    const d = await api('/api/project/?slug=' + encodeURIComponent(S.project));
    S.data = d;
    renderAudit(d.logs || []);
    toast('Audit refreshed', 'ok');
  } catch(e) { toast(e.message, 'err'); }
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
