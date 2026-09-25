/* app.js —— 启动入口：恢复本机偏好与布局状态、注册右栏视图、拉取初始数据、
   接通事件流。六个脚本中最后加载，顺序见 index.html 底部。 */

/* ---------- 初始化 ---------- */

restoreHomeDraftLS(); // 先恢复上次草稿（输入与全部选择），再渲染首页
restoreChatFont();    // 恢复常规页选的对话区字号（px）
restoreContentWidth();// 恢复上次拖定的对话区宽度
setupWidthHandles();  // 对话区两侧拖拽把手
setupJumpLatest();    // 回到底部按钮（挂进上面刚建好的 #msg-zone，需在其后）
setupScrollTuck();    // 滚动显隐：回到底部按钮 + 手机端输入区收起/展开
applyTheme();         // 恢复常规页选的主题色（白/黑/跟随系统）
applySidebarState();
resetEmpty();
loadStatus().then(loadSessions);
loadModels();
connectEvents();
loadApprovals();
loadQuestions();
setInterval(loadApprovals, 20000); // 审批轮询兜底（SSE 断连时也能拿到）
setInterval(loadQuestions, 20000);
