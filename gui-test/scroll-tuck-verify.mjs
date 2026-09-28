// 双层滚动条 + 抽搐 + 发送中占位：新旧代码 A/B 验证（CDP 驱动 headless Edge，真实输入事件）。
//   旧版 = 8804（web/ 取自 git HEAD），新版 = 8803（3 秒延迟代理 → 8802，web/ 为工作区改动）。
// 断言与截图落在 gui-test-screenshots/tuck-*。用例：
//   T1 发送中占位（仅新版）：发送后 3s 延迟窗口内「发送中」虚线气泡在、输入框已清、
//      回显落地后气泡被正常气泡替换；
//   T2 双层滚动条：上翻收起输入区后，文档级是否出现滚动（scrollingElement 溢出）；
//   T3 抽搐：近底边界小步上翻 ×8，采样 tucked 类是否来回翻转（旧版会翻、新版不该翻）；
//   T4 滚回底部输入区展开；失败路（新版）：上游停掉后发送 → 输入框回填 + 报错条（单独跑，见尾注）。
import { newTab, read, click, wheel, key, typeText, shot, center, sleep, closeTab }
  from './cdp.mjs';
import { mkdirSync } from 'node:fs';

mkdirSync('../gui-test-screenshots', { recursive: true });
const results = [];
const ok = (name, pass, detail) => {
  results.push({ name, pass, detail });
  console.log((pass ? 'PASS ' : 'FAIL ') + name + (detail ? '  | ' + detail : ''));
};

const stateExpr = `(() => {
  const doc = document.scrollingElement;
  const msg = document.getElementById('messages');
  const wrap = document.getElementById('composer-wrap');
  const col = document.getElementById('msg-col');
  return {
    tucked: wrap.classList.contains('tucked'),
    pad: parseFloat(col.style.paddingBottom) || 0,
    docScroll: doc.scrollHeight - doc.clientHeight,   // >0 = 文档级出现滚动条
    docScrollY: window.scrollY,
    msgDist: msg.scrollHeight - msg.scrollTop - msg.clientHeight,
    cwH: parseFloat(getComputedStyle(wrap).height) || 0,
  };
})()`;

async function seedChat(client, n, tag) {
  // 发 n 轮消息把会话拉长（mock 模型离线秒回）
  for (let i = 1; i <= n; i++) {
    const c = await center(client, '#input');
    await click(client, c.x, c.y);
    await typeText(client, `${tag} 第${i}条：` + '请多写几段内容便于滚动。'.repeat(3));
    const b = await center(client, '#btn-send');
    await click(client, b.x, b.y);
    await sleep(1600); // 等这轮跑完（mock 流式 + done）
  }
}

// ---------- A. 旧版（HEAD）----------
async function testOld() {
  const t = await newTab('http://127.0.0.1:8804/', 390, 844);
  try {
    await sleep(800);
    await seedChat(t, 3, '旧版');
    // 滚到底
    await read(t, `document.getElementById('messages').scrollTop = 1e9`);
    await sleep(300);
    let s = await read(t, stateExpr);
    ok('旧版·贴底基线（未收起、无文档滚动）', !s.tucked && s.docScroll === 0, JSON.stringify(s));
    // 小步上翻 ×8（旧版预期：近底边界触发收起→钳位→又展开 的翻转）
    const seen = [];
    for (let i = 0; i < 8; i++) {
      await wheel(t, 195, 300, -180, 1); // 每步 -180px（大于 -8 阈值，小于收起安全距）
      await sleep(160);
      seen.push((await read(t, stateExpr)).tucked ? 'T' : 't');
    }
    const bounce = seen.join('').includes('Tt');
    s = await read(t, stateExpr);
    await shot(t, '../gui-test-screenshots/tuck-old-after-wheelup.png');
    ok('旧版·上翻行为记录（对照）', true, '序列 ' + seen.join('') + ' 回跳 ' + (bounce ? '有' : '无') + '；末态 ' + JSON.stringify(s));
    // 远离底部后再看文档滚动（收起常态下）
    await wheel(t, 195, 300, -600, 4);
    await sleep(400);
    s = await read(t, stateExpr);
    await shot(t, '../gui-test-screenshots/tuck-old-docscroll.png');
    ok('旧版·双层滚动条复现（收起后文档级溢出 >0）', s.docScroll > 0, JSON.stringify(s));
  } finally { await closeTab(t); }
}

// ---------- B. 新版（工作区改动 + 延迟代理）----------
async function testNew() {
  const t = await newTab('http://127.0.0.1:8803/', 390, 844);
  try {
    await sleep(800);
    // T1 发送中占位：第一条走 3s 延迟代理
    let c = await center(t, '#input');
    await click(t, c.x, c.y);
    await typeText(t, '新版第一条：验证发送中占位气泡（经 3 秒延迟代理）');
    let b = await center(t, '#btn-send');
    await click(t, b.x, b.y);
    await sleep(1000); // 延迟窗口内
    let s1 = await read(t, `(() => ({
      pending: [...document.querySelectorAll('#msg-col .msg.user.pending-steer')].map(el => el.textContent.trim()),
      inputVal: document.getElementById('input').value,
      stopHidden: document.getElementById('btn-stop').classList.contains('hidden'),
    }))()`);
    await shot(t, '../gui-test-screenshots/tuck-new-pending.png');
    ok('新版·T1 延迟窗口内「发送中」气泡在、输入框已清', s1.pending.length === 1 && s1.pending[0].includes('发送中') && s1.inputVal === '' && !s1.stopHidden, JSON.stringify(s1));
    await sleep(5000); // 等回显 + mock 回复
    let s2 = await read(t, `(() => ({
      pending: [...document.querySelectorAll('#msg-col .msg.user.pending-steer')].length,
      userMsgs: [...document.querySelectorAll('#msg-col .msg.user')].map(el => el.textContent.trim().slice(0, 24)),
      asstMsgs: document.querySelectorAll('#msg-col .msg.assistant').length,
    }))()`);
    await shot(t, '../gui-test-screenshots/tuck-new-echoed.png');
    ok('新版·T1 回显落地：占位退场、真实用户消息在、模型有回复', s2.pending === 0 && s2.userMsgs.some(x => x.includes('验证发送中占位气泡')) && s2.asstMsgs >= 1, JSON.stringify(s2));

    // 拉长会话
    await seedChat(t, 3, '新版');
    await read(t, `document.getElementById('messages').scrollTop = 1e9`);
    await sleep(300);
    let s = await read(t, stateExpr);
    ok('新版·贴底基线（未收起、无文档滚动）', !s.tucked && s.docScroll === 0, JSON.stringify(s));

    // T3 同样的小步上翻 ×8：新版阈值 = pad+48，近底区不收起、不翻转
    const seen = [];
    for (let i = 0; i < 8; i++) {
      await wheel(t, 195, 300, -180, 1);
      await sleep(160);
      seen.push((await read(t, stateExpr)).tucked ? 'T' : 't');
    }
    // 抽搐 = 上翻过程中收起又展开（Tt 回跳）；单向进入收起（…tTTT）是正常行为
    const bounce = seen.join('').includes('Tt');
    await shot(t, '../gui-test-screenshots/tuck-new-after-wheelup.png');
    ok('新版·近底小步上翻无抽搐（无 Tt 回跳）', !bounce, '序列 ' + seen.join(''));

    // T2 远离底部：收起 + 文档不溢出
    await wheel(t, 195, 300, -600, 4);
    await sleep(400);
    s = await read(t, stateExpr);
    await shot(t, '../gui-test-screenshots/tuck-new-docscroll.png');
    ok('新版·收起常态：tucked 且文档级不溢出（无双层滚动条）', s.tucked && s.docScroll === 0, JSON.stringify(s));
    // 停 1.2s 再采样一次：确认稳定不回跳
    await sleep(1200);
    const s2b = await read(t, stateExpr);
    ok('新版·收起后 1.2s 仍稳定（无回跳）', s2b.tucked && s2b.docScroll === 0, JSON.stringify(s2b));

    // T4 滚回底部：展开
    await read(t, `document.getElementById('messages').scrollTop = 1e9`);
    await sleep(500);
    s = await read(t, stateExpr);
    await shot(t, '../gui-test-screenshots/tuck-new-backbottom.png');
    ok('新版·滚回底部输入区展开', !s.tucked && s.docScroll === 0, JSON.stringify(s));
  } finally { await closeTab(t); }
}

// ---------- C. 失败路（新版；打开代理故障开关模拟服务失联）----------
async function testFail() {
  // 先在正常态建会话发一条（保证 state.cur 存在），再拉闸
  const t = await newTab('http://127.0.0.1:8803/', 390, 844);
  try {
    await sleep(800);
    let c = await center(t, '#input');
    await click(t, c.x, c.y);
    await typeText(t, '失败路预热：先正常发一条建会话');
    let b = await center(t, '#btn-send');
    await click(t, b.x, b.y);
    await sleep(5500); // 3s 延迟 + mock 回复
    await fetch('http://127.0.0.1:8803/__chaos?set=on');
    c = await center(t, '#input');
    await click(t, c.x, c.y);
    await typeText(t, '这条应该失败：服务已失联');
    b = await center(t, '#btn-send');
    await click(t, b.x, b.y);
    await sleep(6000); // 3s 延迟 + 502 + backendRunning 探测（也 502）
    const s = await read(t, `(() => ({
      inputVal: document.getElementById('input').value,
      errShown: !document.getElementById('run-error').classList.contains('hidden'),
      errText: document.getElementById('run-error').textContent.trim().slice(0, 60),
      pending: document.querySelectorAll('#msg-col .msg.user.pending-steer').length,
    }))()`);
    await shot(t, '../gui-test-screenshots/tuck-new-fail-restore.png');
    ok('新版·失败路：内容回填输入框 + 报错条显示 + 占位退场',
      s.inputVal.includes('服务已失联') && s.errShown && s.pending === 0, JSON.stringify(s));
  } finally {
    await fetch('http://127.0.0.1:8803/__chaos?set=off').catch(() => {});
    await closeTab(t);
  }
}

const mode = process.argv[2] || 'all';
if (mode === 'old') await testOld();
else if (mode === 'new') await testNew();
else if (mode === 'fail') await testFail();
else { await testOld(); await testNew(); await testFail(); }

const fails = results.filter(r => !r.pass);
console.log(`\n==== ${results.length - fails.length}/${results.length} PASS ====`);
if (fails.length) { fails.forEach(f => console.log('FAILED: ' + f.name)); process.exit(1); }
