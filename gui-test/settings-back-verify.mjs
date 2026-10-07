// 手机端设置页返回链路验证（窄屏 390×844，CDP 真实点击）。
// 背景：手机二级设置页返回键曾「没反应/带出应用回宿主连接页」——根因是返回
// 逻辑完全依赖 history/popstate 且按虚高的记账层数 go(-d)。修复后契约：
//   返回键 UI 直切优先（二级→一级必生效），history 只做栈同步；只退实际压入
//   的层数；pushState 失败降级纯 UI 导航，绝不裸 back() 跳出应用。
// 用例：
//   S1 LAN 直连·真机序列：汉堡开左栏→齿轮进设置→点二级分区→返回键回一级→
//      再按关设置→全程 URL 不变
//   S2 返回手势链：openSettings→二级→history.back() 回一级→再 back 关设置
//   S3 敌对历史环境：patch pushState 抛异常→开设置/进二级/返回键全链路仍正确
//      且 URL 不变（降级为纯 UI 导航）
//   S4 直达二级入口（openSettings('models')）→返回键回一级
// 运行：仓库根目录 node gui-test/settings-back-verify.mjs
import { newTab, read, sleep, closeTab } from './cdp.mjs';
import { spawn, execSync } from 'node:child_process';
import { writeFileSync, mkdirSync, rmSync, accessSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const BASE = 'http://127.0.0.1:8833';
const CDP_HTTP = 'http://127.0.0.1:9223';
const ROOT = fileURLToPath(new URL('..', import.meta.url));
const TMP = join(tmpdir(), 'jx-setback-' + Date.now().toString(36));
const results = [];
const ok = (name, pass, detail) => {
  results.push({ name, pass, detail });
  console.log((pass ? 'PASS ' : 'FAIL ') + name + (detail ? '  | ' + detail : ''));
};

async function ensureEdge() {
  try { await fetch(CDP_HTTP + '/json/version'); return; } catch (e) { /* spawn */ }
  const cands = [
    'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',
    'C:\\Program Files\\Microsoft\\Edge\\Application\\msedge.exe',
  ];
  const edge = cands.find((p) => { try { accessSync(p); return true; } catch (e2) { return false; } });
  if (!edge) throw new Error('没找到 msedge.exe');
  const proc = spawn(edge, ['--headless=new', '--remote-debugging-port=9223',
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
    port: 8833, bindHost: '127.0.0.1', baseURL: 'mock', apiKey: '', model: 'mock-model',
    workspace: join(TMP, 'ws'), updateURL: '',
  }));
  serverProc = spawn(exe, ['-config', 'config.json', '-noopen'], { cwd: TMP, stdio: 'ignore' });
  for (let i = 0; i < 40; i++) {
    await sleep(500);
    try { const r = await fetch(BASE + '/api/status'); if (r.ok) return; } catch (e) { /* wait */ }
  }
  throw new Error('server 没起来');
}

async function tread(t, expr, ms = 6000) {
  return await Promise.race([read(t, expr), new Promise((r) => setTimeout(() => r(undefined), ms))]);
}
async function clickEl(t, sel) {
  const c = await tread(t, `(() => { const el = document.querySelector(${JSON.stringify(sel)}); if (!el) return null; const b = el.getBoundingClientRect(); return { x: b.left + b.width / 2, y: b.top + b.height / 2 }; })()`);
  if (!c) throw new Error('not found: ' + sel);
  for (const type of ['mousePressed', 'mouseReleased']) {
    await t.send('Input.dispatchMouseEvent', { type, x: Math.round(c.x), y: Math.round(c.y), button: 'left', clickCount: 1 });
    await sleep(40);
  }
}
const st = `(() => ({
  open: !document.getElementById('settings').classList.contains('hidden'),
  l2: !!document.querySelector('.settings-card') && document.querySelector('.settings-card').classList.contains('pane-open'),
  url: location.href,
  pushed: typeof setHistPushed === 'number' ? setHistPushed : -1,
}))()`;

// ---------- S1 LAN 直连·真机序列 ----------
async function s1() {
  const t = await newTab(BASE + '/', 390, 844);
  try {
    await sleep(1000);
    const url0 = await tread(t, 'location.href');
    await clickEl(t, '#btn-sidebar'); // 汉堡：开左栏浮层
    await sleep(200);
    const sb = await tread(t, `document.body.classList.contains('sb-show')`);
    await clickEl(t, '#btn-settings'); // 齿轮：进设置（会收起左栏）
    await sleep(700);
    let s = await tread(t, st);
    ok('S1 汉堡→齿轮：设置打开、停一级', sb === true && s.open === true && s.l2 === false, JSON.stringify({ sb, ...s }));
    await clickEl(t, '.set-nav-item'); // 点二级分区
    await sleep(300);
    s = await tread(t, st);
    ok('S1 点分区：进二级', s.l2 === true && s.open === true, JSON.stringify(s));
    await clickEl(t, '#set-back'); // ★ 返回键：二级 → 一级
    await sleep(500);
    s = await tread(t, st);
    ok('S1 返回键：二级回一级（设置仍在）', s.open === true && s.l2 === false, JSON.stringify(s));
    await clickEl(t, '#set-back'); // 再按：退出设置
    await sleep(500);
    s = await tread(t, st);
    ok('S1 再按返回键：退出设置回对话区、全程不跳页', s.open === false && s.url === url0, JSON.stringify(s));
  } finally { await closeTab(t); }
}

// ---------- S2 返回手势链 ----------
async function s2() {
  const t = await newTab(BASE + '/', 390, 844);
  try {
    await sleep(1000);
    await tread(t, `openSettings()`);
    await sleep(500);
    await tread(t, `document.querySelector('.set-nav-item').click()`);
    await sleep(300);
    await tread(t, `history.back()`); // 系统返回手势
    await sleep(500);
    let s = await tread(t, st);
    ok('S2 手势：二级退一级', s.open === true && s.l2 === false, JSON.stringify(s));
    await tread(t, `history.back()`);
    await sleep(500);
    s = await tread(t, st);
    ok('S2 手势：一级退＝关设置', s.open === false, JSON.stringify(s));
  } finally { await closeTab(t); }
}

// ---------- S3 敌对历史环境（pushState 抛异常） ----------
async function s3() {
  const t = await newTab(BASE + '/', 390, 844);
  try {
    await sleep(1000);
    const url0 = await tread(t, 'location.href');
    await tread(t, `(() => { // 模拟远程外壳一类宿主：pushState 不可用
      history.pushState = function () { throw new Error('simulated-no-pushstate'); };
      return 1;
    })()`);
    await tread(t, `openSettings()`); // 修复前：异常沿调用链炸出，UI 状态不一致
    await sleep(700);
    let s = await tread(t, st);
    ok('S3 pushState 不可用：设置照常打开、停一级', s.open === true && s.l2 === false && s.pushed === 0, JSON.stringify(s));
    await tread(t, `document.querySelector('.set-nav-item').click()`);
    await sleep(300);
    s = await tread(t, st);
    ok('S3 点分区：进二级（纯 UI 导航）', s.l2 === true && s.open === true, JSON.stringify(s));
    await tread(t, `$('set-back').click()`); // ★ 修复前这里裸 back() 会跳出应用
    await sleep(600);
    s = await tread(t, st);
    ok('S3 返回键：二级回一级、URL 不变（未被带出应用）', s.open === true && s.l2 === false && s.url === url0, JSON.stringify(s));
    await tread(t, `$('set-back').click()`);
    await sleep(600);
    s = await tread(t, st);
    ok('S3 再按返回键：退出设置、URL 仍不变', s.open === false && s.url === url0, JSON.stringify(s));
  } finally { await closeTab(t); }
}

// ---------- S4 直达二级入口 ----------
async function s4() {
  const t = await newTab(BASE + '/', 390, 844);
  try {
    await sleep(1000);
    await tread(t, `openSettings('models')`);
    await sleep(700);
    let s = await tread(t, st);
    ok('S4 直达分区：直接落二级', s.open === true && s.l2 === true, JSON.stringify(s));
    await tread(t, `$('set-back').click()`);
    await sleep(500);
    s = await tread(t, st);
    ok('S4 返回键：二级回一级', s.open === true && s.l2 === false, JSON.stringify(s));
  } finally { await closeTab(t); }
}

await ensureEdge();
await ensureServer();
await s1(); await s2(); await s3(); await s4();
try { if (serverProc) execSync(`taskkill /PID ${serverProc.pid} /T /F`, { stdio: 'ignore' }); } catch (e) { /* gone */ }
const fails = results.filter((r) => !r.pass);
console.log(`\n==== ${results.length - fails.length}/${results.length} PASS ====`);
if (fails.length) { fails.forEach((f) => console.log('FAILED: ' + f.name)); process.exit(1); }
