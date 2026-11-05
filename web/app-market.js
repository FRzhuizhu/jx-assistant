/* 小程序平台（sites* 系列）：与市场视图平级的独立主区页面。
   市场部分已迁入 组件/skill-market/（开源前组件化：入口与页面随组件开关
   出现/消失）；本文件只剩小程序平台，同样待迁（组件/sites-platform/ 落地
   后本文件删除）。 */

/* ---------- 小程序平台（网页托管平台）：独立主区页面（与市场视图平级） ----------
   不套壳：顶栏以下整页就是平台管理页（已发布小程序列表 / 上传发布 / 数据文件 /
   二维码都在页内完成，平台页自带刷新，本端不重复实现）。本机与局域网形态 src
   用相对路径 /sites——本端透明反向代理（sitesproxy.go）转发到市场服务器：自签
   证书由 Go 端证书策略（tlspolicy.go）接管，浏览器/WebView 自己的证书校验不再
   挡路。远程外壳形态（window.JX_REMOTE）下 '/sites' 导航不经过外壳的 fetch
   劫持、会打到网关自己的平台页——改由外壳代载（密文拉回家侧拼装 blob + 文档
   内 hook，见 remote-boot.js 的 loadSitesFrame）。视图常驻不销毁，来回切
   （市场 ↔ 平台 ↔ 对话）不打扰已打开的页面。 */

// renderSitesFrame：进小程序平台页时确保 iframe 已加载。
function renderSitesFrame() {
  const frame = $('sites-frame');
  if (!frame) return;
  if (window.JX_REMOTE && typeof window.JX_REMOTE.loadSitesFrame === 'function') {
    if (frame.dataset.jxRemoteLoaded !== '1') {
      frame.dataset.jxRemoteLoaded = '1';
      window.JX_REMOTE.loadSitesFrame(frame);
    }
    return;
  }
  if (!frame.src || frame.src === 'about:blank') frame.src = '/sites';
}

function sitesViewOpen() {
  return document.body.classList.contains('sites-mode');
}

function setSitesView(on) {
  // 市场与平台都是主区整页，互斥（marketOpen 由 skill-market 组件挂 window，
  // 组件关闭/摘除后不存在——先探再关）
  if (on && typeof marketOpen === 'function' && marketOpen() && typeof closeMarket === 'function') closeMarket();
  document.body.classList.toggle('sites-mode', on);
  const view = $('sites-view');
  if (view) view.classList.toggle('hidden', !on); // .hidden 是全局 display:none!important，必须跟着切
  const btn = $('btn-sites');
  if (btn) btn.classList.toggle('active', on);
  if (on) renderSitesFrame();
}

function openSites() {
  narrowClosePanels(); // 窄屏：从侧栏浮层点进平台页即收起浮层（与进市场/设置同一套）
  setSitesView(true);
}

function closeSites() { setSitesView(false); }

// openSitesPlatform 侧栏「小程序平台」入口：代理目标从市场地址推导，没配置时
// 引导去设置（代理本身也会回 503 提示）。
async function openSitesPlatform() {
  let cfg = null;
  try { cfg = await api('/api/market'); } catch (e) {}
  if (!(cfg && cfg.configured)) {
    alert('还没配置企业技能包 / 工作流 / 小程序平台地址：请到 设置 · 内置组件 · 企业技能包 / 工作流 填服务器地址（如 https://example.com:2227），技能市场与小程序平台同一台服务端。');
    if (typeof openSettings === 'function') openSettings('components');
    return;
  }
  openSites();
}

function bindSitesEntry() {
  const btn = $('btn-sites');
  if (!btn) return;
  // 与企业技能包 / 工作流同款 toggle：开着再点关闭，关着点开
  btn.onclick = () => {
    if (sitesViewOpen()) { closeSites(); return; }
    openSitesPlatform();
  };
}
document.addEventListener('DOMContentLoaded', bindSitesEntry);
if (document.readyState !== 'loading') bindSitesEntry();
