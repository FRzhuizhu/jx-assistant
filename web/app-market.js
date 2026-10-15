/* 市场前端（market.go 的浏览器半边）：左侧栏底部「市场」按钮进入的主区整页视图。
   技能 / 组件 / 插件 三个 tab + 搜索 + 一键安装/更新/卸载：
     · 全部操作走 /api/market（挂在 /api/ 下，远程手机端经中继隧道天然可用）；
     · 组件装/卸后调 CX.refresh() 热加载/热卸载，不刷新页面；
     · 插件 tab 底部保留「管理外部 Claude 市场」入口（跳设置页统一「插件」页）；
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
  if (tab === 'mcp') return mkData.mcps || [];
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
      mcp: '市场里还没有 MCP 连接器。管理员在服务端管理页（/admin）或 CLI 发布 mcp.json 即可（也支持直接上传 .json 配置文件）。',
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

  const typeLabel = { skill: '技能', component: '组件', plugin: '插件', mcp: 'MCP 连接器' }[mkTab];
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
// 更新=同 id 发新版本（复用上传弹层、预填该条目元信息；包体取自本机已装技能）；
// 删除=下架（进服务端回收站，管理员可恢复；归属校验在服务端做）。
function mkPublishAction(label, e, action) {
  const btn = document.createElement('button');
  btn.className = 'mk-act' + (action === 'unpublish' ? ' ghost' : '');
  btn.textContent = label;
  btn.onclick = async () => {
    if (action === 'republish') { mkuOpenForEdit(e); return; }
    const ok = await confirmCenter('从市场下架「' + (e.name || e.id) + '」v' + (e.version || '?') +
      '？条目会进服务端回收站（管理员可恢复）；其他同事将无法再安装，已安装的不受影响。');
    if (!ok) return;
    btn.disabled = true;
    btn.textContent = '下架中…';
    try {
      await api('/api/market/unpublish', { method: 'POST', body: JSON.stringify({ type: mkTab, id: e.id, version: e.version }) });
      mkData = null;
      await loadMarket(true);
    } catch (err) {
      alert('下架失败：' + err.message);
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
  if (ext) ext.onclick = () => { if (typeof openSettings === 'function') openSettings('plugins'); };
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
   先装后传：从已装的用户技能（GET /api/skills）里选一个 → 后端从安装目录
   现场打包、走发布校验链（SKILL.md 内容体检等）→ 转发市场服务器。身份=
   本机发布者姓名（X-Jxai-User 头），归属记录与「只能动自己发的」判权都在
   服务端；下架是进服务端回收站，不是物理删除。

   元信息预填取自 GET /api/market/publish-meta?skill=<id>（后端单一事实来源：
   五字段回退链与中文→英文 ID 词表都只在 Go 一份，前端不再持有词表副本）。
   提交契约：POST /api/market/publish JSON {skillId, id, name, version,
   description, tags}，成功回 {ok:1, id, version, sha256, size}；失败非 2xx
   + {error}。 */

let mkuSkills = [];       // 已装用户技能清单（GET /api/skills 的 user）
let mkuSel = '';          // 选中的技能 id
let mkuSuggested = false; // 技能 ID 是后端词表建议的（不是包内自带或用户手填的）
let mkuBusy = false;      // 发布在途
let mkuErr = '';          // 提交失败的提示（优先于「就绪」）
let mkuEditOf = null;     // 「更新发布」时正在编辑的市场条目（{id,version}）；普通上传为 null

function mkuOpen() {
  const m = $('mk-upload-modal');
  return !!m && !m.classList.contains('hidden');
}

async function mkuShow() {
  if (mkuOpen()) return;
  mkuReset();
  $('mk-upload-modal').classList.remove('hidden');
  await mkuLoadSkills();
}

function mkuClose() {
  if (!mkuOpen() || mkuBusy) return; // 发布中不关
  $('mk-upload-modal').classList.add('hidden');
}

// mkuOpenForEdit 「更新发布」入口：打开弹层并预填该市场条目的元信息（ID /
// 显示名 / 描述 / 标签），**版本留空**——更新就是发一个新版本（同 id、新
// version）。仍需选中本机已装的技能作为包体来源；没装就装一份再回来。
async function mkuOpenForEdit(e) {
  await mkuShow();
  mkuEditOf = { id: e.id, version: e.version };
  $('mku-id').value = e.id || '';
  $('mku-name').value = e.name || '';
  $('mku-desc').value = e.description || '';
  $('mku-tags').value = (e.tags || []).join(', ');
  $('mku-version').value = '';
  mkuRender();
}

function mkuClearForm() {
  mkuSel = '';
  mkuSuggested = false;
  mkuErr = '';
  mkuEditOf = null;
  ['mku-id', 'mku-name', 'mku-version', 'mku-desc', 'mku-tags'].forEach((id) => { const el = $(id); if (el) el.value = ''; });
  mkuRenderSkills();
  mkuRender();
}

function mkuReset() {
  mkuSkills = [];
  mkuClearForm();
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

/* ---- 已装技能清单与选择 ---- */

async function mkuLoadSkills() {
  const box = $('mku-skills');
  if (!box) return;
  box.innerHTML = '<div class="mku-skill-empty">正在读取已装技能…</div>';
  try {
    const d = await api('/api/skills');
    mkuSkills = d.user || [];
  } catch (e) {
    box.innerHTML = '<div class="mku-skill-empty">已装技能读取失败：' + esc(e.message) + '</div>';
    return;
  }
  mkuRenderSkills();
}

function mkuRenderSkills() {
  const box = $('mku-skills');
  if (!box) return;
  if (!mkuSkills.length) {
    box.innerHTML = '<div class="mku-skill-empty">本机还没有安装用户技能。<br>' +
      '先去 设置 · 技能 安装（支持拖入 zip、.md 文件或技能文件夹），装好、试过能用再回来发布。</div>';
    return;
  }
  box.innerHTML = '';
  for (const s of mkuSkills) {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'mku-skill' + (s.id === mkuSel ? ' sel' : '');
    const disp = s.displayName || s.name || s.id;
    btn.innerHTML =
      '<div class="mku-skill-line"><b>' + esc(disp) + '</b>' +
      (s.version ? '<span class="mku-skill-id">v' + esc(s.version) + '</span>' : '') +
      '<span class="mku-skill-id">' + esc(s.id) + '</span></div>' +
      (s.description ? '<div class="mku-skill-desc">' + esc(s.description) + '</div>' : '');
    btn.onclick = () => mkuPick(s.id);
    box.appendChild(btn);
  }
}

// mkuPick 选中一个已装技能：拉后端推导的元信息预填表单（只填空字段——更新
// 发布时条目既有元信息不被新包内容覆盖）。
async function mkuPick(id) {
  if (mkuBusy) return;
  mkuSel = id;
  mkuErr = '';
  mkuSuggested = false;
  mkuRenderSkills();
  mkuSetStatus('正在推导元信息…');
  try {
    const meta = await api('/api/market/publish-meta?skill=' + encodeURIComponent(id));
    mkuSuggested = !!meta.idSuggested;
    const fill = (elId, v) => { const el = $(elId); if (el && v && !el.value) el.value = v; };
    fill('mku-id', meta.id);
    fill('mku-name', meta.name);
    fill('mku-version', meta.version);
    fill('mku-desc', meta.description);
    fill('mku-tags', (meta.tags || []).join(', '));
  } catch (e) {
    mkuErr = '元信息推导失败：' + e.message + '（仍可手填全部字段提交；后端会再校验一遍）';
  }
  mkuRender();
}

/* ---- 渲染与提交闸门 ---- */

function mkuRender() {
  const id = ($('mku-id').value || '').trim();
  const idOk = /^[a-z0-9][a-z0-9-]*$/.test(id); // 市场端只收纯小写字母/数字/中划线
  const name = ($('mku-name').value || '').trim();
  const desc = ($('mku-desc').value || '').trim();
  const ver = ($('mku-version').value || '').trim();
  const meName = (mkData && mkData.me && mkData.me.name) || '';
  let msg = '';
  let kind = '';
  if (!mkuSel) {
    msg = '先在上面选一个已安装的用户技能。';
  } else if (!id) {
    msg = '技能 ID 还没推导出来：请手填英文短名（如 wechat-chat-ops）。';
    kind = 'warn';
  } else if (!idOk) {
    msg = '技能 ID 只能用纯小写字母/数字/中划线（市场端规则）：像 wechat-chat-ops 这样，不能带中文或空格。';
    kind = 'warn';
  } else if ([...name].length > 64) {
    msg = '显示名称超过 64 字（行业规范上限），请缩短。';
    kind = 'warn';
  } else if ([...desc].length > 1024) {
    msg = '描述超过 1024 字（行业规范上限）：请精简，详细内容写进 SKILL.md 正文。';
    kind = 'warn';
  } else {
    msg = '将发布 ' + id + (ver ? ' v' + ver : '') + '：包体取自本机已装的「' + esc(mkuSel) + '」。';
    kind = 'ok';
    if (mkuSuggested) msg += '（ID 是按中文词表建议的，可改）';
    if (!($('mku-desc').value || '').trim()) msg += '（没写描述：市场卡片上会没有说明文字，建议补一行）';
    if (!meName) msg += '（未设置发布者姓名：市场服务器记不了归属，发布会失败——去 设置 · 网络访问 填一个）';
  }
  if (mkuBusy) { msg = '正在发布…'; kind = ''; }
  else if (mkuErr) { msg = mkuErr; kind = 'err'; } // 提交/推导失败的提示盖过「就绪」
  else if (mkuEditOf) { msg = '【更新发布】' + mkuEditOf.id + ' v' + (mkuEditOf.version || '?') + ' → ' + msg; }
  mkuSetStatus(msg, kind);
  const sub = $('mku-submit');
  if (sub) sub.disabled = mkuBusy || !mkuSel || !id || !idOk;
}

/* ---- 绑定（bindMarket 里调一次） ---- */

function mkuBind() {
  const modal = $('mk-upload-modal');
  if (!modal) return;
  ['mku-id', 'mku-name', 'mku-version', 'mku-desc', 'mku-tags'].forEach((id) => {
    const el = $(id);
    if (el) el.oninput = () => mkuRender();
  });
  $('mku-clear').onclick = mkuClearForm;
  $('mku-cancel').onclick = () => mkuClose();
  $('mku-submit').onclick = mkuSubmit;
  modal.addEventListener('click', (e) => { if (e.target === modal) mkuClose(); }); // 点遮罩=取消
  document.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape' || !mkuOpen()) return;
    const conf = $('confirm-modal');
    if (conf && !conf.classList.contains('hidden')) return; // 确认框在上层，先关它
    e.stopPropagation();
    mkuClose();
  });
}

/* ---- 提交：JSON → POST /api/market/publish（包由后端从安装目录打） ---- */

async function mkuSubmit() {
  if (mkuBusy || !mkuSel) return;
  const id = ($('mku-id').value || '').trim();
  if (!id) { mkuSetStatus('请填技能 ID（英文短名，市场里唯一）', 'warn'); return; }
  const body = {
    skillId: mkuSel,
    id,
    name: ($('mku-name').value || '').trim() || id,
    version: ($('mku-version').value || '').trim(),
    description: ($('mku-desc').value || '').trim(),
    tags: ($('mku-tags').value || '').split(/[,，]/).map((s) => s.trim()).filter(Boolean),
  };
  mkuErr = '';
  mkuBusy = true;
  const btn = $('mku-submit');
  btn.disabled = true;
  btn.textContent = '发布中…';
  mkuRender();
  try {
    const r = await api('/api/market/publish', { method: 'POST', body: JSON.stringify(body) });
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
    mkuErr = '发布失败：' + e.message;
    mkuRender();
  }
}

document.addEventListener('DOMContentLoaded', bindMarket);

/* ---------- 列数：最大化 4 列 / 窗口态 3 列（跨屏幕自适应，不依赖固定像素阈值） ---------- */

// 判定「窗口是否最大化」：最大化后窗口宽度会等于本屏可用宽（screen.availWidth）。
// 注意尺度：innerWidth/outerWidth 恒为 CSS 像素，而 screen.* 在部分宿主里报物理像素
// （本机 1536×864@125%：availWidth=1536 物理 vs 1229 CSS）——故同时按「原值」与
// 「除以 devicePixelRatio」两种尺度严格比对（容差 4px / 10px），任一命中即算最大化。
// 不用固定像素阈值（如 ≥1600px）：这类屏幕最大化也只有 ≈1229px 视口，阈值必然失准。
function updateMarketCols() {
  const iw = window.innerWidth;
  const ow = window.outerWidth || 0;
  const aw = window.screen.availWidth || 0;
  const dpr = window.devicePixelRatio || 1;
  const near = (a, b, tol) => b > 0 && Math.abs(a - b) <= tol;
  const maxed = near(iw, aw, 4) || near(ow, aw, 10) ||
    (dpr > 1 && (near(iw, aw / dpr, 4) || near(ow, aw / dpr, 10)));
  document.body.classList.toggle('mk-4col', iw >= 900 && maxed);
}
window.addEventListener('resize', updateMarketCols);
document.addEventListener('DOMContentLoaded', updateMarketCols);
