// CDP 驱动头less Edge 的最小工具集（测试脚本共用）：
// 全部交互走 Input.*（真实输入事件），DOM 只读走 Runtime.evaluate。
const CDP_HTTP = 'http://127.0.0.1:9223';

export async function newTab(url, width = 1280, height = 900) {
  const res = await fetch(`${CDP_HTTP}/json/new?about:blank`, { method: 'PUT' });
  const info = await res.json();
  const ws = new WebSocket(info.webSocketDebuggerUrl);
  await new Promise((ok, bad) => { ws.onopen = ok; ws.onerror = bad; });
  const client = makeClient(ws);
  await client.send('Page.enable', {});
  await client.send('Runtime.enable', {});
  await client.send('Emulation.setDeviceMetricsOverride', {
    width, height, deviceScaleFactor: 1, mobile: false,
  });
  await client.send('Page.navigate', { url });
  await client.send('Runtime.evaluate', { expression: 'document.readyState' });
  await sleep(1500);
  client.tabId = info.id;
  return client;
}

function makeClient(ws) {
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
  return {
    send(method, params = {}) {
      const id = ++seq;
      ws.send(JSON.stringify({ id, method, params }));
      return new Promise((ok, bad) => pending.set(id, { ok, bad }));
    },
    close() { ws.close(); },
  };
}

export function sleep(ms) { return new Promise((r) => setTimeout(r, ms)); }

// 只读求值：返回 JSON 化结果
export async function read(client, expr) {
  const r = await client.send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
  if (r.exceptionDetails) throw new Error('read failed: ' + JSON.stringify(r.exceptionDetails).slice(0, 300));
  return r.result.value;
}

// 真实点击（mousePressed + mouseReleased）
export async function click(client, x, y) {
  for (const type of ['mousePressed', 'mouseReleased']) {
    await client.send('Input.dispatchMouseEvent', {
      type, x: Math.round(x), y: Math.round(y), button: 'left', clickCount: 1,
    });
    await sleep(40);
  }
}

// 真实滚轮（deltaY 正 = 向下）
export async function wheel(client, x, y, deltaY, times = 1) {
  for (let i = 0; i < times; i++) {
    await client.send('Input.dispatchMouseEvent', {
      type: 'mouseWheel', x: Math.round(x), y: Math.round(y),
      deltaX: 0, deltaY,
    });
    await sleep(60);
  }
}

// 真实按键（如 Enter）
export async function key(client, vk) {
  const spec = {
    Enter: { key: 'Enter', code: 'Enter', vk: 13 },
    PageUp: { key: 'PageUp', code: 'PageUp', vk: 33 },
  }[vk];
  if (!spec) throw new Error('unknown key ' + vk);
  for (const type of ['keyDown', 'keyUp']) {
    await client.send('Input.dispatchKeyEvent', {
      type, key: spec.key, code: spec.code,
      windowsVirtualKeyCode: spec.vk, nativeVirtualKeyCode: spec.vk,
    });
    await sleep(40);
  }
}

// 输入文本（走输入法通道，等同真实键入，触发 input 事件）
export async function typeText(client, text) {
  await client.send('Input.insertText', { text });
}

// 截图存盘
export async function shot(client, path) {
  const r = await client.send('Page.captureScreenshot', { format: 'png' });
  const { writeFileSync } = await import('node:fs');
  writeFileSync(path, Buffer.from(r.data, 'base64'));
  return path;
}

// 元素中心坐标（只读几何）
export async function center(client, selector) {
  const r = await read(client, `(() => {
    const el = document.querySelector(${JSON.stringify(selector)});
    if (!el) return null;
    const b = el.getBoundingClientRect();
    return { x: b.left + b.width / 2, y: b.top + b.height / 2, w: b.width, h: b.height };
  })()`);
  if (!r) throw new Error('not found: ' + selector);
  return r;
}

// 关闭指定标签页（测试结束/失败时调用，防堆积拖慢后续 /json/new）
export async function closeTab(client) {
  if (!client || !client.tabId) return;
  try { await fetch(`${CDP_HTTP}/json/close/${client.tabId}`); } catch (e) { /* already gone */ }
}
