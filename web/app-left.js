/* app-left.js —— 左侧栏 + 顶栏逻辑：会话列表/搜索/回收站/工作区目录、侧栏开合
   （jx.sb 记忆）、顶栏状态胶囊。第二个加载。 */

/* ---------- 状态与侧栏 ---------- */

// 顶栏状态区：异常态（缺 Key / 离线）才显示告警胶囊；正常态收起胶囊，
// 换成手机图标——远程访问的快捷入口，点击直达设置的网络访问分区看扫码。
async function loadStatus() {
  const el = $('status-text');
  const ph = $('status-phone');
  try {
    const st = await api('/api/status');
    state.defaultWs = st.workspace || '';
    if (!st.hasKey) {
      el.textContent = '未配置 Key'; el.className = 'pill warn'; el.classList.remove('hidden');
      ph.classList.add('hidden');
    } else {
      el.classList.add('hidden');
      ph.classList.remove('hidden');
      const ra = st.remote || {};
      ph.className = 'foot-phone' + (ra.enabled ? (ra.connected ? '' : ' half') : ' off');
      ph.title = ra.enabled
        ? (ra.connected ? '手机远程访问已就绪——点击扫码接入' : '远程访问已开启，网关未连上——点击查看')
        : '开启手机远程访问——点击设置';
    }
    syncComposer();
  } catch (e) {
    el.textContent = '服务离线'; el.className = 'pill gray'; el.classList.remove('hidden');
    ph.classList.add('hidden');
  }
}

$('status-phone').addEventListener('click', (e) => {
  e.stopPropagation(); // 拦住外层设置按钮的默认打开路径，改直达网络访问分区
  openSettings('network');
});


// 侧栏视图设置：分组方式（ws=按工作区 flat=单列表）与排序方式
const viewCfg = Object.assign({ group: 'ws', sort: 'updated' }, lsGet('jx.view', {}));
const wsCollapsed = lsGet('jx.wscol', {});   // 工作区分组折叠状态


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

// 搜索范围跟随侧栏视图：正常工作区只搜正常内容，回收站只搜回收站内容。
// ws = 回收目录列表（默认工作区存空串，被回收时把 "" 一并带上）；
// keep = 已单独恢复的会话（目录还回收着，但对话已回到正常区）。
function searchScopeQS() {
  const recycled = lsGet('jx.wsrecycled', []);
  const wss = (state.defaultWs && recycled.includes(state.defaultWs)) ? [...recycled, ''] : recycled;
  const keep = lsGet('jx.wskeep', []);
  return '&bin=' + (binView ? 1 : 0)
    + '&ws=' + encodeURIComponent(JSON.stringify(wss))
    + '&keep=' + encodeURIComponent(JSON.stringify(keep));
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

  const scopeQS = searchScopeQS();
  api('/api/search/title?q=' + eq + scopeQS, { signal }).then((d) => {
    if (token !== searchToken) return;
    renderTitleHits(d.hits || [], q);
  }).catch((e) => {
    if (e.name === 'AbortError' || token !== searchToken) return;
    $('ssr-title').innerHTML = '<div class="ssr-empty">搜索失败：' + esc(e.message) + '</div>';
  });

  api('/api/search/content?q=' + eq + scopeQS, { signal }).then((d) => {
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
    b.onclick = () => { closeSideSearch(); openSession(h.sessionId); };
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
// 窄屏（手机远程访问）例外：原生对话框弹在宿主机屏幕上，手机这边只会
// 永远转圈——改为直接输入绝对路径，按钮在手机端才真正可用。
let pickingFolder = false;
async function pickWorkFolder() {
  if (pickingFolder) return '';
  pickingFolder = true;
  try {
    if (isNarrow()) {
      const p = prompt('输入工作目录的完整路径（如 D:\\项目\\某目录）', state.defaultWs || '');
      return (p || '').trim();
    }
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
  const h = lsGet('jx.wshidden', []);
  if (h.includes(ws)) lsSet('jx.wshidden', h.filter((x) => x !== ws)); // 手动重新添加 = 取消拉黑
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
  if (!(await confirmCenter('删除该会话？此操作不可恢复。'))) return;
  await api('/api/sessions/' + s.id, { method: 'DELETE' });
  removeSessionLocalData(s.id); // 彻底删除连带清掉它的本地草稿/排队缓存
  if (state.cur && state.cur.id === s.id) {
    state.cur = null;
    state.msgs = [];
    resetEmpty();
  }
  loadSessions();
}

function openSessionMenu(s, rect) {
  // 回收站视图里的会话菜单：恢复 / 重命名 / 彻底删除
  if (binView) {
    openCtxMenu([
      { label: '恢复', fn: () => restoreSession(s) },
      { label: '重命名', fn: async () => {
        const t = prompt('重命名会话', s.title || '');
        if (t === null || !t.trim()) return;
        await api('/api/sessions/' + s.id, { method: 'PATCH', body: JSON.stringify({ title: t.trim() }) });
        loadSessions();
      } },
      { label: '彻底删除', danger: true, fn: () => deleteSession(s) },
    ], rect.right - 8, rect.bottom + 4);
    return;
  }
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
        openSession(fork.id);
      } catch (e) { alert('分支失败：' + e.message); }
    } },
    { label: '移入回收站', danger: true, fn: async () => {
      await api('/api/sessions/' + s.id, { method: 'PATCH', body: JSON.stringify({ archived: true }) });
      lsSet('jx.wskeep', lsGet('jx.wskeep', []).filter((x) => x !== s.id)); // 归档即离开"单独保留"，保证恢复往返一致
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
  // 正在执行的会话（含切走后仍在后台跑的那一轮）：时间位换成「⟳ 运行中」，
  // 让用户知道切走不等于停掉
  const timeCell = s.status === 'running'
    ? '<span class="s-time running" title="正在执行（切走不影响，切回来会接上）">⟳ 运行中</span>'
    : `<span class="s-time">${esc(relTime(s.updatedAt))}</span>`;
  // 时间线视图：条目脱离了工作区分组的语境，行首补一枚工作目录小胶囊
  //（取目录名开头两三个字，塞不下的从右缘直接裁掉——不用省略号，固定宽一列对齐）
  let wsChip = '';
  if (viewCfg.group === 'time' && !binView) {
    const ws = wsOf(s);
    const name = lsGet('jx.wsalias', {})[ws] || baseName(ws) || '默认工作区';
    wsChip = `<span class="s-ws" title="${esc(ws ? name + '（' + ws + '）' : '默认工作区')}">${esc(name)}</span>`;
  }
  item.innerHTML =
    wsChip +
    `<span class="dot${dot}"></span>` +
    `<span class="s-title">${esc(s.title || '(未命名)')}</span>` +
    timeCell +
    `<span class="row-acts">` +
      (binView ? `<button class="ra-restore" title="恢复"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 12a9 9 0 1 0 3-6.7"/><path d="M3 4v5h5"/></svg></button>` : '') +
      `<button class="ra-copy" title="复制标题"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h10"/></svg></button>` +
      `<button class="ra-more" title="更多"><svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.6"/><circle cx="12" cy="12" r="1.6"/><circle cx="19" cy="12" r="1.6"/></svg></button>` +
    `</span>`;
  item.onclick = (e) => {
    if (e.target.closest('.row-acts')) return;
    openSession(s.id);
  };
  item.querySelector('.ra-copy').onclick = (e) => {
    e.stopPropagation();
    navigator.clipboard.writeText(s.title || '(未命名)');
    const b = e.currentTarget;
    b.classList.add('ok'); setTimeout(() => b.classList.remove('ok'), 900);
  };
  const raRestore = item.querySelector('.ra-restore');
  if (raRestore) raRestore.onclick = (e) => { e.stopPropagation(); restoreSession(s); };
  item.querySelector('.ra-more').onclick = (e) => {
    e.stopPropagation();
    openSessionMenu(s, item.getBoundingClientRect());
  };
  item.oncontextmenu = (e) => { e.preventDefault(); openSessionMenu(s, item.getBoundingClientRect()); };
  return item;
}

/* ---------- 回收站视图：点底部「回收站」，整个工作区列表切成回收站 ----------
   回收站内容 = 已移入回收站的工作目录（jx.wsrecycled）+ 已归档会话（archived=1）
   以及挂在回收目录下的对话（它们在正常视图里被整组隐藏）。 */

let binView = false;
let binAnimTimer = 0; // 入场动画类的摘除定时器（见 setBinView）

function setBinView(on) {
  binView = !!on;
  if (sideSearchOpen()) closeSideSearch(); // 切换视图前先还原搜索占领态（结果范围已随视图变化）
  $('btn-recycle').classList.toggle('active', binView);
  document.body.classList.toggle('bin-mode', binView);
  // 边框「从下往上」入场动画：0.22s，只有缓入没有缓出（.6,0,1,1：
  // 前半段放缓蓄力、一路加速窜上、到顶即停），
  // 进入时挂 0.35s 动画类，随后摘除、回落到静态红边
  // （动画正常则无缝，时钟冻结也不至于红边一直不可见）
  clearTimeout(binAnimTimer);
  if (binView) {
    document.body.classList.add('bin-anim');
    binAnimTimer = setTimeout(() => document.body.classList.remove('bin-anim'), 350);
  } else {
    document.body.classList.remove('bin-anim');
  }
  $('ws-section-title').textContent = binView ? '回收站' : '工作区';
  $('side-search-input').placeholder = binView ? '搜索回收站' : '搜索工作任务';
  $('wst-search').title = binView ? '搜索回收站' : '搜索工作任务';
  loadSessions();
}

function restoreWorkspace(ws) {
  lsSet('jx.wsrecycled', lsGet('jx.wsrecycled', []).filter((x) => x !== ws));
  // 目录整体恢复时清掉该目录下会话的"单独保留"名单，下次回收从干净状态开始
  const ids = new Set((state.sessions || []).filter((s) => wsOf(s) === ws).map((s) => s.id));
  if (ids.size) lsSet('jx.wskeep', lsGet('jx.wskeep', []).filter((x) => !ids.has(x)));
  loadSessions();
}

// 回收站里恢复单个对话：已归档的取消归档；随目录回收的加入保留名单（正常视图单独可见）
async function restoreSession(s) {
  const wsRecycled = lsGet('jx.wsrecycled', []).includes(wsOf(s));
  if (s.archived) await api('/api/sessions/' + s.id, { method: 'PATCH', body: JSON.stringify({ archived: false }) });
  if (wsRecycled) lsSet('jx.wskeep', [...new Set([...lsGet('jx.wskeep', []), s.id])]);
  loadSessions();
}

// 彻底删除工作目录：删除其名下全部会话记录、注销登记并拉黑（不碰磁盘文件夹）
async function deleteWorkspace(ws) {
  const name = lsGet('jx.wsalias', {})[ws] || baseName(ws) || '默认工作区';
  const targets = (state.sessions || []).filter((s) => s.mode !== 'assistant' && wsOf(s) === ws);
  if (!(await confirmCenter(`彻底删除工作目录「${name}」及其 ${targets.length} 个对话？\n只从应用中移除记录，不会删除磁盘上的文件夹。默认工作区路径来自配置，删除后也不会再显示空目录，直到你重新添加它。\n此操作不可恢复。`))) return;
  if (!(await confirmCenter(`再次确认：目录「${name}」及其全部对话将被彻底删除、无法恢复。\n确定要继续吗？`, false, true))) return;
  for (const s of targets) {
    try { await api('/api/sessions/' + s.id, { method: 'DELETE' }); } catch (e) { /* 已不存在则跳过 */ }
    removeSessionLocalData(s.id);
  }
  unregisterWorkspace(ws);
  hideWorkspace(ws);
  lsSet('jx.wskeep', lsGet('jx.wskeep', []).filter((x) => !targets.some((t) => t.id === x)));
  if (state.cur && targets.some((t) => t.id === state.cur.id)) { state.cur = null; state.msgs = []; resetEmpty(); }
  loadSessions();
}

// 拉黑一个目录：即使它是默认工作区（配置无法注销）也不再出现在列表，
// 直到被手动重新添加、或名下重新有了会话（会话建组不查黑名单，天然复活）
function hideWorkspace(ws) {
  lsSet('jx.wshidden', [...new Set([...lsGet('jx.wshidden', []), ws])]);
}

// 注销一个工作目录的全部本地登记（列表/回收站/别名/折叠状态）
function unregisterWorkspace(ws) {
  lsSet('jx.wslist', lsGet('jx.wslist', []).filter((x) => x !== ws));
  lsSet('jx.wsrecycled', lsGet('jx.wsrecycled', []).filter((x) => x !== ws));
  const am = lsGet('jx.wsalias', {}); delete am[ws]; lsSet('jx.wsalias', am);
  const cm = lsGet('jx.wscol', {}); delete cm[ws]; lsSet('jx.wscol', cm);
}

// 清空回收站的目标集合：回收站内的全部顶层对话 + 它们的子会话
// （keep 保留的、正常区的都不算；目录由回收列表单独注销）
function collectEmptyTargets() {
  const keep = new Set(lsGet('jx.wskeep', []));
  const recycledWs = lsGet('jx.wsrecycled', []);
  const tops = (state.sessions || []).filter((s) =>
    s.mode !== 'assistant' && !s.parentId && !keep.has(s.id) &&
    (s.archived || recycledWs.includes(wsOf(s))));
  const topIds = new Set(tops.map((s) => s.id));
  const targets = (state.sessions || []).filter((s) =>
    topIds.has(s.id) || (s.parentId && topIds.has(s.parentId)));
  return { tops, targets, recycledWs };
}

// 清空回收站：删除全部回收内容并注销全部回收目录。
// 双级确认——比单条「彻底删除对话」多一道确认。
async function emptyRecycleBin() {
  const { tops, targets, recycledWs } = collectEmptyTargets();
  if (!tops.length && !recycledWs.length) { await confirmCenter('回收站是空的', true); return; }
  if (!(await confirmCenter(`清空回收站？将永久删除 ${recycledWs.length} 个工作目录登记和 ${tops.length} 条对话。\n只从应用中移除记录，不会删除磁盘上的文件夹。`))) return;
  if (!(await confirmCenter('再次确认：整个回收站将被清空，所有内容彻底删除、无法恢复。\n确定要继续吗？', false, true))) return;
  for (const s of targets) {
    try { await api('/api/sessions/' + s.id, { method: 'DELETE' }); } catch (e) { /* 已不存在则跳过 */ }
    removeSessionLocalData(s.id);
  }
  for (const ws of recycledWs) { unregisterWorkspace(ws); hideWorkspace(ws); }
  lsSet('jx.wskeep', []); // 目录登记都注销了，保留名单随之失效
  if (state.cur && targets.some((t) => t.id === state.cur.id)) { state.cur = null; state.msgs = []; resetEmpty(); }
  loadSessions();
}

function renderBin(box) {
  const aliasMap = lsGet('jx.wsalias', {});
  const recycledWs = lsGet('jx.wsrecycled', []);
  const sortIt = (arr) => viewCfg.sort === 'created'
    ? [...arr].sort((a, b) => String(b.createdAt || '').localeCompare(String(a.createdAt || '')))
    : arr;
  // 回收站里的对话：已归档的、或挂在回收目录下且未被单独恢复的
  const keep = new Set(lsGet('jx.wskeep', []));
  const all = (state.sessions || []).filter((s) => s.mode !== 'assistant' && !s.parentId);
  const inBin = all.filter((s) => !keep.has(s.id) && (s.archived || recycledWs.includes(wsOf(s))));
  if (!recycledWs.length && !inBin.length) {
    box.innerHTML = '<div class="ws-empty">回收站是空的</div>';
    return;
  }
  if (recycledWs.length) {
    const label = document.createElement('div');
    label.className = 'bin-label';
    label.textContent = '工作目录';
    box.appendChild(label);
  }
  for (const ws of recycledWs) {
    const items = sortIt(inBin.filter((s) => wsOf(s) === ws));
    const group = document.createElement('div');
    group.className = 'ws-group';
    const collapsed = !!wsCollapsed[ws];
    const head = document.createElement('div');
    head.className = 'ws-group-head bin' + (collapsed ? ' collapsed' : '');
    head.title = ws || '（未设置，使用默认工作区）';
    head.innerHTML =
      `<svg class="chev" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M9 6l6 6-6 6"/></svg>` +
      `<svg class="folder" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/></svg>` +
      `<span class="ws-name">${esc(aliasMap[ws] || baseName(ws) || '默认工作区')}</span>` +
      `<span class="ws-count">${items.length}</span>` +
      `<span class="ws-acts">` +
        `<button class="wa-restore" title="恢复到工作区"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 12a9 9 0 1 0 3-6.7"/><path d="M3 4v5h5"/></svg></button>` +
        `<button class="wa-more" title="工作区操作"><svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.6"/><circle cx="12" cy="12" r="1.6"/><circle cx="19" cy="12" r="1.6"/></svg></button>` +
      `</span>`;
    head.onclick = (e) => {
      if (e.target.closest('.ws-acts')) return;
      wsCollapsed[ws] = !collapsed;
      lsSet('jx.wscol', wsCollapsed);
      loadSessions();
    };
    head.querySelector('.wa-restore').onclick = (e) => {
      e.stopPropagation();
      restoreWorkspace(ws);
    };
    head.querySelector('.wa-more').onclick = (e) => {
      e.stopPropagation();
      const r = e.currentTarget.getBoundingClientRect();
      openCtxMenu([
        { label: '复制路径', fn: () => navigator.clipboard.writeText(ws) },
        { label: '彻底删除目录', danger: true, fn: () => deleteWorkspace(ws) },
      ], r.left, r.bottom + 4);
    };
    head.oncontextmenu = (e) => {
      e.preventDefault();
      head.querySelector('.wa-more').click();
    };
    group.appendChild(head);
    if (!collapsed) {
      const list = document.createElement('div');
      list.className = 'ws-group-list';
      for (const s of items) list.appendChild(sessionItem(s));
      if (!items.length) list.innerHTML = '<div class="ws-empty">（无对话）</div>';
      group.appendChild(list);
    }
    box.appendChild(group);
  }
  // 单独回收的对话：已归档、但其目录没被回收的
  const flat = sortIt(inBin.filter((s) => !recycledWs.includes(wsOf(s))));
  if (flat.length) {
    const label = document.createElement('div');
    label.className = 'bin-label';
    label.textContent = '对话';
    box.appendChild(label);
    for (const s of flat) box.appendChild(sessionItem(s));
  }
}

async function loadSessionsNow() {
  const data = await api('/api/sessions');
  state.sessions = data.sessions || [];
  const box = $('session-list');
  box.innerHTML = '';
  if (binView) { renderBin(box); return; } // 回收站视图：整个列表换成回收站内容
  // 左栏只放顶层未归档会话；助理主对话、子会话/辅助对话不占列表
  // （标题/内容的搜索在搜索面板里走 /api/search/*，这里不再本地过滤）
  let visible = state.sessions.filter((s) => s.mode !== 'assistant' && !s.parentId && !s.archived);
  if (viewCfg.sort === 'created') {
    visible.sort((a, b) => String(b.createdAt || '').localeCompare(String(a.createdAt || '')));
  }
  const recycled = new Set(lsGet('jx.wsrecycled', []));
  const keep = new Set(lsGet('jx.wskeep', [])); // 已单独恢复的对话：目录回收着也照常显示
  const aliasMap = lsGet('jx.wsalias', {});
  if (viewCfg.group === 'flat') {
    box.classList.add('flat');
    let n = 0;
    for (const s of visible) {
      if (!recycled.has(wsOf(s)) || keep.has(s.id)) { box.appendChild(sessionItem(s)); n++; }
    }
    if (!n) box.innerHTML = '<div class="ws-empty">没有会话</div>';
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
    visible.forEach((s) => { if (!recycled.has(wsOf(s)) || keep.has(s.id)) bands[bucketOf(s)].items.push(s); });
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
  for (const [ws, raw] of groups) {
    let items = raw;
    if (recycled.has(ws)) {
      items = items.filter((s) => keep.has(s.id)); // 目录回收了：只显示被单独恢复的对话
      if (!items.length) continue;
    }
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
  const label = collapsed ? '展开左侧栏' : '收起左侧栏';
  const btn = document.getElementById('btn-sidebar');
  if (btn) btn.title = label;
  const txt = document.getElementById('sb-btn-text');
  if (txt) txt.textContent = label;
}

function applySidebarState() {
  const collapsed = lsGet('jx.sb', '') === '1';
  document.body.classList.toggle('sb-collapsed', collapsed);
  setSidebarBtn(collapsed);
  rpApply(); // 左侧栏折叠状态影响右栏可占的剩余宽
}


function toggleSidebar() {
  // 窄屏：整屏浮层只切显隐类——不写 jx.sb、不跑宽度动画（CSS transform 过渡）、
  // 不动右栏推导，桌面的收起记忆不被手机污染
  if (isNarrow()) {
    const show = document.body.classList.toggle('sb-show');
    setSidebarBtn(!show);
    return;
  }
  const sb = $('sidebar');
  const collapsed = document.body.classList.toggle('sb-collapsed');
  setSidebarBtn(collapsed);
  lsSet('jx.sb', collapsed ? '1' : '0');
  rpApply();
  slidePane(sb, collapsed ? sbWidth() : 0, collapsed ? 0 : sbWidth()); // 收起 w→0，展开 0→w
}

$('btn-new-task').onclick = () => { if (binView) setBinView(false); goHomeNewTask(); };
$('btn-sidebar').onclick = toggleSidebar;
$('wst-search').onclick = toggleSideSearch;
$('wst-view').onclick = (e) => openViewMenu(e.currentTarget);
$('wst-add').onclick = addWorkFolder;
$('wst-empty').onclick = emptyRecycleBin;
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
  if (binView) setBinView(false);
  setFace('thinking', 1600); // 进入时压眼扫视
  openAssistant();
};
$('btn-recycle').onclick = () => setBinView(!binView);

/* ---------- 左侧栏拖拽调宽：272~560，记忆落本机（jx.sbw，经 --sb-w 生效） ----------
   桌面专属：窄屏浮层恒为整屏宽（把手 CSS 禁指针 + JS 双保险，不写不落盘防污染
   桌面记忆）。拖拽中改 --sb-w，#sidebar 与 .sb-inner 一起变、列表实时回流；
   开合动画照旧走 slidePane（只动 #sidebar 内联宽，--sb-w 不参与）。 */
const SB_MIN = 272, SB_MAX = 560; // 窄到默认即止（再窄列表挤不出信息），宽过手机断点一档

function sbWidth() {
  const v = parseFloat(document.documentElement.style.getPropertyValue('--sb-w'));
  return isNaN(v) ? 272 : v;
}

function applySidebarWidth(px, save) {
  if (isNarrow()) return NaN;
  // 夹取 272~560；窗口太窄时上限收到「窗口宽-320」，给对话区留条活路
  const upper = Math.max(SB_MIN, Math.min(SB_MAX, window.innerWidth - 320));
  px = Math.round(Math.min(Math.max(px, SB_MIN), upper));
  document.documentElement.style.setProperty('--sb-w', px + 'px');
  if (save) lsSet('jx.sbw', String(px));
  rpApply(); // 右栏可瓜分的宽随左栏变（开着时重推导）
  return px;
}

function restoreSidebarWidth() {
  const px = parseFloat(lsGet('jx.sbw', ''));
  if (px >= SB_MIN) applySidebarWidth(px);
}

function setupSidebarResize() {
  const sb = $('sidebar');
  const h = document.createElement('div');
  h.className = 'sb-resize';
  h.title = '拖动调整左侧栏宽度（双击恢复默认）';
  sb.appendChild(h);
  let startX = 0, startW = SB_MIN, moved = false;
  h.addEventListener('pointerdown', (e) => {
    if (isNarrow()) return;
    e.preventDefault();
    sb._slideTok = ++slideSeq; // 作废进行中的开合动画，宽度交还给 --sb-w
    sb.classList.remove('anim');
    sb.style.width = sb.style.minWidth = '';
    startX = e.clientX;
    startW = sbWidth();
    moved = false;
    h.classList.add('dragging');
    try { h.setPointerCapture(e.pointerId); } catch (err) { /* 旧浏览器忽略 */ }
  });
  h.addEventListener('pointermove', (e) => {
    if (!h.classList.contains('dragging')) return;
    if (applySidebarWidth(startW + (e.clientX - startX)) !== startW) moved = true;
  });
  const up = () => {
    if (!h.classList.contains('dragging')) return;
    h.classList.remove('dragging');
    if (moved) applySidebarWidth(sbWidth(), true); // 松手落盘
  };
  h.addEventListener('pointerup', up);
  h.addEventListener('pointercancel', up);
  h.addEventListener('dblclick', () => {
    lsSet('jx.sbw', null);
    applySidebarWidth(272);
  });
  // 窗口缩窄时把过宽的栏夹回可用范围（只改当前宽，不动记忆）
  window.addEventListener('resize', () => { if (!isNarrow()) applySidebarWidth(sbWidth()); });
}
setupSidebarResize();
