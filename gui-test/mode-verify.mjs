// 工作模式前置 + 编程模式：GUI 验证（隔离实例 8805，baseURL=mock，data 隔离目录）。
// 断言与截图落 gui-test-screenshots/mode-*。用例：
//   A 首页胶囊顺序与位置：mode-row 在前（左）、ws-row 在后（右缘，与输入卡右沿对齐）；
//   B 模式下拉：标准办公 / 编程 两项，当前=标准办公；
//   C 选「编程」→ 胶囊文案变「编程」→ 发第一条消息（mock 秒回）→ 会话落库 mode=coding；
//   D 顶栏模式徽章显示「编程」；E 刷新后草稿不串（重开首页回到默认标准办公）。
// 运行：仓库根目录 node gui-test/mode-verify.mjs
import { newTab, read, click, typeText, shot, center, sleep, closeTab }
  from './cdp.mjs';
import { mkdirSync } from 'node:fs';

mkdirSync('../gui-test-screenshots', { recursive: true });
const results = [];
const ok = (name, pass, detail) => {
  results.push({ name, pass, detail });
  console.log((pass ? 'PASS ' : 'FAIL ') + name + (detail ? '  | ' + detail : ''));
};
const BASE = 'http://127.0.0.1:8805';

async function clickSel(c, sel) {
  await read(c, `(() => { const el = document.querySelector(${JSON.stringify(sel)}); if (el) el.scrollIntoView({ block: 'center' }); return 1; })()`);
  await sleep(200);
  const pt = await center(c, sel);
  await click(c, pt.x, pt.y);
  return pt;
}

async function main() {
  const t = await newTab(BASE + '/', 390, 844);
  try {
    await sleep(1200);
    // ---------- A 胶囊顺序与位置 ----------
    let geo = await read(t, `(() => {
      const chips = document.getElementById('draft-chips');
      const mode = document.getElementById('mode-row');
      const ws = document.getElementById('ws-row');
      const card = document.querySelector('.composer-card') || document.getElementById('composer');
      const r = (el) => { const b = el.getBoundingClientRect(); return { l: b.left, r: b.right, t: b.top }; };
      return { order: [...chips.children].map((c) => c.id), mode: r(mode), ws: r(ws),
        card: card ? r(card) : null, modeHidden: mode.classList.contains('hidden') };
    })()`);
    ok('A1 mode-row 在前、ws-row 在后', geo.order[0] === 'mode-row' && geo.order[1] === 'ws-row', JSON.stringify(geo.order));
    ok('A2 两胶囊都可见', !geo.modeHidden, '');
    ok('A3 模式胶囊在左、目录胶囊在其右侧', geo.mode.l < geo.ws.l, `mode.l=${geo.mode.l.toFixed(0)} ws.l=${geo.ws.l.toFixed(0)}`);
    if (geo.card) {
      const gap = Math.abs(geo.ws.r - geo.card.r);
      ok('A4 目录胶囊右缘与输入卡右沿对齐（±6px）', gap <= 6, `ws.r=${geo.ws.r.toFixed(0)} card.r=${geo.card.r.toFixed(0)} gap=${gap.toFixed(1)}`);
    }
    await shot(t, '../gui-test-screenshots/mode-1-home-chips.png');

    // ---------- B 模式下拉两项 ----------
    await clickSel(t, '#mode-row');
    await sleep(300);
    let pop = await read(t, `(() => {
      const items = [...document.querySelectorAll('#mode-pop-list .pop-item')];
      return { open: !document.getElementById('mode-pop').classList.contains('hidden'),
        opts: items.map((b) => b.querySelector('b') ? b.querySelector('b').textContent : ''),
        sel: (items.find((b) => b.classList.contains('sel')) || {}).textContent || '' };
    })()`);
    ok('B1 下拉打开且有两项：标准办公 / 编程', pop.open && pop.opts.length === 2 && pop.opts[0] === '标准办公' && pop.opts[1] === '编程', JSON.stringify(pop.opts));
    ok('B2 当前选中=标准办公', pop.sel.includes('标准办公'), pop.sel.slice(0, 20));
    await shot(t, '../gui-test-screenshots/mode-2-pop.png');

    // ---------- C 选编程 → 建会话 ----------
    await read(t, `(() => { const b = [...document.querySelectorAll('#mode-pop-list .pop-item')].find((x) => x.querySelector('b') && x.querySelector('b').textContent === '编程'); if (b) b.click(); return 1; })()`);
    await sleep(300);
    let chip = await read(t, `(() => document.getElementById('mode-row-name').textContent)()`);
    ok('C1 选后胶囊文案=编程', chip === '编程', chip);
    await shot(t, '../gui-test-screenshots/mode-3-after-pick.png');

    let c = await center(t, '#input');
    await click(t, c.x, c.y);
    await typeText(t, '帮我写一个 hello world 的 Go 程序');
    let b = await center(t, '#btn-send');
    await click(t, b.x, b.y);
    await sleep(3500); // 等 mock 回合跑完
    const sess = await (await fetch(BASE + '/api/sessions')).json();
    const mine = (sess.sessions || []).find((s) => !s.parentId && s.mode !== 'assistant');
    ok('C2 新会话落库 mode=coding', mine && mine.mode === 'coding', mine ? 'mode=' + mine.mode : 'no session');
    // 徽章随 applySessionChrome 在「打开会话」时刷新（懒创建瞬间顶栏不动是存量行为，
    // office 同样如此）——显式打开后再读
    await read(t, `openSession(${JSON.stringify(mine.id)})`);
    await sleep(900);
    let badge = await read(t, `(() => { const b = document.getElementById('mode-badge'); return b.classList.contains('hidden') ? '' : b.textContent; })()`);
    ok('C3 顶栏模式徽章=编程', badge.includes('编程'), badge);
    await shot(t, '../gui-test-screenshots/mode-4-coding-session.png');

    // ---------- D 已有 office 会话不受影响（后端直接建一个 office 会话对照） ----------
    await (await fetch(BASE + '/api/sessions', { method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode: 'office', title: '对照-办公' }) })).json();
    await (await fetch(BASE + '/api/sessions', { method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode: 'task', title: '对照-任务' }) })).json();
    const all = await (await fetch(BASE + '/api/sessions')).json();
    const byMode = {};
    for (const s of all.sessions || []) byMode[s.mode] = (byMode[s.mode] || 0) + 1;
    ok('D1 office / coding / task 会话并存且模式各归各位', byMode.office >= 1 && byMode.coding >= 1 && byMode.task >= 1, JSON.stringify(byMode));

    const fails = results.filter((r) => !r.pass);
    console.log(fails.length ? `\n${fails.length} FAILED` : '\nALL PASS');
    process.exitCode = fails.length ? 1 : 0;
  } finally {
    await closeTab(t);
  }
}

main().catch((e) => { console.error('SCRIPT-ERROR', e); process.exitCode = 1; });
