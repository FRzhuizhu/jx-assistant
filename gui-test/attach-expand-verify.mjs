// 附件区「卡内向上展开」端到端验证（隔离实例 8898，模型地址不可达，仅前端行为）。
// 背景：附件区曾于 bb4a2a5194 被改成输入卡上方的独立白底条带（与卡断开、间距里
// 漏页面底色，难看）；本次改回「输入卡内部第一层」——有附件时白卡自身向上长高，
// 与下方输入区连为一体；没附件时 hidden + :empty 双保险零占位（不留白边）。
// 用例：
//   A 空态（桌面）：轨道在卡内、display:none、零高度，composer-area 不比卡高
//   B 注入附件（图片+文件）：轨道展开在卡内顶部、无独立条带样式（无左右上边框/
//     无圆角/无自身底色）、有底部分隔线、卡片增高
//   C 逐个移除：回到零占位、卡高回落
//   D 空态（手机 390×844）：同样零占位（手机端「多一条白边」回归验证）
//   E 注入附件（手机）：卡内展开
// 运行：仓库根目录 node gui-test/attach-expand-verify.mjs
import { newTab, read, shot, click, center, sleep, closeTab } from './cdp.mjs';
import { mkdirSync } from 'node:fs';

mkdirSync('./gui-test-screenshots', { recursive: true });
const results = [];
const ok = (name, pass, detail) => {
  results.push({ name, pass, detail });
  console.log((pass ? 'PASS ' : 'FAIL ') + name + (detail ? '  | ' + detail : ''));
};
const BASE = 'http://127.0.0.1:8898';

// 页面里现造两张附件（canvas 出真 PNG）并 stageFiles 入列
const STAGE = `(() => new Promise(res => {
  const c = document.createElement('canvas'); c.width = 96; c.height = 96;
  const g = c.getContext('2d');
  g.fillStyle = '#4d6bfe'; g.fillRect(0, 0, 96, 96);
  g.fillStyle = '#fff'; g.font = 'bold 34px sans-serif'; g.fillText('图', 30, 60);
  c.toBlob(b => {
    const f1 = new File([b], '截图素材.png', { type: 'image/png' });
    const f2 = new File([new Uint8Array(2048).fill(65)], '季度报告.docx', { type: '' });
    stageFiles([f1, f2], mainView);
    res(true);
  }, 'image/png');
}))()`;

const PROBE = `(() => {
  const rail = document.getElementById('attachments');
  const card = document.getElementById('composer');
  const cs = rail.classList.contains('hidden') ? null : getComputedStyle(rail);
  const cr = card.getBoundingClientRect(), rr = rail.getBoundingClientRect();
  // 首页「新对话」在卡上方还有工作模式/工作目录胶囊（#draft-chips，属合法占位，
  // 与附件区无关）——areaH 的预期值要把它的「高 + 下边距」扣掉再跟卡高比
  const chips = document.getElementById('draft-chips');
  const chipsH = chips.offsetHeight + (parseFloat(getComputedStyle(chips).marginBottom) || 0);
  return {
    hiddenCls: rail.classList.contains('hidden'),
    parent: rail.parentElement && rail.parentElement.id,
    display: cs ? cs.display : getComputedStyle(rail).display,
    railH: rail.offsetHeight,
    cardH: Math.round(cr.height),
    areaH: Math.round(document.getElementById('composer-area').offsetHeight),
    chipsH: Math.round(chipsH),
    items: rail.querySelectorAll('.attach-item').length,
    inCard: !cs ? null : (rr.top >= cr.top - 0.5 && rr.bottom <= cr.bottom + 0.5 && rr.left >= cr.left - 0.5),
    borderTop: cs ? cs.borderTopWidth : null, borderLeft: cs ? cs.borderLeftWidth : null,
    borderBottom: cs ? cs.borderBottomWidth : null,
    radius: cs ? cs.borderRadius : null,
    bg: cs ? cs.backgroundColor : null,
    marginTop: cs ? cs.marginTop : null,
  };
})()`;

async function main() {
  const t = await newTab(BASE + '/', 1280, 900);
  try {
    await sleep(1200);

    // ---------- A 空态（桌面）：零占位 ----------
    let st = await read(t, PROBE);
    ok('A1 轨道挂在输入卡内部', st.parent === 'composer', 'parent=' + st.parent);
    ok('A2 空态带 hidden 且 display:none', st.hiddenCls === true && st.display === 'none');
    ok('A3 零高度：区域高度=胶囊+卡片，无多余空隙', st.railH === 0 && st.areaH === st.chipsH + st.cardH,
       'railH=' + st.railH + ' areaH=' + st.areaH + ' chipsH=' + st.chipsH + ' cardH=' + st.cardH);
    const emptyCardH = st.cardH;
    await shot(t, './gui-test-screenshots/attach-a-empty-desktop.png');

    // ---------- B 注入附件：卡内向上展开 ----------
    await read(t, STAGE);
    await sleep(400);
    st = await read(t, PROBE);
    ok('B1 两枚附件入列', st.items === 2, 'items=' + st.items);
    ok('B2 轨道显示（去 hidden、flex）', st.hiddenCls === false && st.display === 'flex');
    ok('B3 轨道几何在卡片内部（连为一体）', st.inCard === true,
       'rail top/bottom/left 均落在卡矩形内');
    ok('B4 无独立条带样式：上/左边框 0、圆角 0、自身无底色',
       st.borderTop === '0px' && st.borderLeft === '0px' &&
       (st.radius === '0px' || st.radius === '0px 0px 0px 0px') &&
       (st.bg === 'rgba(0, 0, 0, 0)' || st.bg === 'transparent'),
       'borderTop=' + st.borderTop + ' borderLeft=' + st.borderLeft + ' radius=' + st.radius + ' bg=' + st.bg);
    ok('B5 与下方输入区有分隔线', st.borderBottom === '1px', 'borderBottom=' + st.borderBottom);
    ok('B6 白卡自身向上长高（卡片增高且左沿不变右沿不变仅变高）', st.cardH > emptyCardH + 30,
       'cardH=' + st.cardH + '（空态 ' + emptyCardH + '）');
    await shot(t, './gui-test-screenshots/attach-b-staged-desktop.png');

    // ---------- C 逐个移除：回到零占位 ----------
    let p = await center(t, '#attachments .attach-item .attach-x');
    await click(t, p.x, p.y); await sleep(250);
    st = await read(t, PROBE);
    ok('C1 移除一张后剩一张', st.items === 1, 'items=' + st.items);
    p = await center(t, '#attachments .attach-item .attach-x');
    await click(t, p.x, p.y); await sleep(250);
    st = await read(t, PROBE);
    ok('C2 移除后回到零占位', st.hiddenCls === true && st.display === 'none' && st.areaH === st.chipsH + st.cardH,
       'areaH=' + st.areaH + ' chipsH=' + st.chipsH + ' cardH=' + st.cardH);
    ok('C3 卡高回落到空态值', Math.abs(st.cardH - emptyCardH) <= 1,
       'cardH=' + st.cardH + ' vs 空态 ' + emptyCardH);
    await shot(t, './gui-test-screenshots/attach-c-removed-desktop.png');

    // ---------- D 手机 390×844：空态零占位（白边回归验证） ----------
    await t.send('Emulation.setDeviceMetricsOverride',
      { width: 390, height: 844, deviceScaleFactor: 2, mobile: true });
    await sleep(400);
    st = await read(t, PROBE);
    ok('D1 手机空态同样零占位', st.hiddenCls === true && st.display === 'none' && st.areaH === st.chipsH + st.cardH,
       'areaH=' + st.areaH + ' chipsH=' + st.chipsH + ' cardH=' + st.cardH);
    await shot(t, './gui-test-screenshots/attach-d-empty-mobile.png');

    // ---------- E 手机注入附件：卡内展开 ----------
    await read(t, STAGE);
    await sleep(400);
    st = await read(t, PROBE);
    ok('E1 手机端卡内展开（轨道在卡内、有分隔线、无独立底色）',
       st.inCard === true && st.borderBottom === '1px' && st.items === 2);
    await shot(t, './gui-test-screenshots/attach-e-staged-mobile.png');

    const fail = results.filter(r => !r.pass).length;
    console.log(fail ? ('\n' + fail + ' FAILED') : '\nALL PASS');
    process.exitCode = fail ? 1 : 0;
  } finally {
    await closeTab(t);
  }
}
main().catch(e => { console.error(e); process.exit(1); });
