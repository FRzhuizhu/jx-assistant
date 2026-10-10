/* Claude Code 插件兼容层前端（ccplugin*.go 的浏览器半边）：
   统一「插件」设置页的 Claude 分组行渲染（ccpPluginRow：开关/组件开关/
   配置/卸载）+ 市场浏览面板（renderCCMarketPanel）+ 安装与市场添加按钮
   （bindCCPluginPanel）+ 输入框斜杠命令弹层（/ 触发，列出插件命令，
   回车/点击补全）。清单与总开关走统一 /api/plugins（带 layer 来源），
   组件开关/配置/装卸仍走 /api/cc-plugins。复用 app-core.js 的 $ / api /
   esc；样式复用 plugin-row 体系 + style.css 尾部的 ccp-* 前缀类。 */

/* ---------- 统一插件页：Claude 行 + 市场面板 ---------- */

const CCP_COMPONENT_LABELS = {
  skills: '技能', commands: '命令', agents: '专家',
  hooks: 'Hooks', mods: 'Mods', mcp: 'MCP',
};

function ccpPluginRow(p) {
  const row = document.createElement('div');
  row.className = 'plugin-row';
  // 基本信息行 + 能力徽章
  const info = document.createElement('div');
  info.className = 'pr-info';
  const badges = [];
  if (p.skills) badges.push(`技能×${p.skills}`);
  if (p.commands) badges.push(`命令×${p.commands}`);
  if (p.agents) badges.push(`专家×${p.agents}`);
  if (p.hooks) badges.push('Hooks');
  if (p.mods) badges.push('Mods');
  if (p.mcp) badges.push('MCP');
  info.innerHTML =
    `<div class="pr-line"><b>${esc(p.displayName || p.name)}</b><span class="pr-ver">${esc(p.version || '')}</span>` +
    `<span class="pr-ver">@${esc(p.marketplace)}</span></div>` +
    (badges.length ? `<div class="ccp-badges">${badges.map(b => `<span class="ccp-badge">${esc(b)}</span>`).join('')}</div>` : '') +
    `<p>${esc(p.description || '')}</p>`;
  // 总开关（走统一 /api/plugins：与各体系后端逻辑一致）
  const tg = document.createElement('label');
  tg.className = 'pr-toggle';
  const cb = document.createElement('input');
  cb.type = 'checkbox';
  cb.checked = !!p.enabled;
  cb.onchange = async () => {
    cb.disabled = true;
    try {
      await api('/api/plugins', { method: 'POST', body: JSON.stringify({ layer: p.layer, id: p.id, enabled: cb.checked }) });
      note.textContent = '已更新，新会话生效';
      await renderPluginList();
    } catch (e) { alert('切换失败：' + e.message); cb.checked = !cb.checked; cb.disabled = false; }
  };
  tg.appendChild(cb);
  tg.appendChild(document.createElement('span'));
  row.appendChild(info);
  row.appendChild(tg);
  const note = document.createElement('div');
  note.className = 'pr-note';
  row.appendChild(note);
  // 组件级行：执行型组件（hooks/mods/mcp）单独放行
  const execRow = document.createElement('div');
  execRow.className = 'ccp-comps';
  for (const comp of ['hooks', 'mods', 'mcp']) {
    const has = p[comp];
    if (!has) continue;
    const cLabel = document.createElement('label');
    cLabel.className = 'ccp-comp-toggle';
    const ccb = document.createElement('input');
    ccb.type = 'checkbox';
    ccb.checked = !!(p.components && p.components[comp]);
    ccb.onchange = async () => {
      ccb.disabled = true;
      try {
        await api('/api/cc-plugins/toggle', { method: 'POST', body: JSON.stringify({ key: p.id, component: comp, enabled: ccb.checked }) });
        note.textContent = `${CCP_COMPONENT_LABELS[comp]} 组件已${ccb.checked ? '开启' : '关闭'}，新会话生效`;
        await renderPluginList();
      } catch (e) { alert('切换失败：' + e.message); ccb.checked = !ccb.checked; ccb.disabled = false; }
    };
    cLabel.appendChild(ccb);
    cLabel.appendChild(document.createTextNode((ccb.checked ? '✓ ' : '') + CCP_COMPONENT_LABELS[comp]));
    execRow.appendChild(cLabel);
  }
  if (execRow.childElementCount) {
    const tip = document.createElement('span');
    tip.className = 'ccp-comp-tip';
    tip.textContent = '执行型组件（命令/进程/联网），开启即信任';
    execRow.appendChild(tip);
    row.appendChild(execRow);
  }
  // userConfig 表单（有声明才显示）
  if (p.userConfig && Object.keys(p.userConfig).length) {
    const form = document.createElement('div');
    form.className = 'ccp-config';
    for (const [key, opt] of Object.entries(p.userConfig)) {
      const line = document.createElement('div');
      line.className = 'ccp-config-row';
      const lab = document.createElement('span');
      lab.textContent = opt.title || key;
      const input = document.createElement('input');
      input.type = opt.sensitive ? 'password' : 'text';
      input.value = (p.configValues && p.configValues[key] != null) ? p.configValues[key] : (opt.default != null ? opt.default : '');
      input.placeholder = opt.description || '';
      const save = async () => {
        try {
          await api('/api/cc-plugins/toggle', { method: 'POST', body: JSON.stringify({ key: p.id, values: { [key]: input.value } }) });
          note.textContent = `已保存 ${lab.textContent}`;
        } catch (e) { alert('保存失败：' + e.message); }
      };
      input.onchange = save;
      input.onkeydown = ev => { if (ev.key === 'Enter') { ev.preventDefault(); save(); } };
      line.appendChild(lab);
      line.appendChild(input);
      form.appendChild(line);
    }
    row.appendChild(form);
  }
  // 卸载
  const del = document.createElement('button');
  del.className = 'ccp-uninstall';
  del.textContent = '卸载';
  del.onclick = async () => {
    if (!confirm(`卸载插件「${p.displayName || p.name}」？本地缓存与运行数据将删除。`)) return;
    try {
      await api('/api/cc-plugins/uninstall', { method: 'POST', body: JSON.stringify({ key: p.id }) });
      await renderPluginList();
    } catch (e) { alert('卸载失败：' + e.message); }
  };
  row.appendChild(del);
  return row;
}

function renderCCMarketPanel(markets) {
  const panel = $('ccp-market-panel');
  if (!panel) return;
  panel.innerHTML = '';
  if (!markets.length) return;
  const sel = document.createElement('select');
  sel.className = 'ccp-market-select';
  const optNone = document.createElement('option');
  optNone.value = '';
  optNone.textContent = '浏览市场安装…';
  sel.appendChild(optNone);
  for (const m of markets) {
    const o = document.createElement('option');
    o.value = m.id;
    o.textContent = `${m.name || m.id}（${m.id}）`;
    sel.appendChild(o);
  }
  sel.onchange = async () => {
    const list = $('ccp-market-list');
    if (list) list.remove();
    if (!sel.value) return;
    const listDiv = document.createElement('div');
    listDiv.id = 'ccp-market-list';
    listDiv.className = 'ccp-market-list';
    listDiv.textContent = '加载中…';
    panel.appendChild(listDiv);
    try {
      const data = await api('/api/cc-plugins/marketplace/' + encodeURIComponent(sel.value));
      listDiv.innerHTML = '';
      for (const item of (data.plugins || [])) {
        const it = document.createElement('div');
        it.className = 'ccp-market-item' + (item.installed ? ' installed' : '');
        it.innerHTML =
          `<div class="pr-line"><b>${esc(item.name)}</b>${item.installed ? '<span class="ccp-badge">已装</span>' : ''}</div>` +
          `<p>${esc(item.description || '')}</p>`;
        if (!item.installed) {
          const btn = document.createElement('button');
          btn.className = 'ccp-install-btn';
          btn.textContent = '安装';
          btn.onclick = async () => {
            btn.disabled = true;
            btn.textContent = '安装中…';
            try {
              await api('/api/cc-plugins/install', { method: 'POST', body: JSON.stringify({ marketplace: sel.value, plugin: item.name }) });
              await renderPluginList(); // 统一插件页整页刷新（分组清单 + 市场面板）
              const keep = $('ccp-market-select');
              if (keep) { keep.value = sel.value; keep.onchange(); }
            } catch (e) { alert('安装失败：' + e.message); btn.disabled = false; btn.textContent = '安装'; }
          };
          it.appendChild(btn);
        }
        listDiv.appendChild(it);
      }
    } catch (e) {
      listDiv.textContent = '市场读取失败：' + e.message;
    }
  };
  panel.appendChild(sel);
}

/* 安装与市场添加按钮（绑定一次；成功后刷新统一插件页） */
let ccpBound = false;
function bindCCPluginPanel() {
  if (ccpBound) return;
  ccpBound = true;
  const installBtn = $('ccp-install-btn');
  if (installBtn) installBtn.onclick = async () => {
    const src = ($('ccp-install-src').value || '').trim();
    if (!src) { alert('请输入插件目录、zip 路径或 git/zip URL'); return; }
    installBtn.disabled = true;
    installBtn.textContent = '安装中…';
    try {
      await api('/api/cc-plugins/install', { method: 'POST', body: JSON.stringify(src.startsWith('http') || src.endsWith('.git') ? { url: src } : { path: src }) });
      $('ccp-install-src').value = '';
      await renderPluginList();
    } catch (e) { alert('安装失败：' + e.message); }
    installBtn.disabled = false;
    installBtn.textContent = '安装';
  };
  const marketBtn = $('ccp-market-btn');
  if (marketBtn) marketBtn.onclick = async () => {
    const src = ($('ccp-market-src').value || '').trim();
    if (!src) { alert('请输入市场的 git URL'); return; }
    marketBtn.disabled = true;
    marketBtn.textContent = '添加中…';
    try {
      await api('/api/cc-plugins/marketplace/add', { method: 'POST', body: JSON.stringify({ url: src }) });
      $('ccp-market-src').value = '';
      await renderPluginList();
    } catch (e) { alert('添加失败：' + e.message); }
    marketBtn.disabled = false;
    marketBtn.textContent = '添加市场';
  };
}

/* ---------- 斜杠命令弹层 ---------- */

let ccCmdCache = null;       // 命令清单缓存（fetch 一次）
let ccCmdPopup = null;       // 当前弹层 DOM
let ccCmdItems = [];         // 过滤后的候选
let ccCmdActive = -1;        // 高亮项

async function ccCommands() {
  if (ccCmdCache) return ccCmdCache;
  try { ccCmdCache = (await api('/api/cc-plugins/commands')).commands || []; }
  catch (e) { ccCmdCache = []; }
  return ccCmdCache;
}

function ccCloseCmdPopup() {
  if (ccCmdPopup) { ccCmdPopup.remove(); ccCmdPopup = null; ccCmdItems = []; ccCmdActive = -1; }
}

function ccHighlightCmd() {
  if (!ccCmdPopup) return;
  const rows = ccCmdPopup.querySelectorAll('.ccp-cmd-item');
  rows.forEach((r, i) => r.classList.toggle('active', i === ccCmdActive));
  if (ccCmdActive >= 0 && rows[ccCmdActive]) rows[ccCmdActive].scrollIntoView({ block: 'nearest' });
}

function ccPickCmd(name) {
  const input = $('input');
  if (!input) return;
  const cur = input.value;
  const rest = cur.indexOf(' ') > 0 ? cur.slice(cur.indexOf(' ')) : ' ';
  input.value = '/' + name + rest;
  input.focus();
  ccCloseCmdPopup();
}

async function ccShowCmdPopup(input) {
  const val = input.value;
  if (!val.startsWith('/')) { ccCloseCmdPopup(); return; }
  const word = val.slice(1).split(/\s/)[0].toLowerCase();
  const all = await ccCommands();
  if (!all.length) { ccCloseCmdPopup(); return; }
  ccCmdItems = word
    ? all.filter(c => c.name.toLowerCase().includes(word) || c.name.toLowerCase().split(':').pop().startsWith(word))
    : all;
  if (!ccCmdItems.length) { ccCloseCmdPopup(); return; }
  if (!ccCmdPopup) {
    ccCmdPopup = document.createElement('div');
    ccCmdPopup.id = 'ccp-cmd-pop';
    document.body.appendChild(ccCmdPopup);
    const rect = input.getBoundingClientRect();
    ccCmdPopup.style.left = Math.max(8, rect.left) + 'px';
    ccCmdPopup.style.width = Math.min(420, rect.width) + 'px';
    ccCmdPopup.style.top = ''; // CSS 定到输入框上方；窄屏由 CSS 兜底
    ccCmdPopup.style.bottom = (window.innerHeight - rect.top + 6) + 'px';
  }
  ccCmdPopup.innerHTML = '';
  ccCmdItems.slice(0, 8).forEach((c, i) => {
    const item = document.createElement('div');
    item.className = 'ccp-cmd-item';
    item.innerHTML =
      `<div class="ccp-cmd-name">/${esc(c.name)}${c.argumentHint ? ' <span class="ccp-cmd-hint">' + esc(c.argumentHint) + '</span>' : ''}</div>` +
      `<div class="ccp-cmd-desc">${esc(c.description || '')}</div>`;
    item.onmousedown = ev => { ev.preventDefault(); ccPickCmd(c.name); };
    item.onmouseenter = () => { ccCmdActive = i; ccHighlightCmd(); };
    ccCmdPopup.appendChild(item);
  });
  ccCmdActive = 0;
  ccHighlightCmd();
}

function bindCCSlashComposer() {
  const input = $('input');
  if (!input || input.dataset.ccpBound) return;
  input.dataset.ccpBound = '1';
  input.addEventListener('input', () => ccShowCmdPopup(input));
  input.addEventListener('focus', () => ccShowCmdPopup(input));
  input.addEventListener('blur', () => setTimeout(ccCloseCmdPopup, 150));
  input.addEventListener('keydown', ev => {
    if (!ccCmdPopup || !ccCmdItems.length) return;
    if (ev.key === 'ArrowDown') { ev.preventDefault(); ccCmdActive = Math.min(ccCmdActive + 1, Math.min(ccCmdItems.length, 8) - 1); ccHighlightCmd(); }
    else if (ev.key === 'ArrowUp') { ev.preventDefault(); ccCmdActive = Math.max(ccCmdActive - 1, 0); ccHighlightCmd(); }
    else if (ev.key === 'Enter' && ccCmdActive >= 0 && ccCmdItems[ccCmdActive]) {
      ev.preventDefault();
      ev.stopPropagation();
      ccPickCmd(ccCmdItems[ccCmdActive].name);
    } else if (ev.key === 'Escape') { ccCloseCmdPopup(); }
  }, true); // 捕获阶段：先于 app-chat 的回车发送处理
}

/* ---------- MCP 服务器（独立注册表 mcpservers*.go 的浏览器半边） ----------
   统一插件页的 mcp 分组行渲染（mcpServerRow：开关/测试/删除）+ 手动添加
   表单（bindMCPPanel）+ 设计软件桥目录（renderMCPCatalog，一键安装）。
   清单与总开关走统一 /api/plugins（layer=mcp），测试/删除/目录走 /api/mcp。
   连通状态（工具数/最近错误）由 renderPluginList 并行拉取的 /api/mcp 填进
   mcpStatusCache，行渲染时按名查。 */

let mcpStatusCache = [];   // /api/mcp 的 servers（renderPluginList 时刷新）
let mcpCatalogCache = [];  // /api/mcp/catalog 的 items

function mcpStatusOf(name) { return mcpStatusCache.find(s => s.name === name) || null; }

function mcpServerRow(p) {
  const st = mcpStatusOf(p.id) || {};
  const row = document.createElement('div');
  row.className = 'plugin-row';
  const info = document.createElement('div');
  info.className = 'pr-info';
  const badges = [];
  if (!st.enabled) badges.push('已停用');
  else if (st.connected) badges.push(`已连接 · ${st.toolCount} 个工具`);
  else if (st.lastError) badges.push('连接失败');
  else badges.push('未连接');
  const fromCatalog = (st.source || '').startsWith('catalog:');
  const cmdLine = st.type === 'http' || st.type === 'sse'
    ? st.url : [st.command, ...(st.args || [])].filter(Boolean).join(' ');
  info.innerHTML =
    `<div class="pr-line"><b>${esc(p.name)}</b>` +
    `<span class="pr-ver">${esc(st.type || 'stdio')}</span>` +
    `<span class="pr-ver">${fromCatalog ? '设计软件桥' : '手动添加'}</span></div>` +
    (badges.length ? `<div class="ccp-badges">${badges.map(b => `<span class="ccp-badge">${esc(b)}</span>`).join('')}</div>` : '') +
    `<p>${esc(p.description || '')}</p>` +
    (cmdLine ? `<div class="mcp-cmd">${esc(cmdLine)}</div>` : '') +
    (st.lastError ? `<div class="mcp-cmd">最近错误：${esc(st.lastError)}</div>` : '');
  // 总开关（走统一 /api/plugins，与各体系后端逻辑一致）
  const tg = document.createElement('label');
  tg.className = 'pr-toggle';
  const cb = document.createElement('input');
  cb.type = 'checkbox';
  cb.checked = !!p.enabled;
  cb.onchange = async () => {
    cb.disabled = true;
    try {
      await api('/api/plugins', { method: 'POST', body: JSON.stringify({ layer: p.layer, id: p.id, enabled: cb.checked }) });
      note.textContent = '已更新，新会话生效';
      await renderPluginList();
    } catch (e) { alert('切换失败：' + e.message); cb.checked = !cb.checked; cb.disabled = false; }
  };
  tg.appendChild(cb);
  tg.appendChild(document.createElement('span'));
  row.appendChild(info);
  row.appendChild(tg);
  const note = document.createElement('div');
  note.className = 'pr-note';
  row.appendChild(note);
  // 动作行：测试连通 / 删除
  const acts = document.createElement('div');
  acts.className = 'mcp-actions';
  const testBtn = document.createElement('button');
  testBtn.className = 'mcp-act';
  testBtn.textContent = '测试';
  testBtn.onclick = async () => {
    testBtn.disabled = true;
    testBtn.textContent = '连接中…';
    try {
      const res = await api('/api/mcp/test', { method: 'POST', body: JSON.stringify({ name: p.id }) });
      const names = (res.tools || []).map(t => t.name);
      note.textContent = `已连接：${res.count} 个工具（${names.slice(0, 10).join('、')}${names.length > 10 ? ' 等' : ''}）`;
    } catch (e) {
      note.textContent = '连接失败：' + e.message;
    }
    testBtn.disabled = false;
    testBtn.textContent = '测试';
  };
  acts.appendChild(testBtn);
  const del = document.createElement('button');
  del.className = 'mcp-act';
  del.textContent = '删除';
  del.onclick = async () => {
    if (!confirm(`删除 MCP 服务器「${p.name}」？`)) return;
    try {
      await api('/api/mcp/delete', { method: 'POST', body: JSON.stringify({ name: p.id }) });
      await renderPluginList();
    } catch (e) { alert('删除失败：' + e.message); }
  };
  acts.appendChild(del);
  row.appendChild(acts);
  return row;
}

/* 目录渲染：每桥一行（名称/对接软件/说明/前置条件 + 安装或已装徽章） */
function renderMCPCatalog(items) {
  mcpCatalogCache = items || [];
  const box = $('mcp-catalog');
  if (!box) return;
  box.innerHTML = '';
  const title = document.createElement('div');
  title.className = 'mcp-cat-title';
  title.textContent = '设计软件 MCP 桥目录（一键安装：下载代码 + 建独立虚拟环境 + 装依赖 + 注册，约需 1–3 分钟）';
  box.appendChild(title);
  for (const it of mcpCatalogCache) {
    const d = document.createElement('div');
    d.className = 'ccp-market-item mcp-cat-item' + (it.installed ? ' installed' : '');
    d.innerHTML =
      `<div class="pr-line"><b>${esc(it.name)}</b>${it.installed ? '<span class="ccp-badge">已装</span>' : ''}` +
      `<span class="mcp-vendor">${esc(it.vendor || '')}</span></div>` +
      `<p>${esc(it.description || '')}</p>` +
      (it.requirement ? `<p class="mcp-req">前置：${esc(it.requirement)}</p>` : '');
    if (!it.installed) {
      const btn = document.createElement('button');
      btn.className = 'ccp-install-btn';
      btn.textContent = '安装';
      btn.onclick = async () => {
        btn.disabled = true;
        btn.textContent = '安装中…（下载+装依赖，请勿关闭）';
        try {
          const res = await api('/api/mcp/catalog/install', { method: 'POST', body: JSON.stringify({ id: it.id }) });
          const tips = (res.notes || []).join('\n');
          if (tips) alert(`「${it.name}」安装完成。后续步骤：\n\n${tips}`);
          await renderPluginList();
        } catch (e) {
          alert('安装失败：' + e.message);
          btn.disabled = false;
          btn.textContent = '安装';
        }
      };
      d.appendChild(btn);
    }
    box.appendChild(d);
  }
}

/* 手动添加表单（绑定一次） */
let mcpBound = false;
function bindMCPPanel() {
  if (mcpBound) return;
  mcpBound = true;
  const addBtn = $('mcp-add-btn');
  if (addBtn) addBtn.onclick = async () => {
    const name = ($('mcp-add-name').value || '').trim();
    const type = $('mcp-add-type').value;
    const cmd = ($('mcp-add-cmd').value || '').trim();
    if (!name) { alert('请填写服务器名称'); return; }
    if (!cmd) { alert(type === 'http' ? '请填写服务地址' : '请填写启动命令'); return; }
    let body = { name, type };
    if (type === 'http') {
      body.url = cmd;
    } else {
      const parts = splitCommandLine(cmd);
      if (!parts.length) { alert('启动命令不能为空'); return; }
      body.command = parts[0];
      body.args = parts.slice(1);
    }
    addBtn.disabled = true;
    addBtn.textContent = '添加中…';
    try {
      await api('/api/mcp', { method: 'POST', body: JSON.stringify(body) });
      $('mcp-add-name').value = '';
      $('mcp-add-cmd').value = '';
      await renderPluginList();
    } catch (e) { alert('添加失败：' + e.message); }
    addBtn.disabled = false;
    addBtn.textContent = '添加';
  };
}

/* splitCommandLine 按空白拆启动命令，双/单引号内的空白保留。 */
function splitCommandLine(s) {
  const out = [];
  let cur = '', q = null;
  for (const ch of s) {
    if (q) { if (ch === q) q = null; else cur += ch; }
    else if (ch === '"' || ch === "'") q = ch;
    else if (/\s/.test(ch)) { if (cur) { out.push(cur); cur = ''; } }
    else cur += ch;
  }
  if (cur) out.push(cur);
  return out;
}

/* mcpLoadSideData 连通状态 + 目录（renderPluginList 并行调用，失败静默）。 */
async function mcpLoadSideData() {
  try { const d = await api('/api/mcp'); mcpStatusCache = d.servers || []; }
  catch (e) { mcpStatusCache = []; }
  try { const c = await api('/api/mcp/catalog'); renderMCPCatalog(c.items || []); }
  catch (e) { /* 目录暂不可用不阻断插件页 */ }
}

/* 启动：绑定安装区按钮与斜杠命令（清单渲染由 app-settings.js 的
   renderPluginList 统一负责，打开设置时触发） */
document.addEventListener('DOMContentLoaded', () => {
  bindCCPluginPanel();
  bindCCSlashComposer();
  bindMCPPanel();
});
