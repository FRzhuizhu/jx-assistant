/* 组件 session-title 的浏览器半边：设置区（状态显示 + 恢复自动命名）。
 *
 * 标题生成本体在后端 worker（title_worker.py）：每轮对话结束后按「截至当前
 * 的全部用户消息」独立调一次模型（POST /api/llm/ask，单轮、不进对话上下文）
 * 增量修订标题；用户手动改名（PATCH 打钉）后该会话自动停改。
 * 这里只做用户侧可见的两件小事：
 *   1. 说明节奏与钉住规则；
 *   2. 显示当前会话的钉住状态，「恢复自动命名」解钉（下一轮结束后生效）。 */
(function () {
  const ID = 'session-title';

  function curSessionId() {
    // 与 git-panel 同款口径：app-core.js 的 state.cur（注入时机晚于核心脚本）
    return (typeof state !== 'undefined' && state.cur && state.cur.id) || '';
  }

  async function api(method, path, body) {
    const opt = { method, headers: { 'Content-Type': 'application/json' } };
    if (body !== undefined) opt.body = JSON.stringify(body);
    const r = await fetch(path, opt);
    let d = null;
    try { d = await r.json(); } catch (e) { /* 空体忽略 */ }
    return { status: r.status, data: d };
  }

  function el(tag, cssText, text) {
    const n = document.createElement(tag);
    if (cssText) n.style.cssText = cssText;
    if (text !== undefined) n.textContent = text;
    return n;
  }

  function mount(body) {
    body.appendChild(el('div',
      'font-size:12.5px;line-height:1.7;color:var(--text-2,#666);margin:2px 0 10px',
      '每轮对话结束后，AI 用该轮与历史的用户消息独立生成/修订一次标题（单次调用，不占用对话上下文）：首轮概括主题，之后在旧标题上增量演化——保留最初核心、语义合并新内容，合不拢时用「＋」连接。手动改过标题的会话不再自动改名。'));

    const status = el('div',
      'font-size:12.5px;color:var(--text-2,#666);margin-bottom:8px', '正在读取当前会话状态…');
    const btn = el('button',
      'display:none;font-size:12.5px;padding:4px 12px;cursor:pointer');
    btn.textContent = '恢复自动命名';
    btn.onclick = async () => {
      const sid = curSessionId();
      if (!sid) return;
      btn.disabled = true;
      const r = await api('PATCH', '/api/sessions/' + encodeURIComponent(sid), { autoTitleUnpin: true });
      btn.disabled = false;
      if (r.status === 200) { refresh(); } else {
        status.textContent = '操作失败（HTTP ' + r.status + '），请稍后再试';
      }
    };
    body.appendChild(status);
    body.appendChild(btn);

    async function refresh() {
      const sid = curSessionId();
      if (!sid) {
        status.textContent = '当前不在任何会话里（打开一个会话后可在此管理它的标题状态）。';
        return;
      }
      const r = await api('GET', '/api/sessions/' + encodeURIComponent(sid));
      if (r.status !== 200 || !r.data || !r.data.meta) {
        status.textContent = '读取会话状态失败（HTTP ' + r.status + '）。';
        return;
      }
      if (r.data.titlePinned) {
        status.textContent = '当前会话「' + (r.data.meta.title || '') + '」：已手动命名，自动改名停用。';
        btn.style.display = '';
      } else {
        status.textContent = '当前会话「' + (r.data.meta.title || '') + '」：自动命名进行中（每轮结束后修订）。';
        btn.style.display = 'none';
      }
    }
    refresh();
  }

  CX.register({
    id: ID,
    init() { CX.registerSettings({ id: ID, title: 'AI 自动会话标题', mount }); },
    dispose() { CX.unregisterSettings(ID); },
  });
})();
