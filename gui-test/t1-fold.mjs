// T1：材料行（系统提示词/工具定义/运行环境）应折进思考/工具折叠段
// 步骤：选 mock 模型 → 发消息 → 等回合完成 → DOM 断言 + 截图
import { newTab, read, click, key, typeText, shot, center, sleep } from './cdp.mjs';

const BASE = 'http://127.0.0.1:9721';
const SHOT_DIR = new URL('.', import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1') + '../gui-test-screenshots/';

const client = await newTab(BASE + '/');
console.log('[t1] page opened, title =', await read(client, 'document.title'));

// 1) 打开模型选择弹层
let c = await center(client, '#model-chip');
await click(client, c.x, c.y);
await sleep(400);
const popInfo = await read(client, `(() => {
  const pop = document.getElementById('model-pop');
  if (!pop || pop.classList.contains('hidden')) return { open: false };
  const items = [...pop.querySelectorAll('*')].filter(el => el.textContent.includes('mock-1') && el.children.length <= 2);
  const target = items.find(el => el.textContent.trim().startsWith('mock-1')) || items[0];
  if (!target) return { open: true, found: false };
  const b = target.getBoundingClientRect();
  return { open: true, found: true, x: b.left + b.width / 2, y: b.top + b.height / 2,
           tag: target.tagName, cls: target.className, label: target.textContent.trim().slice(0, 40) };
})()`);
console.log('[t1] model popover:', JSON.stringify(popInfo));
if (!popInfo.open || !popInfo.found) throw new Error('mock-1 选项未找到');

// 2) 选 mock-1
await click(client, popInfo.x, popInfo.y);
await sleep(300);
const chipNow = await read(client, `document.getElementById('model-chip').textContent.trim()`);
console.log('[t1] model chip now =', chipNow);
if (!chipNow.includes('mock-1')) throw new Error('模型未切换为 mock-1');

// 3) 填入消息并发送
c = await center(client, '#input');
await click(client, c.x, c.y);
await typeText(client, '折叠与滚动回归测试第一轮');
await sleep(150);
await key(client, 'Enter');
console.log('[t1] message sent, waiting turn to finish...');

// 4) 等回合结束（发送按钮的停止态消失 / 输入框清空 + 出现完成正文）
await sleep(1500);
for (let i = 0; i < 40; i++) {
  const running = await read(client, `!document.getElementById('btn-stop').classList.contains('hidden') || (window.state && state.streaming)`);
  if (!running) break;
  await sleep(500);
}
await sleep(800);

// 5) DOM 断言：材料行都在 .tf-body 折叠容器内；折叠条标签含材料计数
const audit = await read(client, `(() => {
  const col = document.getElementById('msg-col');
  const matRows = [...col.querySelectorAll('.proc-row')].filter(r =>
    ['系统提示词已拼装','系统提示词变更','工具定义','工具定义变更','运行环境','运行环境更新']
      .some(t => (r.querySelector('.p-name')||{}).textContent === t));
  const inFold = matRows.filter(r => r.closest('.tf-body'));
  const bars = [...col.querySelectorAll('.turn-fold')].map(b => b.textContent.trim());
  const visibleMat = matRows.filter(r => !r.closest('.tf-body') && !r.closest('.proc-row.open'));
  // 顶层直接可见（不在任何 tf-body 里）的行名
  const topLevel = [...col.children].map(ch => ch.className || ch.tagName);
  return {
    matTotal: matRows.length,
    matFolded: inFold.length,
    matVisibleOutside: matRows.length - inFold.length,
    visibleMatNames: visibleMat.map(r => (r.querySelector('.p-name')||{}).textContent),
    foldBars: bars,
    topLevel,
  };
})()`);
console.log('[t1] audit =', JSON.stringify(audit, null, 2));

await shot(client, SHOT_DIR + 't1_after_turn.png');
console.log('[t1] screenshot saved');
client.close();
