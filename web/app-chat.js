/* app-chat.js —— 对话区引擎：消息渲染（折叠基元/回合分组/kind 分发注册表
   ROW_ITEM_BUILDERS，契约见《消息类型规范.md》）、发送与流式、SSE 事件流、
   审批/提问/待办卡片、SSH 远程、输入区与弹出面板、宽度拖把手。第四个加载。 */

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
  syncComposerPad(mainView); // 首页/会话切换后重算悬浮垫高与按钮锚点
}

// 把输入卡挪回底部容器（msg-col 重建 innerHTML 前必须先挪出，否则会被一起销毁）
function detachComposerToWrap() {
  const c = document.getElementById('composer-area');
  if (c) $('composer-wrap').appendChild(c);
}

function resetEmpty() {
  detachComposerToWrap();
  state._pfxSid = null; // 回到首页：消息区的增量渲染缓存失效
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
  rpClose();
  placeComposer();
  // 输入框整体替换为首页草稿（跨重启保留；没有就是空，绝不带着别的对话的字）
  $('input').value = homeDraft.text || '';
  autoGrow();
  syncComposer();
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
  narrowClosePanels(); // 窄屏：从左栏浮层发起，选中目标即收起浮层
  if (marketOpen()) closeMarket(); // 新工作任务也是一张「对话页」：回首页即关掉市场页
  detachStream(mainView); // 回首页只放开本地流，正在跑的那一轮继续在后台跑完
  navTok.main++;          // 作废在途的会话加载（回首页是同步完成的导航）
  navBusy.main = false;
  state._pfxSid = null;   // 消息区的增量渲染缓存随之失效
  state._loading = false; // 在途加载态一并作废（回首页是同步完成的导航）
  state.cur = null;
  loadProgress();
  state.msgs = [];
  resetEmpty();
  renderPanels(); // 回到首页：待决定的浮窗整体转为「来自「某对话」」（本对话已不存在）
  loadSessions();
  refreshCtxChip();
  $('input').focus();
}

async function openSession(id) {
  narrowClosePanels(); // 窄屏：在左栏浮层里选中会话即收起浮层
  if (marketOpen()) closeMarket(); // 市场像一张对话页：打开任何对话即关掉它（app-market.js）
  // 正在流式时切到别的会话：只放开本地这条流（后端执行照旧，回来会自动接上），
  // 不这样做的话旧会话的增量会被写进新会话的界面
  if (state.streaming && (!state.cur || state.cur.id !== id)) detachStream(mainView);
  const tok = ++navTok.main;
  navBusy.main = true;
  // 乐观切换：整包会话数据（含全部历史）在远程访问下要几秒，等待期间界面一步不动，
  // 看起来就是「没切过去」，失败时更是静默留在旧会话——像「跳回」。这里先用列表项
  // （与详情 meta 同型，只可能稍旧）当临时 meta：标题/侧栏高亮/输入框立即换到目标
  // 会话，消息区先进加载态；state.cur 此刻就是目标，期间一切「刷新当前会话」的兜底
  // （refreshCurrent 等）都以目标为准，旧会话的晚到事件不可能再把界面顶回去
  const brief = (state.sessions || []).find((s) => s.id === id);
  if (brief && (!state.cur || state.cur.id !== id)) optimisticSwitch(mainView, brief);
  try {
    await loadSessionInto(id, tok);
  } catch (e) {
    // 加载失败：留在目标会话的壳里，给原因与「重试」，绝不静默退回旧会话
    if (tok === navTok.main) showChatLoadError(mainView, e.message, () => {
      // 重试先手动补加载反馈：state.cur 已是目标会话，openSession 的乐观分支会跳过
      mainView.state._loading = true;
      showChatLoading(mainView);
      openSession(id);
    });
  } finally {
    // 只有仍是最新一次导航才解禁（被更新的导航顶掉时，由它自己负责收尾）
    if (tok === navTok.main) navBusy.main = false;
  }
}

// 乐观切换的落地：壳（标题/徽标/输入区/草稿/侧栏高亮）立即换到目标会话，消息区置为
// 加载态；数据到达后 loadSessionInto / openSubSession 整体接管。列表项当临时 cur，
// 消息与派生数据清空，_pfxSid 失效（渲染缓存随会话作废）
function optimisticSwitch(view, brief) {
  const st = view.state;
  st.cur = { ...brief };
  st.msgs = [];
  st.pendingSteer = [];
  st.pendingQueue = [];
  st.checkpoints = [];
  st.deliveries = [];
  st.streamText = '';
  st.streamThinking = '';
  st._pfxSid = null;
  st._loading = true; // renderMessages / reloadMessages 见此标记让路，别冲掉加载态
  applySessionChrome(view);
  showChatLoading(view);
}

// 会话壳的即时更新（本地纯渲染）：主对话换顶栏标题/模式徽标与侧栏高亮，子对话换
// 下拉头部；输入区控件与草稿、待决定浮窗跟着走。加载完成后的完整收尾（抽屉/进度/
// 计划视图/接流）仍在各加载函数里
function applySessionChrome(view) {
  const st = view.state;
  if (view === mainView) {
    applyTopbar();
    renderSessionList();
    placeComposer();
  } else {
    subRenderHeader();
    syncComposerPad(view);
  }
  syncComposer(view);
  renderPanels(); // 待决定的浮窗跟着会话归属走
  try {
    viewEl(view, 'input').value = localStorage.getItem(
      view === mainView ? sessionDraftKey(st.cur.id) : subDraftKey(st.cur.id)
    ) || '';
  } catch (e) { viewEl(view, 'input').value = ''; }
  autoGrow(viewEl(view, 'input'));
}

// 顶栏标题与模式徽标（主对话区；乐观阶段与数据落地共用）
function applyTopbar() {
  $('sess-title').textContent = state.cur.title || '(未命名)';
  const badge = $('mode-badge');
  // 模式徽标；挂在主对话下的子对话再标出来历（它可能是 task/office/coding/aux 任一模式）
  const modeLabel = state.cur.mode === 'assistant' ? '数字助理'
    : state.cur.mode === 'aux' ? '辅助对话'
    : CHAT_MODES.some((m) => m.v === state.cur.mode) ? chatModeOf(state.cur.mode).label : '';
  const badgeLabel = state.cur.parentId ? (modeLabel ? modeLabel + ' · 子对话' : '子对话') : modeLabel;
  if (badgeLabel) {
    badge.textContent = badgeLabel;
    badge.classList.remove('hidden');
  } else {
    badge.classList.add('hidden');
  }
}

// 会话加载态：消息区居中转圈（样式 .chat-loading）；数据落地后 renderMessages 全量接管
function showChatLoading(view) {
  view.col.innerHTML = '<div class="chat-loading"><span class="cl-spin"></span>正在加载对话…</div>';
  view.scroll.scrollTop = 0;
}

// 加载失败：留在目标会话的壳里，给原因与「重试」（重试即重新走一次切换加载）
function showChatLoadError(view, msg, retry) {
  view.state._loading = false;
  const box = document.createElement('div');
  box.className = 'chat-loading';
  const txt = document.createElement('span');
  txt.textContent = '对话加载失败：' + (msg || '网络异常');
  const btn = document.createElement('button');
  btn.className = 'cl-retry';
  btn.type = 'button';
  btn.textContent = '重试';
  btn.onclick = retry;
  box.appendChild(txt);
  box.appendChild(btn);
  view.col.innerHTML = '';
  view.col.appendChild(box);
}

async function loadSessionInto(id, tok) {
  const data = await api('/api/sessions/' + id);
  // 已被更新的导航（切换/回首页）取代：晚到的旧响应不落地，否则会把界面顶回旧会话
  if (tok !== navTok.main) return;
  state.cur = data.meta;
  state._loading = false; // 数据落地：退出加载态（必须在 renderMessages 之前）
  state.msgs = data.messages || [];
  state.pendingSteer = (data.pending && data.pending.steer) || [];
  state.pendingQueue = (data.pending && data.pending.queue) || [];
  state.queuePaused = !!(data.pending && data.pending.paused);
  // 空闲但队列有保留（上轮停止/出错/暂停后留下的）：横幅给「继续」入口
  state.queueHeld = state.pendingQueue.length > 0 && !state.queuePaused &&
    !(data.running || (state.cur && state.cur.status === 'running'));
  state.checkpoints = data.checkpoints || [];
  state.deliveries = data.deliveries || [];
  if (data.error) showRunError(mainView, data.error); else hideRunError(mainView);
  // 计划/目标二选一：旧数据可能两个都开着，收敛为只留计划并落库
  if (state.cur.planMode && state.cur.goalMode) {
    state.cur.goalMode = false;
    api('/api/sessions/' + id, { method: 'PATCH', body: JSON.stringify({ goalMode: false }) }).catch(() => {});
  }
  applySessionChrome(mainView); // 壳与乐观阶段同一套：标题/徽标/侧栏高亮/输入区/草稿/浮窗
  refreshDrawer();
  refreshCtxChip();
  loadProgress();
  planSid = id;
  planSel = ''; planPinned = ''; planWantSeq = 0; planWantQid = ''; // 换了对话：右栏计划的选择重置
  if (rpActive === 'plan') loadPlanView();
  maybeAutoOpenPlan();
  renderMessages(state.msgs, mainView, { jump: true });
  renderQueueDock(mainView);
  // 打开即视为已读：清红点与绿点
  delete dotDone[id];
  if (state.cur && state.cur.unread) {
    state.cur.unread = false;
    api('/api/sessions/' + id, { method: 'PATCH', body: JSON.stringify({ unread: false }) }).catch(() => {});
  }
  // 会话正在执行（含刷新页面后重新打开）：接上它的流，继续实时显示
  if (!state.streaming && (data.running || state.cur.status === 'running')) attachRun(mainView);
  loadSessions();
  JX.emit('session:open', state.cur);
}

/* ---------- 消息渲染（回合分组 + 折叠体系，逻辑对齐数字助理2 的 dsh-transcript-fold） ---------- */

// 折叠阈值：流式尾部窗口行数；中间正文豁免阈值（非空行数 / 全角当量字数，全角记 1 半角记 0.5）
const TAIL_ROWS = 10;
const LONG_TEXT_LINES = 3;
const LONG_TEXT_CHARS = 85;

// 旧库兼容判定（只读旧数据用，新行一律带显式 kind，见《消息类型规范.md》§5）：
// 历史库里系统注入的通知曾是 kind=chat 的 user 行，只能按正文前缀识别。
// 前缀清单与规范 §1.4 的 origin 子类一一对应。
function isSysNote(txt) {
  return txt.startsWith('【派发回报】') || txt.startsWith('【系统中断】') || txt.startsWith('【执行中断】') ||
    txt.startsWith('【文件上传】') || txt.startsWith('【文件回退】') || txt.startsWith('【目标续跑】') ||
    txt.startsWith('【派发子对话】') || txt.startsWith('【数字助理派发】') || txt.startsWith('【数字助理转达】') ||
    txt.startsWith('【待办任务派发｜') || txt.startsWith('<goal_round>') || txt.startsWith('【交付完成】');
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

// 单行折叠过程行（文本正文）：提示词材料各区与变更行共用
function procTextRow(label, sub, text, seq) {
  const row = document.createElement('div');
  row.className = 'proc-row';
  if (seq != null) row.dataset.seq = seq;
  row.innerHTML =
    '<div class="proc-head">' + chev('p-chev') +
    '<span class="p-name">' + label + '</span><span class="p-sub">· ' + sub + '</span></div>' +
    '<div class="proc-body"><div class="tk-text"></div></div>';
  row.querySelector('.tk-text').textContent = text;
  bindProcToggle(row);
  return row;
}

// 行级 diff（多重集合比较，保序输出）：与后端导出日志同一套口径
function lineDiffOf(prev, cur) {
  const oldL = String(prev == null ? '' : prev).split('\n');
  const curL = String(cur == null ? '' : cur).split('\n');
  const removed = [], added = [];
  const cnt = new Map();
  curL.forEach((l) => cnt.set(l, (cnt.get(l) || 0) + 1));
  oldL.forEach((l) => { const c = cnt.get(l) || 0; if (c > 0) cnt.set(l, c - 1); else removed.push(l); });
  const cnt2 = new Map();
  oldL.forEach((l) => cnt2.set(l, (cnt2.get(l) || 0) + 1));
  curL.forEach((l) => { const c = cnt2.get(l) || 0; if (c > 0) cnt2.set(l, c - 1); else added.push(l); });
  return { removed, added };
}
const DIFF_CAP = 15;
function diffBody(prev, cur) {
  const { removed, added } = lineDiffOf(prev, cur);
  const out = [];
  removed.slice(0, DIFF_CAP).forEach((l) => out.push('- ' + l));
  if (removed.length > DIFF_CAP) out.push(`-（其余 ${removed.length - DIFF_CAP} 行省略）`);
  added.slice(0, DIFF_CAP).forEach((l) => out.push('+ ' + l));
  if (added.length > DIFF_CAP) out.push(`+（其余 ${added.length - DIFF_CAP} 行省略）`);
  return out.join('\n') + (out.length ? '\n————\n' : '') + String(cur ?? '');
}

// 首轮拼装的「系统提示词」行：渲染在回合用户消息上方——它就是请求消息序列的
// 第 1 条（messages[0]），每次请求逐字节重发，不变则命中前缀缓存
function sysHeadRow(sys, seq) {
  return procTextRow('系统提示词已拼装', fmtCount(String(sys).length) + ' 字 · 请求消息序列第 1 条（messages[0]）· 不变则命中前缀缓存', sys, seq);
}

// 工具定义行：渲染在「系统提示词」行下方、用户消息**之前**。它在请求里不是消息——
// 是请求体顶层的 tools 数组（与 messages 平级，不属于消息序列，所以没有"第几条"），
// 由服务端按自己的模板排进提示。排在消息序列之前展示，才对得上真实请求的顺序：
// 系统提示词 → 工具定义 → 消息序列（末尾那条 user 消息是运行环境快照）。
function toolsRow(tools, seq) {
  const row = document.createElement('div');
  row.className = 'proc-row';
  if (seq != null) row.dataset.seq = seq;
  row.innerHTML =
    '<div class="proc-head">' + chev('p-chev') +
    '<span class="p-name">工具定义</span><span class="p-sub">· ' + tools.length + ' 个 · 请求体顶层字段（与 messages 平级，不在消息序列里）</span></div>' +
    '<div class="proc-body"><pre></pre></div>';
  row.querySelector('pre').textContent = JSON.stringify(tools, null, 2);
  bindProcToggle(row);
  return row;
}

// 首轮拼装的尾部材料：只有「运行环境」——它是请求**消息序列里**的一条 user 快照
// （追加式：首条整份、其后按行差分；有变化才追加，且追加在当轮用户消息之后，
// 所以首轮它就是那次请求的最后一条消息），故展示在用户消息下方。
function promptTailRows(env, seq) {
  return procTextRow('运行环境', fmtCount(String(env ?? '').length) + ' 字 · 追加式环境快照（作为 user 消息进消息序列，有变化才追加新的）', env ?? '', seq);
}

// 系统提示词变更行：diff + 新全文；前缀改变意味着此前缓存的前缀将失效
function sysChangeRow(prev, sys, seq) {
  return procTextRow('系统提示词变更', '前缀改变 · 此前缓存的前缀将失效 · 现 ' + fmtCount(String(sys).length) + ' 字', diffBody(prev, sys), seq);
}

// 工具定义变更行：标题列出新增/移除的工具名，正文是新 tools JSON
function toolsChangeRow(prevTools, tools, seq) {
  const names = (arr) => (Array.isArray(arr) ? arr.map((t) => t && t.function && t.function.name).filter(Boolean) : []);
  const { removed, added } = lineDiffOf(names(prevTools).join('\n'), names(tools).join('\n'));
  const parts = [];
  if (removed.length) parts.push('移除 ' + removed.join('、'));
  if (added.length) parts.push('新增 ' + added.join('、'));
  const sub = parts.length ? parts.join(' · ') : '共 ' + (Array.isArray(tools) ? tools.length : 0) + ' 个';
  const row = document.createElement('div');
  row.className = 'proc-row';
  if (seq != null) row.dataset.seq = seq;
  row.innerHTML =
    '<div class="proc-head">' + chev('p-chev') +
    '<span class="p-name">工具定义变更</span><span class="p-sub">· ' + sub + '</span></div>' +
    '<div class="proc-body"><pre></pre></div>';
  row.querySelector('pre').textContent = tools == null ? '（无工具定义）' : JSON.stringify(tools, null, 2);
  bindProcToggle(row);
  return row;
}

// 运行环境更新行：轮内权限提升/模式切换/能力开关/技能增减等真实变化导致环境
// 变化时，在发生位置展示变化内容（diff）与新快照全文；首行的「用户最近发消息
// 时间」只在用户真发消息时变——模型自己执行不会随时间流逝冒出这条行
function envNoteRow(text, prev, seq) {
  return procTextRow('运行环境更新', '随下次请求尾部发送 · ' + fmtCount(String(text).length) + ' 字',
    prev == null ? String(text) : diffBody(prev, text), seq);
}

// 压缩摘要行：摘要注入在发生位置展示（自动/手动、覆盖至第 N 条）
function summaryRow(text, origin, covers, seq) {
  const row = document.createElement('div');
  row.className = 'proc-row';
  if (seq != null) row.dataset.seq = seq;
  const sub = (origin === 'auto' ? '自动' : '手动') + (covers ? ' · 覆盖至第 ' + covers + ' 条' : '');
  row.innerHTML =
    '<div class="proc-head">' + chev('p-chev') +
    '<span class="p-name">压缩摘要</span><span class="p-sub">· ' + sub + '</span></div>' +
    '<div class="proc-body"><div class="tk-text"></div></div>';
  row.querySelector('.tk-text').textContent = text;
  bindProcToggle(row);
  return row;
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
        const attURL = '/attachments/' + encodeURIComponent(f);
        a.target = '_blank';
        a.title = '点击查看原图';
        const im = document.createElement('img');
        im.className = 'tc-img';
        im.loading = 'lazy';
        im.alt = '图片附件';
        if (window.JX_REMOTE) { // 远程模式：附件经隧道 blob 化后再上屏
          JX_REMOTE.assetURL(attURL).then((u) => { a.href = u; im.src = u; });
        } else {
          a.href = attURL;
          im.src = attURL;
        }
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

// 上下文材料条目：首轮尾部材料、运行环境更新、系统提示词/工具定义变更。
// 它们与思考/工具过程一起折叠（想核对「模型看到了什么」时再展开），
// 渲染的两个路径（完成态/流式）都按此归类。
function isMatItem(it) {
  return it.k === 'prompttail' || it.k === 'envnote' || it.k === 'syschange' || it.k === 'toolschange';
}

function foldLabel(cnt) {
  const parts = [];
  if (cnt.think) parts.push(cnt.think + ' 次思考');
  if (cnt.tool) parts.push(cnt.tool + ' 次工具调用');
  if (cnt.mat) parts.push(cnt.mat + ' 条上下文材料');
  if (!parts.length) parts.push('已折叠 ' + cnt.rows + ' 行');
  return parts.join(' · ');
}

function secCount(items) {
  const cnt = { think: 0, tool: 0, mat: 0, rows: items.length };
  for (const it of items) {
    if (it.k === 'think') cnt.think++;
    else if (it.k === 'tool' || it.k === 'subtask') cnt.tool++;
    else if (isMatItem(it)) cnt.mat++;
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

/* 继承段：子对话开头的「来自主对话」历史，整段折叠成一条，点开是完整对话流。
   内部复用主线渲染（用户气泡 + 回合渲染），所以外观与正文完全一致；
   父对话的系统提示词快照行也在其中（一目了然「含系统提示词」）。 */
function inheritBlock(items, resultOf, sid) {
  const key = sid + ':inherit';
  const sec = document.createElement('div');
  sec.className = 'tf-sec inherit';
  sec.dataset.key = key;
  const bar = document.createElement('button');
  bar.type = 'button';
  bar.className = 'turn-fold';
  const label = document.createElement('span');
  label.className = 'tf-label';
  // 环境快照行不显示（与正文一致），计数按可见条数
  const n = items.filter((m) => m.kind !== 'env').length;
  label.textContent = '来自主对话 · ' + n + ' 条（含系统提示词）';
  bar.appendChild(label);
  bar.insertAdjacentHTML('beforeend', chev('tf-chev'));
  const body = document.createElement('div');
  body.className = 'tf-body';
  const turns = groupTurns(items, resultOf);
  turns.forEach((turn, ti) => {
    if (turn.sysHead) body.appendChild(sysHeadRow(turn.sysHead, turn.sysHeadSeq));
    if (turn.headTools) body.appendChild(toolsRow(turn.headTools, turn.headToolsSeq));
    if (turn.userMsg) body.appendChild(userBubble(turn.userMsg, viewOf(sid)));
    renderFinishedTurn(body, turn, key + ':' + ti);
  });
  const open = turnOpen[key] === true;
  bar.classList.toggle('open', open);
  if (open) showEl(body); else hideEl(body);
  bar.onclick = () => {
    const nowOpen = body.hasAttribute('hidden');
    setSecOpen(sec, nowOpen);
    if (nowOpen) turnOpen[key] = true;
    else delete turnOpen[key];
  };
  sec.appendChild(bar);
  sec.appendChild(body);
  return sec;
}

// Ctrl+F 命中折叠内容时浏览器揭开 hidden，这里同步箭头态（过程行与折叠段都处理；
// 折叠段可以嵌套——继承段里还有回合折叠段，所以逐层往上同步）
document.addEventListener('beforematch', (e) => {
  let el = e.target;
  while (el && el.closest) {
    const row = el.closest('.proc-row');
    if (row) { setProcOpen(row, true); el = row.parentElement; continue; }
    const sec = el.closest('.tf-sec');
    if (sec) { setSecOpen(sec, true); el = sec.parentElement; continue; }
    break;
  }
});

/* ---------- 回合分组与条目渲染（类型契约的分发层，见《消息类型规范.md》） ---------- */

// 行的展示类型：新行一律带显式 kind（后端保证）；旧库残留的 chat+【】前缀
// user 行按 report 兼容（规范 §5）。此后一切分发只看 kind，不猜内容。
function displayKindOf(m) {
  const k = m.kind;
  if (k === 'chat' || k === 'report' || k === 'summary' || k === 'env' || k === 'prompt' || k === 'inherit' || k === 'request' || k === 'audit') return k;
  if ((!k || k === 'chat') && m.role === 'user' && isSysNote(String(m.content ?? ''))) return 'report';
  return 'chat';
}

// 存储行类型 → 条目构造器（契约的注册表）。后端新增类型：在此注册一行
// 构造器即可获得标准样式；需要新样式先在规范 §3 登记样式基元。
// 构造器往当前回合塞条目；request/audit 是审计工件不展示（导出功能的数据源，
// 正文由服务端下发瘦身时置空，见 server.go trimMessagesForUI）。
const ROW_ITEM_BUILDERS = {
  // 对话内容：user 开新回合；assistant 拆 think/text/tool 条目（复合行拆分规则见规范 §3）
  chat(m, st) {
    if (m.role === 'user') {
      st.cur = { userMsg: m, items: [] };
      st.turns.push(st.cur);
      return;
    }
    if (m.role !== 'assistant') return; // tool 行经 resultOf 配对进工具条目，不单列
    st.ensure();
    if (m.thinking) st.cur.items.push({ k: 'think', seq: m.seq, text: m.thinking });
    if (m.content) st.cur.items.push({ k: 'text', seq: m.seq, m, text: String(m.content) });
    for (const tc of m.tool_calls || []) {
      const n = (tc.function && tc.function.name) || '';
      st.cur.items.push({
        k: (n === 'dispatch_task' || n === 'dispatch_sub' || n === 'send_to_session') ? 'subtask'
          : (n === 'exit_plan_mode' ? 'plancard' : 'tool'),
        seq: m.seq, tc, result: st.resultOf[tc.id],
      });
    }
  },
  // 系统注入通知：归入当前回合常显
  report(m, st) {
    st.ensure();
    st.cur.items.push({ k: 'note', m });
  },
  // 压缩摘要：在发生位置展示折叠行
  summary(m, st) {
    st.ensure();
    st.cur.items.push({ k: 'summary', seq: m.seq, text: String(m.content ?? ''), origin: m.origin, covers: m.coversUpTo });
  },
  // 运行环境快照：与最近一次已知环境相同则跳过；有真实变化（权限提升、
  // 模式切换、能力开关、技能增减、用户新发了一条消息等）才在发生位置显示
  // 「运行环境更新」折叠行
  env(m, st) {
    const txt = String(m.content ?? '');
    if (txt === st.lastEnv) return;
    st.ensure();
    st.cur.items.push({ k: 'envnote', seq: m.seq, text: txt, prev: st.lastEnv });
    st.lastEnv = txt;
  },
  // 提示词拼装快照：变化驱动展示（规范 §4）——首轮完整交代拼装结构
  // （系统提示词 + 工具定义在用户消息上方、运行环境在请求末尾，即请求真实排布），
  // 此后仅在系统提示词/工具定义变更时出折叠行；环境变化由 env 行负责
  prompt(m, st) {
    let sys = '', env = '', tools = null;
    try {
      const j = JSON.parse(String(m.content ?? '{}'));
      sys = j.sys || ''; env = j.env || '';
      if (Array.isArray(j.tools)) tools = j.tools;
    } catch (e) { /* 忽略坏行 */ }
    st.ensure();
    const toolsJson = tools == null ? '' : JSON.stringify(tools);
    if (!st.sawPrompt) {
      // 首轮：lastEnv 同步为本轮环境，随后的同文快照行会被去重
      st.cur.sysHead = sys; st.cur.sysHeadSeq = m.seq;
      // 工具定义跟着提示词走（都在消息序列之前展示），运行环境留在尾部材料里
      st.cur.headTools = tools; st.cur.headToolsSeq = m.seq;
      st.cur.items.push({ k: 'prompttail', seq: m.seq, env });
      st.lastEnv = env;
    } else {
      // 空壳行（服务端把与上一条逐字节相同的快照瘦成 {} 后）不产生任何变更展示：
      // 有值才比较、才推进基线——sys/tools 真实“变空”不可能发生，空值只来自瘦身
      if (sys && sys !== st.lastSys) st.cur.items.push({ k: 'syschange', seq: m.seq, sys, prev: st.lastSys });
      if (toolsJson && toolsJson !== st.lastToolsJson) st.cur.items.push({ k: 'toolschange', seq: m.seq, tools, prevTools: st.lastTools });
    }
    st.sawPrompt = true;
    if (sys) st.lastSys = sys;
    if (toolsJson) { st.lastToolsJson = toolsJson; st.lastTools = tools; }
  },
  // 继承标记行由 inheritBlock 分段处理（这里不产条目）
  inherit() {},
  // 审计工件不展示
  request() {}, audit() {},
};

// 按真实用户发言切回合；系统通知（回报/中断/上传）归入当前回合且常显；
// assistant 消息按序拆成思考/正文/工具条目，tool 消息经 resultOf 配对进条目。
// 每次请求都会完整重发系统提示词（协议如此）——「不变」= 逐字节相同，
// 前缀缓存据此命中，所以上下文材料只在变化时展示。
function groupTurns(msgs, resultOf) {
  const st = {
    turns: [], cur: null, resultOf,
    sawPrompt: false, lastSys: '', lastToolsJson: '', lastTools: null, lastEnv: null,
  };
  st.ensure = () => {
    if (!st.cur) { st.cur = { userMsg: null, items: [] }; st.turns.push(st.cur); }
  };
  for (const m of msgs) {
    const build = ROW_ITEM_BUILDERS[displayKindOf(m)];
    if (build) build(m, st);
  }
  return st.turns;
}

// 消息时间戳文案：优先 18:45（同一天），跨天补日期 09-28 18:45；时间缺失返回空串。
// 消息行自带的 createdAt 由后端落库时写入（见 StoredMessage.CreatedAt）。
function msgTimeLabel(iso) {
  if (!iso) return '';
  const d = new Date(String(iso).replace(' ', 'T'));
  if (isNaN(d.getTime())) return '';
  const p2 = (n) => String(n).padStart(2, '0');
  const hm = p2(d.getHours()) + ':' + p2(d.getMinutes());
  const now = new Date();
  const sameDay = d.getFullYear() === now.getFullYear() && d.getMonth() === now.getMonth() && d.getDate() === now.getDate();
  return sameDay ? hm : p2(d.getMonth() + 1) + '-' + p2(d.getDate()) + ' ' + hm;
}

// 时间标签节点（放进操作行：用户消息在复制左侧，模型最终输出在最右）
function actTime(iso, side) {
  const el = document.createElement('span');
  el.className = 'act-time' + (side ? ' ' + side : '');
  el.textContent = msgTimeLabel(iso);
  el.title = iso ? String(iso).replace('T', ' ') : '';
  return el;
}

// 用户消息气泡：右下悬停操作（时间 / 复制 / 编辑重发）。编辑只对真实用户消息开放
// （kind=chat）；系统注入的回报/上传/目标续跑/继承标记不参与。
function userBubble(m, view) {
  const wrap = document.createElement('div');
  wrap.className = 'msg user';
  wrap.dataset.seq = m.seq; // 搜索命中定位用
  const b = document.createElement('div');
  b.className = 'bubble';
  b.textContent = String(m.content ?? '');
  wrap.appendChild(b);
  if (m.kind === 'chat' && view) {
    const acts = document.createElement('div');
    acts.className = 'msg-actions user-acts';
    acts.appendChild(actTime(m.createdAt)); // 发送时间：排在复制按钮左边
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
    const ed = document.createElement('button');
    ed.className = 'act-copy act-edit';
    ed.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z"/></svg>编辑';
    ed.title = '修改这条消息并重新发送；它之后的对话记录将被移除';
    ed.onclick = () => editUserMsg(m, view, wrap);
    acts.appendChild(ed);
    wrap.appendChild(acts);
  }
  return wrap;
}

// 悬停操作（仅回合最终输出）：复制原文 / 手动压缩 / 分支 / 插件扩展位
function msgActions(m, opts) {
  opts = opts || {};
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
  const cbOrig = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v5"/><path d="m9.5 5.5 2.5 2.5 2.5-2.5"/><path d="M12 21v-5"/><path d="m9.5 18.5 2.5-2.5 2.5 2.5"/><path d="M4 12h16"/></svg>压缩';
  cb.innerHTML = cbOrig;
  cb.title = '把此消息之前的旧对话压缩为一条摘要（最近几轮保持原样）';
  cb.onclick = async () => {
    if (!state.cur || state.streaming) return;
    cb.textContent = '压缩中…';
    cb.disabled = true;
    try {
      await api('/api/sessions/' + state.cur.id + '/compact', { method: 'POST' });
      cb.textContent = '已压缩';
      await openSession(state.cur.id);
      setTimeout(() => { cb.innerHTML = cbOrig; cb.disabled = false; }, 1500);
    } catch (e) {
      alert('压缩失败：' + e.message);
      cb.innerHTML = cbOrig;
      cb.disabled = false;
    }
  };
  acts.appendChild(cb);
  if (opts.final) {
    const bk = document.createElement('button');
    bk.className = 'act-copy act-branch';
    bk.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="6" cy="5" r="2.2"/><circle cx="6" cy="19" r="2.2"/><circle cx="18" cy="9" r="2.2"/><path d="M6 7.2v9.6"/><path d="M18 11.2c0 3.4-3.2 4-7.5 4.3"/></svg>分支';
    bk.title = '从这条回答处复制出一个新会话并切换过去；当前对话保持不变';
    bk.onclick = () => forkFromAnswer(m, bk);
    acts.appendChild(bk);
  }
  // 消息引用存到行上：组件（如朗读）注册钩子晚于本行渲染时，registerMessageAction
  // 靠它对已渲染的行回放钩子（远程中继下组件加载秒级，历史消息常先于组件渲染）
  acts._jxMsg = m;
  for (const fn of JX._msgActions) { try { fn(acts, m); } catch (e) { console.warn('[JX] action', e); } }
  acts.appendChild(actTime(m.createdAt, 'act-time-end')); // 回复时间：排在最右
  return acts;
}

// 从某条回答处分支出新会话：消息按 seq 前缀复制，创建成功后切过去看。
// 会话执行中也可分支（后端只复制到该 seq，前缀稳定）。
async function forkFromAnswer(m, btn) {
  if (!m.sessionId) return;
  if (btn) { btn.disabled = true; btn.classList.add('ok'); }
  try {
    const fork = await api('/api/sessions/' + m.sessionId + '/fork', {
      method: 'POST',
      body: JSON.stringify({ upTo: m.seq }),
    });
    await loadSessions();
    if (mainView.state.cur && mainView.state.cur.id === m.sessionId) {
      openSession(fork.id);
    } else if (subState.cur && subState.cur.id === m.sessionId) {
      refreshDrawer();
      openSubSession(fork.id);
    }
  } catch (e) {
    alert('分支失败：' + e.message);
  }
  if (btn) { btn.disabled = false; btn.classList.remove('ok'); }
}

function renderItem(it, opts) {
  if (it.k === 'prompttail') return promptTailRows(it.env, it.seq);
  if (it.k === 'envnote') return envNoteRow(it.text, it.prev, it.seq);
  if (it.k === 'syschange') return sysChangeRow(it.prev, it.sys, it.seq);
  if (it.k === 'toolschange') return toolsChangeRow(it.prevTools, it.tools, it.seq);
  if (it.k === 'summary') return summaryRow(it.text, it.origin, it.covers, it.seq);
  if (it.k === 'think') return thinkRow(it.text, it.seq);
  if (it.k === 'tool') return toolRow(it.tc, it.result, { seq: it.seq });
  if (it.k === 'plancard') return planCardRow(it.tc, it.seq, it.result);
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
    // 操作按钮只挂在回合最终输出下：中间正文不带，过程拼得更紧凑
    if (opts && opts.final) wrap.appendChild(msgActions(it.m, { final: true }));
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

// 【派发回报】消息中出现过的子会话 id（渲染期间有效）：子任务卡据此在刷新/
// 重新打开会话后也能正确显示「已完成」，不依赖内存里的 subDone 标记
let reportedSubs = new Set();
function scanReported(msgs) {
  const s = new Set();
  for (const m of msgs) {
    if (m.role !== 'user') continue;
    const txt = String(m.content ?? '');
    if (!txt.startsWith('【派发回报】')) continue;
    const hit = txt.match(/（([^\s（）]+)）/); // 回报格式：…「标题」（会话id）本轮结束
    if (hit) s.add(hit[1]);
  }
  return s;
}

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
  // （id 不能用 \S+ 贪婪匹配：后面的文案没有空格，会把整句中文都吞进 id）
  const sidMatch = out.match(/已派发后台执行：会话 ([^\s「]+)「/) || out.match(/^会话ID:\s*(\S+)$/m);
  const sid = sidMatch ? sidMatch[1] : '';
  const dispatched = out.includes('已派发后台执行');
  const done = (sid && reportedSubs.has(sid)) || subDone[sid] || (result && !dispatched);
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
  if (openBtn) openBtn.onclick = () => openSession(sid);
  if (running) renderSubActs(sid);
  return d;
}

/* ---------- 长消息折叠（只作用于数字助理主对话里「用户自己发的灰色气泡」：
   用户气泡，以及作为「用户消息变体」的派发通知气泡（同样是灰底气泡）。
   **数字助理的正文回复（.msg.assistant 的 .md）不在折叠目标里**——那是模型给出的
   正常回复，多长都完整展开显示，不裁剪、不出「展开 / 收起」控件（见 applyFolds）。 ----------
   需求：渲染高度超过 6 行就默认只显示前 6 行，底部渐隐 + 「展开 / 收起」按钮；
   短消息不折叠、不出按钮。判定必须基于**实际渲染出来的高度**——对话区宽度、
   字号一变，同一段文字换行出的行数就变，所以：
     - 阈值 = 运行时读到的行高（line-height）× 6，与元素内容高度（scrollHeight 去掉
       内边距）比较；裁剪用 max-height + overflow: hidden，scrollHeight 始终是完整
       内容高度，所以一个阈值同时管折叠态与展开态，不必先解开裁剪再量（判定不受
       自己上一次裁剪的影响，也就不会自激）；
     - 会话渲染完、对话区尺寸变化（拖宽/缩窗口）、字号变化时都重算（ResizeObserver +
       window resize，合到一帧）；所以不会出现「该折叠的没折叠」或「明明很短却折叠了」；
     - 用户手动展开过的（data-fold-open=1）重算后保持展开，只是按钮变成「收起」。
   作用域：只在当前会话 mode=assistant（数字助理主对话）时生效；工作任务会话、
   子对话（右栏抽屉）与流式输出中的那半句都不折叠。 */
const FOLD_LINES = 6;   // 折叠阈值（行）
const FOLD_SLACK = 2;   // 容差（px）：溢出不足 2px 不算，避免取整误差误判

let foldScheduled = false;
let foldTimer = 0;
let foldRetry = 0;      // 「当时还没排好版」的重试次数（最多 5 次）

// 是否需要折叠（只在数字助理主对话里做）
function foldEnabled() {
  return !!(state.cur && state.cur.mode === 'assistant');
}

// 折叠目标：主对话里已定稿的用户气泡与派发通知气泡（后者是
// 「用户消息的变体」，同样是长消息）。**数字助理的正文（.msg.assistant > .md）
// 刻意不在目标里**：那是模型给出的正常回复，多长都完整显示，不裁剪、不出折叠
// 控件（用户气泡的折叠效果保持不变）。跳过流式中的那块（边流边折叠会一直遮住
// 最新输出）与编辑态/排队态占位气泡（临时形态，带虚线边框）。
function foldTargets() {
  const col = mainView.col;
  if (!col) return [];
  const live = mainView.liveId ? document.getElementById(mainView.liveId) : null;
  const out = [];
  col.querySelectorAll('.msg.user > .bubble, .msg.sysnote > .bubble').forEach((el) => {
    const msg = el.parentElement;
    if (!msg || msg.classList.contains('editing') || msg.classList.contains('queued') || msg.classList.contains('pending-steer')) return;
    if (live && (msg === live || live.contains(msg))) return; // 流式实时块不折
    out.push(el);
  });
  return out;
}

// 溢出判定：与「当前是否折叠」无关——裁剪用的是 max-height + overflow: hidden，
// scrollHeight 始终是完整内容高度，所以一个阈值同时管折叠态与展开态，不需要先
// 解开裁剪再量（也就不会因为切换类名反过来触发 ResizeObserver）。阈值取
// 运行时读到行高 × 6 行，行高随字号设置变化，所以不写死像素值。
function foldMeasure(el) {
  const cs = getComputedStyle(el);
  const lh = parseFloat(cs.lineHeight);
  if (!lh) return null;
  const pad = parseFloat(cs.paddingTop || 0) + parseFloat(cs.paddingBottom || 0);
  const limit = lh * FOLD_LINES;
  return { overflow: el.scrollHeight - pad > limit + FOLD_SLACK, maxH: pad + limit };
}

// 重算并应用折叠状态（幂等：判定只看内容高度，不受自己上一次的裁剪影响，
// 所以结果稳定、不会自激）
function applyFolds() {
  const col = mainView.col;
  if (!col) return;
  const boxes = Array.from(col.querySelectorAll('.fold-box'));
  const enabled = foldEnabled();
  const seen = new Set();
  let unmeasurable = false; // 还没完成布局（高度 0）的元素：本轮跳过，稍后重算
  foldTargets().forEach((el) => {
    seen.add(el);
    const r = el.getBoundingClientRect();
    if (r.height === 0 || r.width === 0) {
      // 元素被隐藏/还没排好版时 scrollHeight 不可信（会误判成「不折叠」），
      // 所以这里不写结论，只标记需要重试。
      unmeasurable = true;
      return;
    }
    const m = enabled ? foldMeasure(el) : null;
    const btn = el.querySelector(':scope > .fold-btn');
    if (!m || !m.overflow) {
      // 不超过 6 行（或已切走会话）：彻底恢复原样，不留按钮与遮罩
      el.classList.remove('folded', 'fold-box');
      el.style.removeProperty('--fold-h');
      const fade = el.querySelector(':scope > .fold-fade');
      if (fade) fade.remove();
      if (btn) btn.remove();
      el.removeAttribute('data-fold-open');
      return;
    }
    if (!el.classList.contains('fold-box')) {
      el.classList.add('fold-box');
      const fade = document.createElement('div');
      fade.className = 'fold-fade';
      el.appendChild(fade);
    }
    el.style.setProperty('--fold-h', Math.ceil(m.maxH) + 'px');
    if (!btn) {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'fold-btn';
      b.textContent = '展开';
      b.onclick = () => toggleFold(el, b);
      el.appendChild(b);
      el.dataset.foldOpen = '0';
      el.classList.add('folded'); // 默认折叠：只显示前 6 行
    } else {
      // 已经在折叠体系里：保留用户的展开意图，只同步按钮文案
      const open = el.dataset.foldOpen === '1';
      el.classList.toggle('folded', !open);
      btn.textContent = open ? '收起' : '展开';
    }
    const want = el.dataset.foldOpen === '1' ? 'true' : 'false';
    const b2 = el.querySelector(':scope > .fold-btn');
    if (b2 && b2.getAttribute('aria-expanded') !== want) b2.setAttribute('aria-expanded', want); // 同值不写，免得触发 MutationObserver 空转
  });
  // 已经不在目标集合里的（重渲染后残留、切走会话）：清掉折叠痕迹
  boxes.forEach((el) => {
    if (seen.has(el)) return;
    el.classList.remove('folded', 'fold-box');
    el.style.removeProperty('--fold-h');
    el.removeAttribute('data-fold-open');
    el.querySelectorAll(':scope > .fold-fade, :scope > .fold-btn').forEach((n) => n.remove());
  });
  // 有元素当时还没排好版：稍后再算一次（最多 5 次，避免空转）
  if (unmeasurable && foldRetry < 5) { foldRetry++; setTimeout(scheduleFolds, 200); }
  else if (!unmeasurable) foldRetry = 0;
}

// 展开/收起：折叠态只放开高度上限（按钮留在底部），展开态按钮落到正文下方
function toggleFold(el, btn) {
  const folded = el.classList.toggle('folded');
  el.dataset.foldOpen = folded ? '0' : '1';
  btn.textContent = folded ? '展开' : '收起';
  btn.setAttribute('aria-expanded', folded ? 'false' : 'true');
  if (folded) followScroll(mainView, false);
}

// 一帧内合并多次请求（ResizeObserver 会在同一帧连发）
function scheduleFolds() {
  if (foldScheduled) return;
  foldScheduled = true;
  requestAnimationFrame(() => {
    foldScheduled = false;
    try { applyFolds(); } catch (e) { console.warn('[JX] fold', e); }
  });
}

// 尺寸/字号变化都要重算：观察对话列本身（宽度变了每行字数变，字号变了行高变，
// 两种都会让它的盒子尺寸变化）；消息 DOM 变化（流式重绘、尾部重建、后台刷新插入）
// 同样要重算——否则「渲染顺序」会让某条长消息错过那一次重算，出现「该折没折」。
// 另挂窗口 resize 兜底，字体就绪后再算一次（行高/字宽会变）。
function setupFolds() {
  if (setupFolds.done) return;
  setupFolds.done = true;
  if (typeof ResizeObserver === 'function' && mainView.col) {
    new ResizeObserver(() => scheduleFolds()).observe(mainView.col);
  }
  if (typeof MutationObserver === 'function' && mainView.col) {
    // 只算状态是否变，不变就不写 DOM（见 applyFolds），所以不会自激空转
    new MutationObserver(() => scheduleFolds()).observe(mainView.col, { childList: true, subtree: true });
  }
  try { if (document.fonts && document.fonts.ready) document.fonts.ready.then(() => scheduleFolds()); } catch (e) {}
  window.addEventListener('resize', () => {
    clearTimeout(foldTimer);
    foldTimer = setTimeout(scheduleFolds, 120); // 拖窗口时别每像素都量一遍
  });
}

/* ---------- 文件改动面板与交付成果卡片（文件快照子系统的前端半边） ---------- */

// 文件回退请求（「全部撤回」「撤回」「恢复到此版本前」共用）：成功刷新视图，
// 失败按服务端口径提示——409 blocked 时列出"快照后被外部改过"的文件清单。
// payload 走 handleFileRewind 的三种口径之一：{seqs} / {seqs,path} / {checkpointId}。
async function fileRewind(view, payload, btn, what) {
  if (view.state.streaming) {
    view.hint.textContent = '本轮执行中，等结束再撤回';
    setTimeout(() => { if (view.hint.textContent === '本轮执行中，等结束再撤回') view.hint.textContent = ''; }, 2500);
    return false;
  }
  btn.disabled = true;
  try {
    const d = await api('/api/sessions/' + view.state.cur.id + '/file-rewind', {
      method: 'POST', body: JSON.stringify(payload),
    });
    const msg = '已' + what + '：恢复 ' + (d.restored || 0) + ' 个文件、撤回 ' + (d.deleted || 0) + ' 个新建文件';
    view.hint.textContent = msg;
    setTimeout(() => { if (view.hint.textContent === msg) view.hint.textContent = ''; }, 5000);
    if (view === mainView) openSession(view.state.cur.id); else openSubSession(view.state.cur.id);
    return true;
  } catch (err) {
    btn.disabled = false;
    const list = (err.unsafe || []).map((u) => u.path + '：' + (u.reason || '被外部修改')).join('\n');
    if (err.blocked && list) {
      alert('以下文件在快照后被外部修改，无法安全回退：\n' + list);
    } else {
      alert('撤回失败：' + err.message);
    }
    return false;
  }
}

// 一个文件的历史版本列表容器（点「历史版本」就地展开/收起）。数据来自会话
// 快照（checkpoint）：每条 = 该文件的一次改动及其"改动前内容"，可查看内容
// 与当前文件的差异，或「恢复到此版本前」（只影响这一个文件）。
function fileVersionBox(view, path) {
  const box = document.createElement('div');
  box.className = 'fp-vers';
  const list = document.createElement('div');
  list.className = 'fv-list';
  box.appendChild(list);
  hideEl(box);
  box._loaded = false;
  box._load = async () => {
    list.innerHTML = '<div class="fv-tip">读取历史版本…</div>';
    try {
      const d = await api('/api/sessions/' + view.state.cur.id + '/file-versions?path=' + encodeURIComponent(path));
      renderVersionList(view, path, list, d);
    } catch (err) {
      list.innerHTML = '<div class="fv-tip bad">读取失败：' + esc(err.message) + '</div>';
    }
  };
  return box;
}

// 历史版本清单：最旧在前，末条标「当前」（该版本改动后的内容 == 文件现状）。
// 每项两个动作：查看（就地展开内容/差异）与恢复到此版本前（checkpointId 口径）。
function renderVersionList(view, path, list, d) {
  const vers = d.versions || [];
  list.innerHTML = '';
  const cap = document.createElement('div');
  cap.className = 'fv-tip';
  cap.textContent = vers.length
    ? '共 ' + vers.length + ' 个历史版本（最旧在前）' + (d.exists ? ' · 当前文件 ' + fmtBytes(d.size || 0) + '' : ' · 当前文件已不存在')
    : '没有历史版本（快照可能已按保留策略清理）';
  list.appendChild(cap);
  vers.forEach((v, i) => {
    const item = document.createElement('div');
    item.className = 'fv-item';
    const when = (v.createdAt || '').replace('T', ' ').slice(5, 16);
    const what = v.existedBefore ? '改动前 ' + fmtBytes(v.beforeSize || 0) : '此版本前文件不存在';
    const head = document.createElement('div');
    head.className = 'fv-head';
    head.innerHTML = '<span class="fv-idx">v' + (i + 1) + '</span>' +
      '<span class="fv-when">' + esc(when) + ' · ' + esc(v.toolName || '') + '</span>' +
      '<span class="fv-note">' + esc(what) + ' · 改动后 ' + fmtBytes(v.afterSize || 0) + '</span>' +
      (v.current ? '<span class="fv-cur">当前</span>' : '');
    const acts = document.createElement('span');
    acts.className = 'fv-acts';
    const showBtn = document.createElement('button');
    showBtn.type = 'button';
    showBtn.className = 'fv-btn fv-view';
    showBtn.textContent = '查看内容';
    const restBtn = document.createElement('button');
    restBtn.type = 'button';
    restBtn.className = 'fv-btn fv-restore';
    restBtn.textContent = '恢复到此版本前';
    const bodyEl = document.createElement('div');
    bodyEl.className = 'fv-body';
    hideEl(bodyEl);
    showBtn.onclick = async (e) => {
      e.stopPropagation();
      if (!bodyEl.hasAttribute('hidden')) { hideEl(bodyEl); return; }
      bodyEl.innerHTML = '<div class="fv-tip">读取中…</div>';
      showEl(bodyEl);
      try {
        const t = await api('/api/sessions/' + view.state.cur.id +
          '/file-version?path=' + encodeURIComponent(path) + '&id=' + encodeURIComponent(v.id));
        renderVersionBody(view, path, bodyEl, t);
      } catch (err) {
        bodyEl.innerHTML = '<div class="fv-tip bad">读取失败：' + esc(err.message) + '</div>';
      }
    };
    restBtn.onclick = async (e) => {
      e.stopPropagation();
      if (!(await confirmCenter('把这个文件恢复到 v' + (i + 1) + '（' + when + '）这次改动之前？\n' +
        (v.existedBefore ? '将把当前内容换成那次改动前的内容。' : '将删除这个文件（那次改动前它不存在）。') +
        '\n只影响这一个文件；对话记录不变（已按快照校验，外部改过的文件会被拒绝）。'))) return;
      await fileRewind(view, { checkpointId: v.id, path }, restBtn, '恢复 ' + path);
    };
    acts.appendChild(showBtn);
    // 「当前」版本没有可回退的目标；正文缺失（且改动前该文件存在）也无从恢复。
    // 「改动前不存在」的版本（新建）即使没有正文也有意义：恢复它 = 删掉该文件。
    const canRestore = !v.current && (v.existedBefore ? v.hasBody !== false : true);
    if (canRestore) acts.appendChild(restBtn);
    head.appendChild(acts);
    item.appendChild(head);
    item.appendChild(bodyEl);
    if (showBtn) {
      // 整行可点：点条目空白处等同「查看内容」，按钮各自独立生效
      item.onclick = () => showBtn.click();
    }
    list.appendChild(item);
  });
}

// 版本正文面板：文本给"与当前内容的差异 + 该版本全文"，二进制只给说明。
// 版本正文缺失（.jxai/snapshots 里的文件被清掉）时只展示元数据。
function renderVersionBody(view, path, box, t) {
  box.innerHTML = '';
  const note = document.createElement('div');
  note.className = 'fv-tip';
  if (!t.hasBody) {
    note.classList.add('bad');
    note.textContent = '这个版本的正文不在快照库里（已按保留策略清理），只能看到元数据。';
    box.appendChild(note);
    return;
  }
  if (t.existedBefore === false) {
    note.textContent = '这个版本之前该文件不存在（本次改动是新建）。';
    box.appendChild(note);
    return;
  }
  if (t.binary) {
    note.textContent = '这个版本是二进制内容（' + fmtBytes(t.size || 0) + '），不展示文本。';
    box.appendChild(note);
    return;
  }
  if (t.truncated) note.textContent = '内容过长，仅显示前 ' + fmtBytes(t.size || 0) + '。';
  else note.textContent = '改动前内容（' + fmtBytes(t.size || 0) + '）：';
  box.appendChild(note);
  const pre = document.createElement('pre');
  const cur = t.current || {};
  // 当前内容也是文本时给"行级差异 + 版本全文"，一眼看出版本与现状差在哪
  pre.textContent = (cur.exists && !cur.binary && typeof cur.text === 'string')
    ? diffBody(t.text, cur.text)
    : String(t.text ?? '');
  box.appendChild(pre);
}

// 文件改动面板：一回合内动过的文件（自动快照 + 手动 snapshot_file 都算），
// 折叠条显示「修改 N / 新增 M」，整条折叠行可点击展开/收起。折叠行上的
// 「全部撤回」作用于本批全部文件；清单里每行文件另有两个互相独立的按钮：
// 「撤回」（只回退这一行文件，仍限定在本批 seqs 内）与「历史版本」（列出
// 该文件此前的快照版本，可查看内容、恢复到某个版本之前）。
// 字号一律走 .msg-col 继承来的 --chat-fs（设置里的「字体大小」），本面板
// 不再写死字号：内部只用 em 相对值，跟随设置一起变化。
function fileChangesPanel(cps, seqs, view) {
  // 同文件多次改动只显示一行，类型按最早一条（改动前是否存在）
  const byPath = new Map();
  for (const c of cps) {
    if (!byPath.has(c.path)) byPath.set(c.path, c);
  }
  const modified = [], added = [];
  for (const c of byPath.values()) (c.existedBefore ? modified : added).push(c);
  const sec = document.createElement('div');
  sec.className = 'proc-row file-panel';
  const head = document.createElement('div');
  head.className = 'proc-head';
  const label = '文件改动 · 修改 ' + modified.length + ' · 新增 ' + added.length;
  head.innerHTML = chev('p-chev') + '<span class="p-name">' + label + '</span>' +
    '<span class="p-sub">· 可撤回</span>';
  // 整条折叠行上的总按钮：作用于本批全部文件（按轮撤回）
  const undo = document.createElement('button');
  undo.type = 'button';
  undo.className = 'fp-undo';
  undo.textContent = '全部撤回';
  undo.title = '把这批全部文件改动恢复到改动之前（对话记录不变）';
  undo.onclick = async (e) => {
    e.stopPropagation();
    const detail = modified.map((c) => '恢复 ' + c.path).concat(added.map((c) => '删除 ' + c.path)).join('\n');
    if (!(await confirmCenter('撤回本批全部文件改动？\n' + detail + '\n（已按快照校验，外部改过的文件会被拒绝）'))) return;
    await fileRewind(view, { seqs }, undo, '全部撤回');
  };
  head.appendChild(undo);
  const body = document.createElement('div');
  body.className = 'proc-body';
  const one = (tag, c) => {
    const row = document.createElement('div');
    row.className = 'fp-file';
    row.innerHTML = '<span class="fp-tag ' + (tag === '新增' ? 'add' : 'mod') + '">' + tag + '</span>' +
      '<span class="fp-path">' + esc(c.path) + '</span>' +
      '<span class="fp-size">' + fmtBytes(c.afterSize || 0) + '</span>';
    const acts = document.createElement('span');
    acts.className = 'fp-acts';
    const rev = document.createElement('button');
    rev.type = 'button';
    rev.className = 'fp-btn fp-revert';
    rev.textContent = '撤回';
    rev.title = '只把这个文件回退到本次改动之前（本批内的改动，对话记录不变）';
    const vers = document.createElement('button');
    vers.type = 'button';
    vers.className = 'fp-btn fp-vers-btn';
    vers.textContent = '历史版本';
    vers.title = '查看这个文件此前保存的快照版本，可对比或回退';
    acts.appendChild(rev);
    acts.appendChild(vers);
    row.appendChild(acts);
    const vbox = fileVersionBox(view, c.path);
    rev.onclick = async (e) => {
      e.stopPropagation();
      if (!(await confirmCenter('撤回 ' + c.path + ' 的改动？\n' +
        (c.existedBefore ? '将恢复成这次改动前的内容。' : '将删除这个文件（改动前它不存在）。') +
        '\n只影响这一个文件；对话记录不变（已按快照校验，外部改过的文件会被拒绝）。'))) return;
      await fileRewind(view, { seqs, path: c.path }, rev, '撤回 ' + c.path);
    };
    const toggleVers = async (e) => {
      if (e) e.stopPropagation();
      const open = vbox.hasAttribute('hidden');
      if (open) {
        showEl(vbox);
        vers.classList.add('on');
        if (!vbox._loaded) { vbox._loaded = true; await vbox._load(); }
      } else {
        hideEl(vbox);
        vers.classList.remove('on');
      }
    };
    vers.onclick = toggleVers;
    row.onclick = toggleVers; // 整行可点：与「历史版本」同效，展开/收起版本列表
    const wrap = document.createElement('div');
    wrap.appendChild(row);
    wrap.appendChild(vbox);
    return wrap;
  };
  modified.forEach((c) => body.appendChild(one('修改', c)));
  added.forEach((c) => body.appendChild(one('新增', c)));
  sec.appendChild(head);
  sec.appendChild(body);
  bindProcToggle(sec);
  hideEl(body);
  return sec;
}

function fmtBytes(n) {
  if (n >= 1048576) return (n / 1048576).toFixed(1) + ' MB';
  if (n >= 1024) return (n / 1024).toFixed(1) + ' KB';
  return n + ' B';
}

// 交付卡片的两个动作（用户主动操作，不经模型权限门控）。「打开」按文件类型
// 分三路（设置·常规「交付文件打开方式」可整体改回系统应用）：
//   * 代码/文本/图片/PDF → 右栏「文件」查看页（openInFileViewer，/api/fileview
//     自包含页：等宽+行号，桌面/网页/远程三端一致——不依赖原生面板 overlay）；
//   * 网页（html/htm）→ 右栏「浏览器」（桌面=原生 WebView2 面板，网页=新标签）；
//   * 其余（Word/Excel/压缩包/exe 等）→ 系统默认程序（远程会话没有本机路径，
//     尽力进「文件」查看页，二进制会给引导提示）。
// 「在文件夹中查看」= 资源管理器里定位并选中该文件（远程不可用，给直白提示）。

// 走「文件」查看页的后缀：文本/代码 + 图片 + PDF（查看页能渲染的全部）
const FILE_VIEWER_EXT = new Set(['txt', 'md', 'markdown', 'log', 'csv', 'tsv',
  'json', 'xml', 'yaml', 'yml', 'toml', 'ini', 'conf', 'cfg', 'properties', 'env',
  'js', 'mjs', 'cjs', 'ts', 'jsx', 'tsx', 'css', 'scss', 'less', 'go', 'py', 'rb',
  'rs', 'java', 'c', 'h', 'cpp', 'hpp', 'cs', 'php', 'swift', 'kt', 'sh', 'bat',
  'cmd', 'ps1', 'sql', 'lua', 'r', 'vue', 'http', 'diff', 'patch',
  'png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'svg', 'ico', 'pdf']);
// 走右栏「浏览器」的后缀：真网页（原生面板/新标签里的完整浏览体验）
const BROWSER_PANEL_EXT = new Set(['html', 'htm']);

function fileExtOf(p) {
  const m = String(p).toLowerCase().match(/\.([a-z0-9]+)$/);
  return m ? m[1] : '';
}

// 三路路由：'file'（查看页）/ 'panel'（浏览器）/ 'shell'（系统应用）。
// 用户在设置里选了「系统默认应用」时本地一律 shell；远程会话没有系统应用，
// 始终尽力进查看页（html 除外——走浏览器流式渲染）。
function deliveryOpenRoute(view, path) {
  const cur = view === subView ? subState.cur : state.cur;
  const remote = !!(cur && cur.remote);
  if (!remote && lsGet('jx.fileopen', 'panel') === 'shell') return 'shell';
  const ext = fileExtOf(path);
  if (BROWSER_PANEL_EXT.has(ext)) return 'panel';
  if (FILE_VIEWER_EXT.has(ext)) return 'file';
  return remote ? 'file' : 'shell';
}

function deliveryOpenFile(view, sid, d) {
  const route = deliveryOpenRoute(view, d.path);
  if (route === 'file') { openInFileViewer(sid, d.path); return Promise.resolve(); }
  if (route === 'panel') { openInPanelBrowser({ session: sid, path: d.path }); return Promise.resolve(); }
  return api('/api/sessions/' + encodeURIComponent(sid) + '/delivery/open', {
    method: 'POST', body: JSON.stringify({ path: d.path }),
  }).catch((e) => alert('打开失败：' + e.message));
}

function deliveryRevealFile(view, sid, d) {
  const cur = view === subView ? subState.cur : state.cur;
  if (cur && cur.remote) { alert('这是远程工作区里的文件，本机没有对应路径，无法在文件夹中定位。'); return Promise.resolve(); }
  return api('/api/sessions/' + encodeURIComponent(sid) + '/delivery/reveal', {
    method: 'POST', body: JSON.stringify({ path: d.path }),
  }).catch((e) => alert('打开所在文件夹失败：' + e.message));
}

// 交付成果卡片：deliver_result 登记的最终交付文件。每行两个动作（「打开」与
// 「在文件夹中查看」），整行都是点击热区——点行内任何位置（含空白）都等同
// 「打开」该文件，悬停有高亮反馈。不再提供「下载」按钮。
function deliveryCard(dvs, sid, view) {
  view = view || mainView;
  const card = document.createElement('div');
  card.className = 'delivery-card';
  const head = document.createElement('div');
  head.className = 'dc-head';
  head.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 8v13H3V8"/><path d="M1 3h22v5H1z"/><path d="M10 12h4"/></svg>' +
    '<span class="dc-title">交付成果</span><span class="dc-count">' + dvs.length + ' 个文件</span>';
  card.appendChild(head);
  dvs.forEach((d) => {
    const row = document.createElement('div');
    row.className = 'dc-file';
    row.dataset.path = d.path; // 右键菜单取路径用（app-ctxmenu.js）
    row.title = d.path + '\n点击打开这个文件';
    const nm = document.createElement('span');
    nm.className = 'dc-name';
    nm.title = d.path;
    nm.textContent = d.path.split('/').pop();
    row.appendChild(nm);
    if (d.note) {
      const note = document.createElement('span');
      note.className = 'dc-note';
      note.textContent = d.note;
      row.appendChild(note);
    }
    const acts = document.createElement('span');
    acts.className = 'dc-acts';
    const route = deliveryOpenRoute(view, d.path);
    const ov = document.createElement('button');
    ov.type = 'button';
    ov.className = 'dc-btn';
    ov.textContent = '打开';
    ov.title = route === 'file' ? '在右侧「文件」查看页打开（代码样式；设置·常规可改）'
      : route === 'panel' ? '在右侧「浏览器」面板中打开（设置·常规可改为系统应用）'
        : '用系统默认程序打开这个文件';
    const rv = document.createElement('button');
    rv.type = 'button';
    rv.className = 'dc-btn';
    rv.textContent = '在文件夹中查看';
    rv.title = '在文件资源管理器中定位这个文件';
    ov.onclick = (ev) => { ev.stopPropagation(); deliveryOpenFile(view, sid, d); };
    rv.onclick = (ev) => { ev.stopPropagation(); deliveryRevealFile(view, sid, d); };
    acts.appendChild(ov);
    acts.appendChild(rv);
    row.appendChild(acts);
    // 整行可点击（含行内空白）：与「打开」等效
    row.onclick = () => deliveryOpenFile(view, sid, d);
    card.appendChild(row);
  });
  return card;
}

/* 聊天链接 → 右栏浏览器打开（桌面=原生 WebView2 面板，人机共看）。
 * 用户主动操作，不经模型权限门控；后端 open-view 会把内嵌形态设为工具作用
 * 目标（用户开了「操作浏览器」能力后，模型的 browser_screenshot/browser_content
 * 看到的就是这页）并注入【网页查看】回报，模型由此知道用户在看什么。
 * 网页模式没有原生面板：url 新标签打开，工作区文件走流式渲染端点新标签打开。 */
async function openInPanelBrowser(body) {
  const desktop = document.documentElement.classList.contains('desktop-shell');
  if (!desktop) {
    if (body.url) { window.open(body.url, '_blank'); return; }
    if (body.path && body.session) {
      const vurl = '/api/browser/view?sid=' + encodeURIComponent(body.session) +
        '&path=' + encodeURIComponent(body.path);
      if (window.JX_REMOTE) { JX_REMOTE.openURL(vurl); return; } // 远程模式：隧道取 blob 再开
      window.open(vurl, '_blank');
      return;
    }
  }
  try {
    await api('/api/browser/open-view', { method: 'POST', body: JSON.stringify(body) });
  } catch (e) {
    if (body.url) window.open(body.url, '_blank'); // 打不开就退回新标签打开
    else alert(e.message); // 后端错误已带「打开失败: 」前缀，不重复包
    return;
  }
  rpOpen('browser'); // 面板未开自动开；已开只是切页签（不重播动画）
  setTimeout(() => { if (bpwDesktop() && bpwActive()) bpwReport(true, true); }, 350); // 开合动画落定后强制补报（自愈，不吃去重）
}

// 委托接管（主对话/子对话通用）：左键点 ext-link 在右栏打开；Ctrl/中键等放行默认
document.addEventListener('click', (e) => {
  const a = e.target.closest && e.target.closest('a.ext-link');
  if (!a || e.ctrlKey || e.metaKey || e.shiftKey || e.altKey || e.button !== 0) return;
  e.preventDefault();
  openInPanelBrowser({ url: a.getAttribute('href') });
});

// 文件路径链接（file-link，正文/反引号里的路径渲染而来）：按消息所在视图
// 解析出会话，右栏预览该文件——代码/文本/图片/PDF 进「文件」查看页（三端
// 一致），网页进「浏览器」，其余也交给查看页（二进制给引导提示）。
// 后端边界=会话工作区内已存在的文件，越界/不存在给明确报错。
document.addEventListener('click', (e) => {
  const a = e.target.closest && e.target.closest('a.file-link');
  if (!a || e.ctrlKey || e.metaKey || e.shiftKey || e.altKey || e.button !== 0) return;
  e.preventDefault();
  const path = a.getAttribute('data-path');
  const view = a.closest('#sub-msg-col') ? subView : mainView;
  const sid = view.state.cur && view.state.cur.id;
  if (!sid || !path) return;
  if (deliveryOpenRoute(view, path) === 'panel') openInPanelBrowser({ session: sid, path });
  else openInFileViewer(sid, path);
});

/* 渲染契约（性能与滚动）：
   - 前缀复用：最后一个真实用户消息之前的所有回合（含继承段、文件改动面板、
     交付卡）只在结构变化时整体重建；流式期间的 message 事件只重建尾部
     （最后一回合 + 待生效插话），前缀的折叠状态与阅读位置不被整列重渲冲掉。
   - 跟随滚动：默认贴底跟随输出；用户上翻即「钉住」阅读位置，流式增量与
     消息事件都不再拽回，滚回底部或点「回到底部」才恢复跟随；jump=true
     （打开会话/首次渲染）强制到底。 */

// 尾部 = 从最后一个真实用户消息起的回合；没有真实用户消息则返回 -1（全算尾部）
function lastTurnStart(msgs) {
  for (let i = msgs.length - 1; i >= 0; i--) {
    const m = msgs[i];
    if (m.role === 'user' && !isSysNote(String(m.content ?? ''))) return i;
  }
  return -1;
}

// 跟随滚动（钉住式）：默认贴底跟随输出；用户上翻（滚轮/触摸/拖滚动条，经
// setupScrollTuck 钉住）后 view._pinned=true，之后的跟随一律让路，直到用户
// 滚回底部（或点「回到底部」）恢复。jump=true 强制到底并解除钉住。
// 旧版只按「距底 <120px 才跟随」判断：流式输出长得快，滚轮一格约 100px，
// 一步逃不出 120px 窗口就被拽回——表现就是「往上滑不动、总被拉到最底下」。
const FOLLOW_PIN_DIST = 120; // 滚动位置离底超过此距 → 兜底钉住（拖滚动条/键盘翻页）
const FOLLOW_RESUME = 48;    // 滚回离底小于此距 → 恢复跟随
const PIN_GUARD_MS = 350;    // 手势钉住后的短窗口：其间的小距滚动不算「滚回底部」

function followScroll(view, jump) {
  const el = view.scroll;
  if (jump) { view._pinned = false; el.scrollTop = el.scrollHeight; return; }
  if (view._pinned) return; // 钉住阅读位置：不跟随
  el.scrollTop = el.scrollHeight;
}

// 「发送中」占位气泡：点发送到服务端 message 事件回显之间（本机后端极快，
// 但经网关/手机网络远程访问时可达数秒）用户消息处于「输入框已清、对话区没影」
// 的空窗，看起来像没发出去。先按插话同款虚线气泡立显，回显落地即被正常气泡
// 替换；整轮失败则连内容一起退回输入框（见 send 的 catch）。
function pendingSendBubble(content) {
  const wrap = document.createElement('div');
  wrap.className = 'msg user pending-steer';
  const b = document.createElement('div');
  b.className = 'bubble';
  const badge = document.createElement('span');
  badge.className = 'q-badge q-badge-steer';
  badge.textContent = '发送中';
  b.appendChild(badge);
  b.appendChild(document.createTextNode(content));
  wrap.appendChild(b);
  return wrap;
}

// 当前视图是否有待回显的发送占位：带会话 id 自校验（切走后旧条自失效），
// 且服务端历史末尾已是同文用户消息时不再画（attached 模式整体重取后防重）
function pendingSendOf(st, msgs, sid) {
  const ps = st.pendingSend;
  if (!ps || ps.sid !== sid) return null;
  const lastM = msgs[msgs.length - 1];
  if (lastM && lastM.role === 'user' && String(lastM.content ?? '') === ps.content) return null;
  return ps;
}

// 渲染单个回合（用户气泡 + 过程折叠 + 完成态收尾件）；isLast 决定是否走流式尾部窗口
function appendTurn(target, turn, key, view, isLast) {
  if (turn.sysHead) target.appendChild(sysHeadRow(turn.sysHead, turn.sysHeadSeq));
  if (turn.headTools) target.appendChild(toolsRow(turn.headTools, turn.headToolsSeq));
  if (turn.userMsg) target.appendChild(userBubble(turn.userMsg, view));
  if (isLast && view.state.streaming) {
    renderStreamingTurn(target, turn, key);
    return;
  }
  renderFinishedTurn(target, turn, key);
  const seqSet = new Set();
  turn.items.forEach((it) => { if (it.seq != null) seqSet.add(it.seq); });
  if (!seqSet.size) return;
  const sid = (view.state.cur && view.state.cur.id) || '';
  const cps = (view.state.checkpoints || []).filter((c) => seqSet.has(c.seq));
  if (cps.length) target.appendChild(fileChangesPanel(cps, [...seqSet], view));
  const dvs = (view.state.deliveries || []).filter((d) => seqSet.has(d.seq));
  if (dvs.length) target.appendChild(deliveryCard(dvs, sid, view));
}

function renderMessages(msgs, view, opts) {
  view = view || mainView;
  const st = view.state;
  const box = view.col;
  // 会话正在乐观加载：转圈占位不被空列表渲染冲掉（事件触发的就地翻新拿到的
  // 还是清空后的 msgs）；真实数据落地时加载方先清 _loading 再来渲染
  if (st._loading && !msgs.length) return;
  if (!msgs.length) {
    // 无会话（首页）时空列表是常态：问候区由 resetEmpty 铺好且里面挂着输入卡
    // （#home-composer），这里绝不能拿 emptyHTML 顶掉它——销毁问候区等于销毁 #input
    if (!st.cur) return;
    st._pfxSid = null;
    box.innerHTML = view.emptyHTML;
    // 首条消息已发出、还没回显：空态（首页问候）下面也立显「发送中」占位
    const ps0 = pendingSendOf(st, msgs, (st.cur && st.cur.id) || '');
    if (ps0) box.appendChild(pendingSendBubble(ps0.content));
    view._pinned = false; // 空会话没有可钉住的阅读位置
    view.scroll.scrollTop = view.scroll.scrollHeight;
    return;
  }
  const jump = !!(opts && opts.jump);
  reportedSubs = scanReported(msgs); // 子任务卡片的完成态依据（渲染期间同步使用）
  const resultOf = {};
  for (const m of msgs) {
    if (m.role === 'tool' && m.tool_call_id) resultOf[m.tool_call_id] = m;
  }
  const sid = (st.cur && st.cur.id) || '';
  // 子对话开头的继承段（后端克隆父对话时写入的标记行 + N 条历史）：
  // 折叠成一条「来自主对话」折叠条，其余消息照常渲染
  let inherited = null, rest = msgs, off = 0;
  if (msgs[0].kind === 'inherit') {
    let n = 0;
    try { n = JSON.parse(String(msgs[0].content ?? '{}')).count || 0; } catch (e) { /* 坏行就当没有继承段 */ }
    inherited = msgs.slice(1, 1 + n);
    rest = msgs.slice(1 + n);
    off = 1 + n;
  }
  const kRel = lastTurnStart(rest);
  const k = kRel < 0 ? off : off + kRel; // 前缀长度：无真实用户消息时继承段之后全算尾部
  const turns = groupTurns(rest, resultOf);
  // 前缀可复用：同会话、分界没动、分界前最后一条 seq 未变、前缀 DOM 还在
  const reusable = st._pfxSid === sid && st._pfxLen === k && k > 0
    && msgs[k - 1] && msgs[k - 1].seq === st._pfxSeq
    && st._pfxNodes > 0 && box.children.length >= st._pfxNodes;
  if (!reusable) {
    box.innerHTML = '';
    if (inherited && inherited.length) box.appendChild(inheritBlock(inherited, resultOf, sid));
    if (inherited && !rest.length) {
      const empty = document.createElement('div');
      empty.innerHTML = view.emptyHTML;
      box.appendChild(empty);
    }
    turns.forEach((turn, ti) => {
      if (ti === turns.length - 1) return; // 最后一回合属尾部
      appendTurn(box, turn, sid + ':' + (turn.userMsg ? turn.userMsg.seq : 'head' + ti), view, false);
    });
    st._pfxSid = sid;
    st._pfxLen = k;
    st._pfxSeq = msgs[k - 1] ? msgs[k - 1].seq : 0;
    st._pfxNodes = box.children.length;
  }
  // 尾部每次全量重建：最后一回合（流式中走尾部窗口）+ 待生效插话。
  // 渲染前记时间戳：移除尾部的瞬间内容变矮，浏览器会把 scrollTop 钳到临时
  // 底部并发出 dist≈0 的 scroll 事件——若当作「用户滚回底部」会误解除钉住、
  // 随后跟随把阅读位置拽到底（单格上翻场景实测翻车），scroll 监听据此忽略
  view._renderTs = Date.now();
  while (box.children.length > st._pfxNodes) box.removeChild(box.lastChild);
  const tail = document.createDocumentFragment();
  const last = turns[turns.length - 1];
  if (last) {
    appendTurn(tail, last, sid + ':' + (last.userMsg ? last.userMsg.seq : 'head' + (turns.length - 1)), view, true);
  }
  // 已插话、等下一次调用模型生效的消息：直接落在对话最底下（与已发送的用户消息
  // 同一套气泡外观，只多一个「插话中」角标），落库后由 message 事件替换成正常气泡。
  // 数字助理派发注入的插话（kind=report，比如派发到正在执行的会话）例外：它是
  // 「数字助理说的话」，按 sysnote 那套左对齐通知渲染，落库后形态一致。
  (st.pendingSteer || []).forEach((pm) => {
    if (pm.kind === 'report') {
      const nw = document.createElement('div');
      nw.className = 'msg sysnote';
      nw.dataset.pendingSteer = pm.id;
      const nb = document.createElement('div');
      nb.className = 'bubble';
      nb.textContent = pm.content;
      nw.appendChild(nb);
      tail.appendChild(nw);
      return;
    }
    const wrap = document.createElement('div');
    wrap.className = 'msg user pending-steer';
    wrap.dataset.pendingSteer = pm.id;
    const b = document.createElement('div');
    b.className = 'bubble';
    const badge = document.createElement('span');
    badge.className = 'q-badge q-badge-steer';
    badge.textContent = '插话中';
    b.appendChild(badge);
    b.appendChild(document.createTextNode(pm.content));
    wrap.appendChild(b);
    // 悬停操作行：与正式用户气泡同一套样式，只有「编辑」（退回输入框改完重发）。
    // 只给自己发的插话（kind 空/chat）；派发注入的其他形态不参与。
    if (!pm.kind || pm.kind === 'chat') {
      const acts = document.createElement('div');
      acts.className = 'msg-actions user-acts';
      const ed = document.createElement('button');
      ed.className = 'act-copy act-edit';
      ed.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z"/></svg>编辑';
      ed.title = '退回输入框编辑后重新发送';
      ed.onclick = () => editPendingMsg(view, pm);
      acts.appendChild(ed);
      wrap.appendChild(acts);
    }
    tail.appendChild(wrap);
  });
  // 刚发出、等回显的「发送中」占位（最末位，晚于插话）
  const ps = pendingSendOf(st, msgs, sid);
  if (ps) tail.appendChild(pendingSendBubble(ps.content));
  box.appendChild(tail);
  followScroll(view, jump);
  if (view === mainView) scheduleFolds(); // 长消息折叠：渲染完按实际高度重算（只主对话）
}

// 已完成回合：最后一段正文 = 最终输出，常显；中间长正文与系统通知也常显；
// 上下文材料行（首轮尾部材料、运行环境更新、系统提示词/工具定义变更）跟
// 思考/工具过程一起折叠——要核对「模型看到了什么、什么时候变的」时展开折叠段；
// 其余过程按相邻常显块切分成折叠段（每段一条折叠条，显示思考/工具计数）
function renderFinishedTurn(box, turn, key) {
  const items = turn.items;
  let answerIdx = -1;
  items.forEach((it, i) => { if (it.k === 'text') answerIdx = i; });
  const anchors = new Set();
  if (answerIdx >= 0) anchors.add(answerIdx);
  items.forEach((it, i) => {
    if (i === answerIdx) return;
    if (it.k === 'note' || it.k === 'plancard' || (it.k === 'text' && isLongText(it.text))) anchors.add(i);
  });
  let seg = [];
  const flushSeg = () => {
    if (!seg.length) return;
    box.appendChild(foldSection(seg, key, { open: turnOpen[key] === true }));
    seg = [];
  };
  items.forEach((it, i) => {
    if (anchors.has(i)) { flushSeg(); box.appendChild(renderItem(it, { final: i === answerIdx })); }
    else seg.push(it);
  });
  flushSeg();
}

// 进行中回合：过程行只露最近 TAIL_ROWS 行（尾部窗口），上方折叠成一条"已折叠 N 行"折叠条；
// 运行中的子任务卡与系统通知钉住常显（实时进度/派发通知不中断）；上下文材料行落在回合
// 开头、不占窗口——与完成态一致折进折叠段（否则刚发完消息满屏都是变更行）；
// 手动展开过则全部显示
function renderStreamingTurn(box, turn, key) {
  const items = turn.items;
  const mat = items.filter(isMatItem);
  const flow = items.filter((it) => !isMatItem(it));
  const open = turnOpen[key] === true;
  if (flow.length <= TAIL_ROWS) {
    // 回合刚开头：材料行单独折成顶部一段，过程行照常显示
    if (mat.length) box.appendChild(foldSection(mat, key, { open: open }));
    flow.forEach((it) => box.appendChild(renderItem(it)));
    return;
  }
  const foldFrom = flow.length - TAIL_ROWS;
  // 钉住常显、绝不折进过程段的两类：系统通知（note＝「数字助理派发」这类灰底气泡，
  // 正在执行时它正是"这次派发到底要我做什么"的唯一线索）与运行中的子任务卡（实时进度）。
  // 完成态把两者当 anchors 常显（见 renderFinishedTurn 的 note 分支），流式这里同一口径——
  // 否则派发通知会在执行期间跟着上下文材料一起被折起来，跑完才重新露出来。
  const pinned = (it) => it.k === 'note' || it.k === 'plancard' || (it.k === 'subtask' && !it.result);
  let seg = mat.slice(); // 材料行在回合最前，并入头部折叠段
  const flush = () => {
    if (!seg.length) return;
    box.appendChild(foldSection(seg, key, {
      open: open,
      prefix: (c) => '已折叠 ' + c.rows + ' 行' + ((c.think || c.tool || c.mat) ? ' · ' + foldLabel(c) : ''),
    }));
    seg = [];
  };
  // 常显项就地渲染（必要时把折叠段切开），其余过程行折进段里；尾部窗口内一律常显
  flow.forEach((it, i) => {
    if (i >= foldFrom || pinned(it)) { flush(); box.appendChild(renderItem(it)); return; }
    seg.push(it);
  });
  flush();
}

let rafPending = false;
function liveRender(view) {
  view = view || mainView;
  if (rafPending) return;
  rafPending = true;
  requestAnimationFrame(() => {
    rafPending = false;
    let el = document.getElementById(view.liveId);
    if (!el) {
      el = document.createElement('div');
      el.className = 'msg assistant';
      el.id = view.liveId;
      el.innerHTML =
        '<div class="proc-row running open" id="' + view.liveId + '-think">' +
          '<div class="proc-head">' + chev('p-chev') + '<span class="p-name">思考中…</span></div>' +
          '<div class="proc-body"><div class="tk-text"></div></div>' +
        '</div><div class="md"></div>';
      view.col.appendChild(el);
      bindProcToggle(el.querySelector('#' + view.liveId + '-think'));
    }
    const tk = el.querySelector('.proc-row');
    const thinking = view.state.streamThinking || '';
    if (thinking) {
      tk.style.display = '';
      const tkt = tk.querySelector('.tk-text');
      tkt.textContent = thinking;
      // 内滚贴底才跟随：展开思考块往回读推理时不被每帧拽到底（外层钉住同款语义）
      if (tkt.scrollHeight - tkt.scrollTop - tkt.clientHeight < 48) tkt.scrollTop = tkt.scrollHeight;
    } else {
      tk.style.display = 'none';
    }
    el.querySelector('.md').innerHTML = md(view.state.streamText);
    followScroll(view, false); // 贴近底部才跟随：流式中上翻阅读不被拽回
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

function removeLive(view) {
  const live = document.getElementById((view || mainView).liveId);
  if (live) live.remove();
}

// 打开/切换会话的导航状态：切换在途时，旧会话事件触发的「就地翻新」要让路，
// 否则一次切换会被旧会话的刷新顶回去（表现为点了别的会话却切不动）
const navBusy = { main: false, sub: false };
// 导航令牌：每次切换/回首页自增，响应落地前对不上令牌即丢弃。
// 只靠 navBusy 挡不住「晚到的旧响应反超新导航」——完成事件触发的重载与用户
// 点击并发时，旧的大响应可能后到获胜，把界面顶回旧会话（实测的「输出完成后
// 被强切回完成的对话」即此）。令牌保证永远以最后一次导航为准。
const navTok = { main: 0, sub: 0 };

function refreshCurrent() {
  if (navBusy.main) return; // 正在切换：别插队
  if (state.cur && !state.streaming) openSession(state.cur.id);
}

function refreshCurrentSub() {
  if (navBusy.sub) return;
  if (subState.cur && !subState.streaming) openSubSession(subState.cur.id);
}

function handleGlobalEvent(ev) {
  const viewing = state.cur && ev.sessionId === state.cur.id;
  // 子对话视图并行接收同一事件流：右栏正看这个子对话且它没有本地流在跑（后台自动汇报）
  const subViewing = subState.cur && ev.sessionId === subState.cur.id;
  // 侧栏「⏸ 待确认」标记与橙点直接由 state.approvals / state.questions 推导（见
  // app-left.js 的 sessionPendingAsk）：下面的 push/remove 更新清单后 renderPanels
  // 会顺带刷新会话列表，不再单独维护事件驱动的 dotApproval 影子表。
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
      if (subViewing && !subState.streaming) refreshCurrentSub(); // 右栏子对话就地翻新
      // 主对话区正显示派发它的那个会话（子任务卡在场）才翻新卡片状态；
      // 否则不动——不冲掉别的会话/首页的界面，也不拽走阅读位置
      if (state.cur && !state.streaming && subActNode(ev.sessionId)) renderMessages(state.msgs);
      break;
    case 'message':
      // 后台落库的用户消息（todo.go 广播）与环境快照行（权限等 PATCH 后立即
      // 落库的 env 行，role=system）：正看着这个会话就整体翻新
      if (viewing && !state.streaming && ev.message && (ev.message.role === 'user' || ev.kind === 'env')) refreshCurrent();
      break;
    case 'pending':
      // 插话/排队队列变化：只更新对应的那个视图（自己窗口的操作也会推回来，天然多端一致）
      if (state.cur && ev.sessionId === state.cur.id) applyPending(mainView, ev.pending);
      if (subState.cur && ev.sessionId === subState.cur.id) applyPending(subView, ev.pending);
      break;
    case 'stopped':
      // 用户显式停止（runtime.go 终态广播）：不算「完成未查看」，只刷列表
      loadSessions();
      if (viewing && !state.streaming) refreshCurrent();
      break;
    case 'done':
      // 一轮执行结束（含数字助理的自动汇报轮）：没正看着这个会话就点亮绿点，
      // 打开会话即清；正看着则就地翻新出最终结果
      if (!viewing) dotDone[ev.sessionId] = true;
      loadSessions();
      refreshDrawer();
      if (viewing && !state.streaming) refreshCurrent();
      if ((state.sessions.find((s) => s.id === ev.sessionId) || {}).mode === 'assistant') setFace('done', 4000);
      break;
    case 'report_ready':
      // 子智能体/派发任务的回报已落库（数字助理与普通任务派发同走这里）：
      // 没正看着父会话就点亮「完成未查看」绿点；正看着则整体翻新，
      // 并自动接上服务端随回报启动的汇报轮，实时看到汇报输出
      if (!viewing) dotDone[ev.sessionId] = true;
      loadSessions();
      refreshDrawer();
      if (viewing && !state.streaming) refreshCurrent();
      break;
    case 'session_updated':
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
    case 'children':
      // 本会话有子进程起/停：刷新进度面板底部的「运行中的程序」
      loadProgress();
      break;
    case 'progress':
      loadProgress();
      break;
    case 'update_available':
      // 门铃事件：拉全量信息（含安装包直链）渲染更新横幅
      api('/api/status').then((st) => {
        if (st.update && st.update.available) showUpdateBanner(st.update);
      }).catch(() => {});
      break;
    // 浏览器面板（桌面原生 WebView2）：browser_nav 同步工具栏地址/标题
    // （AI 在面板未开时也会导航），browser_resize 是 AI 的 viewport 请求
    // （X/Y 复用为宽高），两个处理函数都在 app-right.js。nav 事件顺带强制
    // 重报矩形：面板曾因矩形上报失联被留在隐藏态（地址已填、舞台空白），
    // 每次导航到达都补一次上报，让面板自愈显示
    case 'browser_nav':
      bpwOnNav(ev);
      if (typeof bpwDesktop === 'function' && bpwDesktop() && typeof bpwActive === 'function' && bpwActive()) bpwReport(true, true);
      break;
    case 'browser_resize':
      bpwApplyViewport(ev.x);
      break;
    // 说明：delta/reasoning 不上全局总线（流式增量只走会话自己的 SSE）；
    // 真实失败也不广播——错误经会话详情的 error 字段到达
    // （session_updated → refreshCurrent → openSession 会带出报错条）
  }
}

/* ---------- 待用户决定的浮窗（审批 / 提问 / 计划审批）：两个落点 ----------
   按「这张卡属于谁」分两处落，各司其职：

   1) 本对话的卡 → 输入框上方（`#ask-bar` 主对话 / `#sub-ask-bar` 右栏子对话）：
      位置在输入卡之上、附件区之上，宽度与输入卡一致（max-width: --content-w），
      就地处理最顺手；手机端有待办时输入区不收起（否则卡跟着滑出屏）。
   2) 别的对话的卡 → 右下角角标堆（`#ask-dock`）：卡片顶部多一行「来自「某对话」」
      （点一下切到那个对话），一叠攒在角落互不打扰当前对话；顶部「—」按钮把它整块
      缩成一枚胶囊（只显示条数，点胶囊再展开），用户回头再处理。左侧栏的橙点
      照旧提示哪个会话在等。

   另外两条（两处共用）：
   · 重绘不动的字：轮询/事件重画按「卡片签名」比对——签名没变的卡片一个节点都不碰，
     内容真的变了才重建那一张；自由输入框的文字按请求 id 存草稿并回填。这正是
     「隔十几秒清空用户输入」的根治点（原来的实现每次都把容器 innerHTML 清空重画）。
   · 计划（kind=plan）不在这里铺长文：卡片只给标题 + 首段 + 三个动作，全文在右侧
     边栏的「计划」视图里按 Markdown 渲染，且那份文档与对话历史同寿命（见下节）。 */

const askCards = { main: new Map(), sub: new Map(), dock: new Map() }; // 落点 → (卡片 key → {sig, el})
const planAutoOpened = {}; // 计划提问 id → 已自动打开过右栏计划视图（用户手动关掉后不再自动弹）

// 自由输入框草稿：按请求 id 存（sessionStorage：刷新页面也还在，关标签页即清）
function askDraftKey(id) { return 'jx.ask.' + id; }
function askDraftGet(id) {
  try { return sessionStorage.getItem(askDraftKey(id)) || ''; } catch (e) { return ''; }
}
function askDraftSet(id, v) {
  try { if (v) sessionStorage.setItem(askDraftKey(id), v); else sessionStorage.removeItem(askDraftKey(id)); } catch (e) {}
}
function askDraftClear(id) { try { sessionStorage.removeItem(askDraftKey(id)); } catch (e) {} }

// 字符串指纹：卡片签名的正文部分（计划全文不进签名的明文，只进哈希）
function askHash(s) {
  let h = 5381;
  for (let i = 0; i < s.length; i++) h = ((h << 5) + h + s.charCodeAt(i)) | 0;
  return h;
}

// 会话标题：后端随请求下发的最准（会话可能已归档、不在当前清单里），再退到会话清单
function sessionTitleOf(sid, hint) {
  if (hint) return hint;
  const s = (state.sessions || []).find((x) => x.id === sid);
  if (s && s.title) return s.title;
  if (state.cur && state.cur.id === sid && state.cur.title) return state.cur.title;
  if (subState.cur && subState.cur.id === sid && subState.cur.title) return subState.cur.title;
  return '另一个对话';
}

// 「来自「某对话」」：别的对话的卡靠上位置的一行来源，点一下切到那个对话
// 样式约定（用户口径）：卡片左边条与本对话的卡同色（主题蓝），来源行里
// 「来自「…」」不加粗、右侧「点击跳转 ›」加粗——见 style.css 的 .ap-src 一段
function askSrcStrip(sid, hint) {
  const b = document.createElement('button');
  b.type = 'button';
  b.className = 'ap-src';
  b.title = '这条请求属于另一个对话：点一下切过去（不点也能就地处理）';
  b.innerHTML =
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' +
    '<path d="M21 11.5a8.4 8.4 0 0 1-8.5 8.3 9 9 0 0 1-3.4-.6L4 21l1.4-4a8.2 8.2 0 0 1-1.4-4.5A8.4 8.4 0 0 1 12.5 4 8.4 8.4 0 0 1 21 11.5z"/></svg>' +
    '<span class="ap-src-title">来自「' + esc(sessionTitleOf(sid, hint)) + '」</span>' +
    '<span class="ap-src-go">点击跳转 ›</span>';
  b.onclick = () => askJumpTo(sid);
  return b;
}

// 切到那条请求所属的对话（就在本对话时只是把焦点还给输入框）
function askJumpTo(sid) {
  if (!sid) return;
  if (state.cur && state.cur.id === sid) { $('input').focus(); return; }
  openSession(sid);
}

// 自由输入行：值存草稿、重建后回填（卡片的「发送/驳回/拒绝」都走它）
function askFreeRow(id, placeholder, btnText, onSend) {
  const row = document.createElement('div');
  row.className = 'q-free';
  row.onclick = (e) => e.stopPropagation(); // 输入区不触发整卡点击（计划卡：整张卡点开计划）
  const input = document.createElement('input');
  input.type = 'text';
  input.placeholder = placeholder;
  input.value = askDraftGet(id);
  input.dataset.draftkey = id;
  input.addEventListener('input', () => askDraftSet(id, input.value));
  const send = () => {
    const v = input.value.trim();
    askDraftSet(id, input.value);
    onSend(v);
  };
  const btn = document.createElement('button');
  btn.className = 'ghost';
  btn.textContent = btnText;
  btn.onclick = send;
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.isComposing) { e.preventDefault(); send(); }
  });
  row.appendChild(input);
  row.appendChild(btn);
  return row;
}

/* ---------- 对话流里的计划卡（exit_plan_mode 提交过的计划） ----------
   输入区上方那张审批卡批了/驳了就收走了，但计划本身得在对话里留个常住的入口：
   这条卡跟着提交它的那一轮常显（不会被折进过程段），点一下就再打开右栏「计划」
   视图看全文——刷新、切走、关掉软件重开，它都还在（正文从会话历史里读回）。 */

// 这份计划还在等审批吗：按正文与 /api/questions 里 kind=plan 的提问配对
// （审批卡与计划卡拿的是同一份正文），配得上就给「待审批」徽章。
function planPendingIdOf(plan) {
  const head = String(plan || '').trim().replace(/…$/, '');
  if (!head) return '';
  const q = (state.questions || []).find((x) => x.kind === 'plan'
    && (x.plan === plan || String(x.plan || '').startsWith(head)));
  return q ? q.id : '';
}

// 计划卡：标题 + 首段 + 「查看计划」（打开右栏并定位到这一份）。正文取不到
// （参数是坏行）时退回普通工具行，别造一张空卡。
function planCardRow(tc, seq, result) {
  let plan = '';
  try { plan = String(JSON.parse((tc && tc.function && tc.function.arguments) || '{}').plan || '').trim(); } catch (e) { plan = ''; }
  if (!plan) return toolRow(tc, result, { seq });
  const info = planSummaryOf(plan);
  const pendingId = planPendingIdOf(plan);
  const sid = (result && result.sessionId) || (state.cur && state.cur.id) || '';
  const card = document.createElement('div');
  card.className = 'plan-card' + (pendingId ? ' pending' : '');
  if (seq != null) card.dataset.seq = seq;
  const head = document.createElement('div');
  head.className = 'pc-head';
  head.innerHTML = '<b>计划已提交</b>' +
    (pendingId ? '<span class="pc-badge">待审批</span>' : '');
  const body = document.createElement('div');
  body.className = 'pc-body';
  body.innerHTML = '<p class="pc-title">' + esc(info.title) + '</p>' +
    (info.desc ? '<p class="pc-desc">' + esc(info.desc) + '</p>' : '');
  const acts = document.createElement('div');
  acts.className = 'pc-acts';
  acts.textContent = pendingId ? '等待你的审批 · 点击这张卡看计划全文' : '点击这张卡看计划全文';
  card.appendChild(head);
  card.appendChild(body);
  card.appendChild(acts);
  card.onclick = () => openPlanView(sid, seq); // 整张卡可点：点哪儿都能打开那份计划
  card._plan = plan; // 徽章翻新用（refreshPlanCards 据此重算「待审批」）
  return card;
}

// 计划摘要（输入区那张卡只显示这一段，全文去右栏看）
function planTitleOf(md) {
  const m = /^#{1,6}\s+(\S.*)$/m.exec(String(md || '').trim());
  if (m) return m[1].trim().slice(0, 80);
  const first = String(md || '').trim().split(/\r?\n/)[0] || '';
  return first.replace(/[#*`>\s]+$/, '').slice(0, 80) || '计划';
}
function planSummaryOf(md) {
  const lines = String(md || '').split(/\r?\n/);
  let i = 0;
  while (i < lines.length && (lines[i].trim() === '' || /^#{1,6}\s/.test(lines[i]))) i++; // 跳过标题
  const para = [];
  while (i < lines.length && lines[i].trim() !== '' && !/^#{1,6}\s/.test(lines[i])) { para.push(lines[i].trim()); i++; }
  const desc = para.join(' ').replace(/\s+/g, ' ')
    .replace(/^[-*+]\s+/, '').replace(/\*\*/g, '').replace(/`/g, '').trim();
  return { title: planTitleOf(md), desc: desc.length > 180 ? desc.slice(0, 180) + '…' : desc };
}

/* ---------- 事件 → 状态 ---------- */

function pushApproval(ev) {
  if (!ev.approvalId || state.approvals.some((a) => a.id === ev.approvalId)) return;
  state.approvals.push({
    id: ev.approvalId, sessionId: ev.sessionId || '', sessionTitle: ev.title || '',
    tool: ev.name || '', summary: ev.summary || '', reason: ev.reason || '',
    createdAt: ev.createdAt || '',
  });
  renderPanels();
}

function removeApproval(id) {
  state.approvals = state.approvals.filter((a) => a.id !== id);
  askDraftClear(id);
  renderPanels();
}

async function loadApprovals() {
  try {
    const d = await api('/api/approvals');
    state.approvals = (d.approvals || []).map((a) => ({
      id: a.id, sessionId: a.sessionId || '', sessionTitle: a.sessionTitle || '',
      tool: a.tool || '', summary: a.summary || '', reason: a.reason || '',
      createdAt: a.createdAt || '',
    }));
    renderPanels();
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

/* ---------- 提问 / 计划审批 ---------- */

function pushQuestion(ev) {
  if (!ev.questionId || state.questions.some((q) => q.id === ev.questionId)) return;
  state.questions.push({
    id: ev.questionId, sessionId: ev.sessionId || '', sessionTitle: ev.title || '',
    kind: ev.kind || 'ask', question: ev.summary || '', plan: ev.plan || '',
    options: ev.options || [],
  });
  renderPanels();
  noteFreshPlanQuestions(); // 新到达的计划：右栏切到这一份（别停在上次看的那版）
  maybeAutoOpenPlan(); // 计划：右栏「计划」视图自动打开（本对话的才自动开）
}

function removeQuestion(id) {
  const q = state.questions.find((x) => x.id === id);
  if (q && q.kind === 'plan') stashPlanDoc(q); // 提问没了，计划本身要留在右栏（持久化）
  state.questions = state.questions.filter((x) => x.id !== id);
  askDraftClear(id);
  renderPanels();
}

async function loadQuestions() {
  try {
    const d = await api('/api/questions');
    state.questions = (d.questions || []).map((q) => ({
      id: q.id, sessionId: q.sessionId, sessionTitle: q.sessionTitle || '',
      kind: q.kind || 'ask', question: q.question || '', plan: q.plan || '', options: q.options || [],
    }));
    renderPanels();
    noteFreshPlanQuestions(); // 新到达的计划（含重启后拉回的待审批）：右栏切到这一份
    maybeAutoOpenPlan();
  } catch (e) { /* 服务离线时忽略 */ }
}

async function decideQuestion(id, answer) {
  const q = state.questions.find((x) => x.id === id);
  if (q && q.kind === 'plan') stashPlanDoc(q); // 先留一份副本，批/驳之后右栏仍看得到
  // 「忽略计划」= 搁置：后端要保留这份计划（它仍在等处置），所以这里不做乐观移除
  // ——移除早了会让用户以为计划没了；批/驳才移除（后端也会确认）。
  if (answer !== '__ignore__') removeQuestion(id); // 先乐观移除，避免双击
  try {
    await api('/api/questions/' + id, { method: 'POST', body: JSON.stringify({ answer }) });
  } catch (e) {
    $('hint').textContent = '提交失败：' + e.message;
    setTimeout(() => { $('hint').textContent = ''; }, 3000);
  }
  if (q && q.kind === 'plan') loadPlanView(); // 历史里落的正文接上来（替换临时副本）
}

/* ---------- 卡片与重绘 ---------- */

// 右栏正显示着的子对话（右栏开着、且停在子对话视图）：它自己的待办卡在右栏输入区
// 上方显示，主对话区那份就不再重复；右栏一关/切走，主对话区立刻接过来显示。
function subVisibleSid() {
  const panel = $('rpanel');
  if (!panel || panel.classList.contains('hidden')) return '';
  if (rpActive !== 'subs') return '';
  return (subState.cur && subState.cur.id) || '';
}

// 本视图要显示的待办（只有「本对话自己的」）：
// · 主对话区＝当前会话的卡；右栏子对话＝它自己的卡。
// 别的对话的卡一律进右下角的角标堆（renderAskDock），不再混进输入框上方——
// 本对话的卡贴在输入区上方「就地处理」，别的对话的卡攒在角落「回头处理」。
function ownAskItems(view) {
  const own = ((view.state.cur || {}).id) || '';
  if (!own) return { own, list: [] };
  const list = [];
  for (const ap of state.approvals) if (ap.sessionId === own) list.push({ type: 'approval', id: ap.id, it: ap });
  for (const q of state.questions) if (q.sessionId === own) list.push({ type: 'question', id: q.id, it: q });
  return { own, list };
}

// 不属于任何「正开着的对话」的待办：主对话与右栏子对话都占着的那两个会话要排除，
// 免得同一张卡在两处出现。
function otherAskItems() {
  const mainSid = (state.cur && state.cur.id) || '';
  const subSid = subVisibleSid();
  const list = [];
  for (const ap of state.approvals) {
    if (ap.sessionId !== mainSid && ap.sessionId !== subSid) list.push({ type: 'approval', id: ap.id, it: ap });
  }
  for (const q of state.questions) {
    if (q.sessionId !== mainSid && q.sessionId !== subSid) list.push({ type: 'question', id: q.id, it: q });
  }
  return list;
}

function askCardKey(e) { return (e.type === 'approval' ? 'a:' : 'q:') + e.id; }

// 卡片签名：只跟「渲染出来的内容」有关。签名一致 → 原节点原样留着，输入/焦点/滚动都不动
function askSigOf(e, own) {
  const it = e.it;
  const src = it.sessionId === own ? '' : sessionTitleOf(it.sessionId, it.sessionTitle);
  return [
    e.type, e.id, src, it.kind || '', it.tool || '',
    it.summary || '', it.reason || '', it.question || '',
    it.plan ? it.plan.length + '.' + askHash(it.plan) : '',
    (it.options || []).join('\u0001'),
  ].join('\u0002');
}

function askPending(view) {
  const bar = viewEl(view, 'ask-bar');
  return !!bar && !bar.classList.contains('hidden');
}

function buildApprovalCard(e, own) {
  const ap = e.it;
  const card = document.createElement('div');
  card.className = 'ap-card' + (ap.sessionId === own ? '' : ' from-other');
  if (ap.sessionId !== own) card.appendChild(askSrcStrip(ap.sessionId, ap.sessionTitle));
  const head = document.createElement('div');
  head.className = 'ap-head';
  head.innerHTML = '<b>需要批准</b>' + (ap.tool ? '<span>' + esc(ap.tool) + '</span>' : '');
  card.appendChild(head);
  const body = document.createElement('div');
  body.className = 'ap-body';
  body.innerHTML = '<code>' + esc(ap.summary || '') + '</code>' +
    (ap.reason ? '<p>' + esc(ap.reason) + '</p>' : '');
  card.appendChild(body);
  const acts = document.createElement('div');
  acts.className = 'ap-actions';
  acts.innerHTML =
    '<button class="ok" data-act="ok">允许</button>' +
    '<button class="ok always" data-act="always" title="常设授权：命令升级点亮对应能力开关；工作区外写入会把会话切到「完全访问」（三项能力捆绑点亮），此后不再逐次审批">始终允许</button>' +
    '<button class="deny" data-act="deny">拒绝</button>';
  acts.querySelectorAll('button').forEach((b) => {
    b.onclick = () => decideApproval(ap.id, b.dataset.act !== 'deny', b.dataset.act === 'always');
  });
  card.appendChild(acts);
  return card;
}

function buildQuestionCard(e, own) {
  const q = e.it;
  const isPlan = q.kind === 'plan';
  const isEnter = q.kind === 'enter-plan';
  const card = document.createElement('div');
  card.className = 'ap-card q-card' + (isPlan ? ' plan' : '') + (q.sessionId === own ? '' : ' from-other');
  if (q.sessionId !== own) card.appendChild(askSrcStrip(q.sessionId, q.sessionTitle));
  const head = document.createElement('div');
  head.className = 'ap-head';
  head.innerHTML = '<b>' + (isPlan ? '计划审批' : (isEnter ? '进入计划模式？' : '助手提问')) + '</b>';
  card.appendChild(head);

  if (isPlan) {
    // 计划：这里只给摘要，全文在右栏「计划」视图（Markdown 渲染，可反复打开）
    const info = planSummaryOf(q.plan || '');
    const body = document.createElement('div');
    body.className = 'ap-body';
    body.innerHTML = '<p class="q-plan-title">' + esc(info.title) + '</p>' +
      (info.desc ? '<p class="q-plan-desc">' + esc(info.desc) + '</p>' : '') +
      '<p class="q-plan-hint">点这张卡看计划全文（右侧边栏「计划」，Markdown 渲染）：除输入框与三个按钮外，卡片任何位置都可以点</p>';
    card.appendChild(body);
    card.appendChild(askFreeRow(q.id, '反馈修改意见后点「驳回」，或直接批准', '驳回',
      (v) => decideQuestion(q.id, v || '（未填写具体意见）')));
    const acts = document.createElement('div');
    acts.className = 'ap-actions';
    // 三个动作：驳回（带反馈文本）、忽略计划（本轮就此停下、计划仍留着）、批准执行。
    const ig = document.createElement('button');
    ig.className = 'ghost';
    ig.textContent = '忽略计划';
    ig.title = '搁置这份计划：本轮对话到此结束（等同点「停止」），计划仍留在这里，随时可以再批准或驳回';
    ig.onclick = (e) => { e.stopPropagation(); decideQuestion(q.id, '__ignore__'); };
    // 整张卡可点 → 打开这份计划（输入框与按钮自己拦下冒泡，不误触）
    card.classList.add('clickable');
    card.title = '点击查看这份计划的全文';
    card.onclick = () => openPlanView(q.sessionId, 0, q.id);
    const ok = document.createElement('button');
    ok.className = 'ok';
    ok.textContent = '批准并开始执行';
    ok.onclick = (e) => { e.stopPropagation(); decideQuestion(q.id, '__approve__'); };
    acts.appendChild(ig);
    acts.appendChild(ok);
    card.appendChild(acts);
    return card;
  }

  const body = document.createElement('div');
  body.className = 'ap-body';
  body.innerHTML = '<p>' + esc(q.question || '') + '</p>';
  card.appendChild(body);
  const opts = q.options || [];
  if (!isEnter && opts.length) {
    const box = document.createElement('div');
    box.className = 'q-opts';
    opts.forEach((o) => {
      const b = document.createElement('button');
      b.className = 'opt';
      b.textContent = o;
      b.onclick = () => decideQuestion(q.id, o);
      box.appendChild(b);
    });
    card.appendChild(box);
  }
  card.appendChild(askFreeRow(
    q.id,
    isEnter ? '可填写不同意见，留空直接拒绝' : (opts.length ? '或输入自定义回答' : '输入你的回答'),
    isEnter ? '拒绝' : '发送',
    (v) => {
      if (isEnter) { decideQuestion(q.id, v || '（未填写具体意见）'); return; }
      if (!v) return;
      decideQuestion(q.id, v);
    },
  ));
  if (isEnter) {
    const acts = document.createElement('div');
    acts.className = 'ap-actions';
    const ok = document.createElement('button');
    ok.className = 'ok';
    ok.textContent = '同意，进入计划模式';
    ok.onclick = (e) => { e.stopPropagation(); decideQuestion(q.id, '__approve__'); };
    acts.appendChild(ok);
    card.appendChild(acts);
  }
  return card;
}

// 一个视图的浮窗渲染：增/删/改都只动该动的那张卡，其余节点一个都不碰
function renderAskBar(view) {
  const bar = viewEl(view, 'ask-bar');
  if (!bar) return;
  const { own, list } = ownAskItems(view);
  const cards = askCards[view.key];
  const keep = new Set();
  const els = [];
  for (const e of list) {
    const key = askCardKey(e);
    keep.add(key);
    const sig = askSigOf(e, own);
    let cur = cards.get(key);
    if (!cur) {
      cur = { sig, el: e.type === 'approval' ? buildApprovalCard(e, own) : buildQuestionCard(e, own) };
      cards.set(key, cur);
    } else if (cur.sig !== sig) {
      const el = e.type === 'approval' ? buildApprovalCard(e, own) : buildQuestionCard(e, own);
      el.dataset.fresh = '1'; // 重建的卡：草稿在 askFreeRow 里回填，值不会丢
      cur.el.replaceWith(el);
      cur = { sig, el };
      cards.set(key, cur);
    }
    els.push(cur.el);
  }
  for (const [key, cur] of [...cards]) {
    if (keep.has(key)) continue;
    cur.el.remove();
    cards.delete(key);
  }
  els.forEach((el, i) => { if (bar.children[i] !== el) bar.insertBefore(el, bar.children[i] || null); });
  bar.classList.toggle('hidden', !els.length);
  if (els.length) tuckComposer(view, false); // 有待办时手机端不收起输入区（否则连卡一起滑出屏）
}

/* ---------- 右下角角标堆（别的对话的待办：攒在对话区右侧的留白里） ----------
   与输入区上方那块的分工：本对话的卡贴输入区「就地处理」，别的对话的卡一律攒到这里。

   尺寸与生灭都由「对话区右侧还剩多少留白」决定（对齐进度面板 pgMetrics 的 auto 模型）：
   · 留白够宽（≥240px）且纵向够高（≥220px）→ 自动展开，宽度取 min(300, 留白)，高度取
     min(560, 可用高度)，绝不吃到正文列或右侧栏；
   · 展开后内容本身塞不下（卡片总高超过可用高度）→ 自动缩成胶囊（点胶囊仍可展开看，
     展开态在堆内滚动）；用户手动点开过就不再自动收，直到卡片增删或他再次收起；
   · 留白不够展开 → 只留胶囊；连胶囊都放不下（右侧栏占光、留白 <96px）→ 整块不显示
     （左侧栏的橙点照旧标出哪个会话在等）。
   窄屏（手机）没有「右侧留白」可用，退回窗口右下角、按视口宽度收窄。 */

const DOCK_W = 300;    // 角标堆最大宽度（比对话区浮窗窄一档）
const DOCK_MIN = 240;  // 自动展开的门槛：右侧留白至少放得下这么宽
const DOCK_HMIN = 220; // 纵向可用高度低于它也不展开（缩成胶囊）
const DOCK_HMAX = 560; // 展开态高度上限（再多就在堆内滚动）
const DOCK_GAP = 12;   // 与对话区右缘、窗口底缘的间距（展开与胶囊共用同一锚点）

const askDock = { min: false, seen: 0, force: false }; // min=用户收了；seen=上次条数；force=用户手动点开过

// 角标堆能用多少空间：按 #main 与消息列的实际矩形算，右栏开合、拖把手改宽、
// 窗口缩放都会自然反映进来（它们都会改这两个矩形）。
function dockMetrics() {
  const main = $('main'), col = document.querySelector('#messages .msg-col');
  if (!main || !col) return null;
  const mainRect = main.getBoundingClientRect();
  if (isNarrow()) {
    // 手机端左右栏是整屏浮层：浮层开着时不铺开（免得盖在浮层上抢空间）
    const overlay = document.body.classList.contains('sb-show') || document.body.classList.contains('rp-open');
    const fitW = window.innerWidth - 2 * DOCK_GAP;
    const fitH = Math.max(0, window.innerHeight - mainRect.top - 2 * DOCK_GAP);
    return { right: DOCK_GAP, fitW, fitH, width: Math.round(Math.min(DOCK_W, fitW)),
      height: Math.round(Math.min(DOCK_HMAX, fitH)),
      auto: !overlay && fitW >= DOCK_MIN && fitH >= DOCK_HMIN, pill: !overlay && fitW >= 96 && fitH >= 36 };
  }
  const room = Math.max(0, mainRect.right - col.getBoundingClientRect().right); // 对话区右侧留白
  const fitW = room - DOCK_GAP;
  const fitH = Math.max(0, window.innerHeight - mainRect.top - DOCK_GAP * 2);
  return {
    right: Math.round(window.innerWidth - mainRect.right + DOCK_GAP),
    fitW: Math.round(fitW), fitH: Math.round(fitH),
    width: Math.round(Math.min(DOCK_W, fitW)), height: Math.round(Math.min(DOCK_HMAX, fitH)),
    auto: fitW >= DOCK_MIN && fitH >= DOCK_HMIN, pill: fitW >= 96 && fitH >= 36,
  };
}

function renderAskDock(list) {
  const dock = $('ask-dock');
  if (!dock) return;
  const box = $('ask-dock-cards');
  const pill = $('ask-dock-pill');
  const cards = askCards.dock;
  const keep = new Set();
  const els = [];
  for (const e of list) {
    const key = askCardKey(e);
    keep.add(key);
    const sig = askSigOf(e, ''); // 角标堆里的卡一律带「来自「某对话」」来源行
    let cur = cards.get(key);
    if (!cur) {
      cur = { sig, el: e.type === 'approval' ? buildApprovalCard(e, '') : buildQuestionCard(e, '') };
      cards.set(key, cur);
    } else if (cur.sig !== sig) {
      const el = e.type === 'approval' ? buildApprovalCard(e, '') : buildQuestionCard(e, '');
      cur.el.replaceWith(el);
      cur = { sig, el };
      cards.set(key, cur);
    }
    els.push(cur.el);
  }
  for (const [key, cur] of [...cards]) {
    if (keep.has(key)) continue;
    cur.el.remove();
    cards.delete(key);
  }
  els.forEach((el, i) => { if (box.children[i] !== el) box.insertBefore(el, box.children[i] || null); });

  // 卡片增删（seen 变化）= 情况变了：不再沿用「用户手动点开」的意愿，重新按空间自动定
  if (askDock.seen !== els.length) askDock.force = false;

  const mt = dockMetrics() || { right: DOCK_GAP, width: DOCK_W, height: DOCK_HMAX, auto: false, pill: false };
  dock.style.right = mt.right + 'px';
  pill.style.right = mt.right + 'px';
  dock.style.width = mt.width + 'px';
  dock.style.height = '';          // 交给内容撑，上限用 maxHeight
  dock.style.maxHeight = mt.height + 'px';
  // 内容本身塞不下 → 自动缩成胶囊（用户手动点开过就尊重他，展开态在堆内滚动）
  const tooTall = !askDock.force && mt.auto && box.scrollHeight > mt.height + 2;
  const noRoom = !mt.auto;                        // 留白根本放不下：任何情况下都不铺开
  const folded = els.length > 0 && (askDock.min || noRoom || tooTall);
  const showPill = els.length > 0 && folded && mt.pill;
  dock.classList.toggle('hidden', !els.length || folded);
  pill.classList.toggle('hidden', !showPill);
  $('ask-dock-count').textContent = els.length ? els.length + ' 项' : '';
  $('ask-dock-pill-num').textContent = String(els.length);
  // 缩着的时候来了新的：胶囊上点一下「有新」的提示（不自动展开，别打断当前对话）
  if (askDock.seen < els.length && folded) pill.classList.add('has-new');
  if (!folded) pill.classList.remove('has-new');
  askDock.seen = els.length;
  askDock.lastW = mt.width;
  askDock.lastAuto = mt.auto;
  askDock.lastPill = mt.pill;
}

// 收起 / 展开（收起是用户的显式选择，记在内存里；展开则标记「手动点开过」）
function setAskDockMin(min) {
  askDock.min = !!min;
  askDock.force = !min;
  renderPanels();
}

// 计划卡上的「待审批」徽章随待办翻新：审批卡批了/驳了、或重启后重新拉回待审批
// 计划时，消息流里那张卡立刻跟上（不整列重渲染，阅读位置不跳）。
function refreshPlanCards() {
  document.querySelectorAll('.plan-card').forEach((card) => {
    const pendingId = planPendingIdOf(card._plan || '');
    card.classList.toggle('pending', !!pendingId);
    const head = card.querySelector('.pc-head');
    const badge = card.querySelector('.pc-badge');
    if (pendingId && !badge && head) {
      const b = document.createElement('span');
      b.className = 'pc-badge';
      b.textContent = '待审批';
      head.appendChild(b);
    } else if (!pendingId && badge) {
      badge.remove();
    }
    const tip = card.querySelector('.pc-acts');
    if (tip) tip.textContent = pendingId ? '等待你的审批 · 点击这张卡看计划全文' : '点击这张卡看计划全文';
  });
}

// 三处一起重画（主对话区 / 右栏子对话 / 右下角角标堆，加右栏「计划」视图联动）；
// 所有入口统一走它
function renderPanels() {
  refreshPlanCards(); // 计划卡上的「待审批」徽章随待办状态翻新
  renderAskBar(mainView);
  renderAskBar(subView);
  renderAskDock(otherAskItems());
  if (rpActive === 'plan') drawPlanView();
  loadSessions(); // 待办清单变了：侧栏的「⏸ 待确认」标记/橙点跟着翻新（自身带合并去抖）
}

// 角标堆的两个交互：整块收起成胶囊 / 胶囊点开（点开＝用户显式要展开）
$('ask-dock-min').onclick = () => setAskDockMin(true);
$('ask-dock-pill').onclick = () => { askDock.seen = 0; setAskDockMin(false); };

// 可展空间随「对话列宽度 / 右栏开合 / 窗口缩放」变。右栏开合是有过渡动画的，过渡
// 途中算出来的留白是中间值（会误判成「放不下」），所以这里做防抖：等布局落定再算，
// 只有真正跨过门槛（能展开 ↔ 只能给胶囊 ↔ 放不下）或宽度变了才重画。
let dockSyncTimer = 0;
function syncAskDock() {
  clearTimeout(dockSyncTimer);
  dockSyncTimer = setTimeout(() => {
    const mt = dockMetrics();
    if (!mt) return;
    if (mt.width === askDock.lastW && mt.auto === askDock.lastAuto && mt.pill === askDock.lastPill) return;
    renderAskDock(otherAskItems());
  }, 150);
}
window.addEventListener('resize', syncAskDock);
if (window.ResizeObserver) {
  // 两个信号都要：#main 的宽度＝留白的来源（右栏开合/拖把手都会改它），
  // 消息列宽度＝内容列自己的宽度（拖把手改 --content-w 时只动它）
  const obs = new ResizeObserver(syncAskDock);
  obs.observe($('main'));
  const dockCol = document.querySelector('#messages .msg-col');
  if (dockCol) obs.observe(dockCol);
}

/* ---------- 右栏「计划」视图（exit_plan_mode 的正文：Markdown + 可反复打开） ----------
   文档来源两路合一：
   · 历史：GET /api/sessions/{id}/plans（后端从会话日志里读回 exit_plan_mode 的调用
     参数——计划与对话同寿命，刷新页面、切走再回来、重启进程后都还在）；
   · 待审批：/api/questions 里该会话 kind=plan 的提问，正文在这里就地批准/驳回。
   计划全文不进输入区（那边只留摘要卡），所以表格/清单/代码块都能在右栏正常展开。 */

let planSid = '';      // 视图绑定的会话（默认跟随主对话；从别的对话的卡点进来则绑那个会话）
let planDocs = [];     // 历史文档（新的在后）
let planSel = '';      // 当前展示的文档 key（渲染结果，回填下拉）
let planPinned = '';   // 用户在下拉里显式选过的那份：优先显示，直到它消失或来了新计划
let planWantSeq = 0;   // 一次性定位：消息流里的计划卡被点击（按提交序号）
let planWantQid = '';  // 一次性定位：刚到达的待审批计划（按提问 id）
let planCur = null;    // 当前展示的文档（复制按钮用）
let planFetching = false;
const planTried = {};  // 计划提问 id → 已补拉过历史（只补一次）

function planKeyOf(d) { return d.pendingId ? 'q:' + d.pendingId : 'h:' + d.seq; }

// 打开计划视图（sid 省略 = 跟随主对话）
// 打开计划视图（sid 省略 = 跟随主对话；给 seq/qid 就是「直接定位到某一份」）
function openPlanView(sid, seq, qid) {
  if (sid) planSid = sid;
  if (!planSid && state.cur) planSid = state.cur.id;
  // 一次性定位：点的是哪一份就看哪一份（右栏清单加载完自动选中，配不上就退回默认）
  if (seq) planWantSeq = seq;
  if (qid) planWantQid = qid;
  if (seq || qid) planPinned = ''; // 显式定位优先于「上次在下拉里选过的那份」
  rpOpen('plan');
}

// 只在「新出现的」待审批计划上把右栏切过去（首次打开、重启后拉回、本轮新提交）：
// 后续重复拉取不再打断用户在右栏里翻看旧版计划的选择。
const planSeenQids = {};
function noteFreshPlanQuestions() {
  let fresh = '';
  for (const q of state.questions || []) {
    if (q.kind !== 'plan' || planSeenQids[q.id]) continue;
    planSeenQids[q.id] = true;
    fresh = q.id;
  }
  if (fresh) { planWantQid = fresh; planPinned = ''; }
}

// 计划提问到达/本对话切换时自动打开一次（用户手动关掉后不再自动弹）
function maybeAutoOpenPlan() {
  const sid = (state.cur && state.cur.id) || '';
  if (!sid) return;
  if (isNarrow()) return; // 手机端右栏是整屏浮层：不自动铺开，卡上的「查看计划」随时可点
  const q = state.questions.find((x) => x.kind === 'plan' && x.sessionId === sid);
  if (!q || planAutoOpened[q.id]) return;
  planAutoOpened[q.id] = true;
  openPlanView(sid);
}

// 提问/回答离场前先留一份正文副本：历史那边还没接上时右栏不会突然空掉
function stashPlanDoc(q) {
  const plan = q.plan || '';
  if (!plan) return;
  const head = plan.replace(/…$/, '');
  if (planDocs.some((d) => d.markdown === plan || d.markdown.startsWith(head))) return;
  planDocs.push({ seq: 0, title: planTitleOf(plan), markdown: plan, createdAt: '', truncated: true });
}

async function loadPlanView() {
  const sid = planSid || (state.cur && state.cur.id) || '';
  planSid = sid;
  if (!sid) { planDocs = []; drawPlanView(); return; }
  planFetching = true;
  try {
    const d = await api('/api/sessions/' + sid + '/plans');
    planDocs = (d.plans || []).map((p) => ({
      seq: p.seq, title: p.title || planTitleOf(p.markdown),
      markdown: p.markdown, createdAt: p.createdAt || '',
    }));
  } catch (e) {
    /* 服务离线/会话不存在：保留上次的清单（空清单时下面会给提示） */
  } finally { planFetching = false; }
  drawPlanView();
}

// 历史文档 + 待审批提问（同一份按正文前缀配对，配不上就以提问自带正文兜底）
function planDocsWithPending() {
  const sid = planSid || (state.cur && state.cur.id) || '';
  const out = planDocs.map((d) => ({ ...d }));
  for (const q of state.questions) {
    if (q.kind !== 'plan' || q.sessionId !== sid) continue;
    const head = (q.plan || '').replace(/…$/, '');
    const hit = out.find((d) => d.markdown === q.plan || d.markdown.startsWith(head));
    if (hit) hit.pendingId = q.id;
    else out.push({ seq: 0, title: planTitleOf(q.plan || ''), markdown: q.plan || '', pendingId: q.id, createdAt: '', truncated: true });
  }
  return out;
}

function drawPlanView() {
  const docEl = $('plan-doc');
  if (!docEl) return;
  const list = planDocsWithPending();
  // 选中规则（优先级从高到低）：
  //   1) 一次性定位：点了某张计划卡 / 本回合新到达的待审批计划；
  //   2) 用户在下拉里显式选过的那份（它消失、或又来了新计划就作废）；
  //   3) 最新的那份「待审批」计划——新计划提交后默认露出新的，不再停在上一版；
  //   4) 上次显示的那份；5) 最新一份。有「待审批」时永远优先看它。
  let cur = null;
  if (planWantSeq) { const hit = list.find((d) => d.seq === planWantSeq); if (hit) { cur = hit; planWantSeq = 0; } }
  if (!cur && planWantQid) { const hit = list.find((d) => d.pendingId === planWantQid); if (hit) { cur = hit; planWantQid = ''; } }
  if (!cur && planPinned) cur = list.find((d) => planKeyOf(d) === planPinned) || null;
  if (!cur) {
    const pend = list.filter((d) => d.pendingId);
    cur = pend.length ? pend[pend.length - 1] : null;
  }
  if (!cur) cur = list.find((d) => planKeyOf(d) === planSel) || null;
  if (!cur) cur = list[list.length - 1] || null;
  // （旧的「有待审批就直接跳过去」兜底已并进上面的规则 3）
  planSel = cur ? planKeyOf(cur) : '';
  planCur = cur || null;

  const pick = $('plan-pick');
  pick.innerHTML = '';
  list.forEach((d) => {
    const o = document.createElement('option');
    o.value = planKeyOf(d);
    o.textContent = (d.pendingId ? '待审批 · ' : '') + d.title +
      (d.createdAt ? '（' + String(d.createdAt).slice(5, 16) + '）' : '');
    pick.appendChild(o);
  });
  pick.value = planSel;
  pick.classList.toggle('hidden', list.length < 2);

  const pendingCount = list.filter((d) => d.pendingId).length;
  $('plan-status').textContent = !list.length ? ''
    : (pendingCount ? '待审批 ' + pendingCount + ' 份 · 共 ' + list.length + ' 份' : '共 ' + list.length + ' 份');

  if (!cur) {
    docEl.innerHTML = '<div class="plan-empty">本对话还没有提交过计划。<br>' +
      '计划模式下模型完成调研、调用 exit_plan_mode 提交后，计划正文就会出现在这里；' +
      '可以随时关掉再打开，刷新页面也还在。</div>';
  } else {
    docEl.innerHTML =
      '<div class="plan-meta"><b>' + esc(cur.title) + '</b>' +
      (cur.createdAt ? '<span>' + esc(cur.createdAt) + '</span>' : '') +
      (cur.pendingId ? '<span class="plan-badge">待审批</span>' : '') +
      (cur.truncated ? '<span class="plan-badge gray">摘要</span>' : '') +
      '</div><div class="md plan-md">' + md(cur.markdown) + '</div>';
  }

  const act = $('plan-act');
  if (cur && cur.pendingId) {
    act.classList.remove('hidden');
    $('plan-feedback').value = askDraftGet(cur.pendingId);
  } else {
    act.classList.add('hidden');
  }
  // 待审批的正文在历史里还没配对上（工具调用刚落库）：拉一次清单补齐全文，
  // 每份提问只补拉一次（planTried），免得历史里确实没有时来回打转
  const missing = list.find((d) => d.pendingId && d.truncated);
  if (missing && !planFetching && !planTried[missing.pendingId]) {
    planTried[missing.pendingId] = true;
    loadPlanView();
  }
}

// 右栏「计划」视图的控件（只绑一次；展示对象从 planCur 取）
$('plan-pick').addEventListener('change', (e) => {
  planPinned = e.target.value; planWantSeq = 0; planWantQid = ''; // 用户显式选择：优先显示它
  planNoteHide(); drawPlanView();
});
$('plan-copy').onclick = () => {
  if (!planCur) return;
  navigator.clipboard.writeText(planCur.markdown).catch(() => {});
  $('plan-status').textContent = '已复制计划全文';
};
$('plan-reload').onclick = () => loadPlanView();

// 导出：把当前这份计划写成工作区里的 .md（后端不覆盖同名，自动加 -2、-3）
function planNoteShow(text, dir, bad) {
  const note = $('plan-note');
  $('plan-note-text').textContent = text;
  note.dataset.dir = dir || '';
  $('plan-note-open').classList.toggle('hidden', !dir);
  note.classList.toggle('bad', !!bad);
  note.classList.remove('hidden');
}
function planNoteHide() {
  const note = $('plan-note');
  if (!note) return;
  note.classList.add('hidden');
  note.dataset.dir = '';
}
$('plan-export').onclick = async () => {
  if (!planCur) return;
  const btn = $('plan-export');
  const sid = planSid || (state.cur && state.cur.id) || '';
  if (!sid) return;
  btn.disabled = true;
  try {
    const d = await api('/api/sessions/' + sid + '/plans/export',
      { method: 'POST', body: JSON.stringify({ seq: planCur.seq || 0 }) });
    planNoteShow('已导出：' + (d.path || d.name || ''), d.dir || '', false);
  } catch (e) {
    planNoteShow('导出失败：' + e.message, '', true);
  } finally {
    btn.disabled = false;
  }
};
$('plan-note-open').onclick = async () => {
  const dir = $('plan-note').dataset.dir || '';
  if (!dir) return;
  try {
    await api('/api/open-folder', { method: 'POST', body: JSON.stringify({ path: dir }) });
  } catch (e) {
    planNoteShow('打开文件夹失败：' + e.message, dir, true);
  }
};

$('plan-feedback').addEventListener('input', (e) => {
  if (planCur && planCur.pendingId) askDraftSet(planCur.pendingId, e.target.value);
});
$('plan-ignore').onclick = () => { if (planCur && planCur.pendingId) decideQuestion(planCur.pendingId, '__ignore__'); };
$('plan-approve').onclick = () => { if (planCur && planCur.pendingId) decideQuestion(planCur.pendingId, '__approve__'); };
$('plan-reject').onclick = () => {
  if (!planCur || !planCur.pendingId) return;
  const v = $('plan-feedback').value.trim();
  decideQuestion(planCur.pendingId, v || '（未填写具体意见）');
};
rpRegister('plan', { title: '计划', order: 2, onOpen: () => { if (!planSid && state.cur) planSid = state.cur.id; loadPlanView(); } });

/* ---------- 进度面板（progress_write）：对话区右上角的一枚浮窗，可折叠 ---------- */
// 定位只有一个锚点：对话区（#main）的右上角。折叠＝一枚「进度」胶囊，展开＝从同一个
// 右上角往左下长出来的卡片——右缘与顶边都不动，所以点开是「原地展开」不跳位。
// 宽度按对话区右侧的留白算（见 pgMetrics）：留白放得下就塞进留白里，不遮正文；留白
// 不够说明是窄屏/中等窗口，那就用一张舒适宽度的卡（用户自己点开的，遮一点也能接受）。
// 用户没点过时（pgFold=null）只在留白够的时候自动展开，否则一律折叠成胶囊；点过之后
// 按用户的选择来（本次页面有效，刷新复位）。面板永远是浮层：不占对话区高度，出现与
// 折叠切换都不触发对话区重排。
const PG_W = 340;    // 卡片最大宽度（也是窄屏下舒适阅读宽度）
const PG_MIN = 220;  // 自动展开的门槛：右侧留白至少放得下这么宽
const PG_GAP = 10;   // 面板与对话区右缘的间距（胶囊与卡片共用，保证同一锚点）
const PG_PAD = 24;   // 内容列自身的内边距：留白不够时可以借它，不算遮正文

let pgItems = [];    // 最近一次取到的进度项（空 = 没有进度项）
let pgRuns = [];     // 本会话「运行中的程序」（在跑的子进程）；两者都空才整体隐藏
let pgFold = null;   // 用户显式选择：null=自动；true=展开；false=收起
let pgDrawn = null;  // 上次画出来的展开态（留白变化时只在翻转时才重画）

// 右侧留白 → 卡片宽度与「够不够自动展开」。按 #main 与消息列的实际矩形算，
// 开关左右栏、拖拽调宽对话列都会自然反映进来（它们都会改变这个留白）。
function pgMetrics() {
  const main = $('main'), col = document.querySelector('#messages .msg-col');
  if (!main || !col) return null;
  const mainRect = main.getBoundingClientRect();
  const room = Math.max(0, mainRect.right - col.getBoundingClientRect().right);
  const fit = room + PG_PAD - PG_GAP; // 塞进留白，顺手借用内容列自己的右内边距
  return {
    width: Math.round(Math.min(PG_W, fit >= PG_MIN ? fit : mainRect.width - 2 * PG_GAP)),
    auto: fit >= PG_MIN,
  };
}
function pgExpanded(mt) { return pgFold === null ? mt.auto : pgFold; }

async function loadProgress() {
  const panel = $('progress-panel');
  if (!panel) return;
  if (!state.cur) { pgItems = []; pgRuns = []; pgFold = null; drawProgress(); return; }
  try {
    const d = await api('/api/sessions/' + state.cur.id + '/progress');
    pgItems = d.progress || [];
    pgRuns = d.running || [];
  } catch (e) { /* 会话不存在/服务离线：维持上一次的样子 */ }
  drawProgress();
}

// 运行时长文案：88s / 3m20s / 1h02m
function fmtElapsed(sec) {
  sec = Math.max(0, Math.round(sec || 0));
  if (sec < 60) return sec + 's';
  const m = Math.floor(sec / 60), s = sec % 60;
  if (m < 60) return m + 'm' + (s < 10 ? '0' + s : s) + 's';
  return Math.floor(m / 60) + 'h' + (m % 60 < 10 ? '0' + (m % 60) : m % 60) + 'm';
}

// 还有在跑的程序时每 5s 刷一次时长（起/停由 SSE 的 children 事件即时刷）
let pgRunTick = null;
function scheduleRunTick() {
  if (pgRunTick) { clearTimeout(pgRunTick); pgRunTick = null; }
  // 只要有登记项（在跑，或刚结束还没被服务端清掉的）就继续刷，直到列表空掉为止
  if (!state.cur || !pgRuns.length) return;
  pgRunTick = setTimeout(() => { pgRunTick = null; loadProgress(); }, 5000);
}

// 按当前折叠状态重画面板：折叠＝胶囊，展开＝头部（整行是收起按钮）+ 清单。
// 两态共用同一个右上角锚点，宽度写进 --pg-w 交给 CSS，所以展开就是在原地长出来。
function drawProgress() {
  const panel = $('progress-panel');
  if (!panel) return;
  const items = pgItems, runs = pgRuns;
  if (!items.length && !runs.length) { panel.classList.add('hidden'); panel.innerHTML = ''; pgDrawn = null; return; }
  const mt = pgMetrics() || { width: PG_W, auto: false };
  const open = pgExpanded(mt);
  pgDrawn = open;
  panel.style.setProperty('--pg-w', mt.width + 'px');
  panel.classList.remove('hidden');
  panel.classList.toggle('pg-folded', !open);
  const running = runs.filter((r) => r.state === 'running').length;
  if (!open) {
    panel.innerHTML = '<button type="button" class="pg-pill" title="展开进度清单">进度'
      + (running ? '<span class="pg-run-dot"></span>' + running + ' 个在跑' : '')
      + chev('pg-chev') + '</button>';
    panel.querySelector('.pg-pill').onclick = () => { pgFold = true; drawProgress(); };
    return;
  }
  const done = items.filter((t) => t.status === 'completed').length;
  let html = '<button type="button" class="progress-head" title="收起进度清单">'
    + '<b>进度</b><span class="pg-count">' + done + '/' + items.length + '</span>'
    + '<svg class="pg-chev" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 15l6-6 6 6"/></svg>'
    + '</button>';
  if (items.length) {
    html += '<ul>';
    for (const t of items) {
      const mark = t.status === 'completed' ? '✔' : (t.status === 'in_progress' ? '▶' : '·');
      html += '<li class="pg-' + (t.status || 'pending') + '"><span class="pg-mark">' + mark + '</span>' + esc(t.content) + '</li>';
    }
    html += '</ul>';
  }
  if (runs.length) {
    // 横线隔开：上面是进度清单，下面专放本会话「运行中的程序」——短命令会一闪而过，
    // 一直后台跑的（后台作业、长命令）会一直留在这里，看得见它在跑多久。
    html += '<div class="pg-sep"></div>'
      + '<div class="pg-runs-head">运行中的程序<span class="pg-runs-n">' + (running ? running + ' 个在跑' : '刚结束') + '</span></div>'
      + '<ul class="pg-runs">';
    for (const r of runs) {
      const live = r.state === 'running';
      const cmd = String(r.command || '');
      html += '<li class="pg-run' + (live ? ' live' : '') + '">'
        + '<span class="pg-run-label">' + esc(r.label || '子进程') + '</span>'
        + '<span class="pg-run-cmd" title="' + esc(cmd) + '">' + esc(cmd.length > 70 ? cmd.slice(0, 70) + '…' : cmd) + '</span>'
        + '<span class="pg-run-time">' + (live ? '已跑 ' + fmtElapsed(r.elapsed) : esc(r.exit || '已结束')) + '</span>'
        + '</li>';
    }
    html += '</ul>';
  }
  panel.innerHTML = html;
  panel.querySelector('.progress-head').onclick = () => { pgFold = false; drawProgress(); };
  scheduleRunTick();
}

// 留白变了（缩放窗口、开关左右栏、拖拽调宽对话列）：宽度跟着变；自动档下展开态翻转
// 才重画，其余情况只改宽度不动 DOM
function syncProgress() {
  if (!pgItems.length && !pgRuns.length) return;
  const mt = pgMetrics();
  if (!mt) return;
  const panel = $('progress-panel');
  if (panel && !panel.classList.contains('hidden')) panel.style.setProperty('--pg-w', mt.width + 'px');
  if (pgExpanded(mt) !== pgDrawn) drawProgress();
}
window.addEventListener('resize', syncProgress);
// 对话列宽度会单独变（拖把手、右栏开合），盯它的尺寸就够了
if (window.ResizeObserver) {
  const pgCol = document.querySelector('#messages .msg-col');
  if (pgCol) new ResizeObserver(syncProgress).observe(pgCol);
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

function setStreamUI(on, view) {
  view = view || mainView;
  // 发送按钮保持可用：执行中发送 = 插话或排队（常规页「对话中途发送行为」）
  view.stopBtn.classList.toggle('hidden', !on);
  if (!on) view.hint.textContent = '';
}

function handleEvent(ev, view) {
  view = view || mainView;
  switch (ev.type) {
    case 'edited': {
      // 编辑重发的截断确认：服务端已删掉 seq 之后的记录并改写正文，
      // 本地对齐后模型续跑的输出会接着这条消息往下走；该点之后的
      // 文件快照与交付登记也已被服务端一并清掉
      view.state.msgs = view.state.msgs.filter((x) => x.seq <= ev.seq);
      view.state.msgs.forEach((x) => { if (x.seq === ev.seq) x.content = ev.delta; });
      if (view.state.checkpoints) view.state.checkpoints = view.state.checkpoints.filter((c) => c.seq <= ev.seq);
      if (view.state.deliveries) view.state.deliveries = view.state.deliveries.filter((d) => d.seq <= ev.seq);
      renderMessages(view.state.msgs, view);
      break;
    }
    case 'message': {
      view.state.streamText = '';
      view.state.streamThinking = '';
      removeLive(view);
      // 用户消息回显落地：「发送中」占位功成身退（助手/工具事件不会先于它到达）
      if (view.state.pendingSend && ev.message && ev.message.role === 'user') {
        view.state.pendingSend = null;
      }
      if (view.state.attached) {
        // 跟随别的连接发起的执行：以服务端为准整体重取，避免与已落库内容重复
        reloadMessages(view);
        break;
      }
      // 后端 message 事件带该步思考原文与消息 seq：
      // 思考块实时渲染不消失（工具调用落地后仍在），seq 用作回合标识与搜索定位
      if (ev.thinking) ev.message.thinking = ev.thinking;
      if (ev.seq) ev.message.seq = ev.seq;
      if (ev.kind) ev.message.kind = ev.kind; // 行类型：与存储 kind 一致（契约见《消息类型规范.md》）
      if (ev.origin) ev.message.origin = ev.origin;
      view.state.msgs.push(ev.message);
      renderMessages(view.state.msgs, view);
      break;
    }
    case 'resume': // 接回执行流时补发的、进行中的那半句
      if (ev.delta) view.state.streamText = ev.delta;
      if (ev.thinking) view.state.streamThinking = ev.thinking;
      liveRender(view);
      break;
    case 'pending': // 插话/排队队列变化（自己发的、别的窗口发的、服务端排水的都在这里）
      applyPending(view, ev.pending);
      break;
    case 'stopped':
      view.hint.textContent = ev.error || '已停止';
      // 停止后队列保留不自动续：横幅给「继续发送」入口（队列空则无事发生）
      view.state.queueHeld = (view.state.pendingQueue || []).length > 0;
      if (view.state.attached) reloadMessages(view);
      break;
    case 'delta':
      view.state.streamText += ev.delta;
      liveRender(view);
      if (view === mainView && state.cur && state.cur.mode === 'assistant') setFace('thinking');
      break;
    case 'reasoning': // 思考增量：实时显示在折叠块里
      view.state.streamThinking = (view.state.streamThinking || '') + ev.delta;
      liveRender(view);
      if (view === mainView && state.cur && state.cur.mode === 'assistant') setFace('thinking');
      break;
    case 'tool_start':
      break;
    case 'tool_wait':
      view.hint.textContent = ev.output || '等待用户批准…';
      break;
    case 'tool_end':
      // deliver_result 一落库就刷新会话轻量状态：交付卡片当轮实时出现
      //（deliveries/checkpoints 只在打开会话时拉取过，不刷就永远不显示）
      if (ev.name === 'deliver_result' && !view.state.attached) reloadMessages(view);
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
      // 首条消息自动起标题（服务端只在会话标题为空时才发）；跟随当前会话更新顶栏
      if (view === mainView && state.cur && ev.sessionId === state.cur.id) {
        $('sess-title').textContent = ev.title;
      }
      break;
    case 'error':
      // 真实失败：收掉流式、把原因显示到输入框上方的报错条（不进对话区）
      view.state.streamText = '';
      view.state.streamThinking = '';
      removeLive(view);
      showRunError(view, ev.error);
      view.state.queueHeld = (view.state.pendingQueue || []).length > 0; // 出错后队列保留
      break;
    case 'retry':
      // 重试即将开始（人工点重试 / 网络失败自动重试）：本步已流出的半截作废——
      // 服务端会从头重新输出，本地同步清干净并收掉报错条（observe 也已清掉
      // 重连补发的半截缓冲）
      hideRunError(view);
      view.state.streamText = '';
      view.state.streamThinking = '';
      removeLive(view);
      break;
    case 'auto_retry':
      // 自动重试提示条：output 非空=显示（几秒后第 X/5 次重试）；空=重试成功，清除
      view.hint.textContent = ev.output || '';
      break;
    case 'done':
      view.hint.textContent = '';
      // 正常收尾但队列仍有保留 = 暂停生效（排水循环被暂停门挡下）：横幅走暂停分支
      view.state.queueHeld = (view.state.pendingQueue || []).length > 0;
      if (view === mainView && state.cur && state.cur.mode === 'assistant') setFace('done', 4000);
      break;
  }
}

/* ---------- 执行中的发消息：插话（贴到对话末尾，下一次调用模型生效）
   与排队（进输入框上方排队坞，本轮结束后自动作为独立一轮发送）。
   两边的队列都在服务端（runtime.go 的 PendingMsg）：刷新页面/换窗口不丢，
   前端只负责呈现与增删。 ---------- */

function pendingHint(view, text) {
  view.hint.textContent = text;
  setTimeout(() => { if (view.hint.textContent === text) view.hint.textContent = ''; }, 6000);
}

/* ---------- 报错条：模型/网络等真实失败显示在输入框上方（不属于对话区），
   带「重试」按钮（重试不再重复落库用户消息，后台直接带着当前上下文再跑一轮）。
   正常完成与用户手动停止都不产生任何内容。 ---------- */

function showRunError(view, text) {
  view = view || mainView;
  const box = viewEl(view, 'run-error');
  if (!box) return;
  if (!text) { hideRunError(view); return; }
  if (view.state.errDismissed === text) return; // 这条已被用户手动关掉
  view.state.errText = text;
  box.innerHTML = '';
  box.classList.remove('hidden');
  const icon = document.createElement('span');
  icon.className = 're-icon';
  icon.textContent = '⚠';
  const msg = document.createElement('span');
  msg.className = 're-text';
  msg.textContent = text;
  const retry = document.createElement('button');
  retry.className = 're-btn re-retry';
  retry.type = 'button';
  retry.textContent = '重试';
  retry.onclick = () => retryRun(view);
  // 反馈：把这条报错一键带进 设置→关于 的反馈表单（远程用户的主报修通道）
  const fb = document.createElement('button');
  fb.className = 're-btn re-retry';
  fb.type = 'button';
  fb.textContent = '反馈';
  fb.onclick = () => {
    const sid = (view.state.cur && view.state.cur.id) || '';
    openFeedbackForm('【运行报错】' + text + (sid ? '\n（会话 ' + sid + '）' : ''), sid);
  };
  const close = document.createElement('button');
  close.className = 're-btn re-close';
  close.type = 'button';
  close.title = '关闭';
  close.textContent = '✕';
  close.onclick = () => hideRunError(view, true);
  box.appendChild(icon);
  box.appendChild(msg);
  box.appendChild(retry);
  box.appendChild(close);
}

// hideRunError 收掉报错条；remember=true 表示「用户手动关闭」（同一条不再自动弹回）
function hideRunError(view, remember) {
  view = view || mainView;
  const box = viewEl(view, 'run-error');
  if (remember) view.state.errDismissed = view.state.errText || '';
  view.state.errText = '';
  if (!box) return;
  box.classList.add('hidden');
  box.innerHTML = '';
}

/* ---------- 更新横幅（服务端推送通知的统一出口） ---------- */

// showUpdateBanner 新版本横幅：置顶常驻，「确认更新」直接下载安装包；
// 在线时由 update_available 门铃事件触发，刚打开页面时从 /api/status 的 update 恢复。
function showUpdateBanner(u) {
  if (!u || !u.latest) return;
  const bar = document.getElementById('update-banner');
  if (!bar) return;
  bar.querySelector('.ub-text').innerHTML =
    '新版本 <b>' + esc(u.latest) + '</b> 可用（当前 ' + esc(u.current || '') + '）' +
    (u.notes ? '　<span class="ub-notes">' + esc(String(u.notes).slice(0, 80)) + '</span>' : '');
  bar.dataset.download = u.downloadUrl || '';
  bar.dataset.url = u.url || '';
  bar.classList.remove('hidden');
}

$('update-banner').addEventListener('click', (e) => {
  const bar = e.currentTarget;
  if (e.target.closest('.ub-close')) { bar.classList.add('hidden'); return; }
  if (e.target.closest('.ub-confirm')) {
    // 确认更新：直接下载安装包（发布附件直链；浏览器接管下载进度）
    const dl = bar.dataset.download;
    if (dl) {
      const a = document.createElement('a');
      a.href = dl;
      a.download = '';
      document.body.appendChild(a);
      a.click();
      a.remove();
    } else if (bar.dataset.url) {
      window.open(bar.dataset.url, '_blank'); // 没挂附件就退回发布页
    }
    bar.classList.add('hidden');
  }
});

// retryRun 重试上一轮：直接启动新一轮执行（用户消息已在库里），并跟随输出
async function retryRun(view) {
  view = view || mainView;
  const cur = view.state.cur;
  if (!cur || view.state.streaming) return;
  hideRunError(view);
  view.state.streaming = true;
  view._pinned = false; // 主动重试：恢复跟随
  setStreamUI(true, view);
  view.state.abort = new AbortController();
  view.state.streamText = '';
  view.state.streamThinking = '';
  let aborted = false;
  try {
    const res = await fetch(`/api/sessions/${cur.id}/retry`, {
      method: 'POST', signal: view.state.abort.signal,
    });
    if (!res.ok) throw new Error(res.statusText || '重试失败');
    await consumeStream(res, view, cur.id);
  } catch (e) {
    if (e.name === 'AbortError') aborted = true;
    // 慢网下请求失败可能晚于切走：报错条只落在仍停在这个会话的界面上
    else if (view.state.cur && view.state.cur.id === cur.id) showRunError(view, '重试失败：' + e.message);
  }
  await endTurn(view, aborted, { sid: cur.id, abort: view.state.abort });
}

// endTurn 一轮结束后的收尾（send / retry / edit / attach 共用）：收流式、对齐状态、刷新周边。
// stream = {sid, abort} 用于辨认「这条流还算不算当前界面的」：用户中途切到别的
// 会话（detachStream）或又接了别的流时，这条流的收尾不再动界面。
async function endTurn(view, aborted, stream) {
  if (stream && (view.state.abort !== stream.abort ||
      !view.state.cur || view.state.cur.id !== stream.sid)) return;
  removeLive(view);
  view.state.streaming = false;
  view.state.attached = false;
  view.state.pendingSend = null; // 收尾兜底：占位气泡不越过本轮（正常路径早已被回显替换）
  if (aborted) {
    await reloadMessages(view); // 本地被动断流：整体对一次
  } else {
    await refreshIfDangling(view); // 流断但后端仍在跑：接回去继续看
    // 正常收尾也重取一次：文件改动面板与交付卡片当轮即出（此前只有
    // 重开会话时才会拉到 deliveries/checkpoints，同页跑完的回合永远不显示）
    if (!view.state.streaming) await reloadMessages(view);
  }
  // 中途的 await（backendRunning / reloadMessages 都取会话详情）在慢网下可达数秒，
  // 期间用户可能已切走/回首页：收尾前复验这条流仍属于当前界面，否则整个尾巴不动。
  // 晚到的旧续体曾把空消息盖到首页问候区上、连里面的输入卡一起销毁
  if (stream && (view.state.abort !== stream.abort ||
      !view.state.cur || view.state.cur.id !== stream.sid)) return;
  renderMessages(view.state.msgs, view);
  setStreamUI(false, view);
  renderQueueDock(view); // 流结束才轮得到横幅判定（执行结束后队列保留的「继续发送」）
  if (view === mainView) {
    loadSessions();
    loadStatus();
    refreshCtxChip();
    JX.emit('message:done', { sessionId: state.cur && state.cur.id });
  } else {
    refreshDrawer();
    refreshCtxChip(subView);
  }
}

// 排队坞：输入框上方的紧凑面板，列出排队的消息（提前为插话/编辑回填/删除/
// 拖拽排序）。头部带队列级「暂停/继续」开关；暂停中、或执行结束后队列保留
// （停止/出错）时给横幅 +「继续发送」。顺序与暂停状态都以服务端为准。
function renderQueueDock(view) {
  view = view || mainView;
  const box = viewEl(view, 'queue-dock');
  if (!box) return;
  const list = view.state.pendingQueue || [];
  if (!view.state.cur || !list.length) { box.classList.add('hidden'); box.innerHTML = ''; return; }
  box.classList.remove('hidden');
  const paused = !!view.state.queuePaused;
  const held = !paused && view.state.queueHeld && !view.state.streaming;
  const expanded = box.dataset.open === '1' || list.length === 1;
  box.innerHTML = '';
  const panel = document.createElement('div');
  panel.className = 'qd-panel';
  // 头部常驻（单条也有——暂停是队列级开关，单条消息同样可能想压着不发）
  const head = document.createElement('div');
  head.className = 'qd-head';
  const tog = document.createElement('button');
  tog.className = 'qd-toggle';
  tog.type = 'button';
  tog.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 12h13M3 6h18M3 18h18"/><path d="M17 9l3 3-3 3"/></svg>' +
    '<span class="qd-count">' + list.length + ' 条排队消息</span>' +
    '<span class="qd-sub">' + (paused ? '已暂停，继续前不自动发送' : '本轮结束后依次发送') + '</span>' +
    (list.length > 1 ? '<svg class="caret" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M6 9l6 6 6-6"/></svg>' : '');
  if (list.length > 1) {
    tog.onclick = () => { box.dataset.open = expanded ? '0' : '1'; renderQueueDock(view); };
  } else {
    tog.tabIndex = -1; // 单条没有折叠语义
    tog.style.cursor = 'default';
  }
  head.appendChild(tog);
  const pause = document.createElement('button');
  pause.className = 'qd-act qd-pause';
  pause.type = 'button';
  pause.title = paused ? '继续：排队消息依次自动发送' : '暂停：本轮结束后排队消息保留不自动发送';
  pause.innerHTML = paused
    ? '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 4l14 8-14 8Z"/></svg>'
    : '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M9 5v14M15 5v14"/></svg>';
  pause.onclick = () => pendingAction(view, '', paused ? 'resume' : 'pause');
  head.appendChild(pause);
  panel.appendChild(head);
  if (paused || held) {
    const banner = document.createElement('div');
    banner.className = 'qd-banner';
    const bt = document.createElement('span');
    bt.className = 'qd-btext';
    bt.textContent = paused
      ? '队列已暂停：' + list.length + ' 条消息保留，不会自动发送'
      : '本轮已结束：' + list.length + ' 条排队消息保留待发';
    banner.appendChild(bt);
    const resume = document.createElement('button');
    resume.className = 'qd-resume';
    resume.type = 'button';
    resume.textContent = '继续发送';
    resume.onclick = () => pendingAction(view, '', 'resume');
    banner.appendChild(resume);
    panel.appendChild(banner);
  }
  if (!expanded) { box.appendChild(panel); return; }
  const ul = document.createElement('ul');
  ul.className = 'qd-list';
  ul.addEventListener('dragend', () => qdClearDropMarks(ul));
  list.forEach((pm) => {
    const li = document.createElement('li');
    li.className = 'qd-row';
    li.dataset.id = pm.id;
    if (list.length > 1) li.appendChild(qdGrip(pm, li));
    const txt = document.createElement('span');
    txt.className = 'qd-text';
    txt.textContent = pm.content.length > 200 ? pm.content.slice(0, 200) + '…' : pm.content;
    txt.title = pm.content;
    li.appendChild(txt);
    li.appendChild(qdAction('steer', view, pm, '插话发送：下一次调用模型前生效', '<path d="M12 19V5M5 12l7-7 7 7"/>'));
    // 编辑只给自己发的排队项（kind 空/chat）；数字助理派发注入的不参与
    if (!pm.kind || pm.kind === 'chat') {
      li.appendChild(qdAction('edit', view, pm, '退回输入框编辑', '<path d="M12 20h9"/><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4Z"/>'));
    }
    li.appendChild(qdAction('remove', view, pm, '移出队列', '<path d="M6 6l12 12M18 6L6 18"/>'));
    qdDropTarget(view, li);
    ul.appendChild(li);
  });
  panel.appendChild(ul);
  box.appendChild(panel);
}

// 拖拽把手（HTML5 DnD，桌面用；触屏拖拽浏览器不支持，排序走桌面端）
function qdGrip(pm, li) {
  const g = document.createElement('span');
  g.className = 'qd-grip';
  g.title = '拖拽调整发送顺序';
  g.draggable = true;
  g.innerHTML = '<svg viewBox="0 0 24 24" fill="currentColor"><circle cx="9" cy="6" r="1.7"/><circle cx="15" cy="6" r="1.7"/><circle cx="9" cy="12" r="1.7"/><circle cx="15" cy="12" r="1.7"/><circle cx="9" cy="18" r="1.7"/><circle cx="15" cy="18" r="1.7"/></svg>';
  g.addEventListener('dragstart', (e) => {
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', pm.id);
    li.classList.add('dragging');
  });
  g.addEventListener('dragend', () => li.classList.remove('dragging'));
  return g;
}

// 行落点：上半行=插到该行前，下半行=插到该行后（用下一行当锚点；末行下半=队尾）。
// 锚点 id 上送服务端定序（move before=锚点），顺序唯一权威在服务端。
function qdDropTarget(view, li) {
  li.addEventListener('dragover', (e) => {
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    const r = li.getBoundingClientRect();
    const top = (e.clientY - r.top) < r.height / 2;
    qdClearDropMarks(li.parentElement);
    li.classList.add(top ? 'drop-before' : 'drop-after');
  });
  li.addEventListener('drop', (e) => {
    e.preventDefault();
    const dragId = e.dataTransfer.getData('text/plain');
    if (!dragId || dragId === li.dataset.id) return;
    const list = view.state.pendingQueue || [];
    const idx = list.findIndex((p) => p.id === li.dataset.id);
    if (idx < 0) return; // 拖的过程中列表变了：放弃本次落点（下次渲染已对齐最新）
    const r = li.getBoundingClientRect();
    const top = (e.clientY - r.top) < r.height / 2;
    const anchor = top ? list[idx].id : (list[idx + 1] ? list[idx + 1].id : '');
    if (anchor === dragId) return; // 原位：不动
    movePendingTo(view, dragId, anchor);
  });
}

function qdClearDropMarks(ul) {
  if (!ul) return;
  ul.querySelectorAll('.drop-before,.drop-after').forEach((n) => n.classList.remove('drop-before', 'drop-after'));
}

// 拖拽排序落地：move 成功后直接应用服务端返回的 pending 快照（多窗口一致）
async function movePendingTo(view, id, before) {
  try {
    const d = await api('/api/sessions/' + view.state.cur.id + '/pending', {
      method: 'POST', body: JSON.stringify({ id, action: 'move', before: before || '' }),
    });
    applyPending(view, d.pending);
  } catch (e) {
    pendingHint(view, '排序失败：' + e.message);
  }
}

function qdAction(action, view, pm, title, path) {
  const b = document.createElement('button');
  b.className = 'qd-act' + (action === 'steer' ? ' qd-steer' : '');
  b.type = 'button';
  b.title = title;
  b.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' + path + '</svg>';
  b.onclick = () => (action === 'edit' ? editPendingMsg(view, pm) : pendingAction(view, pm.id, action));
  return b;
}

async function pendingAction(view, id, action) {
  if (action === 'pause' || action === 'resume') {
    // 队列级开关（不需要消息 id）：暂停后本轮跑完保留不续发；恢复时空闲即接着发
    try {
      const d = await api('/api/sessions/' + view.state.cur.id + '/pending', {
        method: 'POST', body: JSON.stringify({ id: '', action }),
      });
      applyPending(view, d.pending);
      pendingHint(view, action === 'pause' ? '已暂停队列：本轮结束后排队消息保留不发送' : '已继续队列：排队消息将依次发送');
    } catch (e) {
      pendingHint(view, '操作失败：' + e.message);
    }
    return;
  }
  try {
    const d = await api('/api/sessions/' + view.state.cur.id + '/pending', {
      method: 'POST', body: JSON.stringify({ id, action }),
    });
    applyPending(view, d.pending);
    pendingHint(view, action === 'steer' ? '已提前为插话：下一次调用模型前生效' : '已移出队列');
  } catch (e) {
    pendingHint(view, '操作失败：' + e.message);
  }
}

// 编辑待生效消息（排队/插话）＝「撤回式编辑」：先从服务端队列权威删除（remove），
// 成功后才把原文回填输入框当草稿——消息要么在队列、要么在输入框，不会有两份。
// 输入框里已有草稿时拒绝，避免覆盖正在打的内容；删除请求返回前切走了会话则只保留
// 删除、放弃回填（否则会把旧会话的内容写进新会话的输入框）。
async function editPendingMsg(view, pm) {
  view = view || mainView;
  if (view.input.value.trim()) {
    pendingHint(view, '请先发送或清空输入框内容，再编辑排队消息');
    return;
  }
  const sid = view.state.cur && view.state.cur.id;
  if (!sid) return;
  try {
    const d = await api('/api/sessions/' + sid + '/pending', {
      method: 'POST', body: JSON.stringify({ id: pm.id, action: 'remove' }),
    });
    if (!view.state.cur || view.state.cur.id !== sid) return;
    applyPending(view, d.pending);
    view.input.value = pm.content;
    autoGrow(view.input);
    if (view === mainView) persistMainDraftLS(); else persistSubDraftLS(); // 程序化填入也要落盘，切走/重启不丢
    view.input.focus();
    pendingHint(view, '已退回输入框：编辑后重新发送');
  } catch (e) {
    if (view.state.cur && view.state.cur.id === sid) pendingHint(view, '操作失败：' + e.message);
  }
}

// 待生效消息快照入视图（服务端下发或接口返回）：只对当前会话生效
function applyPending(view, pending) {
  if (!pending) return;
  view.state.pendingSteer = pending.steer || [];
  view.state.pendingQueue = pending.queue || [];
  view.state.queuePaused = !!pending.paused;
  if (!view.state.pendingQueue.length) view.state.queueHeld = false; // 队列清空：保留横幅不再有意义
  renderQueueDock(view);
  if (view.state.cur) renderMessages(view.state.msgs, view);
}

// 执行中发出的消息：按「对话中途发送行为」插话或排队（两种模式都在服务端排队）
// 默认值=等待排队：新装/没设置过 jx.midrun 时排队；只有用户在设置里显式选过
// 「插话」（localStorage 里存了 interject）才走 steer。手动切换照旧存同一个键。
async function sendMidrun(content, viaInput, view) {
  view = view || mainView;
  const mode = lsGet('jx.midrun', 'queue') === 'interject' ? 'steer' : 'queue';
  try {
    const d = await api('/api/sessions/' + view.state.cur.id + '/interject', {
      method: 'POST', body: JSON.stringify({ content, mode, midrun: lsGet('jx.midrun', 'queue') }),
    });
    if (!d.accepted) { pendingHint(view, '发送失败：该会话当前不接受消息'); return; }
    // 本地立即出效果（插话贴到对话末尾，排队进坞）；服务端的 pending 广播可能
    // 先于本次响应到达，按 id 去重，避免同一条显示两次
    const known = view.state.pendingSteer.concat(view.state.pendingQueue).some((p) => p.id === d.id);
    if (!known) {
      const pm = { id: d.id, content: content, createdAt: '' };
      if (d.mode === 'steer') view.state.pendingSteer.push(pm);
      else view.state.pendingQueue.push(pm);
    }
    renderQueueDock(view);
    view._pinned = false; // 插话/排队展示在对话末尾：恢复跟随看得到回执
    renderMessages(view.state.msgs, view);
    pendingHint(view, d.mode === 'steer' ? '已插话：下一次调用模型前注入生效' : '已排队：本轮结束后自动发送');
  } catch (e) {
    pendingHint(view, '发送失败：' + e.message);
    return;
  }
  if (viaInput) {
    view.input.value = ''; autoGrow(view.input);
    if (view === mainView) clearSessionDraftLS(view.state.cur.id); else clearSubDraftLS(view.state.cur.id);
  }
}

async function send(forceContent, view) {
  view = view || mainView;
  // 发送前先停语音听写（语音组件交出的钩子：把未定稿的灰字转写同步落进输入框
  // 再硬收尾，随本条消息一起发出）。挂在 send 执行点：回车 / Ctrl+回车 / 发送
  // 按钮 / 子对话，所有发送路径都会经过这里；没在听写时是空操作。
  if (window.JXVoicePause) window.JXVoicePause();
  const input = view.input;
  const fromInput = typeof forceContent !== 'string';
  const content = (fromInput ? input.value : forceContent).trim();
  const staged = attachmentsOf(view);
  if (!content) {
    // 只挂了附件、没写字：不替用户编消息，提示补齐再发（附件留在输入框上方）
    if (staged.length) attachHint(view, '写一句要让数字助理做什么，' + staged.length + ' 个附件会随这条消息一起上传');
    return;
  }
  if (view._attachFlush) return; // 附件正在落盘：忽略重复点发送
  if (view.state.streaming) {
    // 执行中插话/排队：附件先在当前会话落盘（通知照旧），再按原路插话或排队
    if (staged.length && !(await flushAttachments(view))) return;
    await sendMidrun(content, fromInput, view);
    return;
  }
  // 这条消息是不是从首页（新对话）发出的：决定了发送后清哪份草稿
  const fromHome = view === mainView && !view.state.cur;
  if (!view.state.cur) {
    if (view === mainView) {
      // 懒创建：发出第一条消息才真正建会话（侧栏不再出现空会话，首页不再一闪而过）
      const s = await api('/api/sessions', {
        method: 'POST',
        body: JSON.stringify({
          mode: CHAT_MODES.some((m) => m.v === homeDraft.mode) ? homeDraft.mode : 'task',
          workspace: homeDraft.workspace || '',
          remote: homeDraft.remote || null,
          permission: homeDraft.permission,
          provider: homeDraft.provider,
          model: homeDraft.model,
          thinking: homeDraft.thinking || '',
          planMode: homeDraft.planMode,
          goalMode: homeDraft.goalMode,
          goalText: homeDraft.goalText,
          tools: homeDraft.tools,
        }),
      });
      state.cur = s;
      placeComposer();
      syncComposer();
      refreshDrawer();
      loadSessions();
    } else {
      // 没选中子对话：弹出下拉让用户先选/新建，输入内容保留
      renderSubDDPop();
      openPopover('sub-dd-pop', $('sub-dd'));
      view.hint.textContent = '先选择或新建一个子对话';
      setTimeout(() => { view.hint.textContent = ''; }, 2500);
      return;
    }
  }
  // 附件落盘：暂存的文件先写进工作区（后端照旧注入【文件上传】通知，排在用户消息
  // 之前）；有失败就中止本轮——输入内容与失败项都留着，让用户重试或移除
  if (attachmentsOf(view).length) {
    view.state.attachSid = view.state.cur.id;
    if (!(await flushAttachments(view))) return;
  }

  if (fromInput) {
    input.value = '';
    autoGrow(input);
    // 发出即清：首页清 homeDraft 的 text，主对话清 jx.st.<id>，子对话清 jx.subst.<id>
    if (view === mainView) {
      if (fromHome) clearHomeDraftLS();
      else clearSessionDraftLS(view.state.cur.id);
    } else {
      clearSubDraftLS(view.state.cur.id);
    }
  }

  const sid = view.state.cur.id; // 这条流所属的会话：切走后它的帧与收尾都不再动界面
  view.state.streaming = true;
  view._pinned = false; // 自己发的消息：恢复跟随，输出落地即到底
  setStreamUI(true, view);
  view.state.abort = new AbortController();
  view.state.streamText = '';
  view.state.streamThinking = '';
  hideRunError(view); // 新一轮开始：上一轮的报错条收掉
  // 立显「发送中」占位：回显落地前（远程/慢网可数秒）消息不悬空；带 sid，
  // 切走自失效，message 事件的用户回显落地即换正常气泡
  view.state.pendingSend = { sid, content };
  renderMessages(view.state.msgs, view);
  if (view === mainView) JX.emit('send', { content });

  let aborted = false;
  try {
    const res = await fetch(`/api/sessions/${sid}/chat`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ content, midrun: lsGet('jx.midrun', 'queue') }),
      signal: view.state.abort.signal,
    });
    if (!res.ok) {
      const d = await res.json().catch(() => ({}));
      throw new Error(d.error || res.statusText);
    }
    await consumeStream(res, view, sid);
  } catch (e) {
    if (e.name === 'AbortError') {
      aborted = true; // 用户主动停止（已另行请求后端停止）：排队消息保留
    } else if (!view.state.cur || view.state.cur.id !== sid) {
      // 请求失败的信息晚于切走/回首页到达：界面归新导航管，本轮不作任何界面动作
      // （旧代码这里会拿空 msgs 盖掉首页问候区、对空 cur 取 id 还会抛错）
    } else {
      // 流断了不等于执行结束：后端执行体独立于这条连接，先对一次状态再决定提示
      view.state.streamText = '';
      view.state.streamThinking = '';
      view.state.pendingSend = null; // 这轮的去向马上有定论：占位气泡退场
      renderMessages(view.state.msgs, view);
      if (await backendRunning(view.state.cur.id, view)) {
        // 后端刚被别的路径拉起执行（自动汇报/排队排水/别的窗口在发），这条消息
        // 被拒了：降级为插话/排队（在服务端排队，不丢），endTurn 会接上那轮继续看
        await sendMidrun(content, false, view);
      } else {
        // 整轮没发出去（请求根本没到服务端，消息没落库）：内容退回输入框不丢字，
        // 报错条给出原因；用户改完重发即可（别用「重试」——库里没有这条消息）
        if (fromInput && !input.value) { input.value = content; autoGrow(input); }
        showRunError(view, '请求失败：' + e.message);
      }
    }
  }
  await endTurn(view, aborted, { sid, abort: view.state.abort });
}

/* ---------- 编辑历史用户消息并重发（语义对齐 ZCode 的 editUserQuery：
   同会话内截断后重发——该消息之后的记录从当前对话移除、模型回到这条消息
   重新执行。两种提交档（对齐 ZCode 的双提交按钮）：
   - 发送：仅重置对话，文件保持现状；
   - 回退文件并发送：把这条消息之后有快照的文件一并恢复到当时（服务端
     fail-closed 校验，被外部改过的文件会整单拒绝并给出逃生口）。
   想保留原路线，先用助手回答下的「分支」按钮把对话另存一份再编辑。 ---------- */

// 编辑点之后将被回退的文件清单（同一文件按最早一条快照算状态），确认框用
function rewindFileList(view, seq) {
  const byPath = new Map();
  for (const c of (view.state.checkpoints || [])) {
    if (c.seq <= seq) continue;
    if (!byPath.has(c.path)) byPath.set(c.path, c);
  }
  const restore = [], drop = [];
  for (const c of byPath.values()) (c.existedBefore ? restore : drop).push(c.path);
  return { restore, drop };
}

// 进入编辑态：气泡原位换成输入框（Esc/取消还原，Ctrl+Enter 或按钮提交）
function editUserMsg(m, view, wrap) {
  if (wrap.classList.contains('editing')) return;
  if (view.state.streaming) {
    view.hint.textContent = '本轮执行中，等结束再编辑';
    setTimeout(() => { if (view.hint.textContent === '本轮执行中，等结束再编辑') view.hint.textContent = ''; }, 2500);
    return;
  }
  wrap.classList.add('editing');
  const box = document.createElement('div');
  box.className = 'ed-box';
  const ta = document.createElement('textarea');
  ta.className = 'ed-input';
  ta.value = String(m.content ?? '');
  ta.rows = 3;
  const row = document.createElement('div');
  row.className = 'ed-row';
  const cancel = document.createElement('button');
  cancel.className = 'ed-btn';
  cancel.type = 'button';
  cancel.textContent = '取消';
  const rewind = document.createElement('button');
  rewind.className = 'ed-btn rewind';
  rewind.type = 'button';
  rewind.title = '恢复这条消息之后的文件改动（按快照校验），再重置对话并发送';
  rewind.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3.5 2"/></svg>回退文件并发送';
  const ok = document.createElement('button');
  ok.className = 'ed-btn primary';
  ok.type = 'button';
  ok.textContent = '发送';
  row.appendChild(cancel);
  row.appendChild(rewind);
  row.appendChild(ok);
  const note = document.createElement('div');
  note.className = 'ed-note';
  note.textContent = '「发送」仅重置对话；「回退文件并发送」会同时把之后的文件改动恢复到当时（未做快照的文件无法回退）';
  box.appendChild(ta);
  box.appendChild(row);
  box.appendChild(note);
  wrap.appendChild(box);
  ta.focus();
  ta.setSelectionRange(ta.value.length, ta.value.length);
  const close = () => { wrap.classList.remove('editing'); box.remove(); };
  cancel.onclick = close;
  ta.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && $('confirm-modal').classList.contains('hidden')) {
      e.stopPropagation();
      close();
    }
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); ok.click(); }
  });
  const submit = async (mode) => {
    const content = ta.value.trim();
    if (!content) return false;
    const later = view.state.msgs.filter((x) => x.seq > m.seq).length;
    if (later > 0 || mode === 'rewind') {
      let msg = `这条消息之后还有 ${later} 条记录，重新发送后将全部移除（要保留原路线请先用「分支」）。`;
      if (mode === 'rewind') {
        const { restore, drop } = rewindFileList(view, m.seq);
        const parts = [];
        if (restore.length) parts.push('恢复 ' + restore.length + ' 个文件（' + restore.join('、') + '）');
        if (drop.length) parts.push('撤回 ' + drop.length + ' 个新建文件（' + drop.join('、') + '）');
        if (!parts.length) parts.push('这条消息之后没有可回退的文件快照（只重置对话）');
        msg = '将回退文件并重发：\n· ' + parts.join('\n· ') + '\n同时' + msg;
      } else {
        msg += '\n已做的文件修改不回退；要连文件一起回退请用「回退文件并发送」。';
      }
      if (!(await confirmCenter(msg))) return false;
    }
    ok.disabled = true;
    rewind.disabled = true;
    cancel.disabled = true;
    if (await resendEdit(m, content, view, mode)) { close(); return true; }
    ok.disabled = false;
    rewind.disabled = false;
    cancel.disabled = false;
    return false;
  };
  ok.onclick = () => submit('preserve');
  rewind.onclick = () => submit('rewind');
}

// 编辑重发的请求与收流：结构与 send() 的发后处理一致；成功返回 true（此时
// edited 确认帧已把本地记录截断对齐），失败/中止返回 false 保留编辑框。
// mode=rewind 时文件被外部改过会收到 blocked：就地给出「改为仅重发对话」逃生口
async function resendEdit(m, content, view, mode) {
  view = view || mainView;
  if (!view.state.cur || view.state.streaming) return false;
  const sid = view.state.cur.id; // 这条流所属的会话：切走后它的帧与收尾都不再动界面
  view.state.streaming = true;
  view._pinned = false; // 编辑重发也是自己的动作：恢复跟随
  setStreamUI(true, view);
  view.state.abort = new AbortController();
  view.state.streamText = '';
  view.state.streamThinking = '';
  if (view === mainView) JX.emit('send', { content });
  let aborted = false;
  // blocked（文件被外部改过）时给逃生口：改按 preserve 档就地重发一次
  const postEdit = async (wm) => {
    const res = await fetch(`/api/sessions/${sid}/edit`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ seq: m.seq, content, workspaceMode: wm }),
      signal: view.state.abort.signal,
    });
    if (!res.ok) {
      const d = await res.json().catch(() => ({}));
      if (d.blocked && wm === 'rewind') {
        const list = (d.unsafe || []).map((u) => u.path + '：' + (u.reason || '被外部修改')).join('\n');
        const fallback = await confirmCenter('以下文件在快照之后被外部修改，无法安全回退：\n' + list + '\n\n改为仅重发对话（不动这些文件）？');
        if (fallback) return postEdit('preserve');
      }
      throw new Error(d.error || res.statusText);
    }
    await consumeStream(res, view, sid);
    return true;
  };
  try {
    await postEdit(mode === 'rewind' ? 'rewind' : 'preserve');
  } catch (e) {
    if (e.name === 'AbortError') {
      aborted = true; // 用户主动停止：保留编辑框，可改完再试
    } else {
      view.state.streamText = '';
      view.state.streamThinking = '';
      showRunError(view, '编辑发送失败：' + e.message); // 真实失败进报错条，不插进对话区
    }
  }
  await endTurn(view, aborted, { sid, abort: view.state.abort });
  return !aborted;
}

/* ---------- 事件流读取与「接回执行中会话」 ---------- */

// consumeStream 读一条 SSE 响应直到流结束，每帧交给 handleEvent。
// sid 是这条流所属的会话：中途切到别的会话后本函数立刻收手——帧不能再写进
// 新会话的界面（后端执行不受影响，回来时重新接上即可）。
async function consumeStream(res, view, sid) {
  const reader = res.body.getReader();
  const dec = new TextDecoder();
  let buf = '';
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    if (sid && (!view.state.cur || view.state.cur.id !== sid)) return; // 已切走：丢弃
    buf += dec.decode(value, { stream: true });
    let idx;
    while ((idx = buf.indexOf('\n\n')) >= 0) {
      const frame = buf.slice(0, idx).trim();
      buf = buf.slice(idx + 2);
      if (!frame.startsWith('data:')) continue;
      try { handleEvent(JSON.parse(frame.slice(5)), view); } catch (e) { /* 跳过坏帧 */ }
    }
  }
}

// detachStream 放开当前这条本地流：只断开界面侧的读取，后端执行照旧跑完；
// 换会话/回首页/新建对话前必须调用，否则旧会话的增量会写进新会话的界面。
function detachStream(view) {
  view = view || mainView;
  if (!view.state.streaming) return;
  const abort = view.state.abort;
  view.state.streaming = false;
  view.state.attached = false;
  view.state.abort = null;
  view.state.pendingSend = null; // 切走即弃占位：回来时以服务端落库为准（endTurn 不会再跑）
  view.state.streamText = '';
  view.state.streamThinking = '';
  removeLive(view);
  setStreamUI(false, view);
  if (abort) { try { abort.abort(); } catch (e) { /* 已经断了 */ } }
}

async function backendRunning(sid, view) {
  const v = view || mainView;
  try {
    const d = await api('/api/sessions/' + sid);
    // 慢网下响应可能晚于导航：pending 属于被探测的会话，只对仍停在它上面的界面生效
    if (d.pending && v.state.cur && v.state.cur.id === sid) applyPending(v, d.pending);
    return !!(d.running || (d.meta && d.meta.status === 'running'));
  } catch (e) { return false; }
}

// refreshIfDangling 流断开但后端仍在执行：重新接上那条流继续看（刷新/网络抖动都不再打断对话）
async function refreshIfDangling(view) {
  const sid0 = view.state.cur && view.state.cur.id;
  if (!sid0) return;
  if (!(await backendRunning(sid0, view))) return;
  if (!view.state.cur || view.state.cur.id !== sid0) return; // 探测期间已切走：不接流
  attachRun(view);
}

// reloadMessages 只重取当前会话的消息与待生效队列（不触发接流，避免与 attachRun 互相递归）
async function reloadMessages(view) {
  view = view || mainView;
  if (!view.state.cur) return;
  if (view.state._loading) return; // 会话正在整包加载：由 loadSessionInto 收口，不并发重取
  const nav = navTok[view.key]; // 导航令牌：慢网下响应可能晚于切走/回首页，落地前必须复验
  try {
    const d = await api('/api/sessions/' + view.state.cur.id);
    // 已被更新的导航取代：晚到的旧会话数据不落地——否则 state.cur 被旧 meta 复活、
    // 界面被旧消息顶回旧会话，正是远程访问时「切过去又被跳回来」的根源
    if (nav !== navTok[view.key]) return;
    view.state.cur = d.meta;
    view.state.msgs = d.messages || [];
    view.state.pendingSteer = (d.pending && d.pending.steer) || [];
    view.state.pendingQueue = (d.pending && d.pending.queue) || [];
    view.state.checkpoints = d.checkpoints || [];
    view.state.deliveries = d.deliveries || [];
    // 上一轮失败过就继续显示报错条（刷新/切换回来都还在，方便接着重试）
    if (d.error) showRunError(view, d.error);
  } catch (e) { return; /* 取不到就保持现状 */ }
  renderQueueDock(view);
  renderMessages(view.state.msgs, view);
}

// attachRun 接上一个正在执行的会话的事件流（页面刷新/流断开后继续跟随输出）
async function attachRun(view) {
  view = view || mainView;
  const cur = view.state.cur;
  if (!cur || view.state.streaming) return;
  view.state.streaming = true;
  view.state.attached = true;
  const abort = new AbortController();
  view.state.abort = abort;
  view.state.streamText = '';
  view.state.streamThinking = '';
  hideRunError(view); // 执行还在跑：报错条收掉
  setStreamUI(true, view);
  await reloadMessages(view); // 先对齐到服务端现状，再接流
  try {
    const res = await fetch(`/api/sessions/${cur.id}/stream`, { signal: abort.signal });
    if (!res.ok || !res.body) throw new Error(res.statusText || '无法接上执行流');
    await consumeStream(res, view, cur.id);
  } catch (e) {
    if (e.name !== 'AbortError') console.warn('[JX] 接回执行流失败', e);
  }
  // 中途切到别的会话（detachStream）/ 又接了别的流：这条流的收尾不再动界面
  if (view.state.abort !== abort) return;
  removeLive(view);
  view.state.streaming = false;
  view.state.attached = false;
  setStreamUI(false, view);
  // 执行可能在这一段里结束了：重取一次，落库内容与待生效队列都归位
  if (view.state.cur && view.state.cur.id === cur.id) await reloadMessages(view);
  if (view === mainView) {
    loadSessions();
    refreshCtxChip();
  } else {
    refreshDrawer();
    refreshCtxChip(subView);
  }
  // 本轮可能是排队消息驱动的：它发完还有下一条，继续跟着看
  if (view.state.cur && view.state.cur.id === cur.id && view.state.abort === abort) {
    if (await backendRunning(cur.id, view)) attachRun(view);
  }
}

// stopRun 显式停止：后端执行体独立于页面，必须让服务端停——本地只是等着这条流自己收尾
// （取消会立刻作用到模型流/工具/等待，服务端写入的收尾记录也就随流到达）。
async function stopRun(view) {
  view = view || mainView;
  const cur = view.state.cur;
  if (!cur) return;
  view.hint.textContent = '正在停止…';
  try {
    const r = await api('/api/sessions/' + cur.id + '/stop', { method: 'POST' });
    if (r && r.stopped === false) { view.hint.textContent = ''; return; }
  } catch (e) {
    view.hint.textContent = '';
    return;
  }
  // 兜底：5 秒还没收尾（网络异常/执行体卡住）就断掉本地流并重新对齐一次
  const token = view.state.abort;
  setTimeout(async () => {
    if (view.state.streaming && view.state.abort === token) {
      if (view.state.abort) view.state.abort.abort();
      await reloadMessages(view);
    }
  }, 5000);
}

/* ---------- 上下文用量：圆环指示器（模型选择左侧）+ 悬停明细 ---------- */

const CTX_COLORS = { '消息': '#4d6bfe', '系统工具': '#1f9d55', '系统提示词': '#9a6700', '摘要': '#8b5cf6', '环境快照': '#0ea5a5', '其他': '#8b949e' };
const CTX_CIRC = 97.39; // 2πr，r=15.5
let lastCtx = { main: null, sub: null }; // 每个输入区视图各自的上下文用量（圆环+明细共用）

function fmtTokens(n) {
  if (n >= 10000) return (Math.round(n / 1000) / 10).toFixed(1) + '万';
  if (n >= 1000) return (Math.round(n / 100) / 10).toFixed(1) + 'k';
  return String(n);
}

async function refreshCtxChip(view) {
  view = view || mainView;
  const ring = viewEl(view, 'ctx-ring');
  if (!ring) return;
  const num = viewEl(view, 'ctx-num'), tip = viewEl(view, 'ctx-tip');
  const cur = view.state.cur;
  if (!cur) {
    lastCtx[view.key] = null;
    ring.classList.remove('warn', 'full');
    ring.querySelector('.cr-fg').style.strokeDashoffset = CTX_CIRC;
    num.textContent = '–';
    tip.innerHTML = '<div class="ctx-title">当前没有会话</div>';
    return;
  }
  try {
    lastCtx[view.key] = await api('/api/sessions/' + cur.id + '/context');
    const d = lastCtx[view.key];
    const pct = d.window ? Math.min(d.total / d.window, 1) : 0;
    ring.classList.toggle('warn', pct >= .6 && pct < .85);
    ring.classList.toggle('full', pct >= .85);
    ring.querySelector('.cr-fg').style.strokeDashoffset = CTX_CIRC * (1 - pct);
    num.textContent = Math.max(1, Math.round(pct * 100)) + '%';
    renderCtxTip(view);
  } catch (e) { /* 拿不到就保持原样 */ }
}

// 悬停明细：总量/窗口 + 占用条（整条=窗口，已用从左往右按窗口占比增长，
// 灰色轨道=未占用）+ 分类清单（share=占已用量比例）+ 缓存命中率
function renderCtxTip(view) {
  view = view || mainView;
  const tip = viewEl(view, 'ctx-tip');
  const d = lastCtx[view.key];
  if (!d) { tip.innerHTML = '<div class="ctx-title">当前没有会话</div>'; return; }
  const pct = d.window ? Math.round(d.total * 100 / d.window) : 0;
  let html =
    '<div style="display:flex;align-items:baseline;justify-content:space-between">' +
      '<span class="ctx-title">上下文容量</span>' +
      `<span class="ctx-total">${fmtTokens(d.total)}/${fmtTokens(d.window)}（${pct}%）</span>` +
    '</div><div class="ctx-bar" title="已用 ' + pct + '%，灰色为未占用">';
  for (const c of d.categories || []) {
    if (!c.tokens || !d.window) continue;
    const w = (c.tokens * 100 / d.window).toFixed(2); // 段宽=占窗口比例，未占用留给灰色轨道
    html += `<i style="width:${w}%;background:${CTX_COLORS[c.name] || '#8b949e'}"></i>`;
  }
  html += '</div>';
  for (const c of d.categories || []) {
    html +=
      `<div class="ctx-cat"><span class="ctx-dot" style="background:${CTX_COLORS[c.name] || '#8b949e'}"></span>` +
      `<span class="cc-name">${esc(c.name)}</span><span class="cc-tok">${fmtTokens(c.tokens)}</span>` +
      `<span class="cc-pct">${c.share}%</span></div>`;
  }
  // Token 用量分组（截图样式）：命中%按输入口径；未缓存/缓存读取/输出按 token 累计
  const c = d.cache || {};
  const has = c.calls > 0;
  const tokFull = (n) => (n || 0).toLocaleString('en-US') + ' tok';
  const rate = has && c.promptTokens ? Math.round(c.cacheHit * 100 / c.promptTokens) + '%' : '—';
  html +=
    `<div class="ctx-tok-head"><span>Token 用量</span><b>${has ? tokFull((c.promptTokens || 0) + (c.output || 0)) : '—'}</b></div>` +
    `<div class="ctx-cat"><span class="cc-name">缓存命中</span><span class="cc-val">${rate}</span></div>` +
    `<div class="ctx-cat"><span class="cc-name">未缓存输入</span><span class="cc-val">${has ? tokFull(c.uncached) : '—'}</span></div>` +
    `<div class="ctx-cat"><span class="cc-name">缓存读取</span><span class="cc-val">${has ? tokFull(c.cacheHit) : '—'}</span></div>` +
    `<div class="ctx-cat"><span class="cc-name">输出</span><span class="cc-val">${has ? tokFull(c.output) : '—'}</span></div>`;
  tip.innerHTML = html;
}

/* ---------- 对话区选中文字浮窗：复制 / 引用到下方输入框 / 右侧辅助问答 ---------- */

let selKeepText = '';

// 仅识别主对话区与右栏子对话区里的文字选区
function chatSelection() {
  const sel = window.getSelection();
  if (!sel || sel.isCollapsed || sel.rangeCount === 0) return null;
  const text = sel.toString().trim();
  if (!text) return null;
  const n = sel.anchorNode;
  const anchor = n && (n.nodeType === 1 ? n : n.parentElement);
  if (!anchor || !anchor.closest('#messages, #sub-messages')) return null;
  const rect = sel.getRangeAt(0).getBoundingClientRect();
  if (!rect.width && !rect.height) return null;
  return { text, rect };
}

function hideSelPop() { $('sel-pop').classList.add('hidden'); }

function showSelPop() {
  const s = chatSelection();
  if (!s) { hideSelPop(); return; }
  selKeepText = s.text;
  const pop = $('sel-pop');
  pop.classList.remove('hidden');
  const pw = pop.offsetWidth, ph = pop.offsetHeight;
  let x = s.rect.left + s.rect.width / 2 - pw / 2;
  x = Math.max(8, Math.min(x, window.innerWidth - pw - 8));
  let y = s.rect.top - ph - 8;
  if (y < 8) y = s.rect.bottom + 8;
  pop.style.left = x + 'px';
  pop.style.top = y + 'px';
}

// 选中文字送去右侧辅助子对话：不切主对话、不自动发送（填入子对话输入框由用户发出）
async function askInAux(text) {
  if (!state.cur) return;
  rpOpen('subs');
  // 复用已有的子对话（人工建的都算，含改造前建的老 aux）
  let aux = subKids.find((s) => s.mode === 'aux' || s.origin === 'human');
  if (!aux) {
    const fid = drawerFamilyId();
    if (!fid) return;
    try {
      aux = await api('/api/sessions', {
        method: 'POST',
        body: JSON.stringify({ parent: fid, title: '子对话', inheritContext: true }),
      });
      await refreshDrawer();
    } catch (e) {
      alert('新建子对话失败：' + e.message);
      return;
    }
  }
  await openSubSession(aux.id);
  const inp = $('sub-input');
  inp.value = inp.value ? inp.value + '\n\n' + text : text;
  autoGrow(inp);
  persistSubDraftLS(); // 程序化填入也要落盘，切走/重启不丢
  inp.focus();
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

function currentModelKey(view) {
  const cur = (view || mainView).state.cur;
  if (cur) {
    return cur.provider && cur.model ? cur.provider + '|' + cur.model : '__default__';
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

const THINK_LABELS = { enabled: '思考·开', disabled: '思考·关', low: '思考·低', medium: '思考·中', high: '思考·高', max: '思考·深' };

// 当前生效的思考档位（会话级 / 首页草稿 / 指定视图的会话）
function curThinking(view) {
  const cur = (view || mainView).state.cur;
  return cur ? (cur.thinking || '') : (homeDraft.thinking || '');
}

async function pickModel(key, keepOpen, view) {
  view = view || mainView;
  if (!keepOpen) closePopovers();
  const body = key === '__default__' ? { provider: '', model: '' } : {
    provider: key.split('|')[0], model: key.split('|')[1],
  };
  if (view.state.cur) {
    try {
      view.state.cur = await api('/api/sessions/' + view.state.cur.id, { method: 'PATCH', body: JSON.stringify(body) });
    } catch (e) { alert('切换模型失败：' + e.message); }
  } else if (view === mainView) {
    if (key === '__default__') {
      homeDraft.provider = ''; homeDraft.model = '';
    } else {
      homeDraft.provider = key.split('|')[0];
      homeDraft.model = key.split('|')[1];
    }
  }
  syncComposer(view);
}

// 设置思考档位（空串=跟随服务端默认）
async function pickThinking(level, view) {
  view = view || mainView;
  closePopovers();
  if (view.state.cur) {
    try {
      view.state.cur = await api('/api/sessions/' + view.state.cur.id, { method: 'PATCH', body: JSON.stringify({ thinking: level }) });
    } catch (e) { alert('设置思考档位失败：' + e.message); return; }
  } else if (view === mainView) {
    homeDraft.thinking = level;
    persistHomeDraftLS();
  }
  syncComposer(view);
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

function renderModelPop(view) {
  view = view || popView || mainView;
  const pop = $('model-pop');
  const cur = currentModelKey(view);
  const thinking = curThinking(view);
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
      await pickModel(key, true, popView);
      modelPopExpand = modelPopExpand === key ? '' : key;
      renderModelPop(popView);
    };
  });
  pop.querySelectorAll('.mp-lv').forEach((b) => {
    b.onclick = (e) => { e.stopPropagation(); pickThinking(b.dataset.lv, popView); };
  });
}

/* ---------- 弹出面板系统：加号菜单 / 权限面板 / 模型面板 / 目录面板 ---------- */

const PERM_LABELS = { 'read-only': '只读', 'workspace-write': '工作区内读写', 'full': '完全访问' };
// 新对话可选的工作模式（后端 systemPrompt/toolsFor 按 mode 分流）。
// 扩展模式（设计人员等）＝在这里加一项 + 后端加对应提示词分支，
// 其余机制（胶囊、下拉、会话流转、徽章）完全复用；数字助理主对话是专属模式，不在此列。
const CHAT_MODES = [
  { v: 'office', label: '标准办公', desc: '面向日常办公：文档、资料、表格、沟通文案，成果落文件' },
  { v: 'coding', label: '编程', desc: '面向软件开发：读改代码、编译构建、调试排障，成果直接落项目文件' },
  { v: 'skill-create', label: '创建技能', desc: '看用户演示操作（自动截屏＋录音）→ 总结成给其他 AI 照做的技能教程；默认完全访问' },
];
const chatModeOf = (v) => CHAT_MODES.find((m) => m.v === v) || { v: 'office', label: '标准办公', desc: '' };

// applyDraftMode：切换首页工作模式，带「创建技能」的权限联动——该模式要
// 看懂用户演示（截屏/键鼠记录）并亲自验证技能步骤，操作电脑/浏览器是刚需，
// 选中即默认完全访问（用户仍可在权限面板改回）；切去别的模式且权限还是
// 本联动自动设的完全访问时，还原为工作区内读写。FULL_BUNDLED_TOOLS 的
// 勾选随权限档联动（与权限面板同一套规则）。
function applyDraftMode(v) {
  const fullByAuto = homeDraft.permission === 'full' && homeDraft._fullFromMode;
  if (v === 'skill-create' && homeDraft.permission !== 'full') {
    homeDraft._fullFromMode = true;
    homeDraft.permission = 'full';
    homeDraft.tools = [...new Set([...(homeDraft.tools || []), ...FULL_BUNDLED_TOOLS])];
  } else if (v !== 'skill-create' && fullByAuto) {
    homeDraft._fullFromMode = false;
    homeDraft.permission = 'workspace-write';
    homeDraft.tools = applyCapDefaults((homeDraft.tools || []).filter((t) => !FULL_BUNDLED_TOOLS.includes(t)), v, 'workspace-write');
  }
  homeDraft.mode = v;
}

// 模式下拉：选项列表 + 当前项打勾
function renderModePop() {
  const box = $('mode-pop-list');
  if (!box) return;
  box.innerHTML = '';
  for (const m of CHAT_MODES) {
    const b = document.createElement('button');
    b.type = 'button';
    b.className = 'pop-item' + (homeDraft.mode === m.v ? ' sel' : '');
    b.innerHTML =
      '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9z"/></svg>' +
      '<span class="mp2-text"><b>' + esc(m.label) + '</b><i>' + esc(m.desc) + '</i></span>' +
      (homeDraft.mode === m.v ? '<span class="pop-check">✓</span>' : '');
    b.onclick = () => {
      applyDraftMode(m.v);
      syncComposer();
      closePopovers();
    };
    box.appendChild(b);
  }
}

function sessionOrDraftFor(view) {
  view = view || mainView;
  const cur = view.state.cur;
  if (cur) {
    return {
      permission: cur.permission || 'workspace-write',
      tools: cur.tools || [],
      planMode: !!cur.planMode,
      goalMode: !!cur.goalMode,
      goalText: cur.goalText || '',
    };
  }
  if (view === mainView) return homeDraft; // 主对话区没会话时 = 首页草稿
  // 右栏没选中子对话：给一份中性默认，避免串到首页草稿
  return { permission: 'workspace-write', tools: [], planMode: false, goalMode: false, goalText: '' };
}

function sessionOrDraft() { return sessionOrDraftFor(mainView); }

async function patchDraftOrSession(fields, view) {
  view = view || mainView;
  const cur = view.state.cur;
  if (cur) {
    try {
      view.state.cur = await api('/api/sessions/' + cur.id, { method: 'PATCH', body: JSON.stringify(fields) });
    } catch (e) { alert('保存失败：' + e.message); }
  } else if (view === mainView) {
    Object.assign(homeDraft, fields);
  }
  syncComposer(view);
}

// 同步 composer 芯片显示（权限/模式徽标/模型）——按视图更新对应那套控件
function syncComposer(view) {
  view = view || mainView;
  const vcur = view.state.cur;
  const s = sessionOrDraftFor(view);
  const remote = vcur ? !!vcur.remote : (view === mainView && !!homeDraft.remote);
  viewEl(view, 'perm-chip-label').textContent = (PERM_LABELS[s.permission] || s.permission) + (remote ? ' ·远程' : '');
  const tk = curThinking(view);
  viewEl(view, 'model-chip-label').textContent = modelNameOf(currentModelKey(view)) + (tk ? ' · ' + (THINK_LABELS[tk] || tk) : '');
  viewEl(view, 'flag-plan').classList.toggle('on', s.planMode);
  viewEl(view, 'flag-goal').classList.toggle('on', s.goalMode);
  viewEl(view, 'flag-goal').title = s.goalMode ? ('目标模式：' + (s.goalText || '（未填写目标）')) : '目标模式';
  viewEl(view, 'mode-flags').classList.toggle('hidden', !s.planMode && !s.goalMode);
  if (vcur) viewEl(view, 'input').placeholder = sendKeyHint(); // 占位跟随发送快捷键设置
  refreshCtxChip(view);
  renderAttachments(view); // 附件区跟随视图/会话（换会话时放掉上一轮的附件）
  // 以下只属于主对话区（首页草稿 / 顶栏徽标 / 目录胶囊）
  if (view !== mainView) return;
  syncPlusChecks(view);
  // 顶栏远程徽标（挂了远程工作区的会话）
  const rb = $('remote-badge');
  if (state.cur && state.cur.remote) {
    rb.textContent = '远程 ' + remoteDisplay(state.cur.remote);
    rb.title = 'SSH 远程工作区 ' + remoteDisplay(state.cur.remote) + '（点击配置/解除）';
    rb.classList.remove('hidden');
  } else {
    rb.classList.add('hidden');
  }
  // 目录行/模式行只在新建对话（无会话）时出现；窄屏（手机）同样提供工作目录
  // 选择——新建对话要能换已有工作区/远程挂载，只有「添加工作目录」这步由
  // toggleWsPopAddNotice 就地告知（原生选择框弹在宿主机屏幕上手机等不到）
  const wsRow = $('ws-row');
  const modeRow = $('mode-row');
  if (!state.cur) {
    wsRow.classList.remove('hidden');
    modeRow.classList.remove('hidden');
    $('ws-row-name').textContent = homeDraft.remote
      ? remoteDisplay(homeDraft.remote)
      : (baseName(homeDraft.workspace || state.defaultWs || '') || '工作目录');
    $('mode-row-name').textContent = chatModeOf(homeDraft.mode).label;
  } else {
    wsRow.classList.add('hidden');
    modeRow.classList.add('hidden');
  }
  $('input').placeholder = sendKeyHint(); // 占位跟随发送快捷键设置（首页/会话同一提示）
  persistHomeDraftLS(); // 所有选择改动都会走到这里，统一落一次本地草稿
}

// remoteDisplay：user@host[:port]:dir
function remoteDisplay(r) {
  if (!r || !r.host) return '';
  const user = r.user || 'root';
  const port = (r.port && r.port !== 22) ? ':' + r.port : '';
  return user + '@' + r.host + port + ':' + (r.dir || '/');
}

// 加号菜单是单例：打开前按发起它的视图刷新勾选态，并记住归属（popView）
function syncPlusChecks(view) {
  const s = sessionOrDraftFor(view);
  const cur = view.state.cur;
  const remote = cur ? !!cur.remote : (view === mainView && !!homeDraft.remote);
  $('pm-plan-check').textContent = s.planMode ? '✓' : '';
  $('pm-goal-check').textContent = s.goalMode ? '✓' : '';
  $('pm-remote-check').textContent = remote ? '✓' : '';
}

function openPlusMenu(view) {
  popView = view;
  syncPlusChecks(view);
  openPopover('plus-menu', viewEl(view, 'btn-plus'));
}

// 完全访问预设捆绑的能力：切到完全访问即强制勾选、置灰不可关闭（后端权限引擎同样视为开启）
const FULL_BUNDLED_TOOLS = ['unsandboxed', 'browser', 'computer', 'device'];

// 默认能力（后端 perm.go 的 capDefaultsForMode 是同一策略的唯一权威，这里是镜像）：
// 「工作区内读写」默认开启「操作浏览器」，只对真会注册 browser_* 的四种模式生效；
// 只读 / 完全访问不带默认项。用在「切档时把默认项补回」——完全访问 ⇄ 工作区内读写、
// 创建技能模式 ⇄ 普通模式往返，都不能把默认能力丢掉（与后端 strip+merge 对齐）。
const CAP_DEFAULT_MODES = ['task', 'office', 'coding', 'skill-create'];
function applyCapDefaults(tools, mode, perm) {
  const out = [...(tools || [])];
  if (perm === 'workspace-write' && CAP_DEFAULT_MODES.includes(mode) && !out.includes('browser')) out.push('browser');
  return out;
}

function renderPermPop(view) {
  view = view || popView || mainView;
  const s = sessionOrDraftFor(view);
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

/* ---------- 加号菜单动作（按视图：作用于发起操作的那个输入区） ---------- */

let uploadTarget = mainView; // 文件选择框是单例：记住这次选择属于哪个视图

/* ---------- 附件区（输入框上方那条"本轮带进来的文件"轨道） ----------
   选择 / 粘贴 / 拖拽进来的文件先在这里暂存，点发送才真正写进工作区（写入后由后端
   照旧注入【文件上传】通知，通知文案与触发行为都不变）。没有附件时整块带 hidden：
   不占高度、不留白条、也没有空状态提示——有且只有"真有附件"时才出现。
   交互与布局对齐数字助理2 的 ui-attachment（ComposerAttachments / AttachmentRail：
   横向卡片轨道 + 图片缩略图 + 逐项移除 + 拖拽投放）。

   两种载荷与既有上传路径一一对应：桌面壳（本地会话）采集本机真实路径，发送时走
   JSON {path}（登记 uploads 表，该路径可作 deliver_file 交付目标）；其余（浏览器 /
   手机 / 远程 / 粘贴 / 拖拽）暂存 File 对象，发送时走 multipart。 */

const ATTACH_MAX_BYTES = 32 * 1024 * 1024; // 与后端 multipart 上限一致：超了入列时就提示
const ATTACH_MAX_COUNT = 20;               // 一条消息最多带 20 个附件

let attachSeq = 0; // 附件 id 序号（本次页面生命周期内递增，DOM 定位与移除用）

// 附件挂在视图状态上：主对话与右栏子对话各一份，互不相干
function attachmentsOf(view) {
  const st = (view || mainView).state;
  if (!st.attachments) st.attachments = [];
  return st.attachments;
}

// 输入框上方的提示条（附件相关提示都走它，到点自清）
function attachHint(view, text, ms) {
  view.hint.textContent = text;
  if (view._attachHintTimer) clearTimeout(view._attachHintTimer);
  view._attachHintTimer = setTimeout(() => { view.hint.textContent = ''; }, ms || 3200);
}

// 剪贴板来的图片没有文件名，按 MIME 给一个带时间戳的默认名
function pasteImageName(f) {
  const ext = { 'image/png': '.png', 'image/jpeg': '.jpg', 'image/jpg': '.jpg', 'image/gif': '.gif', 'image/webp': '.webp', 'image/bmp': '.bmp' }[f.type] || '.png';
  const d = new Date(), p = (n) => String(n).padStart(2, '0');
  return '粘贴图片-' + d.getFullYear() + p(d.getMonth() + 1) + p(d.getDate()) + '-' +
    p(d.getHours()) + p(d.getMinutes()) + p(d.getSeconds()) + ext;
}

// 桌面壳原生选择框：本机真实路径入列（发送时走 JSON {path}，不受 32MB 限制）
function stagePaths(paths, view) {
  view = view || popView || mainView;
  const list = attachmentsOf(view);
  let dropped = 0;
  for (const p of paths) {
    if (list.length >= ATTACH_MAX_COUNT) { dropped++; continue; }
    const name = String(p).split(/[\\/]/).pop() || String(p);
    list.push({
      id: 'a' + (++attachSeq), kind: 'file', name, size: null, mime: '',
      file: null, path: p, previewUrl: '', status: 'staged',
    });
  }
  if (view.state.cur && !view.state.attachSid) view.state.attachSid = view.state.cur.id;
  renderAttachments(view);
  if (dropped) attachHint(view, '一条消息最多带 ' + ATTACH_MAX_COUNT + ' 个附件，' + dropped + ' 个没有加入');
}

// 浏览器采集到的文件入列（File/Blob：发送时走 multipart）；粘贴与拖拽也走这里
function stageFiles(files, view) {
  view = view || uploadTarget || mainView;
  const list = attachmentsOf(view);
  let dropped = 0, tooBig = 0;
  for (const f of files) {
    if (list.length >= ATTACH_MAX_COUNT) { dropped++; continue; }
    if (f.size > ATTACH_MAX_BYTES) { tooBig++; continue; }
    const isImage = /^image\//.test(f.type || '');
    list.push({
      id: 'a' + (++attachSeq), kind: isImage ? 'image' : 'file',
      name: f.name || pasteImageName(f), size: f.size, mime: f.type || '',
      file: f, path: '', previewUrl: isImage ? URL.createObjectURL(f) : '', status: 'staged',
    });
  }
  if (view.state.cur && !view.state.attachSid) view.state.attachSid = view.state.cur.id;
  renderAttachments(view);
  if (tooBig) attachHint(view, tooBig + ' 个文件超过 ' + fmtBytes(ATTACH_MAX_BYTES) + '，没有加入');
  else if (dropped) attachHint(view, '一条消息最多带 ' + ATTACH_MAX_COUNT + ' 个附件，' + dropped + ' 个没有加入');
}

// 清空附件（换会话 / 发送完毕）：缩略图的 objectURL 一并释放
function clearAttachments(view) {
  const list = attachmentsOf(view);
  for (const a of list) if (a.previewUrl) URL.revokeObjectURL(a.previewUrl);
  list.length = 0;
  view.state.attachSid = null;
  const box = viewEl(view, 'attachments');
  if (box) { box._sig = null; box.innerHTML = ''; box.classList.add('hidden'); }
  syncComposerPad(view);
}

// 单张附件卡：图片给缩略图，其余给文件卡（图标 + 文件名 + 大小 + ✕）
function attachCard(view, a) {
  const card = document.createElement('div');
  card.className = 'attach-item' + (a.kind === 'image' ? ' image' : '') +
    (a.status === 'error' ? ' error' : (a.status === 'uploading' ? ' uploading' : ''));
  card.title = (a.path || a.name) + (a.size != null ? '（' + fmtBytes(a.size) + '）' : '') +
    (a.status === 'error' ? '\n上传失败：' + (a.error || '') : '');
  const rm = document.createElement('button');
  rm.type = 'button';
  rm.className = 'attach-x';
  rm.title = '移除（这条消息不会带上它）';
  rm.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.6" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg>';
  rm.onclick = (e) => { e.stopPropagation(); removeAttachment(view, a.id); };
  if (a.kind === 'image' && a.previewUrl) {
    const img = document.createElement('img');
    img.className = 'attach-thumb';
    img.src = a.previewUrl;
    img.alt = a.name;
    card.appendChild(img);
    card.appendChild(rm);
    return card;
  }
  const icon = document.createElement('span');
  icon.className = 'attach-icon';
  icon.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/></svg>';
  const name = document.createElement('b');
  name.className = 'attach-name';
  name.textContent = a.name;
  const size = document.createElement('span');
  size.className = 'attach-size';
  size.textContent = a.status === 'uploading' ? '上传中…' : (a.status === 'error' ? '未上传' : (a.size != null ? fmtBytes(a.size) : ''));
  card.appendChild(icon);
  card.appendChild(name);
  card.appendChild(size);
  card.appendChild(rm);
  return card;
}

function removeAttachment(view, id) {
  const list = attachmentsOf(view);
  const i = list.findIndex((a) => a.id === id);
  if (i < 0) return;
  const a = list[i];
  if (a.previewUrl) URL.revokeObjectURL(a.previewUrl);
  list.splice(i, 1);
  const box = viewEl(view, 'attachments');
  if (box) box._sig = null; // 强制重画（删到空时立刻回到零占位）
  renderAttachments(view);
}

// 附件区渲染：0 个附件 → 带 hidden（零占位、不参与布局）；有附件才上屏。
// 内容签名没变就不重建 DOM（syncComposer 会被频繁调用）。
function renderAttachments(view) {
  view = view || mainView;
  const box = viewEl(view, 'attachments');
  if (!box) return;
  const list = attachmentsOf(view);
  // 换了会话：这一轮的附件不跟过去（首页还没会话时 attachSid 为空，建会话不算换）
  const sid = view.state.cur ? view.state.cur.id : null;
  if (view.state.attachSid && view.state.attachSid !== sid) clearAttachments(view);
  const sig = list.map((a) => [a.id, a.name, a.size, a.status].join(':')).join('|');
  if (box._sig === sig) return;
  box._sig = sig;
  box.innerHTML = '';
  box.classList.toggle('hidden', !list.length);
  for (const a of list) box.appendChild(attachCard(view, a));
  syncComposerPad(view); // 出现/消失即时重算悬浮垫高（ResizeObserver 之外再兜一次，防首帧抖动）
}

/* 粘贴与拖拽投放：都只入附件区，不碰工作区。
   粘贴只吃剪贴板里的文件对象（截图、网页复制的图片），纯文本粘贴原样放行；
   从资源管理器复制文件再粘贴时 Chromium 只给路径文本、拿不到文件对象，本轮不做。 */
function setupAttachDrop(view) {
  // 投放目标是输入卡本身，不是 composer-wrap：首页时输入卡被搬进页面中央的问候区
  // （wrap 里根本没有它），绑在 wrap 上会漏掉首页那次投放
  const card = viewEl(view, 'composer');
  const input = view.input;
  if (card) {
    let depth = 0; // dragenter/leave 会在子元素上反复触发，用深度计数防闪
    const hasFiles = (e) => !!e.dataTransfer && [...(e.dataTransfer.types || [])].includes('Files');
    card.addEventListener('dragenter', (e) => {
      if (!hasFiles(e)) return;
      e.preventDefault();
      depth++;
      card.classList.add('drop-hot');
    });
    card.addEventListener('dragover', (e) => {
      if (!hasFiles(e)) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = 'copy';
    });
    card.addEventListener('dragleave', () => {
      depth = Math.max(0, depth - 1);
      if (!depth) card.classList.remove('drop-hot');
    });
    card.addEventListener('drop', (e) => {
      depth = 0;
      card.classList.remove('drop-hot');
      const files = e.dataTransfer && e.dataTransfer.files ? [...e.dataTransfer.files] : [];
      if (!files.length) return;
      e.preventDefault();
      stageFiles(files, view);
    });
  }
  const rail = viewEl(view, 'attachments');
  if (rail) {
    // 附件区横向排卡：竖向滚轮平移成横向（否则会去滚后面的对话区）
    rail.addEventListener('wheel', (e) => {
      if (e.deltaY === 0) return;
      e.preventDefault();
      rail.scrollLeft += (e.deltaY > 0 ? 1 : -1) * Math.min(Math.abs(e.deltaY), 60);
    }, { passive: false });
  }
  if (input) {
    input.addEventListener('paste', (e) => {
      const cd = e.clipboardData;
      if (!cd) return;
      const files = [];
      for (const it of (cd.items || [])) {
        if (it.kind !== 'file') continue;
        const f = it.getAsFile();
        if (f) files.push(f);
      }
      if (!files.length && cd.files) files.push(...cd.files);
      if (!files.length) return; // 纯文本：交给默认粘贴
      e.preventDefault();
      stageFiles(files, view);
    });
  }
}

// 上传失败时取后端那句可读的 error（writeErr 的响应体是 {"error":"…"}），
// 别把 JSON 原样甩到提示条上
async function uploadErrText(r) {
  let t = '';
  try { t = await r.text(); } catch (e) { /* 读不到就用状态码 */ }
  try {
    const d = JSON.parse(t);
    if (d && d.error) return String(d.error);
  } catch (e) { /* 不是 JSON：原样用 */ }
  return t.trim() || ('HTTP ' + r.status);
}

/* 发送时落盘：把暂存的附件逐个写进工作区（后端照旧注入【文件上传】通知，顺序在
   用户消息之前）。有失败就中止本轮发送——不留"文件没带进去、消息却发出去了"的
   糊涂账：成功的那几个已经在工作区里（后端已发通知），失败的留在附件区可重试或移除。 */
async function flushAttachments(view) {
  const list = attachmentsOf(view);
  const sid = view.state.cur && view.state.cur.id;
  if (!list.length || !sid) return true;
  view._attachFlush = true; // 上传期间忽略重复点发送（见 send 的入口守卫）
  const box = viewEl(view, 'attachments');
  if (box) box.classList.add('busy');
  let failed = 0, firstErr = '';
  try {
    for (const a of list) {
      a.status = 'uploading';
      renderAttachments(view);
      try {
        const url = '/api/sessions/' + sid + '/upload';
        let r;
        if (a.path) {
          r = await fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ path: a.path }) });
        } else {
          const fd = new FormData();
          fd.append('file', a.file, a.name);
          r = await fetch(url, { method: 'POST', body: fd });
        }
        if (!r.ok) throw new Error(await uploadErrText(r));
        a.status = 'staged';
      } catch (e) {
        a.status = 'error';
        a.error = String((e && e.message) || e);
        if (!firstErr) firstErr = a.error;
        failed++;
      }
    }
  } finally {
    view._attachFlush = false;
    if (box) box.classList.remove('busy');
  }
  if (failed) {
    const keep = list.filter((a) => a.status === 'error');
    for (const a of list) if (a.status !== 'error' && a.previewUrl) URL.revokeObjectURL(a.previewUrl);
    list.length = 0;
    for (const a of keep) list.push(a);
    const bx = viewEl(view, 'attachments');
    if (bx) bx._sig = null;
    renderAttachments(view);
    attachHint(view, failed + ' 个附件上传失败，本轮没有发送（点 ✕ 移除后可重发）：' + firstErr.slice(0, 80), 8000);
    return false;
  }
  clearAttachments(view);
  return true;
}

// 加号菜单「上传文件」：只把文件加进附件区（不再立刻写工作区），落盘交给发送那一步
async function doUpload(view) {
  view = view || popView || mainView;
  closePopovers();
  if (view.state.streaming) {
    attachHint(view, '本轮执行中：加进来的文件会随下一条消息一起上传');
    return;
  }
  // 桌面壳（shell=desktop）的本地会话：走系统原生文件选择框采集本机真实路径
  // （它同时是 deliver_file 的交付目标来源）；用户取消则静默结束，接口不可用
  // 回退浏览器文件选择。手机/远程（JX_REMOTE）与局域网浏览器（非桌面壳，
  // 弹了也是弹在宿主机上、路径对用户那台机器无意义）走 multipart 原路径。
  if (document.documentElement.classList.contains('desktop-shell') &&
      !(view.state.cur && view.state.cur.remote)) {
    try {
      const r = await fetch('/api/pick-file?filter=all&multi=1&title=' + encodeURIComponent('选择要加入的文件'));
      if (r.ok) {
        const d = await r.json();
        const paths = d.paths || (d.path ? [d.path] : []);
        if (paths.length) stagePaths(paths, view);
        return; // 选到了就入列收工，取消就什么都不做
      }
    } catch (e) { /* 原生选择框不可用：走下面的浏览器回退 */ }
  }
  uploadTarget = view;
  $('file-input').value = '';
  $('file-input').click();
}

function doExport(format, view) {
  view = view || popView || mainView;
  closePopovers();
  const cur = view.state.cur;
  if (!cur) { alert('还没有可导出的对话'); return; }
  const q = format === 'json' ? '?format=json' : '';
  const url = '/api/sessions/' + cur.id + '/export' + q;
  if (window.JX_REMOTE) { JX_REMOTE.openURL(url); return; } // 远程模式：隧道取 blob 再开
  window.open(url, '_blank');
}

async function toggleModeFlag(flag, view) {
  view = view || popView || mainView;
  const s = sessionOrDraftFor(view);
  const turningOn = !s[flag];
  const fields = {};
  fields[flag] = turningOn;
  if (turningOn && flag === 'planMode') fields.goalMode = false; // 计划/目标二选一
  if (turningOn && flag === 'goalMode') fields.planMode = false;
  await patchDraftOrSession(fields, view);
  closePopovers();
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

/* ---------- 回到底部按钮 + 手机端输入区滚动收起（主/子对话按 view 装配） ---------- */

// 回到底部按钮：滚离底部一定距离浮现于输入区上方正中（手机/桌面一致）。
// 主对话挂 #msg-zone（上面 setupWidthHandles 建的定位层），子对话现场把
// #sub-messages 包进同款 .sub-msg-zone；按钮不随内容滚动，始终贴消息区底部。
const JUMP_SHOW = 320, JUMP_HIDE = 160; // 滞回阈值：出现/收起取不同距离，防临界抖动

function makeJumpLatest(view) {
  const b = document.createElement('button');
  b.className = 'jump-latest';
  b.title = '回到底部';
  b.innerHTML = chev('p-chev');
  b.onclick = () => {
    view._pinned = false; // 用户明确要回底部：恢复跟随
    view._progUntil = Date.now() + 800; // 平滑下降途中的中间位置不当作上翻
    view.scroll.scrollTo({ top: view.scroll.scrollHeight, behavior: 'smooth' });
    tuckComposer(view, false);
  };
  return b;
}

function setupJumpLatest() {
  for (const view of [mainView, subView]) {
    let zone = null;
    if (view.key === 'main') {
      zone = document.getElementById('msg-zone'); // boot 里 setupWidthHandles 先建好
    } else {
      const inner = view.scroll; // #sub-messages：包一层定位容器（幂等）
      if (inner.parentNode && !inner.parentNode.classList.contains('sub-msg-zone')) {
        zone = document.createElement('div');
        zone.className = 'sub-msg-zone';
        inner.parentNode.insertBefore(zone, inner);
        zone.appendChild(inner);
      } else if (inner.parentNode) {
        zone = inner.parentNode;
      }
    }
    if (!zone) continue;
    view._jumpBtn = makeJumpLatest(view);
    zone.appendChild(view._jumpBtn);
  }
}

// 手机端输入区收起：往回翻历史时整体滑出屏幕底让位；向底部翻/聚焦输入框/点回
// 到底部时滑回。桌面（≥500px）不参与，断点跨越时由 app-core 的复位逻辑清理。
function composerWrapOf(view) {
  if (view.key === 'main') return $('composer-wrap');
  return document.querySelector('.sub-chat .composer-wrap');
}

// 悬浮输入区的配套：输入卡铺在消息区上方（见 style.css 输入区节），内容列要垫
// 等高内边距，最后一条消息才停在卡上方而不是被盖住；回到底部按钮的锚点高度
// --cw-h 也在此同步（收起时归零，按钮落到屏底）。输入区高度随输入/胶囊/提示条
// 变化，由 ResizeObserver 跟随，切换上下文时 placeComposer / openSubSession 补调。
function syncComposerPad(view) {
  const wrap = composerWrapOf(view);
  const zone = view.scroll && view.scroll.parentNode;
  if (!wrap || !zone || !view.col) return;
  const h = wrap.classList.contains('tucked') ? 0 : wrap.offsetHeight;
  view.col.style.paddingBottom = (h + 14) + 'px';
  zone.style.setProperty('--cw-h', h + 'px');
  view._cwH = h; // 未收起时的垫高：收起判定用它做距离阈值（见 setupScrollTuck）
}

function tuckComposer(view, on) {
  const wrap = composerWrapOf(view);
  if (!wrap) return;
  if (on) {
    if (!isNarrow() || wrap.classList.contains('tucked')) return;
    // 有待决定的浮窗（审批/提问/计划审批）时不收起：否则卡片跟着输入区一起滑出屏幕
    if (askPending(view)) return;
    wrap.style.transform = 'translateY(100%)';
    wrap.classList.add('tucked');
    syncComposerPad(view); // 收起即刻收走垫高：内容马上用上让出的高度
  } else {
    if (!wrap.classList.contains('tucked')) return;
    wrap.classList.remove('tucked');
    wrap.style.transform = '';
    // 展开相反：垫高等滑入动画结束（.25s）再恢复——瞬间撑回 189px 的话，最后
    // 一条消息与屏幕底之间会闪出一大块空背景（浅色主题下就是一条白边），卡片
    // 要再过 250ms 才盖上来。动画期间卡从下方滑入、内容暂滚到它后面（悬浮卡
    // 本来就允许）。syncComposerPad 读实时状态，期间若又收起会先按收起收口，
    // 这个迟到的一次同样幂等
    setTimeout(() => syncComposerPad(view), 270);
  }
}

function setupScrollTuck() {
  for (const view of [mainView, subView]) {
    setupAttachDrop(view); // 附件区：粘贴图片与拖拽投放（两个视图各挂一套）
    let lastY = view.scroll.scrollTop;
    // 跟随钉住的手势入口：滚轮上翻/触摸上滑立即钉住（无论离底多近——否则
    // 一步滚轮增量会被下一次跟随吃掉）；拖滚动条/键盘翻页由下方 scroll 兜底
    view.scroll.addEventListener('wheel', (e) => {
      if (e.deltaY < 0) { view._pinned = true; view._pinTs = Date.now(); }
    }, { passive: true });
    let touchY = null;
    view.scroll.addEventListener('touchstart', (e) => {
      touchY = e.touches[0] ? e.touches[0].clientY : null;
    }, { passive: true });
    view.scroll.addEventListener('touchmove', (e) => {
      if (touchY != null && e.touches[0] && e.touches[0].clientY - touchY > 4) {
        view._pinned = true; view._pinTs = Date.now(); // 手指下移 = 内容上翻
      }
    }, { passive: true });
    view.scroll.addEventListener('scroll', () => {
      const el = view.scroll;
      const y = el.scrollTop;
      const dy = y - lastY;
      lastY = y;
      // 跟随钉住/恢复：内容增长不触发 scroll（scrollTop 不变），离底变远只可能
      // 是用户在滚 → 钉住；滚回贴近底部 → 恢复跟随（刚钉住的短窗口内不判恢复，
      // 小距上翻的余量不能立刻抵消钉住）。程序性贴底跟随时 dist≈0，天然维持跟随态。
      // 两类合成事件不算用户意图：渲染钳位（_renderTs 后 120ms 内，尾部重建把
      // scrollTop 夹到临时底部）与回到底部按钮的平滑下降（_progUntil 前的中间位置）
      const dist = el.scrollHeight - el.scrollTop - el.clientHeight;
      const ts = Date.now();
      const synthetic = ts - (view._renderTs || 0) < 120 || ts < (view._progUntil || 0);
      if (!synthetic && dist > FOLLOW_PIN_DIST) { view._pinned = true; view._pinTs = ts; }
      else if (!synthetic && dist < FOLLOW_RESUME && ts - (view._pinTs || 0) > PIN_GUARD_MS) view._pinned = false;
      // 回到底部按钮：滚离底部出现，靠近底部消失（滞回）
      const was = !!view._jumpShow;
      const now = was ? dist > JUMP_HIDE : dist > JUMP_SHOW;
      if (now !== was && view._jumpBtn) {
        view._jumpShow = now;
        view._jumpBtn.classList.toggle('show', now);
      }
      // 输入区收起只作用于窄屏。收起会同步收走内容列底部垫高（syncComposerPad），
      // scrollHeight 随之变矮 pad——若收时离底不足「垫高+余量」，浏览器会把
      // scrollTop 钳到新底部并触发 scroll：近底判定翻转→立即展开→垫高长回→
      // 惯性滚动再收……一来一回就是「文字图像不断抽搐」。收起因此要求离底
      // 超过 pad+FOLLOW_RESUME：收完 dist 仍 >FOLLOW_RESUME，钳位不可能发生。
      if (!isNarrow()) return;
      const nearBottom = y + el.clientHeight >= el.scrollHeight - FOLLOW_RESUME;
      if (nearBottom) tuckComposer(view, false);
      else if (dy < -8 && y + el.clientHeight < el.scrollHeight - (view._cwH || 0) - FOLLOW_RESUME) tuckComposer(view, true); // 往回翻历史：收起
      else if (dy > 12) tuckComposer(view, false); // 向底部翻：展开
    }, { passive: true });
    view.input.addEventListener('focus', () => tuckComposer(view, false));
    // 输入区高度变化（autogrow/胶囊/提示条/排队坞）跟随同步垫高与按钮锚点
    const wrap = composerWrapOf(view);
    if (wrap && window.ResizeObserver) new ResizeObserver(() => syncComposerPad(view)).observe(wrap);
    syncComposerPad(view);
  }
}

$('btn-send').onclick = send;
$('btn-stop').onclick = () => stopRun(mainView);
$('btn-plus').onclick = () => openPlusMenu(mainView);
$('sub-btn-plus').onclick = () => openPlusMenu(subView);
$('perm-chip').onclick = () => { popView = mainView; renderPermPop(popView); openPopover('perm-pop', $('perm-chip')); };
$('sub-perm-chip').onclick = () => { popView = subView; renderPermPop(popView); openPopover('perm-pop', $('sub-perm-chip')); };
$('model-chip').onclick = () => { popView = mainView; renderModelPop(popView); openPopover('model-pop', $('model-chip')); };
$('sub-model-chip').onclick = () => { popView = subView; renderModelPop(popView); openPopover('model-pop', $('sub-model-chip')); };
// 工作目录面板：每次打开重置搜索；搜索实时过滤目录列表
$('ws-row').onclick = () => {
  $('ws-search').value = '';
  $('ws-search-clear').classList.add('hidden');
  renderWsPop();
  $('ws-pop-notice').classList.add('hidden'); // 告知行跟随面板开合复位
  openPopover('ws-pop', $('ws-row'));
};

// 模式选择：与工作目录胶囊同款交互（未来 设计人员/编程人员 等模式照标准办公加选项）
$('mode-row').onclick = () => {
  renderModePop();
  openPopover('mode-pop', $('mode-row'));
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
$('pm-upload').onclick = () => doUpload();
$('pm-plan').onclick = () => toggleModeFlag('planMode');
// 目标模式开启时要求填写目标文本；关闭时清状态（goalText 保留备查）
async function toggleGoalFlag() {
  const s = sessionOrDraftFor(popView);
  if (s.goalMode) {
    await patchDraftOrSession({ goalMode: false }, popView);
  } else {
    const text = prompt('目标模式：要达成的目标是什么？\n（模型将自主持续推进，直到达成或确认受阻）', s.goalText || '');
    if (text === null || !text.trim()) return;
    await patchDraftOrSession({ goalMode: true, planMode: false, goalText: text.trim() }, popView);
  }
  closePopovers();
}
$('pm-goal').onclick = toggleGoalFlag;
// 黄色模式药丸：点 ✕（或药丸本身）关闭该模式（两个输入区视图各一套）
for (const v of [mainView, subView]) {
  viewEl(v, 'mode-flags').addEventListener('click', (e) => {
    const pill = e.target.closest('.mflag');
    if (!pill || !pill.classList.contains('on')) return;
    // 药丸可能是主对话区那份（id=flag-plan）或右栏那份（id=sub-flag-plan）
    toggleModeFlag(pill.id.includes('goal') ? 'goalMode' : 'planMode', v);
  });
}
$('pm-export').onclick = () => doExport('md');
$('pm-export-json').onclick = () => doExport('json');
$('pm-remote').onclick = () => { remoteFormPrefill(); openPopover('remote-pop', viewEl(popView, 'btn-plus')); };
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
$('file-input').addEventListener('change', (e) => { if (e.target.files.length) stageFiles([...e.target.files], uploadTarget); });
// 窄屏（手机远程访问）不支持新增工作目录：原生选择框弹在宿主机屏幕上，手机端
// 永远等不到——点了就地展开一行告知（文案与侧栏那颗按钮的提示卡同源，见
// app-core.js 的 NARROW_WS_ADD_NOTICE），不把请求发出去白等。
function toggleWsPopAddNotice() {
  const el = $('ws-pop-notice');
  if (el.classList.contains('hidden')) el.textContent = NARROW_WS_ADD_NOTICE;
  el.classList.toggle('hidden'); // 再点一次 = 收起，与侧栏告知卡同一套切换语义
}

// 「添加工作目录」：弹系统选择框，选中即设为新对话的工作目录并登记
$('ws-pop-add').onclick = async () => {
  if (isNarrow()) { toggleWsPopAddNotice(); return; } // 手机端：只给提示，不调后端
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
// 权限面板：单选立即生效，多选即时收集；完全访问 ⇄ 三项捆绑能力联动
// （切入强制勾上并置灰，切回「工作区内读写」/「只读」自动取消勾选）
$('perm-pop').addEventListener('change', (e) => {
  const v = popView; // 面板是单例：改动作用在打开它的那个视图的会话上
  if (e.target.name === 'perm') {
    if (e.target.value === 'full') {
      const set = new Set(sessionOrDraftFor(v).tools || []);
      FULL_BUNDLED_TOOLS.forEach((t) => set.add(t));
      patchDraftOrSession({ permission: 'full', tools: [...set] }, v).then(() => renderPermPop(v));
    } else {
      const fields = { permission: e.target.value };
      if (sessionOrDraftFor(v).permission === 'full') {
        // 从完全访问切回：捆绑点亮的三项能力随之自动取消，但默认能力要补回来（工作区内读写默认开启「操作浏览器」——切回时同时打开它）
        const _cur = sessionOrDraftFor(v);
        fields.tools = applyCapDefaults((_cur.tools || []).filter((t) => !FULL_BUNDLED_TOOLS.includes(t)), _cur.mode, e.target.value);
      }
      patchDraftOrSession(fields, v).then(() => renderPermPop(v));
    }
  } else if (e.target.dataset.tool) {
    const set = new Set(sessionOrDraftFor(v).tools || []);
    if (e.target.checked) set.add(e.target.dataset.tool); else set.delete(e.target.dataset.tool);
    patchDraftOrSession({ tools: [...set] }, v);
  }
});
// 点面板外收起所有弹出层。注意排除会打开弹出层的锚点按钮本身：
// 它们的 onclick 先开菜单，事件再冒泡到这里——不排除的话菜单会开完即关。
document.addEventListener('click', (e) => {
  if (!e.target.closest('.popover') && !e.target.closest('#btn-plus') &&
      !e.target.closest('#perm-chip') && !e.target.closest('#model-chip') && !e.target.closest('#ws-row') &&
      !e.target.closest('#mode-row') &&
      !e.target.closest('#sub-dd') &&
      !e.target.closest('#skill-add-btn') && // 设置页「＋ 添加技能」弹层锚点
      !e.target.closest('.ex-model-chip') && // 设置页「子对话专家」的模型选择芯片锚点
      // 右栏子对话输入区那套同款控件也是弹层锚点
      !e.target.closest('#sub-btn-plus') && !e.target.closest('#sub-perm-chip') &&
      !e.target.closest('#sub-model-chip') &&
      !e.target.closest('.ws-tools') && !e.target.closest('.ws-acts') && !e.target.closest('.row-acts')) {
    closePopovers();
  }
});
document.addEventListener('keydown', (e) => {
  if (e.key !== 'Escape') return;
  hideSelPop();
  if (sideSearchOpen()) { closeSideSearch(); return; } // 搜索开着：优先关搜索
  closePopovers();
});
/* ---------- 语音输入：实现已迁入组件 组件/voice/ ------------------------------------------
   原先在这里的语音输入实现（麦克风三态：空闲/连接中转圈/录音中脉冲、灰色暂定层、
   断线退避重连、时长倒计时徽标、一次性轻提示）已整体搬进「语音（朗读与听写）」
   组件：组件/voice/client.js。两个语音输入按钮的 DOM 仍在 index.html（位置、样式、
   图标全不变），但默认带 .hidden——按钮属于组件，组件加载成功才揭开并接管行为；
   组件关掉（设置 → 内置组件）或没加载到页面 = 按钮整组不出现。 */
// 上下文圆环：纯展示，数据在打开会话/发送完成时刷新（refreshCtxChip）；
// 悬停明细由 CSS :hover 驱动，无点击行为
// 对话区选中文字浮窗：松开鼠标出、选区消失/滚动/点外部收起；点浮窗不冲掉选区
$('sel-pop').addEventListener('mousedown', (e) => e.preventDefault());
$('sel-pop').addEventListener('click', (e) => {
  const b = e.target.closest('button');
  if (!b) return;
  const text = selKeepText;
  hideSelPop();
  if (b.dataset.act === 'copy') {
    navigator.clipboard.writeText(text).catch(() => {});
  } else if (b.dataset.act === 'compose') {
    // 插入到主对话输入框光标处
    const inp = $('input');
    const st = inp.selectionStart ?? inp.value.length;
    const en = inp.selectionEnd ?? st;
    inp.value = inp.value.slice(0, st) + text + inp.value.slice(en);
    inp.selectionStart = inp.selectionEnd = st + text.length;
    autoGrow(inp);
    persistMainDraftLS(); // 程序化插入也要落盘，切走/重启不丢
    inp.focus();
  } else if (b.dataset.act === 'aux') {
    askInAux(text);
  }
});
document.addEventListener('mouseup', (e) => {
  if (e.target.closest && e.target.closest('#sel-pop')) return;
  setTimeout(showSelPop, 10); // 等选区状态稳定
});
document.addEventListener('selectionchange', () => {
  const pop = $('sel-pop');
  if (pop && !pop.classList.contains('hidden') && !chatSelection()) hideSelPop();
});
['#messages', '#sub-messages'].forEach((id) => {
  const el = $(id);
  if (el) el.addEventListener('scroll', hideSelPop, { passive: true });
});
$('input').addEventListener('keydown', (e) => composerEnterKeydown(e, () => send()));
$('input').addEventListener('input', () => {
  autoGrow();
  persistMainDraftLS(); // 首页/会话两份草稿按当前上下文即时落本地
});
