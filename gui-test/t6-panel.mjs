// T6：右栏「浏览器」面板全链路（修复相对路径后的首次真实验证）
// 面板打开 → 地址栏导航自检页 → 帧流到达 → 覆盖层点击(+1) → 滚轮 → 视口
import { newTab, read, click, key, typeText, shot, center, sleep, wheel, closeTab } from './cdp.mjs';
import { resolve } from 'node:path';

const BASE = 'http://127.0.0.1:9725';
const DIR = resolve(process.cwd(), '..', 'gui-test-screenshots');

const client = await newTab(BASE + '/', 1280, 900);
const bstate = async () => (await fetch(BASE + '/api/browser/state')).json();
// 自检页把状态镜像进 document.title 有延迟：轮询等到谓词成立
const waitTitle = async (pred, ms = 6000) => {
  const t0 = Date.now();
  for (;;) {
    const t = (await bstate()).title || '';
    if (pred(t)) return t;
    if (Date.now() - t0 > ms) return t;
    await sleep(250);
  }
};
await sleep(1200);

// 1) 打开右栏（上轮可能已开着：localStorage 记忆）
const rpHidden = await read(client, `document.getElementById('rpanel').classList.contains('hidden')`);
if (rpHidden) {
  const rp = await center(client, '#btn-rpanel');
  await click(client, rp.x, rp.y);
  await sleep(1500);
}

// 2) 面板结构：iframe 是否加载、浏览器页签是否在
const panelInfo = await read(client, `(() => {
  const panel = document.getElementById('rpanel');
  const tabs = [...document.querySelectorAll('.rp-tab')].map(b => b.textContent.trim());
  const ifr = [...document.querySelectorAll('#rpanel iframe')].map(f => f.getAttribute('src'));
  return { panelHidden: panel.classList.contains('hidden'), tabs, iframes: ifr };
})()`);
console.log('[t6] panel:', JSON.stringify(panelInfo));

// 2b) 切到「浏览器」页签（iframe 懒挂载）
const tabPt = await read(client, `(() => {
  const b = [...document.querySelectorAll('.rp-tab')].find(t => t.textContent.trim() === '浏览器');
  if (!b) return null;
  const r = b.getBoundingClientRect();
  return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
})()`);
if (!tabPt) throw new Error('浏览器页签未找到');
await click(client, tabPt.x, tabPt.y);
await sleep(1500);

// 3) 聚焦 iframe 里的地址栏并输入自检页地址
const urlBox = await read(client, `(() => {
  const f = [...document.querySelectorAll('#rpanel iframe')].find(fr => (fr.getAttribute('src')||'').includes('browser.html'));
  if (!f) return null;
  const fr = f.getBoundingClientRect();
  const d = f.contentDocument;
  const el = d && d.getElementById('b-url');
  if (!el) return { noInput: true };
  const r = el.getBoundingClientRect();
  return { x: fr.left + r.left + r.width / 2, y: fr.top + r.top + r.height / 2 };
})()`);
if (!urlBox || urlBox.noInput) throw new Error('面板地址栏未找到: ' + JSON.stringify(urlBox));
await click(client, urlBox.x, urlBox.y);
await sleep(200);
// 全选旧内容再输入（insertText 是插入不是替换）
for (const t of ['keyDown', 'keyUp']) {
  await client.send('Input.dispatchKeyEvent', { type: t, key: 'a', code: 'KeyA', windowsVirtualKeyCode: 65, modifiers: 2 });
  await sleep(30);
}
await sleep(100);
await typeText(client, BASE + '/panel/selftest.html');
await sleep(150);
await key(client, 'Enter');
console.log('[t6] navigated via address bar, waiting...');
await sleep(3500);

// 4) 后端状态 + 帧流到达
const state1 = await bstate();
const frameInfo = await read(client, `(() => {
  const f = [...document.querySelectorAll('#rpanel iframe')].find(fr => (fr.getAttribute('src')||'').includes('browser.html'));
  const d = f.contentDocument;
  const img = d.getElementById('b-img');
  return { imgSrc: img.getAttribute('src'), complete: img.complete, nw: img.naturalWidth, nh: img.naturalHeight,
           hint: d.getElementById('b-hint').className };
})()`);
console.log('[t6] backend state:', JSON.stringify(state1));
console.log('[t6] frame img:', JSON.stringify(frameInfo));
await shot(client, resolve(DIR, 't6_panel_selftest.png'));

// 5) 覆盖层点击 +1 按钮（坐标来自自检页 title 镜像 inc=x,y）
const title1 = state1.title || '';
const m = title1.match(/inc=([-\d.]+),([-\d.]+)/);
if (!m) throw new Error('title 未含 inc 坐标: ' + title1);
const pt = await read(client, `(() => {
  const f = [...document.querySelectorAll('#rpanel iframe')].find(fr => (fr.getAttribute('src')||'').includes('browser.html'));
  const fr = f.getBoundingClientRect();
  const d = f.contentDocument;
  const frameEl = d.getElementById('b-frame');
  const r = frameEl.getBoundingClientRect();
  const vw = ${state1.viewportW}, vh = ${state1.viewportH};
  const vx = ${m[1]}, vy = ${m[2]};
  return { x: fr.left + r.left + vx * r.width / vw, y: fr.top + r.top + vy * r.height / vh };
})()`);
await click(client, pt.x, pt.y);
const tClick = await waitTitle((t) => /n=[1-9]/.test(t));
console.log('[t6] after click, title:', tClick);

// 6) 覆盖层滚轮（真实 wheel 打在覆盖层上）
const ovPt = await read(client, `(() => {
  const f = [...document.querySelectorAll('#rpanel iframe')].find(fr => (fr.getAttribute('src')||'').includes('browser.html'));
  const fr = f.getBoundingClientRect();
  const d = f.contentDocument;
  const r = d.getElementById('b-overlay').getBoundingClientRect();
  return { x: fr.left + r.left + r.width / 2, y: fr.top + r.top + r.height / 2 };
})()`);
await wheel(client, ovPt.x, ovPt.y, 240, 3);
const tWheel = await waitTitle((t) => { const y = (t.match(/y=(-?\d+)/) || [])[1]; return y !== undefined && parseInt(y) > 0; });
console.log('[t6] after wheel, title:', tWheel);
await shot(client, resolve(DIR, 't6_panel_scrolled.png'));

await closeTab(client);
client.close();

const n1 = (tClick.match(/n=(\d+)/) || [])[1];
const y1 = (tWheel.match(/y=(-?\d+)/) || [])[1];
const results = {
  面板打开且含浏览器页签: !panelInfo.panelHidden && panelInfo.tabs.includes('浏览器'),
  地址栏导航成功: (state1.url || '').includes('selftest.html'),
  帧流到达_有像素: frameInfo.complete && frameInfo.nw > 0,
  点击转发_计数增加: n1 !== undefined && parseInt(n1) >= 1,
  滚轮转发_页面滚动: y1 !== undefined && parseInt(y1) > 0,
};
console.log('[t6] RESULTS:', JSON.stringify(results, null, 2));
if (Object.values(results).some((v) => !v)) process.exit(2);
