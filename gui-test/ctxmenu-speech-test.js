// 右键菜单「朗读」桩测（不需要起服务、不碰浏览器）：从源码里原样抽取菜单组装
// 函数与语音组件的 ctxMenuFn，喂最小 DOM 桩跑一遍，断言：
//   1) 每一类右键菜单末尾都带「朗读」项（选区/气泡/链接/文件/交付行/图片/输入框
//      + 左栏会话项、左栏工作区头、右栏子对话项）；
//   2) 朗读取值口径：选区优先，否则条目可见文本；代码块/按钮不念；文件读文件名+路径；
//   3) 语音组件正在朗读时，任意菜单的该项都变成「停止朗读」（单飞），且点击停止；
//   4) 语音组件未启用（未注册贡献）时，任何菜单里都不出现朗读项——含分隔线不留尾巴；
//      语义为空（比如选中的只有一段代码）时同样不出现，绝不出现点了没反应的条目。
// 运行：仓库根目录 node gui-test/ctxmenu-speech-test.js
const { readFileSync } = require('fs');
const pathMod = require('path');

const ROOT = pathMod.join(__dirname, '..');
const srcCore = readFileSync(pathMod.join(ROOT, 'web', 'app-core.js'), 'utf8');
const srcCtx = readFileSync(pathMod.join(ROOT, 'web', 'app-ctxmenu.js'), 'utf8');
const srcLeft = readFileSync(pathMod.join(ROOT, 'web', 'app-left.js'), 'utf8');
const srcRight = readFileSync(pathMod.join(ROOT, 'web', 'app-right.js'), 'utf8');
const srcVoice = readFileSync(pathMod.join(ROOT, '组件', 'voice', 'client.js'), 'utf8');

// ---------- 源码抽取（按大括号配对取整段函数，容忍函数体内的注释与模板串） ----------
function grabFn(src, name) {
  const at = src.indexOf('function ' + name + '(');
  if (at < 0) throw new Error('源码里找不到函数：' + name);
  let depth = 0;
  const open = src.indexOf('{', at);
  for (let i = open; i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}') { depth--; if (!depth) return src.slice(at, i + 1); }
  }
  throw new Error('大括号不配对：' + name);
}
function grabArrow(src, marker) {
  const at = src.indexOf(marker);
  if (at < 0) throw new Error('源码里找不到：' + marker);
  let depth = 0;
  const open = src.indexOf('{', at);
  for (let i = open; i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}') { depth--; if (!depth) return src.slice(at, i + 1); }
  }
  throw new Error('大括号不配对：' + marker);
}

const coreFns = ['speechTextOf', 'speechSelectionIn', 'ctxSpeakItems', 'speechFileName', 'speechPathText']
  .map((n) => grabFn(srcCore, n)).join('\n');
const consts = srcCore.match(/const SPEECH_SKIP_TAGS = \[[^\]]*\];/)[0];
const ctxFns = ['edHasSelection', 'editableTarget', 'viewFromNode', 'editMenuItems', 'selectionMenuItems',
  'bubbleMenuItems', 'linkMenuItems', 'fileMenuItems', 'deliveryMenuItems', 'imageMenuItems',
  'buildCtxMenuItems'].map((n) => grabFn(srcCtx, n)).join('\n');
const leftFns = ['sessionSpeechText', 'workspaceSpeechText', 'openSessionMenu']
  .map((n) => grabFn(srcLeft, n)).join('\n');
const rightFns = grabFn(srcRight, 'openSubItemMenu');
const cleanFn = grabFn(srcVoice, 'cleanForSpeech');
const voiceCtxFn = grabArrow(srcVoice, 'ctxMenuFn = (ctx) =>');

// ---------- 最小 DOM 桩 ----------
function matches(node, sel) {
  if (!node || node.nodeType !== 1) return false;
  return String(sel).split(',').some((raw) => {
    const s = raw.trim();
    if (!s) return false;
    if (s[0] === '#') return node.id === s.slice(1);
    if (s[0] === '[') { // [contenteditable] 这类属性选择器：只认 contenteditable
      return s.slice(1, -1).split('=')[0].trim() === 'contenteditable' && !!node.isContentEditable;
    }
    // 支持 "a.file-link" / ".md" / "div"
    const m = s.match(/^([a-zA-Z]+)?(\.[\w-]+)?$/);
    if (!m) return false;
    if (m[1] && node.tagName !== m[1].toUpperCase()) return false;
    if (m[2] && !node.classes.has(m[2].slice(1))) return false;
    return true;
  });
}
function el(tag, o = {}) {
  const e = {
    nodeType: 1, tagName: String(tag).toUpperCase(), id: o.id || '', classes: new Set(o.cls || []),
    attrs: Object.assign({}, o.attrs || {}), dataset: o.dataset || {}, childNodes: [],
    value: o.value, type: o.type, isContentEditable: !!o.ce, href: o.href || '',
    innerText: o.innerText || '', _parent: null,
  };
  // textarea/input 在真 DOM 里 selectionStart/End 恒为数字（光标位置），桩里补上，
  // 否则「有选中→念选中、否则念全文」的分支走不到
  if (e.tagName === 'TEXTAREA' || e.tagName === 'INPUT') {
    if (typeof o.selectionStart === 'number') { e.selectionStart = o.selectionStart; e.selectionEnd = o.selectionEnd; }
    else { e.selectionStart = e.selectionEnd = String(o.value || '').length; }
  }
  e.classList = {
    contains: (c) => e.classes.has(c), add: (c) => e.classes.add(c), remove: (c) => e.classes.delete(c),
    toggle: (c, on) => { if (on === undefined) { e.classes.has(c) ? e.classes.delete(c) : e.classes.add(c); } else if (on) e.classes.add(c); else e.classes.delete(c); },
  };
  e.appendChild = (k) => { k._parent = e; e.childNodes.push(k); return k; };
  e.append = (...ks) => { ks.forEach((k) => e.appendChild(k)); return e; };
  e.getAttribute = (n) => (n in e.attrs ? e.attrs[n] : null);
  Object.defineProperty(e, 'parentElement', { get: () => e._parent });
  Object.defineProperty(e, 'textContent', { get: () => flatText(e) });
  e.contains = (n) => { for (let p = n; p; p = p._parent) if (p === e) return true; return false; };
  e.closest = (sel) => { for (let p = e; p; p = p._parent) if (matches(p, sel)) return p; return null; };
  e.querySelector = (sel) => {
    for (const k of allDesc(e)) if (matches(k, sel)) return k;
    return null;
  };
  e.getBoundingClientRect = () => ({ left: 0, top: 0, right: 120, bottom: 24, width: 120, height: 24 });
  e.focus = () => {}; e.select = () => {}; e.isConnected = true;
  return e;
}
function txt(s) {
  const t = { nodeType: 3, nodeValue: s, _parent: null };
  Object.defineProperty(t, 'parentElement', { get: () => t._parent });
  return t;
}
function kids(n) { return n.childNodes || []; }
function allDesc(n) { const out = []; for (const k of kids(n)) { if (k.nodeType === 1) { out.push(k); out.push(...allDesc(k)); } } return out; }
function flatText(n) { return (n.nodeType === 3 ? n.nodeValue : kids(n).map(flatText).join(' ')); }

// ---------- 装配被测环境 ----------
function buildEnv() {
  const win = { getSelection: () => sel };
  const doc = { activeElement: null, execCommand: () => true, documentElement: el('html') };
  const nav = { clipboard: { writeText: () => Promise.resolve(), readText: () => Promise.resolve('') } };

  const body = `
    var window = __window, document = __document, navigator = __navigator;
    var mainView = { state: { cur: { id: 's1' } } }, subView = { state: { cur: { id: 's2' } } };
    var binView = false;
    var state = { cur: null };
    var CX = { isActive: function () { return false; } };
    var lastMenu = null, speakLog = [], stopLog = 0;
    var active = true, playingBtn = null, ttsStopChain = null;
    function openCtxMenu(items, x, y) { lastMenu = items; }
    function api() { return Promise.resolve({}); }
    function prompt() { return null; }
    function alert() {}
    function loadSessions() {}
    function resetEmpty() {}
    function openSession() {}
    function upsertSessionBrief() {}
    function restoreSession() {}
    function deleteSession() {}
    function lsGet(k, d) { return d; }
    function lsSet() {}
    function baseName(p) { return String(p || '').split(/[\\\\/]/).filter(Boolean).pop() || ''; }
    function wsOf(s) { return s.workspace || '/ws/default'; }
    function setQuote() {}
    function askInAux() {}
    function hideSelPop() {}
    function chatSelection() { return null; }
    function deliveryOpenFile() {}
    function deliveryRevealFile() {}
    function openInPanelBrowser() {}
    function openURLSystem() {}
    function openWorkspacePath() {}
    function revealWorkspacePath() {}
    function copyImageToClipboard() {}
    function cutSelection() {}
    function copySelection() {}
    function pasteIntoTarget() {}
    function edFocus() {}
    var subState = { cur: null };
    function subClearCurrent() {}
    function refreshDrawer() {}
    function stopSpeak() { stopLog++; playingBtn = null; ttsStopChain = null; }
    function speakPlain(t) { speakLog.push(t); }
    ${consts}
    ${coreFns}
    ${ctxFns}
    ${leftFns}
    ${rightFns}
    ${cleanFn}
    var ctxMenuFn = null;
    ${voiceCtxFn}
    var JX = {
      _fns: [ctxMenuFn],
      ctxMenuItems: function (ctx) {
        var out = [];
        for (var i = 0; i < this._fns.length; i++) {
          var items = this._fns[i](ctx);
          if (items && items.length) out = out.concat(items);
        }
        lastCtx = ctx;
        return out;
      },
    };
    window.JX = JX;
    return {
      JX: JX,
      ctxSpeakItems: ctxSpeakItems, speechTextOf: speechTextOf, speechSelectionIn: speechSelectionIn,
      speechPathText: speechPathText, speechFileName: speechFileName,
      editMenuItems: editMenuItems, selectionMenuItems: selectionMenuItems, bubbleMenuItems: bubbleMenuItems,
      linkMenuItems: linkMenuItems, fileMenuItems: fileMenuItems, deliveryMenuItems: deliveryMenuItems,
      imageMenuItems: imageMenuItems, buildCtxMenuItems: buildCtxMenuItems,
      openSessionMenu: openSessionMenu, openSubItemMenu: openSubItemMenu,
      sessionSpeechText: sessionSpeechText, workspaceSpeechText: workspaceSpeechText,
      getMenu: function () { return lastMenu; },
      setBusy: function (v) { playingBtn = v ? { classList: { add: function () {}, remove: function () {} } } : null; ttsStopChain = v ? function () {} : null; },
      speakLog: speakLog,
      stopLogRef: function () { return stopLog; },
      getCtx: function () { return lastCtx; },
      setActive: function (v) { active = v; },
      getSelectionFn: function () { return window.getSelection; },
    };
    var lastCtx = null;
  `;
  const fn = new Function('__window', '__document', '__navigator', body);
  const env = fn(win, doc, nav);
  env.window = win;
  env.document = doc;
  return env;
}

// 选区桩：test 里按需设定
let sel = { isCollapsed: true, rangeCount: 0, anchorNode: null, focusNode: null, toString: () => '' };
function selectInside(node, text) {
  sel = { isCollapsed: false, rangeCount: 1, anchorNode: node, focusNode: node, toString: () => text };
}
function clearSel() { sel = { isCollapsed: true, rangeCount: 0, anchorNode: null, focusNode: null, toString: () => '' }; }

// ---------- 断言工具 ----------
let pass = 0, fail = 0;
function ok(name, cond, detail) {
  if (cond) { pass++; console.log('PASS ' + name); }
  else { fail++; console.log('FAIL ' + name + (detail ? '  | ' + detail : '')); }
}
const labels = (items) => (items || []).filter((it) => !it.sep).map((it) => it.label);
const speakLabel = (items) => labels(items).filter((l) => l === '朗读' || l === '停止朗读');

// ---------- 1. 取文本口径：跳过代码块/按钮，选区只在目标内部才算 ----------
{
  const env = buildEnv();
  const body = el('div', { cls: ['md'] });
  body.append(txt('这里是正文。'));
  const pre = el('pre'); pre.append(txt('const a = 1; doNotRead();'));
  body.append(pre);
  body.append(txt('代码后面的话。'));
  const btn = el('button'); btn.append(txt('复制全文'));
  body.append(btn);
  const t = env.speechTextOf(body);
  ok('取值：跳过 <pre> 代码块与按钮文字', !/doNotRead/.test(t) && !/复制全文/.test(t) && /这里是正文/.test(t) && /代码后面的话/.test(t), t);

  // 选区在目标内部 → 认；在别处 → 不认
  const other = el('div'); other.append(txt('别处选的一段'));
  clearSel();
  selectInside(other.childNodes[0], '别处选的一段');
  ok('取值：目标外部的选区不被采纳', env.speechSelectionIn(body) === '');
  selectInside(body.childNodes[0], '这里是正文');
  ok('取值：目标内部的选区被采纳', env.speechSelectionIn(body) === '这里是正文');

  ok('取值：文件路径念「文件名 + 路径」',
    env.speechPathText('D:\\ws\\报告 v2.md') === '文件 报告 v2.md，路径 D:\\ws\\报告 v2.md',
    env.speechPathText('D:\\ws\\报告 v2.md'));
  ok('取值：路径末段即文件名时只说一次', env.speechPathText('报告.md') === '文件 报告.md');
}

// ---------- 2. 每类菜单都带朗读项（组件启用、空闲态） ----------
function menus(env) {
  const list = [];
  const push = (name, items, kind) => { if (items) list.push([name, items, kind, env.getCtx()]); };
  // 输入框（textarea 内容作 fallback）
  const ta = el('textarea', { value: '这段草稿要念' });
  push('输入框', env.editMenuItems(ta), 'edit');
  // 对话区选区
  push('选区', env.selectionMenuItems('被选中的一句话'), 'selection');
  // 消息气泡（带代码块）
  const msg = el('div', { cls: ['msg'] });
  const md = el('div', { cls: ['md'] });
  md.append(txt('气泡正文一。'));
  const pre = el('pre'); pre.append(txt('x = 1'));
  md.append(pre);
  msg.append(md);
  push('气泡', env.bubbleMenuItems(msg), 'bubble');
  // 网页链接
  const link = el('a', { cls: ['ext-link'], attrs: { href: 'https://example.com/a' } });
  link.append(txt('示例站点'));
  push('链接', env.linkMenuItems(link), 'link');
  // 文件路径链接
  const flink = el('a', { cls: ['file-link'], attrs: { 'data-path': 'D:\\ws\\a.md' } });
  flink.append(txt('D:\\ws\\a.md'));
  const view = { state: { cur: { id: 's1' } } };
  push('文件', env.fileMenuItems(view, 's1', 'D:\\ws\\a.md', flink), 'file');
  // 交付卡文件行
  const drow = el('div', { cls: ['dc-file'], dataset: { path: 'out\\报告.docx' } });
  drow.append(txt('报告.docx'), txt('12.3 KB'));
  push('交付行', env.deliveryMenuItems(view, 's1', 'out\\报告.docx', drow), 'delivery');
  // 图片链接（工具调用截图）
  const imgA = el('a', { cls: ['tc-imgs-a'], href: 'http://127.0.0.1:8830/api/shot/abc.png' });
  const img = el('img'); imgA.append(img);
  push('图片', env.imageMenuItems(imgA), 'image');
  // 左栏会话项
  clearSel();
  const s = { id: 's9', title: '周报整理', workspace: 'D:\\ws\\x' };
  env.openSessionMenu(s, { right: 100, bottom: 50 }, el('div', { cls: ['session-item'] }));
  push('左栏会话项', env.getMenu(), 'session');
  // 右栏子对话项
  env.openSubItemMenu({ id: 's10', title: '子对话一', mode: 'aux' }, { right: 100, bottom: 50 }, el('div'));
  push('右栏子对话项', env.getMenu(), 'sub');
  return list;
}
{
  const env = buildEnv();
  for (const [name, items, kind, ctx] of menus(env)) {
    const has = speakLabel(items);
    ok('菜单含朗读项：' + name, has.length === 1 && has[0] === '朗读', JSON.stringify(labels(items)));
    ok('朗读项在末尾（前面有分隔线）：' + name,
      items[items.length - 2] && items[items.length - 2].sep === true && items[items.length - 1].label === '朗读',
      JSON.stringify(labels(items)));
    ok('朗读取值 kind=' + kind, ctx && ctx.kind === kind, ctx && ctx.kind);
  }
  // 取值细节
  const env2 = buildEnv();
  const msg = el('div', { cls: ['msg'] });
  const md = el('div', { cls: ['md'] }); md.append(txt('气泡正文。')); const pre = el('pre'); pre.append(txt('x=1')); md.append(pre);
  msg.append(md);
  clearSel();
  env2.bubbleMenuItems(msg);
  ok('气泡朗读不含代码块', env2.getCtx().text === '气泡正文。', env2.getCtx().text);
  const ta = el('textarea', { value: '草稿全文' });
  env2.editMenuItems(ta);
  ok('输入框空闲时念框内内容', env2.getCtx().text === '草稿全文', env2.getCtx().text);
  const flink2 = el('a', { cls: ['file-link'], attrs: { 'data-path': '/ws/b.md' } });
  env2.fileMenuItems({ state: { cur: { id: 's1' } } }, 's1', '/ws/b.md', flink2);
  ok('文件项念文件名 + 路径', env2.getCtx().text === '文件 b.md，路径 /ws/b.md', env2.getCtx().text);
  const drow2 = el('div', { cls: ['dc-file'], dataset: { path: 'out/x.docx' } });
  drow2.append(txt('x.docx'), txt('9 KB'));
  env2.deliveryMenuItems({ state: { cur: { id: 's1' } } }, 's1', 'out/x.docx', drow2);
  ok('交付行念行内文字 + 路径', /x\.docx 9 KB，路径 out.x\.docx/.test(env2.getCtx().text), env2.getCtx().text);
  // 会话项 / 工作区头 / 子对话项
  clearSel();
  env2.openSessionMenu({ id: 's9', title: '周报整理', workspace: 'D:\\ws\\x' }, { right: 1, bottom: 1 }, el('div'));
  ok('会话项念「标题，工作区」', env2.getCtx().text === '周报整理，工作区 x', env2.getCtx().text);
  ok('工作区头念「工作区 名，路径」', env2.workspaceSpeechText('D:\\ws\\x') === '工作区 x，路径 D:\\ws\\x', env2.workspaceSpeechText('D:\\ws\\x'));
  env2.openSubItemMenu({ id: 's10', title: '子对话一', mode: 'aux' }, { right: 1, bottom: 1 }, el('div'));
  ok('子对话项念说话人 + 标题', env2.getCtx().text === '人工子对话，子对话一', env2.getCtx().text);
  // 输入框有选中：念选中而不是全文
  const ta2 = el('textarea', { value: '开头 中间 结尾' });
  ta2.selectionStart = 3; ta2.selectionEnd = 5;
  env2.editMenuItems(ta2);
  ok('输入框有选中时念选中', env2.getCtx().text === '中间', env2.getCtx().text);
}

// ---------- 3. 朗读中：任何菜单的该项都变「停止朗读」，点击即停（单飞） ----------
{
  const env = buildEnv();
  env.setBusy(true);
  for (const [name, items] of menus(env)) {
    const has = speakLabel(items);
    ok('朗读中变停止朗读：' + name, has.length === 1 && has[0] === '停止朗读', JSON.stringify(labels(items)));
  }
  const items = env.selectionMenuItems('一段选中的话');
  const stopItem = items[items.length - 1];
  const before = env.stopLogRef();
  stopItem.fn();
  ok('点停止朗读真的停', env.stopLogRef() === before + 1);

  // 空闲态：点「朗读」开始念（清洁后的文本），并顶掉上一条
  env.setBusy(false);
  const env3 = buildEnv();
  const li = env3.selectionMenuItems('**加粗**的一句话');
  ok('空闲时是朗读项', li[li.length - 1].label === '朗读');
  li[li.length - 1].fn();
  ok('点朗读真的念（且已剥 markdown）', env3.speakLog.length === 1 && env3.speakLog[0] === '加粗的一句话', JSON.stringify(env3.speakLog));
}

// ---------- 4. 组件未启用 / 无可念内容：不出现朗读项，也不留多余分隔线 ----------
{
  const env = buildEnv();
  env.JX._fns = []; // 组件关掉：JX 里没有贡献者（真实链路是 unregisterCtxMenu）
  for (const [name, items] of menus(env)) {
    ok('组件未启用时无朗读项：' + name, speakLabel(items).length === 0, JSON.stringify(labels(items)));
    const tail = items[items.length - 1];
    ok('组件未启用时不留下尾部分隔线：' + name, !(tail && tail.sep), JSON.stringify(labels(items)));
  }
  // 语义为空（选中的只有一段代码）：也不出条目，避免死点
  const env2 = buildEnv();
  const onlyCode = env2.selectionMenuItems('```js\nconst a = 1;\n```');
  ok('内容剥壳后为空时不出朗读项', speakLabel(onlyCode).length === 0, JSON.stringify(labels(onlyCode)));
  env2.setBusy(true);
  const onlyCodeBusy = env2.selectionMenuItems('```js\nconst a = 1;\n```');
  ok('内容为空但正在朗读时仍给「停止朗读」',
    speakLabel(onlyCodeBusy).join() === '停止朗读', JSON.stringify(labels(onlyCodeBusy)));
}

// ---------- 5. 静态检查：左栏工作区头（内联组装、不便注射）两处都接了朗读 ----------
{
  const wsHooks = srcLeft.match(/ctxSpeakItems\(\{ kind: 'workspace'/g) || [];
  ok('左栏工作区头（正常视图 + 回收站）都接了朗读', wsHooks.length === 2, String(wsHooks.length));
  const allKinds = ['selection', 'bubble', 'link', 'file', 'delivery', 'image', 'edit', 'session', 'workspace', 'sub'];
  const used = ['app-ctxmenu.js', 'app-left.js', 'app-right.js']
    .map((f) => readFileSync(pathMod.join(ROOT, 'web', f), 'utf8')).join('\n');
  const missing = allKinds.filter((k) => used.indexOf("kind: '" + k + "'") < 0);
  ok('全部 10 类右键目标都接了朗读', missing.length === 0, 'missing=' + missing.join(','));
}

console.log(`\nctxmenu-speech: ${pass} pass, ${fail} fail`);
if (fail) process.exit(2);
