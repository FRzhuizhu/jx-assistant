/* 上下文面板顶部「压缩」按钮的桩测（纯 Node，无浏览器）：
   1) 从 app-chat.js 原样抽出纯函数 compactResultMsg + fmtTokens 跑用例；
   2) 对 web/app-chat.js、web/style.css、web/index.html 做结构断言——按钮位置
      （卡片最上面那一行）、调用的接口、确认框、执行中禁用三处判定、窄屏收边，
      任何一处被后来的改动抹掉就红。
   运行：仓库根目录 node gui-test/ctx-compact-test.js */
const { readFileSync } = require('fs');
const pathMod = require('path');

const web = (f) => readFileSync(pathMod.join(__dirname, '..', 'web', f), 'utf8');
const chat = web('app-chat.js');
const css = web('style.css');
const html = web('index.html');

let pass = 0, fail = 0;
const ok = (cond, name, extra) => {
  if (cond) { pass++; return; }
  fail++;
  console.log('  ✗ ' + name + (extra !== undefined ? ' => ' + JSON.stringify(extra) : ''));
};
const has = (src, needle, name) => ok(src.includes(needle), name || ('缺少：' + needle));
const order = (src, a, b, name) => {
  const ia = src.indexOf(a), ib = src.indexOf(b);
  ok(ia >= 0 && ib >= 0 && ia < ib, name || ('顺序不对：' + a + ' 应早于 ' + b), { ia, ib });
};

/* ---------- 1. 纯函数：压缩完成的提示文案 ---------- */
const grab = (src, re) => {
  const m = src.match(re);
  if (!m) throw new Error('抽不到：' + re);
  return m[0];
};
const fnSrc = grab(chat, /function fmtTokens\(n\) \{[\s\S]*?\n\}/) + '\n' +
  grab(chat, /function compactResultMsg\(r, before, after\) \{[\s\S]*?\n\}/);
const compactResultMsg = new Function(fnSrc + '\nreturn compactResultMsg;')();

const cases = [
  // 规则模式（folded=N）：压了什么 + 上下文 A → B + 省了多少
  [{ folded: 4 }, 31292, 27500, '已折叠 4 个回合：上下文 3.1万 → 2.8万，省了 3.8k'],
  // 两个数折算成「万」后一样（省下的不到一千）：不写「2.8万 → 2.8万」
  [{ folded: 2 }, 28267, 28000, '已折叠 2 个回合：上下文 2.8万，省了 267'],
  // 模型模式（只有 summary，没有 folded）
  [{ summary: '…' }, 50000, 12000, '已把整个对话压缩成摘要：上下文 5.0万 → 1.2万，省了 3.8万'],
  // 数字拿不到（context 重拉失败）：给「—」，不崩
  [{ folded: 1 }, 0, 0, '已折叠 1 个回合：上下文 —'],
];
for (const [r, before, after, want] of cases) {
  const got = compactResultMsg(r, before, after);
  ok(got === want, '文案：' + JSON.stringify(r), { got, want });
}

/* ---------- 2. app-chat.js：按钮位置、接口、确认、执行中禁用、刷新 ---------- */
const tipFn = grab(chat, /function renderCtxTip\(view\) \{[\s\S]*?\n\}/);
has(tipFn, '<div class="ctx-ops">', '卡片顶部有 .ctx-ops 行（放压缩按钮）');
order(tipFn, 'class="ctx-head"', 'class="ctx-ops"', '压缩按钮在标题行之后（卡片最上面第二行）');
order(tipFn, 'class="ctx-ops"', 'class="ctx-bar"', '压缩按钮在占用条/分类清单之前（顶部）');
has(tipFn, 'data-ctx-compact="1"', '按钮带 data-ctx-compact 标记');
has(tipFn, 'ctxRunning(view)', '渲染时读执行态');
has(tipFn, '执行中，等这一轮结束再压缩', '执行中给出置灰原因');
has(tipFn, 'placeCtxTip(view)', '渲染后把卡片收进视口（窄屏）');

const compactFn = grab(chat, /async function compactFromCtxPanel\(view\) \{[\s\S]*?\n\}/);
has(compactFn, "api('/api/sessions/' + cur.id + '/compact', {", '调现有接口 POST /api/sessions/{id}/compact');
has(compactFn, 'body: JSON.stringify({ upTo })', '请求体带 upTo（与「最后一轮下方的压缩按钮」同一口径）');
has(compactFn, 'const upTo = lastRoundSeq(view);', 'upTo＝最后一轮：从最后一轮往上全压（含它在内）');
ok(!compactFn.includes('JSON.stringify({})'), '不再用空请求体（空体＝自动压缩的保留窗口口径，语义不同）');
has(chat, 'function lastRoundSeq(view) {', '最后一轮 seq 的取值函数在');
has(chat, "const CTX_ARCHIVE_KINDS = new Set(['summary', 'prompt', 'request', 'audit', 'inherit'])", '归档行不当作一轮的终点');
has(compactFn, 'confirmCenter(\'压缩当前对话的上下文？', '点一次先弹居中确认');
has(compactFn, '从最后一轮往上，整个对话都会被压缩（最后一轮本身也在内）', '确认框写清「含最后一轮」');
order(compactFn, 'if (!ok) return;', 'ctxCompactBusy[view.key] = true;', '取消就不发请求');
has(compactFn, 'await refreshCtxChip(view);', '完成后重拉 /api/sessions/{id}/context');
has(compactFn, "await confirmCenter('压缩失败：' + e.message, true)", '失败时如实显示后端原话');
has(compactFn, 'if (ctxRunning(view)) {', '执行中前端先拦');
has(compactFn, '这个对话正在执行，等这一轮跑完再压缩', '执行中的拦截提示措辞');

const runningFn = grab(chat, /function ctxRunning\(view\) \{[\s\S]*?\n\}/);
has(runningFn, 'view.state.streaming', '执行中判定：本地流');
has(runningFn, "cur.status === 'running'", '执行中判定：会话 meta');
has(runningFn, 'state.sessions', '执行中判定：列表项 status');
has(chat, 'if (view.state.cur && lastCtx[view.key]) renderCtxTip(view);', '流开始/结束时按钮跟着置灰/恢复');
// 交互只有悬停一套：不点击、不钉住（用户 2026-10-25 明确要求，别再加回来）
ok(!chat.includes('wireCtxRing'), '没有点击/钉住的接线函数', 'wireCtxRing 应已删除');
ok(!/classList\.(add|toggle|contains)\('open'\)/.test(chat.slice(chat.indexOf('function placeCtxTip'), chat.indexOf('function compactResultMsg'))), '压缩流程里不留 .open 钉住');
has(chat, "ring.addEventListener('mouseenter', () => placeCtxTip(v));", '悬停进圆环时把卡片收回视口（窄屏）');

/* ---------- 3. style.css：卡片可交互、按钮样式、窄屏 ---------- */
has(css, '.ctx-ring:hover .ctx-tip { opacity: 1; transform: translateY(0); pointer-events: auto; }', '悬停时卡片接鼠标事件（卡里的按钮可点）');
ok(!css.includes('.ctx-ring.open'), 'CSS 里不再有 .open 钉住态', '应已删除');
has(css, '.ctx-tip::before { content: \'\';', '圆环与卡片之间的空隙补了透明桥（鼠标挪进卡片不消失）');
has(css, '.ctx-compact {', '压缩按钮有独立样式');
has(css, '.ctx-compact:disabled', '禁用态样式');
has(css, '.ctx-ops-hint', '执行中的说明文字样式');
const narrow = css.slice(css.indexOf('@media (max-width: 499.98px)'), css.indexOf('@media (max-width: 499.98px)') + 800);
has(narrow, '.ctx-tip { width: min(250px, calc(100vw - 20px))', '窄屏：卡片按视口收宽');

/* ---------- 4. index.html：两枚圆环都是可点入口 ---------- */
const ringCount = (html.match(/class="ctx-ring"/g) || []).length;
ok(ringCount === 2, '主对话与子对话两枚圆环容器都在', ringCount);
ok(!html.includes('class="ctx-ring" role="button"'), '圆环不再伪装成按钮（交互只有悬停）');
has(html, 'id="ctx-tip"', '主对话明细卡容器在');
has(html, 'id="sub-ctx-tip"', '子对话明细卡容器在');

console.log(`上下文面板「压缩」桩测：${pass} 通过，${fail} 失败`);
if (fail) process.exit(1);
