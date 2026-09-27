// 验证问题2修复（窄视口 390x844，独立测试实例 3081）：
// A 输入 Key 后触发重渲染不丢（回填）→ B 收起后草稿卡常驻 → C 重新展开 Key 仍在
// D 新卡片选预置「智谱 GLM」保存应 200（修复前 400）→ E 拉取模型链路正常（假 Key 预期 401/502）
// 运行：仓库根目录 node gui-test/mtfix-verify.mjs
import { newTab, sleep, read, click, typeText, shot, center, closeTab } from './cdp.mjs';

const BASE = 'http://127.0.0.1:3081';
const W = 390, H = 844;

function log(...a) { console.log(...a); }

async function setHooks(c) {
  await read(c, `(() => {
    window.__alerts = []; window.__fetchLog = []; window.__errs = [];
    window.onerror = (m, s, l, col, e) => { window.__errs.push(String(m) + ' @' + l + ':' + col); };
    window.alert = (m) => { window.__alerts.push(String(m)); };
    window.confirm = () => true;
    const of = window.fetch;
    window.fetch = async (u, opt) => {
      const rec = { url: String(u), method: (opt && opt.method) || 'GET', body: (opt && opt.body) || null, status: 0, resp: null };
      window.__fetchLog.push(rec);
      try {
        const r = await of(u, opt);
        rec.status = r.status;
        try { rec.resp = (await r.clone().text()).slice(0, 260); } catch (e) {}
        return r;
      } catch (e) { rec.status = -1; rec.resp = String(e); throw e; }
    };
    return 1;
  })()`);
}

// 滚动到元素 → 读中心 → 真实点击
async function clickSel(c, sel, scroll = true) {
  if (scroll) {
    await read(c, `(() => { const el = document.querySelector(${JSON.stringify(sel)}); if (el) el.scrollIntoView({ block: 'center' }); return 1; })()`);
    await sleep(250);
  }
  const pt = await center(c, sel);
  await click(c, pt.x, pt.y);
  return pt;
}

// 列表最后一张卡片（草稿卡）状态。注意：专家卡也用了 .prov-card 类，
// 必须限定在 #prov-list 内查询，否则选到专家卡
const LAST = `(() => {
  const cards = [...document.querySelectorAll('#prov-list .prov-card')];
  const last = cards[cards.length - 1];
  const inp = last ? last.querySelector('.key-row input') : null;
  return {
    n: cards.length,
    open: last ? last.classList.contains('open') : null,
    keyVal: inp ? inp.value : null,
    dirty: last ? !!last.querySelector('.pv-dirty') : null,
    head: last && last.querySelector('.pv-head') ? last.querySelector('.pv-head').textContent : '',
    expURL: (() => { const f = last ? last.querySelector('.pf-fixed') : null; return f ? f.textContent : null; })(),
  };
})()`;

async function main() {
  const c = await newTab(BASE, W, H);
  try {
    await setHooks(c);
    await sleep(1200);

    // 进设置 → 模型页
    await clickSel(c, '#btn-sidebar'); await sleep(600);
    await clickSel(c, '#btn-settings'); await sleep(900);
    await clickSel(c, '.set-nav-item[data-pane="models"]'); await sleep(500);

    // ===== A 添加提供方 → 选「智谱 GLM」预置 → 输入 Key =====
    await clickSel(c, '#prov-add-card'); await sleep(500);
    await read(c, `(() => {
      const cards = [...document.querySelectorAll('#prov-list .prov-card')];
      const sel = cards[cards.length - 1].querySelector('select');
      sel.value = 'zhipu';
      sel.dispatchEvent(new Event('change', { bubbles: true }));
      return 1;
    })()`);
    await sleep(400);
    await clickSel(c, '#prov-list .prov-card:last-child .key-row input');
    await sleep(200);
    await typeText(c, 'sk-zhipu-verify-0001');
    await sleep(300);
    log('A 输入 Key 后:', JSON.stringify(await read(c, LAST)));
    await shot(c, 'gui-test-screenshots/mtfix-a1-typed.png');

    // ===== B 触发重渲染（点「＋ 添加模型」）→ Key 应保留 =====
    await clickSel(c, '#prov-list .prov-card:last-child .mc-add'); await sleep(500);
    log('B 点添加模型后:', JSON.stringify(await read(c, LAST)));

    // ===== C 收起 → 草稿卡仍在；再展开 → Key 仍在 =====
    await clickSel(c, '#prov-list .prov-card:last-child .pv-head'); await sleep(400);
    log('C1 收起后:', JSON.stringify(await read(c, LAST)));
    await clickSel(c, '#prov-list .prov-card:last-child .pv-head'); await sleep(400);
    log('C2 重新展开后:', JSON.stringify(await read(c, LAST)));
    await shot(c, 'gui-test-screenshots/mtfix-a2-reopen.png');

    // ===== D 保存（预置新卡片）→ 应 200 =====
    await clickSel(c, '#prov-list .prov-card:last-child .pf-save'); await sleep(2000);
    const d = await read(c, `({
      alerts: window.__alerts.slice(),
      errs: window.__errs.slice(),
      posts: window.__fetchLog.filter(f => f.url.includes('/api/providers') && f.method === 'POST').map(f => ({ u: f.url, st: f.status, body: f.body, resp: f.resp })),
    })`);
    log('D 保存结果:', JSON.stringify(d, null, 1));
    await shot(c, 'gui-test-screenshots/mtfix-a3-saved.png');

    const after = await read(c, `[...document.querySelectorAll('#prov-list .prov-card')].map(el => ({
      name: (el.querySelector('.pv-name') || {}).textContent,
      meta: (el.querySelector('.pv-meta') || {}).textContent,
    }))`);
    log('D2 保存后卡片列表:', JSON.stringify(after));

    // 服务端配置直接验证（Node 侧直连 3081）
    const cfg = await (await fetch(BASE + '/api/config')).json();
    log('D3 服务端提供方:', JSON.stringify((cfg.providers || []).map(p => ({ id: p.id, name: p.name, preset: p.preset, keyMasked: p.apiKeyMasked, models: (p.models || []).length }))));

    // ===== E 展开智谱卡 → 拉取模型（假 Key 预期 401/502，链路应正常返回失败）=====
    const zi = await read(c, `(() => {
      const cards = [...document.querySelectorAll('#prov-list .prov-card')];
      return cards.findIndex(el => ((el.querySelector('.pv-name') || {}).textContent || '') === '智谱 GLM');
    })()`);
    log('E 智谱卡下标:', zi);
    if (zi >= 0) {
      const sel = `#prov-list .prov-card:nth-child(${zi + 1})`;
      await clickSel(c, sel + ' .pv-head'); await sleep(400);
      await clickSel(c, sel + ' .mc-btn:nth-child(3)'); await sleep(10000);
      const e = await read(c, `({
        alerts: window.__alerts.slice(),
        fetch: window.__fetchLog.filter(f => f.url.includes('/api/providers/fetch')).map(f => ({ u: f.url, st: f.status, resp: (f.resp || '').slice(0, 160) })),
      })`);
      log('E 拉取结果:', JSON.stringify(e, null, 1));
      await shot(c, 'gui-test-screenshots/mtfix-a4-fetch.png');
    }

    const finalErrs = await read(c, `window.__errs.slice()`);
    log('最终页面 JS 错误:', JSON.stringify(finalErrs));
  } finally {
    await closeTab(c);
  }
}

main().catch((e) => { console.error('FAIL', e); process.exit(1); });
