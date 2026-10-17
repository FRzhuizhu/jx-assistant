// 待决定的浮窗回归：
//  · 本对话的卡在输入框上方（附件之上、与输入卡同宽）
//  · 别的对话的卡攒在右下角角标堆（带「来自「某对话」」来源行、可点击跳转、可缩成胶囊）
//  · 反复重绘不清空已输入的字
//  · 右栏「计划」视图 Markdown 渲染、可反复打开、可导出成工作区 .md
// 用法：先起开发实例（jx-assistant.exe -webdir web -port 18798 -noopen -browser），
// 再 `node gui-test/ask-plan-verify.mjs`。
import { newTab, read, click, shot, center, sleep, typeText, closeTab } from './cdp.mjs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { existsSync, unlinkSync } from 'node:fs';

const BASE = process.env.JX_BASE || 'http://127.0.0.1:18798';
const DIR = resolve(dirname(fileURLToPath(import.meta.url)), '..', 'gui-test-screenshots');

const results = [];
function check(name, ok, detail) {
  results.push({ name, ok: !!ok, detail: detail === undefined ? '' : detail });
  console.log((ok ? 'PASS  ' : 'FAIL  ') + name + (detail === undefined ? '' : '   ' + JSON.stringify(detail)));
}
function approx(a, b, tol) { return Math.abs(a - b) <= (tol === undefined ? 2 : tol); }

const PLAN_MD = [
  '# 审批浮窗与计划视图改造',
  '',
  '把审批/提问浮窗搬到输入框上方，计划落到右栏并用 Markdown 渲染。',
  '',
  '## 步骤',
  '',
  '- 浮窗落点：输入卡上方、附件区之上',
  '- 跨对话的卡攒在右下角角标堆，可缩成胶囊',
  '- 计划全文进右栏「计划」视图',
  '',
  '| 项 | 期望 |',
  '| --- | --- |',
  '| 宽度 | 与输入卡同宽 |',
  '',
  '```js',
  'renderPanels();',
  '```',
].join('\n');

const geom = `(() => {
  const r = (el) => { if (!el) return null; const b = el.getBoundingClientRect();
    return { x: Math.round(b.x), y: Math.round(b.y), w: Math.round(b.width), h: Math.round(b.height),
             top: Math.round(b.top), bottom: Math.round(b.bottom), right: Math.round(b.right) }; };
  const bar = document.getElementById('ask-bar');
  const dock = document.getElementById('ask-dock');
  const pill = document.getElementById('ask-dock-pill');
  return {
    hidden: bar.classList.contains('hidden'),
    bar: r(bar), composer: r(document.getElementById('composer')),
    attach: r(document.getElementById('attachments')),
    main: r(document.getElementById('main')),
    col: r(document.querySelector('#messages .msg-col')),
    barCards: bar.children.length,
    dockHidden: dock.classList.contains('hidden'),
    dock: r(dock), dockCards: document.getElementById('ask-dock-cards').children.length,
    dockPos: getComputedStyle(dock).position,
    dockCount: document.getElementById('ask-dock-count').textContent,
    pillHidden: pill.classList.contains('hidden'), pill: r(pill),
    pillNum: document.getElementById('ask-dock-pill-num').textContent,
    vw: window.innerWidth, vh: window.innerHeight,
  };
})()`;

// 把某个元素滚进视野再给中心点（浮窗含滚动上限，卡多时后面的卡要滚一下）
const centerInView = (sel) => `(() => {
  const el = document.querySelector(${JSON.stringify(sel)});
  if (!el) return null;
  el.scrollIntoView({ block: 'center' });
  const b = el.getBoundingClientRect();
  return { x: b.left + b.width / 2, y: b.top + b.height / 2 };
})()`;

const c = await newTab(BASE + '/index.html', 1920, 900);
const exported = [];
try {
  await sleep(1200);

  // 1) 打开一个真实会话（真实对话布局：悬浮输入区贴底）
  const ids = await read(c, `(async () => {
    const d = await (await fetch('/api/sessions')).json();
    return (d.sessions || []).slice(0, 3).map((s) => ({ id: s.id, title: s.title }));
  })()`);
  const own = ids[0].id, other = ids[1].id, otherTitle = ids[1].title || '另一个对话';
  await read(c, `openSession(${JSON.stringify(own)})`);
  await sleep(1500);

  // 2) 把两个轮询接口打桩成本例数据（与真实轮询同一条代码路径，数据稳定不打架）
  await read(c, `(() => {
    const approvals = [
      { id: 'ap-test-own', sessionId: ${JSON.stringify(own)}, sessionTitle: '', tool: 'run_command',
        summary: 'git status --short', reason: '命令越出沙箱：只读档下需要用户批准', createdAt: '' },
      { id: 'ap-test-other', sessionId: ${JSON.stringify(other)}, sessionTitle: ${JSON.stringify(otherTitle)}, tool: 'write',
        summary: '写文件 D:/x/y.md', reason: '工作区外写入', createdAt: '' },
    ];
    const questions = [
      { id: 'q-test-ask', sessionId: ${JSON.stringify(own)}, kind: 'ask',
        question: '这批文件要不要一起改？', options: ['只改前端', '前后端一起改'] },
      { id: 'q-test-plan', sessionId: ${JSON.stringify(own)}, kind: 'plan',
        question: '请审批以下执行计划', plan: ${JSON.stringify(PLAN_MD)} },
    ];
    const json = (body) => Promise.resolve(new Response(JSON.stringify(body),
      { status: 200, headers: { 'Content-Type': 'application/json' } }));
    const real = window.fetch;
    window.fetch = (u, o) => {
      const s = String(u);
      if (s.indexOf('/api/approvals') >= 0) return json({ approvals });
      if (s.indexOf('/api/questions') >= 0) return json({ questions });
      return real(u, o);
    };
    return true;
  })()`);
  await read(c, 'loadApprovals()');
  await sleep(150);
  await read(c, 'loadQuestions()');
  await sleep(250);

  // 计划提问到齐即自动打开右栏「计划」视图（本对话的才自动开）
  const auto = await read(c, `(() => ({
    open: !document.getElementById('rpanel').classList.contains('hidden'), active: rpActive,
  }))()`);
  check('计划提问到达时自动打开右栏「计划」', auto.open === true && auto.active === 'plan', auto);
  await read(c, 'toggleRpanel()'); // 收起：后面按「无右栏」的常规布局测位置与宽度
  await sleep(700);
  await read(c, 'loadApprovals()');
  await sleep(150);
  await read(c, 'loadQuestions()');
  await sleep(300);

  // 附件区：造一个可见的附件（位置断言要用）
  await read(c, `(() => {
    const rail = document.getElementById('attachments');
    rail.classList.remove('hidden');
    rail.innerHTML = '<div class="attach-item"><span class="attach-name">示例附件.txt</span></div>';
    return true;
  })()`);
  await sleep(120);

  // 3) 本对话的卡：输入卡上方、附件之上、同宽；别的对话的卡进右下角
  const g1 = await read(c, geom);
  check('输入区上方只有本对话的 3 张卡', !g1.hidden && g1.barCards === 3, { cards: g1.barCards, hidden: g1.hidden });
  check('浮窗在输入卡上方', g1.bar.bottom <= g1.composer.top + 1, { barBottom: g1.bar.bottom, composerTop: g1.composer.top });
  check('浮窗在附件区上方（有附件时更靠上）', g1.attach && g1.bar.bottom <= g1.attach.top + 1,
    { barBottom: g1.bar.bottom, attachTop: g1.attach ? g1.attach.top : null });
  check('浮窗宽度＝输入卡宽度', approx(g1.bar.w, g1.composer.w), { bar: g1.bar.w, composer: g1.composer.w });
  check('别的对话的卡在右下角角标堆里（1 张、fixed 定位）', !g1.dockHidden && g1.dockCards === 1 && g1.dockPos === 'fixed',
    { dockHidden: g1.dockHidden, dockCards: g1.dockCards, dockPos: g1.dockPos });
  check('角标堆只占对话区右侧留白，不吃正文列', g1.dock.x >= g1.col.right - 1 && approx(g1.dock.right, g1.main.right - 12),
    { dockX: g1.dock.x, colRight: g1.col.right, dockRight: g1.dock.right, mainRight: g1.main.right });
  check('角标堆宽度收在 240–300px（比对话区浮窗窄一档）', g1.dock.w >= 240 && g1.dock.w <= 300, { w: g1.dock.w });
  check('角标堆抬头显示条数', /1\s*项/.test(g1.dockCount), { count: g1.dockCount });
  await shot(c, resolve(DIR, 'ask-08-dock-corner.png'));

  // 3b) 右栏占满留白时：角标堆自动收起（不跟右栏抢空间），关掉右栏再回来
  await read(c, `openPlanView(${JSON.stringify(own)})`);
  await sleep(900);
  const gPanel = await read(c, geom);
  check('右栏开着（留白被占）时角标堆不铺开', gPanel.dockHidden === true, { dockHidden: gPanel.dockHidden, gutter: gPanel.main.right - gPanel.col.right });
  await read(c, 'toggleRpanel()');
  await sleep(800);
  const gNoPanel = await read(c, geom);
  check('关掉右栏后角标堆自动回到留白里', gNoPanel.dockHidden === false, { dockHidden: gNoPanel.dockHidden });

  // 4) 来源行与跳转（角标堆里的卡带「来自「某对话」」）
  const src = await read(c, `(() => {
    const card = document.querySelector('#ask-dock .ap-card.from-other');
    if (!card) return null;
    const strip = card.querySelector('.ap-src');
    return { text: strip ? strip.textContent.replace(/\\s+/g, ' ').trim() : '',
             cls: card.className, srcCount: document.querySelectorAll('#ask-dock .ap-src').length };
  })()`);
  check('角标堆里的卡带「来自「某对话」」来源行', src && src.text.includes('来自「') && src.srcCount === 1, src);
  // 角标堆必须在（这是后面点跳转的前提）：不在就报错并跳过点击，避免点到 (0,0) 乱跳
  const dockVisible = await read(c, `(() => {
    const d = document.getElementById('ask-dock');
    return { hidden: d.classList.contains('hidden'), w: Math.round(d.getBoundingClientRect().width),
             gutter: Math.round(document.getElementById('main').getBoundingClientRect().right - document.querySelector('#messages .msg-col').getBoundingClientRect().right) };
  })()`);
  check('点来源行之前角标堆是展开的（留白足够）', dockVisible.hidden === false && dockVisible.w > 0, dockVisible);
  if (!dockVisible.hidden) {
    const p = await read(c, centerInView('#ask-dock .ap-card.from-other .ap-src'));
    await click(c, p.x, p.y);
    await sleep(1600);
    const jumped = await read(c, 'state.cur && state.cur.id');
    check('点来源行切到那个对话', jumped === other, { jumped, other });
  }

  // 5) 缩成胶囊 → 点胶囊再展开
  await read(c, `openSession(${JSON.stringify(own)})`);
  await sleep(1200);
  await read(c, 'loadApprovals()');
  await sleep(150);
  await read(c, 'loadQuestions()');
  await sleep(300);
  const minBox = await read(c, centerInView('#ask-dock-min'));
  await click(c, minBox.x, minBox.y);
  await sleep(400);
  const g2 = await read(c, geom);
  check('点「—」后角标堆收起、胶囊出现并报条数', g2.dockHidden === true && g2.pillHidden === false && g2.pillNum === '1',
    { dockHidden: g2.dockHidden, pillHidden: g2.pillHidden, pillNum: g2.pillNum });
  check('胶囊与角标堆同一锚点（同样只占右侧留白）', approx(g2.pill.right, g2.main.right - 12),
    { pillRight: g2.pill.right, mainRight: g2.main.right });
  await shot(c, resolve(DIR, 'ask-09-dock-pill.png'));
  const pillBox = await read(c, centerInView('#ask-dock-pill'));
  await click(c, pillBox.x, pillBox.y);
  await sleep(400);
  const g3 = await read(c, geom);
  check('点胶囊再展开角标堆', g3.dockHidden === false && g3.pillHidden === true && g3.dockCards === 1,
    { dockHidden: g3.dockHidden, pillHidden: g3.pillHidden, dockCards: g3.dockCards });

  // 6) 反复重绘（真实轮询路径）不清空输入（本对话的卡）
  const askInput = '#ask-bar .q-card .q-free input';
  const box = await read(c, centerInView(askInput));
  await click(c, box.x, box.y);
  await typeText(c, '半句话还没写完');
  await sleep(150);
  const before = await read(c, `(() => {
    const el = document.querySelector(${JSON.stringify(askInput)});
    window.__askNode = el;
    return { value: el.value, cards: document.querySelectorAll('#ask-bar .ap-card').length };
  })()`);
  for (let i = 0; i < 3; i++) { // 连拉三次（等价于隔十几秒来一轮的兜底轮询）
    await read(c, 'loadApprovals()');
    await sleep(120);
    await read(c, 'loadQuestions()');
    await sleep(120);
  }
  await read(c, 'renderPanels()');
  await sleep(200);
  const after = await read(c, `(() => {
    const el = document.querySelector(${JSON.stringify(askInput)});
    return { value: el.value, sameNode: el === window.__askNode, cards: document.querySelectorAll('#ask-bar .ap-card').length };
  })()`);
  check('轮询+重绘后输入的字还在', after.value === '半句话还没写完', { before: before.value, after: after.value });
  check('未变的卡片没被重建（同一 DOM 节点）', after.sameNode === true, { sameNode: after.sameNode });
  check('卡片数量稳定', after.cards === before.cards, { before: before.cards, after: after.cards });

  // 数据真变了（选项变化）→ 只重建那张卡，草稿回填
  await read(c, `(() => {
    state.questions = state.questions.map((q) => q.id === 'q-test-ask' ? { ...q, options: ['只改前端', '前后端一起改', '再加一条选项'] } : q);
    renderPanels();
    return true;
  })()`);
  await sleep(200);
  const rebuilt = await read(c, `(() => {
    const el = document.querySelector(${JSON.stringify(askInput)});
    return { value: el.value, sameNode: el === window.__askNode, planCardStillThere: !!document.querySelector('#ask-bar .q-card.plan') };
  })()`);
  check('内容变了只重建该卡，草稿仍回填', rebuilt.value === '半句话还没写完' && rebuilt.sameNode === false, rebuilt);
  await shot(c, resolve(DIR, 'ask-10-cards.png'));

  // 7) 计划：右栏「计划」视图（Markdown 渲染 + 收起再打开 + 提问离场后仍在）
  await read(c, `openPlanView(${JSON.stringify(own)})`);
  await sleep(800);
  const pv = await read(c, `(() => {
    const panel = document.getElementById('rpanel');
    const doc = document.getElementById('plan-doc');
    return {
      open: !panel.classList.contains('hidden'),
      active: document.getElementById('rp-view-plan').classList.contains('active'),
      tabs: [...document.querySelectorAll('.rp-tab')].map((b) => b.textContent),
      h2: !!doc.querySelector('.plan-md h2'), h3: !!doc.querySelector('.plan-md h3'),
      ul: !!doc.querySelector('.plan-md ul'), table: !!doc.querySelector('.plan-md table'),
      pre: !!doc.querySelector('.plan-md pre'),
      actVisible: !document.getElementById('plan-act').classList.contains('hidden'),
      status: document.getElementById('plan-status').textContent,
    };
  })()`);
  check('右栏计划视图被打开', pv.open && pv.active, { open: pv.open, active: pv.active });
  check('计划按 Markdown 渲染（标题/小标题/列表/表格/代码块）', pv.h2 && pv.h3 && pv.ul && pv.table && pv.pre, pv);
  check('待审批时底部有批准/修改操作区', pv.actVisible === true, { actVisible: pv.actVisible });
  check('标签顺序为 子对话 → 计划 → 浏览器', pv.tabs[0] === '子对话'
    && pv.tabs.indexOf('计划') > 0 && pv.tabs.indexOf('计划') < pv.tabs.indexOf('浏览器'), pv.tabs);
  await shot(c, resolve(DIR, 'ask-04-plan-sidebar.png'));

  await read(c, 'toggleRpanel()');
  await sleep(700);
  const closed = await read(c, "document.getElementById('rpanel').classList.contains('hidden')");
  check('右栏能收起', closed === true);
  await read(c, 'toggleRpanel()');
  await sleep(800);
  const reopen = await read(c, `(() => {
    const doc = document.getElementById('plan-doc');
    return {
      open: !document.getElementById('rpanel').classList.contains('hidden'),
      activeTab: (document.querySelector('.rp-tab.active') || {}).textContent || '',
      md: !!doc.querySelector('.plan-md'),
      hasPlanText: doc.textContent.indexOf('审批浮窗与计划视图改造') >= 0,
    };
  })()`);
  check('顶栏再展开回到「计划」且内容还在', reopen.open && reopen.md && reopen.activeTab === '计划', reopen);

  await read(c, `removeQuestion('q-test-plan')`);
  await sleep(600);
  const afterRemove = await read(c, `(() => {
    const doc = document.getElementById('plan-doc');
    return { md: !!doc.querySelector('.plan-md'), hasPlanText: doc.textContent.indexOf('审批浮窗与计划视图改造') >= 0 };
  })()`);
  check('提问离场后计划文档仍在（持久化）', afterRemove.md && afterRemove.hasPlanText, afterRemove);

  // 8) 导出：真数据会话（历史里有提交过的计划）
  const planSid = await read(c, `(async () => {
    const d = await (await fetch('/api/sessions')).json();
    for (const s of (d.sessions || []).slice(0, 40)) {
      const r = await fetch('/api/sessions/' + s.id + '/plans');
      const j = await r.json();
      if (j.plans && j.plans.length) {
        const last = j.plans[j.plans.length - 1];
        return { id: s.id, n: j.plans.length, title: last.title, seq: last.seq };
      }
    }
    return null;
  })()`);
  check('找到一个有历史计划的真会话', !!planSid, planSid);
  if (planSid) {
    await read(c, `openPlanView(${JSON.stringify(planSid.id)})`);
    await sleep(1000);
    const expBtn = await read(c, centerInView('#plan-export'));
    await click(c, expBtn.x, expBtn.y);
    await sleep(1000);
    const note = await read(c, `(() => {
      const n = document.getElementById('plan-note');
      return { hidden: n.classList.contains('hidden'), text: document.getElementById('plan-note-text').textContent,
               openBtn: !document.getElementById('plan-note-open').classList.contains('hidden'), dir: n.dataset.dir || '' };
    })()`);
    check('导出后视图顶部显示落盘路径', !note.hidden && note.text.indexOf('已导出') === 0 && note.dir !== '', note);
    check('给了「打开所在文件夹」', note.openBtn === true, { openBtn: note.openBtn });
    const first = note.text.replace('已导出：', '').trim();
    exported.push(first);
    check('导出的文件真的在工作区里', existsSync(first), { path: first });
    check('文件名是「计划-<时间>-<标题>.md」', /计划-\d{8}-\d{4}-.+\.md$/.test(first), { name: first.split(/[\\/]/).pop() });
    await shot(c, resolve(DIR, 'ask-11-plan-export.png'));
    // 再导一次：同名不覆盖，自动 -2
    await click(c, expBtn.x, expBtn.y);
    await sleep(1000);
    const note2 = await read(c, `(() => document.getElementById('plan-note-text').textContent)()`);
    const second = note2.replace('已导出：', '').trim();
    exported.push(second);
    check('重复导出自增序号（不覆盖）', second !== first && /-2\.md$/.test(second) && existsSync(second),
      { first: first.split(/[\\/]/).pop(), second: second.split(/[\\/]/).pop() });
    // 端点口径：指定不存在的 seq 要有可读错误
    const bad = await read(c, `(async () => {
      const r = await fetch('/api/sessions/${planSid.id}/plans/export', { method: 'POST',
        headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ seq: 999999 }) });
      return { status: r.status, body: await r.text() };
    })()`);
    check('导出不存在的 seq 给可读错误', bad.status === 404 && bad.body.indexOf('找不到') >= 0, bad);
  }

  // 9) 窄屏：位置关系与宽度同样成立
  await c.send('Emulation.setDeviceMetricsOverride', { width: 393, height: 852, deviceScaleFactor: 2, mobile: true });
  await sleep(700);
  const g4 = await read(c, geom);
  check('窄屏下本对话的卡在输入卡上方且同宽', g4.bar.bottom <= g4.composer.top + 1 && approx(g4.bar.w, g4.composer.w),
    { bar: g4.bar, composer: g4.composer });
  check('窄屏下角标堆退回窗口右下角且不出屏', g4.dockHidden || (approx(g4.vw - g4.dock.right, 12) && g4.dock.bottom <= g4.vh),
    { dockRight: g4.dock.right, dockBottom: g4.dock.bottom, vw: g4.vw, vh: g4.vh, hidden: g4.dockHidden });
  await shot(c, resolve(DIR, 'ask-06-narrow.png'));

  // 9b) 桌面窗口偏窄（右侧留白剩 ~80px）：放不下就干脆不显示（连胶囊都不给）
  await c.send('Emulation.setDeviceMetricsOverride', { width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
  await sleep(700);
  const g5 = await read(c, geom);
  check('留白放不下时角标堆与胶囊都不显示（左侧栏橙点仍在）',
    g5.dockHidden === true && g5.pillHidden === true,
    { gutter: g5.main.right - g5.col.right, dockHidden: g5.dockHidden, pillHidden: g5.pillHidden });
  await shot(c, resolve(DIR, 'ask-12-dock-nospace.png'));

  // 10) 无待办时三块都不占位
  await c.send('Emulation.setDeviceMetricsOverride', { width: 1280, height: 900, deviceScaleFactor: 1, mobile: false });
  await sleep(500);
  const zero = await read(c, `(() => {
    document.getElementById('attachments').classList.add('hidden');
    state.approvals = []; state.questions = [];
    renderPanels();
    const bar = document.getElementById('ask-bar');
    return {
      barHidden: bar.classList.contains('hidden'), barH: Math.round(bar.getBoundingClientRect().height),
      dockHidden: document.getElementById('ask-dock').classList.contains('hidden'),
      pillHidden: document.getElementById('ask-dock-pill').classList.contains('hidden'),
    };
  })()`);
  check('没有待办时输入区上方整块隐藏（零占位）', zero.barHidden === true && zero.barH === 0, zero);
  check('没有待办时角标堆与胶囊都隐藏', zero.dockHidden === true && zero.pillHidden === true, zero);

  const failed = results.filter((r) => !r.ok);
  console.log('\n' + (failed.length ? ('FAILED ' + failed.length + '/' + results.length) : ('ALL PASS ' + results.length)));
  for (const f of failed) console.log('  ✗ ' + f.name);
  process.exitCode = failed.length ? 1 : 0;
} finally {
  for (const f of exported) { try { if (existsSync(f)) unlinkSync(f); } catch (e) {} } // 测试导出的文件清掉
  await closeTab(c);
}
