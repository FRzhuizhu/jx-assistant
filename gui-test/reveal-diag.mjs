// 复现「在文件夹中查看」点击没反应：mock 模型跑一轮产生交付卡片 →
// 点击「在文件夹中查看」→ CDP 层记录 fetch 请求/响应、原生 alert 对话框。
import { newTab, read, click, key, typeText, shot, center, sleep } from './cdp.mjs';
import { resolve } from 'node:path';

const BASE = 'http://127.0.0.1:9721';
const DIR = resolve(process.cwd(), '..', 'gui-test-screenshots');

const client = await newTab(BASE + '/', 1280, 860);
await sleep(1200);

// CDP 层监听：api 网络流量 + 原生 dialog（alert/confirm）
await client.send('Network.enable', {});
await client.send('Page.enable', {});
const netLog = [];
const dialogs = [];
client.ws.addEventListener('message', (ev) => {
  const m = JSON.parse(ev.data);
  if (m.method === 'Network.requestWillBeSent' && m.params.request.url.includes('/api/')) {
    netLog.push({ phase: 'req', url: m.params.request.url, method: m.params.request.method, body: m.params.request.postData });
  }
  if (m.method === 'Network.responseReceived' && m.params.response.url.includes('/api/')) {
    netLog.push({ phase: 'resp', url: m.params.response.url, status: m.params.response.status });
  }
  if (m.method === 'Page.javascriptDialogOpening') {
    dialogs.push({ type: m.params.type, message: m.params.message });
    client.send('Page.handleJavaScriptDialog', { accept: false }).catch(() => {});
  }
  if (m.method === 'Runtime.exceptionThrown') {
    netLog.push({ phase: 'jserr', text: JSON.stringify(m.params.exceptionDetails).slice(0, 300) });
  }
});
await client.send('Runtime.enable', {});

// 选 mock 模型（模型芯片 → 选 mock-1）
let c = await center(client, '#model-chip');
await click(client, c.x, c.y);
await sleep(400);
const opt = await read(client, `(() => {
  const pop = document.getElementById('model-pop');
  if (!pop || pop.classList.contains('hidden')) return null;
  const el = [...pop.querySelectorAll('button, [role=button], div')].find(e => e.textContent.trim().startsWith('mock-1') && e.children.length <= 2);
  if (!el) return null;
  const b = el.getBoundingClientRect();
  return { x: b.left + b.width / 2, y: b.top + b.height / 2 };
})()`);
if (!opt) throw new Error('mock-1 选项未找到');
await click(client, opt.x, opt.y);
await sleep(300);

// 发消息（mock 一轮：write_file → deliver_result → 总结）
c = await center(client, '#input');
await click(client, c.x, c.y);
await typeText(client, '交付卡片在文件夹中查看复现');
await sleep(120);
await key(client, 'Enter');
console.log('[diag] sent, waiting for turn...');

// 等回合结束
for (let i = 0; i < 40; i++) {
  const running = await read(client, `!document.getElementById('btn-stop').classList.contains('hidden') || state.streaming`);
  if (!running) break;
  await sleep(400);
}
await sleep(1500);

// 找到「在文件夹中查看」按钮并点击
const btn = await read(client, `(() => {
  const col = document.getElementById('msg-col');
  const card = col.querySelector('.delivery-card');
  if (!card) return null;
  const rv = [...card.querySelectorAll('button')].find(b => b.textContent.trim() === '在文件夹中查看');
  if (!rv) return { err: 'card found but no reveal button' };
  const b = rv.getBoundingClientRect();
  const hit = document.elementFromPoint(b.left + b.width / 2, b.top + b.height / 2);
  return {
    x: b.left + b.width / 2, y: b.top + b.height / 2,
    hitTag: hit ? hit.tagName + '.' + (hit.className || '') : null,
    hitIsBtn: hit === rv || (hit && rv.contains(hit)),
  };
})()`);
console.log('[diag] reveal button state =', JSON.stringify(btn));
if (!btn || btn.err) { console.log('[diag] FAIL: no button'); client.close(); process.exit(2); }

const before = netLog.length;
await click(client, btn.x, btn.y);
await sleep(3000);

console.log('[diag] api traffic during click =', JSON.stringify(netLog.slice(before), null, 2));
console.log('[diag] native dialogs =', JSON.stringify(dialogs, null, 2));
await shot(client, resolve(DIR, 'reveal_diag_after_click.png'));
client.close();
