// 「发消息出去界面没更新，切走再切回来才正常」复现与修复验证（隔离实例 + CDP）。
// 根因：openSession/openSubSession 的整包加载（GET /api/sessions/{id}）在途时用户
// 发了消息——用户回显经 /chat 的 SSE 流落地并渲染；随后迟到的整包响应（服务端
// 快照取于落库之前）无条件 state.msgs = data.messages，把刚显示的消息整段顶回
// 发送之前。界面看起来就是「没更新」；navTok 只挡「更新的导航」，挡不住「更旧
// 的数据」。用例：
//   A. 主对话：切换在途时发消息 → 释放迟到的整包 → 消息不得从界面消失。
//   B. 子对话：openSubSession 同款竞态，同样不得消失。
//   C. 正常路径不回归：切换加载（不在途发送）后消息照常整包显示。
// 运行：仓库根目录 node gui-test/stale-load-verify.mjs
import { newTab, read, sleep, closeTab, shot } from './cdp.mjs';
import { spawn, execSync } from 'node:child_process';
import { writeFileSync, mkdirSync, accessSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const BASE = 'http://127.0.0.1:8843';
const CDP_HTTP = 'http://127.0.0.1:9223';
const ROOT = fileURLToPath(new URL('..', import.meta.url));
mkdirSync(join(ROOT, 'gui-test-screenshots'), { recursive: true });
const TMP = join(tmpdir(), 'jx-staleload-' + Date.now().toString(36));
const results = [];
function check(name, pass, detail) {
  results.push({ name, pass, detail });
  console.log((pass ? 'PASS ' : 'FAIL ') + name + (detail === undefined ? '' : '  | ' + JSON.stringify(detail)));
}

async function ensureEdge() {
  try { await fetch(CDP_HTTP + '/json/version'); return; } catch (e) { /* spawn */ }
  const cands = [
    'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',
    'C:\\Program Files\\Microsoft\\Edge\\Application\\msedge.exe',
  ];
  const edge = cands.find((p) => { try { accessSync(p); return true; } catch (e2) { return false; } });
  if (!edge) throw new Error('没找到 msedge.exe');
  spawn(edge, ['--headless=new', '--remote-debugging-port=9223',
    '--user-data-dir=' + join(TMP, 'edge'), 'about:blank'], { stdio: 'ignore' });
  for (let i = 0; i < 30; i++) {
    await sleep(500);
    try { await fetch(CDP_HTTP + '/json/version'); return; } catch (e2) { /* wait */ }
  }
  throw new Error('Edge CDP 没起来');
}

let serverProc = null;
async function ensureServer() {
  try { const r = await fetch(BASE + '/api/status'); if (r.ok) return; } catch (e) { /* spawn */ }
  mkdirSync(join(TMP, 'ws'), { recursive: true });
  const exe = join(TMP, 'server.exe');
  execSync(`go build -o "${exe}" .`, { cwd: ROOT, stdio: 'inherit' });
  writeFileSync(join(TMP, 'config.json'), JSON.stringify({
    port: 8843, bindHost: '127.0.0.1', baseURL: 'mock', apiKey: '', model: 'mock-model',
    workspace: join(TMP, 'ws'), updateURL: '',
  }));
  serverProc = spawn(exe, ['-config', 'config.json', '-noopen'], { cwd: TMP, stdio: 'ignore' });
  for (let i = 0; i < 40; i++) {
    await sleep(500);
    try { const r = await fetch(BASE + '/api/status'); if (r.ok) return; } catch (e2) { /* wait */ }
  }
  throw new Error('server 没起来');
}

// 页面侧劫持：__holdDetails>0 时把接下来的 N 次 GET 会话详情扣住不发回，
// 等释放（__releaseHeld()）后才返回响应——精确模拟「整包在途」的慢网窗口。
const HIJACK = `(() => {
  if (window.__staleHijack) return true;
  window.__staleHijack = true;
  window.__holdDetails = 0;
  window.__heldWaiters = [];
  const real = window.fetch.bind(window);
  window.__releaseHeld = () => { const w = window.__heldWaiters; window.__heldWaiters = []; w.forEach((r) => r()); };
  window.fetch = async (u, init) => {
    const s = typeof u === 'string' ? u : String((u && u.url) || '');
    const method = String((init && init.method) || 'GET').toUpperCase();
    const isDetail = /\\/api\\/sessions\\/[^/]+$/.test(s) && method === 'GET';
    if (isDetail && window.__holdDetails > 0) {
      window.__holdDetails--;
      const res = await real(u, init);
      await new Promise((r) => window.__heldWaiters.push(r));
      return res;
    }
    return real(u, init);
  };
  return true;
})()`;

async function makeSession(body) {
  const r = await fetch(BASE + '/api/sessions', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  const s = await r.json();
  return s.id;
}

// 会话跑完一轮（mock 模型）并落库这条用户消息
async function runOneRound(sid, content) {
  await fetch(`${BASE}/api/sessions/${sid}/chat`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content }),
  }).then((r) => r.body.cancel());
  for (let i = 0; i < 60; i++) {
    await sleep(300);
    const d = await (await fetch(`${BASE}/api/sessions/${sid}`)).json();
    const landed = (d.messages || []).some((m) => m.role === 'user' && m.content === content);
    if (!d.running && landed) return d;
  }
  throw new Error('seed 一轮没跑完');
}

// 界面（主列 or 右栏子对话列）里是否看得到这条正文
const visibleIn = (colSel, text) =>
  `Array.from(document.querySelectorAll('${colSel} .msg.user .bubble'))` +
  `.some((b) => b.textContent.includes(${JSON.stringify(text)}))`;

await ensureEdge();
await ensureServer();

// 种子会话先建好（页面启动 loadSessions 就能带上它们，乐观切换的 brief 才在）
const sidA = await makeSession({ mode: 'task', title: 'stale-A-主对话' });
await runOneRound(sidA, '历史消息：这是切换前的旧内容A');
const parentB = await makeSession({ mode: 'task', title: 'stale-B-父对话' });
const sidB = await makeSession({ parent: parentB, title: 'stale-B-子对话' });
await runOneRound(sidB, '历史消息：这是切换前的旧内容B');
const sidC = await makeSession({ mode: 'task', title: 'stale-C-正常路径' });
await runOneRound(sidC, '历史消息：这是切换前的旧内容C');

const c = await newTab(BASE + '/index.html', 1440, 900);
let failed = 0;
try {
  await sleep(1500);
  await read(c, HIJACK);
  await read(c, 'loadSessions()');
  await sleep(300);

  // ---- 用例 A：主对话，切换在途时发消息，迟到的整包不得顶掉已显示的消息 ----
  // 注意：openSession/send 都不能 awaitPromise——整包被扣住时它们的 promise
  // 不会落地，求值会挂死脚本；只点火，用服务端轮询等结果。
  const msgA = '竞态消息A：整包加载在途时发出的这条不能消失';
  await read(c, 'window.__holdDetails = 1');
  await read(c, `void openSession(${JSON.stringify(sidA)}); 'opened'`); // 整包 GET 已被扣住（在途）
  await sleep(400); // 打开即发：不等整包回来
  await read(c, `void send(${JSON.stringify(msgA)}, mainView); 'sent'`);
  await runOneRound(sidA, msgA); // 服务端视角等这轮跑完落库
  await sleep(600); // 让 endTurn 收尾（reloadMessages 也被 _loading 挡住/或在途）
  const beforeRelease = await read(c, visibleIn('#msg-col', msgA));
  await read(c, 'window.__releaseHeld()'); // 释放迟到的整包（快照取于发送之前）
  await sleep(1000);
  const afterRelease = await read(c, visibleIn('#msg-col', msgA));
  const dA = await (await fetch(`${BASE}/api/sessions/${sidA}`)).json();
  const landedA = (dA.messages || []).filter((m) => m.role === 'user' && m.content === msgA).length;
  check('A1. 释放前消息已在界面显示（流式帧已落地）', beforeRelease === true, { beforeRelease });
  check('A2. 释放迟到的整包后消息仍显示（不回退）', afterRelease === true, { afterRelease });
  check('A3. 服务端恰落 1 条（消息确实已收下）', landedA === 1, { landedA });
  // 症状自愈路径也不回归：切走再切回来仍正常
  await read(c, `openSession(${JSON.stringify(sidC)})`);
  await sleep(900);
  await read(c, `openSession(${JSON.stringify(sidA)})`);
  await sleep(900);
  const backA = await read(c, visibleIn('#msg-col', msgA));
  check('A4. 切走再切回仍能看到这条消息', backA === true, { backA });

  // ---- 用例 B：子对话，openSubSession 同款竞态 ----
  const msgB = '竞态消息B：子对话整包加载在途时发出的这条不能消失';
  await read(c, `openSession(${JSON.stringify(parentB)})`);
  await sleep(900);
  await read(c, 'window.__holdDetails = 1');
  await read(c, `void openSubSession(${JSON.stringify(sidB)}); 'opened'`); // 子对话整包 GET 被扣住
  await sleep(400);
  await read(c, `void send(${JSON.stringify(msgB)}, subView); 'sent'`);
  await runOneRound(sidB, msgB);
  await sleep(600);
  await read(c, 'window.__releaseHeld()');
  await sleep(1000);
  const afterB = await read(c, visibleIn('#sub-msg-col', msgB));
  check('B1. 释放迟到的整包后子对话消息仍显示', afterB === true, { afterB });

  // ---- 用例 C：正常路径不回归（不在途发送，整包照常显示） ----
  const msgC = '正常路径C：整包加载完成后界面照常显示历史';
  await read(c, `openSession(${JSON.stringify(sidC)})`);
  await sleep(900);
  const histC = await read(c, visibleIn('#msg-col', '历史消息：这是切换前的旧内容C'));
  check('C1. 正常切换整包加载照常显示历史', histC === true, { histC });

  await shot(c, './gui-test-screenshots/stale-load_final.png');
} catch (e) {
  console.error('脚本异常：', e);
  failed = 1;
} finally {
  closeTab(c);
}
failed = results.some((r) => !r.pass) ? 1 : failed;
console.log(failed ? 'RESULT: FAIL' : 'RESULT: ALL PASS');
if (serverProc) serverProc.kill();
process.exit(failed);
