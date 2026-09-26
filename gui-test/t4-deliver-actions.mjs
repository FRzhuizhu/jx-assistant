// T4：① deliver_result 交付卡片当轮实时出现（不重开会话）+ 文件改动面板
//     ② 操作按钮只在最终输出下；压缩按钮带图标
import { newTab, read, click, key, typeText, shot, center, sleep } from './cdp.mjs';
import { resolve } from 'node:path';

const BASE = 'http://127.0.0.1:9721';
const DIR = resolve(process.cwd(), '..', 'gui-test-screenshots');

const client = await newTab(BASE + '/', 1280, 820);
await sleep(1200);

// 选 mock 模型
let c = await center(client, '#model-chip');
await click(client, c.x, c.y);
await sleep(400);
const opt = await read(client, `(() => {
  const pop = document.getElementById('model-pop');
  if (!pop || pop.classList.contains('hidden')) return null;
  const el = [...pop.querySelectorAll('button, [role=button], div')].find(e => e.textContent.trim().startsWith('mock-1') && e.children.length <= 2);
  if (!el) return null;
  const b = el.getBoundingClientRect();
  return { x: b.left + b.width / 2, y: b.top + b.height / 2 };
})()`);
if (!opt) throw new Error('mock-1 选项未找到');
await click(client, opt.x, opt.y);
await sleep(300);

// 发消息（mock 首轮：write_file → run_command → deliver_result → 总结文本）
c = await center(client, '#input');
await click(client, c.x, c.y);
await typeText(client, '交付卡片与操作按钮回归测试');
await sleep(120);
await key(client, 'Enter');
console.log('[t4] sent, waiting for turn...');

// 中途采样：deliver_result 落库后卡片应即时出现（不等回合收尾）
let midCard = false;
for (let i = 0; i < 24; i++) {
  await sleep(300);
  midCard = await read(client, `!!document.querySelector('#msg-col .delivery-card')`);
  const running = await read(client, `!document.getElementById('btn-stop').classList.contains('hidden') || state.streaming`);
  if (midCard && !running) break;
  if (!running && i > 6) break;
}

// 等回合完全结束
for (let i = 0; i < 40; i++) {
  const running = await read(client, `!document.getElementById('btn-stop').classList.contains('hidden') || state.streaming`);
  if (!running) break;
  await sleep(400);
}
await sleep(1200); // 收尾 reloadMessages 落地

const audit = await read(client, `(() => {
  const col = document.getElementById('msg-col');
  const card = col.querySelector('.delivery-card');
  const fp = col.querySelector('.file-panel');
  const texts = [...col.querySelectorAll('.msg.assistant')].map(m => ({
    hasActions: !!m.querySelector('.msg-actions'),
    text: (m.querySelector('.md')||{}).innerText?.slice(0, 18),
  }));
  const acts = col.querySelector('.msg-actions');
  const compact = acts ? acts.querySelector('.act-compact') : null;
  return {
    deliveryCard: !!card,
    cardText: card ? card.innerText.replace(/\\n/g, ' | ').slice(0, 120) : null,
    filePanel: !!fp,
    filePanelLabel: fp ? (fp.querySelector('.p-name')||{}).textContent : null,
    assistantTexts: texts,
    compactHasIcon: compact ? !!compact.querySelector('svg') : false,
    compactText: compact ? compact.textContent.trim() : null,
    copyHasIcon: acts ? !!acts.querySelector('.act-copy svg') : false,
    branchPresent: acts ? !!acts.querySelector('.act-branch') : false,
  };
})()`);
console.log('[t4] mid-turn card appeared:', midCard);
console.log('[t4] audit =', JSON.stringify(audit, null, 2));
await shot(client, resolve(DIR, 't4_delivery_actions.png'));

// 判定
const lastIdx = audit.assistantTexts.length - 1;
const onlyLastHasActions = audit.assistantTexts.every((t, i) => t.hasActions === (i === lastIdx));
const results = {
  交付卡片当轮出现: audit.deliveryCard,
  交付卡片含下载: !!(audit.cardText && audit.cardText.includes('下载')),
  文件改动面板出现: audit.filePanel,
  仅最终输出带按钮: onlyLastHasActions && lastIdx >= 0,
  压缩按钮有图标: audit.compactHasIcon,
  分支按钮在: audit.branchPresent,
};
console.log('[t4] RESULTS:', JSON.stringify(results, null, 2));
client.close();
if (Object.values(results).some((v) => !v)) process.exit(2);
