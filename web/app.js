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
  streaming: false,
  abort: null,
  defaultWs: '',    // 默认工作区（文件夹），来自 /api/status
  models: [],       // 可选模型清单，来自 /api/models
  defaultProvider: '',
  defaultModel: '',
  approvals: [],    // 待用户决定的越权审批（右下角卡片）
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
function lsGet(k, dft) { try { const v = localStorage.getItem(k); return v === null ? dft : JSON.parse(v); } catch (e) { return dft; } }
function lsSet(k, v) { try { localStorage.setItem(k, JSON.stringify(v)); } catch (e) {} }

// 侧栏视图设置：分组方式（ws=按工作区 flat=单列表）与排序方式
const viewCfg = Object.assign({ group: 'ws', sort: 'updated' }, lsGet('jx.view', {}));
const wsCollapsed = lsGet('jx.wscol', {});   // 工作区分组折叠状态
let sideSearchQ = '';                        // 侧栏搜索词

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

function toggleSideSearch() {
  const row = document.getElementById('side-search');
  if (!row) return;
  const showing = row.classList.contains('hidden');
  row.classList.toggle('hidden');
  if (showing) {
    const inp = document.getElementById('side-search-input');
    inp.value = sideSearchQ;
    inp.focus();
  } else {
    sideSearchQ = '';
    loadSessions();
  }
}

function openViewMenu(anchor) {
  const r = anchor.getBoundingClientRect();
  openCtxMenu([
    { label: '分组：' + (viewCfg.group === 'ws' ? '按工作区 ✓' : '按工作区'), fn: () => { viewCfg.group = 'ws'; lsSet('jx.view', viewCfg); loadSessions(); } },
    { label: '分组：' + (viewCfg.group === 'flat' ? '单列表 ✓' : '单列表'), fn: () => { viewCfg.group = 'flat'; lsSet('jx.view', viewCfg); loadSessions(); } },
    { label: '排序：' + (viewCfg.sort === 'updated' ? '最近更新 ✓' : '最近更新'), fn: () => { viewCfg.sort = 'updated'; lsSet('jx.view', viewCfg); loadSessions(); } },
    { label: '排序：' + (viewCfg.sort === 'created' ? '创建时间 ✓' : '创建时间'), fn: () => { viewCfg.sort = 'created'; lsSet('jx.view', viewCfg); loadSessions(); } },
  ], r.right - 160, r.bottom + 4);
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
  let visible = state.sessions.filter((s) => s.mode !== 'assistant' && !s.parentId && !s.archived);
  if (sideSearchQ) {
    const q = sideSearchQ.toLowerCase();
    visible = visible.filter((s) => (s.title || '').toLowerCase().includes(q));
  }
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
  state.msgs = [];
  resetEmpty();
  loadSessions();
  $('input').focus();
}

async function openSession(id) {
  const data = await api('/api/sessions/' + id);
  state.cur = data.meta;
  state.msgs = data.messages || [];
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
  loadSessions();
  JX.emit('session:open', state.cur);
}

/* ---------- 消息渲染 ---------- */

function toolCard(tc, result) {
  const d = document.createElement('details');
  d.className = 'tool-card' + (result ? '' : ' running');
  const name = (tc.function && tc.function.name) || '?';
  let argsDisp = (tc.function && tc.function.arguments) || '';
  try { argsDisp = JSON.stringify(JSON.parse(argsDisp), null, 2); } catch (e) { /* 原样显示 */ }
  let out = String((result && result.content) || '');
  if (out.length > 4000) out = out.slice(0, 4000) + '\n…';
  d.innerHTML =
    `<summary>工具调用：${esc(name)}${result ? '' : ' — 运行中…'}</summary>` +
    `<div class="tc-args"><b>参数</b><pre>${esc(argsDisp)}</pre></div>` +
    (result ? `<div class="tc-out"><b>结果</b><pre>${esc(out)}</pre></div>` : '');
  return d;
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
  for (const m of msgs) {
    if (m.role === 'user') {
      const txt = String(m.content ?? '');
      // 系统注入的回报/中断/上传记录：以系统通知样式呈现，不冒充用户发言
      if (txt.startsWith('【派发回报】') || txt.startsWith('【系统中断】') || txt.startsWith('【执行中断】') || txt.startsWith('【文件上传】')) {
        const wrap = document.createElement('div');
        wrap.className = 'msg sysnote';
        const b = document.createElement('div');
        b.className = 'bubble';
        b.textContent = txt;
        wrap.appendChild(b);
        box.appendChild(wrap);
        continue;
      }
      const wrap = document.createElement('div');
      wrap.className = 'msg user';
      const b = document.createElement('div');
      b.className = 'bubble';
      b.textContent = txt;
      wrap.appendChild(b);
      box.appendChild(wrap);
    } else if (m.role === 'assistant') {
      const wrap = document.createElement('div');
      wrap.className = 'msg assistant';
      const body = document.createElement('div');
      body.className = 'md';
      if (m.content) body.innerHTML = md(m.content);
      for (const tc of m.tool_calls || []) {
        const n = (tc.function && tc.function.name) || '';
        if (n === 'dispatch_task' || n === 'send_to_session') {
          body.appendChild(subTaskCard(tc, resultOf[tc.id]));
        } else {
          body.appendChild(toolCard(tc, resultOf[tc.id]));
        }
      }
      wrap.appendChild(body);
      // 悬停操作：复制原文 / 手动压缩
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
      wrap.appendChild(acts);
      box.appendChild(wrap);
    }
    // role === 'tool' 的消息已配对进工具卡片，跳过
  }
  $('messages').scrollTop = $('messages').scrollHeight;
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
      el.innerHTML = '<div class="md"></div>';
      $('msg-col').appendChild(el);
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
    case 'message':
      if (viewing && !state.streaming && ev.message && ev.message.role === 'user') refreshCurrent();
      break;
    case 'done':
      if (viewing && !state.streaming) { state.streamText = ''; removeLive(); refreshCurrent(); }
      if (state.cur && state.cur.mode === 'assistant') setFace('done', 4000);
      break;
    case 'report_ready':
    case 'session_updated':
      if (!viewing && ev.type === 'session_updated') dotDone[ev.sessionId] = dotDone[ev.sessionId] || aRunJustEnded(ev.sessionId);
      loadSessions();
      refreshDrawer();
      if (viewing && !state.streaming) refreshCurrent();
      break;
    case 'approval_required':
      pushApproval(ev);
      break;
    case 'approval_resolved':
      removeApproval(ev.approvalId);
      break;
    case 'error':
      if (viewing && !state.streaming) refreshCurrent();
      break;
  }
}

/* ---------- 审批卡片（沙箱外执行等越权动作：允许 / 总是允许 / 拒绝） ---------- */

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
  if (!state.approvals.length) { bar.classList.add('hidden'); return; }
  bar.classList.remove('hidden');
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
      '<button class="ok always" data-act="always">本会话总是允许</button>' +
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

/* ---------- SSH 远程工作区表单 ---------- */

function remoteFormPrefill() {
  const r = (state.cur && state.cur.remote) || homeDraft.remote || {};
  $('rm-host').value = r.host || '';
  $('rm-port').value = r.port && r.port !== 22 ? r.port : '';
  $('rm-user').value = r.user || '';
  $('rm-auth').value = r.auth || 'key';
  $('rm-key').value = r.keyPath || '';
  $('rm-pass').value = r.password || '';
  $('rm-dir').value = r.dir || '';
  $('rm-result').textContent = '';
  $('rm-result').className = 'remote-result';
  $('rm-clear').style.display = ((state.cur && state.cur.remote) || homeDraft.remote) ? '' : 'none';
  syncRemoteFormVis();
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

function showRemoteResult(ok, msg, hostKey) {
  const el = $('rm-result');
  el.className = 'remote-result ' + (ok ? 'ok' : 'bad');
  el.textContent = msg + (ok && hostKey ? '｜主机指纹 ' + hostKey : '');
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

async function applyRemote() {
  const spec = collectRemoteSpec();
  if (!spec.host || !spec.dir) { showRemoteResult(false, '主机与远程目录必填'); return; }
  if (spec.auth === 'key' && !spec.keyPath) { showRemoteResult(false, '私钥文件路径必填（或改用密码认证）'); return; }
  if (spec.auth === 'password' && !spec.password) { showRemoteResult(false, '密码必填'); return; }
  if (state.cur) {
    try {
      state.cur = await api('/api/sessions/' + state.cur.id + '/remote', { method: 'POST', body: JSON.stringify({ remote: spec }) });
    } catch (e) { showRemoteResult(false, '保存失败：' + e.message); return; }
  } else {
    homeDraft.remote = spec;
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
  closePopovers();
  syncComposer();
  loadSessions();
}

/* ---------- 发送与事件流 ---------- */

function setStreamUI(on) {
  $('btn-send').disabled = on;
  $('btn-stop').classList.toggle('hidden', !on);
  if (!on) $('hint').textContent = '';
}

function handleEvent(ev) {
  switch (ev.type) {
    case 'message': {
      state.streamText = '';
      const live = document.getElementById('live-bubble');
      if (live) live.remove();
      state.msgs.push(ev.message);
      renderMessages(state.msgs);
      break;
    }
    case 'delta':
      state.streamText += ev.delta;
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

async function send() {
  const input = $('input');
  const content = input.value.trim();
  if (!content || state.streaming) return;
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
  input.value = '';
  autoGrow();
  clearSessionDraftLS(state.cur.id);

  state.streaming = true;
  setStreamUI(true);
  state.abort = new AbortController();
  state.streamText = '';
  JX.emit('send', { content });

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
    if (e.name !== 'AbortError') {
      state.streamText = '';
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
  renderMessages(state.msgs);
  state.streaming = false;
  setStreamUI(false);
  loadSessions();
  loadStatus();
  JX.emit('message:done', { sessionId: state.cur && state.cur.id });
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

async function pickModel(key) {
  closePopovers();
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

function renderModelPop() {
  const pop = $('model-pop');
  const cur = currentModelKey();
  let html = `<button class="pop-item model-item ${cur === '__default__' ? 'sel' : ''}" data-key="__default__">默认（${esc(state.defaultModel || '未配置')}）</button>`;
  let lastProv = '';
  for (const m of state.models || []) {
    if (m.providerId !== lastProv) {
      html += `<div class="pop-group">${esc(m.providerName)}</div>`;
      lastProv = m.providerId;
    }
    const k = m.providerId + '|' + m.modelId;
    html += `<button class="pop-item model-item ${k === cur ? 'sel' : ''}" data-key="${esc(k)}">${esc(m.modelName || m.modelId)}</button>`;
  }
  pop.innerHTML = html;
  pop.querySelectorAll('.model-item').forEach((b) => { b.onclick = () => pickModel(b.dataset.key); });
}

/* ---------- 弹出面板系统：加号菜单 / 权限面板 / 模型面板 / 目录面板 ---------- */

const PERM_LABELS = { 'read-only': '只读', 'workspace-write': '工作区内读写', 'full': '完全访问' };
const homeDraft = { permission: 'workspace-write', provider: '', model: '', tools: [], planMode: false, goalMode: false, remote: null };

function closePopovers() {
  document.querySelectorAll('.popover').forEach((p) => p.classList.add('hidden'));
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
  $('model-chip-label').textContent = modelNameOf(currentModelKey());
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

function renderPermPop() {
  const s = sessionOrDraft();
  const pop = $('perm-pop');
  pop.querySelectorAll('input[name=perm]').forEach((r) => { r.checked = r.value === s.permission; });
  pop.querySelectorAll('input[data-tool]').forEach((c) => { c.checked = s.tools.includes(c.dataset.tool); });
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

let provDraft = []; // 设置页编辑中的 provider 列表（不直接改全局配置）

// 设置浮窗：左目录切换
function setSettingsPane(name) {
  document.querySelectorAll('.set-nav-item').forEach((b) => {
    b.classList.toggle('active', b.dataset.pane === name);
  });
  document.querySelectorAll('.set-pane').forEach((p) => {
    p.classList.toggle('active', p.id === 'set-pane-' + name);
  });
}

async function openSettings() {
  const c = await api('/api/config');
  $('set-baseurl').value = c.baseURL || '';
  const keyInput = $('set-apikey');
  keyInput.value = '';
  keyInput.placeholder = c.apiKeyMasked ? `已保存（${c.apiKeyMasked}），留空不修改` : 'sk-...';
  provDraft = (c.providers || []).map((p) => ({
    id: p.id, name: p.name, baseURL: p.baseURL,
    key: '', keyMasked: p.apiKeyMasked, models: p.models || [],
  }));
  renderProvList();
  $('set-workspace').value = c.workspace || '';
  $('set-bindhost').value = c.bindHost || '';
  $('set-maxsteps').value = c.maxSteps || 30;
  $('set-allowshell').checked = !!c.allowShell;
  $('set-compact-mode').value = c.compactMode || 'rule';
  $('set-keeprounds').value = c.keepRounds || 5;
  $('set-ruleinterval').value = c.ruleInterval || 5;
  $('set-compact-threshold').value = String(c.compactThreshold || 0.8);
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

function renderProvList() {
  const box = $('prov-list');
  box.innerHTML = '';
  provDraft.forEach((p, i) => {
    const row = document.createElement('div');
    row.className = 'prov-row';
    row.innerHTML =
      `<div class="prov-line1">` +
      `<input class="p-name" value="${esc(p.name)}" placeholder="名称">` +
      `<input class="p-url" value="${esc(p.baseURL)}" placeholder="baseURL">` +
      `<button class="p-del" title="删除">✕</button></div>` +
      `<div class="prov-line2">` +
      `<input class="p-key" type="password" placeholder="${p.keyMasked ? '已保存（' + esc(p.keyMasked) + '），留空不修改' : 'API Key'}">` +
      `<button class="p-fetch">拉取模型</button>` +
      `<span class="p-models">${p.models.length ? p.models.length + ' 个模型' : '未拉取'}</span></div>`;
    row.querySelector('.p-name').oninput = (e) => { provDraft[i].name = e.target.value; };
    row.querySelector('.p-url').oninput = (e) => { provDraft[i].baseURL = e.target.value; };
    row.querySelector('.p-key').oninput = (e) => { provDraft[i].key = e.target.value; };
    row.querySelector('.p-del').onclick = () => { provDraft.splice(i, 1); renderProvList(); };
    row.querySelector('.p-fetch').onclick = async (e) => {
      const btn = e.target;
      btn.textContent = '拉取中…'; btn.disabled = true;
      try {
        const d = await api('/api/providers/fetch', { method: 'POST', body: JSON.stringify({ id: provDraft[i].id }) });
        provDraft[i].models = d.models || [];
        renderProvList();
      } catch (err) {
        alert('拉取失败：' + err.message);
        btn.textContent = '拉取模型'; btn.disabled = false;
      }
    };
    box.appendChild(row);
  });
}

async function saveSettings() {
  const providers = provDraft.map((p) => ({
    id: p.id, name: p.name || p.id, baseURL: p.baseURL, apiKey: p.key || '',
    models: p.models,
  }));
  const body = {
    baseURL: $('set-baseurl').value,
    workspace: $('set-workspace').value,
    bindHost: $('set-bindhost').value,
    allowShell: $('set-allowshell').checked,
    maxSteps: parseInt($('set-maxsteps').value, 10) || 30,
    providers,
  };
  const key = $('set-apikey').value.trim();
  if (key) body.apiKey = key;
  body.compact = {
    mode: $('set-compact-mode').value,
    keepRounds: parseInt($('set-keeprounds').value, 10) || 5,
    ruleInterval: parseInt($('set-ruleinterval').value, 10) || 5,
    threshold: parseFloat($('set-compact-threshold').value) || 0.8,
  };
  await api('/api/config', { method: 'POST', body: JSON.stringify(body) });
  $('settings').classList.add('hidden');
  loadStatus();
  loadModels();
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
document.getElementById('side-search-input').addEventListener('input', (e) => {
  sideSearchQ = e.target.value.trim();
  loadSessions();
});
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
$('rm-apply').onclick = applyRemote;
$('rm-clear').onclick = clearRemote;
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
// 权限面板：单选立即生效，多选即时收集
$('perm-pop').addEventListener('change', (e) => {
  if (e.target.name === 'perm') {
    patchDraftOrSession({ permission: e.target.value });
  } else if (e.target.dataset.tool) {
    const s = sessionOrDraft();
    const set = new Set(s.tools || []);
    if (e.target.checked) set.add(e.target.dataset.tool); else set.delete(e.target.dataset.tool);
    patchDraftOrSession({ tools: [...set] });
  }
});
// 点面板外收起所有弹出层
document.addEventListener('click', (e) => {
  if (!e.target.closest('.popover') && !e.target.closest('#btn-plus') &&
      !e.target.closest('#perm-chip') && !e.target.closest('#model-chip') && !e.target.closest('#ws-row')) {
    closePopovers();
  }
});
document.addEventListener('keydown', (e) => { if (e.key === 'Escape') closePopovers(); });
$('prov-add-btn').onclick = () => {
  const name = $('prov-new-name').value.trim();
  const url = $('prov-new-url').value.trim();
  const key = $('prov-new-key').value.trim();
  if (!name || !url) { alert('名称与 baseURL 必填'); return; }
  provDraft.push({
    id: 'custom-' + Date.now().toString(36), name, baseURL: url,
    key, keyMasked: '', models: [],
  });
  $('prov-new-name').value = ''; $('prov-new-url').value = ''; $('prov-new-key').value = '';
  renderProvList();
};
$('settings').onclick = (e) => { if (e.target === $('settings')) $('settings').classList.add('hidden'); };
document.querySelectorAll('.set-nav-item').forEach((b) => {
  b.onclick = () => setSettingsPane(b.dataset.pane);
});
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
  if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(); }
});
$('input').addEventListener('input', () => {
  autoGrow();
  persistHomeDraftLS();       // 首页：输入与选择即时落本地
  persistSessionDraftLS();    // 对话页：未发送输入按会话落本地
});

restoreHomeDraftLS(); // 先恢复上次草稿（输入与全部选择），再渲染首页
applySidebarState();
resetEmpty();
loadStatus().then(loadSessions);
loadModels();
connectEvents();
loadApprovals();
setInterval(loadApprovals, 20000); // 审批轮询兜底（SSE 断连时也能拿到）
