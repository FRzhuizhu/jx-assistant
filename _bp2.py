# -*- coding: utf-8 -*-
# 前端补丁：composer 重排+语音、会话菜单重做、圆点三色、工作区菜单扩展
import io
miss = []
def patch(path, pairs):
    s = io.open(path, encoding='utf-8').read()
    for old, new in pairs:
        if old in s:
            s = s.replace(old, new, 1)
        else:
            miss.append(path + ': ' + old[:60].replace('\n', '\\n'))
    io.open(path, 'w', encoding='utf-8').write(s)

# ============ 1. index.html：composer 重排（发送最右，往左模型、语音；插件挂点挪左侧） ============
patch('web/index.html', [(
'''        <div class="composer-bar">
          <div class="composer-left">
            <button id="btn-plus" title="上传与更多">''',
'''        <div class="composer-bar">
          <div class="composer-left">
            <span id="plugin-composer-controls"></span>
            <button id="btn-plus" title="上传与更多">'''),
(
'''          <div class="composer-right">
            <span id="plugin-composer-controls"></span>
            <button id="model-chip" title="选择模型">''',
'''          <div class="composer-right">
            <button id="btn-voice" title="语音输入">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="2" width="6" height="12" rx="3"/><path d="M5 10v1a7 7 0 0 0 14 0v-1M12 18v4"/></svg>
            </button>
            <button id="model-chip" title="选择模型">''')])

# ============ 2. app.js：会话菜单（重命名/标为未读/分支对话/移入回收站） ============
patch('web/app.js', [(
'''function openSessionMenu(s, rect) {
  openCtxMenu([
    { label: '重命名', fn: async () => {
      const t = prompt('重命名会话', s.title || '');
      if (t === null || !t.trim()) return;
      await api('/api/sessions/' + s.id, { method: 'PATCH', body: JSON.stringify({ title: t.trim() }) });
      loadSessions();
    } },
    { label: '复制标题', fn: () => navigator.clipboard.writeText(s.title || '(未命名)') },
    { label: s.archived ? '取消归档' : '归档', fn: async () => {
      await api('/api/sessions/' + s.id, { method: 'PATCH', body: JSON.stringify({ archived: !s.archived }) });
      loadSessions();
    } },
    { label: '删除', danger: true, fn: () => deleteSession(s) },
  ], rect.right - 8, rect.bottom + 4);
}''',
'''function openSessionMenu(s, rect) {
  openCtxMenu([
    { label: '重命名', fn: async () => {
      const t = prompt('重命名会话', s.title || '');
      if (t === null || !t.trim()) return;
      await api('/api/sessions/' + s.id, { method: 'PATCH', body: JSON.stringify({ title: t.trim() }) });
      loadSessions();
    } },
    { label: s.unread ? '取消未读' : '标为未读', fn: async () => {
      await api('/api/sessions/' + s.id, { method: 'PATCH', body: JSON.stringify({ unread: !s.unread }) });
      loadSessions();
    } },
    { label: '分支对话', fn: async () => {
      try {
        const fork = await api('/api/sessions/' + s.id + '/fork', { method: 'POST' });
        await loadSessions();
        if (!state.streaming) openSession(fork.id);
      } catch (e) { alert('分支失败：' + e.message); }
    } },
    { label: '移入回收站', danger: true, fn: async () => {
      await api('/api/sessions/' + s.id, { method: 'PATCH', body: JSON.stringify({ archived: true }) });
      if (state.cur && state.cur.id === s.id) { state.cur = null; state.msgs = []; resetEmpty(); }
      loadSessions();
    } },
  ], rect.right - 8, rect.bottom + 4);
}''')])

# ============ 3. 圆点：默认不显示；橙=待审批 绿=完成未看 红=未读 ============
patch('web/app.js', [(
'''const subActs = {}; // 子会话id -> 最近几条工具活动
const subDone = {}; // 子会话id -> 是否已完成（内存态；重载后由回报消息体现）''',
'''const subActs = {}; // 子会话id -> 最近几条工具活动
const subDone = {}; // 子会话id -> 是否已完成（内存态；重载后由回报消息体现）
const dotApproval = {}; // 会话id -> 待审批（橙点）
const dotDone = {};     // 会话id -> 完成未查看（绿点）''')])

patch('web/app.js', [(
'''function sessionItem(s) {
  const item = document.createElement('div');
  item.className = 'session-item' + (state.cur && state.cur.id === s.id ? ' active' : '');
  const hot = (Date.now() - new Date(String(s.updatedAt || '').replace(' ', 'T')).getTime()) < 3600 * 1000;
  item.innerHTML =
    `<span class="dot${hot ? ' hot' : ''}"></span>` +
    `<span class="s-title">${esc(s.title || '(未命名)')}</span>` +''',
'''function sessionItem(s) {
  const item = document.createElement('div');
  item.className = 'session-item' + (state.cur && state.cur.id === s.id ? ' active' : '');
  // 圆点：平时不显示；橙=待审批，绿=完成未查看，红=标为未读
  let dot = '';
  if (dotApproval[s.id]) dot = ' ap';
  else if (dotDone[s.id]) dot = ' done';
  else if (s.unread) dot = ' unread';
  item.innerHTML =
    `<span class="dot${dot}"></span>` +
    `<span class="s-title">${esc(s.title || '(未命名)')}</span>` +''')])

# 打开会话即清未读与绿点
patch('web/app.js', [(
'''  // 恢复该会话上次未发送的输入
  try {
    const t = localStorage.getItem(sessionDraftKey(id));
    if (t) { $('input').value = t; autoGrow(); }
  } catch (e) {}''',
'''  // 打开即视为已读：清红点与绿点
  delete dotDone[id];
  if (state.cur && state.cur.unread) {
    state.cur.unread = false;
    api('/api/sessions/' + id, { method: 'PATCH', body: JSON.stringify({ unread: false }) }).catch(() => {});
  }
  // 恢复该会话上次未发送的输入
  try {
    const t = localStorage.getItem(sessionDraftKey(id));
    if (t) { $('input').value = t; autoGrow(); }
  } catch (e) {}''')])

# 全局事件：审批/完成驱动圆点
patch('web/app.js', [(
'''function handleGlobalEvent(ev) {
  const viewing = state.cur && ev.sessionId === state.cur.id;''',
'''function handleGlobalEvent(ev) {
  const viewing = state.cur && ev.sessionId === state.cur.id;
  if (ev.type === 'approval_required') { dotApproval[ev.sessionId] = true; loadSessions(); }
  if (ev.type === 'approval_resolved') { delete dotApproval[ev.sessionId]; loadSessions(); }''')])

patch('web/app.js', [(
'''    case 'sub_end':
      subDone[ev.sessionId] = true;
      loadSessions();''',
'''    case 'sub_end':
      subDone[ev.sessionId] = true;
      dotDone[ev.sessionId] = true;
      loadSessions();''')])

# 本地轮结束：若不是当前正看的会话（后台轮）标绿点——当前会话无需提示
patch('web/app.js', [(
'''    case 'report_ready':
    case 'session_updated':
      loadSessions();''',
'''    case 'report_ready':
    case 'session_updated':
      if (!viewing && ev.type === 'session_updated') dotDone[ev.sessionId] = dotDone[ev.sessionId] || aRunJustEnded(ev.sessionId);
      loadSessions();''')])

# 记录上一轮状态快照，用于 running→idle 翻转判定
patch('web/app.js', [(
'''async function loadSessions() {
  const data = await api('/api/sessions');
  state.sessions = data.sessions || [];''',
'''const prevStatus = {}; // 会话id -> 上一快照的 status（识别 running→idle 的完成翻转）
function aRunJustEnded(id) {
  const cur = (state.sessions.find((x) => x.id === id) || {}).status;
  const was = prevStatus[id];
  return was === 'running' && cur !== 'running';
}

async function loadSessions() {
  const data = await api('/api/sessions');
  state.sessions = data.sessions || [];
  (state.sessions || []).forEach((x) => { prevStatus[x.id] = x.status; });''')])

# ============ 4. 工作区菜单：加重命名（别名）与移入回收站（红，底部） ============
patch('web/app.js', [(
'''    head.querySelector('.wa-more').onclick = (e) => {
      e.stopPropagation();
      const r = e.currentTarget.getBoundingClientRect();
      const manual = lsGet('jx.wslist', []).includes(ws);
      const acts = [
        { label: '复制路径', fn: () => navigator.clipboard.writeText(ws) },
        { label: '在资源管理器中打开', fn: () => api('/api/open-folder', { method: 'POST', body: JSON.stringify({ path: ws }) }).catch((err) => alert('打开失败：' + err.message)) },
      ];
      if (manual && !items.length) {
        acts.push({ label: '从列表移除', danger: true, fn: () => {
          lsSet('jx.wslist', lsGet('jx.wslist', []).filter((x) => x !== ws));
          loadSessions();
        } });
      }
      openCtxMenu(acts, r.left, r.bottom + 4);
    };''',
'''    head.querySelector('.wa-more').onclick = (e) => {
      e.stopPropagation();
      const r = e.currentTarget.getBoundingClientRect();
      const alias = lsGet('jx.wsalias', {})[ws] || '';
      const acts = [
        { label: '重命名', fn: () => {
          const t = prompt('重命名工作区（仅显示名）', alias || baseName(ws));
          if (t === null) return;
          const m = lsGet('jx.wsalias', {});
          if (t.trim()) m[ws] = t.trim(); else delete m[ws];
          lsSet('jx.wsalias', m);
          loadSessions();
        } },
        { label: '复制路径', fn: () => navigator.clipboard.writeText(ws) },
        { label: '在资源管理器中打开', fn: () => api('/api/open-folder', { method: 'POST', body: JSON.stringify({ path: ws }) }).catch((err) => alert('打开失败：' + err.message)) },
        { label: '移入回收站', danger: true, fn: () => {
          lsSet('jx.wsrecycled', [...new Set([...lsGet('jx.wsrecycled', []), ws])]);
          loadSessions();
        } },
      ];
      openCtxMenu(acts, r.left, r.bottom + 4);
    };''')])

# 组名显示别名；回收的工作区整组隐藏
patch('web/app.js', [(
'''  const box = $('session-list');
  box.innerHTML = '';
  if (viewCfg.group === 'flat') {''',
'''  const box = $('session-list');
  box.innerHTML = '';
  const recycled = new Set(lsGet('jx.wsrecycled', []));
  const aliasMap = lsGet('jx.wsalias', {});
  if (viewCfg.group === 'flat') {''')])

patch('web/app.js', [(
'''      `<span class="ws-name">${esc(baseName(ws) || '默认工作区')}</span>` +''',
'''      `<span class="ws-name">${esc(aliasMap[ws] || baseName(ws) || '默认工作区')}</span>` +''')])

patch('web/app.js', [(
'''  for (const [ws, items] of groups) {
    const group = document.createElement('div');
    group.className = 'ws-group';
    const collapsed = !!wsCollapsed[ws];''',
'''  for (const [ws, items] of groups) {
    if (recycled.has(ws)) continue; // 已移入回收站的工作区整组隐藏
    const group = document.createElement('div');
    group.className = 'ws-group';
    const collapsed = !!wsCollapsed[ws];''')])

# flat 模式同样过滤回收工作区里的会话
patch('web/app.js', [(
'''  if (viewCfg.group === 'flat') {
    box.classList.add('flat');
    for (const s of visible) box.appendChild(sessionItem(s));''',
'''  if (viewCfg.group === 'flat') {
    box.classList.add('flat');
    for (const s of visible) {
      if (!recycled.has(wsOf(s))) box.appendChild(sessionItem(s));
    }''')])

# ============ 5. 语音输入（Web Speech API） ============
patch('web/app.js', [(
'''$('btn-drawer').onclick = toggleDrawer;''',
'''$('btn-drawer').onclick = toggleDrawer;
// 语音输入：浏览器原生识别，边说边追加到输入框
let voiceRecog = null;
let voiceOn = false;
$('btn-voice').onclick = () => {
  const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
  if (!SR) { $('hint').textContent = '当前浏览器不支持语音识别'; setTimeout(() => { $('hint').textContent = ''; }, 2500); return; }
  if (voiceOn) { voiceRecog && voiceRecog.stop(); return; }
  voiceRecog = new SR();
  voiceRecog.lang = 'zh-CN';
  voiceRecog.interimResults = false;
  voiceRecog.continuous = true;
  voiceRecog.onresult = (e) => {
    let text = '';
    for (let i = e.resultIndex; i < e.results.length; i++) text += e.results[i][0].transcript;
    if (text) {
      $('input').value += ($('input').value ? ' ' : '') + text;
      autoGrow();
      persistSessionDraftLS();
    }
  };
  voiceRecog.onend = () => { voiceOn = false; $('btn-voice').classList.remove('on'); };
  voiceRecog.onerror = (e) => {
    voiceOn = false;
    $('btn-voice').classList.remove('on');
    if (e.error !== 'aborted') { $('hint').textContent = '语音识别失败：' + e.error; setTimeout(() => { $('hint').textContent = ''; }, 2500); }
  };
  try {
    voiceRecog.start();
    voiceOn = true;
    $('btn-voice').classList.add('on');
  } catch (e) { /* 重复启动等 */ }
};''')])

print('missed:', len(miss))
for m in miss:
    print(' -', m)
