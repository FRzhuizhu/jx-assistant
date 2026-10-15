const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const list = await (await fetch('http://127.0.0.1:9223/json').catch(() => null)).json().catch(() => null);
if (!list) { console.log('CDP 未就绪'); process.exit(1); }
const page = list.find((t) => t.type === 'page' && t.url.includes('shell=desktop'));
const ws = new WebSocket(page.webSocketDebuggerUrl);
await new Promise((ok) => { ws.onopen = ok; });
let seq = 0; const pending = new Map();
ws.onmessage = (ev) => { const m = JSON.parse(ev.data); if (m.id && pending.has(m.id)) { const { ok } = pending.get(m.id); pending.delete(m.id); ok(m.result); } };
const send = (m, p = {}) => new Promise((ok) => { const id = ++seq; pending.set(id, { ok }); ws.send(JSON.stringify({ id, method: m, params: p })); });
const read = async (e) => (await send('Runtime.evaluate', { expression: e, returnByValue: true })).result.value;
await sleep(1500);
await read(`document.getElementById('btn-voice').click(), 'go'`);
await sleep(2500);
const mid = await read(`(() => { const b = document.getElementById('btn-voice');
  return { on: b.classList.contains('on'), bars: !!b.querySelector('.voice-bars'),
           barsCount: b.querySelectorAll('.voice-bars i').length,
           svgHidden: getComputedStyle(b.querySelector('svg')).display === 'none',
           h: b.querySelector('.voice-bars i') ? b.querySelector('.voice-bars i').style.height : '' }; })()`);
await read(`document.getElementById('btn-voice').click(), 'stop'`);
await sleep(1500);
const st = await read(`(() => { const b = document.getElementById('btn-voice');
  return { on: b.classList.contains('on'), barsGone: !b.querySelector('.voice-bars'),
           svgBack: getComputedStyle(b.querySelector('svg')).display !== 'none',
           barsClass: b.classList.contains('bars-on') }; })()`);
console.log('录音中:', JSON.stringify(mid));
console.log('停止后:', JSON.stringify(st));
ws.close();
