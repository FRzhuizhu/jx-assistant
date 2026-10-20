/* 组件 session-title 的浏览器半边：设置区（状态显示 + 恢复自动命名）。
 *
 * 标题生成本体在后端 worker（title_worker.py）：每轮对话结束后按节奏独立调
 * 一次模型（POST /api/llm/ask，单轮、不进对话上下文）增量修订标题；用户手动
 * 改名（PATCH 打钉）后该会话自动停改。这里只做用户侧可见的两件小事：
 *   1. 说明节奏与钉住规则；
 *   2. 显示当前会话的钉住状态，「恢复自动命名」解钉（后续轮次继续按节奏修订）。
 * 按钮常驻不隐藏：条件不满足时置灰并写明原因——跟着当前会话「时隐时现」
 * 会被当成渲染坏了（本组件初版的实际教训）。 */
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

  function el(tag, cls, text) {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined) n.textContent = text;
    return n;
  }

  function mount(body) {
    body.appendChild(el('p', 'stt-note',
      '第 1 轮对话结束由 AI 命名会话标题，之后每满 3 轮修订一次（执行中的插话消息不计轮次、不用于改标题）。每次只送「当前标题 + 最近一轮的用户消息」独立调一次模型，不占用对话上下文；标题在旧标题上增量演化——保留最初核心、语义合并新内容，合不拢时用「＋」连接。'));

    const row = el('div', 'stt-row');
    row.appendChild(el('span', 'stt-label', '当前会话'));
    const status = el('span', 'stt-status', '正在读取当前会话状态…');
    const btn = el('button', 'stt-btn', '恢复自动命名');
    btn.type = 'button';
    btn.disabled = true;
    btn.title = '当前会话被手动命名（钉住）时可点：解除钉住，恢复 AI 自动改标题';
    btn.onclick = async () => {
      const sid = curSessionId();
      if (!sid) return;
      btn.disabled = true;
      const r = await api('PATCH', '/api/sessions/' + encodeURIComponent(sid), { autoTitleUnpin: true });
      if (!status.isConnected) return; // 设置区已被重挂：结果作废
      if (r.status === 200) {
        status.textContent = '已恢复自动命名：后续轮次将按上述节奏继续修订标题。';
      } else {
        status.textContent = '操作失败（HTTP ' + r.status + '），请稍后再试。';
        btn.disabled = false;
      }
    };
    row.appendChild(status);
    row.appendChild(btn);
    body.appendChild(row);

    body.appendChild(el('p', 'stt-note',
      '手动改过标题的会话不再自动改名（钉住）；要恢复时先在左侧打开该会话，再点上方「恢复自动命名」。'));

    async function refresh() {
      const sid = curSessionId();
      if (!sid) {
        status.textContent = '当前不在任何会话里（打开一个会话后可在此管理它的标题状态）。';
        btn.disabled = true;
        return;
      }
      const r = await api('GET', '/api/sessions/' + encodeURIComponent(sid));
      if (!status.isConnected) return; // 设置区已被重挂：结果作废（同 voice 组件口径）
      if (r.status !== 200 || !r.data || !r.data.meta) {
        status.textContent = '读取会话状态失败（HTTP ' + r.status + '）。';
        btn.disabled = true;
        return;
      }
      const title = (r.data.meta.title || '').trim() || '（无标题）';
      if (r.data.titlePinned) {
        status.textContent = '「' + title + '」已手动命名，自动改名停用。';
        btn.disabled = false;
      } else {
        status.textContent = '「' + title + '」自动命名进行中。';
        btn.disabled = true;
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
