/* 坚祥数字助理（软著版）前端逻辑：原生 JS，无框架、无构建步骤。风格对齐 DSH。 */

const $ = (id) => document.getElementById(id);

/* ---------- JX：插件接口（插件兼容层注入的浏览器半边经此接入原生界面） ---------- */
const JX = {
  _bus: {},
  _msgActions: [],       // fn(actsRow, msg)：向消息悬停操作行追加按钮
  _settingsSections: [], // {title, mount(el)}：设置弹窗分区
  on(ev, fn) { (this._bus[ev] = this._bus[ev] || []).push(fn); },
  emit(ev, payload) { (this._bus[ev] || []).forEach((f) => { try { f(payload); } catch (e) { console.warn('[JX]', ev, e); } }); },
  registerMessageAction(fn) { this._msgActions.push(fn); },
  registerSettingsSection(sec) { this._settingsSections.push(sec); },
  // 插件向输入框右侧挂控件（按钮等），注册即生效
  addComposerControl(el) { const c = document.getElementById('plugin-composer-controls'); if (c) c.appendChild(el); },
};
window.JX = JX;

const state = {
  sessions: [],
  cur: null,        // 当前会话 meta
  msgs: [],         // 当前会话已确认的消息
  streamText: '',   // 正在流式输出的助手文本
  streamThinking: '', // 正在流式输出的思考过程
  streaming: false,
  abort: null,
  queue: [],        // 对话中途发送行为=等待 时的待发队列（本会话）
  defaultWs: '',    // 默认工作区（文件夹），来自 /api/status
  models: [],       // 可选模型清单，来自 /api/models
  defaultProvider: '',
  defaultModel: '',
  approvals: [],    // 待用户决定的越权审批（右下角卡片）
  questions: [],    // 待回答的助手提问 / 计划审批（右下角卡片）
};

async function api(path, opts = {}) {
  const res = await fetch(path, {
    headers: { 'Content-Type': 'application/json' },
    ...opts,
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

function esc(s) {
  return String(s).replace(/[&<>"']/g, (c) => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
  ));
}

/* ---------- Markdown 渲染（围栏代码块、标题、列表、表格、行内样式） ---------- */

function inlineFmt(s) {
  return esc(s)
    .replace(/`([^`\n]+)`/g, '<code>$1</code>')
    .replace(/\*\*([^*\n]+)\*\*/g, '<b>$1</b>');
}

function renderBlocks(seg) {
  const lines = seg.split(/\r?\n/);
  const out = [];
  let i = 0;
  const isTableSep = (l) => /^\s*\|[\s:|-]+\|\s*$/.test(l);
  const isUl = (l) => /^\s*[-*]\s+\S/.test(l);
  const isOl = (l) => /^\s*\d+[.、]\s+\S/.test(l);
  const parseRow = (l) => l.trim().replace(/^\||\|$/g, '').split('|').map((c) => c.trim());

  while (i < lines.length) {
    const line = lines[i];
    if (!line.trim()) { i++; continue; }

    const h = line.match(/^(#{1,6})\s+(.*)$/);
    if (h) {
      const lv = Math.min(h[1].length + 1, 5); // 页面里 h2 起步
      out.push(`<h${lv}>${inlineFmt(h[2])}</h${lv}>`);
      i++; continue;
    }

    if (/^\s*\|.+\|\s*$/.test(line) && i + 1 < lines.length && isTableSep(lines[i + 1])) {
      const head = parseRow(line);
      i += 2;
      const rows = [];
      while (i < lines.length && /^\s*\|.+\|\s*$/.test(lines[i])) rows.push(parseRow(lines[i++]));
      let t = '<table><thead><tr>' + head.map((c) => `<th>${inlineFmt(c)}</th>`).join('') + '</tr></thead><tbody>';
      for (const r of rows) t += '<tr>' + r.map((c) => `<td>${inlineFmt(c)}</td>`).join('') + '</tr>';
      out.push(t + '</tbody></table>');
      continue;
    }

    if (isUl(line) || isOl(line)) {
      const ol = isOl(line) && !isUl(line);
      const items = [];
      while (i < lines.length && (ol ? isOl(lines[i]) : isUl(lines[i]))) {
        items.push(lines[i].replace(ol ? /^\s*\d+[.、]\s+/ : /^\s*[-*]\s+/, ''));
        i++;
      }
      out.push((ol ? '<ol>' : '<ul>') + items.map((it) => `<li>${inlineFmt(it)}</li>`).join('') + (ol ? '</ol>' : '</ul>'));
      continue;
    }

    const para = [];
    while (i < lines.length && lines[i].trim() &&
      !/^#{1,6}\s/.test(lines[i]) && !/^\s*\|/.test(lines[i]) && !isUl(lines[i]) && !isOl(lines[i])) {
      para.push(lines[i]); i++;
    }
    if (para.length) out.push(`<p>${inlineFmt(para.join('\n')).replace(/\n/g, '<br>')}</p>`);
    else { out.push(`<p>${inlineFmt(line)}</p>`); i++; } // 兜底：流式输出中的半成品表格行走到这里，必须强制前进，否则外层循环死循环卡死页面
  }
  return out.join('');
}

function md(text) {
  const parts = String(text ?? '').split('```');
  let html = '';
  parts.forEach((part, i) => {
    if (i % 2 === 1) {
      const code = part.replace(/^[a-zA-Z0-9_#+-]*\r?\n/, '');
      html += `<pre><code>${esc(code)}</code></pre>`;
    } else if (part.trim()) {
      html += renderBlocks(part);
    }
  });
  return html;
}

function relTime(s) {
  if (!s) return '';
  const t = new Date(String(s).replace(' ', 'T'));
  const diff = (Date.now() - t.getTime()) / 1000;
  if (isNaN(diff) || diff < 0) return '';
  if (diff < 60) return '刚刚';
  if (diff < 3600) return Math.floor(diff / 60) + '分钟';
  if (diff < 86400) return Math.floor(diff / 3600) + '小时';
  return Math.floor(diff / 86400) + '天';
}

function baseName(p) {
  if (!p) return '工作区';
  const seg = String(p).split(/[\\/]/).filter(Boolean);
  return seg.length ? seg[seg.length - 1] : p;
}

/* ---------- 状态与侧栏 ---------- */

async function loadStatus() {
  const el = $('status-text');
  try {
    const st = await api('/api/status');
    state.defaultWs = st.workspace || '';
    if (st.mock) { el.textContent = '演示模式'; el.className = 'pill warn'; }
    else if (!st.hasKey) { el.textContent = '未配置 Key'; el.className = 'pill warn'; }
    else { el.textContent = '✓ 已连接'; el.className = 'pill ok'; }
    syncComposer();
  } catch (e) {
    el.textContent = '服务离线';
    el.className = 'pill gray';
  }
}

// 会话所属的工作区（文件夹）：会话能写哪个目录由它挂载的工作区决定，
// 未单独设置的挂在默认工作区下。
// lsGet/lsSet 兼容两种用法：对象/数组经 JSON 存取；纯字符串原样存取
// （读取时仅 { 或 [ 开头的值才做 JSON 解析，避免 '1'/'zh' 这类值被误转）。
function lsGet(k, dft) {
  try {
    const v = localStorage.getItem(k);
    if (v === null) return dft;
    const t = v.trim();
    if (t === 'null') return null;
    if (t.startsWith('{') || t.startsWith('[')) {
      try { return JSON.parse(t); } catch (e) { return v; }
    }
    return v;
  } catch (e) { return dft; }
}
function lsSet(k, v) {
  try { localStorage.setItem(k, typeof v === 'string' ? v : JSON.stringify(v)); } catch (e) {}
}

// 侧栏视图设置：分组方式（ws=按工作区 flat=单列表）与排序方式
const viewCfg = Object.assign({ group: 'ws', sort: 'updated' }, lsGet('jx.view', {}));
const wsCollapsed = lsGet('jx.wscol', {});   // 工作区分组折叠状态

// 已知工作区 = 手动添加 ∪ 会话用过 ∪ 默认
function knownWorkspaces() {
  const set = new Set(lsGet('jx.wslist', []));
  if (state.defaultWs) set.add(state.defaultWs);
  (state.sessions || []).forEach((x) => { if (x.workspace) set.add(x.workspace); });
  return [...set];
}

function wsOf(s) {
  return s.workspace || state.defaultWs || '';
}

/* ---------- 侧栏标题行工具：搜索 / 排序选项 / 添加工作目录 ---------- */

/* ---------- 侧栏搜索：双路逐字搜索（标题 + 聊天记录），微信式分屏结果 ---------- */

// 搜索状态：abortController 中止在途请求；token 防串台；revealTimer 控制逐条渐现
let searchAbort = null;
let searchToken = 0;
let searchRevealTimer = null;
let sideSearchDebounce = null;

function sideSearchOpen() {
  return !$('side-search').classList.contains('hidden');
}

function toggleSideSearch() {
  if (sideSearchOpen()) {
    closeSideSearch();
    return;
  }
  $('side-search').classList.remove('hidden');
  const inp = document.getElementById('side-search-input');
  inp.value = '';
  inp.focus();
}

// 关闭搜索：中止两路请求与渐现动画，还原侧栏
function closeSideSearch() {
  if (searchAbort) { searchAbort.abort(); searchAbort = null; }
  if (searchRevealTimer) { clearInterval(searchRevealTimer); searchRevealTimer = null; }
  if (sideSearchDebounce) { clearTimeout(sideSearchDebounce); sideSearchDebounce = null; }
  searchToken++; // 让在途回调全部失效
  $('side-search-input').value = '';
  $('side-search-close').classList.add('hidden');
  $('side-search').classList.add('hidden');
  $('side-search-results').classList.add('hidden');
  $('ws-section').classList.remove('hidden');
  $('session-list').classList.remove('hidden');
  loadSessions();
}

// 逐字高亮：大小写不敏感地标出所有命中（返回带 <mark> 的 HTML）
function hi(text, q) {
  if (!q) return esc(text);
  const low = String(text).toLowerCase();
  const qi = q.toLowerCase();
  let out = '', i = 0;
  for (;;) {
    const at = low.indexOf(qi, i);
    if (at < 0) { out += esc(String(text).slice(i)); break; }
    out += esc(String(text).slice(i, at)) + '<mark>' + esc(String(text).slice(at, at + q.length)) + '</mark>';
    i = at + q.length;
  }
  return out;
}

// 触发搜索（输入防抖 250ms）：标题路先行渲染，聊天记录路返回后逐条渐现
function runSideSearch(qRaw) {
  const q = qRaw.trim();
  if (searchAbort) { searchAbort.abort(); searchAbort = null; }
  if (searchRevealTimer) { clearInterval(searchRevealTimer); searchRevealTimer = null; }
  const token = ++searchToken;
  $('side-search-close').classList.toggle('hidden', !$('side-search-input').value);
  if (!q) {
    // 清空输入：退出占领态，恢复普通列表
    $('side-search-results').classList.add('hidden');
    $('ws-section').classList.remove('hidden');
    $('session-list').classList.remove('hidden');
    loadSessions();
    return;
  }
  // 搜索结果占领侧栏（标题行隐藏）
  $('side-search-results').classList.remove('hidden');
  $('ws-section').classList.add('hidden');
  $('session-list').classList.add('hidden');

  // 标题路：先占位“搜索中”，结果到达即渲染
  $('ssr-title').innerHTML = '<div class="ssr-empty">搜索中…</div>';
  $('ssr-title-count').textContent = '';
  // 聊天记录路：占位
  $('ssr-content').innerHTML = '<div class="ssr-empty">搜索中…</div>';
  $('ssr-content-count').textContent = '';

  searchAbort = new AbortController();
  const signal = searchAbort.signal;
  const eq = encodeURIComponent(q);

  api('/api/search/title?q=' + eq, { signal }).then((d) => {
    if (token !== searchToken) return;
    renderTitleHits(d.hits || [], q);
  }).catch((e) => {
    if (e.name === 'AbortError' || token !== searchToken) return;
    $('ssr-title').innerHTML = '<div class="ssr-empty">搜索失败：' + esc(e.message) + '</div>';
  });

  api('/api/search/content?q=' + eq, { signal }).then((d) => {
    if (token !== searchToken) return;
    revealContentHits(d.hits || [], q, token);
  }).catch((e) => {
    if (e.name === 'AbortError' || token !== searchToken) return;
    $('ssr-content').innerHTML = '<div class="ssr-empty">搜索失败：' + esc(e.message) + '</div>';
  });
}

function renderTitleHits(hits, q) {
  const box = $('ssr-title');
  box.innerHTML = '';
  $('ssr-title-count').textContent = String(hits.length);
  if (!hits.length) {
    box.innerHTML = '<div class="ssr-empty">没有匹配的工作任务</div>';
    return;
  }
  hits.forEach((h) => {
    const b = document.createElement('button');
    b.className = 'ssr-item';
    b.innerHTML =
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/></svg>' +
      '<span class="ssr-main">' + hi(h.title || '(未命名)', q) + '</span>';
    b.onclick = () => { closeSideSearch(); if (!state.streaming) openSession(h.sessionId); };
    box.appendChild(b);
  });
}

// 聊天记录命中逐条渐现（约 70ms 一条），关闭/换词即停
function revealContentHits(hits, q, token) {
  const box = $('ssr-content');
  box.innerHTML = '';
  if (!hits.length) {
    $('ssr-content-count').textContent = '0';
    box.innerHTML = '<div class="ssr-empty">没有匹配的聊天记录</div>';
    return;
  }
  let i = 0;
  searchRevealTimer = setInterval(() => {
    if (token !== searchToken) { clearInterval(searchRevealTimer); searchRevealTimer = null; return; }
    if (i >= hits.length) {
      clearInterval(searchRevealTimer);
      searchRevealTimer = null;
      $('ssr-content-count').textContent = String(hits.length);
      return;
    }
    box.appendChild(contentHitItem(hits[i++], q));
  }, 70);
}

function contentHitItem(h, q) {
  const b = document.createElement('button');
  b.className = 'ssr-item ssr-msg';
  b.innerHTML =
    '<span class="ssr-session">' + esc(h.title || '(未命名)') + ' · ' + (h.role === 'user' ? '用户' : '助手') + '</span>' +
    '<span class="ssr-main">' + hi(h.snippet || '', q) + '</span>';
  b.onclick = () => jumpToMessage(h.sessionId, h.seq);
  return b;
}

// 跳到会话并定位到命中消息：滚动到视野中心并闪烁高亮
async function jumpToMessage(sessionId, seq) {
  closeSideSearch();
  if (state.streaming) return;
  if (!state.cur || state.cur.id !== sessionId) {
    await openSession(sessionId);
  }
  // 用 setTimeout 而非 rAF：页面在后台时 rAF 会被暂停，定位就丢了
  setTimeout(() => {
    const el = document.querySelector('[data-seq="' + seq + '"]');
    if (!el) return;
    // 目标在折叠段/收起的过程行里：先展开再定位
    const sec = el.closest ? el.closest('.tf-sec') : null;
    if (sec) { setSecOpen(sec, true); if (sec.dataset.key) turnOpen[sec.dataset.key] = true; }
    const row = el.closest ? el.closest('.proc-row') : null;
    if (row) setProcOpen(row, true);
    el.scrollIntoView({ block: 'center' });
    el.classList.add('search-flash');
    setTimeout(() => el.classList.remove('search-flash'), 2400);
  }, 60);
}

// 排序选项菜单：上半区「分组」（按工作区/时间线/单列表），下半区「排序」。
// 选中项左侧亮对勾，仿系统设置的分区单选菜单。
function openViewMenu(anchor) {
  const m = ensureCtxMenu();
  const opt = (key, val, label) => ({ key, val, label, on: viewCfg[key] === val });
  const groups = [
    { title: '分组', opts: [
      opt('group', 'ws', '按工作区'),
      opt('group', 'time', '时间线'),
      opt('group', 'flat', '单列表'),
    ] },
    { title: '排序', opts: [
      opt('sort', 'updated', '最近更新'),
      opt('sort', 'created', '最新创建'),
    ] },
  ];
  const check = '<svg class="vo-check" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M4 12l5 5L20 7"/></svg>';
  let html = '';
  groups.forEach((g) => {
    html += `<div class="pop-group">${g.title}</div>`;
    g.opts.forEach((o) => {
      html += `<button class="pop-item vo-item${o.on ? ' on' : ''}" data-k="${o.key}" data-v="${o.val}">${o.on ? check : '<span class="vo-check"></span>'}${o.label}</button>`;
    });
  });
  m.innerHTML = html;
  closePopovers();
  m.classList.remove('hidden');
  const r = anchor.getBoundingClientRect();
  m.style.left = Math.max(8, Math.min(r.right - 170, window.innerWidth - m.offsetWidth - 8)) + 'px';
  m.style.top = Math.min(r.bottom + 6, window.innerHeight - m.offsetHeight - 8) + 'px';
  m.querySelectorAll('.vo-item').forEach((b) => {
    b.onclick = (e) => {
      e.stopPropagation();
      viewCfg[b.dataset.k] = b.dataset.v;
      lsSet('jx.view', viewCfg);
      closePopovers();
      loadSessions();
    };
  });
}

// 调后端弹出系统原生「选择文件夹」对话框，返回所选目录绝对路径；取消返回空串。
// 浏览器自身的目录选择 API 不回传绝对路径，而本服务就跑在用户本机上，
// 由后端代为弹框既能拿到真实路径，体验也与系统/浏览器一致。
let pickingFolder = false;
async function pickWorkFolder() {
  if (pickingFolder) return '';
  pickingFolder = true;
  try {
    const start = state.defaultWs ? '?start=' + encodeURIComponent(state.defaultWs) : '';
    const res = await api('/api/pick-folder' + start);
    return (res && res.path) || '';
  } catch (e) {
    alert('无法打开文件夹选择框：' + (e.message || e));
    return '';
  } finally {
    pickingFolder = false;
  }
}

// 把目录登记为已知工作区（localStorage），侧栏分组与选择面板都会出现它
function rememberWorkspace(ws) {
  if (!ws) return;
  lsSet('jx.wslist', [...new Set([...lsGet('jx.wslist', []), ws])]);
}

// 侧栏「添加工作目录」：弹选择框并登记
async function addWorkFolder() {
  const p = await pickWorkFolder();
  if (!p) return;
  rememberWorkspace(p);
  loadSessions();
}

// 右键/三个点共用的上下文菜单（动态 popover）
function ensureCtxMenu() {
  let m = document.getElementById('ctx-menu');
  if (!m) {
    m = document.createElement('div');
    m.id = 'ctx-menu';
    m.className = 'popover ctx-menu hidden';
    document.body.appendChild(m);
  }
  return m;
}
function openCtxMenu(items, x, y) {
  const m = ensureCtxMenu();
  m.innerHTML = '';
  items.forEach((it) => {
    const b = document.createElement('button');
    b.className = 'pop-item' + (it.danger ? ' danger' : '');
    b.textContent = it.label;
    b.onclick = (e) => { e.stopPropagation(); closePopovers(); it.fn(); };
    m.appendChild(b);
  });
  closePopovers();
  m.classList.remove('hidden');
  const mw = m.offsetWidth, mh = m.offsetHeight;
  m.style.left = Math.max(8, Math.min(x, window.innerWidth - mw - 8)) + 'px';
  m.style.top = Math.max(8, Math.min(y, window.innerHeight - mh - 8)) + 'px';
}

async function deleteSession(s) {
  if (!confirm('删除该会话？此操作不可恢复。')) return;
  await api('/api/sessions/' + s.id, { method: 'DELETE' });
  if (state.cur && state.cur.id === s.id) {
    state.cur = null;
    state.msgs = [];
    resetEmpty();
  }
  loadSessions();
}

function openSessionMenu(s, rect) {
  openCtxMenu([
    { label: '重命名', fn: async () => {
      const t = prompt('重命名会话', s.title || '');
      if (t === null || !t.trim()) return;
      await api('/api/sessions/' + s.id, { method: 'PATCH', body: JSON.stringify({ title: t.trim() }) });
      loadSessions();
    } },
    { label: s.unread ? '取消未读' : '标为未读', fn: async () => {
      await api('/api/sessions/' + s.id, { method: 'PATCH', body: JSON.stringify({ unread: !s.unread }) });
      loadSessions();
    } },
    { label: '分支对话', fn: async () => {
      try {
        const fork = await api('/api/sessions/' + s.id + '/fork', { method: 'POST' });
        await loadSessions();
        if (!state.streaming) openSession(fork.id);
      } catch (e) { alert('分支失败：' + e.message); }
    } },
    { label: '移入回收站', danger: true, fn: async () => {
      await api('/api/sessions/' + s.id, { method: 'PATCH', body: JSON.stringify({ archived: true }) });
      if (state.cur && state.cur.id === s.id) { state.cur = null; state.msgs = []; resetEmpty(); }
      loadSessions();
    } },
  ], rect.right - 8, rect.bottom + 4);
}

function sessionItem(s) {
  const item = document.createElement('div');
  item.className = 'session-item' + (state.cur && state.cur.id === s.id ? ' active' : '');
  // 圆点：平时不显示；橙=待审批，绿=完成未查看，红=标为未读
  let dot = '';
  if (dotApproval[s.id]) dot = ' ap';
  else if (dotDone[s.id]) dot = ' done';
  else if (s.unread) dot = ' unread';
  item.innerHTML =
    `<span class="dot${dot}"></span>` +
    `<span class="s-title">${esc(s.title || '(未命名)')}</span>` +
    `<span class="s-time">${esc(relTime(s.updatedAt))}</span>` +
    `<span class="row-acts">` +
      `<button class="ra-copy" title="复制标题"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h10"/></svg></button>` +
      `<button class="ra-more" title="更多"><svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.6"/><circle cx="12" cy="12" r="1.6"/><circle cx="19" cy="12" r="1.6"/></svg></button>` +
    `</span>`;
  item.onclick = (e) => {
    if (e.target.closest('.row-acts')) return;
    if (!state.streaming) openSession(s.id);
  };
  item.querySelector('.ra-copy').onclick = (e) => {
    e.stopPropagation();
    navigator.clipboard.writeText(s.title || '(未命名)');
    const b = e.currentTarget;
    b.classList.add('ok'); setTimeout(() => b.classList.remove('ok'), 900);
  };
  item.querySelector('.ra-more').onclick = (e) => {
    e.stopPropagation();
    openSessionMenu(s, item.getBoundingClientRect());
  };
  item.oncontextmenu = (e) => { e.preventDefault(); openSessionMenu(s, item.getBoundingClientRect()); };
  return item;
}

const prevStatus = {}; // 会话id -> 上一快照的 status（识别 running→idle 的完成翻转）
function aRunJustEnded(id) {
  const cur = (state.sessions.find((x) => x.id === id) || {}).status;
  const was = prevStatus[id];
  return was === 'running' && cur !== 'running';
}

async function loadSessions() {
  const data = await api('/api/sessions');
  state.sessions = data.sessions || [];
  (state.sessions || []).forEach((x) => { prevStatus[x.id] = x.status; });
  // 左栏只放顶层未归档会话；助理主对话、子会话/辅助对话不占列表
  // （标题/内容的搜索在搜索面板里走 /api/search/*，这里不再本地过滤）
  let visible = state.sessions.filter((s) => s.mode !== 'assistant' && !s.parentId && !s.archived);
  if (viewCfg.sort === 'created') {
    visible.sort((a, b) => String(b.createdAt || '').localeCompare(String(a.createdAt || '')));
  }
  const box = $('session-list');
  box.innerHTML = '';
  const recycled = new Set(lsGet('jx.wsrecycled', []));
  const aliasMap = lsGet('jx.wsalias', {});
  if (viewCfg.group === 'flat') {
    box.classList.add('flat');
    for (const s of visible) {
      if (!recycled.has(wsOf(s))) box.appendChild(sessionItem(s));
    }
    if (!visible.length) box.innerHTML = '<div class="ws-empty">没有会话</div>';
    return;
  }
  if (viewCfg.group === 'time') {
    // 时间线分组：按最后活动时间（updatedAt，缺省 createdAt）分时间带
    box.classList.add('flat');
    const day0 = (d) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
    const today0 = day0(new Date());
    const bucketOf = (s) => {
      const raw = String(s.updatedAt || s.createdAt || '').replace(' ', 'T');
      const t = new Date(raw).getTime();
      if (isNaN(t)) return 4; // 无时间信息的落「更早」
      const days = Math.floor((today0 - day0(new Date(t))) / 86400000);
      if (days <= 0) return 0;
      if (days === 1) return 1;
      if (days <= 3) return 2;
      if (days <= 7) return 3;
      return 4;
    };
    const bands = ['今天', '昨天', '3天前', '1周前', '更早'].map((label) => ({ label, items: [] }));
    visible.forEach((s) => { if (!recycled.has(wsOf(s))) bands[bucketOf(s)].items.push(s); });
    bands.forEach((b) => {
      if (!b.items.length) return;
      const head = document.createElement('div');
      head.className = 'time-band';
      head.textContent = b.label;
      box.appendChild(head);
      b.items.forEach((s) => box.appendChild(sessionItem(s)));
    });
    if (!visible.length) box.innerHTML = '<div class="ws-empty">没有会话</div>';
    return;
  }
  box.classList.remove('flat');
  // 按工作区分组；手动添加过的工作区即使没有会话也显示
  const groups = new Map();
  for (const ws of knownWorkspaces()) groups.set(ws, []);
  for (const s of visible) {
    const ws = wsOf(s);
    if (!groups.has(ws)) groups.set(ws, []);
    groups.get(ws).push(s);
  }
  for (const [ws, items] of groups) {
    if (recycled.has(ws)) continue; // 已移入回收站的工作区整组隐藏
    const group = document.createElement('div');
    group.className = 'ws-group';
    const collapsed = !!wsCollapsed[ws];
    const head = document.createElement('div');
    head.className = 'ws-group-head' + (collapsed ? ' collapsed' : '');
    head.title = ws || '（未设置，使用默认工作区）';
    head.innerHTML =
      `<svg class="chev" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M9 6l6 6-6 6"/></svg>` +
      `<svg class="folder" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/></svg>` +
      `<span class="ws-name">${esc(aliasMap[ws] || baseName(ws) || '默认工作区')}</span>` +
      `<span class="ws-count">${items.length}</span>` +
      `<span class="ws-acts">` +
        `<button class="wa-add" title="在此目录新建会话"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg></button>` +
        `<button class="wa-more" title="工作区操作"><svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.6"/><circle cx="12" cy="12" r="1.6"/><circle cx="19" cy="12" r="1.6"/></svg></button>` +
      `</span>`;
    head.onclick = (e) => {
      if (e.target.closest('.ws-acts')) return;
      wsCollapsed[ws] = !collapsed;
      lsSet('jx.wscol', wsCollapsed);
      loadSessions();
    };
    head.querySelector('.wa-add').onclick = (e) => {
      e.stopPropagation();
      homeDraft.workspace = ws;
      persistHomeDraftLS();
      goHomeNewTask();
    };
    head.querySelector('.wa-more').onclick = (e) => {
      e.stopPropagation();
      const r = e.currentTarget.getBoundingClientRect();
      const alias = lsGet('jx.wsalias', {})[ws] || '';
      const acts = [
        { label: '重命名', fn: () => {
          const t = prompt('重命名工作区（仅显示名）', alias || baseName(ws));
          if (t === null) return;
          const m = lsGet('jx.wsalias', {});
          if (t.trim()) m[ws] = t.trim(); else delete m[ws];
          lsSet('jx.wsalias', m);
          loadSessions();
        } },
        { label: '复制路径', fn: () => navigator.clipboard.writeText(ws) },
        { label: '在资源管理器中打开', fn: () => api('/api/open-folder', { method: 'POST', body: JSON.stringify({ path: ws }) }).catch((err) => alert('打开失败：' + err.message)) },
        { label: '移入回收站', danger: true, fn: () => {
          lsSet('jx.wsrecycled', [...new Set([...lsGet('jx.wsrecycled', []), ws])]);
          loadSessions();
        } },
      ];
      openCtxMenu(acts, r.left, r.bottom + 4);
    };
    group.appendChild(head);
    if (!collapsed) {
      const list = document.createElement('div');
      list.className = 'ws-group-list';
      for (const s of items) list.appendChild(sessionItem(s));
      if (!items.length) list.innerHTML = '<div class="ws-empty">（空目录）</div>';
      group.appendChild(list);
    }
    box.appendChild(group);
  }
}

/* ---------- 侧栏收起/展开（顶栏最左按钮，状态落本地） ---------- */

function setSidebarBtn(collapsed) {
  const label = collapsed ? '展开侧边栏' : '收起侧边栏';
  const btn = document.getElementById('btn-sidebar');
  if (btn) btn.title = label;
  const txt = document.getElementById('sb-btn-text');
  if (txt) txt.textContent = label;
}

function applySidebarState() {
  const collapsed = lsGet('jx.sb', '') === '1';
  document.body.classList.toggle('sb-collapsed', collapsed);
  setSidebarBtn(collapsed);
}

function toggleSidebar() {
  const collapsed = document.body.classList.toggle('sb-collapsed');
  setSidebarBtn(collapsed);
  lsSet('jx.sb', collapsed ? '1' : '0');
}

/* ---------- 数字助理按钮的脸部动画 ---------- */

let faceTimer = null;
function setFace(st, revertMs) {
  const f = document.getElementById('da-face');
  if (!f) return;
  f.setAttribute('data-state', st);
  if (faceTimer) { clearTimeout(faceTimer); faceTimer = null; }
  if (revertMs) faceTimer = setTimeout(() => f.setAttribute('data-state', 'idle'), revertMs);
}

/* ---------- 首页（新建对话）---------- */

// 输入卡片的位置：首页时挂在页面中央的问候区里，进对话后回到底部
// 搬移的是 composer-area（工作目录胶囊 + 输入卡），两者始终在一起
function placeComposer() {
  const c = $('composer-area');
  const homeSlot = $('home-composer');
  const wrap = $('composer-wrap');
  if (!state.cur && homeSlot) {
    wrap.classList.add('hidden');
    if (c.parentElement !== homeSlot) homeSlot.appendChild(c);
  } else {
    wrap.classList.remove('hidden');
    if (c.parentElement !== wrap) wrap.appendChild(c);
  }
}

// 把输入卡挪回底部容器（msg-col 重建 innerHTML 前必须先挪出，否则会被一起销毁）
function detachComposerToWrap() {
  const c = document.getElementById('composer-area');
  if (c) $('composer-wrap').appendChild(c);
}

function resetEmpty() {
  detachComposerToWrap();
  $('msg-col').innerHTML =
    '<div class="home">' +
      '<div class="hero">' +
        '<img class="logo-img" src="/logo.png" alt="坚祥数字助理">' +
        '<h1>更人性化的数字助手</h1>' +
        '<span class="pill preview">预览版</span>' +
      '</div>' +
      '<p class="home-sub">描述你想做的事，或直接在左上角进入数字助理主对话。</p>' +
      '<div id="home-composer"></div>' +
    '</div>';
  $('sess-title').textContent = '数字助理';
  $('mode-badge').classList.add('hidden');
  $('btn-drawer').classList.add('hidden');
  $('drawer').classList.add('hidden');
  placeComposer();
  // 恢复上次未发送的首页输入
  if (homeDraft.savedText) {
    $('input').value = homeDraft.savedText;
    homeDraft.savedText = '';
    autoGrow();
  }
  syncComposer();
}

/* ---------- 子对话右栏抽屉：派发的子任务 + 辅助对话 ---------- */

function drawerFamilyId() {
  if (!state.cur) return null;
  return state.cur.parentId || state.cur.id; // 抽屉锚定在主对话（父）上
}

async function refreshDrawer() {
  const fid = drawerFamilyId();
  const drawer = $('drawer');
  if (!fid || drawer.classList.contains('hidden')) return;
  try {
    const d = await api('/api/sessions?parent=' + encodeURIComponent(fid));
    const kids = d.sessions || [];
    const subs = kids.filter((s) => s.mode === 'task');
    const auxs = kids.filter((s) => s.mode === 'aux');
    const renderList = (box, list, emptyText) => {
      box.innerHTML = '';
      if (!list.length) {
        box.innerHTML = `<div class="drawer-empty">${emptyText}</div>`;
        return;
      }
      for (const s of list) {
        const item = document.createElement('div');
        item.className = 'drawer-item' + (state.cur && state.cur.id === s.id ? ' active' : '');
        item.innerHTML =
          `<span class="d-status ${s.status === 'running' ? 'run' : ''}">${s.status === 'running' ? '⟳' : '·'}</span>` +
          `<span class="d-title">${esc(s.title || '(未命名)')}</span>` +
          `<span class="d-time">${esc(relTime(s.updatedAt))}</span>`;
        item.onclick = () => { if (!state.streaming) { openSession(s.id); } };
        box.appendChild(item);
      }
    };
    renderList($('sub-list'), subs, '还没有派发的子任务');
    renderList($('aux-list'), auxs, '还没有辅助对话');
  } catch (e) { /* 静默：抽屉刷新失败不打断主流程 */ }
}

function toggleDrawer() {
  if (!state.cur) return;
  const drawer = $('drawer');
  drawer.classList.toggle('hidden');
  refreshDrawer();
}

async function newAuxChat() {
  const fid = drawerFamilyId();
  if (!fid) return;
  try {
    const ws = (state.cur.parentId && state.cur.workspace) || '';
    const s = await api('/api/sessions', {
      method: 'POST',
      body: JSON.stringify({ mode: 'aux', parent: fid, title: '辅助对话', workspace: ws }),
    });
    await openSession(s.id);
    refreshDrawer();
  } catch (e) {
    alert('新建辅助对话失败：' + e.message);
  }
}

/* ---------- 本地草稿持久化：刷新不丢输入与选择 ---------- */

function sessionDraftKey(id) { return 'jx.st.' + id; }

function persistHomeDraftLS() {
  if (state.cur) return; // 只在首页上下文保存
  try {
    localStorage.setItem('jx.homeDraft', JSON.stringify({ ...homeDraft, text: $('input').value }));
  } catch (e) { /* 隐私模式下忽略 */ }
}

function persistSessionDraftLS() {
  if (!state.cur) return;
  try {
    const v = $('input').value;
    if (v) localStorage.setItem(sessionDraftKey(state.cur.id), v);
    else localStorage.removeItem(sessionDraftKey(state.cur.id));
  } catch (e) {}
}

function clearSessionDraftLS(id) {
  try { localStorage.removeItem(sessionDraftKey(id)); } catch (e) {}
}

function restoreHomeDraftLS() {
  try {
    const d = JSON.parse(localStorage.getItem('jx.homeDraft') || 'null');
    if (!d) return;
    homeDraft.workspace = d.workspace || '';
    homeDraft.permission = d.permission || 'workspace-write';
    homeDraft.provider = d.provider || '';
    homeDraft.model = d.model || '';
    homeDraft.thinking = d.thinking || '';
    homeDraft.tools = Array.isArray(d.tools) ? d.tools : [];
    homeDraft.planMode = !!d.planMode;
    homeDraft.goalMode = !!d.goalMode;
    homeDraft.remote = (d.remote && d.remote.host) ? d.remote : null;
    homeDraft.savedText = typeof d.text === 'string' ? d.text : '';
  } catch (e) {}
}

/* ---------- 会话 ---------- */

async function openAssistant() {
  // 数字助理主对话：复用最近的一个数字助理会话，没有才新建
  const list = state.sessions.filter((s) => s.mode === 'assistant');
  if (list.length) return openSession(list[0].id);
  const s = await api('/api/sessions', { method: 'POST', body: JSON.stringify({ mode: 'assistant', title: '数字助理主对话' }) });
  await loadSessions();
  await openSession(s.id);
}

// 「新工作任务」不立即建会话：留在首页聚焦输入框，发出第一条消息才真正创建
function goHomeNewTask() {
  if (state.streaming) return;
  state.cur = null;
  loadTodos();
  state.msgs = [];
  resetEmpty();
  loadSessions();
  $('input').focus();
}

async function openSession(id) {
  const data = await api('/api/sessions/' + id);
  state.cur = data.meta;
  state.msgs = data.messages || [];
  state.queue = loadQueueLS(id);
  $('sess-title').textContent = state.cur.title || '(未命名)';
  const badge = $('mode-badge');
  if (state.cur.mode === 'assistant') {
    badge.textContent = '数字助理';
    badge.classList.remove('hidden');
  } else if (state.cur.mode === 'aux') {
    badge.textContent = '辅助对话';
    badge.classList.remove('hidden');
  } else {
    badge.classList.add('hidden');
  }
  $('btn-drawer').classList.remove('hidden');
  refreshDrawer();
  placeComposer();
  syncComposer();
  loadTodos();
  renderMessages(state.msgs);
  // 打开即视为已读：清红点与绿点
  delete dotDone[id];
  if (state.cur && state.cur.unread) {
    state.cur.unread = false;
    api('/api/sessions/' + id, { method: 'PATCH', body: JSON.stringify({ unread: false }) }).catch(() => {});
  }
  // 恢复该会话上次未发送的输入
  try {
    const t = localStorage.getItem(sessionDraftKey(id));
    if (t) { $('input').value = t; autoGrow(); }
  } catch (e) {}
  // 上次中断遗留的排队消息：会话空闲时从第一条继续
  if (state.queue.length && !state.streaming) setTimeout(flushQueue, 300);
  loadSessions();
  JX.emit('session:open', state.cur);
}

/* ---------- 消息渲染（回合分组 + 折叠体系，逻辑对齐数字助理2 的 dsh-transcript-fold） ---------- */

// 折叠阈值：流式尾部窗口行数；中间正文豁免阈值（非空行数 / 全角当量字数，全角记 1 半角记 0.5）
const TAIL_ROWS = 10;
const LONG_TEXT_LINES = 3;
const LONG_TEXT_CHARS = 85;

// 回合展开记忆：key = 会话id:回合首条seq。手动展开过的回合不再被自动折回——
// 流式期间新来的思考/工具行、回合收尾重渲都保持展开（数字助理2 修过的同类 bug）
const turnOpen = {};

function isSysNote(txt) {
  return txt.startsWith('【派发回报】') || txt.startsWith('【系统中断】') || txt.startsWith('【执行中断】') || txt.startsWith('【文件上传】');
}

function cjkWidth(t) {
  let w = 0;
  for (const ch of String(t || '')) w += ch.charCodeAt(0) >= 0x2e80 ? 1 : 0.5;
  return w;
}

// 中间正文够长则常显不折叠（其上下各折一条折叠条）
function isLongText(t) {
  t = String(t || '').trim();
  if (!t) return false;
  return t.split('\n').filter((l) => l.trim()).length >= LONG_TEXT_LINES || cjkWidth(t) >= LONG_TEXT_CHARS;
}

function fmtCount(n) { return n >= 1000 ? (n / 1000).toFixed(1).replace(/\.0$/, '') + 'k' : String(n); }

function chev(cls) {
  return '<svg class="' + cls + '" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 9l6 6 6-6"/></svg>';
}

// 折叠容器用 until-found 而非纯 display:none：浏览器 Ctrl+F 命中折叠内容时会自动揭开并触发 beforematch
function hideEl(el) { el.setAttribute('hidden', 'until-found'); }
function showEl(el) { el.removeAttribute('hidden'); }

/* ---------- 过程行：思考行与工具行统一单行样式，点开看详情 ---------- */

function setProcOpen(row, open) {
  row.classList.toggle('open', open);
  const body = row.querySelector('.proc-body');
  if (body) { if (open) showEl(body); else hideEl(body); }
}

function bindProcToggle(row) {
  row.querySelector('.proc-head').onclick = () => setProcOpen(row, !row.classList.contains('open'));
  if (!row.classList.contains('open')) hideEl(row.querySelector('.proc-body'));
}

// 思考行：标题带字数，点开看原文
function thinkRow(text, seq, opts = {}) {
  const row = document.createElement('div');
  row.className = 'proc-row' + (opts.open ? ' open' : '');
  if (seq != null) row.dataset.seq = seq;
  row.innerHTML =
    '<div class="proc-head">' + chev('p-chev') +
    '<span class="p-name">思考</span><span class="p-sub">· ' + fmtCount(String(text).length) + ' 字</span></div>' +
    '<div class="proc-body"><div class="tk-text"></div></div>';
  row.querySelector('.tk-text').textContent = text;
  bindProcToggle(row);
  return row;
}

// 工具行：只显示工具名与运行态，点开看参数/结果（含图片信封与 4000 字截断）
function toolRow(tc, result, opts = {}) {
  const row = document.createElement('div');
  const name = (tc.function && tc.function.name) || '?';
  row.className = 'proc-row' + (result ? '' : ' running');
  if (opts.seq != null) row.dataset.seq = opts.seq;
  row.innerHTML =
    '<div class="proc-head">' + chev('p-chev') +
    '<span class="p-name">' + esc(name) + '</span>' +
    (result ? '' : '<span class="p-sub">· 运行中…</span>') + '</div>' +
    '<div class="proc-body"></div>';
  const body = row.querySelector('.proc-body');
  let argsDisp = (tc.function && tc.function.arguments) || '';
  try { argsDisp = JSON.stringify(JSON.parse(argsDisp), null, 2); } catch (e) { /* 原样显示 */ }
  const cap1 = document.createElement('div');
  cap1.className = 'pb-cap';
  cap1.textContent = '参数';
  const pre1 = document.createElement('pre');
  pre1.textContent = argsDisp || '（无）';
  body.appendChild(cap1);
  body.appendChild(pre1);
  if (result) {
    let out = String(result.content || '');
    const imgFiles = [];
    out = out.replace(/<image attachment="([^"]+)"[^>]*\/>\n?/g, (_, f) => { imgFiles.push(f); return ''; });
    if (out.length > 4000) out = out.slice(0, 4000) + '\n…';
    const cap2 = document.createElement('div');
    cap2.className = 'pb-cap';
    cap2.textContent = '结果';
    const pre2 = document.createElement('pre');
    pre2.textContent = out || '（无输出）';
    body.appendChild(cap2);
    body.appendChild(pre2);
    if (imgFiles.length) {
      const imgs = document.createElement('div');
      imgs.className = 'tc-imgs';
      for (const f of imgFiles) {
        const a = document.createElement('a');
        a.href = '/attachments/' + encodeURIComponent(f);
        a.target = '_blank';
        a.title = '点击查看原图';
        const im = document.createElement('img');
        im.className = 'tc-img';
        im.loading = 'lazy';
        im.alt = '图片附件';
        im.src = '/attachments/' + encodeURIComponent(f);
        a.appendChild(im);
        imgs.appendChild(a);
      }
      body.appendChild(imgs);
    }
  }
  bindProcToggle(row);
  return row;
}

/* ---------- 折叠段：折叠条 + 隐藏容器 ---------- */

function foldLabel(cnt) {
  const parts = [];
  if (cnt.think) parts.push(cnt.think + ' 次思考');
  if (cnt.tool) parts.push(cnt.tool + ' 次工具调用');
  if (!parts.length) parts.push('已折叠 ' + cnt.rows + ' 行');
  return parts.join(' · ');
}

function secCount(items) {
  const cnt = { think: 0, tool: 0, rows: items.length };
  for (const it of items) {
    if (it.k === 'think') cnt.think++;
    else if (it.k === 'tool' || it.k === 'subtask') cnt.tool++;
  }
  return cnt;
}

// 折叠段 = 折叠条按钮 + until-found 隐藏容器；点击只切本段显隐，不触发整列重渲（滚动位置不跳）
function foldSection(items, key, opts = {}) {
  const sec = document.createElement('div');
  sec.className = 'tf-sec';
  if (key) sec.dataset.key = key;
  const bar = document.createElement('button');
  bar.type = 'button';
  bar.className = 'turn-fold';
  const label = document.createElement('span');
  label.className = 'tf-label';
  const cnt = secCount(items);
  label.textContent = opts.prefix
    ? opts.prefix(cnt)
    : foldLabel(cnt);
  bar.appendChild(label);
  bar.insertAdjacentHTML('beforeend', chev('tf-chev'));
  const body = document.createElement('div');
  body.className = 'tf-body';
  for (const it of items) body.appendChild(renderItem(it));
  const open = opts.open === true;
  bar.classList.toggle('open', open);
  if (open) showEl(body); else hideEl(body);
  bar.onclick = () => {
    const nowOpen = body.hasAttribute('hidden');
    setSecOpen(sec, nowOpen);
    if (nowOpen) turnOpen[key] = true; // 手动展开记忆：之后不再自动折回
    else delete turnOpen[key];        // 收起后回到自动折叠
  };
  sec.appendChild(bar);
  sec.appendChild(body);
  return sec;
}

function setSecOpen(sec, open) {
  const bar = sec.querySelector('.turn-fold');
  const body = sec.querySelector('.tf-body');
  if (!bar || !body) return;
  bar.classList.toggle('open', open);
  if (open) showEl(body); else hideEl(body);
}

// Ctrl+F 命中折叠内容时浏览器揭开 hidden，这里同步箭头态（过程行与折叠段都处理）
document.addEventListener('beforematch', (e) => {
  const row = e.target && e.target.closest ? e.target.closest('.proc-row') : null;
  if (row) setProcOpen(row, true);
  const sec = e.target && e.target.closest ? e.target.closest('.tf-sec') : null;
  if (sec) setSecOpen(sec, true);
});

/* ---------- 回合分组与条目渲染 ---------- */

// 按真实用户发言切回合；系统通知（回报/中断/上传）归入当前回合且常显；
// assistant 消息按序拆成思考/正文/工具条目，tool 消息经 resultOf 配对进条目
function groupTurns(msgs, resultOf) {
  const turns = [];
  let cur = null;
  const ensure = () => {
    if (!cur) { cur = { userMsg: null, items: [] }; turns.push(cur); }
  };
  for (const m of msgs) {
    if (m.role === 'user') {
      const txt = String(m.content ?? '');
      if (isSysNote(txt)) { ensure(); cur.items.push({ k: 'note', m }); continue; }
      cur = { userMsg: m, items: [] };
      turns.push(cur);
      continue;
    }
    if (m.role === 'assistant') {
      ensure();
      if (m.thinking) cur.items.push({ k: 'think', seq: m.seq, text: m.thinking });
      if (m.content) cur.items.push({ k: 'text', seq: m.seq, m, text: String(m.content) });
      for (const tc of m.tool_calls || []) {
        const n = (tc.function && tc.function.name) || '';
        cur.items.push({
          k: (n === 'dispatch_task' || n === 'send_to_session') ? 'subtask' : 'tool',
          seq: m.seq, tc, result: resultOf[tc.id],
        });
      }
    }
  }
  return turns;
}

function userBubble(m) {
  const wrap = document.createElement('div');
  wrap.className = 'msg user';
  wrap.dataset.seq = m.seq; // 搜索命中定位用
  const b = document.createElement('div');
  b.className = 'bubble';
  b.textContent = String(m.content ?? '');
  wrap.appendChild(b);
  return wrap;
}

// 悬停操作：复制原文 / 手动压缩 / 插件扩展位
function msgActions(m) {
  const acts = document.createElement('div');
  acts.className = 'msg-actions';
  const cp = document.createElement('button');
  cp.className = 'act-copy';
  const cpOrig = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h10"/></svg>复制';
  cp.innerHTML = cpOrig;
  cp.onclick = () => {
    navigator.clipboard.writeText(String(m.content ?? '')).then(() => {
      cp.textContent = '已复制';
      setTimeout(() => { cp.innerHTML = cpOrig; }, 1200);
    });
  };
  acts.appendChild(cp);
  const cb = document.createElement('button');
  cb.className = 'act-copy act-compact';
  cb.textContent = '压缩';
  cb.title = '把此消息之前的旧对话压缩为一条摘要（最近几轮保持原样）';
  cb.onclick = async () => {
    if (!state.cur || state.streaming) return;
    cb.textContent = '压缩中…';
    cb.disabled = true;
    try {
      await api('/api/sessions/' + state.cur.id + '/compact', { method: 'POST' });
      cb.textContent = '已压缩';
      await openSession(state.cur.id);
      setTimeout(() => { cb.textContent = '压缩'; cb.disabled = false; }, 1500);
    } catch (e) {
      alert('压缩失败：' + e.message);
      cb.textContent = '压缩';
      cb.disabled = false;
    }
  };
  acts.appendChild(cb);
  for (const fn of JX._msgActions) { try { fn(acts, m); } catch (e) { console.warn('[JX] action', e); } }
  return acts;
}

function renderItem(it) {
  if (it.k === 'think') return thinkRow(it.text, it.seq);
  if (it.k === 'tool') return toolRow(it.tc, it.result, { seq: it.seq });
  if (it.k === 'subtask') {
    const w = document.createElement('div');
    if (it.seq != null) w.dataset.seq = it.seq;
    w.appendChild(subTaskCard(it.tc, it.result));
    return w;
  }
  if (it.k === 'text') {
    const wrap = document.createElement('div');
    wrap.className = 'msg assistant';
    wrap.dataset.seq = it.seq; // 搜索命中定位用
    const body = document.createElement('div');
    body.className = 'md';
    body.innerHTML = md(it.text);
    wrap.appendChild(body);
    wrap.appendChild(msgActions(it.m));
    return wrap;
  }
  // 系统通知：常显独立块
  const wrap = document.createElement('div');
  wrap.className = 'msg sysnote';
  const b = document.createElement('div');
  b.className = 'bubble';
  b.textContent = String(it.m.content ?? '');
  wrap.appendChild(b);
  return wrap;
}

/* 数字助理派发/转达的子任务卡片：后台执行期间显示实时工具活动，完成后标已完成 */
const subActs = {}; // 子会话id -> 最近几条工具活动
const subDone = {}; // 子会话id -> 是否已完成（内存态；重载后由回报消息体现）
const dotApproval = {}; // 会话id -> 待审批（橙点）
const dotDone = {};     // 会话id -> 完成未查看（绿点）

function subActNode(sid) {
  return document.querySelector(`.subtask-card[data-sid="${sid}"] .st-act`);
}

function renderSubActs(sid) {
  const node = subActNode(sid);
  if (!node) return;
  const arr = subActs[sid] || [];
  node.textContent = arr.length ? arr.join('\n') : '正在准备…';
}

function pushSubAct(ev) {
  const arr = (subActs[ev.sessionId] = subActs[ev.sessionId] || []);
  arr.push(ev.output ? `✓ ${ev.name}` : `→ ${ev.name} ${String(ev.args || '').slice(0, 60)}`);
  if (arr.length > 6) arr.shift();
  renderSubActs(ev.sessionId);
}

function bindOrphanCard(sid) {
  // 运行中的卡片此刻还没有会话 id（id 要等工具结果才能解析出来），
  // 后端按顺序执行派发，这里按到达顺序绑定
  const orphan = Array.from(document.querySelectorAll('.subtask-card.running'))
    .find((c) => !c.dataset.sid);
  if (orphan) orphan.dataset.sid = sid;
}

function subTaskCard(tc, result) {
  let args = {};
  try { args = JSON.parse(tc.function && tc.function.arguments) || {}; } catch (e) { /* 参数原样忽略 */ }
  const out = String((result && result.content) || '');
  // 后台派发：立即返回文本里带子会话 id；旧版同步结果则以「已完成」呈现
  const sidMatch = out.match(/已派发后台执行：会话 (\S+)「/) || out.match(/^会话ID:\s*(\S+)$/m);
  const sid = sidMatch ? sidMatch[1] : '';
  const dispatched = out.includes('已派发后台执行');
  const done = subDone[sid] || (result && !dispatched);
  const running = !done;
  const title = args.title || String(args.task || '').slice(0, 30) || '子任务';
  const d = document.createElement('div');
  d.className = 'subtask-card ' + (done ? 'done' : 'running');
  if (sid) d.dataset.sid = sid;
  d.innerHTML =
    `<div class="st-head">` +
    `<span class="st-icon">${done ? '✓' : '⟳'}</span>` +
    `<span class="st-title">${esc(title)}</span>` +
    `<span class="st-status">${done ? '已完成' : (dispatched ? '后台执行中' : '派发中…')}</span>` +
    `</div>` +
    (running ? `<pre class="st-act">正在准备…</pre>` : '') +
    (sid ? `<div class="st-foot"><button class="st-open">${running ? '查看会话' : '打开会话'}</button></div>` : '') +
    (done && !dispatched && out ? `<details class="st-detail"><summary>执行摘要</summary><pre>${esc(out)}</pre></details>` : '');
  const openBtn = d.querySelector('.st-open');
  if (openBtn) openBtn.onclick = () => { if (!state.streaming) openSession(sid); };
  if (running) renderSubActs(sid);
  return d;
}

function renderMessages(msgs) {
  const box = $('msg-col');
  box.innerHTML = '';
  if (!msgs.length) {
    box.innerHTML =
      '<div class="empty"><h2>开始新的对话</h2><p>在下方输入框发起任务；数字助理会自动派发子任务执行。<br>' +
      'API 地址填 <code>mock</code> 可离线体验完整链路。</p></div>';
    return;
  }
  const resultOf = {};
  for (const m of msgs) {
    if (m.role === 'tool' && m.tool_call_id) resultOf[m.tool_call_id] = m;
  }
  const sid = (state.cur && state.cur.id) || '';
  const turns = groupTurns(msgs, resultOf);
  turns.forEach((turn, ti) => {
    if (turn.userMsg) box.appendChild(userBubble(turn.userMsg));
    const key = sid + ':' + (turn.userMsg ? turn.userMsg.seq : 'head' + ti);
    if (ti === turns.length - 1 && state.streaming) renderStreamingTurn(box, turn, key);
    else renderFinishedTurn(box, turn, key);
  });
  // 等待队列：本轮执行中排入的消息，徽标 + 可移出
  state.queue.forEach((q, qi) => {
    const wrap = document.createElement('div');
    wrap.className = 'msg user queued';
    const b = document.createElement('div');
    b.className = 'bubble';
    const badge = document.createElement('span');
    badge.className = 'q-badge';
    badge.textContent = '排队中';
    b.appendChild(badge);
    b.appendChild(document.createTextNode(q));
    const x = document.createElement('button');
    x.className = 'q-x';
    x.title = '移出队列';
    x.textContent = '✕';
    x.onclick = () => { state.queue.splice(qi, 1); saveQueueLS(); renderMessages(state.msgs); };
    b.appendChild(x);
    wrap.appendChild(b);
    box.appendChild(wrap);
  });
  $('messages').scrollTop = $('messages').scrollHeight;
}

// 已完成回合：最后一段正文 = 最终输出，常显；中间长正文与系统通知也常显；
// 其余过程按相邻常显块切分成折叠段（每段一条折叠条，显示思考/工具计数）
function renderFinishedTurn(box, turn, key) {
  const items = turn.items;
  let answerIdx = -1;
  items.forEach((it, i) => { if (it.k === 'text') answerIdx = i; });
  const anchors = new Set();
  if (answerIdx >= 0) anchors.add(answerIdx);
  items.forEach((it, i) => {
    if (i === answerIdx) return;
    if (it.k === 'note' || (it.k === 'text' && isLongText(it.text))) anchors.add(i);
  });
  let seg = [];
  const flushSeg = () => {
    if (!seg.length) return;
    box.appendChild(foldSection(seg, key, { open: turnOpen[key] === true }));
    seg = [];
  };
  items.forEach((it, i) => {
    if (anchors.has(i)) { flushSeg(); box.appendChild(renderItem(it)); }
    else seg.push(it);
  });
  flushSeg();
}

// 进行中回合：过程行只露最近 TAIL_ROWS 行（尾部窗口），上方折叠成一条"已折叠 N 行"折叠条；
// 运行中的子任务卡钉在窗口上方常显（实时进度不中断）；手动展开过则全部显示
function renderStreamingTurn(box, turn, key) {
  const items = turn.items;
  if (items.length <= TAIL_ROWS) {
    items.forEach((it) => box.appendChild(renderItem(it)));
    return;
  }
  const foldFrom = items.length - TAIL_ROWS;
  const segItems = [];
  const pinned = [];
  items.forEach((it, i) => {
    if (i >= foldFrom) return;
    if (it.k === 'subtask' && !it.result) pinned.push(it);
    else segItems.push(it);
  });
  if (segItems.length) {
    box.appendChild(foldSection(segItems, key, {
      open: turnOpen[key] === true,
      prefix: (c) => '已折叠 ' + c.rows + ' 行' + ((c.think || c.tool) ? ' · ' + foldLabel(c) : ''),
    }));
  }
  pinned.forEach((it) => box.appendChild(renderItem(it)));
  items.slice(foldFrom).forEach((it) => box.appendChild(renderItem(it)));
}

let rafPending = false;
function liveRender() {
  if (rafPending) return;
  rafPending = true;
  requestAnimationFrame(() => {
    rafPending = false;
    let el = document.getElementById('live-bubble');
    if (!el) {
      el = document.createElement('div');
      el.className = 'msg assistant';
      el.id = 'live-bubble';
      el.innerHTML =
        '<div class="proc-row running open" id="live-think">' +
          '<div class="proc-head">' + chev('p-chev') + '<span class="p-name">思考中…</span></div>' +
          '<div class="proc-body"><div class="tk-text"></div></div>' +
        '</div><div class="md"></div>';
      $('msg-col').appendChild(el);
      bindProcToggle(el.querySelector('#live-think'));
    }
    const tk = el.querySelector('#live-think');
    const thinking = state.streamThinking || '';
    if (thinking) {
      tk.style.display = '';
      const tkt = tk.querySelector('.tk-text');
      tkt.textContent = thinking;
      tkt.scrollTop = tkt.scrollHeight; // 长思考自动滚到最新
    } else {
      tk.style.display = 'none';
    }
    el.querySelector('.md').innerHTML = md(state.streamText);
    $('messages').scrollTop = $('messages').scrollHeight;
  });
}

/* ---------- 全局事件流（/api/events）：后台派发进度与回报的统一入口 ---------- */

let eventStream = null;

function connectEvents() {
  try {
    eventStream = new EventSource('/api/events');
    eventStream.onmessage = (e) => {
      try { handleGlobalEvent(JSON.parse(e.data)); } catch (err) { /* 跳过坏帧 */ }
    };
  } catch (e) { /* 不支持 SSE 时退化为手动刷新 */ }
}

function removeLive() {
  const live = document.getElementById('live-bubble');
  if (live) live.remove();
}

function refreshCurrent() {
  if (state.cur && !state.streaming) openSession(state.cur.id);
}

function handleGlobalEvent(ev) {
  const viewing = state.cur && ev.sessionId === state.cur.id;
  if (ev.type === 'approval_required') { dotApproval[ev.sessionId] = true; loadSessions(); }
  if (ev.type === 'approval_resolved') { delete dotApproval[ev.sessionId]; loadSessions(); }
  switch (ev.type) {
    case 'sub_start':
      subActs[ev.sessionId] = [];
      delete subDone[ev.sessionId];
      bindOrphanCard(ev.sessionId);
      renderSubActs(ev.sessionId);
      loadSessions();
      refreshDrawer();
      break;
    case 'sub_tool':
      pushSubAct(ev);
      break;
    case 'sub_end':
      subDone[ev.sessionId] = true;
      dotDone[ev.sessionId] = true;
      loadSessions();
      refreshDrawer();
      if (!state.streaming) renderMessages(state.msgs); // 卡片状态就地翻新
      break;
    case 'delta':
      // 自动汇报轮的流式输出（当前正看这个会话且本地没有流在进行）
      if (viewing && !state.streaming) { state.streamText += ev.delta; liveRender(); }
      if (state.cur && state.cur.mode === 'assistant') setFace('thinking');
      break;
    case 'reasoning':
      if (viewing && !state.streaming) { state.streamThinking = (state.streamThinking || '') + ev.delta; liveRender(); }
      break;
    case 'message':
      if (viewing && !state.streaming && ev.message && ev.message.role === 'user') refreshCurrent();
      break;
    case 'done':
      if (viewing && !state.streaming) { state.streamText = ''; state.streamThinking = ''; removeLive(); refreshCurrent(); }
      if (state.cur && state.cur.mode === 'assistant') setFace('done', 4000);
      break;
    case 'report_ready':
    case 'session_updated':
      if (!viewing && ev.type === 'session_updated') dotDone[ev.sessionId] = dotDone[ev.sessionId] || aRunJustEnded(ev.sessionId);
      loadSessions().then(() => {
        // 权限被审批提升（如「始终允许」把会话切到完全访问）时，
        // 流式中的当前会话也立即同步权限芯片/面板
        if (state.cur && state.cur.id === ev.sessionId && state.streaming) {
          const m = (state.sessions || []).find((s) => s.id === ev.sessionId);
          if (m) {
            state.cur.permission = m.permission;
            state.cur.tools = m.tools;
            syncComposer();
          }
        }
      });
      refreshDrawer();
      if (viewing && !state.streaming) refreshCurrent();
      break;
    case 'approval_required':
      pushApproval(ev);
      break;
    case 'approval_resolved':
      removeApproval(ev.approvalId);
      break;
    case 'question_required':
      pushQuestion(ev);
      break;
    case 'question_resolved':
      removeQuestion(ev.questionId);
      break;
    case 'todos':
      loadTodos();
      break;
    case 'error':
      if (viewing && !state.streaming) refreshCurrent();
      break;
  }
}

/* ---------- 审批卡片（沙箱升级等越权动作：允许 / 始终允许 / 拒绝） ---------- */

function pushApproval(ev) {
  if (!ev.approvalId || state.approvals.some((a) => a.id === ev.approvalId)) return;
  state.approvals.push({
    id: ev.approvalId, sessionId: ev.sessionId || '',
    tool: ev.name || '', summary: ev.summary || '', reason: ev.reason || '',
    createdAt: ev.createdAt || '',
  });
  renderApprovals();
}

function removeApproval(id) {
  state.approvals = state.approvals.filter((a) => a.id !== id);
  renderApprovals();
}

async function loadApprovals() {
  try {
    const d = await api('/api/approvals');
    state.approvals = d.approvals || [];
    renderApprovals();
  } catch (e) { /* 服务离线时忽略 */ }
}

async function decideApproval(id, approved, always) {
  removeApproval(id); // 先乐观移除，避免双击
  try {
    await api('/api/approvals/' + id, { method: 'POST', body: JSON.stringify({ approved, always }) });
  } catch (e) {
    $('hint').textContent = '审批失败：' + e.message;
    setTimeout(() => { $('hint').textContent = ''; }, 3000);
  }
}

function renderApprovals() {
  const bar = $('approval-bar');
  if (!bar) return;
  bar.innerHTML = '';
  if (!state.approvals.length && !state.questions.length) { bar.classList.add('hidden'); return; }
  bar.classList.remove('hidden');
  renderQuestionsInto(bar);
  for (const ap of state.approvals) {
    const card = document.createElement('div');
    card.className = 'ap-card';
    const sessTitle = (state.cur && state.cur.id === ap.sessionId)
      ? (state.cur.title || '')
      : ((state.sessions.find((s) => s.id === ap.sessionId) || {}).title || '');
    card.innerHTML =
      '<div class="ap-head"><b>需要批准</b>' + (sessTitle ? '<span>' + esc(sessTitle) + '</span>' : '') + '</div>' +
      '<div class="ap-body"><code>' + esc(ap.summary || '') + '</code>' +
      (ap.reason ? '<p>' + esc(ap.reason) + '</p>' : '') + '</div>' +
      '<div class="ap-actions">' +
      '<button class="ok" data-act="ok">允许</button>' +
      '<button class="ok always" data-act="always" title="常设授权：命令升级点亮对应能力开关；工作区外写入会把会话切到「完全访问」（三项能力捆绑点亮），此后不再逐次审批">始终允许</button>' +
      '<button class="deny" data-act="deny">拒绝</button>' +
      '</div>';
    card.querySelectorAll('button').forEach((b) => {
      b.onclick = () => {
        const act = b.dataset.act;
        decideApproval(ap.id, act !== 'deny', act === 'always');
      };
    });
    bar.appendChild(card);
  }
}

/* ---------- 提问 / 计划审批卡片（ask_user_question / exit_plan_mode） ---------- */

function pushQuestion(ev) {
  if (!ev.questionId || state.questions.some((q) => q.id === ev.questionId)) return;
  state.questions.push({
    id: ev.questionId, sessionId: ev.sessionId || '', kind: ev.kind || 'ask',
    question: ev.summary || '', plan: ev.plan || '',
    options: ev.options || [],
  });
  renderApprovals(); // 复用同一容器统一重画
}

function removeQuestion(id) {
  state.questions = state.questions.filter((q) => q.id !== id);
  renderApprovals();
}

async function loadQuestions() {
  try {
    const d = await api('/api/questions');
    state.questions = (d.questions || []).map((q) => ({
      id: q.id, sessionId: q.sessionId, kind: q.kind || 'ask',
      question: q.question || '', plan: q.plan || '', options: q.options || [],
    }));
    renderApprovals();
  } catch (e) { /* 服务离线时忽略 */ }
}

async function decideQuestion(id, answer) {
  removeQuestion(id); // 先乐观移除，避免双击
  try {
    await api('/api/questions/' + id, { method: 'POST', body: JSON.stringify({ answer }) });
  } catch (e) {
    $('hint').textContent = '提交失败：' + e.message;
    setTimeout(() => { $('hint').textContent = ''; }, 3000);
  }
}

function renderQuestionsInto(bar) {
  for (const q of state.questions) {
    const card = document.createElement('div');
    card.className = 'ap-card q-card' + (q.kind === 'plan' ? ' plan' : '');
    const isPlan = q.kind === 'plan';
    let body = '';
    if (isPlan) {
      body = '<pre class="q-plan">' + esc(q.plan || '') + '</pre>' +
        '<div class="q-free"><input type="text" placeholder="反馈修改意见后点「驳回」，或直接批准"><button class="ghost" data-free>驳回</button></div>' +
        '<div class="ap-actions"><button class="ok" data-opt="__approve__">批准并开始执行</button></div>';
    } else {
      const opts = (q.options || []).map((o, i) =>
        '<button class="opt" data-i="' + i + '">' + esc(o) + '</button>').join('');
      body = '<div class="ap-body"><p>' + esc(q.question || '') + '</p></div>' +
        (opts ? '<div class="q-opts">' + opts + '</div>' : '') +
        '<div class="q-free"><input type="text" placeholder="' + (opts ? '或输入自定义回答' : '输入你的回答') + '"><button class="ghost" data-free>发送</button></div>';
    }
    card.innerHTML = '<div class="ap-head"><b>' + (isPlan ? '计划审批' : '助手提问') + '</b></div>' + body;
    card.querySelectorAll('button[data-opt]').forEach((b) => {
      b.onclick = () => decideQuestion(q.id, b.dataset.opt);
    });
    card.querySelectorAll('button.opt').forEach((b) => {
      b.onclick = () => decideQuestion(q.id, q.options[parseInt(b.dataset.i, 10)]);
    });
    const free = card.querySelector('.q-free');
    if (free) {
      const send = () => {
        const v = free.querySelector('input').value.trim();
        if (!v) return;
        decideQuestion(q.id, isPlan ? v : v);
      };
      free.querySelector('button').onclick = send;
      free.querySelector('input').addEventListener('keydown', (e) => { if (e.key === 'Enter') send(); });
    }
    bar.appendChild(card);
  }
}

/* ---------- 待办面板（todo_write） ---------- */

async function loadTodos() {
  const panel = $('todo-panel');
  if (!panel) return;
  if (!state.cur) { panel.classList.add('hidden'); panel.innerHTML = ''; return; }
  try {
    const d = await api('/api/sessions/' + state.cur.id + '/todos');
    const todos = d.todos || [];
    if (!todos.length) { panel.classList.add('hidden'); panel.innerHTML = ''; return; }
    const done = todos.filter((t) => t.status === 'completed').length;
    let html = '<div class="todo-head"><b>待办</b><span>' + done + '/' + todos.length + '</span></div><ul>';
    for (const t of todos) {
      const mark = t.status === 'completed' ? '✔' : (t.status === 'in_progress' ? '▶' : '·');
      html += '<li class="td-' + (t.status || 'pending') + '"><span class="td-mark">' + mark + '</span>' + esc(t.content) + '</li>';
    }
    html += '</ul>';
    panel.innerHTML = html;
    panel.classList.remove('hidden');
  } catch (e) { /* 会话不存在等情况忽略 */ }
}

/* ---------- SSH 远程工作区（两步式：连接配置 → 保存 → 浏览目录 → 使用） ---------- */

let rmSavedSpec = null; // 第一步「保存」的连接配置（本轮流程内有效）
let rmCurPath = '';     // 目录浏览器当前所在路径（即选中项）
let rmHomePath = '/';   // 远程用户主目录（后端探测）

function remoteFormPrefill() {
  // 优先级：当前会话 > 首页草稿 > 本机记住的连接配置（密码不落盘）
  const saved = lsGet('jx.rmDraft', null) || {};
  const r = (state.cur && state.cur.remote) || homeDraft.remote || saved;
  rmSavedSpec = null;
  $('rm-host').value = r.host || '';
  $('rm-port').value = r.port && r.port !== 22 ? r.port : '';
  $('rm-user').value = r.user || '';
  $('rm-auth').value = r.auth || 'key';
  $('rm-key').value = r.keyPath || '';
  $('rm-pass').value = r.password || '';
  $('rm-dir').value = (state.cur && state.cur.remote && state.cur.remote.dir) || r.dir || '';
  $('rm-result').textContent = '';
  $('rm-result').className = 'remote-result';
  $('rm-ls-status').textContent = '';
  $('rm-ls-status').className = 'remote-result';
  $('rm-clear').style.display = ((state.cur && state.cur.remote) || homeDraft.remote) ? '' : 'none';
  syncRemoteFormVis();
  showRmStep(1);
}

function showRmStep(n) {
  $('rm-step1').classList.toggle('hidden', n !== 1);
  $('rm-step2').classList.toggle('hidden', n !== 2);
  $('rm-title').textContent = n === 1 ? '添加远程工作目录 · 连接配置' : '选择远程工作目录';
  // 两步内容高度不同，弹窗重新对齐锚点
  if (!$('remote-pop').classList.contains('hidden')) {
    const anchor = $('remote-badge').classList.contains('hidden') ? $('btn-plus') : $('remote-badge');
    openPopover('remote-pop', anchor);
  }
  if (n === 2) browseRemote($('rm-dir').value.trim());
}

function syncRemoteFormVis() {
  const key = $('rm-auth').value === 'key';
  $('rm-key').style.display = key ? '' : 'none';
  $('rm-pass').style.display = key ? 'none' : '';
}

function collectRemoteSpec() {
  return {
    host: $('rm-host').value.trim(),
    port: parseInt($('rm-port').value, 10) || 22,
    user: $('rm-user').value.trim(),
    auth: $('rm-auth').value,
    keyPath: $('rm-key').value.trim(),
    password: $('rm-pass').value,
    dir: $('rm-dir').value.trim(),
  };
}

function validateRemoteSpec(spec) {
  if (!spec.host) return '请先填写主机';
  if (spec.auth === 'key' && !spec.keyPath) return '私钥文件路径必填（或改用密码认证）';
  if (spec.auth === 'password' && !spec.password) return '密码必填';
  return '';
}

function showRemoteResult(ok, msg, hostKey) {
  const el = $('rm-result');
  el.className = 'remote-result ' + (ok ? 'ok' : 'bad');
  el.textContent = msg + (ok && hostKey ? '｜主机指纹 ' + hostKey : '');
}

function showRmLs(msg, bad) {
  const el = $('rm-ls-status');
  el.className = 'remote-result' + (bad ? ' bad' : '');
  el.textContent = msg;
}

// saveRemote 第一步「保存」：校验并记住连接配置，进入目录选择。
function saveRemote() {
  const spec = collectRemoteSpec();
  const bad = validateRemoteSpec(spec);
  if (bad) { showRemoteResult(false, bad); return; }
  rmSavedSpec = spec;
  lsSet('jx.rmDraft', { ...spec, password: '' }); // 本机记住连接配置（密码不落盘）
  showRemoteResult(true, '已保存连接配置，正在连接 ' + spec.host + ' …');
  showRmStep(2);
}

async function testRemote() {
  const spec = collectRemoteSpec();
  if (!spec.host) { showRemoteResult(false, '请先填写主机'); return; }
  const btn = $('rm-test');
  btn.disabled = true; btn.textContent = '测试中…';
  try {
    const d = await api('/api/workspace/test', { method: 'POST', body: JSON.stringify({ remote: spec }) });
    showRemoteResult(!!d.ok, d.message || (d.ok ? '连接成功' : '失败'), d.hostKey);
  } catch (e) {
    showRemoteResult(false, e.message);
  }
  btn.disabled = false; btn.textContent = '测试连接';
}

// browseRemote 列出远程目录；path 为空 = 回到用户主目录。
async function browseRemote(path) {
  const spec = rmSavedSpec || collectRemoteSpec();
  const bad = validateRemoteSpec(spec);
  if (bad) { showRmStep(1); showRemoteResult(false, bad); return; }
  showRmLs('连接中…');
  const list = $('rm-dir-list');
  list.innerHTML = '<div class="rm-empty">加载中…</div>';
  try {
    const d = await api('/api/workspace/ls', { method: 'POST', body: JSON.stringify({ remote: spec, path: path || '' }) });
    if (!d.ok) {
      list.innerHTML = '';
      showRmLs(d.message || '读取目录失败', true);
      return;
    }
    rmCurPath = d.path;
    rmHomePath = d.home || '/';
    $('rm-dir').value = d.path;
    showRmLs('');
    renderDirList(d.entries || []);
  } catch (e) {
    list.innerHTML = '';
    showRmLs('连接失败：' + e.message, true);
  }
}

function renderDirList(entries) {
  const list = $('rm-dir-list');
  list.innerHTML = '';
  const byName = (a, b) => a.name.localeCompare(b.name, 'zh-Hans-CN');
  const dirs = entries.filter((e) => e.isDir).sort(byName);
  const files = entries.filter((e) => !e.isDir).sort(byName);
  const folderSvg = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/></svg>';
  const fileSvg = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6"/></svg>';
  const addRow = (e) => {
    const row = document.createElement('div');
    row.className = 'rm-entry' + (e.isDir ? '' : ' file');
    row.innerHTML = (e.isDir ? folderSvg : fileSvg) + '<span>' + esc(e.name) + (e.isDir ? '/' : '') + '</span>';
    row.dataset.path = rmCurPath === '/' ? '/' + e.name : rmCurPath + '/' + e.name;
    list.appendChild(row);
  };
  dirs.forEach(addRow);
  files.forEach(addRow);
  if (!entries.length) list.innerHTML = '<div class="rm-empty">（空目录）</div>';
  list.scrollTop = 0;
}

// applyRemote 第二步「使用」：以当前所在目录为工作区落库/入草稿。
async function applyRemote() {
  const spec = rmSavedSpec || collectRemoteSpec();
  const bad = validateRemoteSpec(spec);
  if (bad) { showRmStep(1); showRemoteResult(false, bad); return; }
  const dir = ($('rm-dir').value || '').trim() || rmCurPath;
  if (!dir.startsWith('/')) { showRmLs('请先在上方选择或输入一个远程目录', true); return; }
  const full = { ...spec, dir };
  if (state.cur) {
    try {
      state.cur = await api('/api/sessions/' + state.cur.id + '/remote', { method: 'POST', body: JSON.stringify({ remote: full }) });
    } catch (e) { showRmLs('保存失败：' + e.message, true); return; }
  } else {
    homeDraft.remote = full;
  }
  closePopovers();
  syncComposer();
  loadSessions();
}

async function clearRemote() {
  if (state.cur) {
    try {
      state.cur = await api('/api/sessions/' + state.cur.id + '/remote', { method: 'POST', body: JSON.stringify({ clear: true }) });
    } catch (e) { alert('解除失败：' + e.message); return; }
  } else {
    homeDraft.remote = null;
  }
  lsSet('jx.rmDraft', null);
  closePopovers();
  syncComposer();
  loadSessions();
}

/* ---------- 发送与事件流 ---------- */

function setStreamUI(on) {
  // 发送按钮保持可用：执行中发送 = 插话或排队（常规页「对话中途发送行为」）
  $('btn-stop').classList.toggle('hidden', !on);
  if (!on) $('hint').textContent = '';
}

function handleEvent(ev) {
  switch (ev.type) {
    case 'message': {
      state.streamText = '';
      state.streamThinking = '';
      const live = document.getElementById('live-bubble');
      if (live) live.remove();
      // 后端 message 事件带该步思考原文与消息 seq：
      // 思考块实时渲染不消失（工具调用落地后仍在），seq 用作回合标识与搜索定位
      if (ev.thinking) ev.message.thinking = ev.thinking;
      if (ev.seq) ev.message.seq = ev.seq;
      state.msgs.push(ev.message);
      renderMessages(state.msgs);
      break;
    }
    case 'delta':
      state.streamText += ev.delta;
      liveRender();
      if (state.cur && state.cur.mode === 'assistant') setFace('thinking');
      break;
    case 'reasoning': // 思考增量：实时显示在折叠块里
      state.streamThinking = (state.streamThinking || '') + ev.delta;
      liveRender();
      if (state.cur && state.cur.mode === 'assistant') setFace('thinking');
      break;
    case 'tool_start':
      break;
    case 'tool_wait':
      $('hint').textContent = ev.output || '等待用户批准…';
      break;
    case 'tool_end':
      break;
    case 'sub_start':
      // 主对话 SSE 里的派发确认；后续进度统一走全局事件流
      subActs[ev.sessionId] = [];
      bindOrphanCard(ev.sessionId);
      $('hint').textContent = `子任务「${ev.title}」已在后台执行，完成后自动回报…`;
      loadSessions();
      break;
    case 'sub_end':
      $('hint').textContent = '';
      break;
    case 'title':
      if (state.cur && ev.sessionId === state.cur.id && state.cur.title !== '数字助理主对话') {
        $('sess-title').textContent = ev.title;
      }
      break;
    case 'error': {
      state.streamText = '';
      state.streamThinking = '';
      const live = document.getElementById('live-bubble');
      if (live) live.remove();
      renderMessages(state.msgs);
      const box = $('msg-col');
      const err = document.createElement('div');
      err.className = 'msg assistant';
      err.innerHTML = `<div class="md"><p style="color:#c22f2f">⚠ ${esc(ev.error)}</p></div>`;
      box.appendChild(err);
      break;
    }
    case 'done':
      $('hint').textContent = '';
      if (state.cur && state.cur.mode === 'assistant') setFace('done', 4000);
      break;
  }
}

function queueKey(sid) { return 'jx.queue.' + sid; }

function loadQueueLS(sid) {
  try { return JSON.parse(localStorage.getItem(queueKey(sid)) || '[]'); } catch (e) { return []; }
}

function saveQueueLS() {
  if (!state.cur) return;
  try { localStorage.setItem(queueKey(state.cur.id), JSON.stringify(state.queue)); } catch (e) { /* 忽略 */ }
}

// 等待模式：本轮结束后从队列取下一条串行发送
async function flushQueue() {
  if (!state.cur || state.streaming || !state.queue.length) return;
  const next = state.queue.shift();
  saveQueueLS();
  renderMessages(state.msgs);
  await send(next);
}

// 执行中发出的消息：按「对话中途发送行为」插话或排队
async function sendMidrun(content, viaInput) {
  if (lsGet('jx.midrun', 'interject') === 'queue') {
    state.queue.push(content);
    saveQueueLS();
    if (viaInput) { $('input').value = ''; autoGrow(); if (state.cur) clearSessionDraftLS(state.cur.id); }
    renderMessages(state.msgs);
    return;
  }
  try {
    const d = await api('/api/sessions/' + state.cur.id + '/interject', {
      method: 'POST', body: JSON.stringify({ content }),
    });
    if (d.accepted) {
      // 消息本身不在这里入列：后端在注入时会通过聊天流回推 message 事件，
      // 本地再 push 会显示两条。
    } else {
      // 刚好执行结束的竞态：转入排队，稍后自动续发
      state.queue.push(content);
      saveQueueLS();
      renderMessages(state.msgs);
      setTimeout(flushQueue, 200);
    }
  } catch (e) {
    alert('插话失败：' + e.message);
    return;
  }
  if (viaInput) { $('input').value = ''; autoGrow(); if (state.cur) clearSessionDraftLS(state.cur.id); }
  renderMessages(state.msgs);
}

async function send(forceContent) {
  const input = $('input');
  const fromInput = typeof forceContent !== 'string';
  const content = (fromInput ? input.value : forceContent).trim();
  if (!content) return;
  if (state.streaming) { await sendMidrun(content, fromInput); return; }
  if (!state.cur) {
    // 懒创建：发出第一条消息才真正建会话（侧栏不再出现空会话，首页不再一闪而过）
    const s = await api('/api/sessions', {
      method: 'POST',
      body: JSON.stringify({
        mode: 'task',
        workspace: homeDraft.workspace || '',
        remote: homeDraft.remote || null,
        permission: homeDraft.permission,
        provider: homeDraft.provider,
        model: homeDraft.model,
        thinking: homeDraft.thinking || '',
        planMode: homeDraft.planMode,
        goalMode: homeDraft.goalMode,
        tools: homeDraft.tools,
      }),
    });
    state.cur = s;
    $('btn-drawer').classList.remove('hidden');
    placeComposer();
    syncComposer();
    refreshDrawer();
    loadSessions();
  }
  if (fromInput) {
    input.value = '';
    autoGrow();
    clearSessionDraftLS(state.cur.id);
  }

  state.streaming = true;
  setStreamUI(true);
  state.abort = new AbortController();
  state.streamText = '';
  state.streamThinking = '';
  JX.emit('send', { content });

  let aborted = false;
  try {
    const res = await fetch(`/api/sessions/${state.cur.id}/chat`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ content }),
      signal: state.abort.signal,
    });
    if (!res.ok) {
      const d = await res.json().catch(() => ({}));
      throw new Error(d.error || res.statusText);
    }
    const reader = res.body.getReader();
    const dec = new TextDecoder();
    let buf = '';
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buf += dec.decode(value, { stream: true });
      let idx;
      while ((idx = buf.indexOf('\n\n')) >= 0) {
        const frame = buf.slice(0, idx).trim();
        buf = buf.slice(idx + 2);
        if (!frame.startsWith('data:')) continue;
        try { handleEvent(JSON.parse(frame.slice(5))); } catch (e) { /* 跳过坏帧 */ }
      }
    }
  } catch (e) {
    if (e.name === 'AbortError') {
      aborted = true; // 用户主动停止：排队消息保留，不自动续发
    } else {
      state.streamText = '';
      state.streamThinking = '';
      renderMessages(state.msgs);
      const box = $('msg-col');
      const err = document.createElement('div');
      err.className = 'msg assistant';
      err.innerHTML = `<div class="md"><p style="color:#c22f2f">请求失败：${esc(e.message)}</p></div>`;
      box.appendChild(err);
    }
  }
  const live = document.getElementById('live-bubble');
  if (live) live.remove();
  // 先收流式标记再重渲：最后一帧要按"已完成回合"折叠呈现，而不是停留在流式尾部窗口
  state.streaming = false;
  renderMessages(state.msgs);
  setStreamUI(false);
  loadSessions();
  loadStatus();
  JX.emit('message:done', { sessionId: state.cur && state.cur.id });
  if (!aborted) flushQueue(); // 等待模式：本轮彻底结束，取下一条排队消息
}

/* ---------- 模型选择（发送按钮左侧的芯片 + 弹出面板） ---------- */

async function loadModels() {
  try {
    const d = await api('/api/models');
    state.models = d.models || [];
    state.defaultProvider = d.defaultProvider || '';
    state.defaultModel = d.defaultModel || '';
    syncComposer();
  } catch (e) { state.models = []; }
}

function currentModelKey() {
  if (state.cur) {
    return state.cur.provider && state.cur.model
      ? state.cur.provider + '|' + state.cur.model : '__default__';
  }
  return homeDraft.provider && homeDraft.model
    ? homeDraft.provider + '|' + homeDraft.model : '__default__';
}

function modelNameOf(key) {
  if (key === '__default__') return state.defaultModel || '默认模型';
  const [pid, mid] = key.split('|');
  const hit = (state.models || []).find((m) => m.providerId === pid && m.modelId === mid);
  return (hit && (hit.modelName || hit.modelId)) || mid;
}

const THINK_LABELS = { enabled: '思考·开', disabled: '思考·关', low: '思考·低', medium: '思考·中', high: '思考·高' };

// 当前生效的思考档位（会话级或首页草稿）
function curThinking() {
  return state.cur ? (state.cur.thinking || '') : (homeDraft.thinking || '');
}

async function pickModel(key, keepOpen) {
  if (!keepOpen) closePopovers();
  if (state.cur) {
    const body = key === '__default__' ? { provider: '', model: '' } : {
      provider: key.split('|')[0], model: key.split('|')[1],
    };
    try {
      state.cur = await api('/api/sessions/' + state.cur.id, { method: 'PATCH', body: JSON.stringify(body) });
    } catch (e) { alert('切换模型失败：' + e.message); }
  } else if (key === '__default__') {
    homeDraft.provider = ''; homeDraft.model = '';
  } else {
    homeDraft.provider = key.split('|')[0];
    homeDraft.model = key.split('|')[1];
  }
  syncComposer();
}

// 设置思考档位（空串=跟随服务端默认）
async function pickThinking(level) {
  closePopovers();
  if (state.cur) {
    try {
      state.cur = await api('/api/sessions/' + state.cur.id, { method: 'PATCH', body: JSON.stringify({ thinking: level }) });
    } catch (e) { alert('设置思考档位失败：' + e.message); return; }
  } else {
    homeDraft.thinking = level;
    persistHomeDraftLS();
  }
  syncComposer();
}

// 默认模型的思考档位（模型菜单的「默认」条目用）
function levelsOfDefault() {
  for (const m of state.models || []) {
    if (m.providerId === state.defaultProvider && m.modelId === state.defaultModel) {
      return m.reasoningLevels || [];
    }
  }
  return [];
}

let modelPopExpand = ''; // 模型菜单里展开档位的模型 key

function renderModelPop() {
  const pop = $('model-pop');
  const cur = currentModelKey();
  const thinking = curThinking();
  // 档位子列表：仅当前选中的模型行内显示档位选中态
  const lvRows = (key, levels) => {
    if (modelPopExpand !== key || !levels || !levels.length) return '';
    const active = key === cur;
    let h = '<div class="mp-levels">';
    h += `<button class="pop-item mp-lv${active && !thinking ? ' on' : ''}" data-lv="">默认（跟随服务）</button>`;
    for (const lv of levels) {
      h += `<button class="pop-item mp-lv${active && thinking === lv ? ' on' : ''}" data-lv="${esc(lv)}">${THINK_LABELS[lv] || esc(lv)}</button>`;
    }
    return h + '</div>';
  };
  let html = `<button class="pop-item model-item ${cur === '__default__' ? 'sel' : ''}" data-key="__default__">默认（${esc(state.defaultModel || '未配置')}）</button>`;
  html += lvRows('__default__', levelsOfDefault());
  let lastProv = '';
  for (const m of state.models || []) {
    if (m.providerId !== lastProv) {
      html += `<div class="pop-group">${esc(m.providerName)}</div>`;
      lastProv = m.providerId;
    }
    const k = m.providerId + '|' + m.modelId;
    const hasLv = m.reasoningLevels && m.reasoningLevels.length;
    const arrow = hasLv ? '<svg class="caret" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M9 6l6 6-6 6"/></svg>' : '';
    html += `<button class="pop-item model-item ${k === cur ? 'sel' : ''}" data-key="${esc(k)}"><span>${esc(m.modelName || m.modelId)}</span>${arrow}</button>`;
    html += lvRows(k, m.reasoningLevels);
  }
  pop.innerHTML = html;
  // 点模型 = 选中并展开/收起它的思考档位
  pop.querySelectorAll('.model-item').forEach((b) => {
    b.onclick = async () => {
      const key = b.dataset.key;
      await pickModel(key, true);
      modelPopExpand = modelPopExpand === key ? '' : key;
      renderModelPop();
    };
  });
  pop.querySelectorAll('.mp-lv').forEach((b) => {
    b.onclick = (e) => { e.stopPropagation(); pickThinking(b.dataset.lv); };
  });
}

/* ---------- 弹出面板系统：加号菜单 / 权限面板 / 模型面板 / 目录面板 ---------- */

const PERM_LABELS = { 'read-only': '只读', 'workspace-write': '工作区内读写', 'full': '完全访问' };
const homeDraft = { permission: 'workspace-write', provider: '', model: '', thinking: '', tools: [], planMode: false, goalMode: false, remote: null };

function closePopovers() {
  document.querySelectorAll('.popover').forEach((p) => p.classList.add('hidden'));
  closeAllSelects();
}

function openPopover(popId, anchor) {
  const pop = $(popId);
  const reopening = pop.classList.contains('hidden');
  closePopovers();
  if (!reopening) return; // 点同一个按钮 = 收起
  pop.classList.remove('hidden');
  const r = anchor.getBoundingClientRect();
  const pw = pop.offsetWidth, ph = pop.offsetHeight;
  let x = Math.min(r.left, window.innerWidth - pw - 12);
  if (r.left + pw > window.innerWidth - 12) x = Math.max(12, r.right - pw);
  let y = r.top - ph - 8; // 输入区在底部，面板向上弹出
  if (y < 12) y = Math.min(r.bottom + 8, window.innerHeight - ph - 12);
  pop.style.left = x + 'px';
  pop.style.top = y + 'px';
}

function sessionOrDraft() {
  if (state.cur) {
    return {
      permission: state.cur.permission || 'workspace-write',
      tools: state.cur.tools || [],
      planMode: !!state.cur.planMode,
      goalMode: !!state.cur.goalMode,
    };
  }
  return homeDraft;
}

async function patchDraftOrSession(fields) {
  if (state.cur) {
    try {
      state.cur = await api('/api/sessions/' + state.cur.id, { method: 'PATCH', body: JSON.stringify(fields) });
    } catch (e) { alert('保存失败：' + e.message); }
  } else {
    Object.assign(homeDraft, fields);
  }
  syncComposer();
}

// 同步 composer 芯片显示（权限/模式徽标/模型）
function syncComposer() {
  const s = sessionOrDraft();
  const remote = state.cur ? !!state.cur.remote : !!homeDraft.remote;
  $('perm-chip-label').textContent = (PERM_LABELS[s.permission] || s.permission) + (remote ? ' ·远程' : '');
  const tk = curThinking();
  $('model-chip-label').textContent = modelNameOf(currentModelKey()) + (tk ? ' · ' + (THINK_LABELS[tk] || tk) : '');
  $('flag-plan').classList.toggle('on', s.planMode);
  $('flag-goal').classList.toggle('on', s.goalMode);
  $('mode-flags').classList.toggle('hidden', !s.planMode && !s.goalMode);
  $('pm-plan-check').textContent = s.planMode ? '✓' : '';
  $('pm-goal-check').textContent = s.goalMode ? '✓' : '';
  $('pm-remote-check').textContent = remote ? '✓' : '';
  // 顶栏远程徽标（挂了远程工作区的会话）
  const rb = $('remote-badge');
  if (state.cur && state.cur.remote) {
    rb.textContent = '远程 ' + remoteDisplay(state.cur.remote);
    rb.title = 'SSH 远程工作区 ' + remoteDisplay(state.cur.remote) + '（点击配置/解除）';
    rb.classList.remove('hidden');
  } else {
    rb.classList.add('hidden');
  }
  // 目录行只在新建对话（无会话）时出现
  const wsRow = $('ws-row');
  if (!state.cur) {
    wsRow.classList.remove('hidden');
    $('ws-row-name').textContent = homeDraft.remote
      ? remoteDisplay(homeDraft.remote)
      : (baseName(homeDraft.workspace || state.defaultWs || '') || '工作目录');
  } else {
    wsRow.classList.add('hidden');
  }
  $('input').placeholder = state.cur ? '发消息或创建任务…' : '描述你想要构建的内容…';
  persistHomeDraftLS(); // 所有选择改动都会走到这里，统一落一次本地草稿
}

// remoteDisplay：user@host[:port]:dir
function remoteDisplay(r) {
  if (!r || !r.host) return '';
  const user = r.user || 'root';
  const port = (r.port && r.port !== 22) ? ':' + r.port : '';
  return user + '@' + r.host + port + ':' + (r.dir || '/');
}

// 完全访问预设捆绑的能力：切到完全访问即强制勾选、置灰不可关闭（后端权限引擎同样视为开启）
const FULL_BUNDLED_TOOLS = ['unsandboxed', 'browser', 'computer'];

function renderPermPop() {
  const s = sessionOrDraft();
  const pop = $('perm-pop');
  const full = s.permission === 'full';
  pop.querySelectorAll('input[name=perm]').forEach((r) => { r.checked = r.value === s.permission; });
  pop.querySelectorAll('input[data-tool]').forEach((c) => {
    const forced = full && FULL_BUNDLED_TOOLS.includes(c.dataset.tool);
    c.checked = forced || s.tools.includes(c.dataset.tool);
    c.disabled = forced; // 完全访问下捆绑能力不可关闭
    c.closest('.perm-opt').classList.toggle('forced', forced);
  });
}

function renderWsPop() {
  // 候选目录 = 默认工作区 + 手动添加的工作目录 + 历史会话用过的目录
  const seen = new Set();
  const list = [];
  const add = (ws) => {
    if (!ws || seen.has(ws)) return;
    seen.add(ws);
    list.push(ws);
  };
  add(state.defaultWs);
  lsGet('jx.wslist', []).forEach(add);
  (state.sessions || []).forEach((s) => add(s.workspace));
  const q = ($('ws-search').value || '').trim().toLowerCase();
  const filtered = q ? list.filter((ws) => ws.toLowerCase().includes(q)) : list;
  // 挂了远程目录时不标本地选中项
  const cur = homeDraft.remote ? '' : (homeDraft.workspace || state.defaultWs || '');
  const box = $('ws-pop-list');
  box.innerHTML = '';
  if (!filtered.length) {
    box.innerHTML = `<div class="ws-pop-empty">${q ? '没有匹配的工作目录' : '还没有工作目录，用下方按钮添加'}</div>`;
    return;
  }
  filtered.forEach((ws) => {
    const b = document.createElement('button');
    b.className = 'pop-item' + (ws === cur ? ' sel' : '');
    b.innerHTML = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/></svg>${esc(ws)}<span class="pop-check">${ws === cur ? '✓' : ''}</span>`;
    b.onclick = () => {
      homeDraft.remote = null; // 显式选本地目录 = 放弃远程挂载
      homeDraft.workspace = ws;
      closePopovers();
      syncComposer();
    };
    box.appendChild(b);
  });
}

/* ---------- 加号菜单动作 ---------- */

async function doUpload() {
  closePopovers();
  if (!state.cur) { $('hint').textContent = '先发送第一条消息创建对话，再上传文件'; setTimeout(() => { $('hint').textContent = ''; }, 3000); return; }
  if (state.streaming) return;
  $('file-input').value = '';
  $('file-input').click();
}

async function uploadFiles(files) {
  for (const f of files) {
    const fd = new FormData();
    fd.append('file', f);
    try {
      await fetch('/api/sessions/' + state.cur.id + '/upload', { method: 'POST', body: fd });
    } catch (e) { alert('上传失败：' + f.name); }
  }
  await openSession(state.cur.id);
  $('hint').textContent = '文件已上传到工作区';
  setTimeout(() => { $('hint').textContent = ''; }, 3000);
}

function doExport() {
  closePopovers();
  if (!state.cur) { alert('还没有可导出的对话'); return; }
  window.open('/api/sessions/' + state.cur.id + '/export', '_blank');
}

async function toggleModeFlag(flag) {
  const s = sessionOrDraft();
  const turningOn = !s[flag];
  const fields = {};
  fields[flag] = turningOn;
  if (turningOn && flag === 'planMode') fields.goalMode = false; // 计划/目标二选一
  if (turningOn && flag === 'goalMode') fields.planMode = false;
  await patchDraftOrSession(fields);
  closePopovers();
}

let provDraft = [];      // 模型页编辑中的提供方列表（不直接改全局配置）
let presetsCache = [];   // 后端预置提供方模板 [{id,name,baseURL}]
let provOpenKey = null;  // 展开的卡片：provider id / 'new' / null（手风琴式单开）
let provNew = null;      // 「添加提供方」的草稿
let provModelOpen = {};  // 卡片key -> 展开编辑的模型行下标（-1=无）
let provTest = {};       // 卡片key -> { 模型id: {state:'run'|'ok'|'fail', ms, err} }

// lsGet/lsSet 使用顶部 162 行的兼容版（对象 JSON、字符串原样），此处不再重复定义。

// 设置浮窗：左目录切换
function setSettingsPane(name) {
  document.querySelectorAll('.set-nav-item').forEach((b) => {
    b.classList.toggle('active', b.dataset.pane === name);
  });
  document.querySelectorAll('.set-pane').forEach((p) => {
    p.classList.toggle('active', p.id === 'set-pane-' + name);
  });
}

function loadProvDraft(c) {
  return (c.providers || []).map((p) => ({
    id: p.id,
    presetId: p.preset ? p.id : null, // 预置提供方用系统地址，不出 URL 输入框
    presetURL: p.presetURL || '',
    name: p.name || '',
    baseURL: p.baseURL || '',
    key: '', keyMasked: p.apiKeyMasked || '',
    models: (p.models || []).map((m) => ({
      id: m.id || '', name: m.name || '',
      contextWindow: m.contextWindow || 0, maxOutput: m.maxOutput || 0,
      supportsImages: !!m.supportsImages,
    })),
  }));
}

async function openSettings() {
  const c = await api('/api/config');
  presetsCache = c.presets || [];
  provDraft = loadProvDraft(c);
  provOpenKey = null; provNew = null; provTest = {}; provModelOpen = {};
  renderProvCards();
  selRefs.compactMode.set(c.compactMode || 'rule');
  $('set-keeprounds').value = c.keepRounds || 5;
  $('set-ruleinterval').value = c.ruleInterval || 5;
  selRefs.compactThreshold.set(String(c.compactThreshold || 0.8));
  // 常规页：本机浏览器偏好（即时生效，打开时回显当前值）
  selRefs.lang.set(lsGet('jx.lang', 'zh'));
  selRefs.fontsize.set(lsGet('jx.fontpx', '14'));
  syncThemeCards();
  selRefs.sendkey.set(lsGet('jx.sendkey', 'enter'));
  selRefs.midrun.set(lsGet('jx.midrun', 'interject'));
  renderPluginList();
  mountPluginSections();
  renderComponentList();
  setSettingsPane('general');
  // 关于页信息
  try {
    const st = await api('/api/status');
    $('about-version').textContent = '版本 ' + (st.version || '-') + ' · ' + (st.model || '');
    $('about-status').textContent = st.mock ? '当前：离线演示模式（mock）' : (st.hasKey ? '当前：已连接模型服务' : '当前：未配置 Key');
  } catch (e) { $('about-status').textContent = '服务状态未知'; }
  $('settings').classList.remove('hidden');
}

/* ---------- 组件清单（/api/components + CX 运行时热开关） ---------- */

async function renderComponentList() {
  const box = $('component-list');
  if (!box) return;
  box.innerHTML = '加载中…';
  let list = [];
  try { list = (await api('/api/components')).components || []; } catch (e) { box.textContent = '组件清单读取失败：' + e.message; return; }
  box.innerHTML = '';
  for (const c of list) {
    const row = document.createElement('div');
    row.className = 'plugin-row';
    const info = document.createElement('div');
    info.className = 'pr-info';
    info.innerHTML =
      `<div class="pr-line"><b>${esc(c.name)}</b><span class="pr-ver">${esc(c.version || '')}</span></div>` +
      `<p>${esc(c.description || '')}</p>`;
    const tg = document.createElement('label');
    tg.className = 'pr-toggle';
    const cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.checked = !!c.enabled;
    cb.onchange = async () => {
      cb.disabled = true;
      const note = row.querySelector('.pr-note');
      try {
        const r = await CX.toggle(c.id, cb.checked);
        if (note) note.textContent = r.hot ? (cb.checked ? '已即时启用' : '已即时停用') : '该组件不支持热关闭，刷新页面后完全生效';
      } catch (e) {
        alert('切换失败：' + e.message);
        cb.checked = !cb.checked;
      }
      cb.disabled = false;
      setTimeout(() => { if (note) note.textContent = ''; }, 3000);
    };
    tg.appendChild(cb);
    tg.appendChild(document.createElement('span'));
    row.appendChild(info);
    row.appendChild(tg);
    const note = document.createElement('div');
    note.className = 'pr-note';
    row.appendChild(note);
    box.appendChild(row);
  }
  if (!list.length) box.innerHTML = '<div class="pr-note">组件/ 下没有发现组件</div>';
}

/* ---------- 插件清单与分区（/api/plugins + JX 注册的分区） ---------- */

async function renderPluginList() {
  const box = $('plugin-list');
  if (!box) return;
  box.innerHTML = '加载中…';
  let list = [];
  try { list = (await api('/api/plugins')).plugins || []; } catch (e) { box.textContent = '插件清单读取失败：' + e.message; return; }
  box.innerHTML = '';
  for (const p of list) {
    const row = document.createElement('div');
    row.className = 'plugin-row' + (p.adapted ? '' : ' unadapted');
    const info = document.createElement('div');
    info.className = 'pr-info';
    info.innerHTML =
      `<div class="pr-line"><b>${esc(p.name)}</b><span class="pr-ver">${esc(p.version || '')}</span>` +
      (p.adapted ? '' : '<span class="pr-badge">未适配</span>') + '</div>' +
      `<p>${esc(p.description || '')}</p>`;
    const tg = document.createElement('label');
    tg.className = 'pr-toggle';
    const cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.checked = !!p.enabled;
    cb.disabled = !p.adapted;
    cb.onchange = async () => {
      cb.disabled = true;
      try {
        await api('/api/plugins', { method: 'POST', body: JSON.stringify({ id: p.id, enabled: cb.checked }) });
        row.querySelector('.pr-note').textContent = '已更新；界面部分刷新页面后生效';
      } catch (e) {
        alert('切换失败：' + e.message);
        cb.checked = !cb.checked;
      }
      cb.disabled = !p.adapted;
    };
    tg.appendChild(cb);
    tg.appendChild(document.createElement('span'));
    row.appendChild(info);
    row.appendChild(tg);
    const note = document.createElement('div');
    note.className = 'pr-note';
    row.appendChild(note);
    box.appendChild(row);
  }
  if (!list.length) box.innerHTML = '<div class="pr-note">插件兼容层/ 下没有发现插件</div>';
}

function mountPluginSections() {
  const box = $('plugin-sections');
  if (!box) return;
  box.innerHTML = '';
  for (const sec of JX._settingsSections) {
    const wrap = document.createElement('div');
    wrap.className = 'prov-section jx-section';
    const h = document.createElement('h4');
    h.textContent = sec.title;
    wrap.appendChild(h);
    const body = document.createElement('div');
    wrap.appendChild(body);
    try { sec.mount(body); } catch (e) { body.textContent = '插件设置加载失败：' + e.message; }
    box.appendChild(wrap);
  }
}

/* ---------- 模型页：提供方卡片（点击展开即全量编辑，每卡单独保存） ---------- */

function presetOf(id) { return presetsCache.find((p) => p.id === id) || null; }

function fmtTok(n) {
  if (!n || n <= 0) return '';
  if (n >= 1048576) return (Math.round(n / 1048576 * 10) / 10).toString().replace(/\.0$/, '') + 'M';
  return Math.round(n / 1024) + 'K';
}

// 卡片当前生效的 API 地址（预置 = 系统地址，自定义 = 表单里的）
function effURL(p) {
  if (p.presetId) {
    const t = presetOf(p.presetId);
    return (t && t.baseURL) || p.presetURL || p.baseURL;
  }
  return p.baseURL;
}

async function reloadProviders() {
  const c = await api('/api/config');
  provDraft = loadProvDraft(c);
  provOpenKey = null; provNew = null; provTest = {}; provModelOpen = {};
  renderProvCards();
  loadModels();
}

function renderProvCards() {
  const box = $('prov-list');
  if (!box) return;
  box.innerHTML = '';
  provDraft.forEach((p) => box.appendChild(buildProvCard(p, p.id)));
  if (provOpenKey === 'new' && provNew) box.appendChild(buildProvCard(provNew, 'new'));
}

function buildProvCard(p, key) {
  const open = provOpenKey === key;
  const card = document.createElement('div');
  card.className = 'prov-card' + (open ? ' open' : '');

  // 折叠头：状态点 + 名称 + 模型数 + 编辑/收起
  const configured = !!(p.key || p.keyMasked);
  const head = document.createElement('div');
  head.className = 'pv-head';
  head.innerHTML =
    `<span class="pv-dot ${configured ? 'ok' : 'no'}" title="${configured ? '密钥已配置' : '未配置密钥'}"></span>` +
    `<b class="pv-name">${esc(p.name || '未命名提供方')}</b>` +
    `<span class="pv-meta">${p.models.length} 个模型</span>` +
    `<span class="pv-edit">${open ? '收起' : '编辑'}</span>`;
  head.onclick = () => {
    if (open) {
      if (key === 'new') { provNew = null; provOpenKey = null; renderProvCards(); }
      else reloadProviders(); // 收起 = 丢弃未保存的修改
    } else {
      provOpenKey = key;
      if (provModelOpen[key] === undefined) provModelOpen[key] = -1;
      renderProvCards();
    }
  };
  card.appendChild(head);
  if (!open) return card;

  const form = document.createElement('div');
  form.className = 'pv-form';

  // 提供方选择：预置清单 + 自定义
  const lblProv = document.createElement('div');
  lblProv.className = 'pf-label';
  lblProv.textContent = '提供方';
  form.appendChild(lblProv);
  const sel = document.createElement('select');
  sel.className = 'pf-input';
  for (const t of presetsCache) {
    const o = document.createElement('option');
    o.value = t.id; o.textContent = t.name;
    sel.appendChild(o);
  }
  const oCustom = document.createElement('option');
  oCustom.value = '__custom'; oCustom.textContent = '自定义提供方';
  sel.appendChild(oCustom);
  sel.value = p.presetId || '__custom';
  sel.onchange = () => {
    if (sel.value === '__custom') {
      p.presetId = null;
    } else {
      p.presetId = sel.value;
      const t = presetOf(sel.value);
      if (t) { p.name = t.name; p.baseURL = ''; p.presetURL = t.baseURL; }
    }
    renderProvCards();
  };
  form.appendChild(sel);

  // 名称 + API 地址（预置方用系统地址，只展示不可改）
  if (p.presetId) {
    const lblUrl = document.createElement('div');
    lblUrl.className = 'pf-label';
    lblUrl.textContent = 'API 地址';
    form.appendChild(lblUrl);
    const fixed = document.createElement('div');
    fixed.className = 'pf-fixed';
    fixed.textContent = '使用系统预设地址：' + effURL(p);
    form.appendChild(fixed);
  } else {
    const two = document.createElement('div');
    two.className = 'pf-two';
    const cellName = document.createElement('div');
    cellName.innerHTML = '<div class="pf-label">名称</div>';
    const inName = document.createElement('input');
    inName.className = 'pf-input';
    inName.placeholder = '如 硅基流动';
    inName.value = p.name;
    inName.oninput = () => { p.name = inName.value; };
    cellName.appendChild(inName);
    const cellUrl = document.createElement('div');
    cellUrl.innerHTML = '<div class="pf-label">API 地址（OpenAI 兼容）</div>';
    const inUrl = document.createElement('input');
    inUrl.className = 'pf-input';
    inUrl.placeholder = 'https://…/v1';
    inUrl.value = p.baseURL;
    inUrl.oninput = () => { p.baseURL = inUrl.value; };
    cellUrl.appendChild(inUrl);
    two.appendChild(cellName);
    two.appendChild(cellUrl);
    form.appendChild(two);
  }

  // API 密钥：绝不回显，只标「已配置」，输入可替换
  const lblKey = document.createElement('div');
  lblKey.className = 'pf-label';
  lblKey.textContent = 'API 密钥';
  form.appendChild(lblKey);
  const keyRow = document.createElement('div');
  keyRow.className = 'key-row';
  if (p.keyMasked) {
    const chip = document.createElement('span');
    chip.className = 'key-chip';
    chip.textContent = '已配置';
    keyRow.appendChild(chip);
  }
  const inKey = document.createElement('input');
  inKey.className = 'pf-input';
  inKey.type = 'password';
  inKey.autocomplete = 'new-password';
  inKey.placeholder = p.keyMasked ? '留空保持不变，输入可替换' : (p.presetId === 'ollama' ? '本地服务可留空' : '必填，如 sk-…');
  inKey.oninput = () => { p.key = inKey.value; };
  keyRow.appendChild(inKey);
  form.appendChild(keyRow);

  // 模型目录
  const cat = document.createElement('div');
  cat.className = 'model-cat';
  const catHead = document.createElement('div');
  catHead.className = 'mc-head';
  catHead.innerHTML = '<span class="mc-title">模型目录</span>';
  const btnTest = document.createElement('button');
  btnTest.className = 'mc-btn';
  btnTest.textContent = '测试连通';
  const btnFetch = document.createElement('button');
  btnFetch.className = 'mc-btn';
  btnFetch.textContent = '拉取模型';
  catHead.appendChild(btnTest);
  catHead.appendChild(btnFetch);
  cat.appendChild(catHead);

  p.models.forEach((m, mi) => {
    const row = document.createElement('div');
    row.className = 'model-row';
    const line = document.createElement('div');
    line.className = 'mr-line';
    const chips = [
      m.contextWindow ? '上下文 ' + fmtTok(m.contextWindow) : '上下文未知',
      m.maxOutput ? '输出 ' + fmtTok(m.maxOutput) : null,
      m.supportsImages ? '图片 ✓' : null,
    ].filter(Boolean).join(' · ');
    line.innerHTML =
      `<span class="mr-id">${esc(m.id || '(未命名模型)')}</span>` +
      `<span class="mr-test"></span>` +
      `<span class="mr-chips">${esc(chips)}</span>` +
      `<button class="mr-x" title="删除该模型">✕</button>`;
    const testEl = line.querySelector('.mr-test');
    const t = (provTest[key] || {})[m.id];
    if (t && t.state === 'run') { testEl.textContent = '测试中…'; testEl.className = 'mr-test run'; }
    else if (t && t.state === 'ok') { testEl.textContent = '✓ ' + t.ms + 'ms'; testEl.className = 'mr-test ok'; }
    else if (t && t.state === 'fail') { testEl.textContent = '✗ ' + t.err; testEl.title = t.err; testEl.className = 'mr-test fail'; }
    line.querySelector('.mr-x').onclick = (e) => {
      e.stopPropagation();
      p.models.splice(mi, 1);
      renderProvCards();
    };
    line.onclick = () => {
      provModelOpen[key] = provModelOpen[key] === mi ? -1 : mi;
      renderProvCards();
    };
    row.appendChild(line);
    if (provModelOpen[key] === mi) {
      const edit = document.createElement('div');
      edit.className = 'mr-edit';
      const mk = (labelText, inputEl, full) => {
        const cell = document.createElement('div');
        if (full) cell.className = 'full';
        const l = document.createElement('div');
        l.className = 'pf-label';
        l.textContent = labelText;
        cell.appendChild(l);
        cell.appendChild(inputEl);
        return cell;
      };
      const inMid = document.createElement('input');
      inMid.className = 'pf-input';
      inMid.placeholder = '模型 ID，如 deepseek-chat';
      inMid.value = m.id;
      inMid.oninput = () => { m.id = inMid.value; };
      edit.appendChild(mk('模型 ID', inMid));
      const inMname = document.createElement('input');
      inMname.className = 'pf-input';
      inMname.placeholder = '选填，显示名称';
      inMname.value = m.name;
      inMname.oninput = () => { m.name = inMname.value; };
      edit.appendChild(mk('显示名称', inMname));
      const inCtx = document.createElement('input');
      inCtx.className = 'pf-input';
      inCtx.type = 'number';
      inCtx.min = '0';
      inCtx.placeholder = '默认 262144';
      inCtx.value = m.contextWindow || '';
      inCtx.oninput = () => { m.contextWindow = parseInt(inCtx.value, 10) || 0; };
      edit.appendChild(mk('上下文窗口（tokens）', inCtx));
      const inMax = document.createElement('input');
      inMax.className = 'pf-input';
      inMax.type = 'number';
      inMax.min = '0';
      inMax.placeholder = '不限';
      inMax.value = m.maxOutput || '';
      inMax.oninput = () => { m.maxOutput = parseInt(inMax.value, 10) || 0; };
      edit.appendChild(mk('最大输出（tokens）', inMax));
      // 支持图片：苹果式开关
      const tg = document.createElement('label');
      tg.className = 'pr-toggle';
      const cb = document.createElement('input');
      cb.type = 'checkbox';
      cb.checked = !!m.supportsImages;
      cb.onchange = () => { m.supportsImages = cb.checked; };
      tg.appendChild(cb);
      tg.appendChild(document.createElement('span'));
      edit.appendChild(mk('是否支持图片', tg));
      const del = document.createElement('button');
      del.className = 'mr-del2';
      del.textContent = '删除该模型';
      del.onclick = (e) => { e.stopPropagation(); p.models.splice(mi, 1); renderProvCards(); };
      edit.appendChild(del);
      row.appendChild(edit);
    }
    cat.appendChild(row);
  });

  const btnAddModel = document.createElement('button');
  btnAddModel.className = 'mc-add';
  btnAddModel.textContent = '＋ 添加模型';
  btnAddModel.onclick = () => {
    p.models.push({ id: '', name: '', contextWindow: 0, maxOutput: 0, supportsImages: false });
    provModelOpen[key] = p.models.length - 1;
    renderProvCards();
    const inputs = $('prov-list').querySelectorAll('.mr-edit input');
    if (inputs.length) inputs[inputs.length - 5 >= 0 ? inputs.length - 5 : 0].focus();
  };
  cat.appendChild(btnAddModel);
  form.appendChild(cat);

  // 拉取模型：合并策略 —— 已有条目保留手工改过的元信息，新条目用后端猜的
  btnFetch.onclick = async () => {
    btnFetch.disabled = true; btnFetch.textContent = '拉取中…';
    try {
      const d = await api('/api/providers/fetch', {
        method: 'POST',
        body: JSON.stringify({ id: key === 'new' ? '' : p.id, baseURL: effURL(p), apiKey: p.key }),
      });
      const oldMap = new Map(p.models.map((m) => [m.id, m]));
      const merged = [];
      for (const fm of (d.models || [])) {
        const ex = oldMap.get(fm.id);
        if (ex) { oldMap.delete(fm.id); merged.push(ex); }
        else merged.push({ id: fm.id, name: fm.name || '', contextWindow: fm.contextWindow || 0, maxOutput: 0, supportsImages: !!fm.supportsImages });
      }
      for (const ex of oldMap.values()) merged.push(ex); // 手工添加的保留在末尾
      p.models = merged;
      provModelOpen[key] = -1;
      renderProvCards();
    } catch (e) {
      alert('拉取失败：' + e.message);
      btnFetch.disabled = false; btnFetch.textContent = '拉取模型';
    }
  };

  // 测试连通：目录里的所有模型各发一个极小请求
  btnTest.onclick = async () => {
    const ids = p.models.map((m) => m.id).filter(Boolean);
    if (!ids.length) { alert('目录里还没有模型，先「拉取模型」或「添加模型」'); return; }
    provTest[key] = {};
    ids.forEach((id) => { provTest[key][id] = { state: 'run' }; });
    btnTest.disabled = true; btnTest.textContent = '测试中…';
    renderProvCards();
    try {
      const d = await api('/api/providers/test', {
        method: 'POST',
        body: JSON.stringify({ id: key === 'new' ? '' : p.id, baseURL: effURL(p), apiKey: p.key, models: ids }),
      });
      provTest[key] = {};
      for (const r of (d.results || [])) provTest[key][r.id] = { state: r.ok ? 'ok' : 'fail', ms: r.ms, err: r.err || '失败' };
    } catch (e) {
      provTest[key] = {};
      ids.forEach((id) => { provTest[key][id] = { state: 'fail', err: e.message }; });
    }
    renderProvCards();
  };

  // 卡片操作区：删除 | 取消 | 保存
  const acts = document.createElement('div');
  acts.className = 'pf-actions';
  if (key !== 'new') {
    const delBtn = document.createElement('button');
    delBtn.className = 'pf-del';
    delBtn.textContent = '删除提供方';
    delBtn.onclick = async () => {
      if (!confirm('删除提供方「' + (p.name || p.id) + '」？以后可重新添加。')) return;
      try {
        await api('/api/providers/delete', { method: 'POST', body: JSON.stringify({ id: p.id }) });
        reloadProviders();
      } catch (e) { alert('删除失败：' + e.message); }
    };
    acts.appendChild(delBtn);
  }
  const btnCancel = document.createElement('button');
  btnCancel.className = 'pf-cancel';
  btnCancel.textContent = '取消';
  btnCancel.onclick = () => {
    if (key === 'new') { provNew = null; provOpenKey = null; renderProvCards(); }
    else reloadProviders();
  };
  const btnSave = document.createElement('button');
  btnSave.className = 'pf-save';
  btnSave.textContent = '保存';
  btnSave.onclick = async () => {
    if (!p.presetId && !p.name.trim()) { alert('请填写名称'); return; }
    if (!p.presetId && !p.baseURL.trim()) { alert('请填写 API 地址'); return; }
    if (!p.key && !p.keyMasked && !confirm('尚未填写 API 密钥，确定保存？（本地服务如 Ollama 可不填）')) return;
    try {
      await api('/api/providers', {
        method: 'POST',
        body: JSON.stringify({
          id: p.id, name: p.name,
          baseURL: p.presetId ? '' : p.baseURL.trim(),
          apiKey: p.key,
          models: p.models,
        }),
      });
      reloadProviders();
    } catch (e) { alert('保存失败：' + e.message); }
  };
  acts.appendChild(btnCancel);
  acts.appendChild(btnSave);
  form.appendChild(acts);

  card.appendChild(form);
  return card;
}

async function saveSettings() {
  // 模型页各卡片单独保存（/api/providers）；常规页即时落本机浏览器；
  // 底部「保存」只负责上下文页。
  const body = {
    compact: {
      mode: selRefs.compactMode.get(),
      keepRounds: parseInt($('set-keeprounds').value, 10) || 5,
      ruleInterval: parseInt($('set-ruleinterval').value, 10) || 5,
      threshold: parseFloat(selRefs.compactThreshold.get()) || 0.8,
    },
  };
  await api('/api/config', { method: 'POST', body: JSON.stringify(body) });
  $('settings').classList.add('hidden');
  loadStatus();
}

/* ---------- 常规页：本机浏览器偏好（语言/字体/发送行为，即时生效） ---------- */

// 字号只作用于对话区（.msg-col 的 --chat-fs，内部字号均为 em 相对值），
// 侧栏/设置/输入框不受影响；具体 px 值直接可调
function applyChatFont(px) {
  document.documentElement.style.setProperty('--chat-fs', px + 'px');
  lsSet('jx.fontpx', String(px));
}

function restoreChatFont() {
  const px = parseFloat(lsGet('jx.fontpx', '14')) || 14;
  document.documentElement.style.setProperty('--chat-fs', px + 'px');
}

/* ---------- 对话区宽度拖拽：左右把手对称调宽，偏好记入本机 ---------- */

function contentWidth() {
  return parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--content-w')) || 860;
}

function setContentWidth(px, save) {
  // 夹取 640px ~ 滚动区可用宽-176（每侧留 88px 边缘预算）
  const max = Math.max(640, (($('messages') && $('messages').clientWidth) || window.innerWidth) - 176);
  px = Math.round(Math.min(Math.max(px, 640), max));
  document.documentElement.style.setProperty('--content-w', px + 'px');
  if (save) lsSet('jx.contentWidth', String(px));
}

function restoreContentWidth() {
  const px = parseFloat(lsGet('jx.contentWidth', ''));
  if (px >= 640) setContentWidth(px);
}

function setupWidthHandles() {
  const wrap = $('messages');
  if (!wrap) return;
  // 把滚动区包进一层定位容器：把手不能直接放滚动容器里（会随内容滚走）
  let zone = document.getElementById('msg-zone');
  if (!zone) {
    zone = document.createElement('div');
    zone.id = 'msg-zone';
    wrap.parentNode.insertBefore(zone, wrap);
    zone.appendChild(wrap);
  }
  for (const side of ['left', 'right']) {
    const h = document.createElement('div');
    h.className = 'w-handle ' + side;
    h.title = '拖动调整对话区宽度（双击恢复默认）';
    zone.appendChild(h);
    let startX = 0, startW = 860;
    h.addEventListener('pointerdown', (e) => {
      e.preventDefault();
      startX = e.clientX;
      startW = contentWidth();
      h.classList.add('dragging');
      try { h.setPointerCapture(e.pointerId); } catch (err) { /* 旧浏览器忽略 */ }
    });
    h.addEventListener('pointermove', (e) => {
      if (!h.classList.contains('dragging')) return;
      const dx = (e.clientX - startX) * 2 * (side === 'right' ? 1 : -1); // 居中对称放大
      setContentWidth(startW + dx);
    });
    const up = () => {
      if (!h.classList.contains('dragging')) return;
      h.classList.remove('dragging');
      setContentWidth(contentWidth(), true); // 松手落盘
    };
    h.addEventListener('pointerup', up);
    h.addEventListener('pointercancel', up);
    h.addEventListener('dblclick', () => {
      document.documentElement.style.removeProperty('--content-w');
      lsSet('jx.contentWidth', null);
    });
  }
}

/* ---------- 初始化 ---------- */

function autoGrow() {
  const t = $('input');
  t.style.height = 'auto';
  t.style.height = Math.min(t.scrollHeight, 200) + 'px';
}

$('btn-new-task').onclick = goHomeNewTask;
$('btn-sidebar').onclick = toggleSidebar;
$('wst-search').onclick = toggleSideSearch;
$('wst-view').onclick = (e) => openViewMenu(e.currentTarget);
$('wst-add').onclick = addWorkFolder;
// 侧栏搜索输入：防抖后触发双路搜索；✕/Escape 关闭并中止
document.getElementById('side-search-input').addEventListener('input', (e) => {
  if (sideSearchDebounce) clearTimeout(sideSearchDebounce);
  const v = e.target.value;
  sideSearchDebounce = setTimeout(() => { sideSearchDebounce = null; runSideSearch(v); }, 250);
});
document.getElementById('side-search-input').addEventListener('keydown', (e) => {
  if (e.key === 'Escape') { e.stopPropagation(); closeSideSearch(); }
});
$('side-search-close').onclick = closeSideSearch;
$('btn-new-assistant').onclick = () => {
  setFace('thinking', 1600); // 进入时压眼扫视
  if (!state.streaming) openAssistant();
};
$('btn-send').onclick = send;
$('btn-stop').onclick = () => { if (state.abort) state.abort.abort(); };
$('btn-settings').onclick = openSettings;
$('set-cancel').onclick = () => $('settings').classList.add('hidden');
$('set-save').onclick = saveSettings;
$('btn-plus').onclick = () => openPopover('plus-menu', $('btn-plus'));
$('perm-chip').onclick = () => { renderPermPop(); openPopover('perm-pop', $('perm-chip')); };
$('model-chip').onclick = () => { renderModelPop(); openPopover('model-pop', $('model-chip')); };
// 工作目录面板：每次打开重置搜索；搜索实时过滤目录列表
$('ws-row').onclick = () => {
  $('ws-search').value = '';
  $('ws-search-clear').classList.add('hidden');
  renderWsPop();
  openPopover('ws-pop', $('ws-row'));
};
$('ws-search').addEventListener('input', (e) => {
  $('ws-search-clear').classList.toggle('hidden', !e.target.value);
  renderWsPop();
});
$('ws-search-clear').onclick = () => {
  $('ws-search').value = '';
  $('ws-search-clear').classList.add('hidden');
  renderWsPop();
  $('ws-search').focus();
};
$('pm-upload').onclick = doUpload;
$('pm-plan').onclick = () => toggleModeFlag('planMode');
$('pm-goal').onclick = () => toggleModeFlag('goalMode');
$('pm-export').onclick = doExport;
$('pm-remote').onclick = () => { remoteFormPrefill(); openPopover('remote-pop', $('btn-plus')); };
$('remote-badge').onclick = () => { remoteFormPrefill(); openPopover('remote-pop', $('remote-badge')); };
$('rm-auth').addEventListener('change', syncRemoteFormVis);
$('rm-test').onclick = testRemote;
$('rm-save').onclick = saveRemote;
$('rm-back').onclick = () => showRmStep(1);
$('rm-up').onclick = () => {
  let p = ($('rm-dir').value || '').trim() || rmCurPath;
  if (!p.startsWith('/')) p = rmCurPath;
  p = p.replace(/\/+$/, '');
  const i = p.lastIndexOf('/');
  browseRemote(i <= 0 ? '/' : p.slice(0, i));
};
$('rm-refresh').onclick = () => browseRemote(($('rm-dir').value || '').trim() || rmCurPath);
$('rm-dir').addEventListener('keydown', (e) => {
  if (e.key === 'Enter') { e.preventDefault(); browseRemote(($('rm-dir').value || '').trim()); }
});
$('rm-apply').onclick = applyRemote;
$('rm-clear').onclick = clearRemote;
// 目录列表：双击文件夹进入（文件行不可进）
$('rm-dir-list').addEventListener('dblclick', (e) => {
  const row = e.target.closest('.rm-entry');
  if (!row || row.classList.contains('file')) return;
  browseRemote(row.dataset.path);
});
$('file-input').addEventListener('change', (e) => { if (e.target.files.length) uploadFiles([...e.target.files]); });
// 「添加工作目录」：弹系统选择框，选中即设为新对话的工作目录并登记
$('ws-pop-add').onclick = async () => {
  const p = await pickWorkFolder();
  if (!p) return;
  homeDraft.remote = null; // 选了本地目录，放弃远程挂载
  homeDraft.workspace = p;
  rememberWorkspace(p);
  closePopovers();
  syncComposer();
};
// 「添加远程工作目录」：打开 SSH 表单（与加号菜单里的是同一个）
$('ws-pop-remote').onclick = () => { remoteFormPrefill(); openPopover('remote-pop', $('ws-row')); };
// 权限面板：单选立即生效，多选即时收集；切到完全访问时三项捆绑能力联动勾上
$('perm-pop').addEventListener('change', (e) => {
  if (e.target.name === 'perm') {
    if (e.target.value === 'full') {
      const set = new Set(sessionOrDraft().tools || []);
      FULL_BUNDLED_TOOLS.forEach((t) => set.add(t));
      patchDraftOrSession({ permission: 'full', tools: [...set] }).then(renderPermPop);
    } else {
      patchDraftOrSession({ permission: e.target.value }).then(renderPermPop);
    }
  } else if (e.target.dataset.tool) {
    const s = sessionOrDraft();
    const set = new Set(s.tools || []);
    if (e.target.checked) set.add(e.target.dataset.tool); else set.delete(e.target.dataset.tool);
    patchDraftOrSession({ tools: [...set] });
  }
});
// 点面板外收起所有弹出层。注意排除会打开弹出层的锚点按钮本身：
// 它们的 onclick 先开菜单，事件再冒泡到这里——不排除的话菜单会开完即关。
document.addEventListener('click', (e) => {
  if (!e.target.closest('.popover') && !e.target.closest('#btn-plus') &&
      !e.target.closest('#perm-chip') && !e.target.closest('#model-chip') && !e.target.closest('#ws-row') &&
      !e.target.closest('.ws-tools') && !e.target.closest('.ws-acts') && !e.target.closest('.row-acts')) {
    closePopovers();
  }
});
document.addEventListener('keydown', (e) => {
  if (e.key !== 'Escape') return;
  if (sideSearchOpen()) { closeSideSearch(); return; } // 搜索开着：优先关搜索
  closePopovers();
});
$('settings').onclick = (e) => { if (e.target === $('settings')) $('settings').classList.add('hidden'); };
document.querySelectorAll('.set-nav-item').forEach((b) => {
  b.onclick = () => setSettingsPane(b.dataset.pane);
});
// 模型页：「添加提供方」= 插入一张展开的自定义卡片
$('prov-add-card').onclick = () => {
  provNew = { id: '', presetId: null, presetURL: '', name: '', baseURL: '', key: '', keyMasked: '', models: [] };
  provOpenKey = 'new';
  provModelOpen['new'] = -1;
  renderProvCards();
  const box = $('prov-list');
  if (box && box.lastElementChild) box.lastElementChild.scrollIntoView({ block: 'nearest' });
};
// 常规页：语言/字体/发送行为，改动即时生效并落本机
/* ---------- 自定义下拉：圆角按钮 + 浮层，设置页统一外观 ---------- */

const selRefs = {};

function closeAllSelects() {
  document.querySelectorAll('.jx-sel-pop.open').forEach((p) => p.classList.remove('open'));
}

// makeSelect(hostId, key, options, onChange)：在容器里建一套下拉，
// 返回 {set(v), get()}；onChange 在用户选中时触发。
function makeSelect(hostId, key, options, onChange) {
  const host = $(hostId);
  if (!host) return { set() {}, get() { return null; } };
  host.innerHTML = '';
  const wrap = document.createElement('div');
  wrap.className = 'jx-select';
  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = 'jx-sel-btn';
  const label = document.createElement('span');
  label.className = 'jx-sel-label';
  btn.appendChild(label);
  btn.insertAdjacentHTML('beforeend',
    '<svg class="jx-sel-chev" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 9l6 6 6-6"/></svg>');
  const pop = document.createElement('div');
  pop.className = 'jx-sel-pop';
  let value = options.length ? options[0].v : null;
  const render = () => {
    const cur = options.find((o) => o.v === value);
    label.textContent = cur ? cur.label : '请选择';
    pop.innerHTML = '';
    for (const o of options) {
      const it = document.createElement('button');
      it.type = 'button';
      it.className = 'jx-sel-opt' + (o.v === value ? ' cur' : '');
      it.innerHTML = '<span></span><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6L9 17l-5-5"/></svg>';
      it.firstChild.textContent = o.label;
      it.onclick = (e) => {
        e.stopPropagation();
        value = o.v;
        render();
        closeAllSelects();
        if (onChange) onChange(o.v);
      };
      pop.appendChild(it);
    }
  };
  btn.onclick = (e) => {
    e.stopPropagation();
    const wasOpen = pop.classList.contains('open');
    closeAllSelects();
    pop.classList.toggle('open', !wasOpen);
  };
  wrap.appendChild(btn);
  wrap.appendChild(pop);
  host.appendChild(wrap);
  render();
  const ref = { set(v) { value = v; render(); }, get() { return value; } };
  selRefs[key] = ref;
  return ref;
}

/* ---------- 主题色：白 / 黑 / 跟随系统（CSS 变量整体换装） ---------- */

const themeMedia = window.matchMedia ? window.matchMedia('(prefers-color-scheme: dark)') : null;

function applyTheme() {
  const pref = lsGet('jx.theme', 'light');
  let t = pref;
  if (pref === 'auto' && themeMedia) t = themeMedia.matches ? 'dark' : 'light';
  document.documentElement.dataset.theme = t;
}
if (themeMedia && themeMedia.addEventListener) {
  themeMedia.addEventListener('change', () => { if (lsGet('jx.theme', 'light') === 'auto') applyTheme(); });
}

// 常规页各下拉：值即时落本机并生效
makeSelect('sel-lang', 'lang', [
  { v: 'zh', label: '简体中文' },
  { v: 'en', label: 'English（翻译逐步补齐）' },
], (v) => lsSet('jx.lang', v));
makeSelect('sel-fontsize', 'fontsize', [
  { v: '12', label: '12 px' },
  { v: '13', label: '13 px' },
  { v: '14', label: '14 px（默认）' },
  { v: '15', label: '15 px' },
  { v: '16', label: '16 px' },
  { v: '17', label: '17 px' },
  { v: '18', label: '18 px' },
  { v: '20', label: '20 px' },
], (v) => applyChatFont(parseFloat(v) || 14));
// 主题色：白/黑/跟随系统的预览卡片，点选即时生效
function syncThemeCards() {
  const cur = lsGet('jx.theme', 'light');
  document.querySelectorAll('#theme-cards .theme-card').forEach((b) => {
    b.classList.toggle('cur', b.dataset.v === cur);
  });
}
document.querySelectorAll('#theme-cards .theme-card').forEach((b) => {
  b.onclick = () => { lsSet('jx.theme', b.dataset.v); applyTheme(); syncThemeCards(); };
});

// 点缀色：内置强调色板（原 accent-theme 组件并入，8 预设 + 自定义）
const ACCENTS = [
  ['默认蓝', '#4d6bfe', '#3d5bef'],
  ['紫', '#8250df', '#6f42c1'],
  ['绿', '#1f9d55', '#188a49'],
  ['橘', '#bc4c00', '#a34400'],
  ['红', '#c22f2f', '#ab2727'],
  ['青', '#0d7ea5', '#0b6d8d'],
  ['粉', '#bf3989', '#a63075'],
  ['金', '#9a6700', '#855900'],
];

let accentStyleEl = null;

function applyAccent(color, hover, save) {
  if (!accentStyleEl) {
    accentStyleEl = document.createElement('style');
    accentStyleEl.id = 'accent-style';
    document.head.appendChild(accentStyleEl);
  }
  accentStyleEl.textContent = ':root{--accent:' + color + ';--accent-hover:' + hover + ';}';
  if (save) lsSet('jx.accent', color + '|' + hover);
}

function buildAccentRow() {
  const row = $('accent-row');
  if (!row) return;
  const saved = lsGet('jx.accent', lsGet('cx:accent', '')); // 迁移旧组件的本地存储
  row.innerHTML = '';
  for (const [name, c, h] of ACCENTS) {
    const b = document.createElement('button');
    b.type = 'button';
    b.className = 'acc-swatch';
    b.style.background = c;
    b.title = name;
    if (saved.startsWith(c + '|')) b.classList.add('cur');
    b.onclick = () => {
      applyAccent(c, h, true);
      row.querySelectorAll('.acc-swatch').forEach((s) => s.classList.remove('cur'));
      b.classList.add('cur');
    };
    row.appendChild(b);
  }
  const custom = document.createElement('input');
  custom.type = 'color';
  custom.title = '自定义颜色';
  custom.oninput = () => {
    const c = custom.value;
    const d = c.replace(/^#(..)(..)(..)$/, (_, r, g, bl) => {
      const f = (x) => Math.max(0, parseInt(x, 16) - 18).toString(16).padStart(2, '0');
      return '#' + f(r) + f(g) + f(bl);
    });
    applyAccent(c, d, true);
    row.querySelectorAll('.acc-swatch').forEach((s) => s.classList.remove('cur'));
  };
  row.appendChild(custom);
  if (saved) {
    const [c, h] = saved.split('|');
    applyAccent(c, h || c, false);
  }
}
buildAccentRow();
makeSelect('sel-sendkey', 'sendkey', [
  { v: 'enter', label: 'Enter 发送，Shift+Enter 换行' },
  { v: 'ctrl', label: 'Ctrl+Enter 发送，Enter 换行' },
], (v) => lsSet('jx.sendkey', v));
makeSelect('sel-midrun', 'midrun', [
  { v: 'interject', label: '插话：不打断，下一步调用模型前注入' },
  { v: 'queue', label: '等待：本轮结束后排队逐条发送' },
], (v) => lsSet('jx.midrun', v));
makeSelect('sel-compact-mode', 'compactMode', [
  { v: 'rule', label: '规则压缩（零成本，默认）' },
  { v: 'llm', label: '模型摘要（达阈值自动+手动）' },
  { v: 'off', label: '关闭' },
], null);
makeSelect('sel-compact-threshold', 'compactThreshold', [
  { v: '0.8', label: '80%' },
  { v: '0.9', label: '90%' },
], null);
$('btn-drawer').onclick = toggleDrawer;
// 语音输入：浏览器原生识别，边说边追加到输入框
let voiceRecog = null;
let voiceOn = false;
$('btn-voice').onclick = () => {
  const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
  if (!SR) { $('hint').textContent = '当前浏览器不支持语音识别'; setTimeout(() => { $('hint').textContent = ''; }, 2500); return; }
  if (voiceOn) { voiceRecog && voiceRecog.stop(); return; }
  voiceRecog = new SR();
  voiceRecog.lang = 'zh-CN';
  voiceRecog.interimResults = false;
  voiceRecog.continuous = true;
  voiceRecog.onresult = (e) => {
    let text = '';
    for (let i = e.resultIndex; i < e.results.length; i++) text += e.results[i][0].transcript;
    if (text) {
      $('input').value += ($('input').value ? ' ' : '') + text;
      autoGrow();
      persistSessionDraftLS();
    }
  };
  voiceRecog.onend = () => { voiceOn = false; $('btn-voice').classList.remove('on'); };
  voiceRecog.onerror = (e) => {
    voiceOn = false;
    $('btn-voice').classList.remove('on');
    if (e.error !== 'aborted') { $('hint').textContent = '语音识别失败：' + e.error; setTimeout(() => { $('hint').textContent = ''; }, 2500); }
  };
  try {
    voiceRecog.start();
    voiceOn = true;
    $('btn-voice').classList.add('on');
  } catch (e) { /* 重复启动等 */ }
};
$('drawer-close').onclick = () => $('drawer').classList.add('hidden');
$('btn-new-aux').onclick = newAuxChat;
$('input').addEventListener('keydown', (e) => {
  if (e.key !== 'Enter') return;
  if (lsGet('jx.sendkey', 'enter') === 'ctrl') {
    if (e.ctrlKey || e.metaKey) { e.preventDefault(); send(); }
  } else if (!e.shiftKey && !e.ctrlKey && !e.metaKey) {
    e.preventDefault(); send();
  }
});
$('input').addEventListener('input', () => {
  autoGrow();
  persistHomeDraftLS();       // 首页：输入与选择即时落本地
  persistSessionDraftLS();    // 对话页：未发送输入按会话落本地
});

restoreHomeDraftLS(); // 先恢复上次草稿（输入与全部选择），再渲染首页
restoreChatFont();    // 恢复常规页选的对话区字号（px）
restoreContentWidth();// 恢复上次拖定的对话区宽度
setupWidthHandles();  // 对话区两侧拖拽把手
applyTheme();         // 恢复常规页选的主题色（白/黑/跟随系统）
applySidebarState();
resetEmpty();
loadStatus().then(loadSessions);
loadModels();
connectEvents();
loadApprovals();
loadQuestions();
setInterval(loadApprovals, 20000); // 审批轮询兜底（SSE 断连时也能拿到）
setInterval(loadQuestions, 20000);
