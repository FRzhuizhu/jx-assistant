// 桌面壳右键菜单验证（app-ctxmenu.js）：自包含——需要时自动构建隔离实例（mock
// 模型、独立 data/工作区）并拉起无头 Edge（CDP 9223，沿用 cdp.mjs 约定）。
// 会真实调起系统程序的端点（open-url / open-path / reveal-path / delivery 的
// open·reveal）在页内被 fetch 桩短路成假 200——只断言请求形状，不在测试机上
// 乱开浏览器/记事本；这些端点的服务端校验分支由脚本尾部的直连检查覆盖。
// 断言与截图落 gui-test-screenshots/ctxmenu-*。用例：
//   环境钩子：alert/confirm 记录、fetch 记录+短路、剪贴板桩、TTS 桩
//   C1  ?shell=desktop 生效（html.desktop-shell）
//   C0  mock 轮就绪（assistant 消息 + 交付卡）
//   C2  输入框菜单：条目序列 + 无选区时剪切/复制置灰
//   C3  有选区后剪切/复制可用；「复制」入剪贴板桩
//   C4  粘贴插入（execCommand 保撤销栈）→ 撤销回退 → 重做恢复
//   C5  全选
//   C6  对话区选区菜单：条目 + 「引用到输入框」插入
//   C7  气泡菜单：复制全文/朗读；朗读进 TTS 桩；朗读中变「停止朗读」
//   C8  链接菜单：条目 + 「在系统浏览器中打开」请求形状
//   C9  文件路径链接菜单：条目 + open-path/reveal-path 请求形状 + 复制路径
//   C10 交付卡行菜单：条目 + 「复制路径」入剪贴板桩
//   C11 空白处右键不弹
//   C12 左栏会话项右键仍是原会话菜单（全局监听让路）
//   C13 网页模式（无 shell=desktop）右键不弹自定义菜单
//   C14 服务端校验：open-url 非 http(s)/缺参 400；open-path 不存在/越界 404
// 运行：仓库根目录 node gui-test/ctxmenu-verify.mjs
import { newTab, read, click, typeText, shot, center, sleep, closeTab }
  from './cdp.mjs';
import { mkdirSync, writeFileSync, rmSync, accessSync } from 'node:fs';
import { spawn, execSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = fileURLToPath(new URL('..', import.meta.url));
const SCREEN = join(ROOT, 'gui-test-screenshots');
const BASE = 'http://127.0.0.1:8830';
const CDP_HTTP = 'http://127.0.0.1:9223';
const PORT = 8830;
mkdirSync(SCREEN, { recursive: true });
const results = [];
const ok = (name, pass, detail) => {
  results.push({ name, pass, detail });
  console.log((pass ? 'PASS ' : 'FAIL ') + name + (detail ? '  | ' + detail : ''));
};

// ---------- 环境自举：隔离实例 + 无头 Edge ----------
const TMP = join(tmpdir(), 'jx-ctxmenu-test');
let serverProc = null, edgeWasOurs = false, edgePid = 0;
async function up() {
  let alive = false;
  try { const r = await fetch(BASE + '/api/status'); alive = r.ok; } catch (e) { /* down */ }
  if (!alive) {
    rmSync(TMP, { recursive: true, force: true });
    mkdirSync(join(TMP, 'ws'), { recursive: true });
    const exe = join(TMP, 'server.exe');
    execSync(`go build -o "${exe}" .`, { cwd: ROOT, stdio: 'inherit' });
    writeFileSync(join(TMP, 'config.json'), JSON.stringify({
      port: PORT, bindHost: '127.0.0.1',
      baseURL: 'mock', apiKey: '', model: 'mock-model',
      workspace: join(TMP, 'ws'), updateURL: '',
    }));
    serverProc = spawn(exe, ['-config', 'config.json', '-noopen'], { cwd: TMP, stdio: 'ignore' });
    for (let i = 0; i < 40; i++) {
      await sleep(500);
      try { const r = await fetch(BASE + '/api/status'); if (r.ok) { alive = true; break; } } catch (e) { /* wait */ }
    }
  }
  if (!alive) throw new Error('隔离实例没起来（端口 ' + PORT + '）');
  try { await fetch(CDP_HTTP + '/json/version'); } catch (e) {
    const cands = [
      'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',
      'C:\\Program Files\\Microsoft\\Edge\\Application\\msedge.exe',
    ];
    const edge = cands.find((p) => { try { accessSync(p); return true; } catch (e2) { return false; } });
    if (!edge) throw new Error('没找到 msedge.exe');
    const edgeProc = spawn(edge, [
      '--headless=new', '--remote-debugging-port=9223',
      '--user-data-dir=' + join(TMP, 'edge-profile'), 'about:blank',
    ], { stdio: 'ignore' });
    edgeWasOurs = true; edgePid = edgeProc.pid;
    for (let i = 0; i < 30; i++) {
      await sleep(500);
      try { await fetch(CDP_HTTP + '/json/version'); break; } catch (e2) { /* wait */ }
    }
  }
}
function down() {
  if (serverProc) { try { execSync(`taskkill /PID ${serverProc.pid} /T /F`, { stdio: 'ignore' }); } catch (e) { /* already gone */ } }
  if (edgeWasOurs && edgePid) { try { execSync(`taskkill /PID ${edgePid} /T /F`, { stdio: 'ignore' }); } catch (e) { /* already gone */ } }
}

// ---------- 页内工具 ----------
async function rightClick(client, x, y) {
  for (const type of ['mousePressed', 'mouseReleased']) {
    await client.send('Input.dispatchMouseEvent', {
      type, x: Math.round(x), y: Math.round(y), button: 'right', clickCount: 1,
    });
    await sleep(40);
  }
}
async function rectOf(client, expr) {
  const b = await read(client, `(() => { const el = ${expr}; if (!el) return null; const r = el.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()`);
  if (!b) throw new Error('not found: ' + expr);
  return b;
}
async function menuState(client) {
  return await read(client, `(() => {
    const m = document.getElementById('ctx-menu');
    if (!m || m.classList.contains('hidden')) return null;
    return [...m.querySelectorAll('.pop-item')].map((b) => b.textContent + (b.disabled ? '｜禁' : ''));
  })()`);
}
async function clickMenu(client, label) {
  return await read(client, `(() => {
    const b = [...document.querySelectorAll('#ctx-menu .pop-item')].find((x) => x.textContent === ${JSON.stringify(label)});
    if (b && !b.disabled) { b.click(); return true; }
    return false;
  })()`);
}
async function installHooks(client) {
  await read(client, `(() => {
    window.__alerts = []; window.__fetchLog = []; window.__clip = []; window.__tts = [];
    window.onerror = (m) => { window.__errs = window.__errs || []; window.__errs.push(String(m)); };
    window.alert = (m) => { window.__alerts.push(String(m)); };
    window.confirm = () => true;
    // 会真实调起系统程序的端点：短路成假 200（只验请求形状，不真开窗口）
    const SHORT = ['/api/open-url', '/open-path', '/reveal-path', '/delivery/open', '/delivery/reveal'];
    const of = window.fetch;
    window.fetch = async (u, opt) => {
      const us = String(u);
      const rec = { url: us, method: (opt && opt.method) || 'GET', body: (opt && opt.body) || null, stub: false };
      window.__fetchLog.push(rec);
      if (us.includes('/api/clipboard')) { // 桌面壳粘贴走该端点：桩按 __clipText 应答（真机链路由冒烟脚本验证）
        rec.stub = true; return new Response(JSON.stringify({ text: window.__clipText || '' }), { status: 200 });
      }
      if (SHORT.some((s) => us.includes(s))) { rec.stub = true; return new Response('{"ok":"1"}', { status: 200 }); }
      return await of(u, opt);
    };
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: {
      writeText: (t) => { window.__clip.push(String(t)); return Promise.resolve(); },
      readText: () => Promise.resolve(window.__clipText || ''),
      read: () => Promise.resolve([]),
    } });
    try {
      Object.defineProperty(window, 'speechSynthesis', { configurable: true, value: {
        speaking: false, pending: false, onvoiceschanged: null,
        getVoices: () => [{ lang: 'zh-CN', name: 'stub-zh', default: true }],
        cancel() { window.__tts.push('cancel'); this.speaking = false; this.pending = false; },
        speak(u) { window.__tts.push('speak:' + String(u && u.text || '').slice(0, 24)); this.speaking = true; },
      } });
    } catch (e) { /* 已有对象不可覆盖：真实 TTS 也无碍断言 */ }
    return 1;
  })()`);
}
async function mkChat(t) {
  const c = await center(t, '#input');
  await click(t, c.x, c.y);
  await typeText(t, '右键菜单验证：跑一轮 mock 建立消息与交付卡');
  const b = await center(t, '#btn-send');
  await click(t, b.x, b.y);
  for (let i = 0; i < 40; i++) {
    await sleep(500);
    const s = await read(t, `({ a: document.querySelectorAll('.msg.assistant').length, d: document.querySelectorAll('.dc-file').length })`);
    if (s.a > 0 && s.d > 0) { await sleep(800); return s; }
  }
  throw new Error('mock 轮没跑完（等 assistant 消息 + 交付卡超时）');
}

// ---------- 用例 ----------
async function run() {
  await up();
  const t = await newTab(BASE + '/?shell=desktop', 1280, 900);
  try {
    await sleep(1200);
    ok('C1 ?shell=desktop → html.desktop-shell',
      await read(t, `document.documentElement.classList.contains('desktop-shell')`));
    await installHooks(t);
    const chat = await mkChat(t);
    ok('C0 mock 轮就绪（assistant 消息 + 交付卡）', chat.a > 0 && chat.d > 0, JSON.stringify(chat));

    // C2 输入框菜单：无选区 → 剪切/复制置灰
    let p = await center(t, '#input');
    await click(t, p.x, p.y);
    await typeText(t, 'ABC');
    await rightClick(t, p.x, p.y);
    await shot(t, join(SCREEN, 'ctxmenu-input.png'));
    let m = await menuState(t);
    ok('C2 输入框菜单条目（无选区：剪切/复制置灰）',
      m && m.join(',') === '撤销,重做,剪切｜禁,复制｜禁,粘贴,全选', JSON.stringify(m));

    // C3 有选区 → 剪切/复制可用；复制入剪贴板桩
    await read(t, `document.getElementById('input').setSelectionRange(0, 3)`);
    await rightClick(t, p.x, p.y);
    m = await menuState(t);
    ok('C3 有选区后剪切/复制可用', m && m.includes('剪切') && m.includes('复制'), JSON.stringify(m));
    ok('C3 点「复制」入剪贴板', await clickMenu(t, '复制') && await read(t, `window.__clip[window.__clip.length-1]`) === 'ABC',
      JSON.stringify(await read(t, 'window.__clip')));

    // C4 粘贴（桩文本 XYZ；光标收到末尾——有选区时粘贴替换选区是原生行为）
    await read(t, `document.getElementById('input').setSelectionRange(3, 3); window.__clipText = 'XYZ'`);
    await rightClick(t, p.x, p.y);
    await clickMenu(t, '粘贴');
    let v = await read(t, `document.getElementById('input').value`);
    await rightClick(t, p.x, p.y);
    await clickMenu(t, '撤销');
    let vUndo = await read(t, `document.getElementById('input').value`);
    await rightClick(t, p.x, p.y);
    await clickMenu(t, '重做');
    let vRedo = await read(t, `document.getElementById('input').value`);
    ok('C4 粘贴插入且撤销栈保留（粘贴→撤销→重做）',
      v === 'ABCXYZ' && vUndo === 'ABC' && vRedo === 'ABCXYZ', `${v} → ${vUndo} → ${vRedo}`);

    // C5 全选
    await read(t, `document.getElementById('input').setSelectionRange(1, 2)`);
    await rightClick(t, p.x, p.y);
    await clickMenu(t, '全选');
    const sel = await read(t, `(() => { const i = document.getElementById('input'); return [i.selectionStart, i.selectionEnd, i.value.length]; })()`);
    ok('C5 全选覆盖整段输入', sel[0] === 0 && sel[1] === sel[2], JSON.stringify(sel));
    await read(t, `document.getElementById('input').value = ''; window.__clip = [];`);

    // C6 对话区选区菜单
    await read(t, `(() => {
      const el = document.querySelector('.msg.assistant .md');
      const r = document.createRange(); r.selectNodeContents(el);
      const s = window.getSelection(); s.removeAllRanges(); s.addRange(r);
      return s.toString().length;
    })()`);
    const am = await rectOf(t, `document.querySelector('.msg.assistant .md')`);
    await rightClick(t, am.x, am.y);
    await shot(t, join(SCREEN, 'ctxmenu-selection.png'));
    m = await menuState(t);
    ok('C6 选区菜单条目', m && m.join(',') === '复制,引用到输入框,问AI,朗读', JSON.stringify(m));
    await clickMenu(t, '引用到输入框');
    ok('C6 「引用到输入框」插入正文', await read(t, `document.getElementById('input').value.length > 0`) === true);
    await read(t, `document.getElementById('input').value = ''`);

    // C7 气泡菜单（收掉选区）：复制全文 / 朗读 → TTS 桩 → 朗读中变「停止朗读」
    await read(t, `window.getSelection().removeAllRanges()`);
    await rightClick(t, am.x, am.y);
    m = await menuState(t);
    ok('C7 气泡菜单条目', m && m.join(',') === '复制全文,朗读', JSON.stringify(m));
    await clickMenu(t, '朗读');
    const spoke = await read(t, `window.__tts`);
    await rightClick(t, am.x, am.y);
    m = await menuState(t);
    ok('C7 朗读进 TTS 桩且朗读中变「停止朗读」',
      spoke.length > 0 && String(spoke[spoke.length - 1]).startsWith('speak:') && m && m.includes('停止朗读'),
      JSON.stringify(spoke) + ' | ' + JSON.stringify(m));
    await clickMenu(t, '停止朗读');
    await read(t, `closePopovers()`);

    // C8 链接菜单
    await read(t, `(() => {
      const host = document.querySelector('.msg.assistant .md');
      const a = document.createElement('a');
      a.className = 'ext-link'; a.href = 'https://example.com/ctxmenu'; a.textContent = '测试链接';
      host.appendChild(a); return 1;
    })()`);
    const lp = await rectOf(t, `document.querySelector('a.ext-link')`);
    await rightClick(t, lp.x, lp.y);
    await shot(t, join(SCREEN, 'ctxmenu-link.png'));
    m = await menuState(t);
    ok('C8 链接菜单条目', m && m.join(',') === '打开,在系统浏览器中打开,复制链接', JSON.stringify(m));
    await read(t, `window.__fetchLog = []`);
    await clickMenu(t, '在系统浏览器中打开');
    let fl = await read(t, `window.__fetchLog`);
    ok('C8 「在系统浏览器中打开」请求形状（POST /api/open-url + 原 url）',
      fl.length === 1 && fl[0].method === 'POST' && fl[0].url.endsWith('/api/open-url') &&
      JSON.parse(fl[0].body).url === 'https://example.com/ctxmenu', JSON.stringify(fl));

    // C9 文件路径链接菜单
    await read(t, `(() => {
      const host = document.querySelector('.msg.assistant .md');
      const a = document.createElement('a');
      a.className = 'file-link'; a.setAttribute('data-path', 'mock-演示.txt'); a.textContent = 'mock-演示.txt';
      host.appendChild(a); return 1;
    })()`);
    const fp = await rectOf(t, `document.querySelector('a.file-link')`);
    await rightClick(t, fp.x, fp.y);
    await shot(t, join(SCREEN, 'ctxmenu-filelink.png'));
    m = await menuState(t);
    ok('C9 文件路径链接菜单条目',
      m && m.join(',') === '预览（右栏）,在系统中打开,在文件夹中定位,复制路径', JSON.stringify(m));
    await read(t, `window.__fetchLog = []`);
    await clickMenu(t, '在系统中打开');
    await rightClick(t, fp.x, fp.y);
    await clickMenu(t, '在文件夹中定位');
    fl = await read(t, `window.__fetchLog`);
    ok('C9 open-path / reveal-path 请求形状',
      fl.length === 2 && fl.every((x) => x.method === 'POST') &&
      fl[0].url.includes('/open-path') && fl[1].url.includes('/reveal-path') &&
      JSON.parse(fl[0].body).path === 'mock-演示.txt' && fl[1].url.includes('/sessions/'), JSON.stringify(fl));
    await read(t, `window.__clip = []`);
    await rightClick(t, fp.x, fp.y);
    await clickMenu(t, '复制路径');
    ok('C9 「复制路径」入剪贴板', (await read(t, `window.__clip`))[0] === 'mock-演示.txt');

    // C10 交付卡行菜单
    const dp = await rectOf(t, `document.querySelector('.dc-file')`);
    await rightClick(t, dp.x, dp.y);
    await shot(t, join(SCREEN, 'ctxmenu-delivery.png'));
    m = await menuState(t);
    ok('C10 交付卡行菜单条目',
      m && m.join(',') === '打开,在文件夹中查看,预览（右栏）,复制路径', JSON.stringify(m));
    await read(t, `window.__clip = []`);
    await rightClick(t, dp.x, dp.y);
    await clickMenu(t, '复制路径');
    const clipD = await read(t, `window.__clip`);
    ok('C10 交付「复制路径」入剪贴板（带 data-path）', clipD.length === 1 && clipD[0].includes('mock-演示'), JSON.stringify(clipD));

    // C11 空白处右键不弹（页内找一个不属于任何上下文的点）
    const spot = await read(t, `(() => {
      const box = document.getElementById('messages').getBoundingClientRect();
      for (const [dx, dy] of [[5, 30], [5, 90], [-5, 150], [60, 10]]) {
        const x = box.left + (dx < 0 ? box.width + dx : dx);
        const y = box.top + (dy < 0 ? box.height + dy : dy);
        const el = document.elementFromPoint(x, y);
        if (!el) continue;
        if (el.closest('.msg, textarea, input, a, .dc-file, .tc-imgs, [contenteditable]')) continue;
        return { x, y };
      }
      return null;
    })()`);
    await read(t, `closePopovers()`);
    if (spot) {
      await rightClick(t, spot.x, spot.y);
      ok('C11 空白处右键不弹菜单', (await menuState(t)) === null, JSON.stringify(spot));
    } else {
      ok('C11 空白处右键不弹菜单', false, '没找到空白点');
    }

    // C12 左栏会话项右键仍是原会话菜单（含「重命名」；全局监听让路）
    const sp = await rectOf(t, `document.querySelector('.session-item')`);
    await rightClick(t, sp.x, sp.y);
    m = await menuState(t);
    ok('C12 会话项右键 = 原会话菜单（重命名等，未被全局菜单劫持）',
      m && m.includes('重命名') && !m.includes('粘贴'), JSON.stringify(m));
    await read(t, `closePopovers()`);

    // C14 服务端校验分支（直连真实服务；全部走拒绝/404 路，不会真开系统程序）
    const sess = ((await (await fetch(BASE + '/api/sessions')).json()).sessions) || [];
    const sid = Array.isArray(sess) && sess.length ? (sess[0].id || sess[0].ID) : '';
    const post = async (path, body) => (await fetch(BASE + path, {
      method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(body),
    })).status;
    const s1 = await post('/api/open-url', { url: 'file:///c:/windows' });
    const s2 = await post('/api/open-url', {});
    const s3 = await post('/api/sessions/' + sid + '/open-path', { path: '不存在的文件.txt' });
    const s4 = await post('/api/sessions/' + sid + '/reveal-path', { path: '../../' });
    ok('C14 服务端校验：open-url 非法 scheme/缺参 400，open-path 不存在/越界 404',
      s1 === 400 && s2 === 400 && s3 === 404 && s4 === 404, `${s1} ${s2} ${s3} ${s4} sid=${sid}`);

    await closeTab(t);
  } catch (e) {
    ok('主流程异常', false, String(e && e.message || e));
    try { await shot(t, join(SCREEN, 'ctxmenu-error.png')); } catch (e2) { /* tab 可能已关 */ }
    await closeTab(t);
  }

  // C13 网页模式：无 shell=desktop → 不注册自定义右键
  try {
    const w = await newTab(BASE + '/', 1280, 900);
    await sleep(1000);
    await installHooks(w);
    const p2 = await center(w, '#input');
    await rightClick(w, p2.x, p2.y);
    ok('C13 网页模式右键不弹自定义菜单（保留浏览器原生）', (await menuState(w)) === null);
    await closeTab(w);
  } catch (e) {
    ok('C13 异常', false, String(e && e.message || e));
  }
}

try { await run(); } catch (e) { ok('环境异常', false, String(e && e.message || e)); }
down();
const fails = results.filter((r) => !r.pass);
console.log(`\n==== ${results.length - fails.length}/${results.length} PASS ====`);
if (fails.length) { fails.forEach((f) => console.log('FAILED: ' + f.name)); process.exit(1); }
