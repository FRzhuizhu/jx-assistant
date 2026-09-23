/* 组件：输入字数统计 —— 最小组件模板。
 * init：向输入框区域挂一个计数标签并监听 input；dispose：全撤。
 * 新建组件时复制本目录，改 manifest 与本文件即可。 */
(function () {
  let badge = null;
  let onInput = null;

  if (window.CX) {
    CX.register({
      id: 'input-counter',
      init() {
        const input = document.getElementById('input');
        if (!input) return;
        badge = document.createElement('span');
        badge.id = 'cx-counter';
        const update = () => {
          const n = input.value.length;
          badge.textContent = n + ' 字';
          badge.classList.toggle('warn', n > 2000);
        };
        onInput = update;
        input.addEventListener('input', onInput);
        const bar = document.querySelector('.composer-bar');
        (bar || input.parentElement).appendChild(badge);
        update();
      },
      dispose() {
        const input = document.getElementById('input');
        if (input && onInput) input.removeEventListener('input', onInput);
        if (badge) { badge.remove(); badge = null; }
      },
    });
  }
})();
