/* 组件：输入字数统计 —— 最小组件模板。
 * init：在对话框区域右上角挂一个小计数标签并监听输入框；dispose：全撤。
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
        // 挂在对话框区域右上角（#msg-zone 是消息列的定位容器；拿不到就退回消息列）
        const zone = document.getElementById('msg-zone') || document.getElementById('messages');
        (zone || input.parentElement).appendChild(badge);
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
