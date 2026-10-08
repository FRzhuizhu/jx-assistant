// 引用胶囊端到端验证（隔离实例 8899，baseURL=mock，data 隔离目录）。
// 用例：
//   A setQuote 挂引用 → 胶囊出现、显示“前10字…、双引号包裹”、title 含全文；
//   B ✕ 移除引用 → 胶囊隐藏、state.quote 清空；
//   C 再次挂引用 + 输入正文 + 发送（mock）→ 会话历史里消息 content 为
//     「用户引用：“完整文本”\n\n正文」（模型收到完整引用）；气泡里引用段渲染为
//     .msg-quote 灰色行（截断文本）；
//   D 发送后胶囊退场、输入框清空；localStorage 草稿 key 清掉。
// 运行：仓库根目录 node gui-test/quote-verify.mjs
import { newTab, read, typeText, key, shot, click, center, sleep, closeTab }
  from './cdp.mjs';
import { mkdirSync } from 'node:fs';

mkdirSync('./gui-test-screenshots', { recursive: true });
const results = [];
const ok = (name, pass, detail) => {
  results.push({ name, pass, detail });
  console.log((pass ? 'PASS ' : 'FAIL ') + name + (detail ? '  | ' + detail : ''));
};
const BASE = 'http://127.0.0.1:8899';
const LONG = '这是一段用来验证引用胶囊显示与发送组装的较长文本内容，超过十个字符';

async function main() {
  const t = await newTab(BASE + '/', 1280, 900);
  try {
    await sleep(1500);

    // ---------- A 挂引用：胶囊出现与显示形态 ----------
    await read(t, `(() => { setQuote(mainView, ${JSON.stringify(LONG)}); return true; })()`);
    await sleep(300);
    let st = await read(t, `(() => {
      const bar = document.getElementById('quote-bar');
      const tx = bar.querySelector('.q-text');
      return {
        hidden: bar.classList.contains('hidden'),
        text: tx.textContent,
        title: bar.title,
        state: state.quote,
        inputFocused: document.activeElement === document.getElementById('input'),
      };
    })()`);
    ok('A1 胶囊出现', st.hidden === false, 'hidden=' + st.hidden);
    ok('A2 显示截断+双引号', st.text === '“这是一段用来验证引用胶囊显示…',
      JSON.stringify(st.text));
    ok('A3 title 含完整引用', st.title.includes(LONG), 'title len=' + st.title.length);
    ok('A4 state.quote 为全文', st.state === LONG, 'len=' + (st.state || '').length);
    ok('A5 输入框获得焦点', st.inputFocused === true);
    await shot(t, './gui-test-screenshots/quote-a-pill.png');

    // ---------- B ✕ 移除 ----------
    const x = await center(t, '#quote-bar .q-x');
    await click(t, x.x, x.y);
    await sleep(300);
    st = await read(t, `(() => ({
      hidden: document.getElementById('quote-bar').classList.contains('hidden'),
      q: state.quote,
    })()`);
    ok('B1 ✕ 后胶囊隐藏', st.hidden === true);
    ok('B2 ✕ 后 state 清空', st.q === '');

    // ---------- C 挂引用 + 正文 + 发送 ----------
    await read(t, `(() => { setQuote(mainView, ${JSON.stringify(LONG)}); return true; })()`);
    await sleep(200);
    await read(t, `(() => { document.getElementById('input').focus(); return 1; })()`);
    await typeText(t, '请结合引用内容回答');
    await sleep(200);
    await key(t, 'Enter');

    // 等 mock 回合完成：消息回显 + 气泡渲染
    let bubble = null, sid = null;
    for (let i = 0; i < 40; i++) {
      await sleep(300);
      const r = await read(t, `(() => {
        const b = document.querySelector('#msg-col .msg.user .bubble');
        return {
          hasQuoteBar: b ? !!b.querySelector('.msg-quote') : false,
          quoteText: b && b.querySelector('.msg-quote') ? b.querySelector('.msg-quote').textContent : '',
          bodyText: b ? b.textContent : '',
          sid: state.cur ? state.cur.id : '',
        };
      })()`);
      if (r.hasQuoteBar) { bubble = r; sid = r.sid; break; }
    }
    ok('C1 用户气泡渲染出引用行', !!bubble, bubble ? '' : '20s 内未出现');
    if (bubble) {
      ok('C2 气泡引用行截断+双引号', bubble.quoteText === '“这是一段用来验证引用胶囊显示…',
        JSON.stringify(bubble.quoteText));
      ok('C3 气泡正文存在', bubble.bodyText.includes('请结合引用内容回答'), '');
    }
    await shot(t, './gui-test-screenshots/quote-c-sent.png');

    // 服务端视角：模型收到的 content 是完整引用
    st = await read(t, `fetch('/api/sessions/' + ${'sid'} ).then(r=>r.json())`.replace('${' + 'sid}', JSON.stringify(sid || '')));
    const msgs = (st && st.messages) || [];
    const mine = msgs.filter((m) => m.role === 'user');
    const want = '用户引用：“' + LONG + '”\n\n请结合引用内容回答';
    ok('C4 服务端消息=完整引用+正文', mine.some((m) => String(m.content) === want),
      'user msgs=' + mine.length + ' first=' + JSON.stringify(String((mine[0] || {}).content || '').slice(0, 60)));

    // ---------- D 发送后清理 ----------
    st = await read(t, `(() => ({
      hidden: document.getElementById('quote-bar').classList.contains('hidden'),
      input: document.getElementById('input').value,
      q: state.quote,
      lsKey: Object.keys(localStorage).filter((k) => k.indexOf('jx.q.') === 0 || k.indexOf('jx.subq.') === 0),
    })()`);
    ok('D1 发送后胶囊退场', st.hidden === true);
    ok('D2 发送后输入框清空', st.input === '', JSON.stringify(st.input));
    ok('D3 发送后 state.quote 清空', st.q === '');
    ok('D4 引用草稿 key 已清', st.lsKey.length === 0, JSON.stringify(st.lsKey));

    // ---------- E 草稿持久化：挂引用不发送，刷新页面后回来 ----------
    await read(t, `(() => { setQuote(mainView, ${JSON.stringify(LONG)}); return true; })()`);
    await sleep(400); // 落盘
    await t.send('Page.navigate', { url: BASE + '/' });
    await sleep(1800);
    st = await read(t, `(() => ({
      hidden: document.getElementById('quote-bar').classList.contains('hidden'),
      text: document.querySelector('#quote-bar .q-text').textContent,
    })()`);
    // 刷新后回到首页（无会话上下文），首页引用应从 homeDraft 恢复
    ok('E1 刷新后首页胶囊恢复', st.hidden === false, JSON.stringify(st));
    ok('E2 刷新后截断文本一致', st.text === '“这是一段用来验证引用胶囊显示…', JSON.stringify(st.text));
    await shot(t, './gui-test-screenshots/quote-e-restore.png');
  } finally {
    await closeTab(t);
  }
  const fails = results.filter((r) => !r.pass).length;
  console.log(fails ? `\n${fails} FAILED` : '\nALL PASS');
  process.exit(fails ? 1 : 0);
}

main().catch((e) => { console.error('ERROR', e); process.exit(1); });
