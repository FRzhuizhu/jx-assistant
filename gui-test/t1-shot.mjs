// T1 补拍：打开刚才的 mock 会话，截图验证视觉效果
import { newTab, read, click, shot, center, sleep } from './cdp.mjs';
import { resolve } from 'node:path';

const BASE = 'http://127.0.0.1:9721';
const DIR = resolve(process.cwd(), '..', 'gui-test-screenshots');

const client = await newTab(BASE + '/');
await sleep(800);

// 侧栏最新会话（自动标题来自首条消息）
const target = await read(client, `(() => {
  const items = [...document.querySelectorAll('#sess-list .sess-item, #sess-list [class*=sess]')]
    .filter(el => el.textContent.includes('折叠与滚动回归'));
  if (!items.length) return null;
  const b = items[0].getBoundingClientRect();
  return { x: b.left + b.width / 2, y: b.top + b.height / 2 };
})()`);
if (!target) throw new Error('测试会话未在侧栏找到');
await click(client, target.x, target.y);
await sleep(1200);

const audit = await read(client, `(() => {
  const col = document.getElementById('msg-col');
  const bars = [...col.querySelectorAll('.turn-fold')].map(b => b.textContent.trim());
  const matRows = [...col.querySelectorAll('.proc-row')].filter(r =>
    ['运行环境','工具定义','运行环境更新','系统提示词变更','工具定义变更']
      .includes((r.querySelector('.p-name')||{}).textContent));
  return { foldBars: bars, matOutsideFold: matRows.filter(r => !r.closest('.tf-body')).length };
})()`);
console.log('[t1-shot] audit =', JSON.stringify(audit));

await shot(client, resolve(DIR, 't1_after_turn.png'));
console.log('[t1-shot] saved:', resolve(DIR, 't1_after_turn.png'));
client.close();
