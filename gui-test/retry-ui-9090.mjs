// 自动重试 UI 验证（隔离实例 9090，模型地址不可达）：
// 发送消息 → 观察 hint 出现「网络异常…第 X/5 次」提示（间隔 1/2/3/4s）
// → 耗尽后报错条出现、hint 被清除。输出观察时间线与截图。
import { newTab, read, sleep, key, typeText, shot, closeTab } from './cdp.mjs';

const base = process.env.JX_BASE || 'http://127.0.0.1:9090';
const outShot = process.env.JX_SHOT || './gui-test-screenshots/retry_9090_final.png';

const client = await newTab(base);
try {
  await read(client, `(() => { const el = document.querySelector('#input'); el.focus(); return true; })()`);
  await typeText(client, '自动重试测试');
  await sleep(300);
  await key(client, 'Enter');

  const seen = [];
  let lastHint = '';
  let lastErr = '';
  const t0 = Date.now();
  for (let i = 0; i < 90; i++) { // 最多 18 秒
    await sleep(200);
    const st = await read(client, `(() => {
      const h = document.querySelector('#hint');
      const e = document.querySelector('#run-error');
      return {
        hint: h ? h.textContent : '',
        err: e && !e.classList.contains('hidden') ? e.textContent : '',
      };
    })()`);
    if (st.hint && st.hint !== lastHint) { lastHint = st.hint; seen.push({ t: Date.now() - t0, hint: st.hint }); }
    if (st.err && st.err !== lastErr) { lastErr = st.err; seen.push({ t: Date.now() - t0, err: st.err }); break; }
  }
  const path = await shot(client, outShot);
  console.log('TIMELINE:' + JSON.stringify(seen, null, 1));
  console.log('SHOT:' + path);
} finally {
  await closeTab(client);
}
