// 进度面板折叠回归：窄屏（手机）默认折叠成右上角胶囊、点开是浮动卡；宽屏有富余
// 宽度时自动展开。两步都用真实点击（Input.*）走，最后各存一张图。
// 说明：测试实例里没有真实会话，进度项与对话内容是现场注入的布局夹具（面板渲染
// 路径本身走的就是 app-chat.js 的 drawProgress，与线上同源）。
import { newTab, read, click, shot, center, sleep } from './cdp.mjs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const BASE = 'http://127.0.0.1:18799'; // 已在跑的开发实例（-webdir web，前端热加载）
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
    for (let i = 1; i <= 14; i++) m += '<div class="msg assistant"><div class="bubble">第 ' + i + ' 条示例消息：这里是对话区的内容，用来对照进度面板占了多大地盘。</div></div>';
    col.innerHTML = m;
  }
  return true;
})()`;

const geom = `(() => {
  const p = document.getElementById('progress-panel');
  const b = p.getBoundingClientRect();
  const ul = p.querySelector('ul');
  return {
    cls: p.className, text: p.textContent.trim().slice(0, 8),
    pos: getComputedStyle(p).position, z: getComputedStyle(p).zIndex,
    rect: [Math.round(b.left), Math.round(b.top), Math.round(b.width), Math.round(b.height)],
    ulMax: ul ? getComputedStyle(ul).maxHeight : null,
    msgsH: Math.round(document.getElementById('messages').getBoundingClientRect().height),
    vw: innerWidth, vh: innerHeight,
  };
})()`;

const client = await newTab(BASE + '/', 393, 852);
await sleep(600);
await read(client, FIXTURE);
await sleep(200);

// ---- 1) 窄屏（手机）：默认就该是折叠的胶囊 ----
let s = await read(client, geom);
console.log('[pg] 窄屏默认 =', JSON.stringify(s));
if (!s.cls.includes('pg-folded')) throw new Error('窄屏应当默认折叠');
if (s.text !== '进度') throw new Error('胶囊文案应为「进度」，实际 ' + s.text);
if (s.rect[0] + s.rect[2] > s.vw - 8 || s.rect[1] < 60) throw new Error('胶囊没有贴右上角');
await shot(client, resolve(DIR, 'pg_1_phone_collapsed.png'));

// ---- 2) 点胶囊 → 展开成浮动卡（点开才占屏，且限高可滚） ----
let c = await center(client, '#progress-panel .pg-pill');
await click(client, c.x, c.y);
await sleep(300);
s = await read(client, geom);
console.log('[pg] 窄屏点开后 =', JSON.stringify(s));
if (s.cls.includes('pg-folded')) throw new Error('点胶囊后应当展开');
if (s.pos !== 'absolute') throw new Error('窄屏展开态应当是浮层');
if (!s.ulMax || s.ulMax === 'none') throw new Error('展开态清单应当限高（卡内滚动）');
await shot(client, resolve(DIR, 'pg_2_phone_expanded.png'));

// ---- 3) 点头部 → 收回胶囊 ----
c = await center(client, '#progress-panel .progress-head');
await click(client, c.x, c.y);
await sleep(300);
s = await read(client, geom);
console.log('[pg] 窄屏再收起 =', JSON.stringify(s));
if (!s.cls.includes('pg-folded')) throw new Error('点头部后应当收起');
if (s.text !== '进度') throw new Error('收起后应只剩胶囊');

// ---- 4) 宽屏：有富余宽度，自动展开（回到流内，不遮挡对话） ----
await client.send('Emulation.setDeviceMetricsOverride', { width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
await sleep(400);
await read(client, 'pgFold = null; drawProgress(); true'); // 用户选择复位 = 自动
s = await read(client, geom);
console.log('[pg] 宽屏自动 =', JSON.stringify(s));
if (s.cls.includes('pg-folded')) throw new Error('宽屏（有富余宽度）应当自动展开');
if (s.pos !== 'static') throw new Error('宽屏展开态应当在流内（不遮挡对话）');
await shot(client, resolve(DIR, 'pg_3_wide_auto.png'));

// ---- 5) 宽屏手动收起 → 贴右的小胶囊（仍未遮挡对话内容） ----
c = await center(client, '#progress-panel .progress-head');
await click(client, c.x, c.y);
await sleep(250);
s = await read(client, geom);
console.log('[pg] 宽屏收起 =', JSON.stringify(s));
if (!s.cls.includes('pg-folded')) throw new Error('宽屏手动收起失败');
await shot(client, resolve(DIR, 'pg_4_wide_collapsed.png'));

console.log('[pg] 全部断言通过');
client.close();
