/* 市场前端（market.go 的浏览器半边）：左侧栏底部「市场」按钮进入的主区整页视图。
   技能 / 组件 / 插件 三个 tab + 搜索 + 一键安装/更新/卸载：
     · 全部操作走 /api/market（挂在 /api/ 下，远程手机端经中继隧道天然可用）；
     · 组件装/卸后调 CX.refresh() 热加载/热卸载，不刷新页面；
     · 插件 tab 底部保留「管理外部 Claude 市场」入口（复用设置页 ccplugins 分区）；
     · 未配置市场服务器时给出建议地址一键填入（与中继网关同 host 的 jxai-server）。
   样式在 style.css 尾部 mk-* 前缀；复用 app-core.js 的 $ / api / esc / confirmCenter。 */

/* ---------- 视图开关（body.market-mode 占领主区，回收站 bin-mode 同款机制） ---------- */

let mkData = null;        // GET /api/market 的最近一次结果
let mkTab = 'skill';      // 当前 tab：skill | component | plugin
let mkLoading = false;    // 清单在途（防抖动重复渲染）

function marketOpen() {
  return document.body.classList.contains('market-mode');
}

function setMarketView(on) {
  document.body.classList.toggle('market-mode', on);
  const view = $('market-view');
  if (view) view.classList.toggle('hidden', !on); // .hidden 是全局 display:none!important，必须跟着切
  const btn = $('btn-market');
  if (btn) btn.classList.toggle('active', on);
  if (!on) mkuClose(); // 离开市场视图时收起上传技能弹层（上传中不关）
  if (on) loadMarket(false);
}

function toggleMarket() {
  if (marketOpen()) closeMarket();
  else openMarket();
}

function openMarket() {
  setMarketView(true);
  const search = $('mk-search');
  if (search) setTimeout(() => search.focus(), 60);
}

function closeMarket() { setMarketView(false); }

/* ---------- 数据加载与渲染 ---------- */

async function loadMarket(force) {
  const list = $('mk-list');
  if (!list) return;
  if (mkLoading) return;
  mkLoading = true;
  if (force && mkData) list.innerHTML = '<div class="mk-hint">正在刷新清单…</div>';
  else if (!mkData) list.innerHTML = '<div class="mk-hint">正在加载…</div>';
  try {
    mkData = await api('/api/market' + (force ? '?refresh=1' : ''));
  } catch (e) {
    mkData = null;
    mkLoading = false;
    renderMarketState(esc('市场读取失败：' + e.message));
    return;
  }
  mkLoading = false;
  renderMarket();
}

// renderMarketState 收 HTML 片段（调用方负责转义服务端/异常文本）
function renderMarketState(html) {
  const list = $('mk-list');
  if (list) list.innerHTML = '<div class="mk-hint">' + html + '</div>';
  const count = $('mk-count');
  if (count) count.textContent = '';
}

function renderMarket() {
  const ubtn = $('mk-upload-btn'); // 「＋ 上传技能」只在技能 tab 出现
  if (ubtn) ubtn.classList.toggle('hidden', mkTab !== 'skill');
  if (!mkData) return renderMarketState('清单为空');
  const server = $('mk-server');
  if (server) {
    server.textContent = mkData.configured ? (mkData.name || mkData.url || '已配置') : '未配置';
    server.classList.toggle('off', !mkData.configured);
  }
  const ext = $('mk-ext-market');
  if (ext) ext.classList.toggle('hidden', mkTab !== 'plugin');
  if (!mkData.configured) { renderUnconfigured(); return; }
  if (mkData.error) {
    renderMarketState(esc(mkData.error) + '（<span class="mk-link" id="mk-retry">重试</span> 或到 设置 · 网络访问 检查市场地址）');
    const retry = $('mk-retry');
    if (retry) retry.onclick = () => loadMarket(true);
    return;
  }
  renderTab();
}

function renderUnconfigured() {
  const list = $('mk-list');
  if (!list) return;
  const sug = mkData.suggested || '';
  list.innerHTML =
    '<div class="mk-hint">还没配置市场服务器。市场由 jxai-server 提供（部署一次，技能/组件/插件装这一处）。' +
    (sug
      ? '<br>检测到远程接入网关，推荐直接用同一台服务器：' +
        '<div class="mk-suggest"><code>' + esc(sug) + '</code></div>' +
        '<div class="mk-suggest-acts"><button id="mk-use-suggested" class="mk-act">填入此地址</button>' +
        '<button id="mk-go-settings" class="mk-act ghost">手动设置</button></div>'
      : '<br>到 设置 · 网络访问 · 市场服务器 填入清单地址。' +
        '<div class="mk-suggest-acts"><button id="mk-go-settings" class="mk-act">去设置</button></div>') +
    '</div>';
  const use = $('mk-use-suggested');
  if (use) use.onclick = async () => {
    use.disabled = true; use.textContent = '保存中…';
    try {
      await api('/api/config', { method: 'POST', body: JSON.stringify({ marketURL: sug }) });
      mkData = null;
      await loadMarket(true);
    } catch (e) { alert('保存失败：' + e.message); use.disabled = false; use.textContent = '填入此地址'; }
  };
  const go = $('mk-go-settings');
  if (go) go.onclick = () => { if (typeof openSettings === 'function') openSettings('network'); };
}

function entriesOfTab(tab) {
  if (!mkData) return [];
  if (tab === 'skill') return mkData.skills || [];
  if (tab === 'component') return mkData.components || [];
  return mkData.plugins || [];
}

function renderTab() {
  const list = $('mk-list');
  if (!list) return;
  const q = ($('mk-search').value || '').trim().toLowerCase();
  const all = entriesOfTab(mkTab);
  const items = q
    ? all.filter((e) => {
        const hay = ((e.name || '') + ' ' + (e.id || '') + ' ' + (e.description || '') + ' ' + (e.tags || []).join(' ')).toLowerCase();
        return hay.includes(q);
      })
    : all;
  const count = $('mk-count');
  if (count) count.textContent = q ? (all.length ? `匹配 ${items.length}/${all.length} 项` : '') : (all.length ? `共 ${all.length} 项` : '');
  if (!all.length) {
    list.innerHTML = '<div class="mk-hint">' + ({
      skill: '市场里还没有技能。让服务器管理员用 jxai-server publish 发布（或检查市场地址）。',
      component: '市场里还没有组件。',
      plugin: '市场里还没有插件。也可以添加外部 Claude Code 插件市场（右上角入口）。',
    })[mkTab] + '</div>';
    return;
  }
  if (!items.length) {
    list.innerHTML = '<div class="mk-hint">没有匹配「' + esc(q) + '」的条目</div>';
    return;
  }
  list.innerHTML = '';
  for (const e of items) list.appendChild(mkCard(e));
}

function mkCard(e) {
  const card = document.createElement('div');
  card.className = 'mk-card' + (e.installed ? ' installed' : '');
  const tags = (e.tags || []).slice(0, 4)
    .map((t) => `<span class="mk-tag">${esc(t)}</span>`).join('');
  card.innerHTML =
    `<div class="mk-card-head"><b title="${esc(e.id)}">${esc(e.name || e.id)}</b>` +
    `<span class="mk-ver" title="${esc(e.id)}">v${esc(e.version || '?')}</span></div>` +
    `<p class="mk-desc">${esc(e.description || '')}</p>` +
    (tags ? `<div class="mk-tags">${tags}</div>` : '') +
    `<div class="mk-card-foot"></div>`;
  const foot = card.querySelector('.mk-card-foot');

  const typeLabel = { skill: '技能', component: '组件', plugin: '插件' }[mkTab];
  if (e.installed) {
    const st = document.createElement('span');
    st.className = 'mk-installed';
    st.textContent = e.update ? `已装 v${e.installedVersion || '?'}（有新版）` : `已安装 v${e.installedVersion || e.version}`;
    foot.appendChild(st);
    if (e.update) foot.appendChild(mkAction('更新', e, 'install'));
    foot.appendChild(mkAction('卸载', e, 'uninstall'));
  } else {
    foot.appendChild(mkAction(`安装${typeLabel === '插件' ? '' : typeLabel}`, e, 'install'));
  }
  // 发布者管理（只能管自己发的）：条目 owner 与本机「发布者姓名」一致时，
  // 多出「更新发布 / 删除」——更新=同 id 发新版本，删除=从市场下架。
  const me = (mkData && mkData.me && mkData.me.name) || '';
  if (me && e.owner && e.owner === me) {
    foot.appendChild(mkPublishAction('更新发布', e, 'republish'));
    foot.appendChild(mkPublishAction('删除', e, 'unpublish'));
  }
  return card;
}

// mkAction 一张卡上的操作按钮（安装/更新/卸载），动作完成后整页重拉 + 组件热刷新。
function mkAction(label, e, action) {
  const btn = document.createElement('button');
  btn.className = 'mk-act' + (action === 'uninstall' ? ' ghost' : '');
  btn.textContent = label;
  btn.onclick = async () => {
    if (action === 'uninstall') {
      const ok = await confirmCenter(`卸载${e.installed ? '「' + (e.name || e.id) + '」' : ''}？本地文件与设置将一并删除。`);
      if (!ok) return;
    }
    btn.disabled = true;
    btn.textContent = action === 'install' ? '安装中…' : '卸载中…';
    try {
      await api('/api/market/' + action, {
        method: 'POST',
        body: JSON.stringify({ type: mkTab, id: e.id }),
      });
      if (mkTab === 'component' && window.CX && CX.refresh) {
        try { await CX.refresh(); } catch (err) { console.warn('[市场] 组件热刷新失败', err); }
      }
      mkData = null;           // 让 loadMarket 走真拉取（后端清单缓存也会被绕开与否不影响正确性）
      await loadMarket(true);
    } catch (err) {
      alert((action === 'install' ? '安装失败：' : '卸载失败：') + err.message);
      btn.disabled = false;
      btn.textContent = label;
    }
  };
  return btn;
}

// mkPublishAction 卡片上的「更新发布 / 删除」（只对自己发布的条目渲染）：
// 更新=同 id 发新版本（复用上传弹层、预填该条目元信息，旧版本留在服务器上）；
// 删除=从市场下架（二次确认后调 /api/market/unpublish，服务端还会再核一次归属）。
function mkPublishAction(label, e, action) {
  const btn = document.createElement('button');
  btn.className = 'mk-act' + (action === 'unpublish' ? ' ghost' : '');
  btn.textContent = label;
  btn.onclick = async () => {
    if (action === 'republish') { mkuOpenForEdit(e); return; }
    const ok = await confirmCenter('从市场删除「' + (e.name || e.id) + '」v' + (e.version || '?') +
      '？服务器上的包会被移除，其他同事将无法再安装。');
    if (!ok) return;
    btn.disabled = true;
    btn.textContent = '删除中…';
    try {
      await api('/api/market/unpublish', { method: 'POST', body: JSON.stringify({ type: mkTab, id: e.id, version: e.version }) });
      mkData = null;
      await loadMarket(true);
    } catch (err) {
      alert('删除失败：' + err.message);
      btn.disabled = false;
      btn.textContent = label;
    }
  };
  return btn;
}

/* ---------- 绑定（一次） ---------- */

let mkBound = false;
function bindMarket() {
  if (mkBound) return;
  mkBound = true;
  const btn = $('btn-market');
  if (btn) btn.onclick = toggleMarket;
  const search = $('mk-search');
  if (search) {
    search.oninput = () => { if (mkData && mkData.configured && !mkData.error) renderTab(); };
  }
  const refresh = $('mk-refresh');
  if (refresh) refresh.onclick = () => loadMarket(true);
  document.querySelectorAll('.mk-tab').forEach((t) => {
    t.onclick = () => {
      document.querySelectorAll('.mk-tab').forEach((x) => x.classList.toggle('active', x === t));
      mkTab = t.dataset.mkTab || 'skill';
      renderMarket();
    };
  });
  const ext = $('mk-ext-market');
  if (ext) ext.onclick = () => { if (typeof openSettings === 'function') openSettings('ccplugins'); };
  const uploadBtn = $('mk-upload-btn');
  if (uploadBtn) uploadBtn.onclick = mkuShow;
  mkuBind(); // 上传技能弹层（拖拽 / 选取 / Ctrl+V）
  // Esc 关闭市场视图（有任何弹窗开着时不抢键：确认框/设置自己处理 Esc）
  document.addEventListener('keydown', (ev) => {
    if (ev.key !== 'Escape' || !marketOpen()) return;
    const anyModalOpen = [...document.querySelectorAll('.modal')].some((m) => !m.classList.contains('hidden'));
    if (!anyModalOpen) closeMarket();
  });
}

/* ---------- 上传技能到市场（市场 · 技能 tab 的「＋ 上传技能」） ----------
   把一个技能包（.zip / 含 SKILL.md 的文件夹 / 单个 SKILL.md）发布到市场服务器。
   三种投喂方式：拖拽（文件或整个文件夹）、点击选取（文件 / 文件夹输入框）、
   当前窗口 Ctrl+V 粘贴。前端只做整包校验与元信息预填，不装到本机。

   提交契约（后端 POST /api/market/publish 尚未实现，前端已按此装配 FormData）：
     multipart/form-data
       files  技能包内容：单个 .zip 原样上传；否则每个文件一项，filename 用包内相对路径
       meta   JSON 字符串 {id, name, version, description, tags:[], zip:bool, skillMd:''}
     成功回 {ok:true, id, version}；失败非 2xx + {error} */

let mkuFiles = [];        // [{file, path}]：包内相对路径（已剥掉公共顶层目录）
let mkuZip = null;        // 单个 .zip 投喂时：{file, buf, entries}（entries=中央目录条目，只读不解压）
let mkuTopDir = '';       // 包的顶层目录名（推默认技能 ID 用）
let mkuSkillPath = '';    // 包内 SKILL.md 的相对路径（校验通过的标志）
let mkuBusy = false;      // 上传在途
let mkuErr = '';          // 提交失败的提示（优先于「就绪」，重新投喂/清空时清掉）
let mkuEditOf = null;     // 「更新发布」时正在编辑的市场条目（{id,version}）；普通上传为 null
let mkuManifest = null;   // 包内 manifest.json（技能自带元信息，可空）

function mkuOpen() {
  const m = $('mk-upload-modal');
  return !!m && !m.classList.contains('hidden');
}

function mkuShow() {
  if (mkuOpen()) return;
  mkuReset();
  $('mk-upload-modal').classList.remove('hidden');
}

function mkuClose() {
  if (!mkuOpen() || mkuBusy) return; // 上传中不关：XHR 停了状态就丢了
  $('mk-upload-modal').classList.add('hidden');
}

// mkuOpenForEdit 「更新发布」入口：打开上传弹层并预填该市场条目的元信息
// （ID / 名称 / 描述 / 标签），等用户投喂新包。**版本留空**——更新就是发一个新
// 版本（同 id、新 version，旧版本仍留在服务器上）。预填是直接赋值，而
// mkuAnalyze 的 fill() 只填空字段，所以用户看到的就是这条条目的既有元信息，
// 只在他没填的字段上被新包里的元信息补全（行为与"新发布但手填了元信息"一致）。
function mkuOpenForEdit(e) {
  mkuShow();
  mkuReset();
  mkuEditOf = { id: e.id, version: e.version };
  $('mku-id').value = e.id || '';
  $('mku-name').value = e.name || '';
  $('mku-desc').value = e.description || '';
  $('mku-tags').value = (e.tags || []).join(', ');
  $('mku-version').value = '';
  mkuRender();
}

function mkuReset() {
  mkuFiles = [];
  mkuZip = null;
  mkuTopDir = '';
  mkuSkillPath = '';
  mkuErr = '';
  mkuManifest = null;
  ['mku-id', 'mku-name', 'mku-version', 'mku-desc', 'mku-tags'].forEach((id) => { const el = $(id); if (el) el.value = ''; });
  ['mku-file-input', 'mku-dir-input'].forEach((id) => { const el = $(id); if (el) el.value = ''; });
  mkuEditOf = null;
  mkuSetStatus('');
  mkuRender();
}

function mkuSetStatus(msg, kind) {
  const el = $('mku-status');
  if (!el) return;
  el.className = 'mku-status' + (kind ? ' ' + kind : '');
  el.textContent = msg || '';
}

function mkuSize(n) {
  if (!n) return '0 B';
  if (n < 1024) return n + ' B';
  if (n < 1048576) return (n / 1024).toFixed(1) + ' KB';
  return (n / 1048576).toFixed(1) + ' MB';
}

// mkuSlug 收拾成市场接受的技能 ID：只留小写字母/数字/中划线。
// 市场端对中文名会报「推导不出可用 id」，所以推不出时返回空串，
// 让调用方接着试下一个候选，或请用户手填。
// mkuVersionFromName 从包名/目录名取版本号：出现 v 时，v 后面一定是版本号
// （微信操作_技能包_v1.3.1_20261009.zip → 1.3.1）；版本后跟的 +时间戳 / _时间戳
// 不是版本本体，只取 x.x.x。（后端 market_publish.go 的 skillVersionFromName 同规则）
function mkuVersionFromName(name) {
  const s = String(name || '').trim().replace(/[\\/]+$/, '');
  const base = s.slice(s.lastIndexOf('/') + 1).replace(/\.zip$/i, '');
  let m = /[vV](\d+(?:\.\d+){0,3})/.exec(base);
  if (m) return m[1];
  m = /(?:^|[^0-9.])(\d+\.\d+(?:\.\d+){0,2})(?:[^0-9.]|$)/.exec(base);
  return m ? m[1] : '';
}

// mkuStripVersion 去掉名字里的版本与时间戳尾巴（推导技能 id 用）
function mkuStripVersion(name) {
  let s = String(name || '').trim().replace(/[\\/]+$/, '');
  s = s.slice(s.lastIndexOf('/') + 1).replace(/\.zip$/i, '');
  s = s.replace(/[._-]?[vV]?\d+(?:\.\d+){1,3}(?:[+_-]?\d{6,8})?$/, '');
  s = s.replace(/[._-]?\d{8}$/, '');
  return s.replace(/^[\s_-]+|[\s_-]+$/g, '');
}

function mkuSlug(s) {
  const t = String(s || '').trim().replace(/\.[A-Za-z0-9]+$/, '')
    .toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
  return /^[a-z0-9][a-z0-9-]*$/.test(t) ? t : '';
}

/* ---- 文件收集 ---- */

function mkuAdd(items) {
  const seen = new Set(mkuFiles.map((x) => x.path + '|' + x.file.size));
  for (const it of items || []) {
    if (!it || !it.file) continue;
    const path = String(it.path || it.file.name || '').replace(/\\/g, '/').replace(/^\/+/, '');
    if (!path) continue;
    const key = path + '|' + it.file.size;
    if (seen.has(key)) continue;
    seen.add(key);
    mkuFiles.push({ file: it.file, path });
  }
}

// mkuNormalize 剥掉整包最外层目录（拖/选文件夹时所有路径都带顶层目录名），
// 让包内相对路径以 SKILL.md 所在层为根，后端拿到的就是干净的包结构。
function mkuNormalize() {
  if (!mkuFiles.length) return; // 单个文件也照剥（只含 SKILL.md 的技能包同样要归一到根）
  if (!mkuFiles.every((x) => x.path.indexOf('/') > 0)) return;
  const tops = new Set(mkuFiles.map((x) => x.path.split('/')[0]));
  if (tops.size !== 1) return;
  mkuFiles = mkuFiles.map((x) => ({ file: x.file, path: x.path.slice(x.path.indexOf('/') + 1) }));
}

// mkuCommonTop 这批量文件共同的顶层目录名（没有或不唯一时返回空）
function mkuCommonTop(items) {
  const tops = new Set();
  for (const it of items || []) {
    const p = String(it.path || (it.file && it.file.name) || '').split('/');
    if (p.length < 2) return '';
    tops.add(p[0]);
  }
  return tops.size === 1 ? [...tops][0] : '';
}

// mkuEntryFiles 递归读一个拖入项（文件夹会读全部子文件，含子目录）。
function mkuEntryFiles(entry, prefix) {
  return new Promise((resolve) => {
    if (!entry) return resolve([]);
    if (entry.isFile) {
      entry.file((f) => resolve([{ file: f, path: (prefix || '') + entry.name }]), () => resolve([]));
      return;
    }
    if (!entry.isDirectory) return resolve([]);
    const reader = entry.createReader();
    const out = [];
    const step = () => {
      reader.readEntries(async (batch) => {
        if (!batch.length) return resolve(out);
        for (const en of batch) out.push(...await mkuEntryFiles(en, (prefix || '') + entry.name + '/'));
        step();
      }, () => resolve(out));
    };
    step();
  });
}

// mkuFeed 投喂入口：items=[{file, path}]。单个 .zip 视为整包（原样上传）；
// 其余文件合并进当前包（另投一个顶层目录则视为换包，清空重来）。
async function mkuFeed(items) {
  const incoming = (items || []).filter((x) => x && x.file);
  if (!incoming.length || mkuBusy) return;
  mkuErr = ''; // 重新投喂即清掉上一次的提交失败提示
  if (incoming.length === 1 && /\.zip$/i.test(incoming[0].file.name || '')) {
    const f = incoming[0].file;
    mkuFiles = [{ file: f, path: f.name }];
    mkuTopDir = ''; // 交给 mkuAnalyze 按包内顶层目录推断（推不出再退回 zip 文件名）
    mkuZip = { file: f, buf: null, entries: [], err: '' };
    try {
      const buf = new Uint8Array(await f.arrayBuffer());
      mkuZip.buf = buf;
      mkuZip.entries = mkuZipEntries(buf);
    } catch (e) {
      mkuZip.err = e.message || String(e);
    }
  } else {
    if (mkuZip) { mkuFiles = []; mkuZip = null; mkuTopDir = ''; }  // 换包：从 zip 切回散文件
    const top = mkuCommonTop(incoming);
    if (mkuFiles.length && top && mkuTopDir && top !== mkuTopDir) { mkuFiles = []; mkuTopDir = ''; }
    if (!mkuFiles.length && top) mkuTopDir = top;
    mkuAdd(incoming);
    mkuNormalize();
  }
  await mkuAnalyze();
  mkuRender();
}

/* ---- 技能包校验与元信息预填 ---- */

// mkuZipEntries 只读 zip 的中央目录（不解压数据），拿包内文件名与偏移。
function mkuZipEntries(buf) {
  const dv = new DataView(buf.buffer, buf.byteOffset, buf.byteLength);
  let eocd = -1;
  const min = Math.max(0, buf.length - 22 - 65535);
  for (let i = buf.length - 22; i >= min; i--) {
    if (dv.getUint32(i, true) === 0x06054b50) { eocd = i; break; }
  }
  if (eocd < 0) throw new Error('不是有效的 zip 文件（找不到中央目录）');
  const count = dv.getUint16(eocd + 10, true);
  const cdoff = dv.getUint32(eocd + 16, true);
  if (count === 0xffff || cdoff === 0xffffffff) throw new Error('暂不支持 zip64 打包的技能包');
  const dec = new TextDecoder('utf-8');
  const out = [];
  let p = cdoff;
  for (let i = 0; i < count; i++) {
    if (p + 46 > buf.length || dv.getUint32(p, true) !== 0x02014b50) throw new Error('zip 中央目录损坏');
    const method = dv.getUint16(p + 10, true);
    const csize = dv.getUint32(p + 20, true);
    const size = dv.getUint32(p + 24, true);
    const nlen = dv.getUint16(p + 28, true);
    const elen = dv.getUint16(p + 30, true);
    const clen = dv.getUint16(p + 32, true);
    const off = dv.getUint32(p + 42, true);
    // zip 规范要求 '/' 分隔，但 Windows 打包工具常写成 '\'：统一成正斜杠再判断/展示
    const name = dec.decode(buf.subarray(p + 46, p + 46 + nlen)).replace(/\\/g, '/');
    if (name && !name.endsWith('/')) out.push({ name, method, csize, size, off });
    p += 46 + nlen + elen + clen;
  }
  return out;
}

// mkuZipReadText 解压 zip 里的一个条目为文本（只为读 SKILL.md 的 frontmatter）
async function mkuZipReadText(buf, e) {
  const dv = new DataView(buf.buffer, buf.byteOffset, buf.byteLength);
  if (dv.getUint32(e.off, true) !== 0x04034b50) throw new Error('zip 本地头损坏');
  const nlen = dv.getUint16(e.off + 26, true);
  const elen = dv.getUint16(e.off + 28, true);
  const start = e.off + 30 + nlen + elen;
  const raw = buf.subarray(start, start + (e.csize || e.size));
  if (e.method === 0) return new TextDecoder('utf-8').decode(raw);
  if (e.method !== 8) throw new Error('不支持的压缩方式');
  if (typeof DecompressionStream !== 'function') throw new Error('当前浏览器不支持解压 zip');
  const st = new Blob([raw]).stream().pipeThrough(new DecompressionStream('deflate-raw'));
  return new TextDecoder('utf-8').decode(new Uint8Array(await new Response(st).arrayBuffer()));
}

// mkuFrontmatter 只认 SKILL.md 顶部 --- 之间的简单键值（name/description/version/tags）
function mkuFrontmatter(text) {
  const m = /^---\r?\n([\s\S]*?)\r?\n---/.exec(text || '');
  if (!m) return {};
  const out = {};
  for (const line of m[1].split(/\r?\n/)) {
    const kv = /^([A-Za-z_][\w-]*)\s*:\s*(.*)$/.exec(line);
    if (!kv) continue;
    let v = kv[2].trim().replace(/^["']|["']$/g, '');
    if (kv[1].toLowerCase() === 'tags') {
      v = v.replace(/^\[|\]$/g, '').split(/[,，]/).map((s) => s.trim()).filter(Boolean);
    }
    out[kv[1].toLowerCase()] = v;
  }
  return out;
}

// mkuAnalyze 找 SKILL.md（连带同目录的 manifest.json）→ 预填元信息（只填空字段）。
// 取舍与市场对齐：装好后技能列表显示的是 SKILL.md 的 name/description，所以描述
// 优先 SKILL.md；名称优先包内 manifest.json 的友好名；版本号优先文件名（v 后那串）。
async function mkuAnalyze() {
  mkuSkillPath = '';
  mkuManifest = null;
  let text = '';
  let manifest = null;
  if (mkuZip) {
    const names = (mkuZip.entries || []).map((x) => x.name);
    mkuSkillPath = names.find((n) => /(^|\/)SKILL\.md$/i.test(n)) || '';
    if (!mkuTopDir) {
      const tops = new Set(names.map((n) => n.split('/')[0]));
      mkuTopDir = (tops.size === 1 ? [...tops][0] : '') || (mkuZip.file.name || '').replace(/\.zip$/i, '');
    }
    const base = mkuSkillPath.indexOf('/') > 0 ? mkuSkillPath.slice(0, mkuSkillPath.lastIndexOf('/') + 1) : '';
    const entryOf = (want) => (mkuZip.entries || []).find((x) => x.name.toLowerCase() === String(want).toLowerCase());
    if (mkuSkillPath && mkuZip.buf) {
      try { text = await mkuZipReadText(mkuZip.buf, entryOf(mkuSkillPath)); } catch (e) { /* 读不出就不预填，不影响校验 */ }
    }
    const me = mkuZip.buf ? entryOf(base + 'manifest.json') : null;
    if (me) { try { manifest = JSON.parse(await mkuZipReadText(mkuZip.buf, me)); } catch (e) { /* manifest 坏了不影响发布 */ } }
  } else {
    const hit = mkuFiles.find((x) => /(^|\/)SKILL\.md$/i.test(x.path));
    if (hit) {
      mkuSkillPath = hit.path;
      const base = hit.path.indexOf('/') > 0 ? hit.path.slice(0, hit.path.lastIndexOf('/') + 1) : '';
      try { text = await hit.file.text(); } catch (e) { /* 同上 */ }
      const mf = mkuFiles.find((x) => x.path.toLowerCase() === (base + 'manifest.json').toLowerCase());
      if (mf) { try { manifest = JSON.parse(await mf.file.text()); } catch (e) { /* 同上 */ } }
    }
  }
  mkuManifest = manifest;
  const fm = mkuFrontmatter(text);
  const pkgName = mkuZip ? mkuZip.file.name : (mkuTopDir || '');
  const fill = (id, v) => { const el = $(id); if (el && v && !el.value) el.value = v; };
  // 技能 ID：包内 manifest.json 的 id → 包内顶层目录名 → SKILL.md 的 name → 包名
  const skillDirName = mkuSkillPath.indexOf('/') > 0 ? mkuSkillPath.slice(0, mkuSkillPath.lastIndexOf('/')).split('/').pop() : '';
  // ID 候选逐个试：市场只收纯小写字母/数字/中划线（中文会被服务端清洗掉）
  fill('mku-id', [manifest && manifest.id, skillDirName, fm.name, mkuStripVersion(mkuTopDir || pkgName)]
    .map((x) => mkuSlug(x)).find(Boolean) || '');
  // 版本号：文件名/目录名里 v 后面那串 → manifest.json → SKILL.md
  const ver = mkuVersionFromName(pkgName) || (manifest && manifest.version) || fm.version || '';
  fill('mku-version', ver ? String(ver) : '');
  fill('mku-name', (manifest && manifest.name) || fm.name);
  fill('mku-desc', fm.description || (manifest && manifest.description));
  fill('mku-tags', Array.isArray(fm.tags) ? fm.tags.join(', ') : '');
}

/* ---- 渲染 ---- */

function mkuRender() {
  const box = $('mku-files');
  const list = mkuZip ? (mkuZip.entries || []).map((x) => ({ path: x.name, size: x.size }))
    : mkuFiles.map((x) => ({ path: x.path, size: x.file.size }));
  if (box) {
    if (!list.length) {
      box.classList.add('hidden');
      box.innerHTML = '';
    } else {
      box.classList.remove('hidden');
      const total = list.reduce((a, x) => a + (x.size || 0), 0);
      const head = mkuZip
        ? '<div class="mku-sum"><span>技能包 <b>' + esc(mkuZip.file.name) + '</b>：包内 ' + list.length + ' 个文件（' + mkuSize(total) + '）</span></div>'
        : '<div class="mku-sum"><span>已收集 ' + list.length + ' 个文件（' + mkuSize(total) + '）</span></div>';
      const rows = mkuZip ? '' : list.slice(0, 12).map((x) =>
        '<div class="mku-file"><span class="mku-file-name">' +
        esc(x.path).replace(/(^|\/)(SKILL\.md)$/i, '$1<b>$2</b>') +
        '</span><span class="mku-file-size">' + mkuSize(x.size) + '</span></div>').join('') +
        (list.length > 12 ? '<div class="mku-file"><span class="mku-file-name">…还有 ' + (list.length - 12) + ' 个文件</span></div>' : '');
      box.innerHTML = head + rows;
    }
  }
  // 状态行与提交闸门：必须有文件、有 SKILL.md、有技能 ID
  const id = ($('mku-id').value || '').trim();
  const meName = (mkData && mkData.me && mkData.me.name) || ''; // 本机发布者姓名（未设置时空）
  const allPaths = list.map((x) => String(x.path));
  const skillHits = allPaths.filter((p) => /(^|\/)SKILL\.md$/i.test(p)); // 递归算，嵌套包也算得对
  const skillDir = mkuSkillPath.indexOf('/') > 0 ? mkuSkillPath.slice(0, mkuSkillPath.lastIndexOf('/') + 1) : '';
  const stray = skillDir ? allPaths.filter((p) => !p.startsWith(skillDir)) : []; // 技能目录外的散文件（后端会并进技能目录）
  const idOk = /^[a-z0-9][a-z0-9-]*$/.test(id); // 市场端只收纯小写字母/数字/中划线
  let msg = '';
  let kind = '';
  if (!list.length) {
    msg = '还没有技能包：拖进来、点按钮选，或直接按 Ctrl+V 粘贴。';
  } else if (mkuZip && mkuZip.err) {
    msg = '压缩包读取失败：' + mkuZip.err;
    kind = 'err';
  } else if (!mkuSkillPath) {
    msg = '没找到 SKILL.md：技能包里必须有一个 SKILL.md（技能说明与使用方式），否则市场无法识别。';
    kind = 'warn';
  } else if (skillHits.length > 1) {
    msg = '包里有 ' + skillHits.length + ' 处 SKILL.md（' + skillHits.slice(0, 4).join('、') + '）：一个包只能装一个技能，请分开上传。';
    kind = 'err';
  } else if (id && !idOk) {
    msg = '技能 ID 只能用纯小写字母/数字/中划线（市场端规则）：像 wechat-chat-ops 这样，不能带中文或空格。';
    kind = 'warn';
  } else {
    const verText = ($('mku-version').value || '').trim();
    msg = '技能包就绪：SKILL.md 在 ' + mkuSkillPath + '，共 ' + list.length + ' 个文件（' +
      mkuSize(list.reduce((a, x) => a + (x.size || 0), 0)) + '）；将发布 ' + (id || '(待填 ID)') + (verText ? ' v' + verText : '') +
      (stray.length ? '（技能目录外还有 ' + stray.length + ' 个文件：' + stray.slice(0, 3).join('、') + '，发布时会并入技能目录）' : '');
    kind = 'ok';
    if (!meName) msg += '（未设置发布者姓名：发布后不能在这里更新/删除，去 设置 · 网络访问 填一个）';
  }
  if (mkuBusy) { msg = '正在上传…'; kind = ''; }
  else if (mkuErr) { msg = mkuErr; kind = 'err'; } // 提交失败的提示盖过「就绪」
  else if (mkuEditOf) { msg = '【更新发布】' + mkuEditOf.id + ' v' + (mkuEditOf.version || '?') + ' → ' + msg; }
  mkuSetStatus(msg, kind);
  const sub = $('mku-submit');
  if (sub) sub.disabled = mkuBusy || !list.length || !mkuSkillPath || !id || !idOk || !!(mkuZip && mkuZip.err) || skillHits.length > 1;
}

/* ---- 投喂事件的绑定（bindMarket 里调一次） ---- */

function mkuBind() {
  const modal = $('mk-upload-modal');
  if (!modal) return;
  const drop = $('mku-drop');
  let depth = 0; // dragenter/dragleave 会在子元素上反复触发，深度计数防闪
  const hasFiles = (e) => !!e.dataTransfer && [...(e.dataTransfer.types || [])].includes('Files');
  const clearHot = () => { depth = 0; drop.classList.remove('over'); };
  drop.addEventListener('dragenter', (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    depth++;
    drop.classList.add('over');
  });
  drop.addEventListener('dragover', (e) => {
    if (!hasFiles(e)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'copy';
  });
  drop.addEventListener('dragleave', () => {
    depth = Math.max(0, depth - 1);
    if (!depth) drop.classList.remove('over');
  });
  drop.addEventListener('drop', (e) => {
    e.preventDefault();
    clearHot();
    const dt = e.dataTransfer;
    const entries = [];
    if (dt && dt.items && dt.items.length) {
      for (const it of dt.items) {
        if (it.kind !== 'file') continue;
        const en = it.webkitGetAsEntry ? it.webkitGetAsEntry() : null;
        if (en) entries.push(en);
      }
    }
    if (entries.length) {
      // entries 必须在事件同步阶段取出来，await 之后 DataTransfer 就失效了
      (async () => {
        const all = [];
        for (const en of entries) all.push(...await mkuEntryFiles(en, ''));
        await mkuFeed(all);
      })();
      return;
    }
    const files = dt && dt.files ? [...dt.files] : [];
    mkuFeed(files.map((f) => ({ file: f, path: f.name })));
  });
  $('mku-pick-file').onclick = () => $('mku-file-input').click();
  $('mku-pick-dir').onclick = () => {
    // 手机端浏览器没有「选文件夹」这回事（webkitdirectory 无效），按项目惯例直接说明
    if (isNarrow()) { alert('手机端不支持选择技能文件夹：请改用 zip 技能包，或到电脑端上传。'); return; }
    $('mku-dir-input').click();
  };
  $('mku-file-input').onchange = (e) => {
    const fs = [...(e.target.files || [])];
    e.target.value = '';   // 清掉才能再选同一批文件
    mkuFeed(fs.map((f) => ({ file: f, path: f.name })));
  };
  $('mku-dir-input').onchange = (e) => {
    const fs = [...(e.target.files || [])];
    e.target.value = '';
    mkuFeed(fs.map((f) => ({ file: f, path: f.webkitRelativePath || f.name })));
  };
  $('mku-id').oninput = () => mkuRender();
  $('mku-clear').onclick = () => mkuReset();
  $('mku-cancel').onclick = () => mkuClose();
  $('mku-submit').onclick = mkuSubmit;
  modal.addEventListener('click', (e) => { if (e.target === modal) mkuClose(); }); // 点遮罩=取消
  // Ctrl+V：弹层打开时把剪贴板里的文件收进技能包（纯文本粘贴不拦，留给输入框）
  document.addEventListener('paste', (e) => {
    if (!mkuOpen() || e.defaultPrevented || mkuBusy) return;
    const cd = e.clipboardData;
    if (!cd) return;
    const out = [];
    for (const it of (cd.items || [])) {
      if (it.kind !== 'file') continue;
      const f = it.getAsFile && it.getAsFile();
      if (f) out.push({ file: f, path: f.name || ('粘贴文件-' + Date.now()) });
    }
    if (!out.length && cd.files) {
      for (const f of cd.files) out.push({ file: f, path: f.name });
    }
    if (!out.length) return;
    e.preventDefault();
    mkuFeed(out);
  });
  document.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape' || !mkuOpen()) return;
    const conf = $('confirm-modal');
    if (conf && !conf.classList.contains('hidden')) return; // 确认框在上层，先关它
    e.stopPropagation();
    mkuClose();
  });
}

/* ---- 提交：装配 FormData → POST /api/market/publish ---- */

function mkuPost(path, form, onProgress) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('POST', path);
    xhr.upload.onprogress = (e) => { if (onProgress && e.lengthComputable) onProgress(e.loaded, e.total); };
    xhr.onload = () => {
      let data = {};
      try { data = JSON.parse(xhr.responseText || '{}'); } catch (_) { /* 非 JSON：只看状态码 */ }
      if (xhr.status >= 200 && xhr.status < 300) resolve(data);
      else {
        const err = new Error(data.error || ('HTTP ' + xhr.status));
        err.status = xhr.status;
        reject(err);
      }
    };
    xhr.onerror = () => reject(new Error('网络错误（连接中断或服务未响应）'));
    xhr.send(form);
  });
}

async function mkuSubmit() {
  if (mkuBusy) return;
  const id = ($('mku-id').value || '').trim();
  if (!id) { mkuSetStatus('请填技能 ID（英文短名，市场里唯一）', 'warn'); return; }
  const meta = {
    id,
    name: ($('mku-name').value || '').trim() || id,
    version: ($('mku-version').value || '').trim(),
    description: ($('mku-desc').value || '').trim(),
    tags: ($('mku-tags').value || '').split(/[,，]/).map((s) => s.trim()).filter(Boolean),
    zip: !!mkuZip,
    skillMd: mkuSkillPath,
  };
  const form = new FormData();
  form.append('meta', JSON.stringify(meta));
  if (mkuZip) {
    form.append('files', mkuZip.file, mkuZip.file.name);
  } else {
    // multipart 的 filename 只保留 basename（RFC 7578），包内相对路径得另用 paths 给后端
    form.append('paths', JSON.stringify(mkuFiles.map((x) => x.path)));
    for (const x of mkuFiles) form.append('files', x.file, x.path);
  }

  mkuErr = '';
  mkuBusy = true;
  const btn = $('mku-submit');
  btn.disabled = true;
  btn.textContent = '上传中…';
  mkuRender();
  try {
    const r = await mkuPost('/api/market/publish', form, (loaded, total) => {
      btn.textContent = '上传中… ' + Math.round((loaded / total) * 100) + '%';
    });
    mkuBusy = false;
    btn.textContent = '发布到市场';
    alert('已发布到市场：' + (r.id || id) + (r.version ? ' v' + r.version : '') +
      (r.size ? '（' + mkuSize(r.size) + '，sha256 ' + String(r.sha256 || '').slice(0, 12) + '…）' : ''));
    $('mk-upload-modal').classList.add('hidden');
    mkData = null;
    await loadMarket(true);
  } catch (e) {
    mkuBusy = false;
    btn.textContent = '发布到市场';
    const hint = (e.status === 404 || e.status === 405)
      ? '\n（市场服务器上没有发布接口 /admin/api/publish：确认 设置 · 网络访问 · 市场服务器 指向的是 jxai-server）'
      : '';
    mkuErr = '发布失败：' + e.message + hint;
    mkuRender();
  }
}

document.addEventListener('DOMContentLoaded', bindMarket);
