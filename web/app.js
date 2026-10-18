/* app.js —— 启动入口：恢复本机偏好与布局状态、注册右栏视图、拉取初始数据、
   接通事件流。六个脚本中最后加载，顺序见 index.html 底部。 */

/* ---------- 初始化 ---------- */

restoreHomeDraftLS(); // 先恢复上次草稿（输入与全部选择），再渲染首页
restoreChatFont();    // 恢复常规页选的阅读字号（px；对话区与计划显示区共用）
restoreContentWidth();// 恢复上次拖定的对话区宽度
setupWidthHandles();  // 对话区两侧拖拽把手
updateSendKeyHint();  // 输入框占位文案跟随发送快捷键设置（设置页更改时由其回调刷新）
setupJumpLatest();    // 回到底部按钮（挂进上面刚建好的 #msg-zone，需在其后）
setupFolds();         // 长消息折叠（主对话，超 6 行折叠 + 展开/收起）
setupScrollTuck();    // 滚动显隐：回到底部按钮 + 手机端输入区收起/展开
applyTheme();         // 恢复常规页选的背景色（白/黑/跟随系统）
restoreSidebarWidth(); // 恢复上次拖定的左侧栏宽度（--sb-w，须在 applySidebarState 之前）
applySidebarState();
resetEmpty();
loadStatus().then(loadSessions);
loadModels();
loadChatModes(); // 工作模式清单：唯一数据源在后端（GET /api/modes），拉回来重建模式下拉
connectEvents();
loadApprovals();
loadQuestions();
// 首次接入向导（app-wizard.js）：未完成过（!wizardDone）时自动弹——公司名局域网
// 发现 / 服务器地址二选一，接通即配好模型通道、市场与更新
maybeShowWizard();
// 审批 / 提问的兜底轮询：只在事件流没连上时才真的去拉（SSE 正常时它就是多余的）。
// 前端渲染本身也已经是增量的——数据没变就不动 DOM，用户打进卡片的字不会被冲掉。
setInterval(() => {
  if (eventStream && eventStream.readyState === 1) return; // 1 = OPEN：事件流活着，不必轮询
  loadApprovals();
  loadQuestions();
}, 20000);

/* ---------- 唤醒对账（见 app-chat.js 的 resyncViews）----------
   页面回到可见 / 网络恢复时，对当前视图对齐一次服务端真相：手机锁屏、切网、
   桌面 WebView 挂起造成的流静默死亡与事件丢失都靠这一下归位（streaming 假
   阳性复位、后端还在跑则接上流）。500ms 去抖合并连发的 visibilitychange/online。 */
let resyncTimer = null;
function scheduleResync() {
  if (resyncTimer) return;
  resyncTimer = setTimeout(() => {
    resyncTimer = null;
    if (typeof resyncViews === 'function') resyncViews();
  }, 500);
}
document.addEventListener('visibilitychange', () => { if (!document.hidden) scheduleResync(); });
window.addEventListener('online', scheduleResync);
