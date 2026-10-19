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
/* 视图注册表：id → { title, onOpen, saveSession, restoreSession }（计划/
   浏览器…以后往这加）。容器一律是 index.html 里的静态 DOM（id=rp-view-<id>），
   行为代码挂在本仓 js（旧「文件」查看页用过的 iframe 型视图已随该功能移除）。
   saveSession()/restoreSession(saved, sid)：右栏跟会话走的统一挂点——每个视图把
   「属于哪个会话的选择」（子对话选中、计划选中、Git 仓库…）
   交给这里，切换对话时由 rpSnapshot / rpRestoreSession 统一快照与恢复，
   视图自己不再各自监听会话切换（browser 这类全局态视图不挂，行为不变）。
   restoreSession 的第二参就是目标会话 id（回首页为空串）——视图直接用它，
   不要去读 state.cur：挂点被调的时机并不保证 cur 已就位（懒创建等路径）。 */
const rpViews = {};
let rpActive = null;

/* ---------- 右栏的会话作用域状态：每个对话独立记住自己的右栏 ----------
   右栏跟对话走：开合、正在看的视图、各视图内的选择都按会话保存，切换对话时
   整体切到目标对话自己的状态；跨会话共通的只有对话区宽度（jx.contentWidth，
   见上方宽度契约）。会话被删除时随 removeSessionLocalData 一并清掉。
   状态只存内存：刷新页面回到默认（收起）——右栏是「对话进行中的工作台摆设」，
   保存的子对话/计划可能早已被删，跨重启恢复只会带一批过期状态回来。 */
const rpSess = {}; // 会话id -> { open: bool, view: '', views: { <视图id>: saveSession() 的返回值 } }

function rpSessionOf(sid) {
  return rpSess[sid] || (rpSess[sid] = { open: false, view: '', views: {} });
}

// 离开会话前快照右栏当前状态（openSession 切到别的对话 / goHomeNewTask 回首页时调；
// 会话被删除不用调——它的状态直接随 removeSessionLocalData 作废）
function rpSnapshot(sid) {
  if (!sid) return;
  const panel = $('rpanel');
  const st = rpSessionOf(sid);
  st.open = !!(panel && !panel.classList.contains('hidden'));
  st.view = rpActive || rpLastView || ''; // 开着记当前视图；收着记上次看的（rpClose 已统一记录）
  st.views = {};
  for (const [id, def] of Object.entries(rpViews)) {
    if (typeof def.saveSession !== 'function') continue;
    try { st.views[id] = def.saveSession(); } catch (e) { /* 单视图快照失败不拖累整体 */ }
  }
}

// 换到目标会话：所有视图先就位该会话的状态（含未激活的——之后点开页签看到的
// 也是本会话内容），再按该会话的记忆开合面板。没有记忆的对话＝默认态（收起 +
// 各视图空），所以哪怕视图没挂 save/restore 钩子，切换本身也会把旧对话的
// 子对话/计划/文件从右栏清出去。maybeAutoOpenPlan（本对话来了待审批计划）
// 在这之后跑，可以覆盖恢复结果。
function rpRestoreSession(sid) {
  // sid 为空 = 回首页态：不建条目，所有视图清空、右栏收起（Git 等组件的会话跟随一并归位）
  const st = sid ? rpSessionOf(sid) : { open: false, view: '', views: {} };
  for (const [id, def] of Object.entries(rpViews)) {
    if (typeof def.restoreSession !== 'function') continue;
    try { def.restoreSession(st.views[id], sid); } catch (e) { /* 单视图失败不拖累整体 */ }
  }
  if (st.view) rpLastView = st.view; // 收着展开时回到本会话上次看的视图
  if (st.open && st.view && rpViews[st.view]) rpOpen(st.view);
  else rpClose();
}

function rpRegister(id, def) {
  rpViews[id] = def;
  // 视图晚于会话切换注册（组件热加载 / 组件加载晚于首个 openSession）：
  // 当前会话的右栏状态已初始化过（rpSess 有条目），这里补恢复这个视图，
  // 否则它带着初始（或上一次）的会话选择直到下次切换
  const sid = state.cur && state.cur.id;
  if (sid && rpSess[sid] && typeof def.restoreSession === 'function') {
    try { def.restoreSession(rpSess[sid].views[id], sid); } catch (e) { /* 单视图失败不拖累整体 */ }
  }
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
    b.dataset.rpId = id; // 供 app-chat.js 识别「用户点开的是哪个视图」（点开「计划」＝已在处理）
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
  // 多视图显隐切换：目标容器激活，其余全部让位
  const cont = $('rp-view-' + view);
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
  rpLastView = rpActive || rpLastView; // 收起前记下正看的视图：✕ 按钮与 toggle 都走这里，展开时回到它
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

/* 顶栏最右的开合按钮（与左侧栏按钮同款交互）：收起后再展开回到上次看的视图
   （rpClose 里已统一记录，这里只需展开） */
let rpLastView = null; // 收起前在看的视图 id

function setRpanelBtn(open) {
  const label = open ? '收起右侧栏' : '展开右侧栏';
  // ✕ 的提示语跟随当前视图：浏览器页签下它是「真关闭页面」，别的视图只是收起
  const xbtn = $('rp-close');
  if (xbtn) xbtn.title = rpActive === 'browser' ? '关闭浏览器页面并收起侧栏' : '收起侧栏';
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
    // 浏览器视图：拖动期间暂收原生面板（鼠标滑进舞台区域后指针事件会归它，
    // 拖拽会断；拖完 bpwDragPause(false) 按新宽度补报回来）
    bpwDragPause(true);
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
    bpwDragPause(false); // 拖完先让原生面板按新宽度回来，再做下面的落盘
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
      .filter((s) => !s.archived) // 移入回收站的不在下拉出现（回收站里恢复后回来）
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
    e.textContent = '还没有子对话；在下方输入框发消息会自动新建，AI 派发的也出现在这里';
    pop.appendChild(e);
  }
  for (const s of subKids) {
    const item = document.createElement('div');
    item.className = 'drawer-item' + (subState.cur && subState.cur.id === s.id ? ' active' : '');
    // 状态格四态：运行中圆环 > 完成未查看绿点 > 标为未读红点 > 平时的 '·'
    // （完成绿点 = dotDone，打开该子对话即清；父对话侧栏行的绿点同一来源，见
    // app-chat.js 的 done/sub_end 分支）
    const stCls = s.status === 'running' ? 'run' : (dotDone[s.id] ? 'done' : (s.unread ? 'unread' : ''));
    const stTxt = s.status === 'running' || dotDone[s.id] || s.unread ? '' : '·'; // 圆环/绿点/红点留空：图形由 CSS 画，转字符会晃
    item.innerHTML =
      `<span class="d-status ${stCls}">${stTxt}</span>` +
      `<span class="d-title">${esc(s.title || '(未命名)')}</span>` +
      (s.mode === 'aux' || s.origin === 'human' ? '<span class="d-src human">人工</span>' : '<span class="d-src">AI</span>') +
      `<span class="d-time">${esc(relTime(s.updatedAt))}</span>` +
      `<span class="row-acts"><button class="ra-more" title="更多"><svg viewBox="0 0 24 24" fill="currentColor"><circle cx="5" cy="12" r="1.6"/><circle cx="12" cy="12" r="1.6"/><circle cx="19" cy="12" r="1.6"/></svg></button></span>`;
    item.onclick = (e) => {
      if (e.target.closest('.row-acts')) return;
      openSubSession(s.id); closePopovers();
    };
    item.querySelector('.ra-more').onclick = (e) => {
      e.stopPropagation();
      openSubItemMenu(s, item.getBoundingClientRect());
    };
    item.oncontextmenu = (e) => { e.preventDefault(); openSubItemMenu(s, item.getBoundingClientRect()); };
    pop.appendChild(item);
  }
  const neu = document.createElement('button');
  neu.className = 'drawer-item sub-dd-new';
  neu.innerHTML = '<span class="d-status">＋</span><span class="d-title">新建子对话</span>';
  neu.onclick = () => { closePopovers(); newAuxChat(); };
  pop.appendChild(neu);
}

// 子对话下拉项的「更多/右键」菜单：与主对话会话菜单（openSessionMenu）同一套
// 动作与语义——重命名/标为未读/分支/移入回收站；差别只在落地刷新面（下拉清单、
// 右栏当前子对话）与「正看着的子对话被回收时右栏回空态」。分支沿用 forkFromAnswer
// 的口径（fork 保留 parent，新会话还是这个主对话的子对话，就地打开）。
function openSubItemMenu(s, rect) {
  openCtxMenu([
    { label: '重命名', fn: async () => {
      const t = prompt('重命名子对话', s.title || '');
      if (t === null || !t.trim()) return;
      await api('/api/sessions/' + s.id, { method: 'PATCH', body: JSON.stringify({ title: t.trim() }) });
      s.title = t.trim(); // 清单数据就地改，重开下拉即见；顶部标题跟着换
      if (subState.cur && subState.cur.id === s.id) subState.cur.title = s.title;
      subRenderHeader();
      loadSessions();
    } },
    { label: s.unread ? '取消未读' : '标为未读', fn: async () => {
      await api('/api/sessions/' + s.id, { method: 'PATCH', body: JSON.stringify({ unread: !s.unread }) });
      s.unread = !s.unread; // 就地翻转，重开下拉即见红点
    } },
    { label: '分支对话', fn: async () => {
      try {
        const fork = await api('/api/sessions/' + s.id + '/fork', { method: 'POST' });
        await loadSessions();
        refreshDrawer();
        openSubSession(fork.id);
      } catch (e) { alert('分支失败：' + e.message); }
    } },
    { label: '移入回收站', danger: true, fn: async () => {
      await api('/api/sessions/' + s.id, { method: 'PATCH', body: JSON.stringify({ archived: true }) });
      if (subState.cur && subState.cur.id === s.id) subClearCurrent(); // 回收的正是右栏看着的：回空态（后台流照旧跑完）
      refreshDrawer();
      loadSessions();
    } },
  ], rect.right - 8, rect.bottom + 4);
}

// 子对话输入框的 placeholder 随「是否已进入子对话」切换（空态引导直接发消息、
// 进入后回到常态）。发送执行中时 runHintOn 已把当下 placeholder 记为提示基线
// （idlePh），这里同步换基线，免得流式结束 runHintOff 把旧文案恢复回来。
function setSubPlaceholder(text) {
  const ph = $('sub-input');
  ph.placeholder = text;
  if (ph.dataset.runPh) ph.dataset.idlePh = text;
}

// 子对话视图回空态：当前主对话没有选中的子对话（没建过，或保存的那个已被删除）。
// 换主对话时 rpRestoreSession 走这里把上一个对话的子对话彻底清出右栏。
function subClearCurrent() {
  if (subState.streaming) detachStream(subView); // 只放开本地流，子对话在后台照旧跑完
  flushSubDraft(); // 清空前把防抖窗口内的输入落进旧子对话草稿（cur 清掉后 persist 就找不着上下文了）
  navTok.sub++;        // 作废在途的子对话加载：晚到的旧响应不得再落地
  navBusy.sub = false;
  subState.cur = null;
  subState.msgs = [];
  subState.pendingSteer = []; subState.pendingQueue = [];
  subState.checkpoints = []; subState.deliveries = [];
  subState.streamText = ''; subState.streamThinking = '';
  subState.queuePaused = false; subState.queueHeld = false;
  subState._loading = false; subState._pfxSid = null;
  hideRunError(subView);
  subRenderHeader();
  renderQueueDock(subView);
  syncComposer(subView);     // 右栏输入区控件回空态（无会话的中性默认）
  syncComposerPad(subView);
  renderPanels();            // 子对话的待办卡随之回到主对话区/角标堆
  subView.col.innerHTML = '<div class="empty"><h2>子对话</h2><p>在下方输入框直接发消息，会自动新建子对话；顶部下拉可切换已有的，数字助理派发的也出现在这里。</p></div>';
  $('sub-input').value = ''; // 没有选中的子对话：不留上一个子对话的草稿
  setSubPlaceholder('发消息自动新建子对话…'); // 空态引导：像新对话页一样直接打字就能开聊
  autoGrow($('sub-input'));
}

// 子对话只在右栏显示，绝不切换主对话区
async function openSubSession(id) {
  flushSubDraft(); // 切换前把防抖窗口内的输入落进旧子对话的草稿（下面会整体替换输入框）
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
    subState._loading = false; // 数据落地：退出加载态（必须在 renderMessages 之前）
    // 迟到的旧快照（见 app-chat.js 的 snapshotStale，同会话才可比）：右栏已被
    // 流式帧推进到更新，整包不应用，只收掉加载态并按现状重绘
    if (snapshotStale(subView, id, data.messages)) {
      renderMessages(subState.msgs, subView);
      return;
    }
    subState.cur = data.meta;
    subState.msgs = data.messages || [];
    // 打开即视为已读（与主对话 openSession 同款）：清红点与完成绿点，下拉清单数据同步
    delete dotDone[id];
    if (subState.cur.unread) {
      subState.cur.unread = false;
      const kid = subKids.find((k) => k.id === id);
      if (kid) kid.unread = false;
      api('/api/sessions/' + id, { method: 'PATCH', body: JSON.stringify({ unread: false }) }).catch(() => {});
    }
    subState.pendingSteer = (data.pending && data.pending.steer) || [];
    subState.pendingQueue = (data.pending && data.pending.queue) || [];
    subState.queuePaused = !!(data.pending && data.pending.paused);
    // 空闲但队列有保留（上轮停止/出错/暂停后留下的）：横幅给「继续」入口
    subState.queueHeld = subState.pendingQueue.length > 0 && !subState.queuePaused &&
      !(data.running || (subState.cur && subState.cur.status === 'running'));
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
    flushSubDraft(); // 覆盖前落盘：乐观切换后到数据落地之间输入的字不能被顶回旧草稿
    try {
      $('sub-input').value = localStorage.getItem(subDraftKey(id)) || '';
    } catch (e) { $('sub-input').value = ''; }
    setSubPlaceholder('向子对话发消息…'); // 已进入子对话：空态的「自动新建」引导退场
    autoGrow($('sub-input'));
    restoreQuoteLS(subView); // 这个子对话挂着的引用胶囊也一并回来
    // 这个子对话正在后台执行：接上它的流，右栏就地实时跟随
    if (!subState.streaming && (data.running || subState.cur.status === 'running')) attachRun(subView);
  } catch (e) {
    if (tok !== navTok.sub) return;
    // 会话已被删除（换对话时恢复的子对话可能早没了）：回空态，别留一个永远失败的重试
    if (e && e.status === 404) { subClearCurrent(); return; }
    // 加载失败：留在目标子对话的壳里给原因与重试（openSession 同款语义）
    showChatLoadError(subView, e.message, () => {
      // 重试先手动补加载反馈：subState.cur 已是目标子对话，乐观分支会跳过
      subView.state._loading = true;
      showChatLoading(subView);
      openSubSession(id);
    });
  } finally {
    if (tok === navTok.sub) navBusy.sub = false;
  }
}

// 新建子对话并就地打开：下拉「新建」与右栏直接发消息的懒创建路径（prepareSend）
// 共用。返回创建的会话；无父可挂或失败返回 null（已就地提示，输入内容保留）。
async function newAuxChat() {
  const fid = drawerFamilyId();
  if (!fid) {
    // 首页态：子对话必须挂靠某个主对话，先提示用户打开对话再发
    subView.hint.textContent = '子对话挂在当前对话名下；先打开一个对话';
    setTimeout(() => { subView.hint.textContent = ''; }, 2500);
    return null;
  }
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
    return s;
  } catch (e) {
    alert('新建子对话失败：' + e.message);
    return null;
  }
}
$('rp-close').onclick = () => {
  // 浏览器视图的 ✕ 是「真关闭」：先关掉页面（后端销毁实例，并向本会话投
  // 【用户关闭浏览器】注记——AI 不被触发对话，下次醒来自然看到），再收起
  // 面板。其他视图照旧只收起；顶栏 btn-rpanel 任何视图都只是收起（浏览器
  // 后台保留），两条路各管各的。
  if (rpActive === 'browser') {
    api('/api/browser/close', { method: 'POST', body: JSON.stringify({ session: (state.cur && state.cur.id) || '' }) }).catch(() => {});
    $('bpw-url').value = '';
    $('bpw-title').textContent = '未打开页面';
    $('bpw-vp').textContent = '';
  }
  rpClose();
};
$('btn-rpanel').onclick = toggleRpanel; // 顶栏最右：右栏整体开合（收起后再展开回到上次看的视图）
// 子对话视图：下拉选/新建 + 独立输入区（与主对话同一套 send/render 代码，见 subView）
$('sub-dd').onclick = () => { renderSubDDPop(); openPopover('sub-dd-pop', $('sub-dd')); };
$('sub-send').onclick = () => send(null, subView);
$('sub-stop').onclick = () => stopRun(subView);
$('sub-input').addEventListener('keydown', (e) => composerEnterKeydown(e, () => send(null, subView)));
// 与主输入同款（见 app-chat.js 顶部绑定处注释）：量高合并 rAF、草稿防抖 300ms，
// 切换/清空子对话前由 flushSubDraft 兜底
let subGrowRaf = 0;
$('sub-input').addEventListener('input', () => {
  if (!subGrowRaf) subGrowRaf = requestAnimationFrame(() => { subGrowRaf = 0; autoGrow($('sub-input')); });
  scheduleSubDraft();
});
rpRegister('subs', { // 右侧边栏：注册首个视图
  title: '子对话', order: 1,
  onOpen: () => { refreshDrawer(); syncComposer(subView); },
  // 会话态：这个主对话正看着哪个子对话（'' = 没选中）
  saveSession: () => (subState.cur && subState.cur.id) || '',
  restoreSession: (saved) => {
    refreshDrawer(); // 子对话清单跟着主对话换（乐观阶段就刷，下拉里别闪上一个对话的清单）
    const subId = saved || '';
    if (subId) openSubSession(subId);
    else subClearCurrent();
  },
});

/* ---------- 浏览器视图（桌面=原生 WebView2 面板 / Electron=<webview> / 网页=占位提示） ---------- */
/* 工具栏与状态条由本界面渲染；旧桌面壳的网页区域是 #bpw-stage 占位 div，原生
 * WebView2 控件精确覆盖该矩形（宿主 browser_panel_windows.go），网页本身不进
 * HTML。矩形以 CSS px 原值上报 /api/browser/panel-rect（宿主进程的子窗口坐标
 * 与 CSS px 同尺度，乘 devicePixelRatio 会把面板推出窗口——显示缩放 ≠100% 的
 * 机器上就是「永远白屏」的根源，见 handleBrowserPanelRect），窗口缩放/拖分
 * 割条/侧栏开合都触发重算。Electron 壳（electron-shell 类）网页区是页面内
 * <webview> 元素：参与 CSS 布局与 z-order，整套矩形上报在这里整体短路。 */
function bpwDesktop() { return document.documentElement.classList.contains('desktop-shell'); }
function bpwElectron() { return document.documentElement.classList.contains('electron-shell'); }

// 原生控件左缘让位：舞台矩形上报时左边内缩 6px，把 .rp-resize（跨右栏左缘
// -5~+5px 的拖拽把手）完整留给 HTML——否则把手右半与悬停竖线被原生子窗口盖住，
// 指针落不进页面，浏览器视图里宽度就拖不动（其他视图没有原生窗口不受影响）。
// 视觉上是网页与右栏左缘之间 6px 的背景条，当作面板内边框看。
const BPW_EDGE = 6;
// 拖分割条期间：原生面板暂收屏外（同右栏开合动画语义），松手后按新宽度回来。
// 鼠标拖动中一旦滑进舞台区域，后续指针事件就归原生子窗口，拖拽会断——干脆
// 拖动全程收起，bpwReport 也暂停（拖完 force 补报一次）。
let bpwDragHide = false;
function bpwDragPause(on) {
  if (on) {
    if (bpwElectron() || !bpwDesktop() || !bpwActive() || bpwDragHide) return;
    bpwDragHide = true;
    api('/api/browser/panel-rect', { method: 'POST', body: JSON.stringify({ visible: false, desktop: true }) }).catch(() => {});
  } else {
    if (!bpwDragHide) return;
    bpwDragHide = false;
    bpwReport(true, true);
  }
}

function bpwActive() {
  const panel = $('rpanel');
  return !!panel && !panel.classList.contains('hidden') && rpActive === 'browser';
}

let bpwRectTimer = null, bpwLastKey = '';
// force=true 跳过「尺寸没变不上报」去重：导航事件/链接打开的补报是「确保
// 可见」信号——后端面板可能被另一页面实例或异常时序藏在暗处，矩形没变也
// 必须重发一次让它显示（自愈路径不吃去重）。
function bpwReport(immediate, force) {
  if (bpwElectron()) return; // Electron 壳：webview 是 DOM 元素，无矩形同步
  if (!bpwDesktop()) return; // 网页形态没有原生面板，一个字节都不发：
  // 手机/浏览器里开的同一 UI 曾用一次隐藏上报把桌面端的面板顶掉
  // （多写者之祸，后端 handleBrowserPanelRect 现只认 desktop 标记）
  if (bpwDragHide) return; // 拖分割条中：面板已暂收，拖完统一补报
  clearTimeout(bpwRectTimer);
  // attempt：开合动画期间舞台宽 <8 是常态（onOpen 的立即上报跑在动画中段），
  // 旧版只早退一次、之后全靠 ResizeObserver——RO 偶发不触发时原生面板就被
  // 永久留在隐藏态（地址栏有内容、舞台空白）。改为短周期重试兜底。
  const run = (attempt) => {
    const stage = $('bpw-stage');
    if (!stage) return;
    if (!bpwActive()) {
      if (bpwLastKey !== 'hidden') { // 只在从可见转隐藏时通知一次
        bpwLastKey = 'hidden';
        api('/api/browser/panel-rect', { method: 'POST', body: JSON.stringify({ visible: false, desktop: true }) }).catch(() => {});
      }
      return;
    }
    const r = stage.getBoundingClientRect();
    if (r.width < 8 || r.height < 8) { // 布局未就绪（开合动画中），别用脏矩形
      if (attempt < 10) bpwRectTimer = setTimeout(() => run(attempt + 1), 120);
      return;
    }
    // 左缘内缩 BPW_EDGE（见常量注释）：把手完整留给 HTML
    const payload = { x: r.x + BPW_EDGE, y: r.y, w: r.width - BPW_EDGE, h: r.height,
      dpr: window.devicePixelRatio || 1, visible: true, desktop: true,
      iw: window.innerWidth, ih: window.innerHeight, oz: (window.visualViewport && Math.round(window.visualViewport.scale * 100)) || 0 }; // 临时诊断字段
    const key = JSON.stringify(payload);
    if (key === bpwLastKey && !force) return; // 尺寸没变不上报（强制补报除外）
    bpwLastKey = key;
    api('/api/browser/panel-rect', { method: 'POST', body: JSON.stringify(payload) }).catch(() => {});
  };
  if (immediate) run(0); else bpwRectTimer = setTimeout(() => run(0), 80);
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
// 桌面壳里 window.open 只会弹 WebView2 扄件的窗口（不是用户浏览器）；
// 「在系统浏览器打开」桌面形态走后端 /api/open-url（ShellExecute 真默认
// 浏览器），网页形态保持新标签页
$('bpw-external').onclick = () => {
  const u = $('bpw-url').value.trim();
  if (!u) return;
  if (bpwDesktop()) {
    api('/api/open-url', { method: 'POST', body: JSON.stringify({ url: u }) })
      .catch((e) => alert('打开失败：' + e.message));
  } else {
    window.open(u, '_blank');
  }
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

/* 右栏「文件」查看页已整体移除（当年是原生浏览器面板修不好时的替代品，只
 * 支持文本且观感差）：文件一律交系统默认应用打开（/api/sessions/{id}/open-path
 * 的 ShellExecute），仅 html 网页文件与 http(s) 链接仍进右栏「浏览器」。 */
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
