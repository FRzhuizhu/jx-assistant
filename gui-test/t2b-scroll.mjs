// T2 修正版：deltaY 负值 = 上翻（此前符号写反）
import { newTab, read, click, key, typeText, shot, center, sleep, wheel } from './cdp.mjs';
import { resolve } from 'node:path';

const BASE = 'http://127.0.0.1:9721';
const DIR = resolve(process.cwd(), '..', 'gui-test-screenshots');

const client = await newTab(BASE + '/', 1280, 460);
await sleep(800);

const t = await read(client, `(() => {
  const h = [...document.querySelectorAll('aside .s-title')].find(el => el.textContent.includes('折叠与滚动回归'));
  if (!h) return null;
  const b = h.getBoundingClientRect();
  return { x: b.left + b.width / 2, y: b.top + b.height / 2 };
})()`);
if (!t) throw new Error('测试会话未找到');
await click(client, t.x, t.y);
await sleep(1200);

const pos = () => read(client, `(() => {
  const el = document.getElementById('messages');
  return { top: Math.round(el.scrollTop), h: el.scrollHeight, ch: el.clientHeight,
    dist: Math.round(el.scrollHeight - el.scrollTop - el.clientHeight),
    pinned: mainView._pinned === true };
})()`);

const m = await center(client, '#messages');
const sendMsg = async (text) => {
  const c = await center(client, '#input');
  await click(client, c.x, c.y);
  await typeText(client, text);
  await sleep(120);
  await key(client, 'Enter');
};
const waitIdle = async () => {
  for (let i = 0; i < 40; i++) {
    const running = await read(client, `!document.getElementById('btn-stop').classList.contains('hidden') || state.streaming`);
    if (!running) return true;
    await sleep(400);
  }
  return false;
};

console.log('[t2b] initial =', JSON.stringify(await pos()));

// ---- S1：输出中大幅上翻（-120 ×8） ----
await sendMsg('第五轮：输出中大幅上翻');
await sleep(220);
await wheel(client, m.x, m.y, -120, 8);
const s1a = await pos();
console.log('[t2b] S1 right after wheel-up:', JSON.stringify(s1a));
await sleep(400);
await waitIdle();
await sleep(900);
const s1b = await pos();
console.log('[t2b] S1 settled:', JSON.stringify(s1b));
await shot(client, resolve(DIR, 't2b_s1_pinned.png'));

// ---- S2：回到底部按钮出现并点击 ----
const btn = await read(client, `(() => {
  const b = document.querySelector('.jump-latest');
  if (!b) return null;
  const r = b.getBoundingClientRect();
  return { show: b.classList.contains('show'), x: r.left + r.width / 2, y: r.top + r.height / 2 };
})()`);
console.log('[t2b] S2 jump btn:', JSON.stringify(btn));
let s2after = null;
if (btn && btn.show) {
  await click(client, btn.x, btn.y);
  await sleep(1000);
  s2after = await pos();
  console.log('[t2b] S2 after jump click:', JSON.stringify(s2after));
}

// ---- S3：输出中单格上翻（-120 ×1，旧版 120px 窗口必被拽回） ----
await sendMsg('第六轮：单格上翻');
await sleep(200);
await wheel(client, m.x, m.y, -120, 1);
const s3a = await pos();
console.log('[t2b] S3 right after 1-notch:', JSON.stringify(s3a));
await waitIdle();
await sleep(900);
const s3b = await pos();
console.log('[t2b] S3 settled:', JSON.stringify(s3b));
await shot(client, resolve(DIR, 't2b_s3_onenotch.png'));

// ---- S4：向下滚回底部 → 恢复跟随 ----
await wheel(client, m.x, m.y, 120, 12);
await sleep(600);
const s4a = await pos();
console.log('[t2b] S4 wheeled down to bottom:', JSON.stringify(s4a));
await sendMsg('第七轮：回底后跟随');
await sleep(350);
const s4mid = await pos();
await waitIdle();
await sleep(700);
const s4b = await pos();
console.log('[t2b] S4 mid:', JSON.stringify(s4mid), 'end:', JSON.stringify(s4b));
await shot(client, resolve(DIR, 't2b_s4_following.png'));

client.close();

const results = {
  S1_输出中大幅上翻_停住不回底: s1b.dist > 100 && s1b.pinned,
  S2_回底按钮_出现并可回底: !!(btn && btn.show) && (!s2after || s2after.dist <= 8),
  S3_单格上翻_停住不被拽回: s3b.dist > 60 && s3b.pinned,
  S4_滚回底部_恢复跟随: s4a.dist <= 48 && s4b.dist <= 8,
};
console.log('\n[t2b] RESULTS:', JSON.stringify(results, null, 2));
if (Object.values(results).some((v) => !v)) process.exit(2);
