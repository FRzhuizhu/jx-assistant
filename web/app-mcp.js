/* MCP 连接器独立设置页（mcpservers*.go 的浏览器半边）：
   「设置 → MCP」分区——连接器卡片（开关/测试/展开工具清单/删除）+
   手动添加表单 + 设计软件桥目录一键安装。数据走 /api/mcp*；
   渲染入口 renderMCPPage 由 app-settings.js 的 openSettings 统一触发。
   复用 app-core.js 的 $ / api / esc；样式复用 plugin-row / ccp-* 与
   style.css 尾部的 mcp-* 前缀类。 */

/* ---------- 连接器卡片 ---------- */

// 工具清单缓存（name → tools 数组）：展开时惰性拉一次（走 /api/mcp/test，
// 与后端 ReadConnector 的懒启动同一条连接路径）
const mcpToolListCache = {};

function mcpConnectorCard(s) {
  const card = document.createElement('div');
  card.className = 'plugin-row mcp-card' + (s.enabled ? '' : ' off');
  // 基本信息 + 状态徽章
  const info = document.createElement('div');
  info.className = 'pr-info';
  const badges = [];
  if (!s.enabled) badges.push('已停用');
  else if (s.connected) badges.push(`已连接 · ${s.toolCount} 个工具`);
  else if (s.lastError) badges.push('连接失败');
  else badges.push('待命 · 按需启动');
  const fromCatalog = (s.source || '').startsWith('catalog:');
  const cmdLine = (s.type === 'http' || s.type === 'sse')
    ? s.url : [s.command, ...(s.args || [])].filter(Boolean).join(' ');
  info.innerHTML =
    `<div class="pr-line"><b>${esc(s.name)}</b>` +
    `<span class="pr-ver">${esc(s.type || 'stdio')}</span>` +
    `<span class="pr-ver">${fromCatalog ? '设计软件桥' : '手动添加'}</span></div>` +
    (badges.length ? `<div class="ccp-badges">${badges.map(b => `<span class="ccp-badge">${esc(b)}</span>`).join('')}</div>` : '') +
    (s.description ? `<p>${esc(s.description)}</p>` : '') +
    (cmdLine ? `<div class="mcp-cmd">${esc(cmdLine)}</div>` : '') +
    (s.lastError ? `<div class="mcp-cmd">最近错误：${esc(s.lastError)}</div>` : '');
  card.appendChild(info);
  // 总开关（设置页直连 /api/mcp：与后端 SetEnabled 同一条路）
  const tg = document.createElement('label');
  tg.className = 'pr-toggle';
  const cb = document.createElement('input');
  cb.type = 'checkbox';
  cb.checked = !!s.enabled;
  cb.onchange = async () => {
    cb.disabled = true;
    try {
      await api('/api/mcp', { method: 'POST', body: JSON.stringify({ name: s.name, description: s.description, type: s.type, command: s.command, args: s.args, env: s.env, url: s.url, headers: s.headers, enabled: cb.checked }) });
      await renderMCPPage();
    } catch (e) { alert('切换失败：' + e.message); cb.checked = !cb.checked; cb.disabled = false; }
  };
  tg.appendChild(cb);
  tg.appendChild(document.createElement('span'));
  card.appendChild(tg);
  // 动作行：展开工具清单 / 测试连通 / 删除
  const acts = document.createElement('div');
  acts.className = 'mcp-actions';
  const toolsBtn = document.createElement('button');
  toolsBtn.className = 'mcp-act';
  toolsBtn.textContent = '工具清单';
  toolsBtn.onclick = async () => {
    let box = card.querySelector('.mcp-tools');
    if (box) { box.remove(); return; } // 再点一次收起
    box = document.createElement('div');
    box.className = 'mcp-tools';
    box.textContent = '加载中…';
    card.appendChild(box);
    let tools = mcpToolListCache[s.name];
    if (!tools) {
      try {
        const res = await api('/api/mcp/test', { method: 'POST', body: JSON.stringify({ name: s.name }) });
        tools = res.tools || [];
        mcpToolListCache[s.name] = tools;
      } catch (e) {
        box.textContent = '连接失败：' + e.message;
        return;
      }
    }
    box.innerHTML = tools.map(t =>
      `<div class="mcp-tool-item"><b>${esc(t.name)}</b><span>${esc(t.description || '')}</span></div>`
    ).join('') || '（无工具）';
  };
  acts.appendChild(toolsBtn);
  const testBtn = document.createElement('button');
  testBtn.className = 'mcp-act';
  testBtn.textContent = '测试';
  testBtn.onclick = async () => {
    testBtn.disabled = true;
    testBtn.textContent = '连接中…';
    try {
      const res = await api('/api/mcp/test', { method: 'POST', body: JSON.stringify({ name: s.name }) });
      mcpToolListCache[s.name] = res.tools || [];
      await renderMCPPage();
      const note = document.querySelector('#mcp-connector-list .mcp-card .pr-note');
      if (note) note.textContent = `「${s.name}」已连接：${res.count} 个工具`;
    } catch (e) { alert('连接失败：' + e.message); }
    testBtn.disabled = false;
    testBtn.textContent = '测试';
  };
  acts.appendChild(testBtn);
  const del = document.createElement('button');
  del.className = 'mcp-act';
  del.textContent = '删除';
  del.onclick = async () => {
    if (!confirm(`删除 MCP 连接器「${s.name}」？`)) return;
    try {
      await api('/api/mcp/delete', { method: 'POST', body: JSON.stringify({ name: s.name }) });
      delete mcpToolListCache[s.name];
      await renderMCPPage();
    } catch (e) { alert('删除失败：' + e.message); }
  };
  acts.appendChild(del);
  card.appendChild(acts);
  return card;
}

/* ---------- 目录（设计软件桥一键安装） ---------- */

function renderMCPCatalog(items) {
  const box = $('mcp-catalog');
  if (!box) return;
  box.innerHTML = '';
  for (const it of (items || [])) {
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
          await renderMCPPage();
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

/* ---------- 页面渲染与表单绑定 ---------- */

async function renderMCPPage() {
  const list = $('mcp-connector-list');
  if (!list) return;
  list.textContent = '加载中…';
  const [st, cat] = await Promise.all([
    api('/api/mcp').catch(() => null),
    api('/api/mcp/catalog').catch(() => null),
  ]);
  list.innerHTML = '';
  const servers = (st && st.servers) || [];
  if (!servers.length) {
    const empty = document.createElement('div');
    empty.className = 'pr-note';
    empty.textContent = '还没有 MCP 连接器：可从下方目录一键安装，或用上方表单手动添加。';
    list.appendChild(empty);
  } else {
    for (const s of servers) list.appendChild(mcpConnectorCard(s));
  }
  renderMCPCatalog(cat && cat.items);
}

/* 手动添加表单（绑定一次） */
let mcpBound = false;
function bindMCPPanel() {
  if (mcpBound) return;
  mcpBound = true;
  const addBtn = $('mcp-add-btn');
  if (!addBtn) return;
  addBtn.onclick = async () => {
    const name = ($('mcp-add-name').value || '').trim();
    const type = $('mcp-add-type').value;
    const cmd = ($('mcp-add-cmd').value || '').trim();
    if (!name) { alert('请填写连接器名称'); return; }
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
      await renderMCPPage();
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

/* ---------- 拖拽导入 .json 配置（POST /api/mcp/upload） ----------
   把 MCP 配置文件直接拖进本页即注册连接器，形态与 Claude Desktop /
   Cursor / Codex 的配置一致：{"mcpServers":{...}}、{"名":{...}} 或单个
   服务器对象（文件名当服务器名）。多文件依次导入。 */
function bindMCPDropZone() {
  const pane = $('set-pane-mcp');
  if (!pane || pane.dataset.dropBound) return;
  pane.dataset.dropBound = '1';
  const note = document.createElement('div');
  note.className = 'pr-note';
  pane.addEventListener('dragover', (e) => {
    if (![...(e.dataTransfer.types || [])].includes('Files')) return;
    e.preventDefault();
    pane.classList.add('drop-hover');
  });
  pane.addEventListener('dragleave', (e) => {
    if (e.target === pane) pane.classList.remove('drop-hover');
  });
  pane.addEventListener('drop', async (e) => {
    e.preventDefault();
    pane.classList.remove('drop-hover');
    const files = [...(e.dataTransfer.files || [])];
    const jsons = files.filter((f) => /\.json$/i.test(f.name));
    if (!jsons.length) {
      alert(files.length
        ? '只支持拖入 .json 配置文件（{"mcpServers":{...}} 或 {"名":{...}} 形态；目录/zip 请走市场或手动添加）'
        : '拖入的内容不是文件');
      return;
    }
    pane.appendChild(note);
    note.textContent = `导入中（${jsons.length} 个）…`;
    let ok = 0, added = [];
    for (const f of jsons) {
      const fd = new FormData();
      fd.append('file', f, f.name);
      try {
        const res = await fetch('/api/mcp/upload', { method: 'POST', body: fd });
        const data = await res.json().catch(() => ({}));
        if (!res.ok) throw new Error(data.error || res.statusText);
        ok++;
        added = added.concat(data.servers || []);
      } catch (err) {
        alert(`「${f.name}」导入失败：` + err.message);
      }
    }
    note.textContent = ok
      ? `已导入 ${ok} 份配置，注册连接器：${added.join('、')}`
      : '导入失败（见提示）';
    renderMCPPage();
  });
}

document.addEventListener('DOMContentLoaded', () => { bindMCPPanel(); bindMCPDropZone(); });
