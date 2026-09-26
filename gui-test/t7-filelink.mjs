// T7：对话中途的网址与文件路径可点击，在右栏打开
import { newTab, read, click, key, typeText, shot, center, sleep, closeTab } from './cdp.mjs';
import { resolve } from 'node:path';

const BASE = 'http://127.0.0.1:9726';
const DIR = resolve(process.cwd(), '..', 'gui-test-screenshots');

const client = await newTab(BASE + '/', 1280, 900);
await sleep(1200);

// 1) 选 mock 模型并发消息（首轮：write_file → run_command → deliver → 总结带链接）
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
if (!opt) throw new Error('mock-1 未找到');
await click(client, opt.x, opt.y);
await sleep(300);
c = await center(client, '#input');
await click(client, c.x, c.y);
await typeText(client, '文件路径与网址点击测试');
await sleep(120);
await key(client, 'Enter');
for (let i = 0; i < 40; i++) {
  const running = await read(client, `!document.getElementById('btn-stop').classList.contains('hidden') || state.streaming`);
  if (!running) break;
  await sleep(400);
}
await sleep(1500);

// 2) 断言链接渲染
const links = await read(client, `(() => {
  const col = document.getElementById('msg-col');
  const files = [...col.querySelectorAll('a.file-link')].map(a => a.getAttribute('data-path'));
  const urls = [...col.querySelectorAll('a.ext-link')].map(a => a.getAttribute('href'));
  return { files, urls };
})()`);
console.log('[t7] links =', JSON.stringify(links));

// 3) API 负路径：不存在的文件给出明确报错
const bad = await fetch(BASE + '/api/browser/open-view', {
  method: 'POST', headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ session: await read(client, 'state.cur.id'), path: 'demo/不存在.html' }),
});
console.log('[t7] bad path status:', bad.status, (await bad.json()).error);

// 4) GUI 点击 ./mock-演示.txt → 右栏浏览器面板打开该文件
const bstate = async () => (await fetch(BASE + '/api/browser/state')).json();
const fl = await read(client, `(() => {
  const a = [...document.querySelectorAll('#msg-col a.file-link')].find(x => (x.getAttribute('data-path')||'').includes('mock-'));
  if (!a) return null;
  const b = a.getBoundingClientRect();
  return { x: b.left + b.width / 2, y: b.top + b.height / 2, path: a.getAttribute('data-path') };
})()`);
if (!fl) throw new Error('file-link 未渲染');
await click(client, fl.x, fl.y);
await sleep(2500);
const st1 = await bstate();
console.log('[t7] after file-link click, state:', JSON.stringify(st1));
await shot(client, resolve(DIR, 't7_filelink_opened.png'));

// 5) GUI 点击 https://example.com → 面板打开网页
const ul = await read(client, `(() => {
  const a = document.querySelector('#msg-col a.ext-link');
  if (!a) return null;
  const b = a.getBoundingClientRect();
  return { x: b.left + b.width / 2, y: b.top + b.height / 2 };
})()`);
await click(client, ul.x, ul.y);
await sleep(2500);
const st2 = await bstate();
console.log('[t7] after ext-link click, state:', JSON.stringify(st2));

// 6) 反引号单文件名链接也渲染（同一轮正文里）
const codeLink = await read(client, `!!document.querySelector('#msg-col code a.file-link')`);
console.log('[t7] code-span file-link rendered:', codeLink);

await closeTab(client);
client.close();

const results = {
  正文路径链接: links.files.some((p) => p && p.includes('mock-演示.txt')),
  反引号文件名链接: links.files.includes('mock-演示.txt'),
  网址链接: links.urls.includes('https://example.com'),
  坏路径明确报错: bad.status === 404,
  点击文件在右栏打开: (st1.url || '').startsWith('file:///') && st1.url.includes('%E6%BC%94%E7%A4%BA'),
  点击网址在右栏打开: (st2.url || '').startsWith('https://example.com'),
};
console.log('[t7] RESULTS:', JSON.stringify(results, null, 2));
if (Object.values(results).some((v) => !v)) process.exit(2);
