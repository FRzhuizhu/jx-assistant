/* app-core.js —— 共享地基：全局状态、视图抽象（mainView/subView）、通用工具、
   弹层开合、跨区共享字典与对话区宽度读写。六个脚本中第一个加载。
   文件拆分约定见《README.md》开发规矩一节：原生 JS、无构建链，跨文件仅靠全局名引用。 */
/* 坚祥数字助理（软著版）前端逻辑：原生 JS，无框架、无构建步骤。风格对齐 DSH。 */

const $ = (id) => document.getElementById(id);

/* 桌面形态标记：桌面壳（runDesktop）导航的 URL 带 shell=desktop——右栏浏览器
 * 视图启用原生 WebView2 面板（矩形上报 + 工具栏），链接在右栏打开；网页模式
 * 一切照旧，链接新标签页打开。 */
if (new URLSearchParams(location.search).get('shell') === 'desktop') {
  document.documentElement.classList.add('desktop-shell');
}

/* ---------- JX：插件接口（插件兼容层注入的浏览器半边经此接入原生界面） ---------- */
const JX = {
  _bus: {},
  _msgActions: [],       // fn(actsRow, msg)：向消息悬停操作行追加按钮
  _settingsSections: [], // {title, mount(el)}：设置弹窗分区
  on(ev, fn) { (this._bus[ev] = this._bus[ev] || []).push(fn); },
  emit(ev, payload) { (this._bus[ev] || []).forEach((f) => { try { f(payload); } catch (e) { console.warn('[JX]', ev, e); } }); },
  registerMessageAction(fn) { this._msgActions.push(fn); },
  registerSettingsSection(sec) { this._settingsSections.push(sec); },
  // 插件向输入区左侧挂控件（按钮等），注册即生效；右栏子对话输入区镜像一份
  // （点击转发给原件执行，原件的 class 变化同步过来，两边外观/行为一致）
  addComposerControl(el) {
    const c = document.getElementById('plugin-composer-controls');
    if (c) c.appendChild(el);
    const sub = document.getElementById('sub-plugin-composer-controls');
    if (sub && el && el.nodeType === 1) {
      const copy = el.cloneNode(true);
      copy.addEventListener('click', (e) => { e.preventDefault(); e.stopPropagation(); el.click(); });
      new MutationObserver(() => { copy.className = el.className; copy.title = el.title; })
        .observe(el, { attributes: true, attributeFilter: ['class', 'title'] });
      sub.appendChild(copy);
    }
  },
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
  pendingSteer: [], // 执行中已插话、等下一次调用模型生效的消息（服务端为准）
  pendingQueue: [], // 执行中排队的消息：本轮结束后自动作为独立一轮发送
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
  if (!res.ok) {
    // 错误体里的结构字段挂到 Error 上：文件回退的 409 带 blocked/unsafe
    // （哪些文件被外部改过、哪些可退），前端要据此给出可操作的提示，
    // 只保留 message 会把这些信息丢掉
    const err = new Error(data.error || res.statusText);
    err.status = res.status;
    err.blocked = data.blocked;
    err.unsafe = data.unsafe;
    err.safe = data.safe;
    throw err;
  }
  return data;
}

function esc(s) {
  return String(s).replace(/[&<>"']/g, (c) => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
  ));
}

/* ---------- 窄屏断点（CSS 宽 <500px）：浮层布局的总开关 ----------
   窄屏下左/右栏是整屏浮层（样式见 style.css 末节），各宽度引擎冻结：
   不推导 --rp-w、不写 --content-w，避免手机宽度污染桌面记忆
   （jx.sb / jx.contentWidth）。断点跨越回宽屏时恢复桌面推导。 */
const narrowMq = window.matchMedia ? window.matchMedia('(max-width: 499.98px)') : null;
function isNarrow() { return !!(narrowMq && narrowMq.matches); }

// 窄屏下收起左栏浮层：选中/新建会话等入口调用（右栏浮层保持用户手开的状态）
function narrowClosePanels() {
  if (isNarrow()) document.body.classList.remove('sb-show');
}

// 断点跨越（拖拽窗口跨 500px）：窄→宽清浮层类并恢复桌面记忆与右栏推导；
// 宽→窄不用处理——CSS 默认即收起左栏，右栏若开着自动变为浮层。
// mq 的 change 事件在部分嵌入浏览器里不随视口变化派发，resize 兜底同一清理
// （幂等：宽屏下重复执行只是重放 jx.sb 记忆与右栏推导）
function leaveNarrowIfNeeded() {
  if (isNarrow()) return;
  document.body.classList.remove('sb-show');
  // 手机端滚动收起的输入区复位（展开逻辑在 app-chat.js 的 tuckComposer）
  document.querySelectorAll('.composer-wrap.tucked').forEach((w) => {
    w.classList.remove('tucked');
    w.style.transform = '';
    w.style.marginBottom = '';
  });
  if (typeof syncComposerPad === 'function') { // 悬浮输入区：复位后重算垫高与按钮锚点
    syncComposerPad(mainView);
    syncComposerPad(subView);
  }
  applySidebarState(); // 恢复 jx.sb 记忆的左栏状态（内部会重算 rpApply）
  rpApply();
}
if (narrowMq) {
  const onNarrowMq = () => { if (!isNarrow()) leaveNarrowIfNeeded(); };
  if (narrowMq.addEventListener) narrowMq.addEventListener('change', onNarrowMq);
  else if (narrowMq.addListener) narrowMq.addListener(onNarrowMq); // 旧 Safari
}

/* ---------- 侧栏滑动手势（方向×状态感知，触发即等效点击开合按钮） ----------
   定义（关键：栏开没开，手势含义不同）：
   * 栏收着：从屏幕缘热区向中间滑 → 拉出该侧栏（窄屏下两栏都收着才拉得开，
     手势绝不把两栏同时打开；桌面两栏本可并存，不设此限）。
   * 栏开着：在栏身上向屏幕外方向滑 → 收起该栏；向中间滑不动作。
   触发仍是单次位移 ≥44px 且横向主导，直接调用与按钮相同的 toggleSidebar /
   toggleRpanel——状态机、记忆、文案全走按钮那一条路。
   触摸走非被动 Touch 事件（横向确立即 preventDefault，阻断浏览器收编成滚动
   ——真机拉不出来的根因）；鼠标/笔走 Pointer 事件。轻微上下漂移容忍。
   收起手势的起点豁免：拖宽把手（#rp-resize 等）、输入框——它们有自己的交互。 */
(function () {
  const EDGE = 24;    // 屏幕左右缘热区（px），拉出手势的起点
  const TRIGGER = 44; // 单向滑过此距离即触发一次
  const CLAIM = 10;   // 横向位移超过此值且未明显纵向 → 接管手势
  let g = null;

  const modalOpen = () => [...document.querySelectorAll('.modal')].some((m) => !m.classList.contains('hidden'));
  const leftOpen = () => isNarrow() ? document.body.classList.contains('sb-show') : !document.body.classList.contains('sb-collapsed');
  const rightOpen = () => !document.getElementById('rpanel').classList.contains('hidden');
  const verticalIntent = (dx, dy) => Math.abs(dy) > Math.abs(dx) * 1.5 + 12;
  const fire = (side) => { if (side === 'left') toggleSidebar(); else toggleRpanel(); };

  // 按起点与两侧栏状态定手势：先看是不是点在开着的栏身上（收起手势），
  // 再看是不是屏幕缘热区（拉出手势，窄屏要求两栏都收着）
  function armAt(target, x) {
    if (target && target.closest) {
      if (target.closest('#rp-resize, [class*="handle"], [class*="resize"], [class*="drag"]')) return null;
      if (target.closest('input, textarea, select, [contenteditable]')) return null;
      if (target.closest('#sidebar') && leftOpen()) return { side: 'left', close: true };
      if (target.closest('#rpanel') && rightOpen()) return { side: 'right', close: true };
    }
    const vw = window.innerWidth;
    if (x <= EDGE && !leftOpen() && (!isNarrow() || !rightOpen())) return { side: 'left', close: false };
    if (x >= vw - EDGE && !rightOpen() && (!isNarrow() || !leftOpen())) return { side: 'right', close: false };
    return null;
  }

  // toward：开向位移（左栏向右为正 / 右栏向左为正）。拉出在 ≥TRIGGER 触发；
  // 收起在 ≤-TRIGGER（向屏幕外）触发，反向滑回中间则放弃
  const towardOf = (g2, x) => (x - g2.x0) * (g2.side === 'left' ? 1 : -1);
  const hit = (g2, toward) => g2.close ? toward <= -TRIGGER : toward >= TRIGGER;
  const quit = (g2, toward) => g2.close ? toward >= TRIGGER : toward <= -TRIGGER;

  document.addEventListener('touchstart', (e) => {
    if (g || e.touches.length !== 1 || modalOpen()) return;
    const t = e.touches[0];
    const arm = armAt(e.target, t.clientX);
    if (!arm) return;
    g = { ...arm, x0: t.clientX, y0: t.clientY, claimed: false, done: false, touch: true };
  }, { passive: true });
  document.addEventListener('touchmove', (e) => {
    if (!g || !g.touch || e.touches.length !== 1) return;
    const t = e.touches[0];
    const dx = t.clientX - g.x0, dy = t.clientY - g.y0;
    if (!g.claimed) {
      if (verticalIntent(dx, dy)) { g = null; return; } // 纵向：让位给滚动
      if (Math.abs(dx) < CLAIM) return;
      g.claimed = true;
      e.preventDefault(); // 锁定：浏览器不再把本次触摸收编为滚动
    } else {
      e.preventDefault();
    }
    const toward = towardOf(g, t.clientX);
    if (!g.done && hit(g, toward)) { g.done = true; fire(g.side); } // 一次手势只触发一次
    else if (quit(g, toward)) g = null;
  }, { passive: false });
  const touchEnd = () => { if (g && g.touch) g = null; };
  document.addEventListener('touchend', touchEnd);
  document.addEventListener('touchcancel', touchEnd);

  document.addEventListener('pointerdown', (e) => {
    if (g || e.pointerType === 'touch' || e.button !== 0 || modalOpen()) return;
    const arm = armAt(e.target, e.clientX);
    if (!arm) return;
    g = { ...arm, x0: e.clientX, y0: e.clientY, claimed: false, done: false, touch: false };
  });
  document.addEventListener('pointermove', (e) => {
    if (!g || g.touch) return;
    const dx = e.clientX - g.x0, dy = e.clientY - g.y0;
    if (!g.claimed) {
      if (verticalIntent(dx, dy)) { g = null; return; }
      if (Math.abs(dx) < CLAIM) return;
      g.claimed = true;
      e.preventDefault(); // 鼠标：横向确立后抑制拖动选中
    }
    const toward = towardOf(g, e.clientX);
    if (!g.done && hit(g, toward)) { g.done = true; fire(g.side); }
    else if (quit(g, toward)) g = null;
  });
  const pointerEnd = () => { if (g && !g.touch) g = null; };
  document.addEventListener('pointerup', pointerEnd);
  document.addEventListener('pointercancel', pointerEnd);
})();


window.addEventListener('resize', leaveNarrowIfNeeded);

/* ---------- Markdown 渲染（围栏代码块、标题、列表、表格、行内样式、链接） ---------- */

// 行内格式：代码 span → 粗体 → 链接（[文字](url) 与裸 http(s) URL）→ 文件路径。
// 链接带 ext-link 类，点击由 app-chat.js 的委托处理器接管——在内嵌浏览器
// （右栏面板）打开而非整页跳转；Ctrl/中键放行浏览器默认行为。
// 转义次序：代码 span 先摘出占位（其中 URL 不做链接化），其余文本逐段 esc
// 后拼链接，保证不破坏现有 XSS 防线。

// 文件路径识别（对话中途输出的文件地址 → file-link，点击右栏预览）：
// 盘符绝对路径或带分隔符的相对路径，且以字母开头的扩展名结尾（分隔符是
// 硬条件——不然 "Node.js" 这类词全会被误判）；空白与中西文标点断开。
// 含空格的路径在正文里无法安全判界（会吞掉后面的文字），只在反引号里认。
const FILE_PATH_RE = /(?:[A-Za-z]:)?(?:[^\s`'"<>|:,，。；（）【】《》「」]*[\\/])+[^\s`'"<>|:,，。；（）【】《》「」]*[A-Za-z0-9\u4e00-\u9fff_]\.[A-Za-z][A-Za-z0-9]{0,7}(?![\w.])/g;

// 反引号内容整体是否一个文件路径（模型习惯把产物路径用反引号包起来）：
// 有分隔符 → 允许空格（反引号就是边界），只要以扩展名结尾且无引号/竖线；
// 无分隔符 → 单文件名（config.json / My File.txt），最多一个空格。
function codeSpanIsPath(c) {
  if (/[<>|"']/.test(c)) return false;
  if (c.includes('\\') || c.includes('/')) {
    const body = c.replace(/^[A-Za-z]:/, '');
    if (body.includes(':')) return false;
    return /\.[A-Za-z][A-Za-z0-9]{0,7}$/.test(c);
  }
  if ((c.match(/ /g) || []).length > 1) return false;
  return /^[\w\u4e00-\u9fff][\w\u4e00-\u9fff .+-]*\.[A-Za-z][A-Za-z0-9]{0,7}$/.test(c);
}

// 纯文本段里的路径 → file-link（前后片段照常转义）
function pathLinks(seg) {
  if (!seg) return '';
  let out = '', last = 0, m;
  FILE_PATH_RE.lastIndex = 0;
  while ((m = FILE_PATH_RE.exec(seg))) {
    out += esc(seg.slice(last, m.index));
    out += '<a class="file-link" data-path="' + esc(m[0]) + '">' + esc(m[0]) + '</a>';
    last = m.index + m[0].length;
  }
  return out + esc(seg.slice(last));
}

function inlineFmt(s) {
  const codes = [];
  let t = s.replace(/`([^`\n]+)`/g, (_, c) => {
    codes.push(c);
    return '\x00' + (codes.length - 1) + '\x00';
  });
  // 裸 URL 只认 ASCII URL 字符：中文与全角标点（如句号逗号）处截断，避免把后面的正文吞进链接
  const linkRe = /\[([^\]\n]+)\]\((https?:\/\/[^\s)]+)\)|(https?:\/\/[A-Za-z0-9\-._~:\/?#\[\]@!$&'*+,;=%]+)/g;
  let out = '', last = 0, m;
  while ((m = linkRe.exec(t))) {
    out += pathLinks(t.slice(last, m.index));
    const href = m[1] !== undefined ? m[2] : m[0];
    const text = m[1] !== undefined ? m[1] : m[0];
    out += '<a class="ext-link" href="' + esc(href) + '" rel="noopener">' + esc(text) + '</a>';
    last = m.index + m[0].length;
  }
  out += pathLinks(t.slice(last));
  out = out
    .replace(/\x00(\d+)\x00/g, (_, i) => {
      const c = codes[+i];
      return codeSpanIsPath(c)
        ? '<code><a class="file-link" data-path="' + esc(c) + '">' + esc(c) + '</a></code>'
        : '<code>' + esc(c) + '</code>';
    })
    .replace(/\*\*([^*\n]+)\*\*/g, '<b>$1</b>');
  return out;
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
// 已知工作区 = 手动添加 ∪ 会话用过 ∪ 默认，再剔除被「彻底删除/清空」拉黑的目录。
// 默认工作区路径来自配置、没法注销，只能靠这份黑名单才能从列表里消失；
// 名下一旦又有会话，loadSessions 会按会话重新建组（不查黑名单），不会藏掉对话。
function knownWorkspaces() {
  const set = new Set(lsGet('jx.wslist', []));
  if (state.defaultWs) set.add(state.defaultWs);
  (state.sessions || []).forEach((x) => { if (x.workspace) set.add(x.workspace); });
  const hidden = lsGet('jx.wshidden', []);
  return [...set].filter((ws) => !hidden.includes(ws));
}

function wsOf(s) {
  return s.workspace || state.defaultWs || '';
}

/* ---------- 居中确认弹窗：破坏性操作（清空回收站/彻底删除）统一用它 ----------
   Promise 化的 confirm，卡片出现在屏幕正中；Esc/点遮罩=取消，Enter=确定。
   okOnly=true 时只留一个「确定」按钮，当提示框用。 */
let confirmResolve = null;
let confirmOkOnly = false;
function confirmCenter(msg, okOnly, up) {
  return new Promise((resolve) => {
    confirmResolve = resolve;
    confirmOkOnly = !!okOnly;
    $('confirm-msg').textContent = msg;
    $('confirm-cancel').style.display = confirmOkOnly ? 'none' : '';
    document.querySelector('#confirm-modal .confirm-card').classList.toggle('confirm-up', !!up);
    $('confirm-modal').classList.remove('hidden');
    $('confirm-ok').focus();
  });
}
function closeConfirm(v) {
  if ($('confirm-modal').classList.contains('hidden')) return;
  $('confirm-modal').classList.add('hidden');
  const r = confirmResolve; confirmResolve = null;
  if (r) r(v);
}
$('confirm-cancel').onclick = () => closeConfirm(false);
$('confirm-ok').onclick = () => closeConfirm(true);
$('confirm-modal').onclick = (e) => { if (e.target === $('confirm-modal')) closeConfirm(false); };
document.addEventListener('keydown', (e) => {
  if ($('confirm-modal').classList.contains('hidden')) return;
  if (e.key === 'Escape') { e.stopPropagation(); closeConfirm(false); }
  else if (e.key === 'Enter') { e.stopPropagation(); closeConfirm(true); }
});
/* 侧栏开合动画（左右侧栏共用同一个函数）：宽度从 from 过渡到 to，
   结束回调里做状态定版（如隐藏元素）。拖拽调宽时不受影响（不带 .anim 即时生效）。 */
let slideSeq = 0;
function slidePane(el, from, to, onEnd) {
  const tok = ++slideSeq;
  el._slideTok = tok; // 新的开合会顶掉旧动画的定版回调
  // 起始宽必须先于 .anim 提交：否则过渡起点是"开/合之前的旧宽度"（会先闪一下再滑）
  el.classList.remove('anim');
  el.style.width = el.style.minWidth = from + 'px';
  void el.offsetWidth; // 提交起始宽（此刻无过渡，直接落位）
  el.classList.add('anim');
  el.style.width = el.style.minWidth = to + 'px'; // 目标宽 → 触发 from→to 过渡
  let done = false;
  const h = (e) => {
    if (e.target !== el || e.propertyName !== 'width') return;
    fin();
  };
  const fin = () => {
    if (done || el._slideTok !== tok) return; // 已被更新的开合/拖拽取代
    done = true;
    el.removeEventListener('transitionend', h);
    el.classList.remove('anim');
    el.style.width = el.style.minWidth = '';
    if (onEnd) onEnd();
  };
  el.addEventListener('transitionend', h);
  setTimeout(fin, 460); // 兜底：transitionend 不来（嵌入浏览器节流）也定版
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

/* ---------- 子对话视图（右侧边栏首个视图：顶上下拉选/新建，下方内嵌完整对话） ---------- */

/* 对话视图描述：主对话与右栏子对话共用同一套渲染/发送函数（renderMessages /
   liveRender / handleEvent / send 全部按 view 参数化），只差 DOM 挂点与状态。 */
const mainView = {  key: 'main',
  get col() { return $('msg-col'); },
  get scroll() { return $('messages'); },
  get input() { return $('input'); },
  get hint() { return $('hint'); },
  get stopBtn() { return $('btn-stop'); },
  liveId: 'live-bubble',
  emptyHTML:
    '<div class="empty"><h2>开始新的对话</h2><p>在下方输入框发起任务；数字助理会自动派发子任务执行。<br>' +
    'API 地址填 <code>mock</code> 可离线体验完整链路。</p></div>',
  state, // 主对话的全局 state
};
const subState = {
  cur: null, msgs: [], streaming: false, abort: null,
  streamText: '', streamThinking: '', pendingSteer: [], pendingQueue: [],
};
const subView = {
  key: 'sub',
  get col() { return $('sub-msg-col'); },
  get scroll() { return $('sub-messages'); },
  get input() { return $('sub-input'); },
  get hint() { return $('sub-hint'); },
  get stopBtn() { return $('sub-stop'); },
  liveId: 'sub-live-bubble',
  emptyHTML: '<div class="empty"><h2>子对话</h2><p>在下方输入框向这个子对话发消息；它的工作目录与权限跟随主对话。</p></div>',
  state: subState,
};

function drawerFamilyId() {
  if (!state.cur) return null;
  return state.cur.parentId || state.cur.id; // 锚定在主对话（父）上
}

// 会话 id 反查它正展示在哪个视图（主列 or 右栏子对话）；都不在看则返回 null，
// 消息操作条据此决定编辑/分支作用于哪个会话、成功后切到哪个视图
function viewOf(sid) {
  if (state.cur && state.cur.id === sid) return mainView;
  if (subState.cur && subState.cur.id === sid) return subView;
  return null;
}

/* 输入区控件的视图参数化：两个视图各有一套底部控件（id 在右栏加 sub- 前缀），
   样式与行为完全共用；单例弹层（权限/模型/加号菜单）记住是谁打开的（popView），
   处理时就作用于那个视图的会话。 */
let popView = mainView; // 弹层归属视图（打开弹层时设置）
function viewEl(view, id) { return $((view || mainView).key === 'main' ? id : 'sub-' + id); }

// loadSessions 合并短时间内的多次触发：执行中会有成串的会话更新事件，
// 不合并就会打出一串同样的列表请求（也拖慢页面）
let loadSessionsTimer = null, loadSessionsPending = false;
function loadSessions() {
  if (loadSessionsPending) return Promise.resolve();
  loadSessionsPending = true;
  return new Promise((resolve) => {
    if (loadSessionsTimer) clearTimeout(loadSessionsTimer);
    loadSessionsTimer = setTimeout(() => {
      loadSessionsPending = false;
      loadSessionsTimer = null;
      loadSessionsNow().then(resolve, resolve);
    }, 120);
  });
}

/* ---------- 本地草稿持久化：刷新不丢输入与选择 ----------
   缓存模型：输入框内容永远只属于「当前上下文」——
   首页（新对话）一份 jx.homeDraft（含选择项 + text，跨重启保留），
   每个主对话一份 jx.st.<id>，每个子对话一份 jx.subst.<id>。
   三条铁律：
   1) 输入即时落盘（input 事件与程序化写入都走 persist*）；
   2) 切换上下文时输入框整体替换为目标上下文的草稿（没有就是空，绝不残留上一个的字）；
   3) 发送成功即清对应上下文的草稿（首页发送清 homeDraft.text，会话/子对话清各自 key）。 */

function sessionDraftKey(id) { return 'jx.st.' + id; }
function subDraftKey(id) { return 'jx.subst.' + id; }

function persistHomeDraftLS() {
  if (state.cur) return; // 只在首页上下文保存
  try {
    homeDraft.text = $('input').value;
    localStorage.setItem('jx.homeDraft', JSON.stringify({ ...homeDraft, text: homeDraft.text }));
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

function persistSubDraftLS() {
  if (!subState.cur) return;
  try {
    const v = $('sub-input').value;
    if (v) localStorage.setItem(subDraftKey(subState.cur.id), v);
    else localStorage.removeItem(subDraftKey(subState.cur.id));
  } catch (e) {}
}

// 主输入框的统一落盘入口：首页与会话草稿按当前上下文各写各的
function persistMainDraftLS() {
  persistHomeDraftLS();
  persistSessionDraftLS();
}

function clearSessionDraftLS(id) {
  try { localStorage.removeItem(sessionDraftKey(id)); } catch (e) {}
}

// 首页草稿只清 text，保留工作目录/模式等选择项
function clearHomeDraftLS() {
  homeDraft.text = '';
  try { localStorage.setItem('jx.homeDraft', JSON.stringify({ ...homeDraft, text: '' })); } catch (e) {}
}

function clearSubDraftLS(id) {
  try { localStorage.removeItem(subDraftKey(id)); } catch (e) {}
}

// 会话彻底删除时连带清掉它的本地痕迹（主草稿/子草稿与内存里的过程缓存）
function removeSessionLocalData(id) {
  try {
    localStorage.removeItem(sessionDraftKey(id));
    localStorage.removeItem(subDraftKey(id));
  } catch (e) {}
  delete subActs[id];
  delete subDone[id];
  delete dotApproval[id];
  delete dotDone[id];
  Object.keys(turnOpen).forEach((k) => { if (k.indexOf(id + ':') === 0) delete turnOpen[k]; });
}

function restoreHomeDraftLS() {
  try {
    const d = JSON.parse(localStorage.getItem('jx.homeDraft') || 'null');
    if (!d) return;
    homeDraft.workspace = d.workspace || '';
    homeDraft.mode = CHAT_MODES.some((m) => m.v === d.mode) ? d.mode : 'office';
    homeDraft.permission = d.permission || 'workspace-write';
    homeDraft.provider = d.provider || '';
    homeDraft.model = d.model || '';
    homeDraft.thinking = d.thinking || '';
    homeDraft.tools = Array.isArray(d.tools) ? d.tools : [];
    homeDraft.planMode = !!d.planMode;
    homeDraft.goalMode = !!d.goalMode;
    homeDraft.goalText = d.goalText || '';
    // 计划/目标二选一：旧数据可能两个都存着，收敛为只留计划
    if (homeDraft.planMode && homeDraft.goalMode) homeDraft.goalMode = false;
    homeDraft.remote = (d.remote && d.remote.host) ? d.remote : null;
    homeDraft.text = typeof d.text === 'string' ? d.text : '';
  } catch (e) {}
}

/* ---------- 跨区共享字典：左栏会话徽点/搜索定位、对话渲染、全局事件流共用 ---------- */
// 回合展开记忆：key = 会话id:回合首条seq。手动展开过的回合不再被自动折回——
// 流式期间新来的思考/工具行、回合收尾重渲都保持展开（数字助理2 修过的同类 bug）
const turnOpen = {};
/* 数字助理派发/转达的子任务卡片：后台执行期间显示实时工具活动，完成后标已完成 */
const subActs = {}; // 子会话id -> 最近几条工具活动
const subDone = {}; // 子会话id -> 是否已完成（内存态；跨刷新由回报消息补判，见 reportedSubs）
const dotApproval = {}; // 会话id -> 待审批（橙点）
const dotDone = {};     // 会话id -> 完成未查看（绿点）

/* ---------- 首页草稿与弹层开合（弹出面板系统的共享半边） ---------- */
const homeDraft = { mode: 'office', permission: 'workspace-write', provider: '', model: '', thinking: '', tools: [], planMode: false, goalMode: false, goalText: '', remote: null };

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

/* ---------- 对话区宽度拖拽：左右把手对称调宽，偏好记入本机 ---------- */

function contentWidth() {
  return parseFloat(getComputedStyle(document.documentElement).getPropertyValue('--content-w')) || 860;
}

function setContentWidth(px, save) {
  if (isNarrow()) return; // 窄屏浮层下对话区恒为全宽，不写宽度也不落盘（防污染桌面记忆）
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

/* ---------- 输入框自动增高（主/子输入区与语音共用） ---------- */
function autoGrow(t) {
  t = t || $('input');
  t.style.height = 'auto';
  t.style.height = Math.min(t.scrollHeight, 200) + 'px';
}

/* ---------- 输入框占位文案跟随发送快捷键设置（jx.sendkey） ---------- */
// sendKeyHint：快捷键提示短语（渲染路径每次刷新占位符时统一取这里，别写死）
function sendKeyHint() {
  return lsGet('jx.sendkey', 'enter') === 'ctrl'
    ? 'Ctrl + 回车键发送，回车键换行，可到设置里更改快捷键'
    : '回车键发送，Shift + 回车键换行，可到设置里更改快捷键';
}
function updateSendKeyHint() {
  const el = $('input');
  if (!el) return;
  el.placeholder = sendKeyHint();
}
