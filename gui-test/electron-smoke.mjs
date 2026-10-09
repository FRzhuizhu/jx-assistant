// M0/M1 验收：连接已启动的 Electron 壳（--remote-debugging-port=9223），
// 截图并检查 electron-shell 标记、语音按钮隐藏、<webview> 挂载与面板桥连通。
// 用法：先启动壳（带调试端口），再 node gui-test/electron-smoke.mjs
import { writeFileSync } from 'node:fs';

const CDP_HTTP = 'http://127.0.0.1:9223';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function attachExisting() {
  const list = await (await fetch(`${CDP_HTTP}/json`)).json();
  const page = list.find((t) => t.type === 'page' && t.url.includes('shell=desktop'));
  if (!page) throw new Error('未找到 Electron 主窗口目标: ' + JSON.stringify(list.map((t) => t.url)));
  const ws = new WebSocket(page.webSocketDebuggerUrl);
  await new Promise((ok, bad) => { ws.onopen = ok; ws.onerror = bad; });
  const seq = { n: 0 };
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
    const id = ++seq.n;
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
await sleep(500);

const checks = await read(c, `(() => {
  const wv = document.getElementById('bpw-webview');
  return {
    electronClass: document.documentElement.classList.contains('electron-shell'),
    desktopClass: document.documentElement.classList.contains('desktop-shell'),
    jxElectron: !!window.JX_ELECTRON,
    voiceMainHidden: !document.getElementById('btn-voice') || document.getElementById('btn-voice').classList.contains('hidden'),
    webviewExists: !!wv,
    webviewDisplay: wv ? getComputedStyle(wv).display : 'none',
    sessionsLoaded: document.querySelectorAll('#sess-list .sess-item, #drawer .sess-item').length,
    title: document.title,
  };
})()`);
console.log('[smoke]', JSON.stringify(checks, null, 2));

// 打开右栏浏览器视图（模拟用户点顶栏按钮）并截图验证 webview 显示
const shot = await c.send('Page.captureScreenshot', { format: 'png' });
writeFileSync('gui-test-screenshots/electron-m0.png', Buffer.from(shot.data, 'base64'));
console.log('[smoke] 截图已存 gui-test-screenshots/electron-m0.png');

// 面板桥连通性（经 Go 端点间接验证：embeddedTarget 已是 electron 桥时 state 走桥）
const goState = await read(c, `fetch('/api/browser/state').then(r => r.json())`);
console.log('[smoke] /api/browser/state =', JSON.stringify(goState));

c.close();
