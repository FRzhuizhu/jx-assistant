/* 组件：强调色主题 —— 设置「外观」页内的界面强调色选择。
 * 按用户要求从输入区移到设置里；组件契约不变：
 * init() 建立全部 UI 与状态，dispose() 完整撤销，支持设置页热开关。 */
(function () {
  let styleEl = null;
  let section = null;

  // 预设色板：名字 | 主色 | 悬停色（深一档）
  const PRESETS = [
    ['默认蓝', '#4d6bfe', '#3d5bef'],
    ['紫', '#8250df', '#6f42c1'],
    ['绿', '#1f9d55', '#188a49'],
    ['橘', '#bc4c00', '#a34400'],
    ['红', '#c22f2f', '#ab2727'],
    ['青', '#0d7ea5', '#0b6d8d'],
    ['粉', '#bf3989', '#a63075'],
    ['金', '#9a6700', '#855900'],
  ];

  function apply(color, hover) {
    if (!styleEl) {
      styleEl = document.createElement('style');
      styleEl.dataset.cxAccent = '1';
      document.head.appendChild(styleEl);
    }
    styleEl.textContent = `:root{--accent:${color};--accent-hover:${hover};}`;
    try { localStorage.setItem('cx:accent', color + '|' + hover); } catch (e) { /* 忽略 */ }
  }

  function buildUI() {
    const card = document.getElementById('set-pane-appearance');
    if (!card) return;
    const saved = (() => { try { return localStorage.getItem('cx:accent') || ''; } catch (e) { return ''; } })();

    section = document.createElement('div');
    section.className = 'prov-section cx-accent-section';
    const title = document.createElement('h4');
    title.textContent = '界面强调色';
    section.appendChild(title);

    const row = document.createElement('div');
    row.className = 'cx-accent-row';
    PRESETS.forEach(([name, c, h]) => {
      const item = document.createElement('button');
      item.className = 'cx-swatch';
      item.style.background = c;
      item.title = name;
      if (saved.startsWith(c + '|')) item.classList.add('cur');
      item.onclick = () => {
        apply(c, h);
        row.querySelectorAll('.cx-swatch').forEach((s) => s.classList.remove('cur'));
        item.classList.add('cur');
      };
      row.appendChild(item);
    });
    const custom = document.createElement('input');
    custom.type = 'color';
    custom.title = '自定义颜色';
    custom.oninput = () => {
      const c = custom.value;
      const d = c.replace(/^#(..)(..)(..)$/, (_, r, g, b) => {
        const f = (x) => Math.max(0, parseInt(x, 16) - 18).toString(16).padStart(2, '0');
        return '#' + f(r) + f(g) + f(b);
      });
      apply(c, d);
      row.querySelectorAll('.cx-swatch').forEach((s) => s.classList.remove('cur'));
    };
    row.appendChild(custom);
    section.appendChild(row);

    card.appendChild(section);

    if (saved) {
      const [c, h] = saved.split('|');
      apply(c, h || c);
    }
  }

  if (window.CX) {
    CX.register({
      id: 'accent-theme',
      init: buildUI,
      dispose() {
        if (styleEl) { styleEl.remove(); styleEl = null; }
        if (section) { section.remove(); section = null; }
      },
    });
  }
})();
