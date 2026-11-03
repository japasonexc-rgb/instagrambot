// Instagram Marka Hesap Paneli — vanilla JS SPA (no build step).
// Talks to the Go backend's REST API (/api/*) and WebSocket (/api/ws).

const app = document.getElementById('app');

const state = {
  token: localStorage.getItem('token') || null,
  initialized: null,
  accounts: [],
  activeAccountId: null,
  media: [],
  mediaLoaded: false,
  activeMediaId: null,
  comments: [],
  commentFilter: { search: '' },
  selectedCommentIds: new Set(),
  jobs: [],
  activeJobId: null,
  jobLogs: [],
  view: 'account', // 'account' | 'bulk'
  bulk: { file: null, previewUrl: null, uploaded: null, caption: '', selected: new Set(), delaySec: 30 },
  ws: null,
};

const inputCls = 'bg-slate-800 rounded-lg px-3 py-2 outline-none focus:ring-2 ring-pink-500';
const btnCls = 'bg-slate-800 hover:bg-slate-700 rounded-lg px-3 py-1.5 text-sm disabled:opacity-50';
const primaryBtnCls = 'bg-pink-600 hover:bg-pink-500 rounded-lg px-3 py-1.5 text-sm font-medium disabled:opacity-50';

// ---------- API helpers ----------

async function api(path, opts = {}) {
  const headers = Object.assign({ 'Content-Type': 'application/json' }, opts.headers || {});
  if (state.token) headers['Authorization'] = 'Bearer ' + state.token;
  const res = await fetch('/api' + path, Object.assign({}, opts, { headers }));
  if (res.status === 401 && state.token && !path.startsWith('/auth/')) { logout(); throw new Error('Oturum süresi doldu'); }
  if (res.status === 204) return null;
  const data = await res.json().catch(() => null);
  if (!res.ok) throw new Error((data && data.error) || `HTTP ${res.status}`);
  return data;
}

function logout() {
  state.token = null;
  localStorage.removeItem('token');
  if (state.ws) { state.ws.onclose = null; state.ws.close(); state.ws = null; }
  render();
}

// ---------- WebSocket ----------

function connectWS() {
  if (state.ws || !state.token) return;
  const proto = location.protocol === 'https:' ? 'wss' : 'ws';
  const ws = new WebSocket(`${proto}://${location.host}/api/ws?token=${encodeURIComponent(state.token)}`);
  ws.onmessage = (ev) => {
    try {
      const msg = JSON.parse(ev.data);
      if (msg.type === 'job_update') {
        const job = msg.payload;
        const inView = state.view === 'bulk' ? job.type === 'bulk_publish' : job.account_id === state.activeAccountId;
        if (!inView) return;
        const idx = state.jobs.findIndex(j => j.id === job.id);
        const prev = idx >= 0 ? state.jobs[idx] : null;
        if (idx >= 0) state.jobs[idx] = job; else state.jobs.unshift(job);
        renderJobsList();
        // A finished moderation job changes the comment list; reload it.
        if (prev && prev.status !== job.status && job.status !== 'running' && job.type === 'moderate_comments' && job.media_id === state.activeMediaId) {
          refreshComments();
        }
      } else if (msg.type === 'job_log' && msg.job_id === state.activeJobId) {
        state.jobLogs.unshift(msg.payload);
        renderJobLogs();
      }
    } catch (e) { /* ignore malformed frames */ }
  };
  ws.onclose = () => { state.ws = null; setTimeout(connectWS, 3000); };
  state.ws = ws;
}

// ---------- Bootstrap / routing ----------

async function boot() {
  try {
    const s = await api('/setup/status');
    state.initialized = s.initialized;
  } catch (e) {
    state.initialized = true;
  }
  if (!state.initialized) return renderSetup();
  if (!state.token) return renderLogin();
  try { await loadAccounts(); } catch (e) { return; }
  connectWS();
  render();
}

function render() {
  if (!state.token) return renderLogin();
  renderDashboard();
}

// ---------- Setup / Login ----------

function authScreen(id, title, subtitle, fields, submitLabel) {
  app.innerHTML = `
    <div class="min-h-screen flex items-center justify-center px-4">
      <div class="w-full max-w-sm bg-slate-900 border border-slate-800 rounded-xl p-8 shadow-xl">
        <div class="ig-ring w-12 h-12 mb-4"><div class="w-full h-full rounded-full bg-slate-900 flex items-center justify-center font-bold">IG</div></div>
        <h1 class="text-xl font-semibold mb-1">${title}</h1>
        <p class="text-slate-400 text-sm mb-6">${subtitle}</p>
        <form id="${id}-form" class="space-y-3">
          ${fields}
          <button class="w-full bg-pink-600 hover:bg-pink-500 transition rounded-lg py-2 font-medium">${submitLabel}</button>
          <p id="${id}-error" class="text-rose-400 text-sm"></p>
        </form>
      </div>
    </div>`;
}

async function afterAuth(token) {
  state.token = token;
  localStorage.setItem('token', token);
  state.initialized = true;
  await loadAccounts();
  connectWS();
  render();
}

function renderSetup() {
  authScreen('setup', 'İlk Kurulum', 'Panel için bir yönetici hesabı oluşturun.', `
    <input name="username" required minlength="3" placeholder="Kullanıcı adı" class="w-full ${inputCls}" />
    <input name="password" type="password" required minlength="8" placeholder="Parola (min 8 karakter)" class="w-full ${inputCls}" />`, 'Oluştur');
  document.getElementById('setup-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    try {
      const res = await api('/setup/init', { method: 'POST', body: JSON.stringify(Object.fromEntries(new FormData(e.target))) });
      await afterAuth(res.token);
    } catch (err) {
      document.getElementById('setup-error').textContent = err.message;
    }
  });
}

function renderLogin() {
  authScreen('login', 'Panele Giriş', 'Yönetici kimlik bilgilerinizle giriş yapın.', `
    <input name="username" required placeholder="Kullanıcı adı" class="w-full ${inputCls}" />
    <input name="password" type="password" required placeholder="Parola" class="w-full ${inputCls}" />`, 'Giriş Yap');
  document.getElementById('login-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    try {
      const res = await api('/auth/login', { method: 'POST', body: JSON.stringify(Object.fromEntries(new FormData(e.target))) });
      await afterAuth(res.token);
    } catch (err) {
      document.getElementById('login-error').textContent = err.message;
    }
  });
}

// ---------- Data loaders ----------

async function loadAccounts() {
  state.accounts = await api('/accounts/');
  // The Instagram login window may come back to "/?account=<id>" to select the new account.
  const fromLogin = Number(new URLSearchParams(location.search).get('account'));
  if (fromLogin) {
    state.activeAccountId = fromLogin;
    history.replaceState(null, '', location.pathname);
  }
  if (!state.accounts.some(a => a.id === state.activeAccountId)) {
    state.activeAccountId = state.accounts.length ? state.accounts[0].id : null;
  }
}

function upsertAccount(acc) {
  const idx = state.accounts.findIndex(a => a.id === acc.id);
  if (idx >= 0) state.accounts[idx] = acc; else state.accounts.push(acc);
}

async function loadJobs() {
  if (state.view === 'bulk') { state.jobs = await api('/bulk-publish/jobs'); return; }
  if (!state.activeAccountId) { state.jobs = []; return; }
  state.jobs = await api(`/accounts/${state.activeAccountId}/jobs`);
}

async function loadJobLogs(jobId) {
  state.jobLogs = await api(`/jobs/${jobId}/logs?limit=1000`);
}

function resetAccountView() {
  state.media = [];
  state.mediaLoaded = false;
  state.activeMediaId = null;
  state.comments = [];
  state.selectedCommentIds.clear();
  state.activeJobId = null;
  state.jobLogs = [];
}

// ---------- Dashboard shell ----------

async function renderDashboard() {
  await loadJobs().catch(() => {});
  app.innerHTML = `
    <div class="flex h-screen">
      <aside class="w-80 bg-slate-900 border-r border-slate-800 flex flex-col">
        <div class="p-4 border-b border-slate-800 flex items-center justify-between">
          <div class="flex items-center gap-2">
            <div class="ig-ring w-7 h-7"><div class="w-full h-full rounded-full bg-slate-900 flex items-center justify-center text-[10px] font-bold">IG</div></div>
            <h1 class="font-semibold">Marka Hesapları</h1>
          </div>
          <button id="logout-btn" class="text-slate-400 hover:text-rose-400 text-sm">Çıkış</button>
        </div>
        <div class="p-3 border-b border-slate-800">
          <button id="bulk-btn" class="w-full rounded-lg py-2.5 text-sm font-semibold"></button>
        </div>
        <div id="account-list" class="flex-1 overflow-y-auto p-3 space-y-2"></div>
        <div class="p-3 border-t border-slate-800 space-y-1">
          <button id="add-account-btn" class="w-full bg-pink-600 hover:bg-pink-500 rounded-lg py-2 text-sm font-medium">+ Instagram Hesabı Ekle</button>
        </div>
      </aside>
      <main class="flex-1 overflow-y-auto p-6" id="main-panel"></main>
    </div>`;
  document.getElementById('logout-btn').addEventListener('click', logout);
  document.getElementById('add-account-btn').addEventListener('click', () => showLoginModal());
  document.getElementById('bulk-btn').addEventListener('click', () => openBulk());
  renderAccountList();
  renderMainPanel();
}

function avatar(acc, size) {
  const initial = escapeHtml((acc.username || acc.label || '?').charAt(0).toUpperCase());
  const img = acc.profile_picture_url
    ? `<img src="${escapeHtml(acc.profile_picture_url)}" referrerpolicy="no-referrer" class="absolute inset-0 w-full h-full object-cover" onerror="this.remove()" />`
    : '';
  return `<div class="ig-ring shrink-0" style="width:${size}px;height:${size}px"><div class="relative w-full h-full rounded-full bg-slate-800 flex items-center justify-center font-semibold overflow-hidden">${initial}${img}</div></div>`;
}

function accountStatusBadge(acc) {
  return ({
    active: '<span class="badge bg-emerald-500/20 text-emerald-400">aktif</span>',
    error: '<span class="badge bg-rose-500/20 text-rose-400">hata</span>',
  })[acc.status] || '<span class="badge bg-amber-500/20 text-amber-400">doğrulanmadı</span>';
}

function proxyHost(acc) {
  if (!acc.proxy) return '';
  return acc.proxy.replace(/^[a-z0-9]+:\/\//, '').replace(/^.*@/, '');
}

function renderAccountList() {
  const el = document.getElementById('account-list');
  if (!el) return;
  const bulkBtn = document.getElementById('bulk-btn');
  if (bulkBtn) {
    bulkBtn.textContent = 'Toplu Paylaşım';
    bulkBtn.className = 'w-full rounded-lg py-2.5 text-sm font-semibold ' + (state.view === 'bulk' ? 'ig-gradient' : 'bg-slate-800 hover:bg-slate-700');
  }
  el.innerHTML = state.accounts.map(a => {
    const active = state.view === 'account' && a.id === state.activeAccountId;
    return `
      <button data-id="${a.id}" class="account-item w-full text-left px-3 py-2 rounded-lg flex items-center gap-3 ${active ? 'bg-pink-600/15 border border-pink-600' : 'bg-slate-800/60 border border-transparent hover:border-slate-700'}">
        ${avatar(a, 36)}
        <div class="min-w-0 flex-1">
          <div class="flex items-center justify-between gap-2">
            <span class="font-medium truncate">${escapeHtml(a.label)}</span>
            ${accountStatusBadge(a)}
          </div>
          <div class="text-xs text-slate-400 truncate">${a.username ? '@' + escapeHtml(a.username) : 'kullanıcı adı bilinmiyor'}</div>
          <div class="text-xs truncate ${a.proxy ? 'text-sky-400' : 'text-slate-500'}">${a.proxy ? 'özel IP: ' + escapeHtml(a.proxy_last_ip || proxyHost(a)) : 'özel IP yok · sunucu IP\'si'}</div>
        </div>
      </button>`;
  }).join('') || `<p class="text-slate-500 text-sm p-2">Henüz Instagram hesabı eklenmedi.</p>`;
  el.querySelectorAll('.account-item').forEach(btn => {
    btn.addEventListener('click', async () => {
      const id = Number(btn.dataset.id);
      if (state.view === 'account' && id === state.activeAccountId) return;
      state.view = 'account';
      state.activeAccountId = id;
      resetAccountView();
      renderAccountList();
      await loadJobs().catch(() => {});
      renderMainPanel();
    });
  });
}

const proxyHelp = `Her hesabın Instagram'a farklı bir IP adresinden görünmesi için proxy sağlayıcınızdan aldığınız IP bilgisini girin, örn. <code>185.12.34.56:8000:kullanıcı:parola</code>, <code>socks5://kullanıcı:parola@185.12.34.56:1080</code> veya <code>http://185.12.34.56:8000</code>. Boş bırakılırsa hesap sunucunun kendi IP'sini kullanır.`;

async function selectAccountAfterConnect(accountId) {
  await loadAccounts();
  state.view = 'account';
  if (accountId) state.activeAccountId = accountId;
  resetAccountView();
  closeModal();
  await loadJobs().catch(() => {});
  renderAccountList();
  renderMainPanel();
}

// showLoginModal adds an account (no argument) or logs an existing account
// in again: username + password (+ optional IP), then a code step if
// Instagram asks for one.
function showLoginModal(acc) {
  const relogin = Boolean(acc);
  openModal(`
    <h2 class="text-lg font-semibold mb-4">${relogin ? 'Yeniden Giriş — ' + escapeHtml(acc.label) : 'Instagram Hesabı Ekle'}</h2>
    <form id="login-form" class="space-y-3" autocomplete="off">
      ${relogin ? '' : `
      <label class="block text-sm text-slate-300">Etiket <span class="text-slate-500">(opsiyonel)</span>
        <input name="label" placeholder="örn. Marka TR" class="mt-1 w-full ${inputCls}" />
      </label>`}
      <label class="block text-sm text-slate-300">Instagram kullanıcı adı
        <input name="username" required ${relogin ? 'readonly' : ''} value="${relogin ? escapeHtml(acc.username) : ''}" placeholder="kullaniciadi" autocapitalize="none" spellcheck="false" class="mt-1 w-full ${inputCls} ${relogin ? 'opacity-60' : ''}" />
      </label>
      <label class="block text-sm text-slate-300">Şifre
        <input name="password" type="password" ${relogin ? '' : 'required'} autocomplete="new-password" placeholder="${relogin ? 'Boş bırakırsanız kayıtlı şifre kullanılır' : ''}" class="mt-1 w-full ${inputCls}" />
      </label>
      ${relogin ? '' : `
      <label class="block text-sm text-slate-300">Özel IP (proxy) <span class="text-slate-500">(opsiyonel)</span>
        <input name="proxy_url" placeholder="ip:port:kullanıcı:parola" spellcheck="false" class="mt-1 w-full ${inputCls} font-mono text-sm" />
      </label>
      <p class="text-xs text-slate-500">${proxyHelp}</p>`}
      <button id="login-submit" class="w-full ig-gradient rounded-lg py-2.5 font-semibold disabled:opacity-50">Giriş Yap</button>
    </form>
    <form id="code-form" class="space-y-3 hidden" autocomplete="off">
      <p id="code-message" class="text-sm text-slate-300"></p>
      <input name="code" required inputmode="numeric" autocomplete="one-time-code" placeholder="Doğrulama kodu" class="w-full ${inputCls} text-center tracking-[0.4em] text-lg font-mono" />
      <button id="code-submit" class="w-full ig-gradient rounded-lg py-2.5 font-semibold disabled:opacity-50">Doğrula</button>
    </form>
    <p id="login-status" class="text-sm mt-3"></p>
    <button type="button" id="modal-cancel" class="w-full mt-3 bg-slate-800 hover:bg-slate-700 rounded-lg py-2">Kapat</button>
  `);

  const loginForm = document.getElementById('login-form');
  const codeForm = document.getElementById('code-form');
  const statusEl = document.getElementById('login-status');
  const setStatus = (text, cls) => { statusEl.textContent = text; statusEl.className = 'text-sm mt-3 ' + cls; };
  let loginId = null;

  document.getElementById('modal-cancel').addEventListener('click', closeModal);

  const handle = async (button, busyLabel, request) => {
    const label = button.textContent;
    button.disabled = true; button.textContent = busyLabel;
    setStatus("Instagram'a bağlanılıyor... (10-60 sn sürebilir)", 'text-slate-400');
    try {
      const res = await request();
      if (res.status === 'code_required') {
        loginId = res.login_id;
        loginForm.classList.add('hidden');
        codeForm.classList.remove('hidden');
        codeForm.code.value = '';
        document.getElementById('code-message').textContent = res.message || 'Instagram bir doğrulama kodu istedi.';
        setStatus('', '');
        codeForm.code.focus();
      } else {
        setStatus(relogin ? 'Giriş yapıldı.' : 'Hesap eklendi.', 'text-emerald-400');
        await selectAccountAfterConnect(res.account && res.account.id);
      }
    } catch (err) {
      setStatus(err.message, 'text-rose-400');
    } finally {
      button.disabled = false; button.textContent = label;
    }
  };

  loginForm.addEventListener('submit', (e) => {
    e.preventDefault();
    const body = Object.fromEntries(new FormData(loginForm));
    handle(document.getElementById('login-submit'), 'Giriş yapılıyor...', () => relogin
      ? api(`/accounts/${acc.id}/relogin`, { method: 'POST', body: JSON.stringify({ password: body.password }) })
      : api('/accounts/login', { method: 'POST', body: JSON.stringify(body) }));
  });
  codeForm.addEventListener('submit', (e) => {
    e.preventDefault();
    handle(document.getElementById('code-submit'), 'Doğrulanıyor...', () =>
      api('/accounts/login/code', { method: 'POST', body: JSON.stringify({ login_id: loginId, code: codeForm.code.value }) }));
  });
}

// ---------- Main panel ----------

function activeAccount() {
  return state.accounts.find(a => a.id === state.activeAccountId);
}

function renderMainPanel() {
  const panel = document.getElementById('main-panel');
  if (!panel) return;
  if (state.view === 'bulk') return renderBulkPanel(panel);
  const acc = activeAccount();
  if (!acc) {
    panel.innerHTML = `
      <div class="max-w-lg mx-auto mt-24 text-center">
        <h2 class="text-xl font-semibold mb-2">Marka hesaplarınızı tek yerden yönetin</h2>
        <p class="text-slate-400 mb-6">Kullanıcı adı ve şifreyle Instagram hesabı ekleyin, her hesaba ayrı bir IP atayın, paylaşım yapın ve yorumları yönetin.</p>
        <button id="empty-add" class="${primaryBtnCls} px-5 py-2">+ Instagram Hesabı Ekle</button>
      </div>`;
    document.getElementById('empty-add').addEventListener('click', () => showLoginModal());
    return;
  }
  panel.innerHTML = `
    <div class="space-y-6 max-w-6xl">
      <div id="account-header"></div>
      <div class="grid grid-cols-1 xl:grid-cols-2 gap-6">
        <div id="proxy-section"></div>
        <div id="publish-section"></div>
      </div>
      <div id="media-section"></div>
      <div id="comments-section"></div>
      <div id="jobs-section"></div>
    </div>`;
  renderAccountHeader();
  renderProxySection();
  renderPublishSection();
  renderMediaSection();
  renderCommentsSection();
  renderJobsList();
}

function refreshAccountViews() {
  renderAccountList();
  renderAccountHeader();
  renderProxySection();
}

function renderAccountHeader() {
  const el = document.getElementById('account-header');
  const acc = activeAccount();
  if (!el || !acc) return;
  el.innerHTML = `
    <div class="bg-slate-900 border border-slate-800 rounded-xl p-4">
      <div class="flex flex-wrap items-center gap-4">
        ${avatar(acc, 64)}
        <div class="flex-1 min-w-[220px]">
          <div class="flex flex-wrap items-center gap-2">
            <span class="font-semibold text-lg">${acc.username ? '@' + escapeHtml(acc.username) : escapeHtml(acc.label)}</span>
            ${accountStatusBadge(acc)}
            ${acc.account_type ? `<span class="badge bg-slate-700/60 text-slate-300">${escapeHtml(acc.account_type)}</span>` : ''}
          </div>
          <div class="text-sm text-slate-400">${escapeHtml(acc.label)}${acc.name ? ' · ' + escapeHtml(acc.name) : ''}</div>
          <div class="flex gap-4 text-sm mt-1">
            <span><b>${fmtNum(acc.followers_count)}</b> <span class="text-slate-400">takipçi</span></span>
            <span><b>${fmtNum(acc.follows_count)}</b> <span class="text-slate-400">takip</span></span>
            <span><b>${fmtNum(acc.media_count)}</b> <span class="text-slate-400">gönderi</span></span>
          </div>
          <div class="text-xs text-slate-500 mt-1">
            Son giriş: ${acc.logged_in_at ? fmtDate(acc.logged_in_at) : '—'}
            · Son senkronizasyon: ${acc.last_synced_at ? fmtDate(acc.last_synced_at) : '—'}
          </div>
        </div>
        <div class="flex flex-wrap gap-2">
          <button id="acc-sync" class="${btnCls}">Profili Senkronize Et</button>
          <button id="acc-relogin" class="${btnCls}">Yeniden Giriş Yap</button>
          <button id="acc-delete" class="bg-rose-700/80 hover:bg-rose-600 rounded-lg px-3 py-1.5 text-sm">Hesabı Kaldır</button>
        </div>
      </div>
      ${acc.last_error ? `<div class="mt-3 text-sm text-rose-300 bg-rose-500/10 border border-rose-500/30 rounded-lg px-3 py-2">${escapeHtml(acc.last_error)}</div>` : ''}
      <p id="acc-msg" class="text-sm mt-2"></p>
    </div>`;

  const msg = document.getElementById('acc-msg');
  const withBusy = (id, label, fn) => {
    const btn = document.getElementById(id);
    btn.addEventListener('click', async () => {
      const orig = btn.textContent;
      btn.disabled = true; btn.textContent = label;
      msg.textContent = ''; msg.className = 'text-sm mt-2';
      try { await fn(); } catch (err) { msg.textContent = err.message; msg.className = 'text-sm mt-2 text-rose-400'; }
      finally { btn.disabled = false; btn.textContent = orig; }
    });
  };

  withBusy('acc-sync', 'Senkronize ediliyor...', async () => {
    upsertAccount(await api(`/accounts/${acc.id}/sync`, { method: 'POST' }));
    refreshAccountViews();
  });
  document.getElementById('acc-relogin').addEventListener('click', () => showLoginModal(acc));
  document.getElementById('acc-delete').addEventListener('click', async () => {
    if (!confirm(`"${acc.label}" hesabı panelden kaldırılacak (kayıtlı şifre, oturum, IP ve iş geçmişi silinir; Instagram hesabınıza dokunulmaz). Emin misiniz?`)) return;
    try {
      await api(`/accounts/${acc.id}`, { method: 'DELETE' });
      await loadAccounts();
      resetAccountView();
      await loadJobs().catch(() => {});
      renderAccountList();
      renderMainPanel();
    } catch (err) { msg.textContent = err.message; msg.className = 'text-sm mt-2 text-rose-400'; }
  });
}

// ---------- Proxy ----------

function renderProxySection() {
  const el = document.getElementById('proxy-section');
  const acc = activeAccount();
  if (!el || !acc) return;
  el.innerHTML = `
    <div class="bg-slate-900 border border-slate-800 rounded-xl p-4 h-full space-y-3">
      <div class="flex items-center justify-between">
        <h2 class="font-semibold">Hesaba Özel IP</h2>
        ${acc.proxy ? '<span class="badge bg-sky-500/20 text-sky-400">özel IP aktif</span>' : '<span class="badge bg-slate-600/30 text-slate-400">sunucu IP\'si kullanılıyor</span>'}
      </div>
      <div class="text-sm">
        <div class="text-slate-400">Instagram'ın gördüğü IP: <span class="font-mono text-lg text-slate-100">${acc.proxy_last_ip ? escapeHtml(acc.proxy_last_ip) : '— (test edin)'}</span>${acc.proxy_checked_at ? ` <span class="text-xs text-slate-500">(${fmtDate(acc.proxy_checked_at)})</span>` : ''}</div>
        <div class="text-slate-500 text-xs">Kayıtlı proxy: <span class="font-mono">${acc.proxy ? escapeHtml(acc.proxy) : 'yok'}</span></div>
      </div>
      <form id="proxy-form" class="flex flex-wrap gap-2">
        <input id="proxy-input" placeholder="${acc.proxy ? 'Yeni IP ile değiştir (ip:port:kullanıcı:parola)' : 'ip:port:kullanıcı:parola'}" class="flex-1 min-w-[220px] ${inputCls} py-1.5 font-mono text-sm" />
        <button class="${primaryBtnCls}">IP'yi Kaydet</button>
      </form>
      <div class="flex flex-wrap gap-2">
        <button id="proxy-test" class="${btnCls}">IP'yi Test Et</button>
        ${acc.proxy ? `<button id="proxy-remove" class="${btnCls} text-rose-300">Özel IP'yi Kaldır</button>` : ''}
      </div>
      <p class="text-xs text-slate-500">${proxyHelp}</p>
      <p id="proxy-msg" class="text-sm"></p>
    </div>`;

  const saveProxy = async (value) => {
    const updated = await api(`/accounts/${acc.id}/proxy`, { method: 'PUT', body: JSON.stringify({ proxy_url: value }) });
    upsertAccount(updated);
    refreshAccountViews();
    return updated;
  };

  document.getElementById('proxy-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const value = document.getElementById('proxy-input').value.trim();
    if (!value) return setProxyMsg('IP bilgisini girin (kaldırmak için "Özel IP\'yi Kaldır").', false);
    try {
      await saveProxy(value);
      await testProxy(acc.id);
    } catch (err) { setProxyMsg(err.message, false); }
  });
  document.getElementById('proxy-test').addEventListener('click', () => testProxy(acc.id));
  const removeBtn = document.getElementById('proxy-remove');
  if (removeBtn) removeBtn.addEventListener('click', async () => {
    if (!confirm('Bu hesabın özel IP\'si kaldırılacak ve hesap sunucunun kendi IP\'sini kullanacak. Emin misiniz?')) return;
    try { await saveProxy(''); setProxyMsg('Özel IP kaldırıldı.', true); } catch (err) { setProxyMsg(err.message, false); }
  });
}

function setProxyMsg(text, ok) {
  const msg = document.getElementById('proxy-msg');
  if (!msg) return;
  msg.textContent = text;
  msg.className = 'text-sm ' + (ok ? 'text-emerald-400' : 'text-rose-400');
}

async function testProxy(accountId) {
  setProxyMsg('Test ediliyor...', true);
  try {
    const res = await api(`/accounts/${accountId}/proxy/test`, { method: 'POST' });
    const acc = state.accounts.find(a => a.id === accountId);
    if (acc) { acc.proxy_last_ip = res.ip; acc.proxy_checked_at = new Date().toISOString(); }
    refreshAccountViews();
    setProxyMsg(`Bağlantı başarılı — bu hesap Instagram'a ${res.ip} IP adresinden bağlanıyor.`, true);
  } catch (err) {
    setProxyMsg('IP\'ye bağlanılamadı: ' + err.message, false);
  }
}

// ---------- Publish ----------

function renderPublishSection() {
  const el = document.getElementById('publish-section');
  const acc = activeAccount();
  if (!el || !acc) return;
  el.innerHTML = `
    <div class="bg-slate-900 border border-slate-800 rounded-xl p-4 h-full flex flex-col">
      <h2 class="font-semibold mb-2">Paylaşım</h2>
      <p class="text-sm text-slate-400 flex-1">Gönderiyi bu hesapta ya da aynı anda birden fazla hesapta paylaşın. Hesaplar sırayla, aralarında bekleyerek paylaşılır.</p>
      <button id="open-bulk" class="mt-3 ig-gradient rounded-lg py-2.5 font-semibold">Gönderi Paylaş</button>
    </div>`;
  document.getElementById('open-bulk').addEventListener('click', () => openBulk([acc.id]));
}

// ---------- Bulk publish ----------

// openBulk shows the bulk publish screen, optionally with accounts ticked.
async function openBulk(accountIds) {
  if (accountIds) state.bulk.selected = new Set(accountIds);
  state.view = 'bulk';
  state.activeJobId = null;
  state.jobLogs = [];
  renderAccountList();
  await loadJobs().catch(() => { state.jobs = []; });
  renderMainPanel();
}

function setBulkFile(file) {
  const ok = /\.(jpe?g|png|webp|mp4|mov)$/i.test(file.name);
  if (!ok) { alert('Desteklenen dosyalar: JPG, PNG, WebP görsel veya MP4, MOV video.'); return; }
  if (state.bulk.previewUrl) URL.revokeObjectURL(state.bulk.previewUrl);
  state.bulk.file = file;
  state.bulk.previewUrl = URL.createObjectURL(file);
  state.bulk.uploaded = null;
  renderBulkPreview();
}

function renderBulkPreview() {
  const el = document.getElementById('bulk-preview');
  if (!el) return;
  const f = state.bulk.file;
  if (!f) {
    el.innerHTML = `
      <div class="py-10 text-slate-400">
        <div class="text-3xl mb-2">＋</div>
        <div class="font-medium text-slate-200">Görsel veya video seçin</div>
        <div class="text-xs mt-1">Tıklayın ya da dosyayı buraya sürükleyin · JPG, PNG, WebP, MP4, MOV · en fazla 200 MB</div>
      </div>`;
    return;
  }
  const isVideo = /\.(mp4|mov)$/i.test(f.name);
  el.innerHTML = `
    ${isVideo
      ? `<video src="${state.bulk.previewUrl}" class="max-h-72 mx-auto rounded-lg" controls muted></video>`
      : `<img src="${state.bulk.previewUrl}" class="max-h-72 mx-auto rounded-lg object-contain" />`}
    <div class="text-xs text-slate-400 mt-2">${escapeHtml(f.name)} · ${(f.size / 1048576).toFixed(1)} MB · ${isVideo ? 'Reels' : 'Görsel'} · <span class="text-pink-400 underline">değiştir</span></div>`;
}

function renderBulkAccounts() {
  const el = document.getElementById('bulk-accounts');
  if (!el) return;
  const sel = state.bulk.selected;
  el.innerHTML = state.accounts.map(a => `
    <label class="flex items-center gap-3 px-2 py-2 rounded-lg cursor-pointer ${sel.has(a.id) ? 'bg-pink-600/10' : 'hover:bg-slate-800/60'}">
      <input type="checkbox" class="bulk-acc w-4 h-4 accent-pink-500" data-id="${a.id}" ${sel.has(a.id) ? 'checked' : ''} />
      ${avatar(a, 32)}
      <div class="min-w-0 flex-1">
        <div class="flex items-center gap-2"><span class="font-medium truncate">${escapeHtml(a.label)}</span>${accountStatusBadge(a)}</div>
        <div class="text-xs text-slate-400 truncate">${a.username ? '@' + escapeHtml(a.username) : ''}${a.proxy ? ' · özel IP: ' + escapeHtml(a.proxy_last_ip || proxyHost(a)) : ' · sunucu IP\'si'}</div>
      </div>
    </label>`).join('') || `<p class="text-slate-500 text-sm p-2">Önce soldan hesap ekleyin.</p>`;

  const total = state.accounts.length;
  const count = state.accounts.filter(a => sel.has(a.id)).length;
  document.getElementById('bulk-count').textContent = `${count} / ${total} hesap seçili`;
  const all = document.getElementById('bulk-all');
  all.checked = total > 0 && count === total;
  all.indeterminate = count > 0 && count < total;

  el.querySelectorAll('.bulk-acc').forEach(cb => cb.addEventListener('change', () => {
    const id = Number(cb.dataset.id);
    cb.checked ? sel.add(id) : sel.delete(id);
    renderBulkAccounts();
  }));
}

function renderBulkPanel(panel) {
  const b = state.bulk;
  panel.innerHTML = `
    <div class="space-y-6 max-w-6xl">
      <div class="bg-slate-900 border border-slate-800 rounded-xl p-5">
        <h2 class="text-lg font-semibold">Toplu Paylaşım</h2>
        <p class="text-sm text-slate-400">Gönderiyi seçin, paylaşılacak hesapları işaretleyin. "Paylaş"a basınca gönderi seçtiğiniz hesaplarda sırayla paylaşılır.</p>

        <div class="grid grid-cols-1 lg:grid-cols-2 gap-6 mt-5">
          <div class="space-y-3">
            <div class="font-medium flex items-center gap-2"><span class="ig-gradient w-6 h-6 rounded-full text-xs flex items-center justify-center">1</span> Gönderiyi seçin</div>
            <label id="bulk-drop" class="block border-2 border-dashed border-slate-700 hover:border-pink-500 rounded-xl p-3 text-center cursor-pointer transition">
              <input type="file" id="bulk-file" accept="image/jpeg,image/png,image/webp,video/mp4,video/quicktime,.mov" class="hidden" />
              <div id="bulk-preview"></div>
            </label>
            <textarea id="bulk-caption" rows="4" maxlength="2200" placeholder="Açıklama, #etiketler (tüm hesaplarda aynı)" class="w-full ${inputCls} text-sm">${escapeHtml(b.caption)}</textarea>
            <div id="bulk-caption-count" class="text-xs text-slate-500 text-right">${b.caption.length} / 2200</div>
          </div>

          <div class="space-y-3">
            <div class="font-medium flex items-center gap-2"><span class="ig-gradient w-6 h-6 rounded-full text-xs flex items-center justify-center">2</span> Paylaşılacak hesapları seçin</div>
            <div class="flex items-center justify-between text-sm">
              <label class="flex items-center gap-2 cursor-pointer"><input type="checkbox" id="bulk-all" class="w-4 h-4 accent-pink-500" /> Tümünü seç</label>
              <span id="bulk-count" class="text-slate-400"></span>
            </div>
            <div id="bulk-accounts" class="max-h-80 overflow-y-auto space-y-1 border border-slate-800 rounded-lg p-1"></div>
            <label class="flex items-center gap-2 text-sm text-slate-300">Hesaplar arası bekleme
              <input id="bulk-delay" type="number" min="0" max="3600" value="${b.delaySec}" class="w-20 bg-slate-800 rounded-lg px-2 py-1" /> saniye
            </label>
          </div>
        </div>

        <div class="mt-6 flex flex-wrap items-center gap-4">
          <button id="bulk-submit" class="ig-gradient rounded-lg px-8 py-2.5 font-semibold disabled:opacity-50">Paylaş</button>
          <div id="bulk-upload" class="flex-1 min-w-[200px] hidden">
            <div class="text-xs text-slate-400 mb-1" id="bulk-upload-text">Dosya yükleniyor...</div>
            <div class="progress-track"><div id="bulk-upload-bar" class="progress-fill" style="width:0%"></div></div>
          </div>
          <p id="bulk-msg" class="text-sm"></p>
        </div>
      </div>
      <div id="jobs-section"></div>
    </div>`;

  renderBulkPreview();
  renderBulkAccounts();

  const fileInput = document.getElementById('bulk-file');
  fileInput.addEventListener('change', () => { if (fileInput.files[0]) setBulkFile(fileInput.files[0]); });
  const drop = document.getElementById('bulk-drop');
  drop.addEventListener('dragover', (e) => { e.preventDefault(); drop.classList.add('border-pink-500'); });
  drop.addEventListener('dragleave', () => drop.classList.remove('border-pink-500'));
  drop.addEventListener('drop', (e) => {
    e.preventDefault();
    drop.classList.remove('border-pink-500');
    if (e.dataTransfer.files[0]) setBulkFile(e.dataTransfer.files[0]);
  });
  document.getElementById('bulk-caption').addEventListener('input', (e) => {
    b.caption = e.target.value;
    document.getElementById('bulk-caption-count').textContent = `${b.caption.length} / 2200`;
  });
  document.getElementById('bulk-delay').addEventListener('input', (e) => { b.delaySec = Number(e.target.value) || 0; });
  document.getElementById('bulk-all').addEventListener('change', (e) => {
    b.selected = e.target.checked ? new Set(state.accounts.map(a => a.id)) : new Set();
    renderBulkAccounts();
  });
  document.getElementById('bulk-submit').addEventListener('click', submitBulk);
  renderJobsList();
}

function uploadFile(file, onProgress) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('POST', '/api/uploads');
    xhr.setRequestHeader('Authorization', 'Bearer ' + state.token);
    xhr.upload.onprogress = (e) => { if (e.lengthComputable) onProgress(e.loaded / e.total); };
    xhr.onload = () => {
      let data = null;
      try { data = JSON.parse(xhr.responseText); } catch (e) { /* not JSON */ }
      if (xhr.status >= 200 && xhr.status < 300) resolve(data);
      else reject(new Error((data && data.error) || `HTTP ${xhr.status}`));
    };
    xhr.onerror = () => reject(new Error('Dosya yüklenemedi (bağlantı hatası).'));
    const fd = new FormData();
    fd.append('file', file);
    xhr.send(fd);
  });
}

async function submitBulk() {
  const b = state.bulk;
  const msg = document.getElementById('bulk-msg');
  const setMsg = (text, cls) => { msg.textContent = text; msg.className = 'text-sm ' + cls; };
  const ids = state.accounts.filter(a => b.selected.has(a.id)).map(a => a.id); // panel order
  if (!b.file) return setMsg('Önce gönderiyi (görsel/video) seçin.', 'text-rose-400');
  if (!ids.length) return setMsg('Paylaşılacak en az bir hesap seçin.', 'text-rose-400');
  if (!confirm(`Gönderi ${ids.length} hesapta sırasıyla paylaşılacak${ids.length > 1 ? ` (hesaplar arası ${b.delaySec} sn)` : ''}. Devam edilsin mi?`)) return;

  const btn = document.getElementById('bulk-submit');
  btn.disabled = true;
  setMsg('', '');
  try {
    if (!b.uploaded || b.uploaded.file !== b.file) {
      const box = document.getElementById('bulk-upload');
      box.classList.remove('hidden');
      const res = await uploadFile(b.file, (p) => {
        document.getElementById('bulk-upload-bar').style.width = Math.round(p * 100) + '%';
        document.getElementById('bulk-upload-text').textContent = `Dosya yükleniyor... %${Math.round(p * 100)}`;
      });
      b.uploaded = { file: b.file, id: res.upload_id, name: res.file_name };
      box.classList.add('hidden');
    }
    const job = await api('/bulk-publish', {
      method: 'POST',
      body: JSON.stringify({ upload_id: b.uploaded.id, file_name: b.uploaded.name, caption: b.caption, account_ids: ids, delay_sec: b.delaySec }),
    });
    setMsg(`Paylaşım başladı: ${ids.length} hesap sırayla paylaşılıyor. Durumu aşağıdan izleyebilirsiniz.`, 'text-emerald-400');
    await showJob(job.id);
    scrollToJobs();
  } catch (err) {
    document.getElementById('bulk-upload')?.classList.add('hidden');
    setMsg(err.message, 'text-rose-400');
  } finally {
    btn.disabled = false;
  }
}

async function showJob(jobId) {
  await loadJobs();
  state.activeJobId = jobId;
  await loadJobLogs(jobId);
  renderJobsList();
}

function jobParams(job) {
  try { return JSON.parse(job.params_json || '{}'); } catch (e) { return {}; }
}

// bulkStatuses turns a bulk job's logs into one status per account.
function bulkStatuses(job) {
  const params = jobParams(job);
  const finished = !['pending', 'running', 'paused'].includes(job.status);
  return (params.account_ids || []).map(id => {
    const logs = state.jobLogs.filter(l => l.target_id === String(id));
    const failed = logs.find(l => l.level === 'error');
    const done = logs.find(l => l.message.includes(': paylaşıldı'));
    if (failed) return { id, status: 'failed', detail: failed.message.replace(/^.*?: başarısız — /, '') };
    if (done) return { id, status: 'done', detail: (done.message.match(/https?:\/\/\S+/) || [''])[0] };
    if (logs.length) return { id, status: 'running' };
    return { id, status: finished ? 'skipped' : 'waiting' };
  });
}

function renderBulkStatus(job) {
  const el = document.getElementById('bulk-status');
  if (!el) return;
  const rows = bulkStatuses(job);
  const badge = {
    waiting: '<span class="badge bg-slate-600/30 text-slate-400">sırada</span>',
    running: '<span class="badge bg-sky-500/20 text-sky-400">paylaşılıyor…</span>',
    done: '<span class="badge bg-emerald-500/20 text-emerald-400">paylaşıldı</span>',
    failed: '<span class="badge bg-rose-500/20 text-rose-400">başarısız</span>',
    skipped: '<span class="badge bg-slate-600/30 text-slate-400">paylaşılmadı</span>',
  };
  const params = jobParams(job);
  const retryIds = rows.filter(r => r.status === 'failed' || r.status === 'skipped').map(r => r.id);
  const finished = !['pending', 'running', 'paused'].includes(job.status);
  el.innerHTML = `
    <div class="text-sm text-slate-400 mb-2">${escapeHtml(params.file_name || 'gönderi')}${params.caption ? ' — ' + escapeHtml(params.caption.slice(0, 80)) : ''}</div>
    <div class="space-y-1 mb-3">
      ${rows.map((r, i) => {
        const acc = state.accounts.find(a => a.id === r.id);
        const name = acc ? (acc.username ? '@' + acc.username : acc.label) : `hesap #${r.id}`;
        return `
          <div class="flex items-center gap-3 px-3 py-2 rounded-lg bg-slate-800/40 text-sm">
            <span class="text-slate-500 w-5 text-right">${i + 1}.</span>
            <span class="font-medium flex-1 truncate">${escapeHtml(name)}</span>
            ${r.status === 'done' && r.detail ? `<a href="${escapeHtml(r.detail)}" target="_blank" rel="noopener" class="text-pink-400 hover:underline text-xs">gönderiyi aç</a>` : ''}
            ${r.status === 'failed' ? `<span class="text-xs text-rose-300 truncate max-w-xs" title="${escapeHtml(r.detail)}">${escapeHtml(r.detail)}</span>` : ''}
            ${badge[r.status]}
          </div>`;
      }).join('')}
    </div>
    ${finished && retryIds.length && params.upload_id ? `<button id="bulk-retry" class="${primaryBtnCls} mb-3">Paylaşılamayan ${retryIds.length} hesapta tekrar dene</button>` : ''}`;

  const retry = document.getElementById('bulk-retry');
  if (retry) retry.addEventListener('click', async () => {
    if (!confirm(`${retryIds.length} hesapta tekrar denenecek. Devam edilsin mi?`)) return;
    retry.disabled = true;
    try {
      const next = await api('/bulk-publish', {
        method: 'POST',
        body: JSON.stringify({ upload_id: params.upload_id, file_name: params.file_name, caption: params.caption, account_ids: retryIds, delay_sec: Math.round(job.delay_ms / 1000) }),
      });
      await showJob(next.id);
    } catch (err) { alert('Hata: ' + err.message); retry.disabled = false; }
  });
}

// ---------- Media ----------

function renderMediaSection() {
  const el = document.getElementById('media-section');
  const acc = activeAccount();
  if (!el || !acc) return;
  el.innerHTML = `
    <div class="bg-slate-900 border border-slate-800 rounded-xl p-4">
      <div class="flex items-center justify-between mb-3">
        <h2 class="font-semibold">Gönderiler</h2>
        <button id="load-media" class="${btnCls}" ${acc.status !== 'active' ? 'disabled' : ''}>${state.mediaLoaded ? 'Yenile' : 'Gönderileri Yükle'}</button>
      </div>
      <div id="media-grid" class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-6 gap-3"></div>
      <p id="media-msg" class="text-sm text-rose-400"></p>
    </div>`;
  document.getElementById('load-media').addEventListener('click', async (e) => {
    const btn = e.target;
    btn.disabled = true; btn.textContent = 'Yükleniyor...';
    try {
      state.media = await api(`/accounts/${acc.id}/media?limit=24`);
      state.mediaLoaded = true;
      renderMediaSection();
    } catch (err) {
      document.getElementById('media-msg').textContent = err.message;
      btn.disabled = false; btn.textContent = 'Tekrar Dene';
    }
  });
  renderMediaGrid();
}

function renderMediaGrid() {
  const el = document.getElementById('media-grid');
  if (!el) return;
  if (!state.mediaLoaded) {
    el.innerHTML = `<p class="text-slate-500 text-sm col-span-full">Gönderileri görmek ve yorumlarını yönetmek için yükleyin.</p>`;
    return;
  }
  el.innerHTML = state.media.map(m => {
    const thumb = m.media_type === 'VIDEO' ? (m.thumbnail_url || '') : (m.media_url || m.thumbnail_url || '');
    return `
      <button data-id="${escapeHtml(m.id)}" class="media-item text-left rounded-lg overflow-hidden border ${m.id === state.activeMediaId ? 'border-pink-500 ring-2 ring-pink-500/40' : 'border-slate-800 hover:border-slate-600'} bg-slate-800/40">
        <div class="aspect-square bg-slate-800 relative">
          ${thumb ? `<img src="${escapeHtml(thumb)}" referrerpolicy="no-referrer" loading="lazy" class="w-full h-full object-cover" />` : ''}
          <span class="absolute top-1 left-1 badge bg-black/60 text-slate-200">${mediaTypeLabel(m.media_type)}</span>
        </div>
        <div class="p-2">
          <div class="text-xs text-slate-300 line-clamp-2 min-h-[2rem]">${escapeHtml(m.caption || '—')}</div>
          <div class="text-xs text-slate-500 mt-1">${fmtNum(m.like_count)} beğeni · ${fmtNum(m.comments_count)} yorum</div>
          <div class="text-[11px] text-slate-600">${m.timestamp ? fmtDate(m.timestamp) : ''}</div>
        </div>
      </button>`;
  }).join('') || `<p class="text-slate-500 text-sm col-span-full">Bu hesapta gönderi yok.</p>`;
  el.querySelectorAll('.media-item').forEach(btn => {
    btn.addEventListener('click', async () => {
      state.activeMediaId = btn.dataset.id;
      state.selectedCommentIds.clear();
      state.comments = [];
      renderMediaGrid();
      await refreshComments();
      document.getElementById('comments-section')?.scrollIntoView({ behavior: 'smooth', block: 'start' });
    });
  });
}

function mediaTypeLabel(t) {
  return ({ IMAGE: 'Görsel', VIDEO: 'Video', CAROUSEL_ALBUM: 'Albüm' })[t] || t || '';
}

// ---------- Comments ----------

function activeMedia() {
  return state.media.find(m => m.id === state.activeMediaId);
}

async function refreshComments() {
  if (!state.activeMediaId) return;
  const el = document.getElementById('comments-section');
  if (el && !state.comments.length) el.innerHTML = `<div class="bg-slate-900 border border-slate-800 rounded-xl p-4 text-sm text-slate-400">Yorumlar yükleniyor...</div>`;
  try {
    state.comments = await api(`/accounts/${state.activeAccountId}/media/${state.activeMediaId}/comments?limit=500`);
    const ids = new Set(state.comments.map(c => c.id));
    state.selectedCommentIds.forEach(id => { if (!ids.has(id)) state.selectedCommentIds.delete(id); });
    renderCommentsSection();
  } catch (err) {
    if (el) el.innerHTML = `<div class="bg-slate-900 border border-slate-800 rounded-xl p-4 text-sm text-rose-400">Yorumlar alınamadı: ${escapeHtml(err.message)}</div>`;
  }
}

function filteredComments() {
  const f = state.commentFilter;
  const q = f.search.trim().toLowerCase();
  return state.comments.filter(c =>
    (!q || (c.text || '').toLowerCase().includes(q) || (c.username || '').toLowerCase().includes(q)));
}

function renderCommentsSection() {
  const el = document.getElementById('comments-section');
  if (!el) return;
  const media = activeMedia();
  if (!media) { el.innerHTML = ''; return; }
  const acc = activeAccount();

  el.innerHTML = `
    <div class="bg-slate-900 border border-slate-800 rounded-xl p-4 space-y-4">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <h2 class="font-semibold truncate max-w-xl">Yorumlar — ${escapeHtml((media.caption || 'gönderi').slice(0, 60))}</h2>
        <div class="flex gap-2">
          ${media.permalink ? `<a href="${escapeHtml(media.permalink)}" target="_blank" rel="noopener" class="${btnCls}">Instagram'da Aç</a>` : ''}
          <button id="comments-reload" class="${btnCls}">Yenile</button>
          <button id="comments-export" class="${btnCls}">CSV Dışa Aktar</button>
        </div>
      </div>

      <div class="flex flex-wrap gap-3 items-center text-sm">
        <input id="c-search" value="${escapeHtml(state.commentFilter.search)}" placeholder="Yorum veya kullanıcı ara" class="${inputCls} py-1 text-sm w-64" />
        <span class="text-slate-500">${state.comments.length} yorum yüklendi</span>
      </div>

      <div class="flex flex-wrap items-center gap-2 text-sm">
        <span id="c-selection-count" class="text-slate-400">${state.selectedCommentIds.size} seçili</span>
        <label class="flex items-center gap-1">Worker <input id="c-workers" type="number" min="1" max="5" value="2" class="w-14 bg-slate-800 rounded-lg px-2 py-1" /></label>
        <label class="flex items-center gap-1">Gecikme (ms) <input id="c-delay" type="number" min="0" value="1500" class="w-20 bg-slate-800 rounded-lg px-2 py-1" /></label>
        <button id="c-apply" class="bg-rose-600 hover:bg-rose-500 rounded-lg px-3 py-1.5">Seçilenleri Sil</button>
      </div>

      <div class="overflow-x-auto max-h-[460px] overflow-y-auto border border-slate-800 rounded-lg">
        <table class="w-full text-sm">
          <thead class="bg-slate-800/80 sticky top-0">
            <tr>
              <th class="p-2 w-8"><input type="checkbox" id="c-select-all"/></th>
              <th class="p-2 text-left">Kullanıcı</th>
              <th class="p-2 text-left">Yorum</th>
              <th class="p-2 text-left whitespace-nowrap">Tarih</th>
              <th class="p-2 text-left">Beğeni</th>
              <th class="p-2"></th>
            </tr>
          </thead>
          <tbody id="comment-rows"></tbody>
        </table>
      </div>
    </div>`;

  document.getElementById('comments-reload').addEventListener('click', refreshComments);
  document.getElementById('comments-export').addEventListener('click', async () => {
    try {
      await api(`/accounts/${acc.id}/jobs/export-comments`, { method: 'POST', body: JSON.stringify({ media_id: media.id }) });
      await loadJobs();
      renderJobsList();
      scrollToJobs();
    } catch (err) { alert('Hata: ' + err.message); }
  });
  document.getElementById('c-search').addEventListener('input', (e) => {
    state.commentFilter.search = e.target.value;
    renderCommentRows();
  });
  document.getElementById('c-select-all').addEventListener('change', (e) => {
    filteredComments().forEach(c => e.target.checked ? state.selectedCommentIds.add(c.id) : state.selectedCommentIds.delete(c.id));
    renderCommentRows();
  });
  document.getElementById('c-apply').addEventListener('click', async () => {
    const ids = Array.from(state.selectedCommentIds);
    if (!ids.length) return alert('Önce yorum seçin.');
    if (!confirm(`${ids.length} yorum KALICI OLARAK silinecek. Emin misiniz?`)) return;
    try {
      await api(`/accounts/${acc.id}/jobs/moderate-comments`, {
        method: 'POST',
        body: JSON.stringify({
          media_id: media.id,
          comment_ids: ids,
          action: 'delete',
          worker_count: Number(document.getElementById('c-workers').value) || 1,
          delay_ms: Number(document.getElementById('c-delay').value) || 0,
        }),
      });
      state.selectedCommentIds.clear();
      renderCommentRows();
      await loadJobs();
      renderJobsList();
      scrollToJobs();
    } catch (err) { alert('Hata: ' + err.message); }
  });
  renderCommentRows();
}

function renderCommentRows() {
  const tbody = document.getElementById('comment-rows');
  if (!tbody) return;
  const rows = filteredComments();
  tbody.innerHTML = rows.map(c => `
    <tr class="border-t border-slate-800">
      <td class="p-2"><input type="checkbox" class="c-check" data-id="${escapeHtml(c.id)}" ${state.selectedCommentIds.has(c.id) ? 'checked' : ''}/></td>
      <td class="p-2 whitespace-nowrap">@${escapeHtml(c.username || '?')}</td>
      <td class="p-2 max-w-md break-words">${escapeHtml(c.text)}</td>
      <td class="p-2 text-xs text-slate-500 whitespace-nowrap">${c.timestamp ? fmtDate(c.timestamp) : ''}</td>
      <td class="p-2">${fmtNum(c.like_count)}</td>
      <td class="p-2"><button class="c-reply text-pink-400 hover:text-pink-300 text-xs" data-id="${escapeHtml(c.id)}" data-user="${escapeHtml(c.username || '')}">Yanıtla</button></td>
    </tr>`).join('') || `<tr><td colspan="6" class="p-4 text-center text-slate-500">Yorum bulunamadı.</td></tr>`;

  const countEl = document.getElementById('c-selection-count');
  if (countEl) countEl.textContent = `${state.selectedCommentIds.size} seçili`;
  const selectAll = document.getElementById('c-select-all');
  if (selectAll) selectAll.checked = rows.length > 0 && rows.every(c => state.selectedCommentIds.has(c.id));

  tbody.querySelectorAll('.c-check').forEach(cb => cb.addEventListener('change', () => {
    cb.checked ? state.selectedCommentIds.add(cb.dataset.id) : state.selectedCommentIds.delete(cb.dataset.id);
    if (countEl) countEl.textContent = `${state.selectedCommentIds.size} seçili`;
  }));
  tbody.querySelectorAll('.c-reply').forEach(btn => btn.addEventListener('click', async () => {
    const message = prompt(`@${btn.dataset.user} kullanıcısına yanıt:`);
    if (!message || !message.trim()) return;
    try {
      await api(`/accounts/${state.activeAccountId}/media/${state.activeMediaId}/comments/${btn.dataset.id}/reply`, { method: 'POST', body: JSON.stringify({ message }) });
      alert('Yanıt gönderildi.');
    } catch (err) { alert('Hata: ' + err.message); }
  }));
}

// ---------- Jobs ----------

function scrollToJobs() {
  document.getElementById('jobs-section')?.scrollIntoView({ behavior: 'smooth' });
}

function renderJobsList() {
  const el = document.getElementById('jobs-section');
  if (!el) return;
  el.innerHTML = `
    <div class="bg-slate-900 border border-slate-800 rounded-xl p-4">
      <h2 class="font-semibold mb-3">İşler</h2>
      <div class="space-y-2 max-h-80 overflow-y-auto" id="job-items"></div>
      <div id="job-detail" class="mt-4"></div>
    </div>`;
  const itemsEl = document.getElementById('job-items');
  itemsEl.innerHTML = state.jobs.map(j => {
    const pct = j.total > 0 ? Math.round((j.processed / j.total) * 100) : 0;
    return `
      <button data-id="${j.id}" class="job-item w-full text-left p-3 rounded-lg border ${j.id === state.activeJobId ? 'border-pink-600 bg-pink-600/10' : 'border-slate-800 bg-slate-800/40 hover:border-slate-700'}">
        <div class="flex justify-between items-center text-sm mb-1">
          <span class="font-medium truncate">${jobTypeLabel(j.type)} #${j.id}${j.type === 'bulk_publish' && jobParams(j).file_name ? ' · ' + escapeHtml(jobParams(j).file_name) : ''}</span>
          <span class="badge ${statusColor(j.status)}">${statusText(j.status)}</span>
        </div>
        <div class="progress-track"><div class="progress-fill" style="width:${pct}%"></div></div>
        <div class="text-xs text-slate-500 mt-1">${j.processed}/${j.total}${j.type === 'bulk_publish' ? ' hesap' : ''} · başarılı ${j.succeeded} · başarısız ${j.failed} · ${fmtDate(j.created_at)}</div>
      </button>`;
  }).join('') || `<p class="text-slate-500 text-sm">Henüz iş yok.</p>`;

  itemsEl.querySelectorAll('.job-item').forEach(btn => {
    btn.addEventListener('click', async () => {
      state.activeJobId = Number(btn.dataset.id);
      renderJobsList();
      await loadJobLogs(state.activeJobId);
      renderJobDetail();
    });
  });
  if (state.activeJobId) renderJobDetail();
}

function jobTypeLabel(t) {
  return ({
    moderate_comments: 'Yorum Moderasyonu', publish_media: 'Paylaşım', export_comments: 'Yorum Dışa Aktarma',
    bulk_publish: 'Toplu Paylaşım',
  })[t] || t;
}
function statusText(s) {
  return ({ pending: 'bekliyor', running: 'çalışıyor', paused: 'duraklatıldı', completed: 'tamamlandı', failed: 'başarısız', cancelled: 'iptal edildi' })[s] || s;
}
function statusColor(s) {
  return ({
    pending: 'bg-slate-500/20 text-slate-400', running: 'bg-sky-500/20 text-sky-400',
    paused: 'bg-amber-500/20 text-amber-400', completed: 'bg-emerald-500/20 text-emerald-400',
    failed: 'bg-rose-500/20 text-rose-400', cancelled: 'bg-slate-500/20 text-slate-400',
  })[s] || '';
}

function renderJobDetail() {
  const el = document.getElementById('job-detail');
  if (!el) return;
  const job = state.jobs.find(j => j.id === state.activeJobId);
  if (!job) { el.innerHTML = ''; return; }
  el.innerHTML = `
    <div class="border-t border-slate-800 pt-3 flex flex-wrap items-center gap-2 mb-2">
      <button id="job-pause" class="${btnCls}" ${job.status !== 'running' ? 'disabled' : ''}>Duraklat</button>
      <button id="job-resume" class="${btnCls}" ${job.status !== 'paused' ? 'disabled' : ''}>Devam Et</button>
      <button id="job-cancel" class="bg-rose-700 hover:bg-rose-600 rounded-lg px-3 py-1.5 text-sm disabled:opacity-50" ${['running', 'paused'].includes(job.status) ? '' : 'disabled'}>Durdur</button>
      ${job.result_path ? `<button id="job-download" class="${primaryBtnCls}">CSV İndir</button>` : ''}
    </div>
    ${job.type === 'bulk_publish' ? '<div id="bulk-status"></div>' : ''}
    <div id="job-log-list" class="max-h-64 overflow-y-auto space-y-1 text-xs font-mono bg-slate-950/60 rounded-lg p-2"></div>`;

  document.getElementById('job-pause').addEventListener('click', async () => { await api(`/jobs/${job.id}/pause`, { method: 'POST' }); });
  document.getElementById('job-resume').addEventListener('click', async () => { await api(`/jobs/${job.id}/resume`, { method: 'POST' }); });
  document.getElementById('job-cancel').addEventListener('click', async () => {
    if (confirm('İşi durdurmak istediğinize emin misiniz?')) await api(`/jobs/${job.id}/cancel`, { method: 'POST' });
  });
  const dl = document.getElementById('job-download');
  if (dl) dl.addEventListener('click', () => downloadJobResult(job));
  renderJobLogs();
}

function renderJobLogs() {
  const el = document.getElementById('job-log-list');
  if (!el) return;
  const job = state.jobs.find(j => j.id === state.activeJobId);
  if (job && job.type === 'bulk_publish') renderBulkStatus(job);
  el.innerHTML = state.jobLogs.map(l =>
    `<div class="${l.level === 'error' ? 'text-rose-400' : l.level === 'warn' ? 'text-amber-400' : 'text-slate-300'}">[${new Date(l.created_at).toLocaleTimeString()}] ${escapeHtml(l.message)}</div>`
  ).join('') || '<div class="text-slate-600">Kayıt yok.</div>';
}

async function downloadJobResult(job) {
  try {
    const res = await fetch(`/api/jobs/${job.id}/download`, { headers: { Authorization: 'Bearer ' + state.token } });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const url = URL.createObjectURL(await res.blob());
    const a = Object.assign(document.createElement('a'), { href: url, download: `job_${job.id}_comments.csv` });
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
  } catch (err) { alert('İndirme başarısız: ' + err.message); }
}

// ---------- Helpers ----------

function openModal(html) {
  let overlay = document.getElementById('modal-overlay');
  if (!overlay) {
    overlay = document.createElement('div');
    overlay.id = 'modal-overlay';
    overlay.className = 'fixed inset-0 bg-black/60 flex items-center justify-center z-50 px-4';
    document.body.appendChild(overlay);
  }
  overlay.innerHTML = `<div class="bg-slate-900 border border-slate-800 rounded-xl p-6 w-full max-w-lg max-h-[92vh] overflow-y-auto">${html}</div>`;
  overlay.style.display = 'flex';
}
function closeModal() {
  const overlay = document.getElementById('modal-overlay');
  if (overlay) overlay.style.display = 'none';
}

function fmtNum(n) {
  return Number(n || 0).toLocaleString('tr-TR');
}
function fmtDate(s) {
  const d = new Date(s);
  return isNaN(d) ? '' : d.toLocaleString('tr-TR', { dateStyle: 'short', timeStyle: 'short' });
}

function escapeHtml(str) {
  return String(str ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

boot();
