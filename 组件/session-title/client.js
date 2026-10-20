/* 组件 session-title 的浏览器半边：设置区只留一句说明。
 *
 * 标题生成本体在后端 worker（title_worker.py）：每轮对话结束后按节奏独立调
 * 一次模型（POST /api/llm/ask，单轮、不进对话上下文）增量修订标题；改名的
 * 落库经主程序 PATCH（autoTitle 通道），主程序广播 title 事件、前端侧栏与
 * 顶栏实时跟进。用户侧唯一的动作「恢复自动命名」不在设置区——贴着使用场景
 * 放在会话右键菜单（app-left.js / app-right.js，仅对被手动命名钉住的会话
 * 出现），设置区只负责开关（内置组件清单那行）与规则说明。 */
(function () {
  const ID = 'session-title';

  function mount(body) {
    const p = document.createElement('p');
    p.className = 'stt-note';
    p.textContent = '对话进行中由 AI 自动命名与修订会话标题：第 1 轮结束命名，之后每满 3 轮修订一次（插话不计轮次）；每次只送当前标题与最近一轮消息独立调一次模型，不占用对话上下文。手动改过标题的会话自动停改（钉住）；要恢复时在该会话上右键选「恢复自动命名」。';
    body.appendChild(p);
  }

  CX.register({
    id: ID,
    init() { CX.registerSettings({ id: ID, title: 'AI 自动会话标题', mount }); },
    dispose() { CX.unregisterSettings(ID); },
  });
})();
