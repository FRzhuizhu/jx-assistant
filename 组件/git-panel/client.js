/* 组件：Git 面板 —— 浏览器半边。
 *
 * 右侧栏多一个「Git」页签（原生视图，注册进 app-right.js 的 rpViews，不用 iframe）：
 *   - 顶栏：仓库候选下拉 + 当前分支徽标（↑领先 ↓落后）+ 刷新；
 *   - 「更改」页：VSCode 源代码管理式四组文件清单（冲突/已暂存/未暂存/未跟踪，
 *     状态字母配色），逐文件与整组暂存/取消暂存，提交信息输入框 + AI 摘要 + 提交，
 *     点文件看逐行 diff（大 diff 先渲染 400 行可展开）；
 *   - 「图谱」页：分支泳道图（git log --all --topo-order 的父母哈希在前端算泳道，
 *     SVG 贝塞尔曲线画合并分叉，引用徽标标注分支/远程/标签，HEAD 高亮），
 *     可展开本地/远程分支清单，点提交看完整信息、文件清单与分文件 diff，可加载更早；
 *   - 跟随当前会话的工作区（JX 'session:open' 换仓库，'message:done' 自动刷新——
 *     AI 在对话里做的提交/合并，图谱随后就变）；
 *   - 面板打开 / 切仓库 / 页面重新可见时刷新一次，另有 12 秒轻量轮询（仅 status）。
 *
 * 安全与边界（与宿主半边 gitpanel.go 对应）：仓库只能选宿主扫描出的候选；
 * 写操作只有暂存 / 取消暂存 / 提交（AI 摘要只生成不提交）；不做 push/pull/
 * reset/checkout/丢弃。渲染全部走文本节点与 createTextNode，不用 innerHTML 拼
 * 仓库内容（防提交信息/路径里的 HTML）。dispose 撤掉视图、定时器与事件监听。
 */
(function () {
  if (!window.CX) return;

  const ID = 'git-panel';
  const SVGNS = 'http://www.w3.org/2000/svg';
  const POLL_MS = 12000;
  const DIFF_FIRST_LINES = 400;
  // 泳道配色（与对话区亮色主题协调；下标 = 泳道色序号轮转）
  const PAL = ['#4d6bfe', '#e08a1e', '#1f9d55', '#b05bd6', '#0ea5b7', '#d6455d', '#8a7a2a', '#6b7684'];
  const LANE_W = 13, ROW_H = 46, DOT_R = 4;
  const LETTER_CLS = { M: 'm', A: 'a', D: 'd', U: 'd', R: 'r', C: 'c', T: 'm', '?': 'a' };

  /* ---------- 本地小工具 ---------- */

  const $id = (id) => document.getElementById(id);
  const esc = (s) => String(s == null ? '' : s).replace(/[&<>"']/g, (c) => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
  ));
  const el = (tag, cls, text) => {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  };
  function gitGet(path, params) {
    const q = new URLSearchParams({ session: S.session, repo: S.repo, ...(params || {}) });
    return fetch('/api/git/' + path + '?' + q).then(async (r) => {
      const d = await r.json().catch(() => ({}));
      if (!r.ok) throw new Error(d.error || r.statusText);
      return d;
    });
  }
  function gitPost(path, extra) {
    return fetch('/api/git/' + path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ session: S.session, repo: S.repo, ...(extra || {}) }),
    }).then(async (r) => {
      const d = await r.json().catch(() => ({}));
      if (!r.ok) throw new Error(d.error || r.statusText);
      return d;
    });
  }
  function relTime(s) {
    if (!s) return '';
    const t = new Date(String(s).replace(' ', 'T'));
    const diff = (Date.now() - t.getTime()) / 1000;
    if (isNaN(diff) || diff < 0) return '';
    if (diff < 60) return '刚刚';
    if (diff < 3600) return Math.floor(diff / 60) + '分钟前';
    if (diff < 86400) return Math.floor(diff / 3600) + '小时前';
    if (diff < 86400 * 30) return Math.floor(diff / 86400) + '天前';
    return String(s).slice(0, 10);
  }

  /* ---------- 状态 ---------- */

  const S = {
    session: null,        // 跟随的会话 id（null=首页）
    repo: lsGet('jx.gitp.repo', ''),
    repos: [], branch: '', oid: '', upstream: '', ahead: 0, behind: 0,
    groups: { staged: [], unstaged: [], untracked: [], conflicted: [] },
    reason: '', error: '', note: '',
    tab: 'changes',       // changes | graph
    detail: null,         // {type:'file', path, staged} | {type:'commit', hash}
    diff: null, diffShowAll: false,
    commit: null,         // 点开的提交详情（图谱页）
    commits: [], commitsMore: false, commitsLoading: false,
    branches: null, branchesOpen: false,
    timer: null, inFlight: 0,
  };

  /* ---------- 泳道计算：单遍状态机（思路对齐 VSCode 内置源代码管理图谱） ----------
   * commits 须是 --topo-order（父在子后）。lanes[i] = 第 i 条泳道当前等待的 hash；
   * 每行输出：node（落点列与颜色）与 edges（from=行顶列号 → to=行底列号）。
   * 颜色跟着"线条"走：新分叉的线取调色板下一个颜色，合并线取来线的颜色。 */
  function computeLanes(commits) {
    const MAX_LANES = 24;
    const lanes = [], colors = [];
    let colorSeq = 0;
    const rows = [];
    for (const c of commits) {
      const before = lanes.slice();
      const edges = [];
      const push = (from, to, color) => {
        const key = from + '-' + to + '-' + color;
        if (!edges.some((e) => e._key === key)) edges.push({ from, to, color, _key: key });
      };
      // 收口：所有指向本提交的泳道折叠到最左一条，其余画汇入弧线后消失
      let nodeCol = -1;
      for (let i = 0; i < lanes.length; i++) {
        if (lanes[i] === c.hash) {
          if (nodeCol === -1) nodeCol = i;
          else { push(i, nodeCol, colors[i]); lanes[i] = null; }
        }
      }
      if (nodeCol === -1) { // 新出现的根或分页边界：找空位或新开一条
        const free = lanes.indexOf(null);
        if (free >= 0) { nodeCol = free; colors[free] = colorSeq++ % PAL.length; }
        else if (lanes.length < MAX_LANES) { nodeCol = lanes.length; lanes.push(null); colors.push(colorSeq++ % PAL.length); }
        else nodeCol = lanes.length - 1; // 病态仓库兜底：挤在最后一列
      }
      const nodeColor = colors[nodeCol] != null ? colors[nodeCol] : 0;
      const parents = c.parents || [];
      parents.forEach((p, pi) => {
        const existing = lanes.indexOf(p);
        if (pi === 0) {
          if (existing === -1) lanes[nodeCol] = p; // 第一父继承本泳道
          else if (existing !== nodeCol) { push(nodeCol, existing, nodeColor); lanes[nodeCol] = null; }
        } else if (existing >= 0) {
          push(nodeCol, existing, nodeColor); // 合并进已有线：本线画弧汇入
        } else if (lanes.length < MAX_LANES || lanes.indexOf(null) >= 0) {
          const free = lanes.indexOf(null);
          if (free >= 0) { lanes[free] = p; colors[free] = colorSeq++ % PAL.length; push(nodeCol, free, colors[free]); }
          else { lanes.push(p); colors.push(colorSeq++ % PAL.length); push(nodeCol, lanes.length - 1, colors[lanes.length - 1]); }
        }
      });
      if (parents.length === 0) lanes[nodeCol] = null; // 根提交：线到此为止
      // 直行段：其余存活泳道上下贯通（分叉新线在本行已画过，去重时会合并不加）
      for (let i = 0; i < lanes.length; i++) {
        if (!lanes[i]) continue;
        const j = before.indexOf(lanes[i]);
        push(j >= 0 ? j : nodeCol, i, colors[i]);
      }
      rows.push({ node: { col: nodeCol, color: nodeColor }, edges, width: Math.max(before.length, lanes.length) });
    }
    return rows;
  }

  /* ---------- 挂载点 ---------- */

  let root = null; // #rp-view-git 容器

  function buildDom() {
    root = el('div', 'rp-view gitp');
    root.id = 'rp-view-git';

    /* 顶栏：仓库下拉 + 分支徽标 + 刷新 */
    const top = el('div', 'gitp-top');
    const repoSel = el('select', 'gitp-repo');
    repoSel.id = 'gitp-repo';
    repoSel.title = '切换仓库（候选由服务端扫描）';
    repoSel.onchange = () => {
      S.repo = repoSel.value;
      lsSet('jx.gitp.repo', S.repo);
      S.commits = []; S.branches = null; S.detail = null; S.commit = null;
      refreshAll();
    };
    const refresh = el('button', 'gitp-icon-btn', '⟳');
    refresh.title = '刷新';
    refresh.onclick = refreshAll;
    top.appendChild(repoSel);
    const branchBadge = el('span', 'gitp-branch');
    branchBadge.id = 'gitp-branch-badge';
    top.appendChild(branchBadge);
    top.appendChild(refresh);
    root.appendChild(top);

    /* 状态条：错误 / 提示 */
    const strip = el('div', 'gitp-strip hidden');
    strip.id = 'gitp-strip';
    root.appendChild(strip);

    /* 内页签 */
    const tabs = el('div', 'gitp-tabs');
    for (const [key, name] of [['changes', '更改'], ['graph', '图谱']]) {
      const b = el('button', 'gitp-tab', name);
      b.dataset.tab = key;
      b.onclick = () => {
        S.tab = key;
        S.detail = null; S.commit = null;
        renderTabs();
        render();
        if (key === 'graph' && !S.commits.length) loadGraph(true);
        if (key === 'graph') loadBranches();
      };
      tabs.appendChild(b);
    }
    root.appendChild(tabs);

    /* 更改页 */
    const changes = el('div', 'gitp-page');
    changes.id = 'gitp-changes';
    root.appendChild(changes);

    /* 图谱页 */
    const graph = el('div', 'gitp-page hidden');
    graph.id = 'gitp-graph';
    root.appendChild(graph);

    return root;
  }

  function renderTabs() {
    root.querySelectorAll('.gitp-tab').forEach((b) => b.classList.toggle('active', b.dataset.tab === S.tab));
    $id('gitp-changes').classList.toggle('hidden', S.tab !== 'changes');
    $id('gitp-graph').classList.toggle('hidden', S.tab !== 'graph');
  }

  /* 状态条：error（红，持续到下次成功）> note（绿，短暂）> reason（黄，没仓库） */
  function renderStrip() {
    const strip = $id('gitp-strip');
    strip.innerHTML = '';
    strip.classList.add('hidden');
    const show = (cls, text) => {
      strip.classList.remove('hidden');
      strip.classList.add(cls);
      strip.textContent = text;
    };
    if (S.error) show('err', S.error);
    else if (S.note) { show('note', S.note); }
    else if (S.reason) show('warn', S.reason);
    if (S.note) {
      setTimeout(() => { S.note = ''; renderStrip(); }, 4000);
    }
  }

  /* ---------- 渲染：顶栏 ---------- */

  function renderTop() {
    const sel = $id('gitp-repo');
    sel.innerHTML = '';
    for (const r of S.repos) {
      const o = el('option', null, r.name);
      o.value = r.path;
      o.title = r.path;
      sel.appendChild(o);
    }
    sel.value = S.repo;
    if (!S.repo && S.repos.length) sel.value = S.repos[0].path;

    const badge = $id('gitp-branch-badge');
    badge.innerHTML = '';
    if (!S.repo) return;
    const name = S.branch || '(游离 HEAD)';
    const b = el('span', 'gitp-branch-name', name);
    b.title = S.upstream ? S.branch + ' → ' + S.upstream : name;
    badge.appendChild(b);
    if (S.ahead > 0 || S.behind > 0) {
      badge.appendChild(el('span', 'gitp-ab', (S.ahead ? '↑' + S.ahead : '') + (S.behind ? ' ↓' + S.behind : '')));
    }
  }

  /* ---------- 渲染：更改页 ---------- */

  function renderChanges() {
    const box = $id('gitp-changes');
    box.innerHTML = '';
    if (S.detail) { box.appendChild(renderFileDetail()); return; }

    /* 提交区（VSCode SCM 式：消息框在清单上方） */
    const cm = el('div', 'gitp-commitbar');
    const ta = el('textarea', 'gitp-commit-msg');
    ta.id = 'gitp-commit-msg';
    ta.rows = 2;
    ta.placeholder = '提交信息（已暂存改动会一起提交）';
    const btns = el('div', 'gitp-commit-btns');
    const ai = el('button', 'gitp-btn', 'AI 摘要');
    ai.title = '用当前会话配置的模型为已暂存改动生成提交信息（只生成，不提交）';
    ai.onclick = async () => {
      ai.disabled = true; ai.textContent = '生成中…';
      try {
        const d = await gitPost('summarize');
        ta.value = d.message;
        S.error = '';
      } catch (e) { S.error = e.message; renderStrip(); }
      ai.disabled = false; ai.textContent = 'AI 摘要';
    };
    const ok = el('button', 'gitp-btn primary', '✓ 提交');
    ok.onclick = async () => {
      if (!ta.value.trim()) { S.error = '提交信息为空'; renderStrip(); ta.focus(); return; }
      ok.disabled = true;
      try {
        const d = await gitPost('commit', { message: ta.value });
        ta.value = '';
        S.error = '';
        S.note = '已提交：' + (d.summary || '完成');
        refreshAll();
      } catch (e) { S.error = e.message; renderStrip(); }
      ok.disabled = false;
    };
    btns.appendChild(ai);
    btns.appendChild(ok);
    cm.appendChild(ta);
    cm.appendChild(btns);
    if (!S.groups.staged.length) ok.disabled = true;
    box.appendChild(cm);

    const body = el('div', 'gitp-groups');
    const mkGroup = (title, list, kind) => {
      if (!list.length) return;
      const g = el('div', 'gitp-group');
      const head = el('div', 'gitp-group-head');
      head.appendChild(el('span', 'gitp-group-title', title + '（' + list.length + '）'));
      const all = el('button', 'gitp-mini-btn', kind === 'stage' ? '全部暂存 +' : '全部取消暂存 −');
      all.onclick = () => act(kind, list.map((f) => f.path));
      head.appendChild(all);
      g.appendChild(head);
      for (const f of list) g.appendChild(fileRow(f, kind));
      body.appendChild(g);
    };
    mkGroup('冲突', S.groups.conflicted, 'stage');
    mkGroup('已暂存', S.groups.staged, 'unstage');
    mkGroup('未暂存的更改', S.groups.unstaged, 'stage');
    mkGroup('未跟踪', S.groups.untracked, 'stage');
    if (!body.children.length) {
      const clean = el('div', 'gitp-clean', '工作区干净，没有待提交的改动');
      body.appendChild(clean);
    }
    box.appendChild(body);
  }

  function fileRow(f, kind) {
    const row = el('div', 'gitp-file');
    const which = f.index !== '.' && f.index !== ' ' ? f.index : f.worktree;
    const letter = el('span', 'gitp-letter ' + (LETTER_CLS[which] || 'm'), f.mark || which);
    row.appendChild(letter);
    const name = el('span', 'gitp-name', f.path);
    name.title = f.original ? f.original + ' → ' + f.path : f.path;
    row.appendChild(name);
    if (f.original) row.appendChild(el('span', 'gitp-dir', '← ' + f.original));
    const actBtn = el('button', 'gitp-mini-btn row-act', kind === 'stage' ? '+' : '−');
    actBtn.title = kind === 'stage' ? '暂存' : '取消暂存';
    actBtn.onclick = (e) => { e.stopPropagation(); act(kind, [f.path]); };
    row.appendChild(actBtn);
    row.onclick = () => openFileDiff(f.path, kind === 'unstage');
    return row;
  }

  async function act(kind, paths) {
    try {
      await gitPost(kind, { paths });
      S.error = '';
      await loadStatus();
      if (S.commits.length || S.tab === 'graph') await loadGraph(true);
    } catch (e) { S.error = e.message; renderStrip(); }
  }

  /* ---------- 渲染：文件 diff 详情 ---------- */

  async function openFileDiff(path, staged) {
    S.detail = { type: 'file', path, staged };
    S.diff = null; S.diffShowAll = false;
    renderChanges();
    try {
      S.diff = await gitGet('diff', { path, staged: staged ? '1' : '0' });
    } catch (e) { S.error = e.message; renderStrip(); }
    renderChanges();
  }

  function renderFileDetail() {
    const d = S.detail;
    const box = el('div', 'gitp-detail');
    const head = el('div', 'gitp-detail-head');
    const back = el('button', 'gitp-mini-btn', '← 返回');
    back.onclick = () => { S.detail = null; S.commit = null; render(); };
    head.appendChild(back);
    if (d.type === 'file') {
      head.appendChild(el('span', 'gitp-detail-title', d.path));
      head.appendChild(el('span', 'gitp-dir', d.staged ? '已暂存的改动' : '工作区的改动'));
    } else {
      head.appendChild(el('span', 'gitp-detail-title', d.subject));
      head.appendChild(el('span', 'gitp-dir', d.short + ' · ' + d.author + ' · ' + relTime(d.date)));
    }
    box.appendChild(head);
    const body = el('div', 'gitp-detail-body');
    if (!S.diff) {
      body.appendChild(el('div', 'gitp-clean', '加载中…'));
    } else if (S.diff.empty) {
      body.appendChild(el('div', 'gitp-clean', '没有可展示的内容'));
    } else if (S.diff.binary) {
      body.appendChild(el('div', 'gitp-clean', '二进制文件，不展示 diff'));
    } else {
      body.appendChild(diffView(S.diff.text, S.diff.truncated));
    }
    box.appendChild(body);
    return box;
  }

  /* unified diff → 逐行配色（+ 绿 / − 红 / @@ 紫 / 头部暗）。大 diff 先渲染
     DIFF_FIRST_LINES 行，按钮展开其余。 */
  function diffView(text, truncated) {
    const wrap = el('div', 'gitp-diff');
    const all = text.split('\n');
    if (all.length && all[all.length - 1] === '') all.pop();
    const limit = S.diffShowAll ? all.length : Math.min(all.length, DIFF_FIRST_LINES);
    for (let i = 0; i < limit; i++) {
      const line = all[i];
      const row = el('div', 'gitp-dl');
      if (/^diff --git /.test(line) || /^\+\+\+ |^--- /.test(line)) row.classList.add('meta');
      else if (/^@@ /.test(line)) row.classList.add('hunk');
      else if (line.startsWith('+')) row.classList.add('add');
      else if (line.startsWith('-')) row.classList.add('del');
      row.textContent = line || ' ';
      wrap.appendChild(row);
    }
    if (limit < all.length || truncated) {
      const more = el('button', 'gitp-mini-btn gitp-diff-more');
      more.textContent = truncated
        ? 'diff 过大已截断，已展示前 ' + limit + ' 行'
        : '还有 ' + (all.length - limit) + ' 行，点击全部展开';
      more.onclick = () => { S.diffShowAll = true; render(); };
      wrap.appendChild(more);
    }
    return wrap;
  }

  /* ---------- 渲染：图谱页 ---------- */

  function renderGraph() {
    const box = $id('gitp-graph');
    box.innerHTML = '';
    if (S.commit) { box.appendChild(commitDetail()); return; }

    /* 分支条：当前分支 + 可展开清单 */
    const bl = el('div', 'gitp-branchline');
    const cur = el('button', 'gitp-branchline-head');
    cur.appendChild(el('span', 'gitp-branch-dot'));
    cur.appendChild(el('span', null, S.branch || '(游离 HEAD)'));
    if (S.upstream) cur.appendChild(el('span', 'gitp-dir', '→ ' + S.upstream));
    const bn = S.branches ? S.branches.locals.length + '/' + S.branches.remotes.length : '';
    cur.appendChild(el('span', 'gitp-dir', bn ? '　' + S.branches.locals.length + ' 本地 · ' + S.branches.remotes.length + ' 远程' : ''));
    cur.appendChild(el('span', 'gitp-caret', S.branchesOpen ? '▾' : '▸'));
    cur.onclick = () => { S.branchesOpen = !S.branchesOpen; renderGraph(); };
    bl.appendChild(cur);
    if (S.branchesOpen && S.branches) {
      const list = el('div', 'gitp-branchlist');
      const row = (b, cls) => {
        const r = el('div', 'gitp-branch-row ' + (cls || ''));
        r.appendChild(el('span', 'gitp-name', b.name));
        if (b.current) r.appendChild(el('span', 'gitp-chip cur', '当前'));
        if (b.upstream) {
          const ab = (b.ahead ? ' ↑' + b.ahead : '') + (b.behind ? ' ↓' + b.behind : '');
          r.appendChild(el('span', 'gitp-dir', (b.gone ? '上游已删除' : '→ ' + b.upstream + ab)));
        }
        r.appendChild(el('span', 'gitp-dir right', b.subject));
        r.title = b.short + ' ' + b.subject;
        return r;
      };
      for (const b of S.branches.locals) list.appendChild(row(b, b.current ? 'cur' : ''));
      if (S.branches.remotes.length) {
        list.appendChild(el('div', 'gitp-group-title', '远程'));
        for (const b of S.branches.remotes) list.appendChild(row(b, 'remote'));
      }
      bl.appendChild(list);
    }
    box.appendChild(bl);

    /* 泳道图 */
    const scroll = el('div', 'gitp-graph-scroll');
    if (!S.commits.length) {
      scroll.appendChild(el('div', 'gitp-clean', S.commitsLoading ? '加载中…' : '这个仓库还没有提交'));
      box.appendChild(scroll);
      return;
    }
    const lanes = computeLanes(S.commits);
    let maxW = 1;
    for (const r of lanes) maxW = Math.max(maxW, r.width);
    const svgW = maxW * LANE_W + 8;
    const x = (col) => col * LANE_W + LANE_W / 2 + 4;

    S.commits.forEach((c, i) => {
      const row = el('div', 'gitp-crow');
      row.dataset.hash = c.hash;
      const svg = document.createElementNS(SVGNS, 'svg');
      svg.setAttribute('width', svgW);
      svg.setAttribute('height', ROW_H);
      svg.setAttribute('viewBox', '0 0 ' + svgW + ' ' + ROW_H);
      // 主界面样式表有全局 svg { width:16px; height:16px }，行内样式把它压回去
      svg.style.width = svgW + 'px';
      svg.style.height = ROW_H + 'px';
      svg.classList.add('gitp-lane');
      const lr = lanes[i];
      for (const e of lr.edges) {
        const p = document.createElementNS(SVGNS, 'path');
        const xf = x(e.from), xt = x(e.to);
        p.setAttribute('d', xf === xt
          ? 'M ' + xf + ' 0 L ' + xt + ' ' + ROW_H
          : 'M ' + xf + ' 0 C ' + xf + ' ' + (ROW_H * 0.45) + ', ' + xt + ' ' + (ROW_H * 0.55) + ', ' + xt + ' ' + ROW_H);
        p.setAttribute('stroke', PAL[e.color % PAL.length]);
        p.setAttribute('stroke-width', '2');
        p.setAttribute('fill', 'none');
        svg.appendChild(p);
      }
      const dot = document.createElementNS(SVGNS, 'circle');
      dot.setAttribute('cx', x(lr.node.col));
      dot.setAttribute('cy', ROW_H / 2);
      dot.setAttribute('r', DOT_R);
      dot.setAttribute('fill', PAL[lr.node.color % PAL.length]);
      svg.appendChild(dot);
      if (c.hash === S.oid) { // HEAD 所在提交：外圈高亮
        const ring = document.createElementNS(SVGNS, 'circle');
        ring.setAttribute('cx', x(lr.node.col));
        ring.setAttribute('cy', ROW_H / 2);
        ring.setAttribute('r', DOT_R + 3);
        ring.setAttribute('fill', 'none');
        ring.setAttribute('stroke', PAL[lr.node.color % PAL.length]);
        ring.setAttribute('stroke-width', '1.5');
        svg.appendChild(ring);
      }
      row.appendChild(svg);

      const info = el('div', 'gitp-cinfo');
      const l1 = el('div', 'gitp-cl1');
      for (const ref of c.refs || []) {
        l1.appendChild(el('span', 'gitp-chip ' + ref.kind + (ref.current ? ' cur' : ''), ref.name));
      }
      l1.appendChild(el('span', 'gitp-csubject', c.subject));
      info.appendChild(l1);
      const l2 = el('div', 'gitp-cl2', c.author + ' · ' + c.hash.slice(0, 8) + ' · ' + relTime(c.date));
      info.appendChild(l2);
      row.appendChild(info);
      row.onclick = () => openCommit(c);
      scroll.appendChild(row);
    });

    if (S.commitsMore) {
      const more = el('button', 'gitp-mini-btn gitp-loadmore', S.commitsLoading ? '加载中…' : '加载更早的提交');
      more.onclick = () => loadGraph(false);
      scroll.appendChild(more);
    }
    box.appendChild(scroll);
  }

  async function openCommit(c) {
    S.commit = { ...c, files: null, message: '' };
    S.diff = null; S.diffShowAll = false;
    renderGraph();
    try {
      const d = await gitGet('commit', { hash: c.hash });
      S.commit.message = d.message || '';
      S.commit.files = d.files || [];
      S.diff = d.diff || { text: '', empty: true };
    } catch (e) { S.error = e.message; renderStrip(); }
    renderGraph();
  }

  /* 提交详情：完整信息 + 文件清单（点文件滚动到 diff 里对应段落）+ 分文件 diff */
  function commitDetail() {
    const c = S.commit;
    const box = el('div', 'gitp-detail');
    const head = el('div', 'gitp-detail-head');
    const back = el('button', 'gitp-mini-btn', '← 返回图谱');
    back.onclick = () => { S.commit = null; S.diff = null; renderGraph(); };
    head.appendChild(back);
    head.appendChild(el('span', 'gitp-detail-title', c.subject));
    box.appendChild(head);

    const meta = el('div', 'gitp-commit-meta');
    meta.appendChild(el('div', 'gitp-cl2', c.author + ' · ' + c.hash.slice(0, 8) + ' · ' + relTime(c.date)));
    box.appendChild(meta);

    const body = el('div', 'gitp-detail-body');
    if (!S.diff) {
      body.appendChild(el('div', 'gitp-clean', '加载中…'));
      box.appendChild(body);
      return box;
    }
    // 完整提交信息（主题之外可能还有正文；宿主已按 %s%n%n%b 组装）
    const msg = (S.commit.message || '').trim();
    if (msg) body.appendChild(el('div', 'gitp-commit-msg', msg));

    // 文件清单：点文件滚动/定位到 diff 里对应段落
    if (Array.isArray(S.commit.files) && S.commit.files.length) {
      const filesBox = el('div', 'gitp-cfiles');
      for (const f of S.commit.files) {
        const r = el('div', 'gitp-file');
        r.appendChild(el('span', 'gitp-letter ' + (LETTER_CLS[f.index] || 'm'), f.index));
        r.appendChild(el('span', 'gitp-name', f.path));
        filesBox.appendChild(r);
        r.onclick = () => {
          const sec = body.querySelector('.gitp-psec[data-file="' + CSS.escape(f.path) + '"]');
          if (sec) {
            sec.scrollIntoView({ block: 'start' });
            sec.classList.add('flash');
            setTimeout(() => sec.classList.remove('flash'), 900);
          }
        };
      }
      body.appendChild(filesBox);
    }

    if (S.diff.binary) {
      body.appendChild(el('div', 'gitp-clean', '包含二进制改动，不展示 diff'));
      box.appendChild(body);
      return box;
    }

    /* 按 diff --git 分段：每段一个可折叠文件区块 */
    const text = S.diff.text || '';
    if (!text.trim()) {
      body.appendChild(el('div', 'gitp-clean', '此提交没有可展示的 diff'));
      box.appendChild(body);
      return box;
    }
    const parts = text.split(/\n(?=diff --git )/);
    parts.forEach((part, idx) => {
      const lines = part.split('\n');
      let title = '改动';
      const m = /^diff --git a\/(.+) b\/(.+)$/.exec(lines[0] || '');
      if (m) title = m[2];
      else if (idx === 0 && !m && lines[0] && !/^diff --git /.test(lines[0])) {
        // 段首可能有非 diff 的说明行，并进第一段
      }
      const sec = el('div', 'gitp-psec');
      const secHead = el('div', 'gitp-psec-head');
      secHead.appendChild(el('span', 'gitp-name', title));
      secHead.appendChild(el('span', 'gitp-caret', '▾'));
      const secBody = el('div', 'gitp-psec-body');
      for (const line of lines) {
        const row = el('div', 'gitp-dl');
        if (/^diff --git /.test(line) || /^\+\+\+ |^--- /.test(line)) row.classList.add('meta');
        else if (/^@@ /.test(line)) row.classList.add('hunk');
        else if (line.startsWith('+')) row.classList.add('add');
        else if (line.startsWith('-')) row.classList.add('del');
        row.textContent = line || ' ';
        secBody.appendChild(row);
      }
      secHead.onclick = () => {
        secBody.classList.toggle('hidden');
        secHead.querySelector('.gitp-caret').textContent = secBody.classList.contains('hidden') ? '▸' : '▾';
      };
      sec.dataset.file = title;
      sec.appendChild(secHead);
      sec.appendChild(secBody);
      body.appendChild(sec);
    });
    if (S.diff.truncated) body.appendChild(el('div', 'gitp-clean', 'diff 过大，超出部分未展示'));
    box.appendChild(body);
    return box;
  }

  /* ---------- 数据加载 ---------- */

  function syncSession() {
    const cur = (typeof state !== 'undefined' && state.cur && state.cur.id) || '';
    if (cur !== S.session) {
      S.session = cur;
      S.repo = ''; // 会话换了，工作区多半也换了：回默认仓库
      S.commits = []; S.branches = null; S.detail = null; S.commit = null;
      lsSet('jx.gitp.repo', '');
    }
  }

  async function loadStatus() {
    S.inFlight++;
    try {
      const d = await gitGet('status');
      S.repos = d.repos || [];
      S.repo = d.repo || '';
      S.branch = d.branch || '';
      S.oid = d.oid || '';
      S.upstream = d.upstream || '';
      S.ahead = d.ahead || 0;
      S.behind = d.behind || 0;
      S.groups = { staged: d.staged || [], unstaged: d.unstaged || [], untracked: d.untracked || [], conflicted: d.conflicted || [] };
      S.reason = d.reason || '';
      S.error = '';
      if (S.tab === 'changes' && S.detail) {
        // 文件在刷新后可能已消失（被提交/撤销）：退回清单
        const stillThere = S.groups.staged.concat(S.groups.unstaged, S.groups.untracked, S.groups.conflicted)
          .some((f) => f.path === S.detail.path);
        if (!stillThere) S.detail = null;
      }
      renderTop();
      renderChanges();
      renderStrip();
    } catch (e) {
      S.error = e.message;
      renderStrip();
    }
    S.inFlight--;
  }

  async function loadGraph(reset) {
    if (S.commitsLoading) return;
    S.commitsLoading = true;
    try {
      const d = await gitGet('graph', reset ? {} : { skip: String(S.commits.length), limit: '60' });
      if (reset) S.commits = d.commits || [];
      else {
        const seen = new Set(S.commits.map((c) => c.hash));
        for (const c of d.commits || []) if (!seen.has(c.hash)) S.commits.push(c);
      }
      S.commitsMore = !!d.more;
      renderGraph();
    } catch (e) {
      S.error = e.message;
      renderStrip();
    }
    S.commitsLoading = false;
  }

  async function loadBranches() {
    try {
      S.branches = await gitGet('branches');
      renderGraph();
    } catch (e) { /* 分支清单失败不拦主流程 */ }
  }

  function render() {
    renderTabs();
    if (S.tab === 'changes') renderChanges();
    else renderGraph();
    renderStrip();
  }

  function refreshAll() {
    syncSession();
    loadStatus();
    if (S.tab === 'graph') { loadGraph(true); loadBranches(); }
  }

  /* ---------- 生命周期 ---------- */

  let onSessionOpen = null, onMessageDone = null, onVisible = null;

  CX.register({
    id: ID,
    init() {
      if (!root) root = buildDom();
      const body = $id('rp-body');
      if (body && !root.parentNode) body.appendChild(root);
      rpRegister('git', {
        title: 'Git',
        onOpen() { syncSession(); renderTabs(); refreshAll(); },
      });
      if (typeof rpRenderTabs === 'function') rpRenderTabs();
      syncSession();
      renderTop();

      onSessionOpen = () => { syncSession(); if (rpActive === 'git') refreshAll(); };
      onMessageDone = () => { if (rpActive === 'git') { loadStatus(); if (S.tab === 'graph') loadGraph(true); } };
      onVisible = () => { if (rpActive === 'git' && !document.hidden) loadStatus(); };
      JX.on('session:open', onSessionOpen);
      JX.on('message:done', onMessageDone);
      document.addEventListener('visibilitychange', onVisible);
      S.timer = setInterval(() => {
        if (rpActive === 'git' && !document.hidden) loadStatus();
      }, POLL_MS);
    },
    dispose() {
      if (S.timer) { clearInterval(S.timer); S.timer = null; }
      JX._bus['session:open'] = (JX._bus['session:open'] || []).filter((f) => f !== onSessionOpen);
      JX._bus['message:done'] = (JX._bus['message:done'] || []).filter((f) => f !== onMessageDone);
      document.removeEventListener('visibilitychange', onVisible);
      delete rpViews.git;
      if (typeof rpRenderTabs === 'function') rpRenderTabs();
      if (root && root.parentNode) root.parentNode.removeChild(root);
      root = null;
      if (rpActive === 'git') {
        rpActive = null;
        const first = Object.keys(rpViews)[0];
        if (first) rpOpen(first);
        else rpClose();
      }
    },
  });
})();
