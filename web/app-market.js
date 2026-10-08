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
  // Esc 关闭市场视图（有任何弹窗开着时不抢键：确认框/设置自己处理 Esc）
  document.addEventListener('keydown', (ev) => {
    if (ev.key !== 'Escape' || !marketOpen()) return;
    const anyModalOpen = [...document.querySelectorAll('.modal')].some((m) => !m.classList.contains('hidden'));
    if (!anyModalOpen) closeMarket();
  });
}

document.addEventListener('DOMContentLoaded', bindMarket);
