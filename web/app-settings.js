/* app-settings.js —— 设置弹窗：常规页/模型页/上下文页/组件/插件/技能、
   主题色与自定义下拉。第五个加载。 */

let provDraft = [];      // 模型页编辑中的提供方列表（不直接改全局配置）
let presetsCache = [];   // 后端预置提供方模板 [{id,name,baseURL}]
let provOpenKey = null;  // 展开的卡片：provider id / 'new' / null（手风琴式单开）
let provNew = null;      // 「添加提供方」的草稿
let provModelOpen = {};  // 卡片key -> 展开编辑的模型行下标（-1=无）
let provTest = {};       // 卡片key -> { 模型id: {state:'run'|'ok'|'fail', ms, err} }

// lsGet/lsSet 使用顶部 162 行的兼容版（对象 JSON、字符串原样），此处不再重复定义。

// 设置浮窗：左目录切换
function setSettingsPane(name) {
  document.querySelectorAll('.set-nav-item').forEach((b) => {
    b.classList.toggle('active', b.dataset.pane === name);
  });
  document.querySelectorAll('.set-pane').forEach((p) => {
    p.classList.toggle('active', p.id === 'set-pane-' + name);
  });
}

function loadProvDraft(c) {
  return (c.providers || []).map((p) => ({
    id: p.id,
    presetId: p.preset ? p.id : null, // 预置提供方用系统地址，不出 URL 输入框
    presetURL: p.presetURL || '',
    name: p.name || '',
    baseURL: p.baseURL || '',
    key: '', keyMasked: p.apiKeyMasked || '',
    models: (p.models || []).map((m) => ({
      id: m.id || '', name: m.name || '',
      contextWindow: m.contextWindow || 0, maxOutput: m.maxOutput || 0,
      supportsImages: !!m.supportsImages,
    })),
  }));
}

// pane 可选：打开后直接落在指定分区（如 'network'，左下角手机图标用）；缺省常规页
async function openSettings(pane) {
  const c = await api('/api/config');
  presetsCache = c.presets || [];
  provDraft = loadProvDraft(c);
  provOpenKey = null; provNew = null; provTest = {}; provModelOpen = {};
  renderProvCards();
  selRefs.compactMode.set(c.compactMode || 'rule');
  $('set-keeprounds').value = c.keepRounds || 5;
  $('set-ruleinterval').value = c.ruleInterval || 5;
  selRefs.compactThreshold.set(String(c.compactThreshold || 0.8));
  // 常规页：文件备份（服务端配置，改动即时保存）
  $('set-snapshot-on').checked = c.snapshotEnabled !== false;
  syncSnapshotState(c.snapshotEnabled !== false);
  $('set-snapshot-days').value = c.snapshotDays != null ? c.snapshotDays : 7;
  // 常规页：任务最大步数（服务端配置，改动即时保存）
  $('set-maxsteps').value = c.maxSteps || 30;
  // 常规页：局域网访问（服务端配置，写 bindHost，重启程序后生效）
  $('set-lan-on').checked = c.bindHost === '0.0.0.0' || c.bindHost === '::';
  syncLanState(c);
  // 常规页：远程接入（服务端配置，写 remoteAccess.enabled，重启程序后生效）
  $('set-remote-on').checked = !!(c.remoteAccess && c.remoteAccess.enabled);
  syncRemoteState(c);
  // 常规页：本机浏览器偏好（即时生效，打开时回显当前值）
  selRefs.lang.set(lsGet('jx.lang', 'zh'));
  selRefs.fontsize.set(lsGet('jx.fontpx', '14'));
  syncThemeCards();
  selRefs.sendkey.set(lsGet('jx.sendkey', 'enter'));
  selRefs.midrun.set(lsGet('jx.midrun', 'interject'));
  renderPluginList();
  mountPluginSections();
  renderComponentList();
  renderSkillsPanel();
  renderExpertsPanel();
  setSettingsPane(pane && document.querySelector('.set-nav-item[data-pane="' + pane + '"]') ? pane : 'general');
  // 关于页信息
  try {
    const st = await api('/api/status');
    $('about-version').textContent = '版本 ' + (st.version || '-') + ' · ' + (st.model || '');
    $('about-status').textContent = st.hasKey ? '当前：已连接模型服务' : '当前：未配置 Key';
  } catch (e) { $('about-status').textContent = '服务状态未知'; }
  $('settings').classList.remove('hidden');
  // 手机端：进设置即收起侧栏浮层（返回后落在对话区），并从一级菜单起步；
  // 压一层历史让返回手势可以退出设置（见底部两级菜单节）
  narrowClosePanels();
  setL2On(false);
  if (isNarrow() && setHistDepth === 0) {
    setHistDepth = 1;
    history.pushState({ jxSettings: 1 }, '');
  }
}

/* ---------- 组件清单（/api/components + CX 运行时热开关） ---------- */

async function renderComponentList() {
  const box = $('component-list');
  if (!box) return;
  box.innerHTML = '加载中…';
  let list = [];
  try { list = (await api('/api/components')).components || []; } catch (e) { box.textContent = '组件清单读取失败：' + e.message; return; }
  box.innerHTML = '';
  for (const c of list) {
    const row = document.createElement('div');
    row.className = 'plugin-row';
    const info = document.createElement('div');
    info.className = 'pr-info';
    info.innerHTML =
      `<div class="pr-line"><b>${esc(c.name)}</b><span class="pr-ver">${esc(c.version || '')}</span></div>` +
      `<p>${esc(c.description || '')}</p>`;
    const tg = document.createElement('label');
    tg.className = 'pr-toggle';
    const cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.checked = !!c.enabled;
    cb.onchange = async () => {
      cb.disabled = true;
      const note = row.querySelector('.pr-note');
      try {
        const r = await CX.toggle(c.id, cb.checked);
        if (note) note.textContent = r.hot ? (cb.checked ? '已即时启用' : '已即时停用') : '该组件不支持热关闭，刷新页面后完全生效';
      } catch (e) {
        alert('切换失败：' + e.message);
        cb.checked = !cb.checked;
      }
      cb.disabled = false;
      setTimeout(() => { if (note) note.textContent = ''; }, 3000);
    };
    tg.appendChild(cb);
    tg.appendChild(document.createElement('span'));
    row.appendChild(info);
    row.appendChild(tg);
    const note = document.createElement('div');
    note.className = 'pr-note';
    row.appendChild(note);
    box.appendChild(row);
  }
  if (!list.length) box.innerHTML = '<div class="pr-note">组件/ 下没有发现组件</div>';
}

/* ---------- 插件清单与分区（/api/plugins + JX 注册的分区） ---------- */

async function renderPluginList() {
  const box = $('plugin-list');
  if (!box) return;
  box.innerHTML = '加载中…';
  let list = [];
  try { list = (await api('/api/plugins')).plugins || []; } catch (e) { box.textContent = '插件清单读取失败：' + e.message; return; }
  box.innerHTML = '';
  for (const p of list) {
    const row = document.createElement('div');
    row.className = 'plugin-row' + (p.adapted ? '' : ' unadapted');
    const info = document.createElement('div');
    info.className = 'pr-info';
    info.innerHTML =
      `<div class="pr-line"><b>${esc(p.name)}</b><span class="pr-ver">${esc(p.version || '')}</span>` +
      (p.adapted ? '' : '<span class="pr-badge">未适配</span>') + '</div>' +
      `<p>${esc(p.description || '')}</p>`;
    const tg = document.createElement('label');
    tg.className = 'pr-toggle';
    const cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.checked = !!p.enabled;
    cb.disabled = !p.adapted;
    cb.onchange = async () => {
      cb.disabled = true;
      try {
        await api('/api/plugins', { method: 'POST', body: JSON.stringify({ id: p.id, enabled: cb.checked }) });
        row.querySelector('.pr-note').textContent = '已更新；界面部分刷新页面后生效';
      } catch (e) {
        alert('切换失败：' + e.message);
        cb.checked = !cb.checked;
      }
      cb.disabled = !p.adapted;
    };
    tg.appendChild(cb);
    tg.appendChild(document.createElement('span'));
    row.appendChild(info);
    row.appendChild(tg);
    const note = document.createElement('div');
    note.className = 'pr-note';
    row.appendChild(note);
    box.appendChild(row);
  }
  if (!list.length) box.innerHTML = '<div class="pr-note">插件兼容层/ 下没有发现插件</div>';
}

function mountPluginSections() {
  const box = $('plugin-sections');
  if (!box) return;
  box.innerHTML = '';
  for (const sec of JX._settingsSections) {
    const wrap = document.createElement('div');
    wrap.className = 'prov-section jx-section';
    const h = document.createElement('h4');
    h.textContent = sec.title;
    wrap.appendChild(h);
    const body = document.createElement('div');
    wrap.appendChild(body);
    try { sec.mount(body); } catch (e) { body.textContent = '插件设置加载失败：' + e.message; }
    box.appendChild(wrap);
  }
}

/* ---------- 模型页：提供方卡片（点击展开即全量编辑，每卡单独保存） ---------- */

function presetOf(id) { return presetsCache.find((p) => p.id === id) || null; }

function fmtTok(n) {
  if (!n || n <= 0) return '';
  if (n >= 1048576) return (Math.round(n / 1048576 * 10) / 10).toString().replace(/\.0$/, '') + 'M';
  return Math.round(n / 1024) + 'K';
}

// 卡片当前生效的 API 地址（预置 = 系统地址，自定义 = 表单里的）
function effURL(p) {
  if (p.presetId) {
    const t = presetOf(p.presetId);
    return (t && t.baseURL) || p.presetURL || p.baseURL;
  }
  return p.baseURL;
}

async function reloadProviders() {
  const c = await api('/api/config');
  provDraft = loadProvDraft(c);
  provOpenKey = null; provNew = null; provTest = {}; provModelOpen = {};
  renderProvCards();
  loadModels();
}

function renderProvCards() {
  const box = $('prov-list');
  if (!box) return;
  box.innerHTML = '';
  provDraft.forEach((p) => box.appendChild(buildProvCard(p, p.id)));
  if (provOpenKey === 'new' && provNew) box.appendChild(buildProvCard(provNew, 'new'));
}

function buildProvCard(p, key) {
  const open = provOpenKey === key;
  const card = document.createElement('div');
  card.className = 'prov-card' + (open ? ' open' : '');

  // 折叠头：状态点 + 名称 + 模型数 + 编辑/收起
  const configured = !!(p.key || p.keyMasked);
  const head = document.createElement('div');
  head.className = 'pv-head';
  head.innerHTML =
    `<span class="pv-dot ${configured ? 'ok' : 'no'}" title="${configured ? '密钥已配置' : '未配置密钥'}"></span>` +
    `<b class="pv-name">${esc(p.name || '未命名提供方')}</b>` +
    `<span class="pv-meta">${p.models.length} 个模型</span>` +
    `<span class="pv-edit">${open ? '收起' : '编辑'}</span>`;
  head.onclick = () => {
    if (open) {
      if (key === 'new') { provNew = null; provOpenKey = null; renderProvCards(); }
      else reloadProviders(); // 收起 = 丢弃未保存的修改
    } else {
      provOpenKey = key;
      if (provModelOpen[key] === undefined) provModelOpen[key] = -1;
      renderProvCards();
    }
  };
  card.appendChild(head);
  if (!open) return card;

  const form = document.createElement('div');
  form.className = 'pv-form';

  // 提供方选择：预置清单 + 自定义
  const lblProv = document.createElement('div');
  lblProv.className = 'pf-label';
  lblProv.textContent = '提供方';
  form.appendChild(lblProv);
  const sel = document.createElement('select');
  sel.className = 'pf-input';
  for (const t of presetsCache) {
    const o = document.createElement('option');
    o.value = t.id; o.textContent = t.name;
    sel.appendChild(o);
  }
  const oCustom = document.createElement('option');
  oCustom.value = '__custom'; oCustom.textContent = '自定义提供方';
  sel.appendChild(oCustom);
  sel.value = p.presetId || '__custom';
  sel.onchange = () => {
    if (sel.value === '__custom') {
      p.presetId = null;
    } else {
      p.presetId = sel.value;
      const t = presetOf(sel.value);
      if (t) { p.name = t.name; p.baseURL = ''; p.presetURL = t.baseURL; }
    }
    renderProvCards();
  };
  form.appendChild(sel);

  // 名称 + API 地址（预置方用系统地址，只展示不可改）
  if (p.presetId) {
    const lblUrl = document.createElement('div');
    lblUrl.className = 'pf-label';
    lblUrl.textContent = 'API 地址';
    form.appendChild(lblUrl);
    const fixed = document.createElement('div');
    fixed.className = 'pf-fixed';
    fixed.textContent = '使用系统预设地址：' + effURL(p);
    form.appendChild(fixed);
  } else {
    const two = document.createElement('div');
    two.className = 'pf-two';
    const cellName = document.createElement('div');
    cellName.innerHTML = '<div class="pf-label">名称</div>';
    const inName = document.createElement('input');
    inName.className = 'pf-input';
    inName.placeholder = '如 硅基流动';
    inName.value = p.name;
    inName.oninput = () => { p.name = inName.value; };
    cellName.appendChild(inName);
    const cellUrl = document.createElement('div');
    cellUrl.innerHTML = '<div class="pf-label">API 地址（OpenAI 兼容）</div>';
    const inUrl = document.createElement('input');
    inUrl.className = 'pf-input';
    inUrl.placeholder = 'https://…/v1';
    inUrl.value = p.baseURL;
    inUrl.oninput = () => { p.baseURL = inUrl.value; };
    cellUrl.appendChild(inUrl);
    two.appendChild(cellName);
    two.appendChild(cellUrl);
    form.appendChild(two);
  }

  // API 密钥：绝不回显，只标「已配置」，输入可替换
  const lblKey = document.createElement('div');
  lblKey.className = 'pf-label';
  lblKey.textContent = 'API 密钥';
  form.appendChild(lblKey);
  const keyRow = document.createElement('div');
  keyRow.className = 'key-row';
  if (p.keyMasked) {
    const chip = document.createElement('span');
    chip.className = 'key-chip';
    chip.textContent = '已配置';
    keyRow.appendChild(chip);
  }
  const inKey = document.createElement('input');
  inKey.className = 'pf-input';
  inKey.type = 'password';
  inKey.autocomplete = 'new-password';
  inKey.placeholder = p.keyMasked ? '留空保持不变，输入可替换' : (p.presetId === 'ollama' ? '本地服务可留空' : '必填，如 sk-…');
  inKey.oninput = () => { p.key = inKey.value; };
  keyRow.appendChild(inKey);
  form.appendChild(keyRow);

  // 模型目录
  const cat = document.createElement('div');
  cat.className = 'model-cat';
  const catHead = document.createElement('div');
  catHead.className = 'mc-head';
  catHead.innerHTML = '<span class="mc-title">模型目录</span>';
  const btnTest = document.createElement('button');
  btnTest.className = 'mc-btn';
  btnTest.textContent = '测试连通';
  const btnFetch = document.createElement('button');
  btnFetch.className = 'mc-btn';
  btnFetch.textContent = '拉取模型';
  catHead.appendChild(btnTest);
  catHead.appendChild(btnFetch);
  cat.appendChild(catHead);

  p.models.forEach((m, mi) => {
    const row = document.createElement('div');
    row.className = 'model-row';
    const line = document.createElement('div');
    line.className = 'mr-line';
    const chips = [
      m.contextWindow ? '上下文 ' + fmtTok(m.contextWindow) : '上下文未知',
      m.maxOutput ? '输出 ' + fmtTok(m.maxOutput) : null,
      m.supportsImages ? '图片 ✓' : null,
    ].filter(Boolean).join(' · ');
    line.innerHTML =
      `<span class="mr-id">${esc(m.id || '(未命名模型)')}</span>` +
      `<span class="mr-test"></span>` +
      `<span class="mr-chips">${esc(chips)}</span>` +
      `<button class="mr-x" title="删除该模型">✕</button>`;
    const testEl = line.querySelector('.mr-test');
    const t = (provTest[key] || {})[m.id];
    if (t && t.state === 'run') { testEl.textContent = '测试中…'; testEl.className = 'mr-test run'; }
    else if (t && t.state === 'ok') { testEl.textContent = '✓ ' + t.ms + 'ms'; testEl.className = 'mr-test ok'; }
    else if (t && t.state === 'fail') { testEl.textContent = '✗ ' + t.err; testEl.title = t.err; testEl.className = 'mr-test fail'; }
    line.querySelector('.mr-x').onclick = (e) => {
      e.stopPropagation();
      p.models.splice(mi, 1);
      renderProvCards();
    };
    line.onclick = () => {
      provModelOpen[key] = provModelOpen[key] === mi ? -1 : mi;
      renderProvCards();
    };
    row.appendChild(line);
    if (provModelOpen[key] === mi) {
      const edit = document.createElement('div');
      edit.className = 'mr-edit';
      const mk = (labelText, inputEl, full) => {
        const cell = document.createElement('div');
        if (full) cell.className = 'full';
        const l = document.createElement('div');
        l.className = 'pf-label';
        l.textContent = labelText;
        cell.appendChild(l);
        cell.appendChild(inputEl);
        return cell;
      };
      const inMid = document.createElement('input');
      inMid.className = 'pf-input';
      inMid.placeholder = '模型 ID，如 deepseek-chat';
      inMid.value = m.id;
      inMid.oninput = () => { m.id = inMid.value; };
      edit.appendChild(mk('模型 ID', inMid));
      const inMname = document.createElement('input');
      inMname.className = 'pf-input';
      inMname.placeholder = '选填，显示名称';
      inMname.value = m.name;
      inMname.oninput = () => { m.name = inMname.value; };
      edit.appendChild(mk('显示名称', inMname));
      const inCtx = document.createElement('input');
      inCtx.className = 'pf-input';
      inCtx.type = 'number';
      inCtx.min = '0';
      inCtx.placeholder = '默认 262144';
      inCtx.value = m.contextWindow || '';
      inCtx.oninput = () => { m.contextWindow = parseInt(inCtx.value, 10) || 0; };
      edit.appendChild(mk('上下文窗口（tokens）', inCtx));
      const inMax = document.createElement('input');
      inMax.className = 'pf-input';
      inMax.type = 'number';
      inMax.min = '0';
      inMax.placeholder = '不限';
      inMax.value = m.maxOutput || '';
      inMax.oninput = () => { m.maxOutput = parseInt(inMax.value, 10) || 0; };
      edit.appendChild(mk('最大输出（tokens）', inMax));
      // 支持图片：苹果式开关
      const tg = document.createElement('label');
      tg.className = 'pr-toggle';
      const cb = document.createElement('input');
      cb.type = 'checkbox';
      cb.checked = !!m.supportsImages;
      cb.onchange = () => { m.supportsImages = cb.checked; };
      tg.appendChild(cb);
      tg.appendChild(document.createElement('span'));
      edit.appendChild(mk('是否支持图片', tg));
      const del = document.createElement('button');
      del.className = 'mr-del2';
      del.textContent = '删除该模型';
      del.onclick = (e) => { e.stopPropagation(); p.models.splice(mi, 1); renderProvCards(); };
      edit.appendChild(del);
      row.appendChild(edit);
    }
    cat.appendChild(row);
  });

  const btnAddModel = document.createElement('button');
  btnAddModel.className = 'mc-add';
  btnAddModel.textContent = '＋ 添加模型';
  btnAddModel.onclick = () => {
    p.models.push({ id: '', name: '', contextWindow: 0, maxOutput: 0, supportsImages: false });
    provModelOpen[key] = p.models.length - 1;
    renderProvCards();
    const inputs = $('prov-list').querySelectorAll('.mr-edit input');
    if (inputs.length) inputs[inputs.length - 5 >= 0 ? inputs.length - 5 : 0].focus();
  };
  cat.appendChild(btnAddModel);
  form.appendChild(cat);

  // 拉取模型：合并策略 —— 已有条目保留手工改过的元信息，新条目用后端猜的
  btnFetch.onclick = async () => {
    btnFetch.disabled = true; btnFetch.textContent = '拉取中…';
    try {
      const d = await api('/api/providers/fetch', {
        method: 'POST',
        body: JSON.stringify({ id: key === 'new' ? '' : p.id, baseURL: effURL(p), apiKey: p.key }),
      });
      const oldMap = new Map(p.models.map((m) => [m.id, m]));
      const merged = [];
      for (const fm of (d.models || [])) {
        const ex = oldMap.get(fm.id);
        if (ex) { oldMap.delete(fm.id); merged.push(ex); }
        else merged.push({ id: fm.id, name: fm.name || '', contextWindow: fm.contextWindow || 0, maxOutput: 0, supportsImages: !!fm.supportsImages });
      }
      for (const ex of oldMap.values()) merged.push(ex); // 手工添加的保留在末尾
      p.models = merged;
      provModelOpen[key] = -1;
      renderProvCards();
    } catch (e) {
      alert('拉取失败：' + e.message);
      btnFetch.disabled = false; btnFetch.textContent = '拉取模型';
    }
  };

  // 测试连通：目录里的所有模型各发一个极小请求
  btnTest.onclick = async () => {
    const ids = p.models.map((m) => m.id).filter(Boolean);
    if (!ids.length) { alert('目录里还没有模型，先「拉取模型」或「添加模型」'); return; }
    provTest[key] = {};
    ids.forEach((id) => { provTest[key][id] = { state: 'run' }; });
    btnTest.disabled = true; btnTest.textContent = '测试中…';
    renderProvCards();
    try {
      const d = await api('/api/providers/test', {
        method: 'POST',
        body: JSON.stringify({ id: key === 'new' ? '' : p.id, baseURL: effURL(p), apiKey: p.key, models: ids }),
      });
      provTest[key] = {};
      for (const r of (d.results || [])) provTest[key][r.id] = { state: r.ok ? 'ok' : 'fail', ms: r.ms, err: r.err || '失败' };
    } catch (e) {
      provTest[key] = {};
      ids.forEach((id) => { provTest[key][id] = { state: 'fail', err: e.message }; });
    }
    renderProvCards();
  };

  // 卡片操作区：删除 | 取消 | 保存
  const acts = document.createElement('div');
  acts.className = 'pf-actions';
  if (key !== 'new') {
    const delBtn = document.createElement('button');
    delBtn.className = 'pf-del';
    delBtn.textContent = '删除提供方';
    delBtn.onclick = async () => {
      if (!confirm('删除提供方「' + (p.name || p.id) + '」？以后可重新添加。')) return;
      try {
        await api('/api/providers/delete', { method: 'POST', body: JSON.stringify({ id: p.id }) });
        reloadProviders();
      } catch (e) { alert('删除失败：' + e.message); }
    };
    acts.appendChild(delBtn);
  }
  const btnCancel = document.createElement('button');
  btnCancel.className = 'pf-cancel';
  btnCancel.textContent = '取消';
  btnCancel.onclick = () => {
    if (key === 'new') { provNew = null; provOpenKey = null; renderProvCards(); }
    else reloadProviders();
  };
  const btnSave = document.createElement('button');
  btnSave.className = 'pf-save';
  btnSave.textContent = '保存';
  btnSave.onclick = async () => {
    if (!p.presetId && !p.name.trim()) { alert('请填写名称'); return; }
    if (!p.presetId && !p.baseURL.trim()) { alert('请填写 API 地址'); return; }
    if (!p.key && !p.keyMasked && !confirm('尚未填写 API 密钥，确定保存？（本地服务如 Ollama 可不填）')) return;
    try {
      await api('/api/providers', {
        method: 'POST',
        body: JSON.stringify({
          id: p.id, name: p.name,
          baseURL: p.presetId ? '' : p.baseURL.trim(),
          apiKey: p.key,
          models: p.models,
        }),
      });
      reloadProviders();
    } catch (e) { alert('保存失败：' + e.message); }
  };
  acts.appendChild(btnCancel);
  acts.appendChild(btnSave);
  form.appendChild(acts);

  card.appendChild(form);
  return card;
}

async function saveSettings() {
  // 模型页各卡片单独保存（/api/providers）；常规页即时落本机浏览器；
  // 底部「保存」只负责上下文页。
  const body = {
    compact: {
      mode: selRefs.compactMode.get(),
      keepRounds: parseInt($('set-keeprounds').value, 10) || 5,
      ruleInterval: parseInt($('set-ruleinterval').value, 10) || 5,
      threshold: parseFloat(selRefs.compactThreshold.get()) || 0.8,
    },
  };
  await api('/api/config', { method: 'POST', body: JSON.stringify(body) });
  closeSettingsUI();
  loadStatus();
}

/* ---------- 常规页：本机浏览器偏好（语言/字体/发送行为，即时生效） ---------- */

// 字号只作用于对话区（.msg-col 的 --chat-fs，内部字号均为 em 相对值），
// 侧栏/设置/输入框不受影响；具体 px 值直接可调
function applyChatFont(px) {
  document.documentElement.style.setProperty('--chat-fs', px + 'px');
  lsSet('jx.fontpx', String(px));
}

function restoreChatFont() {
  const px = parseFloat(lsGet('jx.fontpx', '14')) || 14;
  document.documentElement.style.setProperty('--chat-fs', px + 'px');
}
/* ---------- 常规页：文件备份（服务端配置，改动即时保存） ---------- */

function syncSnapshotState(on) {
  $('set-snapshot-state').textContent = on ? '已开启' : '已关闭';
}

async function saveSnapshotCfg(patch) {
  try {
    const c = await api('/api/config', { method: 'POST', body: JSON.stringify(patch) });
    $('set-snapshot-on').checked = c.snapshotEnabled !== false;
    syncSnapshotState(c.snapshotEnabled !== false);
    if (c.snapshotDays != null) $('set-snapshot-days').value = c.snapshotDays;
  } catch (e) {
    alert('保存失败：' + e.message);
  }
}

$('set-snapshot-on').onchange = () => saveSnapshotCfg({ snapshotEnabled: $('set-snapshot-on').checked });
$('set-snapshot-days').onchange = () => {
  const v = parseInt($('set-snapshot-days').value, 10);
  if (isNaN(v) || (v < 1 && v !== -1)) { alert('天数需为 ≥1 的整数，或 -1（永不过期）'); return; }
  saveSnapshotCfg({ snapshotDays: v });
};

/* ---------- 常规页：任务最大步数（服务端配置，改动即时保存） ---------- */

$('set-maxsteps').onchange = async () => {
  const v = parseInt($('set-maxsteps').value, 10);
  if (isNaN(v) || v < 1) { alert('步数需为 ≥1 的整数'); return; }
  try {
    const c = await api('/api/config', { method: 'POST', body: JSON.stringify({ maxSteps: v }) });
    $('set-maxsteps').value = c.maxSteps || v; // 以服务端落库值为准回显
  } catch (e) {
    alert('保存失败：' + e.message);
  }
};

/* ---------- 常规页：局域网访问（bindHost 127.0.0.1 ⇄ 0.0.0.0，重启生效） ---------- */

function syncLanState(c) {
  const on = c.bindHost === '0.0.0.0' || c.bindHost === '::';
  $('set-lan-state').textContent = on ? '已开启（重启程序后对外可访问）' : '已关闭（仅本机可访问）';
  const urls = c.lanIPs || [];
  const port = location.port || '80';
  $('set-lan-urls').innerHTML = on && urls.length
    ? '手机连同一 Wi-Fi 后用浏览器打开：' + urls.map((ip) => `http://${esc(ip)}:${esc(port)}`).join('　或　')
    : '';
}

$('set-lan-on').onchange = async () => {
  const on = $('set-lan-on').checked;
  try {
    const c = await api('/api/config', {
      method: 'POST',
      body: JSON.stringify({ bindHost: on ? '0.0.0.0' : '127.0.0.1' }),
    });
    syncLanState(c);
  } catch (e) {
    alert('保存失败：' + e.message);
    $('set-lan-on').checked = !on;
  }
};

/* ---------- 网络访问页：远程接入（开关 + 设备号/密钥/网关编辑，重启生效） ---------- */

let lastRemoteEntry = ''; // 入口链接快照：复制用 + 二维码按它增量重绘

function syncRemoteState(c) {
  const ra = c.remoteAccess || {};
  $('set-remote-state').textContent = ra.connected
    ? '已连接网关（重启程序后改动生效）'
    : (ra.enabled ? '已开启（网关未连上：' + (ra.lastError || '等待重连') + '）' : '已关闭');
  const entryEl = $('set-remote-entry');
  entryEl.innerHTML = ra.enabled && ra.entry
    ? '手机（外网可用）扫码或浏览器打开并收藏：<b>' + esc(ra.entry) + '</b>'
    : '';
  entryEl.style.cursor = ra.entry ? 'pointer' : '';
  lastRemoteEntry = ra.entry || '';
  // 二维码：入口变化才重绘（生成不便宜），未开启时收起
  const qrBox = $('remote-qr');
  if (qrBox) {
    if (ra.enabled && ra.entry) {
      if (qrBox.dataset.entry !== ra.entry) {
        try {
          qrBox.innerHTML = qrLib.qrSVG(ra.entry, { size: 208 });
          qrBox.dataset.entry = ra.entry;
        } catch (e) { qrBox.textContent = '二维码生成失败：' + e.message; }
      }
      qrBox.classList.remove('hidden');
    } else {
      qrBox.classList.add('hidden');
      qrBox.dataset.entry = '';
    }
  }
  // 输入框以服务端落库值为准回显（焦点中不清扰，保存回包才刷新）
  if (document.activeElement !== $('set-remote-id')) $('set-remote-id').value = ra.myId || '';
  if (document.activeElement !== $('set-remote-pk')) $('set-remote-pk').value = ra.pairKey || '';
  if (document.activeElement !== $('set-remote-host')) $('set-remote-host').value = ra.host || '';
  if (document.activeElement !== $('set-remote-port')) $('set-remote-port').value = ra.port || 2227;
}

// 入口链接点击复制（含配对密钥，复制成功给个轻提示）
$('set-remote-entry').addEventListener('click', () => {
  if (!lastRemoteEntry) return;
  navigator.clipboard.writeText(lastRemoteEntry).then(() => {
    const el = $('set-remote-entry');
    const old = el.innerHTML;
    el.innerHTML = '<b>已复制 ✓</b>';
    setTimeout(() => { el.innerHTML = old; }, 1200);
  }).catch(() => {});
});

$('set-remote-on').onchange = async () => {
  const on = $('set-remote-on').checked;
  try {
    const c = await api('/api/config', {
      method: 'POST',
      body: JSON.stringify({ remoteEnabled: on }),
    });
    syncRemoteState(c);
  } catch (e) {
    alert('保存失败：' + e.message);
    $('set-remote-on').checked = !on;
  }
};

// 设备号/密钥/网关：改动即存（单字段 PATCH），失败回滚为回包值
function bindRemoteInput(id, patchKey, parse) {
  $(id).onchange = async () => {
    const v = parse ? parse($(id).value) : $(id).value.trim();
    try {
      const c = await api('/api/config', {
        method: 'POST',
        body: JSON.stringify({ [patchKey]: v }),
      });
      syncRemoteState(c);
    } catch (e) {
      alert('保存失败：' + e.message);
      const cur = (await api('/api/config').catch(() => null));
      if (cur) syncRemoteState(cur);
    }
  };
}
bindRemoteInput('set-remote-id', 'remoteMyId');
bindRemoteInput('set-remote-pk', 'remotePairKey');
bindRemoteInput('set-remote-host', 'remoteHost');
bindRemoteInput('set-remote-port', 'remotePort', (v) => parseInt(v, 10) || 0);

$('btn-settings').onclick = openSettings;
$('set-save').onclick = saveSettings;

/* ---------- 手机端：两级菜单 + 返回键逐级退出 ----------
   一级＝导航列表（整屏），点入二级＝对应分区（带返回头）。打开/进入二级时
   pushState 压栈，手机返回手势由此逐级退回（二级→一级→关设置回对话区）；
   取消/保存则主动回退等量历史，不留死栈。桌面不压栈、不分页，维持左右布局。 */
let setHistDepth = 0; // 已压入的设置历史层数：0 无 / 1 设置一级 / 2 二级分区

function settingsCard() { return document.querySelector('.settings-card'); }

// 手机一级 ⇄ 二级的视图切换（只动 class，桌面 CSS 不消费这些类）。
// 返回头标题随之切换：一级叫「设置」，二级＝当前分区名
function setL2On(on) {
  const card = settingsCard();
  if (!card) return;
  card.classList.toggle('pane-open', !!on);
  const nav = document.querySelector('.set-nav-item.active');
  $('set-back-title').textContent = on && nav ? nav.textContent.trim() : '设置';
}

function closeSettingsUI() {
  $('settings').classList.add('hidden');
  setL2On(false);
  const d = setHistDepth;
  setHistDepth = 0;
  if (d > 0) history.go(-d); // 退掉自己压入的栈；popstate 到达时已隐藏，无副作用
  loadStatus(); // 设置里可能刚改了远程开关：左下角手机图标状态随之对齐
}

window.addEventListener('popstate', (e) => {
  const d = (e.state && e.state.jxSettings) || 0;
  if ($('settings').classList.contains('hidden')) { setHistDepth = 0; return; } // UI 已关：只对齐
  setHistDepth = d;
  if (d >= 1) setL2On(d === 2); // 返回手势：二级退一级、一级再退＝下面的 d===0
  if (d === 0) closeSettingsUI();
});

$('set-cancel').onclick = closeSettingsUI;
$('settings').onclick = (e) => { if (e.target === $('settings')) closeSettingsUI(); };
$('set-back').onclick = () => {
  if (setHistDepth >= 2) history.back(); // 二级：返回＝回一级（走 popstate 统一处理）
  else closeSettingsUI();                // 一级：返回＝退出设置回对话区
};
document.querySelectorAll('.set-nav-item').forEach((b) => {
  b.onclick = () => {
    setSettingsPane(b.dataset.pane);
    if (isNarrow()) {
      setL2On(true);
      if (setHistDepth < 2) {
        setHistDepth++;
        history.pushState({ jxSettings: setHistDepth }, '');
      }
    }
  };
});
// 模型页：「添加提供方」= 插入一张展开的自定义卡片
$('prov-add-card').onclick = () => {
  provNew = { id: '', presetId: null, presetURL: '', name: '', baseURL: '', key: '', keyMasked: '', models: [] };
  provOpenKey = 'new';
  provModelOpen['new'] = -1;
  renderProvCards();
  const box = $('prov-list');
  if (box && box.lastElementChild) box.lastElementChild.scrollIntoView({ block: 'nearest' });
};
// 常规页：语言/字体/发送行为，改动即时生效并落本机
/* ---------- 自定义下拉：圆角按钮 + 浮层，设置页统一外观 ---------- */

const selRefs = {};

function closeAllSelects() {
  document.querySelectorAll('.jx-sel-pop.open').forEach((p) => p.classList.remove('open'));
}

// makeSelect(hostId, key, options, onChange)：在容器里建一套下拉，
// 返回 {set(v), get()}；onChange 在用户选中时触发。
function makeSelect(hostId, key, options, onChange) {
  const host = $(hostId);
  if (!host) return { set() {}, get() { return null; } };
  host.innerHTML = '';
  const wrap = document.createElement('div');
  wrap.className = 'jx-select';
  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = 'jx-sel-btn';
  const label = document.createElement('span');
  label.className = 'jx-sel-label';
  btn.appendChild(label);
  btn.insertAdjacentHTML('beforeend',
    '<svg class="jx-sel-chev" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 9l6 6 6-6"/></svg>');
  const pop = document.createElement('div');
  pop.className = 'jx-sel-pop';
  let value = options.length ? options[0].v : null;
  const render = () => {
    const cur = options.find((o) => o.v === value);
    label.textContent = cur ? cur.label : '请选择';
    pop.innerHTML = '';
    for (const o of options) {
      const it = document.createElement('button');
      it.type = 'button';
      it.className = 'jx-sel-opt' + (o.v === value ? ' cur' : '');
      it.innerHTML = '<span></span><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6L9 17l-5-5"/></svg>';
      it.firstChild.textContent = o.label;
      it.onclick = (e) => {
        e.stopPropagation();
        value = o.v;
        render();
        closeAllSelects();
        if (onChange) onChange(o.v);
      };
      pop.appendChild(it);
    }
  };
  btn.onclick = (e) => {
    e.stopPropagation();
    const wasOpen = pop.classList.contains('open');
    closeAllSelects();
    pop.classList.toggle('open', !wasOpen);
  };
  wrap.appendChild(btn);
  wrap.appendChild(pop);
  host.appendChild(wrap);
  render();
  const ref = { set(v) { value = v; render(); }, get() { return value; } };
  selRefs[key] = ref;
  return ref;
}

/* ---------- 主题色：白 / 黑 / 跟随系统（CSS 变量整体换装） ---------- */

const themeMedia = window.matchMedia ? window.matchMedia('(prefers-color-scheme: dark)') : null;

function applyTheme() {
  const pref = lsGet('jx.theme', 'light');
  let t = pref;
  if (pref === 'auto' && themeMedia) t = themeMedia.matches ? 'dark' : 'light';
  document.documentElement.dataset.theme = t;
}
if (themeMedia && themeMedia.addEventListener) {
  themeMedia.addEventListener('change', () => { if (lsGet('jx.theme', 'light') === 'auto') applyTheme(); });
}

// 常规页各下拉：值即时落本机并生效
makeSelect('sel-lang', 'lang', [
  { v: 'zh', label: '简体中文' },
  { v: 'en', label: 'English（翻译逐步补齐）' },
], (v) => lsSet('jx.lang', v));
makeSelect('sel-fontsize', 'fontsize', [
  { v: '12', label: '12 px' },
  { v: '13', label: '13 px' },
  { v: '14', label: '14 px（默认）' },
  { v: '15', label: '15 px' },
  { v: '16', label: '16 px' },
  { v: '17', label: '17 px' },
  { v: '18', label: '18 px' },
  { v: '20', label: '20 px' },
], (v) => applyChatFont(parseFloat(v) || 14));
// 主题色：白/黑/跟随系统的预览卡片，点选即时生效
function syncThemeCards() {
  const cur = lsGet('jx.theme', 'light');
  document.querySelectorAll('#theme-cards .theme-card').forEach((b) => {
    b.classList.toggle('cur', b.dataset.v === cur);
  });
}
document.querySelectorAll('#theme-cards .theme-card').forEach((b) => {
  b.onclick = () => { lsSet('jx.theme', b.dataset.v); applyTheme(); syncThemeCards(); };
});

// 点缀色：内置强调色板（原 accent-theme 组件并入，8 预设 + 自定义）
const ACCENTS = [
  ['默认蓝', '#4d6bfe', '#3d5bef'],
  ['紫', '#8250df', '#6f42c1'],
  ['绿', '#1f9d55', '#188a49'],
  ['橘', '#bc4c00', '#a34400'],
  ['红', '#c22f2f', '#ab2727'],
  ['青', '#0d7ea5', '#0b6d8d'],
  ['粉', '#bf3989', '#a63075'],
  ['金', '#9a6700', '#855900'],
];

let accentStyleEl = null;

function applyAccent(color, hover, save) {
  if (!accentStyleEl) {
    accentStyleEl = document.createElement('style');
    accentStyleEl.id = 'accent-style';
    document.head.appendChild(accentStyleEl);
  }
  accentStyleEl.textContent = ':root{--accent:' + color + ';--accent-hover:' + hover + ';}';
  if (save) lsSet('jx.accent', color + '|' + hover);
}

function buildAccentRow() {
  const row = $('accent-row');
  if (!row) return;
  const saved = lsGet('jx.accent', lsGet('cx:accent', '')); // 迁移旧组件的本地存储
  row.innerHTML = '';
  for (const [name, c, h] of ACCENTS) {
    const b = document.createElement('button');
    b.type = 'button';
    b.className = 'acc-swatch';
    b.style.background = c;
    b.title = name;
    if (saved.startsWith(c + '|')) b.classList.add('cur');
    b.onclick = () => {
      applyAccent(c, h, true);
      row.querySelectorAll('.acc-swatch').forEach((s) => s.classList.remove('cur'));
      b.classList.add('cur');
    };
    row.appendChild(b);
  }
  const custom = document.createElement('input');
  custom.type = 'color';
  custom.title = '自定义颜色';
  custom.oninput = () => {
    const c = custom.value;
    const d = c.replace(/^#(..)(..)(..)$/, (_, r, g, bl) => {
      const f = (x) => Math.max(0, parseInt(x, 16) - 18).toString(16).padStart(2, '0');
      return '#' + f(r) + f(g) + f(bl);
    });
    applyAccent(c, d, true);
    row.querySelectorAll('.acc-swatch').forEach((s) => s.classList.remove('cur'));
  };
  row.appendChild(custom);
  if (saved) {
    const [c, h] = saved.split('|');
    applyAccent(c, h || c, false);
  }
}
buildAccentRow();
makeSelect('sel-sendkey', 'sendkey', [
  { v: 'enter', label: 'Enter 发送，Shift+Enter 换行' },
  { v: 'ctrl', label: 'Ctrl+Enter 发送，Enter 换行' },
], (v) => lsSet('jx.sendkey', v));
makeSelect('sel-midrun', 'midrun', [
  { v: 'interject', label: '插话：不打断，下一步调用模型前注入' },
  { v: 'queue', label: '等待：本轮结束后排队逐条发送' },
], (v) => lsSet('jx.midrun', v));
makeSelect('sel-compact-mode', 'compactMode', [
  { v: 'rule', label: '规则压缩（零成本，默认）' },
  { v: 'llm', label: '模型摘要（达阈值自动+手动）' },
  { v: 'off', label: '关闭' },
], null);
makeSelect('sel-compact-threshold', 'compactThreshold', [
  { v: '0.8', label: '80%' },
  { v: '0.9', label: '90%' },
], null);

/* ---------- 技能管理（/api/skills：自带只读展示，用户可加/开关/删） ---------- */

async function renderSkillsPanel() {
  const bBox = $('skill-builtin-list');
  const uBox = $('skill-user-list');
  if (!bBox || !uBox) return;
  bBox.innerHTML = '加载中…';
  uBox.innerHTML = '';
  let data;
  try { data = await api('/api/skills'); } catch (e) { bBox.textContent = '技能清单读取失败：' + e.message; return; }
  bBox.innerHTML = '';

  for (const s of (data.builtin || [])) {
    const row = document.createElement('div');
    row.className = 'plugin-row';
    row.innerHTML =
      `<div class="pr-info"><div class="pr-line"><b>${esc(s.name)}</b>${s.version ? `<span class="pr-ver">v${esc(s.version)}</span>` : ''}</div>` +
      `<p>${esc(s.description || '')}</p></div>`;
    const tag = document.createElement('span');
    tag.className = 'skill-constant';
    tag.textContent = '常开';
    row.appendChild(tag);
    bBox.appendChild(row);
  }

  for (const s of (data.user || [])) {
    const row = document.createElement('div');
    row.className = 'plugin-row';
    row.innerHTML =
      `<div class="pr-info"><div class="pr-line"><b>${esc(s.name)}</b><span class="pr-ver">${esc(s.id)}</span></div>` +
      `<p>${esc(s.description || '')}</p></div>`;
    const tg = document.createElement('label');
    tg.className = 'pr-toggle';
    const cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.checked = !!s.enabled;
    cb.onchange = async () => {
      cb.disabled = true;
      const note = row.querySelector('.pr-note');
      try {
        await api('/api/skills/toggle', { method: 'POST', body: JSON.stringify({ id: s.id, enabled: cb.checked }) });
        if (note) note.textContent = cb.checked ? '已开启' : '已停用';
      } catch (e) {
        alert('切换失败：' + e.message);
        cb.checked = !cb.checked;
      }
      cb.disabled = false;
      setTimeout(() => { if (note) note.textContent = ''; }, 3000);
    };
    tg.appendChild(cb);
    tg.appendChild(document.createElement('span'));
    row.appendChild(tg);
    const del = document.createElement('button');
    del.className = 'skill-del';
    del.title = '删除该技能';
    del.innerHTML = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"/><path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/><path d="M10 11v6"/><path d="M14 11v6"/></svg>';
    del.onclick = async () => {
      if (!(await confirmCenter('删除技能「' + s.name + '」？技能目录将被一并移除。', false, true))) return;
      try {
        await api('/api/skills/delete', { method: 'POST', body: JSON.stringify({ id: s.id }) });
        renderSkillsPanel();
      } catch (e) { alert('删除失败：' + e.message); }
    };
    row.appendChild(del);
    const note = document.createElement('div');
    note.className = 'pr-note';
    row.appendChild(note);
    uBox.appendChild(row);
  }

  if (!(data.user || []).length) {
    uBox.innerHTML = '<div class="skill-empty">还没有用户技能。点上方「添加技能」，选择包含 SKILL.md 的文件夹或 ZIP 压缩包。</div>';
  }
}

// 添加技能：弹操作系统原生选择框（后端代选，拿真实路径），文件夹整目录复制、
// ZIP 解压安装；用户取消选择框时静默返回。
async function installSkillFromPicker(kind) {
  closePopovers();
  const btn = $('skill-add-btn');
  let picked;
  try {
    const url = (kind === 'zip' ? '/api/pick-file' : '/api/pick-folder') +
      '?title=' + encodeURIComponent(kind === 'zip' ? '选择技能 ZIP 压缩包' : '选择技能文件夹');
    picked = await api(url);
  } catch (e) { alert('无法打开选择框：' + e.message); return; }
  const p = (picked && picked.path) || '';
  if (!p) return; // 用户取消了选择框
  btn.disabled = true;
  btn.textContent = '安装中…';
  try {
    await api('/api/skills/add', { method: 'POST', body: JSON.stringify({ path: p }) });
    renderSkillsPanel();
  } catch (e) { alert('添加失败：' + e.message); }
  btn.disabled = false;
  btn.textContent = '＋ 添加技能';
}

$('skill-add-btn').onclick = () => openPopover('skill-add-pop', $('skill-add-btn'));
$('sa-folder').onclick = () => installSkillFromPicker('folder');
$('sa-zip').onclick = () => installSkillFromPicker('zip');

/* ---------- 子对话专家（/api/experts：内置只读展示，自定义可增删改开关） ---------- */

// 名称即调用参数：这里展示的每个名称（内置与自定义）都与 dispatch_sub 的
// expert 参数共用同一份后端事实来源（/api/experts 的 builtin/user），前端
// 不抄写不改写——保证「所见名称 = 可填参数」100% 一致。

let expertDraft = [];     // 编辑中的专家列表（改动先落草稿，点保存才提交）
let expertOrig = [];      // 打开面板时的原始快照（取消 = 回滚到它）
let expertOpenID = null;  // 展开编辑的卡片：专家 id / '__new' / null（手风琴式单开）

// 专家预设的模型格式：''（跟随主对话）| providerId/modelId | 纯 modelId。
// 显示名从已配置模型清单（state.models）解析，解析不到原样展示。
function expertModelLabel(m) {
  m = (m || '').trim();
  if (!m) return '';
  const slash = m.indexOf('/');
  const pid = slash > 0 ? m.slice(0, slash) : '';
  const mid = slash > 0 ? m.slice(slash + 1) : m;
  const list = state.models || [];
  const hit = list.find((x) => x.providerId === pid && x.modelId === mid) ||
    (pid === '' ? list.find((x) => x.modelId === mid) : null);
  return (hit && (hit.modelName || hit.modelId)) || m;
}

function expertMeta(e) {
  const parts = [e.inheritContext ? '继承上下文' : '不继承上下文'];
  parts.push(e.model ? '默认模型 ' + expertModelLabel(e.model) : '模型随主对话');
  return parts.join(' · ');
}

// 专家表单的模型选择面板：与对话区同源（/api/models），点选写回草稿。
// 选中后 renderExpertCards 重建卡片（expertOpenID 不变，卡片保持展开）。
async function openExpertModelPop(e, anchor) {
  if (!(state.models || []).length) await loadModels(); // 设置页早于首页加载时兜底
  const pop = $('expert-model-pop');
  const cur = (e.model || '').trim();
  let html = `<button class="pop-item model-item ${cur === '' ? 'sel' : ''}" data-key="">跟随主对话（默认）</button>`;
  let lastProv = '';
  for (const m of state.models || []) {
    if (m.providerId !== lastProv) {
      html += `<div class="pop-group">${esc(m.providerName)}</div>`;
      lastProv = m.providerId;
    }
    const k = m.providerId + '/' + m.modelId;
    html += `<button class="pop-item model-item ${k === cur ? 'sel' : ''}" data-key="${esc(k)}"><span>${esc(m.modelName || m.modelId)}</span></button>`;
  }
  if (!(state.models || []).length) html = '<div class="pop-group">还没有已配置的模型，先到「模型」页添加提供方</div>';
  pop.innerHTML = html;
  pop.querySelectorAll('.model-item').forEach((b) => {
    b.onclick = () => {
      e.model = b.dataset.key;
      closePopovers();
      renderExpertCards();
    };
  });
  openPopover('expert-model-pop', anchor);
}

async function renderExpertsPanel() {
  const bBox = $('expert-builtin-list');
  const uBox = $('expert-user-list');
  if (!bBox || !uBox) return;
  bBox.innerHTML = '';
  uBox.innerHTML = '加载中…';
  let data;
  try { data = await api('/api/experts'); } catch (e) { uBox.textContent = '专家清单读取失败：' + e.message; return; }
  // 内置：直接渲染后端清单（名称=可填参数，一个字都不改）
  for (const b of (data.builtin || [])) {
    const row = document.createElement('div');
    row.className = 'plugin-row';
    row.innerHTML =
      `<div class="pr-info"><div class="pr-line"><b>${esc(b.name)}</b></div>` +
      `<p>${esc(b.description || '')}</p></div>`;
    const tag = document.createElement('span');
    tag.className = 'skill-constant';
    tag.textContent = '常开';
    row.appendChild(tag);
    bBox.appendChild(row);
  }
  const fields = (e) => ({
    id: e.id || '', name: e.name || '', description: e.description || '',
    prompt: e.prompt || '', model: e.model || '',
    inheritContext: !!e.inheritContext, enabled: !!e.enabled,
  });
  expertDraft = (data.user || []).map(fields);
  expertOrig = (data.user || []).map(fields);
  expertOpenID = null;
  renderExpertCards();
}

function renderExpertCards() {
  const box = $('expert-user-list');
  if (!box) return;
  box.innerHTML = '';
  expertDraft.forEach((e) => box.appendChild(buildExpertCard(e)));
  if (!expertDraft.length) {
    box.innerHTML = '<div class="skill-empty">还没有自定义专家。点上方「添加专家」创建——比如一个带专属审查提示词、默认用别的模型的审查员。</div>';
  }
}

// 开关即时保存：upsert 同一条记录，只翻 enabled
async function toggleExpertEnabled(e, on) {
  try {
    await api('/api/experts', { method: 'POST', body: JSON.stringify({ ...e, enabled: on }) });
    e.enabled = on;
  } catch (err) {
    alert('切换失败：' + err.message);
  }
}

function buildExpertCard(e) {
  const isNew = e.id === '__new'; // 新建草稿的临时 id（保存时才换成后端 id）
  const open = expertOpenID === e.id;
  const card = document.createElement('div');
  card.className = 'prov-card' + (open ? ' open' : '') + (e.enabled ? '' : ' off');

  // 折叠头：名称+描述（与「内置组件」行同款排版）+ 元信息 + 开关 + 编辑；
  // 点名称区展开，开关不冒泡
  const head = document.createElement('div');
  head.className = 'pv-head';
  const info = document.createElement('div');
  info.className = 'pr-info ex-head-info';
  info.innerHTML =
    `<div class="pr-line"><b>${esc(e.name || '未命名专家')}</b></div>` +
    `<p>${esc(e.description || '（没有描述）')}</p>`;
  head.appendChild(info);
  const meta = document.createElement('span');
  meta.className = 'ex-meta';
  meta.textContent = expertMeta(e);
  head.appendChild(meta);
  if (!isNew) { // 新卡片保存前不给开关（还没落到后端）
    const tg = document.createElement('label');
    tg.className = 'pr-toggle';
    tg.title = e.enabled ? '停用后主对话看不到该专家' : '启用后主对话可按名称派发';
    const cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.checked = !!e.enabled;
    cb.onclick = (ev) => ev.stopPropagation();
    cb.onchange = async () => { await toggleExpertEnabled(e, cb.checked); renderExpertCards(); };
    tg.appendChild(cb);
    tg.appendChild(document.createElement('span'));
    tg.onclick = (ev) => ev.stopPropagation(); // 点开关只翻转，不触发展开/收起
    head.appendChild(tg);
  }
  const edit = document.createElement('span');
  edit.className = 'pv-edit';
  edit.textContent = open ? '收起' : '编辑';
  head.appendChild(edit);
  head.onclick = () => {
    if (open) {
      if (isNew) { expertDraft = expertDraft.filter((x) => x !== e); }
      else { // 收起 = 丢弃未保存的修改，回滚到打开面板时的快照
        const o = expertOrig.find((x) => x.id === e.id);
        if (o) Object.assign(e, o);
      }
      expertOpenID = null;
    } else {
      expertOpenID = e.id;
    }
    renderExpertCards();
  };
  card.appendChild(head);
  if (!open) return card;

  // 展开表单
  const form = document.createElement('div');
  form.className = 'pv-form';
  const mk = (labelText, inputEl, full) => {
    const cell = document.createElement('div');
    if (full) cell.className = 'full';
    const l = document.createElement('div');
    l.className = 'pf-label';
    l.textContent = labelText;
    cell.appendChild(l);
    cell.appendChild(inputEl);
    return cell;
  };
  const grid = document.createElement('div');
  grid.className = 'pf-two ex-form-grid';
  const inName = document.createElement('input');
  inName.className = 'pf-input';
  inName.placeholder = '专家名称（主对话派发时填它）';
  inName.value = e.name;
  inName.oninput = () => { e.name = inName.value; };
  grid.appendChild(mk('名称', inName));
  const btnModel = document.createElement('button');
  btnModel.type = 'button';
  btnModel.className = 'chip-btn ex-model-chip';
  btnModel.title = '选择该专家的默认模型';
  btnModel.innerHTML =
    `<span>${esc(expertModelLabel(e.model) || '跟随主对话（默认）')}</span>` +
    '<svg class="caret" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M6 9l6 6 6-6"/></svg>';
  btnModel.onclick = (ev) => { ev.stopPropagation(); openExpertModelPop(e, btnModel); };
  grid.appendChild(mk('默认模型', btnModel));
  form.appendChild(grid);

  const inDesc = document.createElement('input');
  inDesc.className = 'pf-input';
  inDesc.placeholder = '给主对话看的选用依据（会列进 dispatch_sub 的工具说明）';
  inDesc.value = e.description;
  inDesc.oninput = () => { e.description = inDesc.value; };
  form.appendChild(mk('描述', inDesc, true));

  const inPrompt = document.createElement('textarea');
  inPrompt.className = 'pf-input pf-textarea';
  inPrompt.placeholder = '追加的系统提示词：拼在该子对话模式基础提示词之后，定义这位专家的立场与工作方式';
  inPrompt.value = e.prompt;
  inPrompt.oninput = () => { e.prompt = inPrompt.value; };
  form.appendChild(mk('追加提示词', inPrompt, true));

  // 继承上下文开关
  const ctxRow = document.createElement('div');
  ctxRow.className = 'ex-inline-toggle';
  const ctxTg = document.createElement('label');
  ctxTg.className = 'pr-toggle';
  const ctxCb = document.createElement('input');
  ctxCb.type = 'checkbox';
  ctxCb.checked = !!e.inheritContext;
  ctxCb.onchange = () => { e.inheritContext = ctxCb.checked; };
  ctxTg.appendChild(ctxCb);
  ctxTg.appendChild(document.createElement('span'));
  ctxRow.appendChild(ctxTg);
  const ctxDesc = document.createElement('span');
  ctxDesc.className = 'sf-desc';
  ctxDesc.textContent = '继承对话上下文（开启后子对话完整继承主对话聊天记录，任务只需写「接下来做什么」；是否继承完全由专家预设决定）';
  ctxRow.appendChild(ctxDesc);
  form.appendChild(mk('上下文', ctxRow, true));

  // 操作区：删除 | 取消 | 保存
  const acts = document.createElement('div');
  acts.className = 'pf-actions';
  if (!isNew) {
    const del = document.createElement('button');
    del.className = 'pf-del';
    del.textContent = '删除专家';
    del.onclick = async () => {
      if (!(await confirmCenter('删除专家「' + (e.name || '未命名') + '」？已派发的子对话保留，只是不再追加该提示词。', false, true))) return;
      try {
        await api('/api/experts/delete', { method: 'POST', body: JSON.stringify({ id: e.id }) });
        renderExpertsPanel();
      } catch (err) { alert('删除失败：' + err.message); }
    };
    acts.appendChild(del);
  }
  const btnCancel = document.createElement('button');
  btnCancel.className = 'pf-cancel';
  btnCancel.textContent = '取消';
  btnCancel.onclick = () => {
    if (isNew) expertDraft = expertDraft.filter((x) => x !== e);
    else { // 取消 = 丢弃未保存的修改
      const o = expertOrig.find((x) => x.id === e.id);
      if (o) Object.assign(e, o);
    }
    expertOpenID = null;
    renderExpertCards();
  };
  const btnSave = document.createElement('button');
  btnSave.className = 'pf-save';
  btnSave.textContent = '保存';
  btnSave.onclick = async () => {
    if (!e.name.trim()) { alert('请填写名称（它是主对话派发时的调用参数）'); return; }
    if (!e.prompt.trim()) { alert('请填写追加提示词'); return; }
    try {
      await api('/api/experts', {
        method: 'POST',
        body: JSON.stringify({
          id: isNew ? '' : e.id, name: e.name, description: e.description,
          prompt: e.prompt, model: e.model,
          inheritContext: !!e.inheritContext, enabled: !!e.enabled,
        }),
      });
      renderExpertsPanel();
    } catch (err) { alert('保存失败：' + err.message); }
  };
  acts.appendChild(btnCancel);
  acts.appendChild(btnSave);
  form.appendChild(acts);

  card.appendChild(form);
  return card;
}

// 「添加专家」= 插入一张展开的新卡片（enabled 默认开；__new 是临时 id）
$('expert-add-btn').onclick = () => {
  if (expertOpenID === '__new') return;
  expertDraft.push({ id: '__new', name: '', description: '', prompt: '', model: '', inheritContext: false, enabled: true });
  expertOpenID = '__new';
  renderExpertCards();
  const box = $('expert-user-list');
  if (box && box.lastElementChild) {
    box.lastElementChild.scrollIntoView({ block: 'nearest' });
    const first = box.lastElementChild.querySelector('.pf-input');
    if (first) first.focus();
  }
};
