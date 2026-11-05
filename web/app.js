/* app.js —— 启动入口：恢复本机偏好与布局状态、注册右栏视图、拉取初始数据、
   接通事件流。六个脚本中最后加载，顺序见 index.html 底部。 */

/* ---------- 初始化 ---------- */

restoreHomeDraftLS(); // 先恢复上次草稿（输入与全部选择），再渲染首页
restoreChatFont();    // 恢复常规页选的阅读字号（px；对话区与计划显示区共用）
restoreContentWidth();// 恢复上次拖定的对话区宽度
setupWidthHandles();  // 对话区两侧拖拽把手
updateSendKeyHint();  // 输入框占位文案跟随发送快捷键设置（设置页更改时由其回调刷新）
setupJumpLatest();    // 回到底部按钮（挂进上面刚建好的 #msg-zone，需在其后）
setupFolds();         // 长消息折叠（用户气泡超 6 行、AI 用户气泡超 2 行折叠 + 展开/收起）
setupScrollTuck();    // 滚动显隐：回到底部按钮 + 手机端输入区收起/展开
applyTheme();         // 恢复常规页选的背景色（白/黑/跟随系统）
restoreSidebarWidth(); // 恢复上次拖定的左侧栏宽度（--sb-w，须在 applySidebarState 之前）
applySidebarState();
resetEmpty();
loadStatus().then(loadSessions).then(restoreLastSession); // 会话列表到位后再定落哪一屏（上次的对话 / 首页）
loadModels();
loadChatModes(); // 工作模式清单：唯一数据源在后端（GET /api/modes），拉回来重建模式下拉
connectEvents();
// 广播无回放的另两类丢帧窗口也各补一次（与 connectEvents 的 onopen 同一根治，
// 详见那边注释）：
// · jx:tunnel——远程隧道真正可用的转折点（首通/P2P 直连建立/SSE 重连成功，
//   remote-boot 派发）：手机从后台回来隧道重建后，错过窗口里的审批/提问广播
//   由此补上；
// · visibilitychange——页面回前台：SSE 可能假死未报错（readyState 仍 1，
//   兜底轮询被跳过），后台期间错过的广播只能靠这里补。
window.addEventListener('jx:tunnel', () => { loadApprovals(); loadQuestions(); });
document.addEventListener('visibilitychange', () => {
  if (document.hidden) return;
  loadApprovals();
  loadQuestions();
});
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

/* ---------- 上次所在的对话：页面重新加载后回到那里 ----------
   手机壳从桌面切回、进程被系统杀掉后冷启动、远程外壳自愈整页重拉——只要 WebView
   里的文档重载，前端就是从零启动：没有这条记忆就永远落在首页（用户 2026-10-22 报的
   「一从外面进 APP 就只进首页，之前的对话记忆没了」）。记的是「最后打开的那个主对话」：
   - 打开即写：主对话区每成功开一个会话都会发 JX session:open（侧栏点选、通知进去、
     子任务「打开会话」、分支/派生切换全走同一条路）；
   - 明确点「新工作任务」回首页就清（app-chat.js 的 goHomeNewTask）——清了之后
     重启也回默认页（主对话），首页只作为发起新任务的过渡停留；
   - 启动时确认它还在列表里才恢复：没有记录（新安装/首次使用）或列表里真没有
     这条（已删/已归档，记忆一并清掉）→ 落**默认页＝数字助理主对话（长期助理）**
     （2026-11-05 用户口径「新安装的直接进来就是长期助理」），主对话也打不开才
     留在首页；上次那条打不开（电脑端刚不可达）→ 提示一句回首页。两种情况都不
     留白屏、不留转圈。 */
function rememberLastSession(id) { lsSet('jx.lastSess', id || ''); }
JX.on('session:open', (s) => { if (s && s.id) rememberLastSession(s.id); });

async function restoreLastSession() {
  const id = lsGet('jx.lastSess', '');
  // 没有「上次会话」记录（新安装/首次使用/上次明确回首页）：落默认页——数字
  // 助理主对话（长期助理），不再停在「新建对话」首页。openAssistant 内部任何
  // 一步失败（接口打不开等）都不抛白屏：catch 掉留在首页即可
  if (!id) { try { await openAssistant(); } catch (e) { /* 留在首页 */ } return; }
  const brief = (state.sessions || []).find((s) => s.id === id);
  if (!brief || brief.archived) {
    // 列表空着多半是这一拉没成功（电脑端刚不可达）：留着记忆下次再试；
    // 列表非空而里面没有这条，才是真的没了（被删/被归档）——清掉记忆后同样
    // 落默认页（主对话）
    if ((state.sessions || []).length) rememberLastSession('');
    try { await openAssistant(); } catch (e) { /* 留在首页 */ }
    return;
  }
  try {
    await openSession(id); // 失败不抛：openSession 把失败画在对话区（下面按它判定）
  } catch (e) { /* 按「打不开」处理 */ }
  // 真落到了目标会话上（不在加载态、也不是加载失败的报错壳）才算恢复成功
  if (state.cur && state.cur.id === id && !state._loading && !mainView.col.querySelector('.cl-retry')) return;
  // 打不开：撤干净、提示一句回首页。记忆保留——下次网络正常进来还能回到这里
  navTok.main++;
  navBusy.main = false;
  state._loading = false;
  state._pfxSid = null;
  state.cur = null;
  state.msgs = [];
  resetEmpty();
  renderPanels();
  refreshCtxChip();
  toast('没能回到上次的对话，已回到首页');
}

