/* app-ctxmenu.js —— 桌面壳右键菜单。
 * 背景：go-webview2 库在 Debug=false 时把 WebView2 的默认右键菜单与 DevTools
 * 一并禁用（desktop_windows.go 建窗口时未开 Debug），桌面壳右键此前什么都不弹。
 * 这里补一个自定义上下文菜单：仅桌面形态（URL 带 shell=desktop →
 * html.desktop-shell，标记在 app-core.js 加）生效；网页/手机远程模式保留
 * 浏览器原生右键行为不动。
 * 菜单项按右键目标动态组装，弹层直接复用左栏的 openCtxMenu（动态 popover，
 * 定位/关闭/主题都已就绪）。编辑类动作对焦目标控件后用 document.execCommand
 * 执行——undo/redo/cut/copy/insertText 都作用在焦点元素上且保留原生撤销栈
 * （先例：app-core.js 的换行插入特意走 execCommand 就是这个原因）；剪贴板读
 * 权限桌面壳创建时已放行（库 SetPermission(ClipboardRead, Allow)）。 */

/* ---------- 朗读（Web Speech API：WebView2 内核支持，系统离线声库） ---------- */

let ttsVoice = null;
function ttsPickVoice() {
  const vs = speechSynthesis.getVoices();
  if (!vs.length) return null;
  return vs.find((v) => /^zh[-_]/i.test(v.lang)) || vs.find((v) => v.default) || vs[0];
}
if ('speechSynthesis' in window) {
  ttsVoice = ttsPickVoice();
  speechSynthesis.onvoiceschanged = () => { ttsVoice = ttsPickVoice(); };
}
function ttsSupported() { return 'speechSynthesis' in window; }
function ttsSpeaking() {
  return ttsSupported() && (speechSynthesis.speaking || speechSynthesis.pending);
}
function ttsStop() { if (ttsSupported()) speechSynthesis.cancel(); }
function ttsSpeak(text) {
  if (!ttsSupported()) { alert('当前环境不支持语音朗读'); return; }
  const clean = ttsClean(text);
  if (!clean) return;
  speechSynthesis.cancel(); // 单飞：新朗读顶掉旧的
  const u = new SpeechSynthesisUtterance(clean);
  if (!ttsVoice) ttsVoice = ttsPickVoice();
  if (ttsVoice) u.voice = ttsVoice;
  u.lang = (ttsVoice && ttsVoice.lang) || 'zh-CN';
  speechSynthesis.speak(u);
}
// 朗读前的清洗：代码块整段丢弃、markdown 标记剥壳，只留适合读出来的自然语言
function ttsClean(text) {
  return String(text || '')
    .replace(/```[\s\S]*?```/g, ' ')
    .replace(/`([^`]*)`/g, '$1')
    .replace(/!\[([^\]]*)\]\([^)]*\)/g, ' ')
    .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/^\s{0,3}#{1,6}\s+/gm, '')
    .replace(/^\s{0,3}>\s?/gm, '')
    .replace(/^\s*[-*+]\s+/gm, '')
    .replace(/^\s*\d+[.)]\s+/gm, '')
    .replace(/[*_~]{1,3}([^*_~]+)[*_~]{1,3}/g, '$1')
    .replace(/https?:\/\/\S+/g, ' 链接 ')
    .replace(/\s+/g, ' ')
    .trim();
}

/* ---------- 文本编辑动作（execCommand 在焦点元素上执行，保留撤销栈） ---------- */

function edFocus(ed) { if (document.activeElement !== ed) ed.focus(); }
function edHasSelection(ed) {
  if (typeof ed.selectionStart === 'number') return ed.selectionStart !== ed.selectionEnd;
  const s = window.getSelection();
  return !!(s && !s.isCollapsed && ed.contains(s.anchorNode));
}
function insertTextUndoable(ed, text) {
  edFocus(ed);
  if (!document.execCommand('insertText', false, text)) {
    // 兜底（execCommand 不可用的老内核）：直接改值——撤销栈会断，但不至于插不进去
    const st = ed.selectionStart ?? ed.value.length;
    const en = ed.selectionEnd ?? st;
    ed.setRangeText(text, st, en, 'end');
    ed.dispatchEvent(new Event('input', { bubbles: true }));
  }
}
// 取剪贴板内容（右键「粘贴」用）。桌面壳里 navigator.clipboard.readText 会
// 【永久挂起】——WebView2 的剪贴板读权限停在 prompt 且不弹授权 UI（真机诊断
// 见 gui-test/ctxmenu-clipboard-diag.mjs），所以桌面壳直接读本机端点
// /api/clipboard（Go 读系统剪贴板，仅回环可达，见 clipboard_windows.go）；
// 网页模式 readText 行为正常（有权限提示），走标准 API。
async function clipboardForPaste() {
  if (document.documentElement.classList.contains('desktop-shell')) {
    try { return await api('/api/clipboard'); } catch (e) { return { text: '' }; }
  }
  try { return { text: await navigator.clipboard.readText() }; } catch (e) { return { text: '' }; }
}
async function pasteIntoTarget(ed) {
  const clip = await clipboardForPaste();
  if (clip && clip.text) { insertTextUndoable(ed, clip.text); return; }
  const view = ed === mainView.input ? mainView : (ed === subView.input ? subView : null);
  if (!view) return;
  // 文本为空：剪贴板里可能是截图 → 与 Ctrl+V 同口径入附件（主/子对话输入框）。
  // 桌面壳用 /api/clipboard 带回的 PNG（截图工具常连带放置该格式）；网页模式
  // 走 clipboard.read()。都拿不到就静默（与原生粘贴空剪贴板一致）。
  if (clip && clip.png) {
    const bin = atob(clip.png);
    const bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    stageFiles([new File([bytes], '剪贴板图片.png', { type: 'image/png' })], view);
    return;
  }
  if (document.documentElement.classList.contains('desktop-shell')) return; // read() 同样会挂
  try {
    const items = await navigator.clipboard.read();
    const files = [];
    for (const it of items) {
      const type = (it.types || []).find((x) => x.startsWith('image/'));
      if (!type) continue;
      const blob = await it.getType(type);
      const ext = (type.split('/')[1] || 'png').replace('jpeg', 'jpg');
      files.push(new File([blob], '剪贴板图片.' + ext, { type }));
    }
    if (files.length) stageFiles(files, view);
  } catch (e) { /* 剪贴板整体不可读：静默无操作 */ }
}
// textarea/input 的选区文本自己就能取到：直接 writeText，不赌 execCommand 的
// 用户激活语义；删除选区仍走 execCommand('delete') 保住撤销栈
function copySelection(ed) {
  edFocus(ed);
  if (typeof ed.selectionStart === 'number') {
    navigator.clipboard.writeText(ed.value.slice(ed.selectionStart, ed.selectionEnd)).catch(() => {});
  } else {
    document.execCommand('copy'); // contenteditable：交给原生
  }
}
function cutSelection(ed) {
  edFocus(ed);
  if (typeof ed.selectionStart === 'number') {
    const st = ed.selectionStart, en = ed.selectionEnd;
    navigator.clipboard.writeText(ed.value.slice(st, en)).catch(() => {});
    if (!document.execCommand('delete')) {
      ed.setRangeText('', st, en, 'end');
      ed.dispatchEvent(new Event('input', { bubbles: true }));
    }
  } else {
    document.execCommand('cut');
  }
}

/* ---------- 打开 / 定位 / 复制图片（复用既有端点与右栏浏览器） ---------- */

function viewFromNode(n) { return n.closest('#sub-msg-col') ? subView : mainView; }
function openURLSystem(url) {
  api('/api/open-url', { method: 'POST', body: JSON.stringify({ url }) })
    .catch((e) => alert('打开失败：' + e.message));
}
function openWorkspacePath(view, sid, path) {
  const cur = view.state.cur;
  if (cur && cur.remote) { openInPanelBrowser({ session: sid, path }); return; } // 远程工作区：右栏看内容
  api('/api/sessions/' + encodeURIComponent(sid) + '/open-path', {
    method: 'POST', body: JSON.stringify({ path }),
  }).catch((e) => alert('打开失败：' + e.message));
}
function revealWorkspacePath(view, sid, path) {
  const cur = view.state.cur;
  if (cur && cur.remote) { alert('这是远程工作区里的文件，本机没有对应路径，无法在文件夹中定位。'); return; }
  api('/api/sessions/' + encodeURIComponent(sid) + '/reveal-path', {
    method: 'POST', body: JSON.stringify({ path }),
  }).catch((e) => alert('定位失败：' + e.message));
}
async function copyImageToClipboard(url) {
  try {
    const blob = await (await fetch(url)).blob();
    if (!blob) throw new Error('读取图片失败');
    let payload = blob;
    if (blob.type !== 'image/png') { // 剪贴板图片只收 PNG：其他格式经 canvas 转一道
      const bmp = await createImageBitmap(blob);
      const cv = document.createElement('canvas');
      cv.width = bmp.width; cv.height = bmp.height;
      cv.getContext('2d').drawImage(bmp, 0, 0);
      payload = await new Promise((ok) => cv.toBlob(ok, 'image/png'));
    }
    await navigator.clipboard.write([new ClipboardItem({ 'image/png': payload })]);
  } catch (e) { alert('复制图片失败：' + (e && e.message ? e.message : e)); }
}

/* ---------- 各上下文的菜单项组装 ---------- */

function editMenuItems(ed) {
  const hasSel = edHasSelection(ed);
  return [
    { label: '撤销', fn: () => { edFocus(ed); document.execCommand('undo'); } },
    { label: '重做', fn: () => { edFocus(ed); document.execCommand('redo'); } },
    { sep: true },
    { label: '剪切', disabled: !hasSel, fn: () => cutSelection(ed) },
    { label: '复制', disabled: !hasSel, fn: () => copySelection(ed) },
    { label: '粘贴', fn: () => pasteIntoTarget(ed) },
    { sep: true },
    { label: '全选', fn: () => { edFocus(ed); if (ed.select) ed.select(); else document.execCommand('selectAll'); } },
  ];
}
function selectionMenuItems(text) {
  return [
    { label: '复制', fn: () => navigator.clipboard.writeText(text).catch(() => {}) },
    { label: '引用到输入框', fn: () => setQuote(mainView, text) },
    { label: '问AI', fn: () => askInAux(text) },
    { sep: true },
    ttsSpeaking() ? { label: '停止朗读', fn: ttsStop } : { label: '朗读', fn: () => ttsSpeak(text) },
  ];
}
function bubbleMenuItems(msgEl) {
  const body = msgEl.querySelector('.md') || msgEl.querySelector('.bubble');
  const text = body ? body.innerText : '';
  const items = [];
  if (text) items.push({ label: '复制全文', fn: () => navigator.clipboard.writeText(text).catch(() => {}) });
  items.push(ttsSpeaking()
    ? { label: '停止朗读', fn: ttsStop }
    : { label: '朗读', fn: () => ttsSpeak(text) });
  return items;
}
function linkMenuItems(a) {
  const url = a.getAttribute('href') || '';
  if (!url) return null;
  return [
    { label: '打开', fn: () => openInPanelBrowser({ url }) },
    { label: '在系统浏览器中打开', fn: () => openURLSystem(url) },
    { sep: true },
    { label: '复制链接', fn: () => navigator.clipboard.writeText(url).catch(() => {}) },
  ];
}
function fileMenuItems(view, sid, path) {
  if (!sid || !path) return null;
  return [
    { label: '预览（右栏）', fn: () => openInPanelBrowser({ session: sid, path }) },
    { label: '在系统中打开', fn: () => openWorkspacePath(view, sid, path) },
    { label: '在文件夹中定位', fn: () => revealWorkspacePath(view, sid, path) },
    { sep: true },
    { label: '复制路径', fn: () => navigator.clipboard.writeText(path).catch(() => {}) },
  ];
}
function deliveryMenuItems(view, sid, path) {
  const d = { path }; // deliveryOpenFile/Reveal 只读 path
  return [
    { label: '打开', fn: () => deliveryOpenFile(view, sid, d) },
    { label: '在文件夹中查看', fn: () => deliveryRevealFile(view, sid, d) },
    { sep: true },
    { label: '预览（右栏）', fn: () => openInPanelBrowser({ session: sid, path }) },
    { label: '复制路径', fn: () => navigator.clipboard.writeText(path).catch(() => {}) },
  ];
}
function imageMenuItems(a) {
  return [
    { label: '在右栏查看', fn: () => openInPanelBrowser({ url: a.href }) },
    { label: '复制图片', fn: () => copyImageToClipboard(a.href) },
  ];
}

/* ---------- 上下文识别与分发 ---------- */

// 可编辑目标：textarea、常规 input（排除勾选/按钮/文件类）、contenteditable
function editableTarget(t) {
  const ed = t.closest ? t.closest('textarea, input, [contenteditable]') : null;
  if (!ed) return null;
  if (ed.tagName === 'TEXTAREA') return ed;
  if (ed.tagName === 'INPUT') {
    return ['checkbox', 'radio', 'file', 'button', 'submit', 'reset', 'color', 'range', 'hidden'].includes(ed.type) ? null : ed;
  }
  return ed.isContentEditable ? ed : null;
}

function buildCtxMenuItems(e) {
  const t = e.target;
  if (!t || !t.closest) return null;

  // 「对象」类目标优先（与原生浏览器一致：链接上的右键不看文字选区）
  const link = t.closest('a.ext-link');
  if (link) return linkMenuItems(link);
  const flink = t.closest('a.file-link');
  if (flink) {
    const view = viewFromNode(flink);
    return fileMenuItems(view, view.state.cur && view.state.cur.id, flink.getAttribute('data-path'));
  }
  const drow = t.closest('.dc-file');
  if (drow && drow.dataset.path) {
    const view = viewFromNode(drow);
    const sid = view.state.cur && view.state.cur.id;
    if (sid) return deliveryMenuItems(view, sid, drow.dataset.path);
  }
  const imgA = t.closest('.tc-imgs a');
  if (imgA && imgA.href) return imageMenuItems(imgA);

  const ed = editableTarget(t);
  if (ed) return editMenuItems(ed);

  const sel = chatSelection();
  if (sel) { hideSelPop(); return selectionMenuItems(sel.text); }

  const msgEl = t.closest('.msg');
  if (msgEl) return bubbleMenuItems(msgEl);

  return null; // 空白处等：不弹菜单（维持现状）
}

// 仅桌面形态注册；左栏会话/工作区自己的右键菜单（preventDefault 过）让路
if (document.documentElement.classList.contains('desktop-shell')) {
  document.addEventListener('contextmenu', (e) => {
    if (e.defaultPrevented) return;
    let items = buildCtxMenuItems(e);
    // 右键落在已打开的菜单本体上（同点连击时菜单正好盖住底下的目标）：临时
    // 藏一下取菜单底下的真实目标重建菜单——与原生「重开菜单」的语义一致
    if (items === null && e.target.closest && e.target.closest('#ctx-menu')) {
      const m = document.getElementById('ctx-menu');
      m.style.visibility = 'hidden';
      const under = document.elementFromPoint(e.clientX, e.clientY);
      m.style.visibility = '';
      if (under) items = buildCtxMenuItems({ target: under, clientX: e.clientX, clientY: e.clientY });
    }
    if (!items || !items.length) return;
    e.preventDefault();
    openCtxMenu(items, e.clientX, e.clientY);
  });
}
