/* 组件：Git 面板 —— 浏览器半边。
 *
 * 右侧栏多一个「Git」页签（原生视图，注册进 app-right.js 的 rpViews，不用 iframe）。
 * 版面同屏双区（对齐 VSCode 源代码管理 + 图形的上下结构）：
 *   - 顶栏：仓库候选下拉 + 分支下拉（切换本地分支，冲突时 git 拒绝）+ 布局切换 + 刷新；
 *   - 更改区：提交信息框（AI 摘要 / 提交）+ 四组文件清单（冲突/已暂存/未暂存/未跟踪），
 *     逐文件与整组暂存/取消暂存，点文件看逐行 diff（大 diff 先渲染 400 行可展开）；
 *   - 图谱区：分支泳道图（git log --all --topo-order 的父母哈希在前端算泳道，
 *     SVG 贝塞尔曲线画合并分叉，引用徽标标注分支/远程/标签，HEAD 高亮，紧凑单行），
 *     分支行可展开本地/远程分支清单，点提交看完整信息、文件清单与分文件 diff，可加载更早；
 *   - 布局：默认上下堆叠，可一键切左右分栏；分隔条可拖，比例与方向落盘记忆；
 *   - 跟随当前会话的工作区（JX 'session:open' 换仓库，'message:done' 自动刷新——
 *     AI 在对话里做的提交/合并，图谱随后就变）；面板打开 / 切仓库 / 页面重新可见
 *     时刷新一次，另有 12 秒轻量轮询（仅 status）。
 *
 * 安全与边界（与宿主半边 gitpanel.go 对应）：仓库只能选宿主扫描出的候选；
 * 写操作：暂存 / 取消暂存 / 提交 / 切换本地分支（分支名限宿主列出的清单，
 * 有覆盖风险的切换被 git 拒绝）；AI 摘要只生成不提交；不做 push/pull/reset/discard。
 * 渲染全部走文本节点与 createTextNode，不用 innerHTML 拼仓库内容。
 * dispose 撤掉视图、定时器与事件监听。
 */
(function () {
  if (!window.CX) return;

  const ID = 'git-panel';
  const SVGNS = 'http://www.w3.org/2000/svg';
  const POLL_MS = 12000;
  const DIFF_FIRST_LINES = 400;
  // 泳道配色（与对话区亮色主题协调；下标 = 泳道色序号轮转）
  const PAL = ['#4d6bfe', '#e08a1e', '#1f9d55', '#b05bd6', '#0ea5b7', '#d6455d', '#8a7a2a', '#6b7684'];
  const LANE_W = 12, ROW_H = 26, DOT_R = 3.5;
  const LETTER_CLS = { M: 'm', A: 'a', D: 'd', U: 'd', R: 'r', C: 'c', T: 'm', '?': 'a' };

  /* ---------- 本地小工具 ---------- */

  const $id = (id) => document.getElementById(id);
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
    repo: '',
    repos: [], branch: '', oid: '', upstream: '', ahead: 0, behind: 0,
    groups: { staged: [], unstaged: [], untracked: [], conflicted: [] },
    reason: '', error: '', note: '',
    layout: 'vertical',   // vertical=上下 | horizontal=左右
    ratio: 46,            // 更改区占 split 的百分比（20..80）
    detail: null,         // 更改区：{type:'file', path, staged}
    diff: null, diffShowAll: false,
    commit: null,         // 图谱区：点开的提交详情
    commits: [], commitsMore: false, commitsLoading: false,
    branches: null, branchesOpen: false,
    commitMsg: '',        // 提交信息框文本（轮询刷新只重画清单、不重画提交框，但文本另存一份兜底）
    aiBusy: false,        // AI 摘要生成中（跨刷新保留：按钮文案/禁用不被轮询复位）
    commitBusy: false,    // 提交进行中（同上：防刷新后按钮回活、重复提交）
    opError: '',          // 操作类错误（用户点出来的）：轮询不抹，下一次成功操作才清
    timer: null,
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

    /* 顶栏：仓库 + 分支 + 布局 + 刷新 */
    const top = el('div', 'gitp-top');
    const repoSel = el('select', 'gitp-repo');
    repoSel.id = 'gitp-repo';
    repoSel.title = '切换仓库（候选由服务端扫描）';
    repoSel.onchange = () => {
      S.repo = repoSel.value;
      lsSet('jx.gitp.repo', S.repo);
      S.commits = []; S.branches = null; S.detail = null; S.commit = null;
      // 仓库换了：提交信息框清空，别把上一个仓库的信息提交到另一个仓库
      S.commitMsg = '';
      S.opError = '';
      syncCommitBar();
      refreshAll();
    };
    top.appendChild(repoSel);
    const brSel = el('select', 'gitp-branch-sel');
    brSel.id = 'gitp-branch-sel';
    brSel.title = '切换本地分支（未提交改动会被覆盖时 git 会拒绝）';
    brSel.onchange = () => checkoutBranch(brSel.value);
    top.appendChild(brSel);
    const layoutBtn = el('button', 'gitp-icon-btn gitp-layout-btn', S.layout === 'vertical' ? '⇕' : '⇔');
    layoutBtn.id = 'gitp-layout-btn';
    layoutBtn.title = '切换布局：上下堆叠 / 左右分栏（当前：' + (S.layout === 'vertical' ? '上下' : '左右') + '）';
    layoutBtn.onclick = toggleLayout;
    top.appendChild(layoutBtn);
    const refresh = el('button', 'gitp-icon-btn', '⟳');
    refresh.title = '刷新';
    refresh.onclick = refreshAll;
    top.appendChild(refresh);
    root.appendChild(top);

    /* 状态条：错误 / 提示 */
    const strip = el('div', 'gitp-strip hidden');
    strip.id = 'gitp-strip';
    root.appendChild(strip);

    /* 同屏双区：更改（上/左） + 图谱（下/右），分隔条可拖 */
    const split = el('div', 'gitp-split ' + S.layout);
    split.id = 'gitp-split';
    split.style.setProperty('--gitp-ratio', S.ratio);

    const paneCh = el('div', 'gitp-pane gitp-pane-changes');
    const chHead = el('div', 'gitp-pane-head');
    chHead.appendChild(el('span', 'gitp-pane-title', '更改'));
    chHead.appendChild(el('span', 'gitp-pane-badge', ''));
    paneCh.appendChild(chHead);
    const chBody = el('div', 'gitp-pane-body');
    chBody.id = 'gitp-changes';
    paneCh.appendChild(chBody);

    const divider = el('div', 'gitp-divider');
    divider.id = 'gitp-divider';
    divider.title = '拖动调整两区大小（双击恢复）';

    const paneGr = el('div', 'gitp-pane gitp-pane-graph');
    const grBody = el('div', 'gitp-pane-body');
    grBody.id = 'gitp-graph';
    paneGr.appendChild(grBody);

    split.appendChild(paneCh);
    split.appendChild(divider);
    split.appendChild(paneGr);
    root.appendChild(split);
    setupDivider(split, divider);
    return root;
  }

  /* 布局切换与分隔条拖拽；方向与比例落盘记忆 */
  function applyLayout() {
    const split = $id('gitp-split');
    if (!split) return;
    split.classList.toggle('vertical', S.layout === 'vertical');
    split.classList.toggle('horizontal', S.layout === 'horizontal');
    split.style.setProperty('--gitp-ratio', S.ratio);
    const btn = $id('gitp-layout-btn');
    if (btn) {
      btn.textContent = S.layout === 'vertical' ? '⇕' : '⇔';
      btn.title = '切换布局：上下堆叠 / 左右分栏（当前：' + (S.layout === 'vertical' ? '上下' : '左右') + '）';
    }
  }

  function toggleLayout() {
    S.layout = S.layout === 'vertical' ? 'horizontal' : 'vertical';
    lsSet('jx.gitp.split', JSON.stringify({ mode: S.layout, ratio: S.ratio }));
    applyLayout();
  }

  function setupDivider(split, divider) {
    let dragging = false, rect = null;
    divider.addEventListener('pointerdown', (e) => {
      if (e.button !== 0) return;
      dragging = true;
      rect = split.getBoundingClientRect();
      divider.setPointerCapture(e.pointerId);
      divider.classList.add('dragging');
      e.preventDefault();
    });
    divider.addEventListener('pointermove', (e) => {
      if (!dragging || !rect) return;
      let pct = S.layout === 'vertical'
        ? (e.clientY - rect.top) / rect.height * 100
        : (e.clientX - rect.left) / rect.width * 100;
      S.ratio = Math.round(Math.min(80, Math.max(20, pct)));
      split.style.setProperty('--gitp-ratio', S.ratio);
    });
    const done = () => {
      if (!dragging) return;
      dragging = false;
      divider.classList.remove('dragging');
      lsSet('jx.gitp.split', JSON.stringify({ mode: S.layout, ratio: S.ratio }));
    };
    divider.addEventListener('pointerup', done);
    divider.addEventListener('pointercancel', done);
    divider.addEventListener('dblclick', () => {
      S.ratio = 46;
      split.style.setProperty('--gitp-ratio', S.ratio);
      lsSet('jx.gitp.split', JSON.stringify({ mode: S.layout, ratio: S.ratio }));
    });
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
    // 操作类错误（点按钮失败）优先；它们只由下一次成功操作清除——轮询的 loadStatus
    // 成功时会把 S.error 清空（那是读取类错误的语义），不能让它顺手抹掉刚弹出的
    // 「生成摘要失败」提示，否则用户只看到按钮转一圈又复原、没有任何原因。
    if (S.opError) show('err', S.opError);
    else if (S.error) show('err', S.error);
    else if (S.note) { show('note', S.note); }
    else if (S.reason) show('warn', S.reason);
    if (S.note) {
      setTimeout(() => { S.note = ''; renderStrip(); }, 4000);
    }
  }

  /* ---------- 渲染：顶栏（仓库 + 分支） ---------- */

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

    /* 分支下拉：本地分支清单（切到未加载时先只有当前分支一项） */
    const br = $id('gitp-branch-sel');
    br.innerHTML = '';
    if (!S.repo) {
      br.appendChild(el('option', null, '分支'));
      br.disabled = true;
      return;
    }
    const locals = (S.branches && S.branches.locals) || [];
    if (S.branch && !locals.some((b) => b.name === S.branch)) {
      const o = el('option', null, S.branch);
      o.value = S.branch;
      br.appendChild(o);
    }
    for (const b of locals) {
      const o = el('option', null, b.name + (b.current ? ' ✓' : '') + (b.ahead ? ' ↑' + b.ahead : '') + (b.behind ? ' ↓' + b.behind : ''));
      o.value = b.name;
      o.title = b.short + ' ' + (b.subject || '');
      br.appendChild(o);
    }
    if (!br.children.length) br.appendChild(el('option', null, S.branch || '(无提交)'));
    br.value = S.branch || '';
    br.disabled = locals.length === 0;
  }

  /* 切换本地分支：确认后调宿主 checkout（分支名校验与冲突拒绝在服务端） */
  async function checkoutBranch(name) {
    if (!name || name === S.branch) {
      renderTop(); // 复位下拉显示
      return;
    }
    let ok = true;
    if (typeof confirmCenter === 'function') {
      ok = await confirmCenter('切换到分支「' + name + '」？工作区会切到该分支（未提交的改动一并带过去；有冲突时 git 会拒绝）。', false);
    }
    if (!ok) { renderTop(); return; }
    try {
      const d = await gitPost('checkout', { branch: name });
      S.error = '';
      S.opError = '';
      S.note = d.summary || '已切换到 ' + name;
      S.commits = []; S.detail = null; S.commit = null;
      await refreshAll();
    } catch (e) {
      S.opError = e.message;
      renderStrip();
      renderTop(); // 切换失败：下拉回到当前分支
    }
  }

  /* ---------- 渲染：更改区 ---------- */

  /* 提交信息栏（textarea + AI 摘要 / 提交）全生命周期只建一次。
   * 关键：loadStatus 每轮（12 秒轮询、切页可见、对话完成、手动刷新、切页签）
   * 都会重画更改区。若连提交框一起重建，会同时踩三个坑——
   *   1) 收到的 AI 摘要写进的是闭包里那个已经被替换掉（脱离文档）的旧 textarea，
   *      界面看不到任何输出，按钮却恢复原状（「转一圈没反应」）；
   *   2) 用户正在手打的提交信息随新节点一起消失（每 12 秒一次）；
   *   3) 焦点与光标丢失，中文输入法组合被打断。
   * 所以这里只重画下面的文件清单，提交框节点始终复用；文本另存 S.commitMsg，
   * 详情视图往返、组件重建等节点被搬动的场景也能恢复。 */
  let commitBar = null; // { cm, ta, ai, ok }

  function buildCommitBar() {
    const cm = el('div', 'gitp-commitbar');
    const ta = el('textarea', 'gitp-commit-msg');
    ta.id = 'gitp-commit-msg';
    ta.rows = 2;
    ta.placeholder = '提交信息（已暂存改动会一起提交）';
    ta.value = S.commitMsg;
    ta.oninput = () => { S.commitMsg = ta.value; };
    const btns = el('div', 'gitp-commit-btns');
    const ai = el('button', 'gitp-btn', 'AI 摘要');
    ai.title = '用当前会话配置的模型为已暂存改动生成提交信息（只生成，不提交）';
    ai.onclick = async () => {
      if (S.aiBusy) return; // 生成中重复点击直接忽略（按钮本就禁用，这里防程序化触发）
      S.aiBusy = true;
      S.opError = '';
      syncCommitBar();
      renderStrip();
      try {
        const d = await gitPost('summarize');
        S.commitMsg = d.message || '';
        S.error = '';
        S.opError = '';
      } catch (e) {
        S.opError = e.message;
      }
      S.aiBusy = false;
      syncCommitBar();
      renderStrip();
    };
    const ok = el('button', 'gitp-btn primary', '✓ 提交');
    ok.onclick = async () => {
      if (S.commitBusy) return;
      if (!(S.commitMsg || '').trim()) {
        S.opError = '提交信息为空';
        renderStrip();
        ta.focus();
        return;
      }
      S.commitBusy = true;
      S.opError = '';
      syncCommitBar();
      renderStrip();
      try {
        const d = await gitPost('commit', { message: S.commitMsg });
        S.commitMsg = '';
        S.error = '';
        S.opError = '';
        S.note = '已提交：' + (d.summary || '完成');
        refreshAll();
      } catch (e) {
        S.opError = e.message;
      }
      S.commitBusy = false;
      syncCommitBar();
      renderStrip();
    };
    btns.appendChild(ai);
    btns.appendChild(ok);
    cm.appendChild(ta);
    cm.appendChild(btns);
    return { cm, ta, ai, ok };
  }

  // 把状态刷到提交框（节点可能此刻不在文档里，比如正在看文件 diff——值照样留住）
  function syncCommitBar() {
    if (!commitBar) return;
    const { ta, ai, ok } = commitBar;
    if (ta.value !== S.commitMsg) ta.value = S.commitMsg;
    ai.disabled = S.aiBusy;
    ai.textContent = S.aiBusy ? '生成中…' : 'AI 摘要';
    ok.disabled = S.commitBusy || !S.groups.staged.length;
  }

  function ensureCommitBar() {
    if (!commitBar) commitBar = buildCommitBar();
    syncCommitBar();
    return commitBar.cm;
  }

  function renderChanges() {
    const box = $id('gitp-changes');
    if (!box) return;
    const head = root.querySelector('.gitp-pane-changes .gitp-pane-badge');
    const total = S.groups.staged.length + S.groups.unstaged.length + S.groups.untracked.length + S.groups.conflicted.length;
    if (head) head.textContent = total ? String(total) : '';

    if (S.detail) { // 详情视图整块替换（提交框节点留在 commitBar 里，返回时原样放回）
      box.textContent = '';
      box.appendChild(renderFileDetail());
      return;
    }

    /* 提交区（VSCode SCM 式：消息框在清单上方）——节点复用，刷新不重建 */
    const cm = ensureCommitBar();
    if (cm.parentNode !== box) box.insertBefore(cm, box.firstChild);
    for (const child of Array.from(box.children)) if (child !== cm) child.remove();

    const body = el('div', 'gitp-groups');
    const mkGroup = (title, list, kind) => {
      if (!list.length) return;
      const g = el('div', 'gitp-group');
      const ghead = el('div', 'gitp-group-head');
      ghead.appendChild(el('span', 'gitp-group-title', title + '（' + list.length + '）'));
      const all = el('button', 'gitp-mini-btn', kind === 'stage' ? '全部暂存 +' : '全部取消暂存 −');
      all.onclick = () => act(kind, list.map((f) => f.path));
      ghead.appendChild(all);
      g.appendChild(ghead);
      for (const f of list) g.appendChild(fileRow(f, kind));
      body.appendChild(g);
    };
    mkGroup('冲突', S.groups.conflicted, 'stage');
    mkGroup('已暂存', S.groups.staged, 'unstage');
    mkGroup('未暂存的更改', S.groups.unstaged, 'stage');
    mkGroup('未跟踪', S.groups.untracked, 'stage');
    if (!body.children.length) {
      body.appendChild(el('div', 'gitp-clean', '工作区干净，没有待提交的改动'));
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
      S.opError = '';
      await loadStatus();
      await loadGraph(true);
    } catch (e) { S.opError = e.message; renderStrip(); }
  }

  /* ---------- 渲染：文件 diff 详情（更改区内） ---------- */

  async function openFileDiff(path, staged) {
    S.detail = { type: 'file', path, staged };
    S.diff = null; S.diffShowAll = false;
    renderChanges();
    try {
      S.diff = await gitGet('diff', { path, staged: staged ? '1' : '0' });
      S.opError = '';
    } catch (e) { S.opError = e.message; renderStrip(); }
    renderChanges();
  }

  function renderFileDetail() {
    const d = S.detail;
    const box = el('div', 'gitp-detail');
    const head = el('div', 'gitp-detail-head');
    const back = el('button', 'gitp-mini-btn', '← 返回');
    back.onclick = () => { S.detail = null; renderChanges(); };
    head.appendChild(back);
    head.appendChild(el('span', 'gitp-detail-title', d.path));
    head.appendChild(el('span', 'gitp-dir', d.staged ? '已暂存的改动' : '工作区的改动'));
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
      more.onclick = () => { S.diffShowAll = true; renderChanges(); };
      wrap.appendChild(more);
    }
    return wrap;
  }

  /* ---------- 渲染：图谱区 ---------- */

  function renderGraph() {
    const box = $id('gitp-graph');
    if (!box) return;
    box.innerHTML = '';
    if (S.commit) { box.appendChild(commitDetail()); return; }

    /* 分支行：当前分支 + 可展开清单 */
    const bl = el('div', 'gitp-branchline');
    const cur = el('button', 'gitp-branchline-head');
    cur.appendChild(el('span', 'gitp-branch-dot'));
    cur.appendChild(el('span', null, S.branch || '(游离 HEAD)'));
    if (S.upstream) cur.appendChild(el('span', 'gitp-dir', '→ ' + S.upstream));
    if (S.branches) {
      cur.appendChild(el('span', 'gitp-dir', '　' + S.branches.locals.length + ' 本地 · ' + S.branches.remotes.length + ' 远程'));
    }
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

    /* 泳道图（紧凑单行：徽标 + 主题 + 右侧暗色作者·时间） */
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
      row.title = c.subject + '\n' + c.author + ' · ' + c.hash.slice(0, 8) + ' · ' + (c.date || '').slice(0, 19).replace('T', ' ');
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
        ring.setAttribute('r', DOT_R + 2.5);
        ring.setAttribute('fill', 'none');
        ring.setAttribute('stroke', PAL[lr.node.color % PAL.length]);
        ring.setAttribute('stroke-width', '1.5');
        svg.appendChild(ring);
      }
      row.appendChild(svg);

      const info = el('div', 'gitp-cinfo');
      for (const ref of c.refs || []) {
        info.appendChild(el('span', 'gitp-chip ' + ref.kind + (ref.current ? ' cur' : ''), ref.name));
      }
      info.appendChild(el('span', 'gitp-csubject', c.subject));
      info.appendChild(el('span', 'gitp-cmeta', c.author + ' · ' + relTime(c.date)));
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
      S.opError = '';
    } catch (e) { S.opError = e.message; renderStrip(); }
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

    const body = el('div', 'gitp-detail-body');
    body.appendChild(el('div', 'gitp-cl2', c.author + ' · ' + c.hash.slice(0, 8) + ' · ' + relTime(c.date)));

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
    parts.forEach((part) => {
      const lines = part.split('\n');
      let title = '改动';
      const m = /^diff --git a\/(.+) b\/(.+)$/.exec(lines[0] || '');
      if (m) title = m[2];
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
      S.commitMsg = ''; // 同上：跟着工作区一起换的还有提交信息框
      S.opError = '';
      syncCommitBar();
      lsSet('jx.gitp.repo', '');
    }
  }

  async function loadStatus() {
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
      S.error = ''; // 只清读取类错误；S.opError（用户点操作失败的原因）留给用户读
      if (S.detail) {
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
      if (reset) S.commits = [];
      renderGraph();
    }
    S.commitsLoading = false;
  }

  async function loadBranches() {
    try {
      S.branches = await gitGet('branches');
      renderTop();
      renderGraph();
    } catch (e) { /* 分支清单失败不拦主流程 */ }
  }

  function render() {
    renderTop();
    renderChanges();
    renderGraph();
    renderStrip();
  }

  function refreshAll() {
    syncSession();
    loadStatus();
    loadGraph(true);
    loadBranches();
  }

  /* ---------- 生命周期 ---------- */

  let onSessionOpen = null, onMessageDone = null, onVisible = null;

  CX.register({
    id: ID,
    init() {
      // 记忆的布局：方向与分隔比例
      try {
        const saved = JSON.parse(lsGet('jx.gitp.split', 'null') || 'null');
        if (saved && (saved.mode === 'vertical' || saved.mode === 'horizontal')) {
          S.layout = saved.mode;
          if (saved.ratio >= 20 && saved.ratio <= 80) S.ratio = saved.ratio;
        }
      } catch (e) { /* 记忆坏了用默认 */ }

      if (!root) root = buildDom();
      applyLayout();
      const body = $id('rp-body');
      if (body && !root.parentNode) body.appendChild(root);
      rpRegister('git', {
        title: 'Git',
        onOpen() { syncSession(); refreshAll(); },
      });
      if (typeof rpRenderTabs === 'function') rpRenderTabs();
      syncSession();
      render();

      onSessionOpen = () => { syncSession(); if (rpActive === 'git') refreshAll(); };
      onMessageDone = () => { if (rpActive === 'git') { loadStatus(); loadGraph(true); } };
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
      commitBar = null; // 提交框节点随 root 一起丢弃：下次激活重建（文本仍在 S.commitMsg 里）
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
