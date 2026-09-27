// 打开主实例的中断验证会话并截图（确认前端工具卡片渲染补齐的中断结尾）。
import { newTab, read, sleep, shot, closeTab } from './cdp.mjs';

const sid = process.env.JX_SID;
if (!sid) { console.error('need JX_SID'); process.exit(1); }
const client = await newTab('http://127.0.0.1:8799');
try {
  await sleep(1500); // 等应用加载
  await read(client, `openSession(${JSON.stringify(sid)})`);
  await sleep(1500); // 等消息渲染
  // 展开含「工具调用」的折叠段（看工具卡片里的中断结尾文案）
  await read(client, `(() => {
    const bars = [...document.querySelectorAll('.turn-fold')];
    const bar = bars.find((b) => b.textContent.includes('工具调用'));
    if (bar) bar.click();
    return !!bar;
  })()`);
  await sleep(800);
  const path = await shot(client, './gui-test-screenshots/interrupt_verify_expanded.png');
  console.log('SHOT:' + path);
} finally {
  await closeTab(client);
}
