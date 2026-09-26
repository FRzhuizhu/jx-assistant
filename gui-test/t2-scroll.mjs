// T2：流式输出期间上翻不被拽回底部（钉住式跟随）
// 场景1：输出中大幅上翻 → 回合结束后停在阅读位置（不回底）
// 场景2：回到底部按钮出现并可点击回底
// 场景3：输出中单格上翻（旧版 120px 窗口必被拽回的最小复现）→ 停住
// 场景4：滚回底部后恢复跟随（新一轮输出自动贴底）
import { newTab, read, click, key, typeText, shot, center, sleep, wheel } from './cdp.mjs';
import { resolve } from 'node:path';

const BASE = 'http://127.0.0.1:9721';
const DIR = resolve(process.cwd(), '..', 'gui-test-screenshots');

const client = await newTab(BASE + '/', 1280, 460);
await sleep(800);

// 打开 mock 测试会话
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

console.log('[t2] initial pos =', JSON.stringify(await pos()));
if ((await pos()).h <= (await pos()).ch) throw new Error('transcript not scrollable at 460px viewport');

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

// ---- 场景1：输出中大幅上翻 ----
await sendMsg('第二轮：输出中大幅上翻');
await sleep(220); // 输出进行中
const m = await center(client, '#messages');
await wheel(client, m.x, m.y, 120, 8);
const afterWheel = await pos();
console.log('[t2] S1 after 8-notch wheel-up:', JSON.stringify(afterWheel));
await sleep(400); // 剩余输出与收尾渲染继续落地
await waitIdle();
await sleep(900);
const settled1 = await pos();
console.log('[t2] S1 settled after turn end:', JSON.stringify(settled1));
await shot(client, resolve(DIR, 't2_s1_pinned.png'));

// ---- 场景2：回到底部按钮 ----
const btn = await read(client, `(() => {
  const b = document.querySelector('.jump-latest');
  if (!b) return null;
  const show = b.classList.contains('show');
  const r = b.getBoundingClientRect();
  return { show, x: r.left + r.width / 2, y: r.top + r.height / 2 };
})()`);
console.log('[t2] S2 jump btn =', JSON.stringify(btn));
if (btn && btn.show) {
  await click(client, btn.x, btn.y);
  await sleep(900); // smooth 滚动
  console.log('[t2] S2 after jump click:', JSON.stringify(await pos()));
}

// ---- 场景3：输出中单格上翻（旧版必被拽回） ----
await sendMsg('第三轮：单格上翻');
await sleep(200);
await wheel(client, m.x, m.y, 120, 1);
const oneNotch = await pos();
console.log('[t2] S3 after 1-notch wheel-up:', JSON.stringify(oneNotch));
await waitIdle();
await sleep(900);
const settled3 = await pos();
console.log('[t2] S3 settled after turn end:', JSON.stringify(settled3));
await shot(client, resolve(DIR, 't2_s3_onenotch.png'));

// ---- 场景4：滚回底部恢复跟随 ----
await wheel(client, m.x, m.y, -120, 12);
await sleep(500);
console.log('[t2] S4 wheeled to bottom:', JSON.stringify(await pos()));
await sendMsg('第四轮：回底后跟随');
await sleep(300);
const midFollow = await pos();
await waitIdle();
await sleep(600);
const endFollow = await pos();
console.log('[t2] S4 mid-output:', JSON.stringify(midFollow), 'end:', JSON.stringify(endFollow));
await shot(client, resolve(DIR, 't2_s4_following.png'));

client.close();

// ---- 判定 ----
const ok1 = settled1.dist > 100 && settled1.pinned;
const ok2 = !btn || !btn.show || (await pos, true);
const ok3 = settled3.dist > 60 && settled3.pinned;
const ok4 = endFollow.dist <= 8;
console.log('\n[t2] RESULTS:', { S1_大幅上翻钉住: ok1, S2_回底按钮出现: btn && btn.show, S3_单格上翻停住: ok3, S4_回底后恢复跟随: ok4 });
