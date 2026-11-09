// IM（微信）会话记录面板 —— 只读视图。
//
// 口径（评审图 mockup-A-im-panel.png）：左列聊天列表（备注/打码 id + 最后一条 +
// 时间 + 未读红点），右侧消息流水（左灰=入站、右蓝=出站，带时间与推送状态），
// 顶部渠道状态 + 白名单人数，底部「面板只读：回复请在微信里发」。
// **不提供界面发微信的入口**：避免双端消息不一致，也不给 AI 开一条给真人发消息
// 的通道（服务端同样没有登记发送接口）。
//
// 数据来自 im_log.go 的流水表 + 绑定表：
//   GET  /api/im/chats                      聊天清单（含未读/已推序号/失败数）
//   GET  /api/im/messages?chat=&before=     某个聊天的流水
//   POST /api/im/read   {chat, upTo}        推进已读位标（打开聊天时）
//   POST /api/im/remark {chat, remark}      改本地备注（微信协议拿不到昵称）
// 两个 POST 服务端要求同源 Origin（只能在界面里点），本文件的 fetch 天然满足。

const imState = {
  open: false,
  chats: [],
  cur: '',            // 当前选中的 chatId
  msgs: [],
  filter: 'all',      // all | failed | inbound
  q: '',
  loading: false,
  err: '',
  timer: 0,           // SSE 触发的刷新防抖
  poll: 0,            // 兜底轮询（SSE 断开时也能更新）
  conn: null,         // 渠道状态
  whitelist: 0,
};

// imNow() 在服务端是 "2006-01-02 15:04:05"，这里只取 HH:MM（跨天则带日期）。
function imClock(at) {
  const s = String(at || '');
  const m = s.match(/^(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2})/);
  if (!m) return s ? esc(s) : '';
  const today = new Date();
  const sameDay = Number(m[1]) === today.getFullYear() && Number(m[2]) === today.getMonth() + 1 && Number(m[3]) === today.getDate();
  return sameDay ? `${m[4]}:${m[5]}` : `${m[2]}-${m[3]} ${m[4]}:${m[5]}`;
}

function imInitial(name) {
  const s = String(name || '').trim();
  return s ? esc(Array.from(s)[0]) : '?';
}

// imChatName 显示名：备注 > 打码 id（服务端已算好 display，这里只做兜底）。
function imChatName(c) {
  return c.display || c.remark || c.masked || c.chatId;
}

function imFilteredChats() {
  const q = imState.q.trim().toLowerCase();
  return imState.chats.filter((c) => {
    if (imState.filter === 'failed' && !(c.failed > 0)) return false;
    if (imState.filter === 'inbound' && !(c.inbound > 0)) return false;
    if (!q) return true;
    const hay = `${imChatName(c)} ${c.remark || ''} ${c.masked || ''} ${c.lastText || ''}`.toLowerCase();
    return hay.includes(q);
  });
}

// ---------- 面板骨架（容器在 JS 里建：不动 index.html 的布局结构） ----------

function imEnsureView() {
  let v = document.getElementById('im-view');
  if (v) return v;
  v = document.createElement('div');
  v.id = 'im-view';
  v.className = 'im-view hidden';
  v.innerHTML = `
    <div class="im-head">
      <div class="im-head-title">IM 消息<span class="im-badge hidden" id="im-total-unread"></span></div>
      <div class="im-head-right">
        <span class="im-pill" id="im-conn">微信</span>
        <span class="im-muted" id="im-whitelist"></span>
        <button class="im-icon-btn" id="im-refresh" title="刷新">刷新</button>
        <button class="im-icon-btn" id="im-close" title="关闭面板">关闭</button>
      </div>
    </div>
    <div class="im-body">
      <div class="im-list">
        <input id="im-search" class="im-search" placeholder="搜索昵称 / 备注 / 内容">
        <div class="im-filters" id="im-filters"></div>
        <div class="im-chats" id="im-chats"></div>
      </div>
      <div class="im-chat">
        <div class="im-chat-head">
          <button class="im-icon-btn im-back" id="im-back" title="返回列表">‹ 列表</button>
          <div class="im-chat-title" id="im-chat-title">选一个聊天看记录</div>
          <div class="im-chat-sub" id="im-chat-sub"></div>
        </div>
        <div class="im-msgs" id="im-msgs"></div>
        <div class="im-readonly">
          <span>面板只读：回复请在微信里发（避免双端消息不一致）</span>
          <span class="im-muted" id="im-bound"></span>
        </div>
      </div>
    </div>`;
  const main = document.getElementById('main');
  (main || document.body).appendChild(v);
  v.querySelector('#im-close').onclick = () => imClose();
  v.querySelector('#im-refresh').onclick = () => imReload(true);
  v.querySelector('#im-back').onclick = () => v.classList.remove('im-detail');
  v.querySelector('#im-search').oninput = (e) => { imState.q = e.target.value; imRenderChats(); };
  return v;
}

// ---------- 入口按钮（插在左栏「回收站」上方，与内置格同一套样式） ----------

function imEnsureEntry() {
  if (document.getElementById('btn-im')) return;
  const recycle = document.getElementById('btn-recycle');
  const grid = recycle && recycle.closest('.foot-grid');
  if (!grid) return;
  const box = document.createElement('div');
  box.className = 'foot-grid';
  box.innerHTML = `
    <button id="btn-im" class="foot-btn" title="微信会话记录（IM 面板）">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 11.5a8.5 8.5 0 0 1-8.5 8.5 8.7 8.7 0 0 1-3.9-.92L3 21l1.95-5.29A8.5 8.5 0 1 1 21 11.5z"/></svg>
      微信<span class="im-entry-badge hidden" id="im-entry-badge"></span>
    </button>`;
  grid.before(box);
  box.querySelector('#btn-im').onclick = () => imOpen();
}

function imSetEntryBadge(n) {
  const b = document.getElementById('im-entry-badge');
  if (!b) return;
  if (n > 0) { b.textContent = n > 99 ? '99+' : String(n); b.classList.remove('hidden'); }
  else b.classList.add('hidden');
}

// ---------- 打开 / 关闭 ----------

async function imOpen() {
  const v = imEnsureView();
  v.classList.remove('hidden');
  document.body.classList.add('im-mode');
  imState.open = true;
  await imReload(true);
  if (!imState.poll) imState.poll = setInterval(() => { if (imState.open) imReload(false); }, 20000);
}

function imClose() {
  const v = document.getElementById('im-view');
  if (v) v.classList.add('hidden');
  document.body.classList.remove('im-mode');
  imState.open = false;
  if (imState.poll) { clearInterval(imState.poll); imState.poll = 0; }
  if (imState.timer) { clearTimeout(imState.timer); imState.timer = 0; }
}

function imIsOpen() {
  return imState.open && !document.getElementById('im-view')?.classList.contains('hidden');
}

// SSE 里 IM 流水变了：会话内的来源/送达徽标要跟着变；面板开着就防抖刷新。
function imOnServerEvent(ev) {
  if (ev && ev.type === 'im') imRefreshMarks();
  if (!imIsOpen()) return;
  if (imState.timer) return;
  imState.timer = setTimeout(() => { imState.timer = 0; imReload(false); }, 250);
}

// ---------- 取数 ----------

async function imReload(force) {
  if (imState.loading) return;
  imState.loading = true;
  try {
    const d = await api('/api/im/chats');
    imState.chats = d.chats || [];
    imState.whitelist = d.whitelist || 0;
    imState.conn = (d.channels || [])[0] || null;
    imState.err = '';
    const unread = imState.chats.reduce((n, c) => n + (c.unread || 0), 0);
    imSetEntryBadge(unread);
    const tot = document.getElementById('im-total-unread');
    if (tot) {
      tot.textContent = unread > 99 ? '99+' : String(unread);
      tot.classList.toggle('hidden', unread <= 0);
    }
    const conn = document.getElementById('im-conn');
    if (conn) {
      const st = imState.conn ? String(imState.conn.state || '') : '';
      const ok = st === 'connected';
      conn.textContent = ok ? '微信已连接' : (st === 'disabled' ? '未启用' : (st ? `微信 ${st}` : '微信未连接'));
      conn.className = `im-pill ${ok ? 'ok' : 'off'}`;
    }
    const wl = document.getElementById('im-whitelist');
    if (wl) wl.textContent = `白名单 ${imState.whitelist} 条`;
    imRenderChats();
    // 当前聊天还在列表里就刷新它的流水（推送状态可能刚回填）。
    // 注意第二个参数是「看完就标已读」：面板正开着这个聊天，新到的入站消息
    // 就该当场清掉红点，所以这里恒为 true（不是「是否强制」）。
    if (imState.cur && imState.chats.some((c) => c.chatId === imState.cur)) {
      await imLoadMessages(imState.cur, true);
    } else if (imState.cur && force) {
      imState.cur = ''; imState.msgs = []; imRenderMessages();
    }
  } catch (e) {
    imState.err = e && e.message ? e.message : '读取失败';
    imRenderChats();
  } finally {
    imState.loading = false;
  }
}

async function imLoadMessages(chatId, markRead) {
  try {
    const d = await api(`/api/im/messages?chat=${encodeURIComponent(chatId)}&limit=200`);
    imState.msgs = d.messages || [];
    imRenderMessages();
    if (markRead !== false && imState.msgs.length) {
      const lastId = imState.msgs[imState.msgs.length - 1].id;
      const c = imState.chats.find((x) => x.chatId === chatId);
      if (c && lastId > (c.readUpTo || 0)) {
        c.readUpTo = lastId;
        const before = c.unread || 0;
        c.unread = 0;
        imRenderChats();
        const unread = imState.chats.reduce((n, x) => n + (x.unread || 0), 0);
        imSetEntryBadge(unread);
        const tot = document.getElementById('im-total-unread');
        if (tot) { tot.textContent = String(unread); tot.classList.toggle('hidden', unread <= 0); }
        if (before > 0) {
          api('/api/im/read', { method: 'POST', body: JSON.stringify({ chat: chatId, upTo: lastId }) }).catch(() => {});
        }
      }
    }
  } catch (e) {
    imState.msgs = [];
    imRenderMessages(e && e.message ? e.message : '读取流水失败');
  }
}

// ---------- 渲染 ----------

function imRenderChats() {
  const list = document.getElementById('im-chats');
  const filters = document.getElementById('im-filters');
  if (!list || !filters) return;
  const chats = imState.chats;
  const nAll = chats.length;
  const nFailed = chats.filter((c) => c.failed > 0).length;
  const nIn = chats.filter((c) => c.inbound > 0).length;
  const fdef = [['all', `全部 ${nAll}`], ['failed', `仅失败 ${nFailed}`], ['inbound', `仅入站 ${nIn}`]];
  filters.innerHTML = fdef.map(([k, label]) => (
    `<button class="im-chip ${imState.filter === k ? 'on' : ''}" data-f="${k}">${esc(label)}</button>`
  )).join('');
  filters.querySelectorAll('[data-f]').forEach((b) => {
    b.onclick = () => { imState.filter = b.dataset.f; imRenderChats(); };
  });

  if (imState.err) {
    list.innerHTML = `<div class="im-empty">读取失败：${esc(imState.err)}<br><button class="im-icon-btn" id="im-retry">重试</button></div>`;
    const r = list.querySelector('#im-retry');
    if (r) r.onclick = () => imReload(true);
    return;
  }
  const rows = imFilteredChats();
  if (!rows.length) {
    list.innerHTML = `<div class="im-empty">${chats.length ? '没有匹配的聊天' : '还没有聊天记录：在微信里给助手发一条消息就会出现在这里'}</div>`;
    return;
  }
  list.innerHTML = rows.map((c) => {
    const name = imChatName(c);
    const sub = c.lastText ? `${c.lastDir === 'out' ? '已推送：' : ''}${c.lastText}` : (c.allowed ? '暂无消息' : '未授权（不在白名单）');
    const badge = c.unread > 0 ? `<span class="im-unread">${c.unread > 99 ? '99+' : c.unread}</span>` : (c.failed > 0 ? '<span class="im-unread warn">!</span>' : '');
    return `<div class="im-chat-item ${c.chatId === imState.cur ? 'active' : ''}" data-chat="${esc(c.chatId)}">
      <div class="im-avatar">${imInitial(name)}</div>
      <div class="im-chat-main">
        <div class="im-chat-top"><span class="im-name">${esc(name)}</span><span class="im-time">${imClock(c.lastAt)}</span></div>
        <div class="im-chat-sub" title="${esc(sub)}">${esc(sub)}</div>
      </div>${badge}</div>`;
  }).join('');
  list.querySelectorAll('[data-chat]').forEach((el) => {
    el.onclick = () => imOpenChat(el.dataset.chat);
  });
}

function imOpenChat(chatId) {
  if (!chatId) return;
  imState.cur = chatId;
  imState.msgs = [];
  const v = document.getElementById('im-view');
  if (v) v.classList.add('im-detail'); // 窄屏：切到「详情」只显示流水（列表由「‹ 列表」返回）
  imRenderChats();
  imRenderMessages('加载中…');
  imLoadMessages(chatId, true);
}

function imStatusLine(m) {
  if (m.direction === 'in') {
    if (m.status === 'rejected') return { cls: 'bad', text: `被拒：${m.error || '未受理'}` };
    if (m.status === 'group') return { cls: 'bad', text: '群聊未受理' };
    if (m.status === 'command') return { cls: 'muted', text: '指令（已处理）' };
    return { cls: 'muted', text: '来自微信（入站）' };
  }
  if (m.status === 'sent') return { cls: 'ok', text: `✓ 已送达${m.at ? ' · ' + imClock(m.at) : ''}` };
  if (m.status === 'failed') {
    const r = m.retries > 0 ? `，已重试 ${m.retries} 次` : '';
    return { cls: 'bad', text: `⚠ 失败：${m.error || '未知原因'}${r}` };
  }
  if (m.status === 'skipped') return { cls: 'warn', text: '静默跳过（桌面在线）' };
  if (m.status === 'pending') return { cls: 'muted', text: '发送中…' };
  return { cls: 'muted', text: m.status || '' };
}

function imRenderMessages(placeholder) {
  const box = document.getElementById('im-msgs');
  const title = document.getElementById('im-chat-title');
  const sub = document.getElementById('im-chat-sub');
  const bound = document.getElementById('im-bound');
  if (!box) return;
  const c = imState.chats.find((x) => x.chatId === imState.cur);
  if (title) title.textContent = c ? imChatName(c) : '选一个聊天看记录';
  if (sub) {
    if (!c) sub.textContent = '';
    else {
      const bits = [];
      if (c.remark) bits.push(`微信备注：${c.remark}`);
      bits.push(c.masked ? `id ${c.masked}` : '');
      bits.push(c.pushedUpTo > 0 ? `已推到第 ${c.pushedUpTo} 条` : '');
      bits.push(c.allowed ? '在白名单' : '不在白名单');
      sub.textContent = bits.filter(Boolean).join(' · ');
    }
  }
  if (bound) bound.textContent = imState.chats.length ? `已绑定 ${imState.chats.length} 个聊天` : '';

  if (!imState.cur) {
    box.innerHTML = '<div class="im-empty">左边选一个聊天，这里显示与微信的收发记录</div>';
    return;
  }
  if (placeholder && !imState.msgs.length) {
    box.innerHTML = `<div class="im-empty">${esc(placeholder)}</div>`;
    return;
  }
  if (!imState.msgs.length) {
    box.innerHTML = '<div class="im-empty">这个聊天还没有记录</div>';
    return;
  }
  box.innerHTML = imState.msgs.map((m) => {
    const st = imStatusLine(m);
    const meta = m.direction === 'in'
      ? `<div class="im-meta">${imClock(m.at)} · ${esc(st.text)}</div>`
      : `<div class="im-meta ${st.cls}">${esc(st.text)}</div>`;
    return `<div class="im-msg ${m.direction}">
      <div class="im-bubble ${m.status === 'failed' ? 'failed' : ''}">${esc(m.text)}</div>
      ${meta}</div>`;
  }).join('');
  box.scrollTop = box.scrollHeight;
}

// ---------- 启动 ----------

function imBoot() {
  imEnsureEntry();
  // 入口未读数：面板没开也要能看见有几条没读（轻量：只拉聊天清单）
  api('/api/im/chats').then((d) => {
    const unread = (d.chats || []).reduce((n, c) => n + (c.unread || 0), 0);
    imSetEntryBadge(unread);
  }).catch(() => {});
  imWatchMessages();
  // 会话切换后补标记（不改 app-chat.js 的渲染流程，靠观察 state.cur 变化）
  let lastSid = '';
  setInterval(() => {
    const sid = (typeof state !== 'undefined' && state.cur && state.cur.id) || '';
    if (sid !== lastSid) { lastSid = sid; imRefreshMarks(); }
  }, 1500);
}

// ==================== C：会话内 IM 来源徽标 + 「只看微信来源」 ====================
//
// 口径（评审图 mockup-C-inline-tag.png）：入站消息气泡上方一行「微信 · 备注 · 时间 ·
// 入站」，出站答复气泡下方「✓ 微信 已送达 · 时间」（失败/静默跳过各有说法）；顶部一个
// 「只看微信来源」开关，打开后非 IM 消息**淡出保留**（不删除、不新建会话）。
//
// 实现上不碰 app-chat.js 的渲染流程：消息行本身带 data-seq，这里按 seq 对 DOM 打标，
// 用 MutationObserver 跟随重渲染（流式输出、切会话、翻历史都会重排 DOM）。

const imMarkState = { sid: '', bySeq: new Map(), only: false, obs: null, timer: 0, mute: false };

function imSrcText(mk) {
  const at = imClock(mk.at);
  const who = mk.name ? ` · ${mk.name}` : '';
  if (mk.direction === 'in') return `微信${who} · ${at} · 入站`;
  if (mk.status === 'sent') return `✓ 微信 已送达 · ${at}`;
  if (mk.status === 'failed') {
    const r = mk.retries > 0 ? `，已重试 ${mk.retries} 次` : '';
    return `⚠ 微信 失败：${mk.error || '未知原因'}${r}`;
  }
  if (mk.status === 'skipped') return '微信 未推送（桌面在线）';
  if (mk.status === 'pending') return '微信 发送中…';
  return `微信 ${mk.status || ''}`;
}

async function imRefreshMarks() {
  const sid = (typeof state !== 'undefined' && state.cur && state.cur.id) || '';
  if (!sid) {
    imMarkState.sid = '';
    imMarkState.bySeq = new Map();
    imDecorateMessages();
    imSyncOnlyChip();
    return;
  }
  if (sid !== imMarkState.sid) { imMarkState.sid = sid; imMarkState.bySeq = new Map(); }
  try {
    const d = await api(`/api/im/session-marks?session=${encodeURIComponent(sid)}`);
    const map = new Map();
    for (const m of (d.marks || [])) map.set(m.seq, m);
    imMarkState.bySeq = map;
  } catch (e) {
    imMarkState.bySeq = new Map(); // 拉不到就当没有标记，绝不影响会话本身
  }
  imDecorateMessages();
  imSyncOnlyChip();
}

function imDecorateMessages() {
  const col = document.getElementById('msg-col');
  if (!col) return;
  imMarkState.mute = true; // 自己改 DOM 会触发 observer，先闭嘴
  try {
    col.querySelectorAll('[data-seq]').forEach((el) => {
      const seq = Number(el.dataset.seq);
      const mk = imMarkState.bySeq.get(seq);
      el.classList.toggle('im-tagged', !!mk);
      let tag = el.querySelector('.im-src');
      if (!mk) { if (tag) tag.remove(); return; }
      if (!tag) {
        tag = document.createElement('div');
        tag.className = 'im-src';
        // 入站在气泡上方、出站在气泡下方（与评审图一致）
        if (mk.direction === 'in') el.prepend(tag); else el.append(tag);
      }
      // 只在真变了才写 DOM：否则每次重渲染都产生新的 mutation 记录，
      // 观察者会被自己的写入反复叫醒（150ms 一轮的空转）。
      const cls = `im-src ${mk.direction === 'in' ? 'in' : (mk.status || '')}`;
      const txt = imSrcText(mk);
      if (tag.className !== cls) tag.className = cls;
      if (tag.textContent !== txt) tag.textContent = txt;
    });
  } finally {
    imMarkState.mute = false;
  }
  if (imMarkState.only) imApplyOnlyCounts();
}

function imEnsureOnlyChip() {
  if (document.getElementById('im-only-chip')) return;
  const bar = document.getElementById('topbar');
  if (!bar) return;
  const b = document.createElement('button');
  b.id = 'im-only-chip';
  b.className = 'im-only-chip hidden';
  b.title = '只看微信来源：桌面端发的消息淡出保留（不删除）';
  b.onclick = () => {
    imMarkState.only = !imMarkState.only;
    document.body.classList.toggle('im-only', imMarkState.only);
    imSyncOnlyChip();
  };
  bar.appendChild(b);
}

function imApplyOnlyCounts() {
  const col = document.getElementById('msg-col');
  const b = document.getElementById('im-only-chip');
  if (!col || !b || !imMarkState.only) return;
  const all = col.querySelectorAll('[data-seq]').length;
  const hit = col.querySelectorAll('[data-seq].im-tagged').length;
  b.textContent = `只看微信来源 · 命中 ${hit} 条（共 ${all} 条）`;
}

function imSyncOnlyChip() {
  imEnsureOnlyChip();
  const b = document.getElementById('im-only-chip');
  if (!b) return;
  const n = imMarkState.bySeq.size;
  if (!n && imMarkState.only) {
    imMarkState.only = false;
    document.body.classList.remove('im-only');
  }
  b.classList.toggle('hidden', n === 0);
  b.classList.toggle('on', imMarkState.only);
  b.textContent = '只看微信来源';
  if (imMarkState.only) imApplyOnlyCounts();
}

function imWatchMessages() {
  const col = document.getElementById('msg-col');
  if (!col || imMarkState.obs) return;
  imMarkState.obs = new MutationObserver(() => {
    if (imMarkState.mute || imMarkState.timer) return;
    imMarkState.timer = setTimeout(() => {
      imMarkState.timer = 0;
      imDecorateMessages();
    }, 150);
  });
  imMarkState.obs.observe(col, { childList: true, subtree: true });
}

if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', imBoot);
else imBoot();
