// 模型菜单思考档位二级浮窗验证（web/app-chat.js renderModelPop + syncModelSubPop）：
// 自包含——需要时自动构建隔离实例（mock 模型、独立 data/工作区，落 .jxai/tmp）
// 并拉起无头 Edge（CDP 9223，沿用 cdp.mjs 约定）。断言与截图落
// gui-test-screenshots/modelsub-*。用例：
//   A 打开模型菜单：面板可见、无内嵌档位残留（不再有 .mp-levels/.mp-lv 在主列表里）
//   B 点带档位的模型行：二级浮窗 #model-sub-pop 显示、贴该行右侧、垂直对齐、
//     行标 exp、内容=「默认（跟随服务）」+ 该模型档位
//   C 点档位「思考·低」：双面板都收起、芯片文案带「思考·低」
//   D 重新打开菜单：上次展开状态保留，二级浮窗自动弹出且 .on=思考·低；
//     再点同一行 = 收起（toggle）
//   E 窄视口（420px）：二级浮窗仍完整落在视口内（右侧放不下自动翻左侧）
// 运行：仓库根目录 node gui-test/model-subpop-verify.mjs
import { newTab, read, click, shot, center, sleep, closeTab } from './cdp.mjs';
import { mkdirSync, writeFileSync, rmSync, accessSync } from 'node:fs';
import { spawn, execSync } from 'node:child_process';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = fileURLToPath(new URL('..', import.meta.url));
const SCREEN = join(ROOT, 'gui-test-screenshots');
const BASE = 'http://127.0.0.1:8831';
const CDP_HTTP = 'http://127.0.0.1:9223';
const PORT = 8831;
const TMP = join(ROOT, '.jxai', 'tmp', 'jx-modelsub-test');
mkdirSync(SCREEN, { recursive: true });
const results = [];
const ok = (name, pass, detail) => {
  results.push({ name, pass, detail });
  console.log((pass ? 'PASS ' : 'FAIL ') + name + (detail ? '  | ' + detail : ''));
};

// ---------- 环境自举：隔离实例 + 无头 Edge（同 ctxmenu-verify 模式） ----------
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
      baseURL: 'mock', apiKey: '', model: 'glm-5.3',
      providers: [{
        id: 'mock', name: 'Mock 服务', baseURL: 'mock', apiKey: '',
        models: [
          { id: 'glm-5.3', name: 'GLM-5.3', reasoningLevels: ['enabled', 'low', 'high', 'max'] },
          { id: 'plain-model', name: '无档位模型' },
          { id: 'deepseek-v4-pro', name: 'DeepSeek-V4-Pro', reasoningLevels: ['enabled', 'disabled'] },
        ],
      }],
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
    const edge = [
      'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',
      'C:\\Program Files\\Microsoft\\Edge\\Application\\msedge.exe',
    ].find((p) => { try { accessSync(p); return true; } catch (e2) { return false; } });
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

// ---------- 工具 ----------
async function clickEl(c, expr) {
  await read(c, `(() => { const el = ${expr}; if (el) el.scrollIntoView({ block: 'center' }); return 1; })()`);
  await sleep(150);
  const b = await read(c, `(() => { const el = ${expr}; if (!el) return null; const r = el.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()`);
  if (!b) throw new Error('not found: ' + expr);
  await click(c, b.x, b.y);
  return b;
}
const state = (c) => read(c, `(() => ({
  mainOpen: !document.getElementById('model-pop').classList.contains('hidden'),
  subOpen: !document.getElementById('model-sub-pop').classList.contains('hidden'),
  inlineLv: !!document.querySelector('#model-pop .mp-levels, #model-pop .mp-lv'),
})()`);

async function main() {
  await up();
  // 选一个档位多的模型（内置 glm-5.3 有 4 档；没有就退而求其次取第一个有档位的）
  const models = (await (await fetch(BASE + '/api/models')).json()).models || [];
  const target = models.find((m) => (m.reasoningLevels || []).length >= 3)
    || models.find((m) => (m.reasoningLevels || []).length > 0);
  if (!target) throw new Error('mock 模型清单里没有带思考档位的模型');
  const key = target.providerId + '|' + target.modelId;

  const t = await newTab(BASE + '/', 1280, 900);
  try {
    await sleep(1200);

    // ---------- A 打开菜单 ----------
    await clickEl(t, `document.getElementById('model-chip')`);
    await sleep(350);
    const a = await state(t);
    ok('A1 模型面板打开', a.mainOpen, '');
    ok('A2 主列表里不再有内嵌档位（.mp-levels/.mp-lv）', !a.inlineLv, '');
    ok('A3 打开时二级浮窗默认收起', !a.subOpen, '');

    // ---------- B 点带档位的模型行 ----------
    const rowExpr = `document.querySelector('#model-pop .model-item[data-key="${key}"]')`;
    const rowBox = await clickEl(t, rowExpr);
    await sleep(350);
    const b = await read(t, `(() => {
      const sub = document.getElementById('model-sub-pop');
      const row = document.querySelector('#model-pop .model-item[data-key="${key}"]');
      const sr = sub.getBoundingClientRect(), rr = row.getBoundingClientRect();
      return { open: !sub.classList.contains('hidden'), rowExp: row.classList.contains('exp'),
        items: [...sub.querySelectorAll('.mp-lv')].map((x) => x.textContent.trim()),
        onItem: (sub.querySelector('.mp-lv.on') || {}).textContent || '',
        gap: sr.left - rr.right, subTop: sr.top, rowTop: rr.top,
        overlapY: sr.top <= rr.bottom && sr.bottom >= rr.top };
    })()`);
    ok('B1 二级浮窗弹出', b.open, '');
    ok('B2 浮窗贴该行右侧（0~6px）', b.gap >= 0 && b.gap <= 6, `gap=${b.gap.toFixed(1)}`);
    ok('B3 浮窗与行垂直方向有交集', b.overlapY, `subTop=${b.subTop.toFixed(0)} rowTop=${b.rowTop.toFixed(0)}`);
    ok('B4 行带 exp 高亮', b.rowExp, '');
    const want = ['默认（跟随服务）', ...(target.reasoningLevels || [])];
    const labels = b.items.map((s) => s.replace(/^思考·/, '').replace('（跟随服务）', '（跟随服务）'));
    ok('B5 内容=默认 + ' + target.reasoningLevels.join('/') + '（思考· 前缀）',
      b.items[0] === '默认（跟随服务）' && b.items.length === want.length, JSON.stringify(b.items));
    await shot(t, join(SCREEN, 'modelsub-1-flyout.png'));

    // ---------- C 点「思考·低」 ----------
    const low = (target.reasoningLevels || []).includes('low')
      ? (target.reasoningLevels || []).find((x) => x === 'low')
      : (target.reasoningLevels || [])[0];
    const lowLabel = { enabled: '思考·开', disabled: '思考·关', low: '思考·低', medium: '思考·中', high: '思考·高', max: '思考·深' }[low];
    await clickEl(t, `[...document.querySelectorAll('#model-sub-pop .mp-lv')].find((x) => x.textContent.trim() === ${JSON.stringify(lowLabel)})`);
    await sleep(400);
    const c1 = await state(t);
    ok('C1 选档位后主面板收起', !c1.mainOpen, '');
    ok('C2 选档位后二级浮窗收起', !c1.subOpen, '');
    const chip = await read(t, `document.getElementById('model-chip-label').textContent`);
    ok('C3 芯片文案带档位「' + lowLabel + '」', chip.includes(lowLabel), chip);

    // ---------- D 重开菜单：展开状态保留 + toggle ----------
    await clickEl(t, `document.getElementById('model-chip')`);
    await sleep(500);
    const d = await read(t, `(() => {
      const sub = document.getElementById('model-sub-pop');
      return { open: !sub.classList.contains('hidden'),
        onItem: (sub.querySelector('.mp-lv.on') || {}).textContent || '',
        selKey: (document.querySelector('#model-pop .model-item.sel') || {}).dataset || null };
    })()`);
    ok('D1 重开菜单二级浮窗自动弹出（保留上次展开）', d.open, '');
    ok('D2 档位选中项=' + lowLabel, d.onItem === lowLabel, d.onItem);
    ok('D3 选中模型行 sel', d.selKey && d.selKey.key === key, d.selKey && d.selKey.key);
    await shot(t, join(SCREEN, 'modelsub-2-reopen-on.png'));
    await clickEl(t, rowExpr);
    await sleep(300);
    ok('D4 再点同一行 = 收起浮窗（toggle）', !(await state(t)).subOpen, '');

    // ---------- E 窄视口：浮窗完整落在视口内 ----------
    await t.send('Emulation.setDeviceMetricsOverride', { width: 420, height: 800, deviceScaleFactor: 1, mobile: false });
    await sleep(300);
    await clickEl(t, rowExpr);
    await sleep(350);
    const e = await read(t, `(() => {
      const sub = document.getElementById('model-sub-pop');
      const row = document.querySelector('#model-pop .model-item[data-key="${key}"]');
      const sr = sub.getBoundingClientRect(), rr = row.getBoundingClientRect();
      return { open: !sub.classList.contains('hidden'), l: sr.left, r: sr.right, t: sr.top, b: sr.bottom,
        vw: window.innerWidth, vh: window.innerHeight, rowL: rr.left, rowR: rr.right };
    })()`);
    ok('E1 窄视口浮窗仍显示', e.open, '');
    ok('E2 浮窗完整在视口内', e.l >= 0 && e.r <= e.vw && e.t >= 0 && e.b <= e.vh,
      `l=${e.l.toFixed(0)} r=${e.r.toFixed(0)} vw=${e.vw}`);
    await shot(t, join(SCREEN, 'modelsub-3-narrow.png'));
  } finally {
    await closeTab(t);
  }

  const fail = results.filter((r) => !r.pass).length;
  console.log(fail ? `\n${fail} 项失败` : '\n全部通过');
  down();
  process.exit(fail ? 1 : 0);
}

main().catch((e) => { console.error('ERROR', e); down(); process.exit(2); });
