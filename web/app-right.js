/* app-right.js —— 右侧边栏：宽度推导引擎（--rp-w 永不落盘）、视图注册表、
   子对话视图与子输入区绑定。第三个加载。 */

/* ---------- 右侧边栏：宽度引擎 + 视图注册 ---------- */
/* 宽度契约（对齐数字助理2 ui-layout 的 auto 模型）：
   1) 右栏宽度永远是推导值（可用宽 − 对话区宽度），本身从不落盘；
   2) 打开时不动左侧栏、不动对话区宽度，只吃对话区以外的剩余宽；
      窗口缩放 / 左侧栏折叠时跟随重算；
   3) 拖右栏左缘 = 同时调宽右栏与对话区，落盘的只有对话区宽度
      （jx.contentWidth，与对话区两侧拖把手共享同一记忆），
      因此关闭再打开，右栏自动回到「新的剩余宽」，等效记住了拖拽结果；
   4) 窄窗口时右栏停在最小宽，对话区列（max-width 模型）自然让步，
      不把更小的可见宽写进记忆。 */

const RP_MIN = 300;          // 右栏最小宽
const RP_CONTENT_MIN = 640;  // 对话区最小宽（与拖把手一致）
/* 视图注册表：id → { title, type, src, onOpen }（计划/文件…以后往这加）。
   type 缺省 native：容器是 index.html 里的静态 DOM（id=rp-view-<id>），行为
   代码挂在本仓 js；type=iframe：视图是一个独立面板页（web/panel/<name>，
   经 rpContainerFor 惰性创建 iframe，首开加载、此后常驻不重载）——面板页
   自成一体（自己的 HTML/JS/CSS + 直连同源 API），与主界面仅共享源。 */
const rpViews = {};
let rpActive = null;

function rpRegister(id, def) { rpViews[id] = def; }

// 取（必要时创建）视图容器：native 用静态 DOM，iframe 视图惰性建 iframe 挂进 #rp-body
function rpContainerFor(view) {
  const def = rpViews[view] || {};
  let el = $('rp-view-' + view);
  if (el || def.type !== 'iframe') return el;
  el = document.createElement('div');
  el.id = 'rp-view-' + view;
  el.className = 'rp-view rp-view-iframe';
  const f = document.createElement('iframe');
  f.src = def.src;
  f.title = def.title || view;
  el.appendChild(f);
  $('rp-body').appendChild(el);
  return el;
}

function rpAvail() { // 右栏可瓜分的总宽 = #app 宽 − 左侧栏（折叠时 offsetWidth 为 0）
  const app = $('app'), sb = $('sidebar');
  return Math.max(0, (app ? app.clientWidth : window.innerWidth) - (sb ? sb.offsetWidth : 0));
}

function rpCurrentW() {
  const v = parseFloat(document.documentElement.style.getPropertyValue('--rp-w'));
  return isNaN(v) ? 0 : v;
}

function rpApply() { // 重算并应用右栏宽度（仅打开时有效）
  const panel = $('rpanel');
  if (!panel || panel.classList.contains('hidden')) return;
  if (isNarrow()) return; // 窄屏浮层恒为整屏宽，推导冻结（断点回宽屏时由 mq 监听补算）
  const avail = rpAvail();
  const upper = Math.max(avail - RP_CONTENT_MIN, RP_MIN); // 窄窗口时上限退化到最小宽
  const w = Math.round(Math.min(Math.max(avail - contentWidth(), RP_MIN), upper));
  document.documentElement.style.setProperty('--rp-w', w + 'px');
}

function rpRenderTabs() {
  const box = $('rp-tabs');
  box.innerHTML = '';
  // 标签顺序按注册表里的 order（没写的排最后）：子对话 / 计划 / 浏览器，与注册顺序无关
  const ordered = Object.entries(rpViews).sort((a, b) => (a[1].order || 99) - (b[1].order || 99));
  for (const [id, def] of ordered) {
    const b = document.createElement('button');
    b.className = 'rp-tab' + (id === rpActive ? ' active' : '');
    b.textContent = def.title;
    b.onclick = () => { if (id !== rpActive) rpOpen(id); };
    box.appendChild(b);
  }
}

function rpOpen(view) {
  const panel = $('rpanel');
  if (!panel || !rpViews[view]) return;
  const wasOpen = !panel.classList.contains('hidden');
  rpActive = view;
  rpRenderTabs();
  // 多视图显隐切换：目标容器激活，其余全部让位（native/iframe 同一机制）
  const cont = rpContainerFor(view);
  if (cont) {
    for (const el of document.querySelectorAll('#rp-body > .rp-view')) el.classList.remove('active');
    cont.classList.add('active');
  }
  panel.classList.remove('hidden');
  if (!wasOpen) {
    // 从收起到展开：播滑入动画。浮层模式先提交隐藏起点（translateX(100%)）
    // 再加类过渡才成立；不设 --rp-w、不跑 slidePane（内联 width 顶掉整屏宽）
    if (isNarrow()) {
      void panel.offsetWidth;
      document.body.classList.add('rp-open');
    } else {
      document.body.classList.add('rp-open');
      rpApply(); // 先按剩余宽定好 --rp-w，再从 0 滑入
      slidePane(panel, 0, rpCurrentW());
    }
  } else {
    // 面板本来就开着（页签间切换）：不动面板本身，只换视图容器，不重播动画
    document.body.classList.add('rp-open');
    if (!isNarrow()) rpApply(); // 幂等重算（宽度契约不变时无视觉变化）
  }
  setRpanelBtn(true);
  if (rpViews[view].onOpen) rpViews[view].onOpen();
  if (typeof renderPanels === 'function') renderPanels(); // 浮窗归属随「右栏正显示哪个子对话」变
}

function rpClose() {
  const panel = $('rpanel');
  if (!panel || panel.classList.contains('hidden')) return;
  document.body.classList.remove('rp-open');
  rpActive = null;
  setRpanelBtn(false);
  if (typeof renderPanels === 'function') renderPanels(); // 右栏收起：它的子对话待办转回主对话区
  if (isNarrow()) {
    // 浮层模式：transform 过渡回藏侧，结束（或 460ms 兜底）再加回 hidden；
    // 代数作废机制与 slidePane 同口径，防快速开关时旧回调抢先定版
    const tok = ++slideSeq; panel._slideTok = tok;
    let done = false;
    const fin = () => {
      if (done || panel._slideTok !== tok) return;
      done = true;
      panel.classList.add('hidden');
    };
    panel.addEventListener('transitionend', (e) => {
      if (e.target === panel && e.propertyName === 'transform') fin();
    }, { once: true });
    setTimeout(fin, 460);
    return;
  }
  const w = rpCurrentW();
  slidePane(panel, w, 0, () => {
    panel.classList.add('hidden');
    document.documentElement.style.removeProperty('--rp-w'); // 宽度不记忆：重开重新占剩余宽
  });
}

function rpToggle(view) {
  if ($('rpanel').classList.contains('hidden') || rpActive !== view) rpOpen(view);
  else rpClose();
}

/* 顶栏最右的开合按钮（与左侧栏按钮同款交互）：收起后再展开回到上次看的视图 */
let rpLastView = null; // 收起前在看的视图 id

function setRpanelBtn(open) {
  const label = open ? '收起右侧栏' : '展开右侧栏';
  const btn = $('btn-rpanel');
  if (btn) btn.title = label;
  const txt = $('rp-btn-text');
  if (txt) txt.textContent = label;
}

function toggleRpanel() {
  const panel = $('rpanel');
  if (!panel || panel.classList.contains('hidden')) {
    // 展开：回到上次看的视图；从没开过就用注册表第一个（子对话）
    const first = Object.keys(rpViews)[0];
    rpOpen(rpLastView && rpViews[rpLastView] ? rpLastView : first);
  } else {
    rpLastView = rpActive;
    rpClose();
  }
}

/* 右栏左缘拖拽：拖动中右栏与对话区同时变宽窄；松手只把对话区宽度落盘 */
function setupRpanelResize() {
  const h = $('rp-resize');
  if (!h) return;
  let startX = 0, startW = 0, avail = 0, moved = false;
  h.addEventListener('pointerdown', (e) => {
    if (isNarrow()) return; // 窄屏浮层恒为整屏宽，拖拽调宽无意义（CSS 已禁指针，双保险）
    e.preventDefault();
    // 拖拽即时生效：作废进行中的开合动画，宽度交还给 --rp-w
    h.closest('#rpanel')._slideTok = ++slideSeq;
    h.closest('#rpanel').classList.remove('anim');
    h.closest('#rpanel').style.width = h.closest('#rpanel').style.minWidth = '';
    startX = e.clientX;
    startW = rpCurrentW();
    avail = rpAvail();
    moved = false;
    h.classList.add('dragging');
    try { h.setPointerCapture(e.pointerId); } catch (err) { /* 旧浏览器忽略 */ }
  });
  h.addEventListener('pointermove', (e) => {
    if (!h.classList.contains('dragging')) return;
    const upper = Math.max(avail - RP_CONTENT_MIN, RP_MIN);
    const w = Math.round(Math.min(Math.max(startW + (startX - e.clientX), RP_MIN), upper));
    if (w !== rpCurrentW()) {
      moved = true;
      document.documentElement.style.setProperty('--rp-w', w + 'px');
    }
  });
  const up = () => {
    if (!h.classList.contains('dragging')) return;
    h.classList.remove('dragging');
    if (!moved) return; // 窄窗口宽度被钉死时不算一次调整，别覆盖记忆
    // 对话区新宽 = 可用宽 − 右栏拖定宽。直接写 var + 落盘：
    // 不走 setContentWidth——它按当前可视宽夹取，右栏打开时会把值越夹越小
    const cw = Math.max(avail - rpCurrentW(), RP_CONTENT_MIN);
    document.documentElement.style.setProperty('--content-w', cw + 'px');
    lsSet('jx.contentWidth', String(cw));
  };
  h.addEventListener('pointerup', up);
  h.addEventListener('pointercancel', up);
}

let subKids = []; // 当前主对话的子对话清单（下拉面板数据）

async function refreshDrawer() {
  const fid = drawerFamilyId();
  if (!fid) { subKids = []; subRenderHeader(); return; }
  try {
    const d = await api('/api/sessions?parent=' + encodeURIComponent(fid));
    subKids = (d.sessions || [])
      .slice()
      .sort((a, b) => String(b.updatedAt || '').localeCompare(String(a.updatedAt || '')));
  } catch (e) { return; /* 静默：刷新失败不打断主流程 */ }
  subRenderHeader();
  // 下拉正开着时就地翻新列表
  const pop = $('sub-dd-pop');
  if (pop && !pop.classList.contains('hidden')) renderSubDDPop();
}

// 顶栏下拉按钮的当前态：子对话标题 + 运行中徽标
function subRenderHeader() {
  const name = $('sub-dd-name'), st = $('sub-dd-status');
  if (!name) return;
  const cur = subKids.find((s) => subState.cur && s.id === subState.cur.id);
  name.textContent = (cur && cur.title) || (subState.cur && subState.cur.title) || '选择子对话';
  const running = cur ? cur.status === 'running' : (subState.cur && subState.cur.status === 'running');
  st.textContent = running ? '⟳ 运行中' : '';
}

function renderSubDDPop() {
  const pop = $('sub-dd-pop');
  pop.innerHTML = '';
  if (!subKids.length) {
    const e = document.createElement('div');
    e.className = 'sub-dd-empty';
    e.textContent = '还没有子对话；AI 派发或点下方新建都会出现在这里';
    pop.appendChild(e);
  }
  for (const s of subKids) {
    const item = document.createElement('div');
    item.className = 'drawer-item' + (subState.cur && subState.cur.id === s.id ? ' active' : '');
    item.innerHTML =
      `<span class="d-status ${s.status === 'running' ? 'run' : ''}">${s.status === 'running' ? '⟳' : '·'}</span>` +
      `<span class="d-title">${esc(s.title || '(未命名)')}</span>` +
      (s.mode === 'aux' || s.origin === 'human' ? '<span class="d-src human">人工</span>' : '<span class="d-src">AI</span>') +
      `<span class="d-time">${esc(relTime(s.updatedAt))}</span>`;
    item.onclick = () => { openSubSession(s.id); closePopovers(); };
    pop.appendChild(item);
  }
  const neu = document.createElement('button');
  neu.className = 'drawer-item sub-dd-new';
  neu.innerHTML = '<span class="d-status">＋</span><span class="d-title">新建子对话</span>';
  neu.onclick = () => { closePopovers(); newAuxChat(); };
  pop.appendChild(neu);
}

// 子对话只在右栏显示，绝不切换主对话区
async function openSubSession(id) {
  // 右栏换子对话：同样先放开本地这条流（后端执行照旧，回来自动接上）
  if (subState.streaming && (!subState.cur || subState.cur.id !== id)) detachStream(subView);
  const tok = ++navTok.sub;
  navBusy.sub = true;
  // 乐观切换（与主对话 openSession 同款）：壳先到位、消息区转圈；失败留在目标子对话
  // 给原因与重试，不再静默保持旧子对话（远程访问整包慢时尤其明显）
  const brief = (state.sessions || []).find((s) => s.id === id);
  if (brief && (!subState.cur || subState.cur.id !== id)) optimisticSwitch(subView, brief);
  try {
    const data = await api('/api/sessions/' + id);
    if (tok !== navTok.sub) return; // 已被更新的导航取代：晚到的旧响应不落地
    subState.cur = data.meta;
    subState._loading = false; // 数据落地：退出加载态（必须在 renderMessages 之前）
    subState.msgs = data.messages || [];
    subState.pendingSteer = (data.pending && data.pending.steer) || [];
    subState.pendingQueue = (data.pending && data.pending.queue) || [];
    subState.checkpoints = data.checkpoints || [];
    subState.deliveries = data.deliveries || [];
    if (data.error) showRunError(subView, data.error); else hideRunError(subView);
    subState.streamText = '';
    subState.streamThinking = '';
    subRenderHeader();
    renderMessages(subState.msgs, subView, { jump: true });
    renderQueueDock(subView);
    syncComposer(subView); // 右栏那套输入区控件跟着这个子对话刷新（权限/模型/模式/上下文圆环）
    syncComposerPad(subView); // 悬浮输入区：重算子对话消息列的垫高与按钮锚点
    renderPanels(); // 待决定的浮窗：这个子对话自己的卡落在右栏输入区上方，主对话区那份随之撤掉
    // 输入框整体替换为该子对话的草稿（打字时已即时落盘；没有草稿就是空，不残留上一个子对话的字）
    try {
      $('sub-input').value = localStorage.getItem(subDraftKey(id)) || '';
    } catch (e) { $('sub-input').value = ''; }
    autoGrow($('sub-input'));
    // 这个子对话正在后台执行：接上它的流，右栏就地实时跟随
    if (!subState.streaming && (data.running || subState.cur.status === 'running')) attachRun(subView);
  } catch (e) {
    // 加载失败：留在目标子对话的壳里给原因与重试（openSession 同款语义）
    if (tok === navTok.sub) showChatLoadError(subView, e.message, () => openSubSession(id));
  } finally {
    if (tok === navTok.sub) navBusy.sub = false;
  }
}

async function newAuxChat() {
  const fid = drawerFamilyId();
  if (!fid) return;
  try {
    // 子对话＝当前对话的分支：后端照抄父会话的模式/工作区/权限/能力并完整复制历史，
    // 系统提示词与上下文与主对话一致（继承段在对话里折叠成一条「来自主对话」）
    const s = await api('/api/sessions', {
      method: 'POST',
      body: JSON.stringify({ parent: fid, title: '子对话', inheritContext: true }),
    });
    await openSubSession(s.id);
    refreshDrawer();
    loadSessions();
  } catch (e) {
    alert('新建子对话失败：' + e.message);
  }
}
$('rp-close').onclick = rpClose;
$('btn-rpanel').onclick = toggleRpanel; // 顶栏最右：右栏整体开合（收起后再展开回到上次看的视图）
// 子对话视图：下拉选/新建 + 独立输入区（与主对话同一套 send/render 代码，见 subView）
$('sub-dd').onclick = () => { renderSubDDPop(); openPopover('sub-dd-pop', $('sub-dd')); };
$('sub-send').onclick = () => send(null, subView);
$('sub-stop').onclick = () => stopRun(subView);
$('sub-input').addEventListener('keydown', (e) => composerEnterKeydown(e, () => send(null, subView)));
$('sub-input').addEventListener('input', (e) => { autoGrow(e.target); persistSubDraftLS(); });
rpRegister('subs', { title: '子对话', order: 1, onOpen: () => { refreshDrawer(); syncComposer(subView); } }); // 右侧边栏：注册首个视图

/* ---------- 浏览器视图（桌面=原生 WebView2 面板 / 网页=占位提示） ---------- */
/* 工具栏与状态条由本界面渲染；网页区域是 #bpw-stage 占位 div，桌面模式下
 * 原生 WebView2 控件精确覆盖该矩形（宿主 browser_panel_windows.go），网页
 * 本身不进 HTML。矩形（CSS px × devicePixelRatio = 物理像素）防抖后上报
 * /api/browser/panel-rect，窗口缩放/拖分割条/侧栏开合都触发重算。 */
function bpwDesktop() { return document.documentElement.classList.contains('desktop-shell'); }

function bpwActive() {
  const panel = $('rpanel');
  return !!panel && !panel.classList.contains('hidden') && rpActive === 'browser';
}

let bpwRectTimer = null, bpwLastKey = '';
function bpwReport(immediate) {
  clearTimeout(bpwRectTimer);
  const run = () => {
    const stage = $('bpw-stage');
    if (!stage) return;
    if (!bpwDesktop() || !bpwActive()) {
      if (bpwLastKey !== 'hidden') { // 只在从可见转隐藏时通知一次
        bpwLastKey = 'hidden';
        api('/api/browser/panel-rect', { method: 'POST', body: JSON.stringify({ visible: false }) }).catch(() => {});
      }
      return;
    }
    const r = stage.getBoundingClientRect();
    if (r.width < 8 || r.height < 8) return; // 布局未就绪（开合动画中），别用脏矩形
    const payload = { x: r.x, y: r.y, w: r.width, h: r.height, dpr: window.devicePixelRatio || 1, visible: true };
    const key = JSON.stringify(payload);
    if (key === bpwLastKey) return; // 尺寸没变不上报
    bpwLastKey = key;
    api('/api/browser/panel-rect', { method: 'POST', body: JSON.stringify(payload) }).catch(() => {});
  };
  if (immediate) run(); else bpwRectTimer = setTimeout(run, 80);
}

// 工具栏（逻辑自旧 panel/browser.js 平移）：后退/前进/刷新/地址栏/外部打开
function bpwNav(body) {
  api('/api/browser/navigate', { method: 'POST', body: JSON.stringify(body) })
    .then((st) => { bpwOnNav(st); })
    .catch((e) => { $('bpw-title').textContent = '导航失败：' + e.message; });
}
function bpwOnNav(d) {
  if (d && d.url) $('bpw-url').value = d.url;
  $('bpw-title').textContent = (d && d.title) || '未打开页面';
  if (d && d.viewportW) $('bpw-vp').textContent = d.viewportW + '×' + d.viewportH;
  else $('bpw-vp').textContent = '';
}
function bpwSyncState() {
  api('/api/browser/state').then(bpwOnNav).catch(() => {});
}
$('bpw-back').onclick = () => bpwNav({ action: 'back' });
$('bpw-fwd').onclick = () => bpwNav({ action: 'forward' });
$('bpw-reload').onclick = () => bpwNav({ action: 'reload' });
$('bpw-url').addEventListener('keydown', (e) => {
  if (e.key !== 'Enter') return;
  e.preventDefault();
  let u = $('bpw-url').value.trim();
  if (!u) return;
  if (!/^https?:\/\//i.test(u)) u = 'https://' + u; // 裸域名补协议
  bpwNav({ url: u });
});
$('bpw-external').onclick = () => {
  const u = $('bpw-url').value.trim();
  if (u) window.open(u, '_blank');
};

// AI 的 browser_viewport 请求（browser_resize 事件，X/Y 复用为宽高）：
// 面板宽度是推导值（可用宽 − 对话区宽），让对话区让位即等效调宽面板；
// 不落盘（临时视口，不动用户的布局记忆），高度跟随右栏无法独立设定
function bpwApplyViewport(w) {
  if (!w || isNarrow() || !bpwActive()) return;
  const avail = rpAvail();
  const cw = Math.max(avail - Math.round(w), RP_CONTENT_MIN);
  document.documentElement.style.setProperty('--content-w', cw + 'px');
  rpApply();
  bpwReport(true);
}

rpRegister('browser', {
  title: '浏览器',
  order: 3,
  onOpen: () => {
    bpwSyncState();
    bpwReport(true); // 打开即上报矩形：宿主据此显示并定位原生控件
  },
});
// 矩形变化监听：舞台尺寸（开合动画、拖分割条、窗口缩放都会传导到这里）
if (window.ResizeObserver) {
  new ResizeObserver(() => bpwReport()).observe($('bpw-stage'));
  new ResizeObserver(() => bpwReport()).observe($('app'));
}
window.addEventListener('resize', () => bpwReport());
if (window.visualViewport) window.visualViewport.addEventListener('resize', () => bpwReport()); // 主界面缩放（Ctrl+滚轮）时 dpr 变化
setupRpanelResize();  // 右栏左缘拖拽（同时调宽对话区与右栏）
window.addEventListener('resize', rpApply); // 窗口缩放时右栏跟随重占剩余宽
// 嵌入式浏览器的 resize 事件可能延迟一拍：直接观察 #app 尺寸，变化即重算
if (window.ResizeObserver) new ResizeObserver(() => rpApply()).observe($('app'));
