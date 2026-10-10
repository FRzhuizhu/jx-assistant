// 「同一条消息一边执行、一边又进了排队队列」修复验证（隔离实例 + CDP）。
// 背景（手机远程访问高频）：远程链路抖动把 /chat 的 SSE 响应流中途掐断时，
// send() 的 catch 探测到会话仍在执行，就把同一条消息再发一遍到 /interject——
// 而服务端其实早已落库这条消息并在执行，于是出现「执行记录 + 排队项」两条
// 一样的。修复：拿到 2xx 响应头即认定已落库绝不重发；响应头都没回来的断连
// 再用内容比对（userChatLanded）兜底；只有确认服务端没收下且会话在跑才降级
// 排队。三个用例：
//   A. 断流不重发：chat 请求真发到服务端（落库+执行），响应 body 透传第一批
//      数据后 error —— 不得调 /interject，服务端队列无此消息、库里恰 1 条。
//   B. 响应头没回来的兜底：chat 发出后 900ms 才 throw（请求其实已被服务端
//      收下）—— 靠 userChatLanded 内容比对拦住重发，断言同 A。
//   C. 真 409 不回归：chat 被(劫持)拒 409、详情探测在跑且库里没有这条 ——
//      必须仍降级调一次 /interject（排队不丢的老行为）。
// 运行：仓库根目录 node gui-test/dupqueue-verify.mjs
import { newTab, read, sleep, closeTab, shot } from './cdp.mjs';
import { spawn, execSync } from 'node:child_process';
import { writeFileSync, mkdirSync, accessSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const BASE = 'http://127.0.0.1:8842';
const CDP_HTTP = 'http://127.0.0.1:9223';
const ROOT = fileURLToPath(new URL('..', import.meta.url));
const TMP = join(tmpdir(), 'jx-dupqueue-' + Date.now().toString(36));
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
    port: 8842, bindHost: '127.0.0.1', baseURL: 'mock', apiKey: '', model: 'mock-model',
    workspace: join(TMP, 'ws'), updateURL: '',
  }));
  serverProc = spawn(exe, ['-config', 'config.json', '-noopen'], { cwd: TMP, stdio: 'ignore' });
  for (let i = 0; i < 40; i++) {
    await sleep(500);
    try { const r = await fetch(BASE + '/api/status'); if (r.ok) return; } catch (e2) { /* wait */ }
  }
  throw new Error('server 没起来');
}

// 页面侧劫持：__chatMode 控制 chat 请求的失败形态；__fakeDetail 用假会话详情
// （在跑、库里没有这条）喂给降级探测；__blockInterject 把 interject 拦在本地
// 只记录不真发（用例 C 断言前端行为即可，不污染服务端状态）。
const HIJACK = `(() => {
  if (window.__dupqHijack) return true;
  window.__dupqHijack = true;
  window.__chatMode = null;
  window.__fakeDetail = false;
  window.__blockInterject = false;
  window.__interjectCalls = [];
  const real = window.fetch.bind(window);
  window.fetch = async (u, init) => {
    const s = typeof u === 'string' ? u : String((u && u.url) || '');
    const method = String((init && init.method) || 'GET').toUpperCase();
    const isChat = /\\/chat$/.test(s) && method === 'POST';
    const isDetail = /^\\/api\\/sessions\\/[^/]+$/.test(s) && method === 'GET';
    if (isChat && window.__chatMode === '409') {
      return new Response(JSON.stringify({ error: '该会话正在执行中，请稍候再发' }),
        { status: 409, headers: { 'Content-Type': 'application/json' } });
    }
    if (isDetail && window.__fakeDetail) {
      return new Response(JSON.stringify({ meta: { id: window.__curSid, status: 'running' },
        running: true, messages: [], pending: { steer: [], queue: [] } }),
        { status: 200, headers: { 'Content-Type': 'application/json' } });
    }
    if (/\\/interject$/.test(s) && method === 'POST') {
      window.__interjectCalls.push(String((init && init.body) || ''));
      if (window.__blockInterject) {
        return new Response(JSON.stringify({ accepted: true, id: 'fake', mode: 'queue' }),
          { status: 200, headers: { 'Content-Type': 'application/json' } });
      }
    }
    const res = await real(u, init);
    if (isChat && res.body && window.__chatMode === 'breakbody') {
      const reader = res.body.getReader();
      let broke = false;
      const stream = new ReadableStream({
        async pull(ctrl) {
          if (broke) { ctrl.error(new TypeError('Failed to fetch')); return; }
          const r = await reader.read();
          if (r.done) { ctrl.close(); return; }
          ctrl.enqueue(r.value);
          broke = true; // 第一批已透传：下一次 pull 模拟网络中断
        },
      });
      return new Response(stream, { status: res.status, headers: res.headers });
    }
    if (isChat && window.__chatMode === 'throw') {
      real(u, init); // 请求真发出去（服务端照常落库执行），只是不给前端响应头
      await new Promise((r) => setTimeout(r, 900));
      throw new TypeError('network seems broken');
    }
    return res;
  };
  return true;
})()`;

// 服务端视角核对：这条内容在库里落了几条 user/chat、排队队列里有没有
async function serverState(sid, content) {
  const d = await (await fetch(`${BASE}/api/sessions/${sid}`)).json();
  const msgs = (d.messages || []).filter((m) => m.role === 'user' && (!m.kind || m.kind === 'chat') && m.content === content);
  const queued = ((d.pending && d.pending.queue) || []).filter((p) => p.content === content);
  return { landedCount: msgs.length, queuedCount: queued.length };
}

async function makeSession(title) {
  const r = await fetch(BASE + '/api/sessions', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title }),
  });
  const s = await r.json();
  return s.id;
}

await ensureEdge();
await ensureServer();

const c = await newTab(BASE + '/index.html', 1440, 900);
let failed = 0;
try {
  await sleep(1500);
  await read(c, HIJACK);

  // ---- 用例 A：流中途断（响应头已到，消息已落库执行）----
  const sidA = await makeSession('dupq-A-断流');
  await read(c, `openSession(${JSON.stringify(sidA)})`);
  await sleep(1200);
  await read(c, `(() => { window.__curSid = ${JSON.stringify(sidA)}; window.__chatMode = 'breakbody'; return true; })()`);
  const msgA = '断流场景验证：这条消息只应执行一次';
  await read(c, `send(${JSON.stringify(msgA)}, mainView)`);
  await sleep(1500); // 断流 catch 与收尾都在 send 返回前完成，稍等探测余波
  const stA = await serverState(sidA, msgA);
  const callsA = await read(c, 'window.__interjectCalls.length');
  check('A1. 断流后不再调 /interject 降级', callsA === 0, { interjectCalls: callsA });
  check('A2. 服务端排队队列无此消息', stA.queuedCount === 0, stA);
  check('A3. 库里这条消息恰落 1 条', stA.landedCount === 1, stA);

  // ---- 用例 B：响应头都没回来（内容比对兜底）----
  const sidB = await makeSession('dupq-B-无响应头');
  await read(c, `openSession(${JSON.stringify(sidB)})`);
  await sleep(1200);
  await read(c, `(() => { window.__curSid = ${JSON.stringify(sidB)}; window.__chatMode = 'throw'; return true; })()`);
  const msgB = '无响应头场景验证：靠内容比对拦住重发';
  await read(c, `send(${JSON.stringify(msgB)}, mainView)`);
  await sleep(1500);
  const stB = await serverState(sidB, msgB);
  const callsB = await read(c, 'window.__interjectCalls.length');
  check('B1. 无响应头也不降级重发', callsB === 0, { interjectCalls: callsB });
  check('B2. 服务端排队队列无此消息', stB.queuedCount === 0, stB);
  check('B3. 库里这条消息恰落 1 条', stB.landedCount === 1, stB);

  // ---- 用例 C：真被 409 拒收时降级排队仍工作（不回归）----
  const sidC = await makeSession('dupq-C-真409');
  await read(c, `openSession(${JSON.stringify(sidC)})`);
  await sleep(1200);
  await read(c, `(() => { window.__curSid = ${JSON.stringify(sidC)};
    window.__chatMode = '409'; window.__fakeDetail = true; window.__blockInterject = true; return true; })()`);
  const msgC = '真409场景验证：必须降级排队一次';
  await read(c, `send(${JSON.stringify(msgC)}, mainView)`);
  await sleep(1200);
  const callsC = await read(c, 'window.__interjectCalls');
  const cOk = Array.isArray(callsC) && callsC.length === 1 && callsC[0].includes(msgC);
  check('C1. 409 被拒后仍降级调一次 /interject（带原文）', cOk, { calls: callsC });
  await read(c, `(() => { window.__chatMode = null; window.__fakeDetail = false; window.__blockInterject = false; return true; })()`);
  await sleep(800);
  const stC = await serverState(sidC, msgC);
  check('C2. 拦截模式下服务端不被污染（库里 0 条）', stC.landedCount === 0 && stC.queuedCount === 0, stC);

  await shot(c, './gui-test-screenshots/dupqueue_final.png');
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
