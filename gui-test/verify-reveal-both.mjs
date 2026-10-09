// 双入口 GUI 实测「在文件夹中查看」修复有效性（真实鼠标事件）：
//   入口 A：聊天页交付成果卡片 →「在文件夹中查看」→ POST /delivery/reveal
//   入口 B：交付文件「打开」→ 右栏「文件」查看页（iframe /api/fileview）
//           → 页内「在文件夹中定位」→ POST /reveal-path
// 判定 = 请求 200 + 点击后新增可见的 explorer 窗口（Win32 枚举，见
// enum-explorer.ps1）。
import { newTab, read, click, key, typeText, shot, center, sleep } from './cdp.mjs';
import { execSync } from 'node:child_process';
import { resolve } from 'node:path';

const BASE = 'http://127.0.0.1:9721';
const DIR = resolve(process.cwd(), '..', 'gui-test-screenshots');
const PS = (s) => JSON.stringify(s);

function explorerWins() {
  try {
    const out = execSync(
      `powershell -NoProfile -ExecutionPolicy Bypass -File "${resolve(process.cwd(), 'enum-explorer.ps1')}"`,
      { encoding: 'utf8', timeout: 15000 },
    );
    return out.trim().split(/\r?\n/).filter(Boolean).map((l) => JSON.parse(l));
  } catch { return []; }
}
const newVisible = (before, after) => {
  const old = new Set(before.map((w) => w.hwnd));
  return after.filter((w) => !old.has(w.hwnd) && w.visible);
};

const client = await newTab(BASE + '/', 1280, 860);
await sleep(1200);

// CDP 层抓 api 流量（主文档 + iframe 的 fetch 都在同一个 target 里）
await client.send('Network.enable', {});
const netLog = [];
client.ws.addEventListener('message', (ev) => {
  const m = JSON.parse(ev.data);
  if (m.method === 'Network.requestWillBeSent' && /\/api\/.*(reveal|open)/.test(m.params.request.url)) {
    netLog.push({ phase: 'req', url: m.params.request.url });
  }
  if (m.method === 'Network.responseReceived' && /\/api\/.*(reveal|open)/.test(m.params.response.url)) {
    netLog.push({ phase: 'resp', url: m.params.response.url, status: m.params.response.status });
  }
});

// 选 mock 模型
let c = await center(client, '#model-chip');
await click(client, c.x, c.y);
await sleep(400);
const opt = await read(client, `(() => {
  const pop = document.getElementById('model-pop');
  const el = pop && [...pop.querySelectorAll('button, [role=button], div')].find(e => e.textContent.trim().startsWith('mock-1') && e.children.length <= 2);
  if (!el) return null;
  const b = el.getBoundingClientRect();
  return { x: b.left + b.width / 2, y: b.top + b.height / 2 };
})()`);
if (!opt) throw new Error('mock-1 选项未找到');
await click(client, opt.x, opt.y);
await sleep(300);

// 发消息跑一轮 mock（write_file → deliver_result → 总结）
c = await center(client, '#input');
await click(client, c.x, c.y);
await typeText(client, '双入口在文件夹中查看验证');
await sleep(120);
await key(client, 'Enter');
for (let i = 0; i < 40; i++) {
  const running = await read(client, `!document.getElementById('btn-stop').classList.contains('hidden') || state.streaming`);
  if (!running) break;
  await sleep(400);
}
await sleep(1500);

// ---- 入口 A：交付卡片「在文件夹中查看」 ----
const btnA = await read(client, `(() => {
  const card = document.querySelector('#msg-col .delivery-card');
  if (!card) return null;
  const rv = [...card.querySelectorAll('button')].find(b => b.textContent.trim() === '在文件夹中查看');
  if (!rv) return null;
  const b = rv.getBoundingClientRect();
  return { x: b.left + b.width / 2, y: b.top + b.height / 2 };
})()`);
if (!btnA) throw new Error('交付卡片或「在文件夹中查看」按钮未找到');
const beforeA = explorerWins();
const netBeforeA = netLog.length;
await click(client, btnA.x, btnA.y); // 真实鼠标事件
await sleep(3000);
const gainedA = newVisible(beforeA, explorerWins());
const netA = netLog.slice(netBeforeA);
console.log('[A 交付卡片] api =', JSON.stringify(netA));
console.log('[A 交付卡片] 新增可见 explorer 窗口 =', JSON.stringify(gainedA));
const passA = netA.some((n) => n.phase === 'resp' && n.url.includes('/delivery/reveal') && n.status === 200)
  && gainedA.some((w) => w.title.includes('work'));
await shot(client, resolve(DIR, 'verify_A_delivery_card.png'));

// ---- 入口 B：右栏「文件」查看页「在文件夹中定位」 ----
// 先点交付文件的「打开」（txt 走 file 路由 → 右栏文件页 iframe）
const btnOpen = await read(client, `(() => {
  const card = document.querySelector('#msg-col .delivery-card');
  const ov = card && [...card.querySelectorAll('button')].find(b => b.textContent.trim() === '打开');
  if (!ov) return null;
  const b = ov.getBoundingClientRect();
  return { x: b.left + b.width / 2, y: b.top + b.height / 2 };
})()`);
if (!btnOpen) throw new Error('「打开」按钮未找到');
await click(client, btnOpen.x, btnOpen.y);
// 等 iframe 加载出页内按钮（同源：主文档直接量 contentDocument）
let btnB = null;
for (let i = 0; i < 20 && !btnB; i++) {
  await sleep(400);
  btnB = await read(client, `(() => {
    const f = document.querySelector('#rp-view-file iframe');
    const doc = f && f.contentDocument;
    if (!doc || !doc.body) return null;
    const btn = [...doc.querySelectorAll('header button')].find(b => b.textContent.trim() === '在文件夹中定位');
    if (!btn) return null;
    const fb = f.getBoundingClientRect();
    const b = btn.getBoundingClientRect();
    return { x: fb.left + b.left + b.width / 2, y: fb.top + b.top + b.height / 2, fname: (doc.querySelector('.fname')||{}).textContent };
  })()`);
}
if (!btnB) throw new Error('右栏文件页或「在文件夹中定位」按钮未找到（iframe 未加载？）');
console.log('[B 文件页] 查看页文件 =', btnB.fname);
const beforeB = explorerWins();
const netBeforeB = netLog.length;
await click(client, btnB.x, btnB.y); // 真实鼠标事件（iframe 内按钮的全局坐标）
await sleep(3000);
const gainedB = newVisible(beforeB, explorerWins());
const netB = netLog.slice(netBeforeB);
console.log('[B 文件页] api =', JSON.stringify(netB));
console.log('[B 文件页] 新增可见 explorer 窗口 =', JSON.stringify(gainedB));
const passB = netB.some((n) => n.phase === 'resp' && n.url.includes('/reveal-path') && n.status === 200)
  && gainedB.some((w) => w.title.includes('work'));
await shot(client, resolve(DIR, 'verify_B_fileview.png'));

console.log('[RESULT] A 交付卡片「在文件夹中查看」:', passA ? 'PASS' : 'FAIL');
console.log('[RESULT] B 右栏文件页「在文件夹中定位」:', passB ? 'PASS' : 'FAIL');
client.close();
process.exit(passA && passB ? 0 : 2);
