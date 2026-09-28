/* app.js —— 启动入口：恢复本机偏好与布局状态、注册右栏视图、拉取初始数据、
   接通事件流。六个脚本中最后加载，顺序见 index.html 底部。 */

/* ---------- 初始化 ---------- */

restoreHomeDraftLS(); // 先恢复上次草稿（输入与全部选择），再渲染首页
restoreChatFont();    // 恢复常规页选的对话区字号（px）
restoreContentWidth();// 恢复上次拖定的对话区宽度
setupWidthHandles();  // 对话区两侧拖拽把手
updateSendKeyHint();  // 输入框占位文案跟随发送快捷键设置（设置页更改时由其回调刷新）
setupJumpLatest();    // 回到底部按钮（挂进上面刚建好的 #msg-zone，需在其后）
setupFolds();         // 长消息折叠（主对话，超 6 行折叠 + 展开/收起）
setupScrollTuck();    // 滚动显隐：回到底部按钮 + 手机端输入区收起/展开
applyTheme();         // 恢复常规页选的主题色（白/黑/跟随系统）
restoreSidebarWidth(); // 恢复上次拖定的左侧栏宽度（--sb-w，须在 applySidebarState 之前）
applySidebarState();
resetEmpty();
loadStatus().then(loadSessions);
loadModels();
connectEvents();
loadApprovals();
loadQuestions();
// 审批 / 提问的兜底轮询：只在事件流没连上时才真的去拉（SSE 正常时它就是多余的）。
// 前端渲染本身也已经是增量的——数据没变就不动 DOM，用户打进卡片的字不会被冲掉。
setInterval(() => {
  if (eventStream && eventStream.readyState === 1) return; // 1 = OPEN：事件流活着，不必轮询
  loadApprovals();
  loadQuestions();
}, 20000);
