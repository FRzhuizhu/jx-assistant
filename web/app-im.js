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
        <button class="im-icon-btn" id="im-bind-btn" title="扫码绑定 / 解绑 / 发测试消息">绑定</button>
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
    </div>
    <div class="im-drawer hidden" id="im-drawer">
      <div class="im-drawer-head">
        <div class="im-drawer-title">微信绑定</div>
        <button class="im-icon-btn" id="im-drawer-close">关闭</button>
      </div>
      <div class="im-drawer-body" id="im-drawer-body"></div>
    </div>`;
  const main = document.getElementById('main');
  (main || document.body).appendChild(v);
  v.querySelector('#im-close').onclick = () => imClose();
  v.querySelector('#im-bind-btn').onclick = () => imBindOpen();
  v.querySelector('#im-drawer-close').onclick = () => imBindClose();
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
  imBindClose(); // 关面板顺手收起绑定抽屉、停掉扫码轮询
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

// ==================== 绑定抽屉：扫码 / 解绑 / 测试消息 ====================
//
// 这里是**绑定运维**，不是消息通道：面板仍然只读，不给 AI 也不给界面开
// 「给真人发消息」的口子（除了用户自己按下的那一条测试消息）。
//
// 接口与闸（见 im_api.go）：
//   POST /api/im/weixin/qr        同源 POST → 过 requireUIAction ✓
//   GET  /api/im/weixin/qr?id=    轮询：**刻意不加闸**（浏览器同源 GET 不带 Origin），
//                                 所以这里 getAll 式调用，绝不加自定义头
//   POST /api/im/weixin/logout    过闸 + 二次确认（会删凭据、清绑定表）
//   POST /api/im/weixin/test      过闸；返回语义诚实（submitted / platformReceipt:false）
//
// 真机上 qrImage 不是 base64 PNG，而是 liteapp 链接（https://liteapp.weixin.qq.com/q/…），
// 所以用 qrLib.qrSVG() 现渲染成二维码（它接受任意文本），失败再退回「可点链接」。

const imBind = {
  snap: null,      // 最近一次扫码快照（id/status/qrImage/expiresIn/verifyNeeded）
  timer: 0,        // 轮询定时器
  busy: false,     // 防连点
  note: '',        // 测试消息等操作的回执文案（诚实口径）
  err: '',         // 最近一次错误
  renderedQr: '',  // 已经渲染过的 qrImage（避免轮询时反复重画、闪一下）
};

function imBindChannel() {
  return imState.conn || null;
}

function imBindEl(id) {
  return document.getElementById(id);
}

function imBindOpen() {
  const d = imBindEl('im-drawer');
  if (!d) return;
  d.classList.remove('hidden');
  imBind.note = '';
  imBindRender();
}

function imBindClose() {
  const d = imBindEl('im-drawer');
  if (d) d.classList.add('hidden');
  imBindStopPoll();
}

function imBindStopPoll() {
  if (imBind.timer) { clearTimeout(imBind.timer); imBind.timer = 0; }
}

// imBindStatusText 把平台状态翻成人话（wait→扫码中、scaned→去手机确认…）
function imBindStatusText() {
  const s = imBind.snap;
  if (!s) return '';
  const left = typeof s.expiresIn === 'number' ? s.expiresIn : 0;
  switch (String(s.status || '')) {
    case 'wait': return `等待扫码…（约 ${left} 秒后过期）`;
    case 'scaned': return '已扫码，请在手机上确认登录';
    case 'need_verifycode': return '手机要求输入配对码：请在下方填写并提交';
    case 'confirmed': return '绑定成功，正在刷新…';
    case 'expired': return '二维码已过期，点「重新生成」再来一次';
    case 'failed': return `绑定失败：${s.error || '未知原因'}`;
    case 'binded': return '这台设备此前已绑定过微信，可直接使用';
    default: return s.error || String(s.status || '');
  }
}

function imBindRender() {
  const box = imBindEl('im-drawer-body');
  if (!box) return;
  const ch = imBindChannel();
  const connected = !!(ch && ch.state === 'connected');
  const s = imBind.snap;
  const st = s ? String(s.status || '') : '';
  const hasQr = !!(s && s.qrImage);
  const dead = st === 'expired' || st === 'failed' || st === 'confirmed';
  const rows = [
    ['渠道状态', ch ? (ch.state || '未知') : '未启用'],
    ['账号', (ch && (ch.accountId || ch.ownerId)) || '未绑定'],
    ['白名单', `${ch ? (ch.allow || 0) : 0} 条${ch && ch.allow ? '' : '（0 条＝谁都不能下指令）'}`],
    ['会话凭据', ch && ch.contextToken ? '已有（可发主动消息）' : '没有（平台不投递主动消息）'],
  ];
  let html = `<div class="im-card">${rows.map(([k, v]) =>
    `<div class="im-card-row"><span class="im-muted">${esc(k)}</span><span>${esc(v)}</span></div>`).join('')}`;
  if (ch && ch.lastError) {
    html += `<div class="im-card-row"><span class="im-muted">最近错误</span><span class="im-err">${esc(ch.lastError)}</span></div>`;
  }
  html += '</div>';

  if (hasQr && !dead) {
    html += `<div class="im-qr"><div class="im-qr-img" id="im-qr-img">二维码生成中…</div>
      <div class="im-qr-tip" id="im-qr-tip">${esc(imBindStatusText())}</div>`;
    if (st === 'need_verifycode') {
      html += `<div class="im-qr-verify">
        <input id="im-verify" placeholder="手机上显示的配对码">
        <button class="im-icon-btn" id="im-verify-go">提交</button>
      </div>`;
    }
    html += `<div class="im-qr-tip im-muted">手机微信扫一扫 → 确认登录；二维码约 2 分钟内有效</div>`;
    html += '</div>';
  } else if (!connected || dead) {
    html += `<button class="im-primary" id="im-qr-start">${dead ? '重新生成二维码' : '扫码绑定微信'}</button>`;
  }

  if (st === 'expired' || st === 'failed') {
    html += `<div class="im-err" id="im-bind-err">${esc(imBindStatusText())}</div>`;
  }
  if (imBind.err) {
    html += `<div class="im-err">${esc(imBind.err)}</div>`;
  }
  if (imBind.note) {
    html += `<div class="im-note">${esc(imBind.note)}</div>`;
  }

  html += '<div class="im-drawer-actions">';
  if (connected) {
    html += `<button class="im-icon-btn" id="im-test">发一条测试消息</button>`;
    html += `<button class="im-icon-btn im-danger" id="im-unbind">解除绑定</button>`;
  }
  html += `<button class="im-icon-btn" id="im-bind-refresh">刷新状态</button></div>`;

  box.innerHTML = html;

  // 事件（每次重渲染都重新绑定：innerHTML 会丢掉旧监听）
  const start = imBindEl('im-qr-start');
  if (start) start.onclick = () => imBindStart();
  const vgo = imBindEl('im-verify-go');
  if (vgo) vgo.onclick = () => imBindVerify((imBindEl('im-verify') || {}).value || '');
  const test = imBindEl('im-test');
  if (test) test.onclick = () => imBindTest();
  const unb = imBindEl('im-unbind');
  if (unb) unb.onclick = () => imBindLogout(unb);
  const rf = imBindEl('im-bind-refresh');
  if (rf) rf.onclick = () => imBindRefreshChannel();

  // 二维码只在 qrImage 变化时重画（轮询每 1.5 秒一次，重画会闪）
  const qrBox = imBindEl('im-qr-img');
  if (qrBox && hasQr) imBindRenderQr(qrBox, String(s.qrImage));
}

// imBindRenderQr 优先用 qrLib 把 qrImage（liteapp 链接）渲染成 SVG 二维码；
// 渲染不了就退回「可点链接 + 原文」，至少不会白屏。
async function imBindRenderQr(box, text) {
  if (imBind.renderedQr === text && box.querySelector('svg,img,a')) return;
  imBind.renderedQr = text;
  if (/^data:image\//i.test(text)) {
    box.innerHTML = `<img alt="微信绑定二维码" src="${esc(text)}">`;
    return;
  }
  if (typeof qrLib !== 'undefined' && qrLib && typeof qrLib.qrSVG === 'function') {
    try {
      box.innerHTML = await qrLib.qrSVG(text, { size: 200 });
      return;
    } catch (e) { /* 落到链接兜底 */ }
  }
  box.innerHTML = `<a href="${esc(text)}" target="_blank" rel="noopener">在手机浏览器里打开绑定链接</a>
    <div class="im-qr-raw">${esc(text)}</div>`;
}

async function imBindStart() {
  if (imBind.busy) return;
  imBind.busy = true;
  imBind.err = '';
  imBind.note = '';
  imBind.snap = null;
  imBind.renderedQr = '';
  imBindStopPoll();
  try {
    const snap = await api('/api/im/weixin/qr', { method: 'POST', body: '{}' });
    imBind.snap = snap;
    imBindRender();
    imBindPoll();
  } catch (e) {
    imBind.err = '生成二维码失败：' + ((e && e.message) || '未知原因');
    imBindRender();
  } finally {
    imBind.busy = false;
  }
}

// imBindPoll 每 1.5 秒问一次扫码进度。GET 是**同源无自定义头**的调用：
// 服务端这条路由刻意没加 Origin 闸（浏览器同源 GET 不带 Origin）。
function imBindPoll() {
  imBindStopPoll();
  const s = imBind.snap;
  if (!s || !s.id) return;
  const st = String(s.status || '');
  if (st === 'confirmed' || st === 'expired' || st === 'failed') return;
  imBind.timer = setTimeout(async () => {
    try {
      const next = await api(`/api/im/weixin/qr?id=${encodeURIComponent(s.id)}`);
      imBind.snap = next;
      const ns = String(next.status || '');
      // 状态机转折点整块重画（要出验证码输入框 / 重生成按钮），其余只改提示文字
      if (ns === 'need_verifycode' || ns === 'expired' || ns === 'failed' || ns === 'confirmed') {
        imBindRender();
      } else {
        const tip = imBindEl('im-qr-tip');
        if (tip) tip.textContent = imBindStatusText();
      }
      if (ns === 'confirmed') {
        toast('微信绑定成功');
        imBindStopPoll();
        imBind.snap = null;      // 收掉二维码区，回到「已连接」形态
        await imReload(true);    // 顶栏渠道状态、左列聊天都跟着更新
        imBindRender();
      } else if (ns === 'expired' || ns === 'failed') {
        imBindStopPoll();
      } else {
        imBindPoll();
      }
    } catch (e) {
      imBind.err = '查询扫码状态失败：' + ((e && e.message) || '未知原因');
      imBindStopPoll();
      imBindRender();
    }
  }, 1500);
}

async function imBindVerify(code) {
  const s = imBind.snap;
  const v = String(code || '').trim();
  if (!s || !s.id) return;
  if (!v) { toast('请先输入手机上的配对码'); return; }
  try {
    const next = await api(`/api/im/weixin/qr?id=${encodeURIComponent(s.id)}&verify=${encodeURIComponent(v)}`);
    imBind.snap = next;
    imBind.err = '';
    imBindRender();
    imBindPoll();
  } catch (e) {
    imBind.err = '提交配对码失败：' + ((e && e.message) || '未知原因');
    imBindRender();
  }
}

// imBindRefreshChannel 只刷新右上角/抽屉里的渠道状态（不动扫码流程）
async function imBindRefreshChannel() {
  try {
    const d = await api('/api/im/status');
    const ch = (d.channels || []).find((c) => c.channel === 'weixin') || (d.channels || [])[0] || null;
    imState.conn = ch;
    imState.whitelist = ch ? (ch.allow || 0) : 0;
    const pill = document.getElementById('im-conn');
    const wl = document.getElementById('im-whitelist');
    const ok = !!(ch && ch.state === 'connected');
    if (pill) {
      pill.textContent = ok ? '微信已连接' : ((ch && ch.state) ? `微信 ${ch.state}` : '微信未连接');
      pill.className = `im-pill ${ok ? 'ok' : 'off'}`;
    }
    if (wl) wl.textContent = `白名单 ${imState.whitelist} 条`;
    imBind.err = '';
    imBindRender();
  } catch (e) {
    imBind.err = '读取渠道状态失败：' + ((e && e.message) || '未知原因');
    imBindRender();
  }
}

// imBindLogout 二次确认（第一次点只把按钮换成确认态，5 秒后自动收回）
function imBindLogout(btn) {
  if (!btn) return;
  if (btn.dataset.armed !== '1') {
    btn.dataset.armed = '1';
    btn.textContent = '确认解除？会删凭据与绑定';
    btn.classList.add('im-danger-armed');
    setTimeout(() => {
      if (!document.body.contains(btn)) return;
      btn.dataset.armed = '';
      btn.textContent = '解除绑定';
      btn.classList.remove('im-danger-armed');
    }, 5000);
    return;
  }
  btn.disabled = true;
  btn.textContent = '正在解除…';
  api('/api/im/weixin/logout', { method: 'POST', body: '{}' })
    .then(async () => {
      imBind.snap = null;
      imBind.renderedQr = '';
      imBind.note = '已解除绑定：凭据与绑定表都清了。要再连，重新扫码即可。';
      imBind.err = '';
      await imReload(true);
      await imBindRefreshChannel();
    })
    .catch((e) => {
      imBind.err = '解除绑定失败：' + ((e && e.message) || '未知原因');
      imBindRender();
    });
}

// imBindTest 发一条测试消息。服务端的语义是诚实的（submitted 只代表平台接受），
// 界面上不许把它说成「已送达」；没有 context_token 时 409 的原文照抄给用户。
function imBindTest() {
  const btn = imBindEl('im-test');
  if (btn) { btn.disabled = true; btn.textContent = '正在发送…'; }
  api('/api/im/weixin/test', { method: 'POST', body: '{}' })
    .then((d) => {
      const ok = d && d.ok;
      const note = (d && d.note) || '';
      imBind.note = ok
        ? `已提交给微信（平台未回真实回执，是否收到以手机为准）。${note}`
        : `平台没有接受这条消息。${note}`;
      imBind.err = '';
      imBindRender();
    })
    .catch((e) => {
      // 409 = 没有 context_token：把那句「先去微信说一句话」原样给用户看
      imBind.err = (e && e.message) || '发送测试消息失败';
      imBind.note = '';
      imBindRender();
    });
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
