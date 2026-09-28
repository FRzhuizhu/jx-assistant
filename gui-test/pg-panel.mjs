// 进度面板定位回归：对话区右上角一枚浮窗，折叠＝胶囊、展开＝原地长出的卡片。
// 关键断言：展开前后右缘与顶边一模一样（原地展开不跳位）、窄屏展开不吃对话区高度、
// 宽屏有留白时展开不遮正文、窗口变窄时自动档翻回折叠。
// 说明：测试实例里没有真实会话，进度项与对话内容是现场注入的布局夹具（面板渲染走的
// 就是 app-chat.js 的 drawProgress，与线上同源）。
import { newTab, read, click, shot, center, sleep } from './cdp.mjs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const BASE = process.env.JX_BASE || 'http://127.0.0.1:18799'; // 开发实例（-webdir web，前端热加载）
const DIR = resolve(dirname(fileURLToPath(import.meta.url)), '..', 'gui-test-screenshots');

const ITEMS = [
  { status: 'completed', content: '定位两个问题的根因（桌面选择框 / 手机端新对话页添加工作目录）' },
  { status: 'in_progress', content: '备份待改文件到 .jxai/tmp' },
  { status: 'pending', content: '修 pickfolder_windows.go 的选择框焦点' },
  { status: 'pending', content: '手机端新对话页「添加工作目录」给告知卡' },
  { status: 'pending', content: '重建 exe 并提交' },
  { status: 'pending', content: '验证' },
  { status: 'pending', content: '写文档' },
];

const FIXTURE = `(() => {
  pgItems = ${JSON.stringify(ITEMS)};
  pgFold = null;
  drawProgress();
  const col = document.querySelector('#messages .msg-col');
  if (col) {
    let m = '';
    for (let i = 1; i <= 14; i++) m += '<div class="msg assistant"><div class="bubble">第 ' + i + ' 条示例消息：这里是对话区的内容，用来对照进度浮窗压住了哪里。</div></div>';
    col.innerHTML = m;
  }
  return true;
})()`;

// 一次读完面板、消息列、对话区的几何：面板是不是「原地展开」全靠这几个矩形说话
const geom = `(() => {
  const p = document.getElementById('progress-panel');
  const b = p.getBoundingClientRect();
  const main = document.getElementById('main').getBoundingClientRect();
  const col = document.querySelector('#messages .msg-col').getBoundingClientRect();
  const r = (x) => Math.round(x);
  return {
    folded: p.classList.contains('pg-folded'), text: p.textContent.trim().slice(0, 6),
    pos: getComputedStyle(p).position,
    right: r(main.right - b.right), top: r(b.top), w: r(b.width), h: r(b.height),
    left: r(b.left), colRight: r(col.right),
    msgsH: r(document.getElementById('messages').getBoundingClientRect().height),
    vw: innerWidth, vh: innerHeight,
  };
})()`;

const near = (a, b, tol = 1) => Math.abs(a - b) <= tol;

const client = await newTab(BASE + '/', 393, 852);
await sleep(600);
await read(client, FIXTURE);
await sleep(200);

// ---- 1) 手机窄屏：默认折叠成右上角胶囊 ----
let s = await read(client, geom);
console.log('[pg] 窄屏默认 =', JSON.stringify(s));
const pill = { right: s.right, top: s.top, msgsH: s.msgsH };
if (!s.folded || s.text !== '进度') throw new Error('窄屏默认应当是「进度」胶囊');
if (s.right !== 10) throw new Error('胶囊右缘应当贴对话区右缘 10px，实际 ' + s.right);
if (s.top !== 74) throw new Error('胶囊顶边应当在窄屏顶栏（66）下 8px＝74，实际 ' + s.top);
await shot(client, resolve(DIR, 'pg_1_phone_collapsed.png'));

// ---- 2) 点胶囊 → 原地展开（右缘、顶边都不动） ----
let c = await center(client, '#progress-panel .pg-pill');
await click(client, c.x, c.y);
await sleep(300);
s = await read(client, geom);
console.log('[pg] 窄屏展开 =', JSON.stringify(s));
if (s.folded) throw new Error('点胶囊后应当展开');
if (s.pos !== 'absolute') throw new Error('展开态应当是浮层');
if (!near(s.right, pill.right) || !near(s.top, pill.top)) throw new Error('展开跳位了：右缘/顶边应当与胶囊一致');
if (s.w !== 340) throw new Error('窄屏没有留白可借，卡片应当是舒适宽度 340，实际 ' + s.w);
if (s.msgsH !== pill.msgsH) throw new Error('展开不该改变对话区高度（浮层不占位）');
await shot(client, resolve(DIR, 'pg_2_phone_expanded.png'));

// ---- 3) 点头部 → 收回胶囊 ----
c = await center(client, '#progress-panel .progress-head');
await click(client, c.x, c.y);
await sleep(300);
s = await read(client, geom);
console.log('[pg] 窄屏再收起 =', JSON.stringify(s));
if (!s.folded || s.text !== '进度') throw new Error('点头部后应当收回胶囊');
if (!near(s.right, pill.right) || !near(s.top, pill.top)) throw new Error('收起同样不该跳位');

// ---- 4) 宽屏且右侧确实有留白 → 自动展开，且正好塞进留白不遮正文 ----
// （实例默认开着左侧栏 272px：1600 视口 → 对话区 1328、内容列 860，右侧留白 234，
//   留白+借用内容列内边距-边距 = 248 ≥ 220 → 自动展开，宽度就取 248）
// 先把用户选择清回「自动」（窄屏下先画成折叠），再由窗口变宽触发翻转——这就是线上
// 缩放窗口/转屏时走的同步路径。
await read(client, 'pgFold = null; drawProgress(); true');
await client.send('Emulation.setDeviceMetricsOverride', { width: 1600, height: 900, deviceScaleFactor: 1, mobile: false });
await sleep(500);
s = await read(client, geom);
console.log('[pg] 宽屏自动 =', JSON.stringify(s));
if (s.folded) throw new Error('宽屏右侧有留白（234+24-10=248 ≥ 220）应当自动展开');
if (s.pos !== 'absolute') throw new Error('宽屏同样应当是浮层（不再占流内一行）');
if (s.w !== 248) throw new Error('卡片应当正好塞进留白（248），实际 ' + s.w);
if (s.top !== 56) throw new Error('宽屏卡片顶边应当在顶栏（48）下 8px＝56，实际 ' + s.top);
// 不遮正文：卡片左缘不能越过「消息列正文右缘」（消息列右缘减去它自己的内边距）
if (s.left < s.colRight - 24 - 1) throw new Error('卡片遮到了正文：left=' + s.left + ' 正文右缘=' + (s.colRight - 24));
await shot(client, resolve(DIR, 'pg_3_wide_auto_in_gutter.png'));

// ---- 5) 窗口收窄到中等宽度：留白不够 → 自动档翻回折叠 ----
await client.send('Emulation.setDeviceMetricsOverride', { width: 1200, height: 900, deviceScaleFactor: 1, mobile: false });
await sleep(500);
s = await read(client, geom);
console.log('[pg] 中等宽度自动 =', JSON.stringify(s));
if (!s.folded) throw new Error('留白不够（34+24-10=48 < 220）应当自动折叠成胶囊');
if (s.right !== 10) throw new Error('折叠后仍应贴对话区右缘');
await shot(client, resolve(DIR, 'pg_4_mid_auto_collapsed.png'));

console.log('[pg] 全部断言通过');
client.close();
