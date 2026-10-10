// 语音快捷键误触发回归验证（组件/voice/client.js 的快捷键引擎）
//
// 背景缺陷（2026-10-10）：点「＋ → 文件上传」却启动了麦克风听写。
// 根因＝引擎用"累计按键集合"判定组合，且只有 window blur 一条兜底清理：
//   一次没有配到 mouseup 的鼠标按下（默认主快捷键＝鼠标侧键）、或窗口未聚焦时
//   的按下（不触发但**照样记账**），会在 heldMouse 里留下 'mouse-side'；
//   此后任何一次普通鼠标点击都会被判成"快捷键按满" → toggle() 掉听写。
//
// 本脚本用真实 CDP 输入事件（Input.dispatchMouseEvent/KeyEvent，非合成事件）验证：
//   V0  页面/语音组件就绪，SR 桩可观测
//   V1a 残留 + 兜底失效（未派发 focus）：点「文件上传」不应启动听写  ← 修前必 FAIL
//   V1b 残留 + 恢复聚焦（派发 focus）：兜底清理生效，点「文件上传」不应启动听写
//   V1c 残留未被清时，按自定义组合仍应正常触发（防"修过头"）
//   V2  默认侧键：正常按下/松开仍触发（tap 开）
//   V3  再按一次：仍能 toggle 关（firedLatch 正常复位）
//   V4  副快捷键 Alt 独按松开仍触发（tap 语义不回归）
//   V5  hold 模式：按住侧键开、松开停
//
// 语音识别用页面内 SR 桩（离线、不碰真实语音服务、不弹麦克风授权）；
// 窗口聚焦状态用注入的 __setFocus/__refocus 精确模拟（无头环境无法真失焦）。
// 运行：仓库根目录  node gui-test/voice-shortcut-verify.mjs
import { newTab, read, sleep, closeTab, center } from './cdp.mjs';
import { mkdirSync, writeFileSync, rmSync, accessSync, symlinkSync } from 'node:fs';
import { spawn, execSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = fileURLToPath(new URL('..', import.meta.url));
const SCREEN = join(ROOT, 'gui-test-screenshots');
const BASE = 'http://127.0.0.1:8831';
const CDP_HTTP = 'http://127.0.0.1:9223';
const PORT = 8831;
mkdirSync(SCREEN, { recursive: true });

const results = [];
const ok = (name, pass, detail) => {
  results.push({ name, pass, detail });
  console.log((pass ? 'PASS ' : 'FAIL ') + name + (detail ? '  | ' + detail : ''));
};

// ---------- 隔离实例 + 无头 Edge（沿用 gui-test/cdp.mjs 的 9223 约定） ----------
const TMP = join(tmpdir(), 'jx-voice-sc-test');
let serverProc = null, edgeWasOurs = false, edgePid = 0;
async function up() {
  let alive = false;
  try { const r = await fetch(BASE + '/api/status'); alive = r.ok; } catch (e) { /* down */ }
  if (!alive) {
    rmSync(TMP, { recursive: true, force: true });
    mkdirSync(join(TMP, 'ws'), { recursive: true });
    // 组件目录在宿主里是相对 cwd 扫描的（components.go: InitComponents("组件", …)）：
    // 隔离实例跑在临时目录下，得把仓库的 组件/ 联接进来，否则语音组件根本不存在。
    try { symlinkSync(join(ROOT, '组件'), join(TMP, '组件'), 'junction'); } catch (e) { /* 已存在 */ }
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
    const p = spawn(edge, [
      '--headless=new', '--remote-debugging-port=9223',
      '--user-data-dir=' + join(TMP, 'edge-profile'), 'about:blank',
    ], { stdio: 'ignore' });
    edgeWasOurs = true; edgePid = p.pid;
    for (let i = 0; i < 30; i++) {
      await sleep(500);
      try { await fetch(CDP_HTTP + '/json/version'); break; } catch (e2) { /* wait */ }
    }
  }
}
function down() {
  if (serverProc) { try { execSync(`taskkill /PID ${serverProc.pid} /T /F`, { stdio: 'ignore' }); } catch (e) { /* gone */ } }
  if (edgeWasOurs && edgePid) { try { execSync(`taskkill /PID ${edgePid} /T /F`, { stdio: 'ignore' }); } catch (e) { /* gone */ } }
}

// ---------- 页面注入：SR 桩 + 可控聚焦 + file-input 桩 ----------
const INJECT = `
(() => {
  window.__voiceLog = [];
  class StubSR {
    constructor() { this._running = false; }
    start() {
      window.__voiceLog.push('start');
      this._running = true;
      setTimeout(() => { if (this._running && this.onstart) this.onstart(); }, 20);
    }
    stop() {
      if (!this._running) return;
      this._running = false;
      window.__voiceLog.push('stop');
      setTimeout(() => { if (this.onend) this.onend(); }, 20);
    }
    abort() { this.stop(); }
  }
  window.SpeechRecognition = StubSR;
  window.webkitSpeechRecognition = StubSR;
  let focused = true;
  document.hasFocus = () => focused;
  window.__setFocus = (v) => { focused = !!v; };
  window.__refocus = () => { focused = true; window.dispatchEvent(new Event('focus')); };
  document.addEventListener('DOMContentLoaded', () => {
    const fi = document.getElementById('file-input');
    if (fi) fi.click = function () { window.__voiceLog.push('file-input.click()'); };
  });
})();
`;

async function waitFor(client, expr, ms = 8000) {
  const t0 = Date.now();
  while (Date.now() - t0 < ms) {
    try { if (await read(client, expr)) return true; } catch (e) { /* 页面还在加载 */ }
    await sleep(200);
  }
  return false;
}

async function openPage() {
  const client = await newTab(BASE + '/', 1280, 900);
  await client.send('Page.addScriptToEvaluateOnNewDocument', { source: INJECT });
  await client.send('Page.reload');
  const ready = await waitFor(client, `(() => {
    const b = document.getElementById('btn-voice');
    return !!(b && !b.classList.contains('hidden') && window.__voiceLog);
  })()`);
  if (!ready) throw new Error('语音组件没就绪（#btn-voice 未出现）');
  return client;
}

async function reload(client) {
  await client.send('Page.reload');
  await waitFor(client, `(() => {
    const b = document.getElementById('btn-voice');
    return !!(b && !b.classList.contains('hidden') && window.__voiceLog);
  })()`);
}

// ---------- 真实输入事件 ----------
async function mouse(client, type, button, x, y) {
  await client.send('Input.dispatchMouseEvent', {
    type, x: Math.round(x), y: Math.round(y), button, clickCount: 1,
  });
  await sleep(60);
}
async function clickSel(client, sel) {
  const c = await center(client, sel);
  await mouse(client, 'mousePressed', 'left', c.x, c.y);
  await mouse(client, 'mouseReleased', 'left', c.x, c.y);
}
async function sideDown(client) {
  // 侧键用合成事件：CDP 真实派发 button=back 会触发浏览器"后退"导航，把测试页导航走
  // （引擎只读 e.button，合成事件足以覆盖判定逻辑；左键点击仍走真实输入）。
  await read(client, `(() => {
    document.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, cancelable: true, button: 3, buttons: 8 }));
    return 1;
  })()`);
  await sleep(60);
}
async function sideUp(client) {
  await read(client, `(() => {
    document.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, cancelable: true, button: 3, buttons: 0 }));
    return 1;
  })()`);
  await sleep(60);
}
async function keyEvent(client, type, key, code, vk, modifiers = 0) {
  await client.send('Input.dispatchKeyEvent', {
    type, key, code, windowsVirtualKeyCode: vk, nativeVirtualKeyCode: vk, modifiers,
  });
  await sleep(60);
}
const keyDown = (c, key, code, vk, mod) => keyEvent(c, 'rawKeyDown', key, code, vk, mod);
const keyUp = (c, key, code, vk, mod) => keyEvent(c, 'keyUp', key, code, vk, mod);

// 每个用例前：收尾在跑的听写 + 清掉上一次可能留下的按键残留（真实场景里松手会发 release）
// + 清空观测日志 + 回到聚焦态
async function reset(client) {
  await read(client, `(() => {
    if (window.JXVoicePause) window.JXVoicePause();
    window.__setFocus(true);
    for (const b of [0, 1, 2, 3, 4]) {
      document.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, button: b, buttons: 0 }));
    }
    for (const k of ['Control', 'Alt', 'Shift', 'Meta']) {
      document.dispatchEvent(new KeyboardEvent('keyup', { bubbles: true, key: k }));
    }
    return 1;
  })()`);
  await sleep(150);
  await read(client, 'window.__voiceLog.length = 0; 0');
}
const logOf = (client) => read(client, 'window.__voiceLog.slice()');

// ---------- 主流程 ----------
let client = null;
try {
  await up();
  client = await openPage();

  // V0 就绪
  const hasBtn = await read(client, `!!document.getElementById('btn-voice')`);
  const srIsStub = await read(client, `window.SpeechRecognition && window.SpeechRecognition.name === 'StubSR'`);
  ok('V0 语音组件就绪且 SR 为测试桩', !!hasBtn && !!srIsStub, `btn=${hasBtn} stubSR=${srIsStub}`);

  // V1a 残留 + 兜底失效：点「文件上传」不应启动听写（只可能靠边沿判定通过）
  await reset(client);
  await read(client, 'window.__setFocus(false); 0');
  await sideDown(client);                          // 窗口未聚焦时按侧键：不触发，但记账
  await read(client, 'window.__setFocus(true); 0'); // 恢复聚焦，但**不**派发 focus 事件
  await clickSel(client, '#btn-plus');
  await clickSel(client, '#pm-upload');            // 点「文件上传」
  let log = await logOf(client);
  ok('V1a 残留+兜底失效：点「文件上传」不启动听写', !log.includes('start'), JSON.stringify(log));

  // V1b 残留 + 恢复聚焦（派发 focus）：兜底清理生效
  await reset(client);
  await read(client, 'window.__setFocus(false); 0');
  await sideDown(client);
  await read(client, 'window.__refocus(); 0');      // 真实恢复聚焦 → 应清掉残留
  await clickSel(client, '#btn-plus');
  await clickSel(client, '#pm-upload');
  log = await logOf(client);
  ok('V1b 恢复聚焦清残留：点「文件上传」不启动听写', !log.includes('start'), JSON.stringify(log));

  // V1c 残留未被清时，自定义组合仍应正常触发（防修过头）——留到阶段 C 一起测

  // V2/V3 默认侧键：正常按/松仍触发，且能 toggle 关
  await reset(client);
  await sideDown(client); await sideUp(client);
  log = await logOf(client);
  ok('V2 默认侧键正常按/松仍触发听写', log.includes('start'), JSON.stringify(log));
  await read(client, 'window.__voiceLog.length = 0; 0');
  await sideDown(client); await sideUp(client);
  log = await logOf(client);
  ok('V3 再按一次仍能 toggle 关闭（闩锁正常复位）', log.includes('stop'), JSON.stringify(log));

  // V4 副快捷键 Alt 独按松开仍触发（tap 语义）
  await reset(client);
  await keyDown(client, 'Alt', 'AltLeft', 18, 1);
  await keyUp(client, 'Alt', 'AltLeft', 18, 0);
  log = await logOf(client);
  ok('V4 副快捷键 Alt 独按松开仍触发听写', log.includes('start'), JSON.stringify(log));

  // V5 hold 模式：按住侧键开、松开停
  await read(client, `localStorage.setItem('cx:voice:shortcutMode', JSON.stringify('hold')); 0`);
  await reload(client);
  await reset(client);
  await sideDown(client);
  const logHoldDown = await logOf(client);
  await sideUp(client);
  await sleep(120);
  const logHoldUp = await logOf(client);
  ok('V5 hold 模式：按住侧键开、松开停',
    logHoldDown.includes('start') && logHoldUp.includes('stop'),
    'down=' + JSON.stringify(logHoldDown) + ' up=' + JSON.stringify(logHoldUp));

  // 阶段 C：自定义组合 ctrl+F9（tap）
  await read(client, `(() => {
    localStorage.setItem('cx:voice:shortcutMode', JSON.stringify('tap'));
    localStorage.setItem('cx:voice:scMain', JSON.stringify(['ctrl', 'f9']));
    localStorage.setItem('cx:voice:scSub', JSON.stringify([]));
  })()`);
  await reload(client);

  // V6 组合键正常触发（ctrl 单按不触发，f9 补齐才触发）
  await reset(client);
  await keyDown(client, 'Control', 'ControlLeft', 17, 2);
  const logCtrlOnly = await logOf(client);
  await keyDown(client, 'F9', 'F9', 120, 2 | 2);
  const logWithF9 = await logOf(client);
  await keyUp(client, 'F9', 'F9', 120, 2);
  await keyUp(client, 'Control', 'ControlLeft', 17, 0);
  ok('V6 自定义组合 ctrl+F9 正常触发（且 ctrl 单按不触发）',
    !logCtrlOnly.includes('start') && logWithF9.includes('start'),
    'ctrlOnly=' + JSON.stringify(logCtrlOnly) + ' withF9=' + JSON.stringify(logWithF9));

  // V1c 残留（未清）+ 自定义组合：仍应触发（边沿判定不能误伤正常快捷键）
  await reset(client);
  await read(client, 'window.__setFocus(false); 0');
  await sideDown(client);                           // 残留 mouse-side
  await read(client, 'window.__setFocus(true); 0'); // 不派发 focus：残留保留
  await clickSel(client, '#input');                 // 普通点击：不应触发
  const logClickOnly = await logOf(client);
  await keyDown(client, 'Control', 'ControlLeft', 17, 2);
  await keyDown(client, 'F9', 'F9', 120, 2);
  const logCombo = await logOf(client);
  await keyUp(client, 'F9', 'F9', 120, 2);
  await keyUp(client, 'Control', 'ControlLeft', 17, 0);
  ok('V1c 残留下：普通点击不触发、自定义组合仍触发',
    !logClickOnly.includes('start') && logCombo.includes('start'),
    'click=' + JSON.stringify(logClickOnly) + ' combo=' + JSON.stringify(logCombo));

  // 清掉测试改动，避免污染后续手工验证
  await read(client, `(() => {
    localStorage.removeItem('cx:voice:scMain');
    localStorage.removeItem('cx:voice:scSub');
    localStorage.removeItem('cx:voice:shortcutMode');
  })()`);
} catch (e) {
  ok('脚本执行', false, String(e && e.stack || e).slice(0, 400));
} finally {
  const pass = results.filter((r) => r.pass).length;
  console.log(`\n==== 语音快捷键验证：${pass}/${results.length} 通过 ====`);
  try { writeFileSync(join(SCREEN, 'voice-shortcut-result.json'), JSON.stringify(results, null, 2)); } catch (e) { /* 忽略 */ }
  if (client) await closeTab(client);
  down();
  if (pass !== results.length) process.exitCode = 1;
}
