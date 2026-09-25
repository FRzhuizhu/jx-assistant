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
const rpViews = {};          // 视图注册表：id → { title, onOpen }（计划/浏览器/文件/子对话…以后往这加）
let rpActive = null;

function rpRegister(id, def) { rpViews[id] = def; }

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
  for (const [id, def] of Object.entries(rpViews)) {
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
  rpActive = view;
  rpRenderTabs();
  panel.classList.remove('hidden');
  if (isNarrow()) {
    // 浮层模式：先提交隐藏起点（transform:translateX(100%)）再加类，过渡才成立；
    // 不设 --rp-w、不跑 slidePane（内联 width 会顶掉 CSS 的整屏宽）
    void panel.offsetWidth;
    document.body.classList.add('rp-open');
  } else {
    document.body.classList.add('rp-open');
    rpApply(); // 先按剩余宽定好 --rp-w，再从 0 滑入
    slidePane(panel, 0, rpCurrentW());
  }
  if (rpViews[view].onOpen) rpViews[view].onOpen();
}

function rpClose() {
  const panel = $('rpanel');
  if (!panel || panel.classList.contains('hidden')) return;
  document.body.classList.remove('rp-open');
  rpActive = null;
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
  try {
    const data = await api('/api/sessions/' + id);
    if (tok !== navTok.sub) return; // 已被更新的导航取代：晚到的旧响应不落地
    subState.cur = data.meta;
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
    // 输入框整体替换为该子对话的草稿（打字时已即时落盘；没有就是空，不残留上一个子对话的字）
    try {
      $('sub-input').value = localStorage.getItem(subDraftKey(id)) || '';
    } catch (e) { $('sub-input').value = ''; }
    autoGrow($('sub-input'));
    // 这个子对话正在后台执行：接上它的流，右栏就地实时跟随
    if (!subState.streaming && (data.running || subState.cur.status === 'running')) attachRun(subView);
  } catch (e) { /* 打不开就保持原状 */ } finally {
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
// 子对话视图：下拉选/新建 + 独立输入区（与主对话同一套 send/render 代码，见 subView）
$('sub-dd').onclick = () => { renderSubDDPop(); openPopover('sub-dd-pop', $('sub-dd')); };
$('sub-send').onclick = () => send(null, subView);
$('sub-stop').onclick = () => stopRun(subView);
$('sub-input').addEventListener('keydown', (e) => {
  if (e.key !== 'Enter') return;
  if (lsGet('jx.sendkey', 'enter') === 'ctrl') {
    if (e.ctrlKey || e.metaKey) { e.preventDefault(); send(null, subView); }
  } else if (!e.shiftKey && !e.ctrlKey && !e.metaKey) {
    e.preventDefault();
    send(null, subView);
  }
});
$('sub-input').addEventListener('input', (e) => { autoGrow(e.target); persistSubDraftLS(); });
rpRegister('subs', { title: '子对话', onOpen: () => { refreshDrawer(); syncComposer(subView); } }); // 右侧边栏：注册首个视图
setupRpanelResize();  // 右栏左缘拖拽（同时调宽对话区与右栏）
window.addEventListener('resize', rpApply); // 窗口缩放时右栏跟随重占剩余宽
// 嵌入式浏览器的 resize 事件可能延迟一拍：直接观察 #app 尺寸，变化即重算
if (window.ResizeObserver) new ResizeObserver(() => rpApply()).observe($('app'));
