// T3：真实历史会话（多次 prompt/env 变更）的完成态渲染——变更行必须全部折进折叠段
import { newTab, read, click, shot, sleep } from './cdp.mjs';
import { resolve } from 'node:path';

const DIR = resolve(process.cwd(), '..', 'gui-test-screenshots');
const client = await newTab('http://127.0.0.1:9721/', 1280, 900);
await sleep(1500);

const t = await read(client, `(() => {
  const h = [...document.querySelectorAll('aside .s-title')].find(el => el.textContent.includes('代码核查'));
  if (!h) return null;
  const b = h.getBoundingClientRect();
  return { x: b.left + b.width / 2, y: b.top + b.height / 2 };
})()`);
if (!t) throw new Error('代码核查会话未找到');
await click(client, t.x, t.y);
await sleep(2000);

const audit = await read(client, `(() => {
  const col = document.getElementById('msg-col');
  const names = ['系统提示词变更','工具定义变更','运行环境更新','运行环境','工具定义'];
  const rows = [...col.querySelectorAll('.proc-row')].filter(r => names.includes((r.querySelector('.p-name')||{}).textContent));
  const outside = rows.filter(r => !r.closest('.tf-body'));
  const bars = [...col.querySelectorAll('.turn-fold')].map(b => b.textContent.trim());
  return {
    changeRows: rows.map(r => (r.querySelector('.p-name')||{}).textContent),
    outsideFoldNames: outside.map(r => (r.querySelector('.p-name')||{}).textContent),
    foldBarSamples: bars.slice(0, 12),
    foldBarCount: bars.length,
    matInBarCount: bars.filter(b => b.includes('上下文材料')).length,
  };
})()`);
console.log('[t3] audit =', JSON.stringify(audit, null, 2));
await shot(client, resolve(DIR, 't3_history.png'));
console.log('[t3] shot saved');
client.close();

const pass = audit.outsideFoldNames.length === 0 && audit.matInBarCount > 0;
console.log('[t3] RESULT:', pass ? 'PASS' : 'FAIL');
if (!pass) process.exit(2);
