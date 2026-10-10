const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const list = await (await fetch('http://127.0.0.1:9223/json')).json();
const page = list.find((t) => t.type === 'page' && t.url.includes('shell=desktop'));
const ws = new WebSocket(page.webSocketDebuggerUrl);
await new Promise((ok) => { ws.onopen = ok; });
let seq = 0; const pending = new Map();
ws.onmessage = (ev) => { const m = JSON.parse(ev.data); if (m.id && pending.has(m.id)) { const { ok } = pending.get(m.id); pending.delete(m.id); ok(m.result); } };
const send = (m, p = {}) => new Promise((ok) => { const id = ++seq; pending.set(id, { ok }); ws.send(JSON.stringify({ id, method: m, params: p })); });
const read = async (e) => (await send('Runtime.evaluate', { expression: e, returnByValue: true, awaitPromise: true })).result.value;
// components.json 里 input-counter:false 是删目录前残留的开关态——市场安装不会
// 改它；先经正门（CX.toggle）启用，这是用户在设置页会做的动作
await read(`CX.refresh().then(()=>1)`);   // 页面清单对账（市场安装后 app-market 的同款动作）
await new Promise(r => setTimeout(r, 1000));
let active = await read(`CX.isActive('input-counter')`);
console.log('refresh 后激活态:', active);
if (String(active) !== 'true') {
  await read(`CX.toggle('input-counter', true).then(()=>1)`);
  await sleep(1500);
  console.log('toggle 后激活:', await read(`CX.isActive('input-counter')`));
}
const ui = await read(`(async () => {
  const ta = document.getElementById('input');
  ta.value = '市场安装后的组件 hello'; ta.dispatchEvent(new Event('input'));
  await new Promise(r => setTimeout(r, 1200));
  const badge = document.querySelector('.ic-badge');
  const stats = await fetch('/api/c/input-counter/stats', { method: 'POST', headers: {'Content-Type':'application/json'},
    body: JSON.stringify({cmd:'stats', text: ta.value}) }).then(r => r.json());
  return { badgeText: badge ? badge.textContent : '(无)', padRight: getComputedStyle(ta).paddingRight,
           worker经桥: JSON.stringify(stats) };
})()`);
console.log('免编译运行:', JSON.stringify(ui));
ws.close();
