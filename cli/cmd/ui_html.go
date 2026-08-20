package cmd

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>DotSync</title>
<style>
:root {
  --bg:      #0C0C10;
  --s1:      #111116;
  --s2:      #18181F;
  --s3:      #21212A;
  --border:  rgba(255,255,255,0.07);
  --accent:  #00D4FF;
  --purple:  #8B5CF6;
  --green:   #00E887;
  --yellow:  #FFD060;
  --red:     #FF4444;
  --text:    #E8E8F0;
  --muted:   #606070;
  --mono:    'JetBrains Mono','Fira Code','Cascadia Code',ui-monospace,monospace;
  --sans:    -apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;
}
*{box-sizing:border-box;margin:0;padding:0}
html,body{height:100%;background:var(--bg);color:var(--text);font-family:var(--sans);font-size:13px;line-height:1.5}

/* ── scrollbar ── */
::-webkit-scrollbar{width:5px;height:5px}
::-webkit-scrollbar-track{background:transparent}
::-webkit-scrollbar-thumb{background:var(--border);border-radius:3px}

/* ── topbar ── */
.topbar{
  display:flex;align-items:center;gap:0;
  height:48px;border-bottom:1px solid var(--border);
  background:var(--s1);position:fixed;top:0;left:0;right:0;z-index:100;
  padding:0 20px;
}
.logo{font-family:var(--mono);font-weight:700;font-size:14px;color:var(--accent);letter-spacing:-.5px;margin-right:24px}
.logo em{color:var(--purple);font-style:normal}
.topbar-sep{width:1px;height:20px;background:var(--border);margin:0 16px}
.project-select{
  background:transparent;border:none;color:var(--text);
  font-family:var(--mono);font-size:13px;cursor:pointer;outline:none;
  padding:2px 4px;
}
.project-select option{background:var(--s2)}
.env-tabs{display:flex;gap:2px;margin-left:16px}
.env-tab{
  padding:3px 10px;border-radius:5px;cursor:pointer;
  font-family:var(--mono);font-size:11px;color:var(--muted);
  transition:.12s;border:1px solid transparent;
}
.env-tab:hover{color:var(--text);background:var(--s2)}
.env-tab.active{color:var(--accent);background:rgba(0,212,255,.08);border-color:rgba(0,212,255,.2)}
.topbar-right{margin-left:auto;display:flex;align-items:center;gap:10px}
.user-chip{display:flex;align-items:center;gap:7px;font-family:var(--mono);font-size:12px;color:var(--muted)}
.avatar{width:26px;height:26px;border-radius:50%;background:var(--purple);display:flex;align-items:center;justify-content:center;font-size:11px;font-weight:700;color:#fff;flex-shrink:0}

/* ── layout ── */
.shell{display:flex;height:calc(100vh - 48px);margin-top:48px;overflow:hidden}

/* ── sidebar ── */
.sidebar{
  width:192px;flex-shrink:0;
  border-right:1px solid var(--border);
  background:var(--s1);
  padding:12px 8px;
  overflow-y:auto;
}
.nav-section{margin-bottom:16px}
.nav-section-label{
  font-size:10px;font-weight:600;text-transform:uppercase;letter-spacing:1px;
  color:var(--muted);padding:0 8px 6px;
}
.nav-item{
  display:flex;align-items:center;gap:9px;
  padding:6px 8px;border-radius:6px;cursor:pointer;
  color:var(--muted);transition:.1s;
  font-size:13px;user-select:none;
}
.nav-item:hover{color:var(--text);background:var(--s2)}
.nav-item.active{color:var(--accent);background:rgba(0,212,255,.07)}
.nav-icon{font-size:13px;width:16px;text-align:center;flex-shrink:0}

/* ── main ── */
.main{flex:1;overflow-y:auto;padding:20px;display:flex;flex-direction:column;gap:14px}

/* ── page ── */
.page{display:none;flex-direction:column;gap:14px}
.page.active{display:flex}

/* ── stats ── */
.stats{display:grid;grid-template-columns:repeat(3,1fr);gap:10px}
.stat{
  background:var(--s1);border:1px solid var(--border);border-radius:9px;
  padding:14px 16px;
}
.stat-label{font-size:10px;text-transform:uppercase;letter-spacing:.8px;color:var(--muted);margin-bottom:6px}
.stat-value{font-family:var(--mono);font-size:20px;font-weight:700;line-height:1}
.stat-sub{font-family:var(--mono);font-size:11px;color:var(--muted);margin-top:4px}

/* ── card ── */
.card{background:var(--s1);border:1px solid var(--border);border-radius:9px;overflow:hidden}
.card-head{
  display:flex;align-items:center;justify-content:space-between;
  padding:12px 16px;border-bottom:1px solid var(--border);
}
.card-title{font-size:12px;font-weight:600;color:var(--text);display:flex;align-items:center;gap:8px}
.card-actions{display:flex;gap:6px}
.card-body{padding:16px}

/* ── buttons ── */
.btn{
  display:inline-flex;align-items:center;gap:5px;
  padding:5px 12px;border-radius:6px;font-size:12px;font-weight:500;
  cursor:pointer;border:none;transition:.12s;
  font-family:var(--mono);white-space:nowrap;line-height:1.4;
}
.btn-primary{background:var(--accent);color:#000}
.btn-primary:hover{filter:brightness(1.1)}
.btn-ghost{background:transparent;color:var(--muted);border:1px solid var(--border)}
.btn-ghost:hover{border-color:var(--accent);color:var(--accent)}
.btn-green{background:var(--green);color:#000}
.btn-green:hover{filter:brightness(1.05)}
.btn-red{background:transparent;color:var(--red);border:1px solid rgba(255,68,68,.25)}
.btn-red:hover{background:rgba(255,68,68,.08)}
.btn:disabled{opacity:.35;cursor:not-allowed}

/* ── editor ── */
.editor{
  width:100%;min-height:220px;
  background:var(--s2);border:1px solid var(--border);
  color:var(--text);font-family:var(--mono);font-size:12.5px;line-height:1.75;
  padding:14px;border-radius:7px;resize:vertical;outline:none;
  transition:border-color .15s;tab-size:2;
}
.editor:focus{border-color:var(--accent)}
.editor-note{font-size:11px;color:var(--muted);margin-bottom:8px;font-family:var(--mono)}

/* ── input / select ── */
.input,.sel{
  background:var(--s2);border:1px solid var(--border);
  color:var(--text);font-family:var(--mono);font-size:12px;
  padding:6px 10px;border-radius:6px;outline:none;transition:border-color .15s;
}
.input:focus,.sel:focus{border-color:var(--accent)}
.sel{cursor:pointer}
.sel option{background:var(--s2)}
.form-row{display:flex;gap:7px;align-items:center}

/* ── list items ── */
.list-item{
  display:flex;align-items:center;gap:11px;
  padding:9px 0;border-bottom:1px solid var(--border);
}
.list-item:last-child{border:none}

/* ── badges / pills ── */
.pill{
  font-family:var(--mono);font-size:10px;font-weight:600;
  padding:2px 7px;border-radius:99px;
}
.pill-accent{background:rgba(0,212,255,.12);color:var(--accent);border:1px solid rgba(0,212,255,.2)}
.pill-green {background:rgba(0,232,135,.1); color:var(--green); border:1px solid rgba(0,232,135,.2)}
.pill-purple{background:rgba(139,92,246,.12);color:var(--purple);border:1px solid rgba(139,92,246,.2)}
.pill-muted {background:var(--s3);color:var(--muted);border:1px solid var(--border)}
.pill-red   {background:rgba(255,68,68,.1);color:var(--red);border:1px solid rgba(255,68,68,.2)}

/* ── avatar ── */
.member-av{
  width:30px;height:30px;border-radius:50%;
  background:var(--s3);border:1px solid var(--border);
  display:flex;align-items:center;justify-content:center;
  font-size:11px;font-weight:700;color:var(--muted);
  font-family:var(--mono);flex-shrink:0;
}

/* ── audit ── */
.audit-time{font-family:var(--mono);font-size:11px;color:var(--muted);width:64px;flex-shrink:0}
.audit-action{
  font-family:var(--mono);font-size:10px;font-weight:600;
  padding:2px 7px;border-radius:4px;width:56px;text-align:center;flex-shrink:0;
}
.a-push  {background:rgba(0,232,135,.1);color:var(--green)}
.a-pull  {background:rgba(0,212,255,.1);color:var(--accent)}
.a-invite{background:rgba(255,208,96,.1);color:var(--yellow)}
.a-revoke,.a-token_revoke{background:rgba(255,68,68,.1);color:var(--red)}
.a-token_create{background:rgba(139,92,246,.1);color:var(--purple)}
.a-other {background:var(--s3);color:var(--muted)}

/* ── history ── */
.history-item{
  display:flex;align-items:center;gap:11px;
  padding:9px 8px;border-radius:7px;cursor:pointer;
  border-bottom:1px solid var(--border);transition:.1s;
}
.history-item:last-child{border:none}
.history-item:hover{background:var(--s2)}
.ver-badge{
  font-family:var(--mono);font-size:11px;font-weight:700;
  padding:2px 8px;border-radius:4px;width:38px;text-align:center;flex-shrink:0;
}
.ver-current{background:rgba(0,232,135,.12);color:var(--green);border:1px solid rgba(0,232,135,.25)}
.ver-old    {background:var(--s3);color:var(--muted);border:1px solid var(--border)}

/* ── token reveal ── */
.token-reveal{
  font-family:var(--mono);font-size:11.5px;word-break:break-all;
  background:rgba(0,232,135,.05);border:1px solid rgba(0,232,135,.2);
  color:var(--green);padding:12px;border-radius:7px;margin-top:10px;
  position:relative;cursor:pointer;
}
.token-reveal::after{
  content:'click to copy';position:absolute;right:10px;top:50%;transform:translateY(-50%);
  font-size:10px;color:rgba(0,232,135,.5);
}

/* ── empty ── */
.empty{text-align:center;padding:36px 20px;color:var(--muted);font-size:12px}
.empty-ico{font-size:28px;margin-bottom:10px}

/* ── toasts ── */
.toasts{position:fixed;bottom:20px;right:20px;display:flex;flex-direction:column;gap:7px;z-index:999}
.toast{
  display:flex;align-items:center;gap:9px;padding:10px 14px;
  border-radius:8px;font-size:12px;font-family:var(--mono);
  background:var(--s2);border:1px solid var(--border);
  box-shadow:0 8px 24px rgba(0,0,0,.5);max-width:340px;
  animation:tIn .18s ease;
}
.t-ok  {border-color:rgba(0,232,135,.3);color:var(--green)}
.t-err {border-color:rgba(255,68,68,.3); color:var(--red)}
.t-info{border-color:rgba(0,212,255,.3); color:var(--accent)}
@keyframes tIn{from{transform:translateX(16px);opacity:0}to{transform:translateX(0);opacity:1}}

/* ── spinner ── */
.spin{width:12px;height:12px;border:2px solid var(--border);border-top-color:var(--accent);border-radius:50%;animation:rot .65s linear infinite;display:inline-block;flex-shrink:0}
@keyframes rot{to{transform:rotate(360deg)}}

/* ── divider ── */
.divider{height:1px;background:var(--border);margin:8px 0}
</style>
</head>
<body>

<div class="topbar">
  <div class="logo">dot<em>sync</em></div>
  <div class="topbar-sep"></div>
  <select class="project-select" id="projectSel" onchange="switchProject(this.value)">
    <option>loading...</option>
  </select>
  <div class="env-tabs" id="envTabs"></div>
  <div class="topbar-right">
    <div class="user-chip">
      <div class="avatar" id="avatarEl">?</div>
      <span id="usernameEl">...</span>
    </div>
  </div>
</div>

<div class="shell">
  <nav class="sidebar">
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
        <span class="nav-icon">⬡</span>Team
      </div>
      <div class="nav-item" data-page="tokens" onclick="nav(this,'tokens')">
        <span class="nav-icon">⬡</span>Tokens
      </div>
      <div class="nav-item" data-page="audit" onclick="nav(this,'audit')">
        <span class="nav-icon">⬡</span>Audit Log
      </div>
    </div>
  </nav>

  <main class="main">

    <!-- Secrets -->
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
          <div class="stat-sub">encrypted</div>
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
          </div>
          <div class="card-actions">
            <button class="btn btn-ghost" id="pullBtn" onclick="doPull()">↓ Pull</button>
            <button class="btn btn-green" id="pushBtn" onclick="doPush()">↑ Push</button>
          </div>
        </div>
        <div class="card-body">
          <div class="editor-note">Decrypted locally — server never sees values. Edit then push to share.</div>
          <textarea class="editor" id="editor"
            placeholder="DATABASE_URL=postgres://...&#10;API_KEY=sk-live-...&#10;NODE_ENV=production"
            oninput="countKeys()" spellcheck="false"></textarea>
        </div>
      </div>
    </div>

    <!-- History -->
    <div class="page" id="page-history">
      <div class="card">
        <div class="card-head">
          <div class="card-title">Version History</div>
          <div class="card-actions">
            <button class="btn btn-ghost" onclick="loadHistory()">↻ Refresh</button>
          </div>
        </div>
        <div class="card-body" id="historyBody">
          <div class="empty"><div class="empty-ico">◷</div>Loading...</div>
        </div>
      </div>
    </div>

    <!-- Team -->
    <div class="page" id="page-team">
      <div class="card">
        <div class="card-head"><div class="card-title">Team Members</div></div>
        <div class="card-body">
          <div class="form-row" style="margin-bottom:14px">
            <input class="input" id="memberInput" placeholder="github-username" style="flex:1">
            <select class="sel" id="memberRole" style="width:100px">
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

    <!-- Tokens -->
    <div class="page" id="page-tokens">
      <div class="card">
        <div class="card-head"><div class="card-title">Service Tokens</div></div>
        <div class="card-body">
          <div class="form-row" style="margin-bottom:14px">
            <input class="input" id="tokenName" placeholder="name (e.g. github-prod)" style="flex:1">
            <select class="sel" id="tokenEnv" style="width:120px">
              <option value="*">all envs (*)</option>
            </select>
            <button class="btn btn-primary" onclick="createToken()">+ Create</button>
          </div>
          <div id="tokenReveal"></div>
          <div id="tokenBody"></div>
        </div>
      </div>
    </div>

    <!-- Audit -->
    <div class="page" id="page-audit">
      <div class="card">
        <div class="card-head">
          <div class="card-title">Audit Log</div>
          <div class="card-actions">
            <button class="btn btn-ghost" onclick="loadProject()">↻ Refresh</button>
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
// ── state ─────────────────────────────────────────────────────────────────────
const S = { project: null, env: 'dev', me: null, data: null };

// ── boot ──────────────────────────────────────────────────────────────────────
(async () => {
  try {
    const me = await api('/api/me');
    S.me = me;
    const u = me.user || {};
    document.getElementById('usernameEl').textContent = '@' + (u.username || '?');
    document.getElementById('avatarEl').textContent = (u.username || '?')[0].toUpperCase();

    const ps = await api('/api/projects');
    const projects = ps.projects || [];
    const sel = document.getElementById('projectSel');
    sel.innerHTML = projects.map(p => '<option value="' + esc(p.slug) + '">' + esc(p.slug) + '</option>').join('');

    const defSlug = me.project && me.project.project_slug;
    const defEnv  = me.project && me.project.default_env;
    if (defSlug) sel.value = defSlug;
    if (defEnv)  S.env = defEnv;

    await switchProject(sel.value);
  } catch(e) { toast('Connection failed: ' + e.message, 'err'); }
})();

async function switchProject(slug) {
  if (!slug) return;
  S.project = slug;
  await loadProject();
}

async function loadProject() {
  if (!S.project) return;
  const d = await api('/api/project/?slug=' + S.project);
  S.data = d;

  // env tabs
  const envs = d.envs && d.envs.length ? d.envs : ['dev','staging','production'];
  document.getElementById('envTabs').innerHTML = envs.map(e =>
    '<div class="env-tab' + (e===S.env?' active':'') + '" onclick="switchEnv(\'' + e + '\')">' + e + '</div>'
  ).join('');

  // token env selector
  document.getElementById('tokenEnv').innerHTML =
    '<option value="*">all envs (*)</option>' +
    envs.map(e => '<option value="' + e + '"' + (e===S.env?' selected':'') + '>' + e + '</option>').join('');

  renderMembers(d.members || []);
  renderTokens(d.tokens || []);
  renderAudit(d.logs || []);
  document.getElementById('sEnv').textContent = S.env;

  await pullSecrets();
}

function switchEnv(env) {
  S.env = env;
  document.querySelectorAll('.env-tab').forEach(t => t.classList.toggle('active', t.textContent===env));
  document.getElementById('sEnv').textContent = env;
  pullSecrets();
  if (document.getElementById('page-history').classList.contains('active')) loadHistory();
}

// ── secrets ───────────────────────────────────────────────────────────────────
async function pullSecrets() {
  try {
    const r = await api('/api/pull?slug=' + S.project + '&env=' + S.env);
    document.getElementById('editor').value = r.content || '';
    document.getElementById('sVer').textContent = 'v' + r.version;
    document.getElementById('sBy').textContent = r.by ? '@' + r.by : '';
    countKeys();
  } catch(_) {
    document.getElementById('sVer').textContent = 'none';
    document.getElementById('sBy').textContent = 'no push yet';
  }
}

function countKeys() {
  const lines = (document.getElementById('editor').value || '').split('\n');
  const n = lines.filter(l => l.trim() && !l.trim().startsWith('#') && l.includes('=')).length;
  document.getElementById('keyCountBadge').textContent = n + ' key' + (n!==1?'s':'');
  document.getElementById('sKeys').textContent = n;
}

async function doPull() {
  const btn = document.getElementById('pullBtn');
  btn.disabled = true; btn.innerHTML = '<span class="spin"></span> Pulling';
  try {
    const r = await api('/api/pull?slug=' + S.project + '&env=' + S.env);
    document.getElementById('editor').value = r.content || '';
    document.getElementById('sVer').textContent = 'v' + r.version;
    document.getElementById('sBy').textContent = r.by ? '@' + r.by : '';
    countKeys();
    toast('Pulled v' + r.version + ' — ' + r.keys + ' secrets', 'ok');
  } catch(e) { toast(e.message, 'err'); }
  finally { btn.disabled=false; btn.innerHTML='↓ Pull'; }
}

async function doPush() {
  const content = document.getElementById('editor').value.trim();
  if (!content) { toast('Editor is empty', 'err'); return; }
  const btn = document.getElementById('pushBtn');
  btn.disabled = true; btn.innerHTML = '<span class="spin"></span> Pushing';
  try {
    const r = await post('/api/push', { slug: S.project, env: S.env, content });
    document.getElementById('sVer').textContent = 'v' + r.version;
    countKeys();
    toast('Pushed v' + r.version + ' — ' + r.keys + ' keys encrypted', 'ok');
    loadProject();
  } catch(e) { toast(e.message, 'err'); }
  finally { btn.disabled=false; btn.innerHTML='↑ Push'; }
}

// ── history ───────────────────────────────────────────────────────────────────
async function loadHistory() {
  const el = document.getElementById('historyBody');
  el.innerHTML = '<div class="empty"><span class="spin"></span></div>';
  try {
    const r = await api('/api/history?slug=' + S.project + '&env=' + S.env);
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
  if (!confirm('Restore v' + version + ' as new current version?')) return;
  try {
    const r = await post('/api/rollback', { slug:S.project, env:S.env, version });
    toast('v' + version + ' restored as v' + r.version, 'ok');
    loadHistory(); pullSecrets();
  } catch(e) { toast(e.message, 'err'); }
}

// ── team ──────────────────────────────────────────────────────────────────────
function renderMembers(members) {
  const el = document.getElementById('memberBody');
  if (!members.length) { el.innerHTML = '<div class="empty"><div class="empty-ico">⬡</div>No members yet</div>'; return; }
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
  const u = document.getElementById('memberInput').value.trim().replace('@','');
  const r = document.getElementById('memberRole').value;
  if (!u) { toast('Enter a username', 'err'); return; }
  try {
    await post('/api/team/add', { slug:S.project, username:u, role:r });
    document.getElementById('memberInput').value = '';
    toast('@' + u + ' added as ' + r, 'ok');
    loadProject();
  } catch(e) { toast(e.message, 'err'); }
}

async function removeMember(u) {
  if (!confirm('Remove @' + u + '?')) return;
  try {
    await post('/api/team/remove', { slug:S.project, username:u });
    toast('@' + u + ' removed', 'ok');
    loadProject();
  } catch(e) { toast(e.message, 'err'); }
}

// ── tokens ────────────────────────────────────────────────────────────────────
function renderTokens(tokens) {
  const el = document.getElementById('tokenBody');
  if (!tokens.length) { el.innerHTML = '<div class="empty" style="padding-top:12px"><div class="empty-ico">⬡</div>No tokens. Create one for CI/CD.</div>'; return; }
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
    loadProject();
  } catch(e) { toast(e.message, 'err'); }
}

async function revokeToken(id, name) {
  if (!confirm('Revoke "' + name + '"? Cannot be undone.')) return;
  try {
    await post('/api/tokens/revoke', { slug:S.project, token_id:id });
    toast('Token revoked', 'ok');
    loadProject();
  } catch(e) { toast(e.message, 'err'); }
}

// ── audit ─────────────────────────────────────────────────────────────────────
function renderAudit(logs) {
  const el = document.getElementById('auditBody');
  if (!logs.length) { el.innerHTML = '<div class="empty"><div class="empty-ico">◷</div>No events yet</div>'; return; }
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

// ── nav ───────────────────────────────────────────────────────────────────────
function nav(el, page) {
  document.querySelectorAll('.nav-item').forEach(n => n.classList.remove('active'));
  el.classList.add('active');
  document.querySelectorAll('.page').forEach(p => p.classList.remove('active'));
  document.getElementById('page-' + page).classList.add('active');
  if (page==='history') loadHistory();
}

// ── utils ─────────────────────────────────────────────────────────────────────
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
  el.innerHTML = (type==='ok'?'✓':type==='err'?'✗':'→') + ' ' + msg;
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
</script>
</body>
</html>`
