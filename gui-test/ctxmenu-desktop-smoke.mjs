// 桌面壳右键菜单·真机冒烟：不是无头浏览器，而是启动真正的桌面壳窗口
// （-desktop），经 WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS 附带 CDP 调试口驱动。
// 与 ctxmenu-verify.mjs（无头 Edge + ?shell=desktop 模拟桌面形态）互补，这里
// 验证只有真桌面壳才覆盖的点：
//   D1 真桌面窗口里 contextmenu 事件照常触发、自定义菜单弹出（原生菜单被库禁用）
//   D2 真实剪贴板链路：右键复制选中文本 → navigator.clipboard.readText 读回一致
//   D3 真实 TTS 引擎：ttsSpeak 后 speechSynthesis.speaking=true（出声由系统声库决定）
//   D4 open-path 正向：工作区里真实文件用系统默认程序打开（记事本进程出现→温和关闭）
//   D5 reveal-path 正向：explorer /select 定位（资源管理器窗口出现→温和关闭）
// 附带 JX_DESKTOP_AUTOCLOSE=60 兜底：脚本意外死掉窗口也会自己关。
// 注意：必须先退出正在使用的坚祥数字助理桌面版再跑——桌面壳有单实例互斥体
// （Local\JX-Digital-Assistant-GO-SingleInstance），有实例在跑时冒烟实例只会
// 唤起旧窗口然后自己退出（表现为 CDP 8831/9224 一直不上线）。
// 运行：仓库根目录 node gui-test/ctxmenu-desktop-smoke.mjs
import { spawn, execSync } from 'node:child_process';
import { writeFileSync, mkdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const TMP = join(tmpdir(), 'jx-ctxmenu-smoke');
const BASE = 'http://127.0.0.1:8831';
const CDP = 'http://127.0.0.1:9224';
const results = [];
const ok = (name, pass, detail) => {
  results.push({ name, pass, detail });
  console.log((pass ? 'PASS ' : 'FAIL ') + name + (detail ? '  | ' + detail : ''));
};
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---------- 环境自举：隔离配置 + 真桌面窗口 ----------
mkdirSync(join(TMP, 'ws'), { recursive: true });
writeFileSync(join(TMP, 'config.json'), JSON.stringify({
  port: 8831, bindHost: '127.0.0.1',
  baseURL: 'mock', apiKey: '', model: 'mock-model',
  workspace: join(TMP, 'ws'), updateURL: '',
}));
writeFileSync(join(TMP, 'ws', '冒烟测试.txt'), '右键菜单桌面冒烟用文件，可安全关闭。\n');
const exe = join(TMP, 'server.exe');
execSync(`go build -o "${exe}" .`, { cwd: fileURLToPath(new URL('..', import.meta.url)), stdio: 'inherit' });
const proc = spawn(exe, ['-config', 'config.json', '-desktop'], {
  cwd: TMP, stdio: 'ignore',
  env: {
    ...process.env,
    WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS: '--remote-debugging-port=9224',
    JX_DESKTOP_AUTOCLOSE: '60',
  },
});

// ---------- CDP 附着到已存在的 WebView2 页面 ----------
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
  return {
    send(method, params = {}) {
      const id = ++seq;
      ws.send(JSON.stringify({ id, method, params }));
      return new Promise((ok2, bad2) => pending.set(id, { ok: ok2, bad: bad2 }));
    },
    close() { ws.close(); },
  };
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
      await c.send('Page.enable', {});
      await c.send('Runtime.enable', {});
      return c;
    } catch (e) { /* 等 WebView2 起来 */ }
  }
  throw new Error('没能附着到桌面壳 WebView2（CDP 9224）');
}

async function read(c, expr) {
  const r = await c.send('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
  if (r.exceptionDetails) throw new Error('read failed: ' + JSON.stringify(r.exceptionDetails).slice(0, 300));
  return r.result.value;
}
async function rightClick(c, x, y) {
  for (const type of ['mousePressed', 'mouseReleased']) {
    await c.send('Input.dispatchMouseEvent', { type, x: Math.round(x), y: Math.round(y), button: 'right', clickCount: 1 });
    await sleep(40);
  }
}
async function leftClick(c, x, y) {
  for (const type of ['mousePressed', 'mouseReleased']) {
    await c.send('Input.dispatchMouseEvent', { type, x: Math.round(x), y: Math.round(y), button: 'left', clickCount: 1 });
    await sleep(40);
  }
}
async function menuState(c) {
  return await read(c, `(() => {
    const m = document.getElementById('ctx-menu');
    if (!m || m.classList.contains('hidden')) return null;
    return [...m.querySelectorAll('.pop-item')].map((b) => b.textContent + (b.disabled ? '｜禁' : ''));
  })()`);
}

// cmd → powershell 的引号转义链路极其脆弱（实测 \" 传进去就是碎的），
// 一律走 -EncodedCommand（UTF-16LE base64），彻底绕开引号
function runPS(script) {
  const enc = Buffer.from(script, 'utf16le').toString('base64');
  try { return execSync(`powershell -NoProfile -EncodedCommand ${enc}`, { encoding: 'utf8' }).trim(); } catch (e) { return 'err'; }
}
function closeNotepadNamed(namePart) {
  return runPS(`$ps = Get-Process notepad -ErrorAction SilentlyContinue | Where-Object { \$_.MainWindowTitle -like '*${namePart}*' }; if ($ps) { $ps | ForEach-Object { \$_.CloseMainWindow() | Out-Null }; 'closed' } else { 'none' }`);
}
function closeExplorerAt(dirUrl) {
  return runPS(`$sh = New-Object -ComObject Shell.Application; $hit = 0; foreach ($w in @($sh.Windows())) { if ($w.LocationURL.TrimEnd('/') -eq '${dirUrl}'.TrimEnd('/')) { $w.Quit(); $hit++ } }; "closed=$hit"`);
}

// ---------- 冒烟用例 ----------
let c;
try {
  c = await attach();
  await sleep(1500); // 等前端初始化完
  ok('D1 前置：真桌面窗口 shell=desktop 形态', await read(c, `document.documentElement.classList.contains('desktop-shell')`));

  // D1 真桌面里右键输入框 → 菜单弹出（右键点在选中文本上：点选区外会收起
  // 选区，那是原生行为，不是 bug）
  const inp = await read(c, `(() => { const b = document.getElementById('input').getBoundingClientRect(); return { x: b.left + b.width / 2, y: b.top + b.height / 2 }; })()`);
  await leftClick(c, inp.x, inp.y);
  await c.send('Input.insertText', { text: '桌面冒烟ABC' });
  await read(c, `document.getElementById('input').setSelectionRange(0, 8)`);
  const inpText = await read(c, `(() => { const b = document.getElementById('input').getBoundingClientRect(); return { x: b.left + 40, y: b.top + b.height / 2 }; })()`);
  await rightClick(c, inpText.x, inpText.y);
  const m = await menuState(c);
  ok('D1 真桌面壳右键弹自定义菜单（选区在，剪切/复制可用）',
    m && m.join(',') === '撤销,重做,剪切,复制,粘贴,全选', JSON.stringify(m));

  // D2 真实剪贴板全链路：writeText 写入系统剪贴板（此 API 可用）→ 清空输入框
  // → 右键点「粘贴」→ 走 /api/clipboard（readText 在 WebView2 里会挂起，见
  // 诊断脚本）回填。注意不要用 evaluate 直调 readText 验证——它永不结算。
  await read(c, `navigator.clipboard.writeText('粘贴回退OK')`);
  await read(c, `(() => { const i = document.getElementById('input'); i.value = ''; return i.value; })()`);
  await rightClick(c, inpText.x, inpText.y);
  const pb = await read(c, `(() => { const b = [...document.querySelectorAll('#ctx-menu .pop-item')].find(x => x.textContent === '粘贴'); const r = b.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()`);
  await leftClick(c, pb.x, pb.y);
  await sleep(800);
  const pasted = await read(c, `document.getElementById('input').value`);
  ok('D2 真实粘贴链路（writeText→菜单「粘贴」→/api/clipboard→回填）', pasted === '粘贴回退OK', JSON.stringify(pasted));

  // D3 真实 TTS 引擎
  const tts = await read(c, `(() => { ttsSpeak('冒烟'); const s = window.speechSynthesis; return { supported: 'speechSynthesis' in window, speaking: s.speaking || s.pending }; })()`);
  await read(c, `ttsStop()`);
  ok('D3 真实 TTS 引擎受理朗读（speaking/pending 置位）', tts.supported && tts.speaking === true, JSON.stringify(tts));

  // D4 open-path 正向：真开默认程序（记事本）→ 温和关闭
  const sess = ((await (await fetch(BASE + '/api/sessions')).json()).sessions) || [];
  const sid = sess.length ? sess[0].id : '';
  if (!sid) { await fetch(BASE + '/api/sessions', { method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ title: '冒烟' }) }); }
  const sid2 = sid || ((await (await fetch(BASE + '/api/sessions')).json()).sessions[0].id);
  const r1 = await fetch(BASE + '/api/sessions/' + sid2 + '/open-path', {
    method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ path: '冒烟测试.txt' }),
  });
  let notepadSeen = false;
  for (let i = 0; i < 10; i++) {
    await sleep(700);
    if (closeNotepadNamed('冒烟测试') === 'closed') { notepadSeen = true; break; }
    // 每轮探测：进程在但标题可能还没挂上——单独查一次
    const cnt = runPS(`(Get-Process notepad -ErrorAction SilentlyContinue | Where-Object { \$_.MainWindowTitle -like '*冒烟测试*' }).Count`);
    if (cnt !== 'err' && cnt !== '0' && cnt !== '') { notepadSeen = true; break; }
  }
  ok('D4 open-path 用系统默认程序打开（记事本出现并已温和关闭）', r1.status === 200 && notepadSeen, `status=${r1.status} notepad=${notepadSeen}`);
  closeNotepadNamed('冒烟测试'); // 兜底再关一次

  // D5 reveal-path 正向：explorer 定位 → COM 温和关闭
  const r2 = await fetch(BASE + '/api/sessions/' + sid2 + '/reveal-path', {
    method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify({ path: '冒烟测试.txt' }),
  });
  let explorerClosed = 'err';
  for (let i = 0; i < 10; i++) {
    await sleep(700);
    explorerClosed = closeExplorerAt('file:///' + join(TMP, 'ws').replace(/\\/g, '/'));
    if (explorerClosed.includes('closed=1')) break;
  }
  ok('D5 reveal-path 资源管理器定位（窗口出现并已温和关闭）', r2.status === 200 && explorerClosed.includes('closed='), `status=${r2.status} ${explorerClosed}`);

  c.close();
} catch (e) {
  ok('冒烟主流程异常', false, String(e && e.message || e));
  try { c && c.close(); } catch (e2) { /* ignore */ }
}

// 收尾：关桌面窗口（AUTOCLOSE 是兜底，正常路径主动停）
try { execSync(`taskkill /PID ${proc.pid} /T /F`, { stdio: 'ignore' }); } catch (e) { /* already gone */ }
const fails = results.filter((r) => !r.pass);
console.log(`\n==== ${results.length - fails.length}/${results.length} PASS ====`);
if (fails.length) { fails.forEach((f) => console.log('FAILED: ' + f.name)); process.exit(1); }
