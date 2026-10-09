// M1 验收：Electron 壳右栏浏览器面板全链路（经 Go 端点，与用户工具栏同一路径）。
// 链路：前端 rpOpen('browser') → POST /api/browser/navigate（Go electronPanel）
//   → panel-bridge → webview loadURL → did-navigate 回推 /api/browser/electron-report
//   → browser_nav SSE → 工具栏地址更新。任何一环断掉，下面的断言都会失败。
// 用法：壳已带 --remote-debugging-port=9223 启动后运行本脚本。
import { writeFileSync } from 'node:fs';

const CDP_HTTP = 'http://127.0.0.1:9223';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function attachExisting() {
  const list = await (await fetch(`${CDP_HTTP}/json`)).json();
  const page = list.find((t) => t.type === 'page' && t.url.includes('shell=desktop'));
  if (!page) throw new Error('未找到 Electron 主窗口目标');
  const ws = new WebSocket(page.webSocketDebuggerUrl);
  await new Promise((ok, bad) => { ws.onopen = ok; ws.onerror = bad; });
  let seq = 0;
  const pending = new Map();
  ws.onmessage = (ev) => {
    const m = JSON.parse(ev.data);
    if (m.id && pending.has(m.id)) {
      const { ok, bad } = pending.get(m.id);
      pending.delete(m.id);
      m.error ? bad(new Error(m.error.message)) : ok(m.result);
    }
  };
  const send = (method, params = {}) => {
    const id = ++seq;
    ws.send(JSON.stringify({ id, method, params }));
    return new Promise((ok, bad) => pending.set(id, { ok, bad }));
  };
  return { send, close: () => ws.close() };
}

const read = async (c, expr) => {
  const r = await c.send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
  if (r.exceptionDetails) throw new Error('read failed: ' + JSON.stringify(r.exceptionDetails).slice(0, 300));
  return r.result.value;
};

const c = await attachExisting();
await sleep(400);

// 1) 打开右栏浏览器视图
await read(c, `rpOpen('browser')`);
await sleep(800);

// 2) 经 Go 导航（example.com 轻量页；失败则回退本机自服务页）
const nav = await read(c, `fetch('/api/browser/navigate', { method: 'POST', headers: {'Content-Type':'application/json'},
  body: JSON.stringify({ url: 'https://example.com' }) }).then(r => r.json()).catch(e => ({ err: String(e) }))`);
console.log('[panel] navigate →', JSON.stringify(nav));
if (!nav.url) throw new Error('navigate 未落定');

// 3) 等 webview 真实加载 + 导航回报链（工具栏地址栏应同步）
let toolbar = null;
for (let i = 0; i < 20; i++) {
  await sleep(300);
  toolbar = await read(c, `(() => {
    const wv = document.getElementById('bpw-webview');
    return { url: wv && wv.getURL ? wv.getURL() : '', barUrl: document.getElementById('bpw-url').value,
             title: document.getElementById('bpw-title').textContent,
             visible: getComputedStyle(wv).display !== 'none' && !!wv.offsetParent };
  })()`);
  if (toolbar.url === nav.url && toolbar.barUrl === nav.url) break;
}
console.log('[panel] webview/toolbar →', JSON.stringify(toolbar));
if (toolbar.url !== nav.url) throw new Error('webview 未加载到目标地址');
if (toolbar.barUrl !== nav.url) throw new Error('工具栏地址未随 browser_nav 同步');

// 4) state 端点（Go→桥 的直读路径）
const st = await read(c, `fetch('/api/browser/state').then(r => r.json())`);
console.log('[panel] state →', JSON.stringify(st));
if (!st.running || st.url !== nav.url) throw new Error('state 未反映运行态');

// 5) NavAction reload（再经 Go→桥走一次）
const rl = await read(c, `fetch('/api/browser/navigate', { method: 'POST', headers: {'Content-Type':'application/json'},
  body: JSON.stringify({ action: 'reload' }) }).then(r => r.json())`);
console.log('[panel] reload →', JSON.stringify(rl));
if (rl.url !== nav.url) throw new Error('reload 后地址不一致');

// 6) 视觉证据：右栏面板里渲染的 example.com
const shot = await c.send('Page.captureScreenshot', { format: 'png' });
writeFileSync('gui-test-screenshots/electron-m1-panel.png', Buffer.from(shot.data, 'base64'));
console.log('[panel] 截图已存 gui-test-screenshots/electron-m1-panel.png');

// 7) CDP 目标列表里应出现 guest（webview 的独立渲染进程）
const targets = await (await fetch(`${CDP_HTTP}/json`)).json();
const guest = targets.find((t) => t.url.includes('example.com'));
console.log('[panel] guest target →', guest ? guest.url : '（未列出，不阻断：Electron 对 webview guest 的暴露策略不同）');

c.close();
console.log('[panel] M1 验收全过 ✓');
