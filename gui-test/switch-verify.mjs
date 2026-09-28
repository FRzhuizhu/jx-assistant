// 会话切换三性验证（CDP 驱动 headless Edge，真实点击/输入）：
//   T1 乐观切换：经 2.5s 延迟代理点开另一会话——延迟窗口内顶栏标题已换、侧栏高亮已换、
//      消息区转圈（.chat-loading），数据落地后渲染真实消息、转圈退场；
//   T2 失败停留：代理故障开关打开后切换——失败后留在目标会话的壳里（标题不回退），
//      给出「对话加载失败 + 重试」，点重试（关闸后）能正常加载；
//   T3 连点竞态：A→B 快速连点（B 最后）——最终停在 B，无残留转圈，晚到的 A 响应被令牌丢弃。
// 环境：mock 实例 8802（隔离目录 data/testrun）+ 延迟代理 8803（见 data/testrun/delay-proxy.mjs）。
import { newTab, read, click, typeText, shot, sleep, closeTab } from './cdp.mjs';
import { mkdirSync } from 'node:fs';

mkdirSync('../gui-test-screenshots', { recursive: true });
const results = [];
const ok = (name, pass, detail) => {
  results.push({ name, pass });
  console.log((pass ? 'PASS ' : 'FAIL ') + name + (detail ? '  | ' + detail : ''));
};

const stateExpr = `(() => ({
  title: document.getElementById('sess-title').textContent,
  loading: !!document.querySelector('#msg-col .chat-loading'),
  loadingText: (document.querySelector('#msg-col .chat-loading') || {}).textContent || '',
  activeTitle: (document.querySelector('.session-item.active .s-title') || {}).textContent || '(无)',
  userMsgs: [...document.querySelectorAll('#msg-col .msg.user')].map(e => e.textContent.trim().slice(0, 14)),
  inputVal: document.getElementById('input').value,
}))()`;

// 侧栏里按标题找会话行中心坐标（真实点击用）
const itemCenter = (needle) => read(t_global,
  `(() => { const items = [...document.querySelectorAll('.session-item')];
    const it = items.find(i => (i.querySelector('.s-title')||{}).textContent.includes(${JSON.stringify(needle)}));
    if (!it) return null; const b = it.getBoundingClientRect();
    return { x: b.left + b.width / 2, y: b.top + b.height / 2 }; })()`);
let t_global;

async function seed(tab, firstMsg) {
  // 从首页发第一条消息建会话（首消息即标题），等一轮跑完
  const c = await read(tab, `(() => { const b = document.getElementById('input').getBoundingClientRect(); return { x: b.left + 40, y: b.top + 20 }; })()`);
  await click(tab, c.x, c.y);
  await typeText(tab, firstMsg);
  const btn = await read(tab, `(() => { const b = document.getElementById('btn-send').getBoundingClientRect(); return { x: b.left + b.width/2, y: b.top + b.height/2 }; })()`);
  await click(tab, btn.x, btn.y);
  await sleep(6500); // chat 延迟 1.5s + mock 流式 + done + endTurn 整取（详情又延迟 2.5s）
}

const t = await newTab('http://127.0.0.1:8803/', 1280, 900);
t_global = t;
try {
  await sleep(900);
  await seed(t, '甲会话开头：这是甲的第一条消息');
  await read(t, `document.querySelector('.side-btn') ? 1 : 1`); // 让位
  // 回首页建第二个会话
  const homeBtn = await read(t, `(() => { const b = document.getElementById('btn-new-task').getBoundingClientRect(); return { x: b.left + b.width/2, y: b.top + b.height/2 }; })()`);
  await click(t, homeBtn.x, homeBtn.y);
  await sleep(400);
  await seed(t, '乙会话开头：这是乙的第一条消息');
  let s = await read(t, stateExpr);
  ok('预备·两个会话已建好且当前在乙', s.activeTitle.includes('乙会话开头') && s.userMsgs.some(x => x.includes('乙的第一条')), JSON.stringify({ active: s.activeTitle, msgs: s.userMsgs }));

  // ---------- T1 乐观切换：点甲，2.5s 延迟窗口内壳已换 ----------
  let p = await itemCenter('甲会话开头');
  if (!p) throw new Error('侧栏找不到甲会话');
  await click(t, p.x, p.y);
  await sleep(700); // 延迟窗口内（详情 +2.5s）
  s = await read(t, stateExpr);
  await shot(t, '../gui-test-screenshots/switch-t1-loading.png');
  ok('T1·延迟窗口内：标题已切甲', s.title.includes('甲会话开头'), 'title=' + s.title);
  ok('T1·延迟窗口内：侧栏高亮已切甲', s.activeTitle.includes('甲会话开头'), 'active=' + s.activeTitle);
  ok('T1·延迟窗口内：消息区转圈占位', s.loading && s.loadingText.includes('正在加载对话'), JSON.stringify(s.loadingText));
  await sleep(3500); // 等详情落地 + 渲染
  s = await read(t, stateExpr);
  await shot(t, '../gui-test-screenshots/switch-t1-loaded.png');
  ok('T1·数据落地：甲的消息渲染、转圈退场', !s.loading && s.userMsgs.some(x => x.includes('甲的第一条')), JSON.stringify({ loading: s.loading, msgs: s.userMsgs }));

  // ---------- T2 失败停留：拉闸后切乙 ----------
  await fetch('http://127.0.0.1:8803/__chaos?set=on');
  p = await itemCenter('乙会话开头');
  await click(t, p.x, p.y);
  await sleep(700);
  s = await read(t, stateExpr); // 延迟窗口内（chaos 立即 502，但乐观壳应已切）
  ok('T2·故障下点击：壳仍切到乙（标题/高亮）', s.title.includes('乙会话开头') && s.activeTitle.includes('乙会话开头'), JSON.stringify({ title: s.title, active: s.activeTitle }));
  await sleep(2500); // 502 返回 + 错误态
  s = await read(t, stateExpr);
  await shot(t, '../gui-test-screenshots/switch-t2-error.png');
  ok('T2·失败停留：给出原因与重试、不回退到甲', s.title.includes('乙会话开头') && s.loadingText.includes('对话加载失败') && !s.userMsgs.some(x => x.includes('甲的第一条')), JSON.stringify({ title: s.title, loadingText: s.loadingText }));
  // 关闸 → 点重试
  await fetch('http://127.0.0.1:8803/__chaos?set=off');
  const r = await read(t, `(() => { const b = document.querySelector('#msg-col .cl-retry'); if (!b) return null; const g = b.getBoundingClientRect(); return { x: g.left + g.width/2, y: g.top + g.height/2 }; })()`);
  if (!r) throw new Error('找不到重试按钮');
  await click(t, r.x, r.y);
  await sleep(700); // 重试也走乐观：立即转圈
  s = await read(t, stateExpr);
  ok('T2·重试即转圈', s.loading && s.loadingText.includes('正在加载对话'), JSON.stringify(s.loadingText));
  await sleep(3500);
  s = await read(t, stateExpr);
  await shot(t, '../gui-test-screenshots/switch-t2-retry-loaded.png');
  ok('T2·重试成功：乙的消息渲染', !s.loading && s.userMsgs.some(x => x.includes('乙的第一条')), JSON.stringify({ loading: s.loading, msgs: s.userMsgs }));

  // ---------- T3 连点竞态：乙 → 甲 → 乙（最后是乙）----------
  const pa = await itemCenter('甲会话开头');
  const pb = await itemCenter('乙会话开头');
  await click(t, pb.x, pb.y);           // 已在乙：同会话重开（无乐观层）
  await click(t, pa.x, pa.y);           // 甲（乐观切甲、开始加载）
  await sleep(250);                     // 甲在途中
  await click(t, pb.x, pb.y);           // 立刻又点乙（顶掉甲的导航）
  let s3 = null;
  for (let i = 0; i < 10; i++) {        // 轮询至多 10s：允许一轮额外的导航往返收口
    await sleep(1000);
    s3 = await read(t, `(() => ({
      title: document.getElementById('sess-title').textContent,
      activeTitle: (document.querySelector('.session-item.active .s-title') || {}).textContent || '(无)',
      loading: !!document.querySelector('#msg-col .chat-loading'),
      loadingText: (document.querySelector('#msg-col .chat-loading') || {}).textContent || '',
      msgs: [...document.querySelectorAll('#msg-col .msg.user')].map(e => e.textContent.trim().slice(0, 14)),
      navTok: (typeof navTok !== 'undefined') ? navTok.main : null,
      navBusy: (typeof navBusy !== 'undefined') ? navBusy.main : null,
      curId: (typeof state !== 'undefined' && state.cur) ? state.cur.id : null,
    }))()`);
    if (!s3.loading && s3.msgs.length) break;
  }
  await shot(t, '../gui-test-screenshots/switch-t3-race.png');
  ok('T3·连点竞态：最终停在乙、无残留加载态、甲的晚到响应未顶回',
    s3.title.includes('乙会话开头') && s3.activeTitle.includes('乙会话开头') && !s3.loading
      && s3.msgs.some(x => x.includes('乙的第一条')) && !s3.msgs.some(x => x.includes('甲的第一条')),
    JSON.stringify(s3));
} finally {
  await fetch('http://127.0.0.1:8803/__chaos?set=off').catch(() => {});
  await closeTab(t);
}

const fails = results.filter(r => !r.pass);
console.log(`\n==== ${results.length - fails.length}/${results.length} PASS ====`);
if (fails.length) { fails.forEach(f => console.log('FAILED: ' + f.name)); process.exit(1); }
