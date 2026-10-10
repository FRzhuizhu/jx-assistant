/* input-counter —— 输入字数统计（纯组件：浏览器半边 + 桥 worker）。
 * 角标浮在输入卡右上角（pointer-events:none 不挡任何交互）；同时给 textarea
 * 加右侧内边距让文字提前约两格换行，绝不与角标重叠——不与任何按钮抢位。
 * 统计经声明式桥（POST /api/c/input-counter/stats）由 worker 计算，验证
 * 自包含后端免编译运行链路。dispose 完整撤销（角标/类名/监听全清）。 */
(function () {
  const ID = 'input-counter';
  let disposing = false;
  let debounce = 0;
  const mounted = []; // {ta, badge, onInput, card}

  function fmt(d) {
    if (!d || !d.ok) return '';
    const parts = [d.chars + ' 字'];
    if (d.words) parts.push(d.words + ' 词');
    return parts.join(' · ');
  }

  function update(ta, badge) {
    clearTimeout(debounce);
    debounce = setTimeout(() => {
      if (disposing) return;
      fetch('/api/c/input-counter/stats', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ cmd: 'stats', text: ta.value || '' }),
      }).then((r) => r.json()).then((d) => {
        if (disposing) return;
        badge.textContent = fmt(d);
      }).catch(() => { if (!disposing) badge.textContent = ''; });
    }, 250);
  }

  function mount(ta) {
    const card = ta.closest('.composer-card') || ta.parentElement;
    if (!card || !ta) return;
    card.classList.add('ic-card');
    const badge = document.createElement('span');
    badge.className = 'ic-badge';
    badge.textContent = '';
    card.appendChild(badge);
    ta.classList.add('ic-on'); // 右侧内边距：文字提前换行，不与角标重叠
    const onInput = () => update(ta, badge);
    ta.addEventListener('input', onInput);
    mounted.push({ ta, badge, onInput, card });
    update(ta, badge);
  }

  CX.register({
    id: ID,
    init() {
      disposing = false;
      mount(document.getElementById('input'));
      mount(document.getElementById('sub-input')); // 子对话输入（视图隐藏时挂不上，组件热开关重挂）
    },
    dispose() {
      disposing = true;
      clearTimeout(debounce);
      for (const m of mounted) {
        m.ta.removeEventListener('input', m.onInput);
        m.ta.classList.remove('ic-on');
        m.card.classList.remove('ic-card');
        m.badge.remove();
      }
      mounted.length = 0;
    },
  });
})();
