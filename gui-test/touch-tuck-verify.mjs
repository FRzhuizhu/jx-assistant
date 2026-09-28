// 手机端触摸滚动复现探针：CDP Input.dispatchTouchEvent 模拟「手指上滑回看历史」的
// 真实手势（拖动 + 抬起后惯性），采样 tucked 类翻转（抽搐）、滚动几何、以及输入卡
// 上方区域的元素/背景构成（白边定位）。窄屏 390×844 直连 8802（无代理延迟）。
import { newTab, read, click, typeText, shot, sleep, closeTab } from './cdp.mjs';
import { mkdirSync } from 'node:fs';

mkdirSync('../gui-test-screenshots', { recursive: true });

async function touchSwipe(t, x, y0, y1, steps) {
  // 手指从 y0 拖到 y1（上滑看历史 = 手指下移），抬起结束（无额外惯性，拖多少滚多少）
  await t.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y: y0 }] });
  for (let i = 1; i <= steps; i++) {
    const y = y0 + (y1 - y0) * (i / steps);
    await t.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x, y }] });
    await sleep(16);
  }
  await t.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
}

const t = await newTab('http://127.0.0.1:8802/', 390, 844);
try {
  await sleep(900);
  // 拉长对话：发几轮（mock 秒回）
  for (let i = 1; i <= 4; i++) {
    const c = await read(t, `(() => { const b = document.getElementById('input').getBoundingClientRect(); return { x: b.left + 40, y: b.top + 20 }; })()`);
    await click(t, c.x, c.y);
    await typeText(t, '触摸滚动测试第' + i + '轮：' + '这是一段用来撑高对话历史的内容。'.repeat(4));
    const btn = await read(t, `(() => { const b = document.getElementById('btn-send').getBoundingClientRect(); return { x: b.left + b.width/2, y: b.top + b.height/2 }; })()`);
    await click(t, btn.x, btn.y);
    await sleep(2600);
  }
  // 滚到底
  await read(t, `document.getElementById('messages').scrollTop = 1e9`);
  await sleep(400);

  const stateExpr = `(() => {
    const el = document.getElementById('messages');
    const wrap = document.getElementById('composer-wrap');
    const col = document.getElementById('msg-col');
    return {
      tucked: wrap.classList.contains('tucked'),
      dist: Math.round(el.scrollHeight - el.scrollTop - el.clientHeight),
      pad: col.style.paddingBottom,
      cwH: (typeof mainView !== "undefined" && mainView._cwH) ?? null,
      docScroll: document.scrollingElement.scrollHeight - document.scrollingElement.clientHeight,
    };
  })()`;

  console.log('基线(贴底):', JSON.stringify(await read(t, stateExpr)));
  await shot(t, '../gui-test-screenshots/touch-0-bottom.png');

  // —— 切到深色主题再测（用户看到的白边大概率在深色下；--bg 浅色=纯白看不出）
  await read(t, `(() => { try { localStorage.setItem('jx.theme', 'dark'); } catch (e) {} document.documentElement.dataset.theme = 'dark'; return 1; })()`);
  await sleep(300);
  await shot(t, '../gui-test-screenshots/touch-0-dark.png');

  // —— 输入卡上方区域的构成（白边定位）：列出卡片上沿以上 90px 内的可见元素与背景
  const bandExpr = `(() => {
    const card = document.getElementById('composer');
    const cb = card.getBoundingClientRect();
    const y = cb.top;
    const hits = [];
    for (const dy of [-72, -48, -24, -8]) {
      const el = document.elementFromPoint(195, y + dy);
      if (!el) { hits.push({ dy, el: 'null' }); continue; }
      const cs = getComputedStyle(el);
      hits.push({ dy, el: el.tagName + (el.id ? '#' + el.id : '') + (typeof el.className === 'string' && el.className ? '.' + el.className.split(' ')[0] : ''), bg: cs.backgroundColor });
    }
    const wrapCs = getComputedStyle(document.getElementById('composer-wrap'));
    const before = getComputedStyle(document.getElementById('composer-wrap'), '::before');
    return {
      cardTop: Math.round(y),
      hits,
      wrapBg: wrapCs.backgroundColor,
      beforeBg: before.backgroundImage.slice(0, 60),
      beforeH: before.height,
      bodyBg: getComputedStyle(document.body).backgroundColor,
      msgColBg: getComputedStyle(document.getElementById('msg-col')).backgroundColor,
      bodyBgVar: getComputedStyle(document.documentElement).getPropertyValue('--bg').trim(),
    };
  })()`;
  console.log('卡片上方构成:', JSON.stringify(await read(t, bandExpr), null, 1));

  // —— 触摸上滑 ×3（真实手势），期间 100ms 采样 tucked 序列
  for (let round = 1; round <= 3; round++) {
    const seq = [];
    await touchSwipe(t, 195, 300, 460, 12); // 手指下移 160px = 内容上翻
    for (let i = 0; i < 30; i++) {
      await sleep(100);
      const s = await read(t, stateExpr);
      seq.push((s.tucked ? 'T' : 't') + s.dist);
      if (i > 8 && seq.slice(-6).every((v, j, a) => v === a[0])) break; // 已稳定
    }
    console.log('上滑第' + round + '轮采样:', seq.join(' '));
    await shot(t, '../gui-test-screenshots/touch-' + round + '-after-swipeup.png');
  }

  // —— 滚回底部触发展开，抓展开瞬间的动画帧（疑似白边闪现）
  await read(t, `document.getElementById('messages').scrollTop = 1e9`);
  await sleep(80);
  await shot(t, '../gui-test-screenshots/touch-untuck-anim.png');
  await sleep(600);
  await shot(t, '../gui-test-screenshots/touch-untuck-settled.png');
  console.log('展开后:', JSON.stringify(await read(t, stateExpr)));
} finally { await closeTab(t); }
