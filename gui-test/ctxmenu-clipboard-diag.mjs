// 剪贴板读链路诊断：真桌面壳（WebView2）里 navigator.clipboard.readText 的行为。
// 背景：冒烟 D2 发现 readText 在 Runtime.evaluate 直调时挂起不结算。本脚本区分
// 「焦点/激活缺失导致」与「WebView2 环境根本不可用」——前者真用户不受影响
// （点菜单时窗口必然有焦点），后者需要给「粘贴」加回退。所有 readText 都套
// Promise.race 超时，保证 evaluate 永远结算（不会再挂死脚本）。
// 运行：仓库根目录 node gui-test/ctxmenu-clipboard-diag.mjs
import { spawn, execSync } from 'node:child_process';
import { writeFileSync, mkdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const TMP = join(tmpdir(), 'jx-ctxmenu-smoke');
const CDP = 'http://127.0.0.1:9224';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

mkdirSync(join(TMP, 'ws'), { recursive: true });
writeFileSync(join(TMP, 'config.json'), JSON.stringify({
  port: 8831, bindHost: '127.0.0.1', baseURL: 'mock', apiKey: '', model: 'mock-model',
  workspace: join(TMP, 'ws'), updateURL: '',
}));
const exe = join(TMP, 'server.exe');
execSync(`go build -o "${exe}" .`, { cwd: fileURLToPath(new URL('..', import.meta.url)), stdio: 'inherit' });
const proc = spawn(exe, ['-config', 'config.json', '-desktop'], {
  cwd: TMP, stdio: 'ignore',
  env: {
    ...process.env,
    WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS: '--remote-debugging-port=9224',
    JX_DESKTOP_AUTOCLOSE: '90',
  },
});

function makeClient(ws) {
  let seq = 0;
  const pending = new Map();
  ws.onmessage = (ev) => {
    const m = JSON.parse(ev.data);
    if (m.id && pending.has(m.id)) {
      const { ok: resolve, bad } = pending.get(m.id);
      pending.delete(m.id);
      m.error ? bad(new Error(m.error.message)) : resolve(m.result);
    }
  };
  return { send(method, params = {}) {
    const id = ++seq;
    ws.send(JSON.stringify({ id, method, params }));
    return new Promise((ok2, bad2) => pending.set(id, { ok: ok2, bad: bad2 }));
  }, close() { ws.close(); } };
}
async function attach() {
  for (let i = 0; i < 60; i++) {
    await sleep(1000);
    try {
      const list = await (await fetch(CDP + '/json/list')).json();
      const page = list.find((t) => t.type === 'page' && String(t.url).includes('shell=desktop'));
      if (!page) continue;
      const ws = new WebSocket(page.webSocketDebuggerUrl);
      await new Promise((ok2, bad2) => { ws.onopen = ok2; ws.onerror = bad2; });
      const c = makeClient(ws);
      await c.send('Runtime.enable', {});
      return c;
    } catch (e) { /* wait */ }
  }
  throw new Error('attach 失败');
}
async function read(c, expr) {
  const r = await c.send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
  if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 200));
  return r.result.value;
}
async function click(c, x, y, button = 'left') {
  for (const type of ['mousePressed', 'mouseReleased']) {
    await c.send('Input.dispatchMouseEvent', { type, x: Math.round(x), y: Math.round(y), button, clickCount: 1 });
    await sleep(40);
  }
}

// 所有 readText 都套 1.5s 超时，永远结算
const PROBE = `Promise.race([
  navigator.clipboard.readText().then((t) => ({ ok: String(t).slice(0, 30) }), (e) => ({ err: String(e && e.message || e) })),
  new Promise((r) => setTimeout(() => r({ hang: true }), 1500)),
])`;

let c;
try {
  c = await attach();
  await sleep(1500);
  console.log('权限状态:', JSON.stringify(await read(c, `navigator.permissions.query({name:'clipboard-read'}).then(p=>p.state)`)));
  console.log('hasFocus(初):', await read(c, `document.hasFocus()`));
  console.log('readText(无激活直调):', JSON.stringify(await read(c, PROBE)));
  // 真实点击页面（带激活）后立刻读
  const pt = await read(c, `(() => { const b = document.body.getBoundingClientRect(); return { x: innerWidth/2, y: 60 }; })()`);
  await click(c, pt.x, pt.y);
  console.log('hasFocus(点击后):', await read(c, `document.hasFocus()`));
  console.log('readText(点击激活后):', JSON.stringify(await read(c, PROBE)));
  // window.focus() 强行抢焦点后读
  await read(c, `window.focus()`);
  console.log('readText(window.focus后):', JSON.stringify(await read(c, PROBE)));
  // writeText 方向也顺带验一下（菜单「复制」用的路径）
  console.log('writeText:', JSON.stringify(await read(c, `navigator.clipboard.writeText('诊断写入').then(() => ({ ok: 1 }), (e) => ({ err: String(e && e.message || e) }))`)));
  console.log('readText(写入后):', JSON.stringify(await read(c, PROBE)));
  c.close();
} catch (e) {
  console.log('诊断异常:', String(e && e.message || e));
  try { c && c.close(); } catch (e2) { /* ignore */ }
}
try { execSync(`taskkill /PID ${proc.pid} /T /F`, { stdio: 'ignore' }); } catch (e) { /* gone */ }
