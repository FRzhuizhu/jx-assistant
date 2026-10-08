/* 首次接入向导前端（wizard.go 的浏览器半边）：公司名（局域网自动发现）/ 服务器地址
 * 二选一 + 用户名 → POST /api/wizard/discover|connect → 自动配好内部模型通道、
 * 技能/组件市场、软件更新。首次启动 app.js 调 maybeShowWizard 自动弹（!wizardDone），
 * 设置·网络访问的「重新运行接入向导」可随时重开；「跳过」写 wizardDone 不再自动弹，
 * 点遮罩/Esc 关闭视为暂缓（下次启动还会弹）。 */

let wzBusy = false; // 连接在途：防重复提交

function openWizard() {
  const m = $('welcome-modal');
  if (!m) return;
  wzSetResult('', '');
  const go = $('wz-go');
  go.disabled = false;
  go.textContent = '连 接';
  go.onclick = wzConnect; // 上次成功后会被换成「完成即关」，重开时必须复位
  m.classList.remove('hidden');
  setTimeout(() => ($('wz-user').value ? $('wz-company') : $('wz-user')).focus(), 60); // 称呼在最上：先填它
}

function closeWizard() {
  const m = $('welcome-modal');
  if (m) m.classList.add('hidden');
}

function wizardOpen() {
  const m = $('welcome-modal');
  return !!m && !m.classList.contains('hidden');
}

// 首启自动弹（app.js boot 调）：向导未完成才出现
async function maybeShowWizard() {
  try {
    const st = await api('/api/status');
    if (!st.wizardDone) openWizard();
  } catch (e) { /* 状态拉不到就不打扰 */ }
}

// 结果区：kind 为 ''（隐藏）/ 'ok' / 'bad' / 'busy'
function wzSetResult(kind, html) {
  const el = $('wz-result');
  if (!el) return;
  el.className = 'remote-result' + (kind ? ' ' + kind : ' hidden');
  el.innerHTML = html || '';
}

async function wzConnect() {
  if (wzBusy) return;
  const company = ($('wz-company').value || '').trim();
  const server = ($('wz-server').value || '').trim();
  const user = ($('wz-user').value || '').trim();
  if (!company && !server) { wzSetResult('bad', '请填公司名称或服务器地址（二选一）'); return; }
  if (!user) { wzSetResult('bad', '请填用户名（服务端按人分配 API key 并统计用量）'); return; }
  wzBusy = true;
  const go = $('wz-go');
  go.disabled = true;
  try {
    let target = server;
    if (!target) {
      wzSetResult('busy', '正在局域网内查找「' + esc(company) + '」的服务端…（约 2 秒）');
      const d = await api('/api/wizard/discover', { method: 'POST', body: JSON.stringify({ company }) });
      const list = d.servers || [];
      if (!list.length) {
        wzSetResult('bad', '局域网没有发现「' + esc(company) + '」的服务端' +
          '。<br>请确认：本机与公司服务端在同一网段；或改填服务器地址；或在 设置·网络访问 ' +
          '配置总服务器地址后再试。');
        return;
      }
      target = list[0].server;
      wzSetResult('busy', '已发现服务端 ' + esc(target) + (list.length > 1 ? '（共 ' + list.length + ' 个，取第一个）' : '') + '，正在接入…');
    } else {
      wzSetResult('busy', '正在连接 ' + esc(target) + ' …');
    }
    const res = await api('/api/wizard/connect', { method: 'POST', body: JSON.stringify({ server: target, user }) });
    // 成功：刷新模型/状态（胶囊、模型菜单、市场页都会跟上）
    if (typeof loadModels === 'function') { try { await loadModels(); } catch (e) {} }
    if (typeof loadStatus === 'function') { try { loadStatus(); } catch (e) {} }
    const bits = ['<b>' + esc(res.company || '公司') + '</b> 已接入（' + esc(target) + '）'];
    if (res.api) {
      const api = res.apiDetail || {};
      bits.push('模型通道：默认 <b>' + esc(res.defaultModel || '') + '</b>（共 ' + (res.modelCount || 0) + ' 个模型，API key 已按「' + esc(user) + '」分配' + (api.monthlyQuotaUSD ? '，每月额度 $' + api.monthlyQuotaUSD : '') + '）');
    } else {
      bits.push('<span style="color:var(--amber)">模型通道未配置：' + esc(res.apiError || '服务端未接 new-api') + '（市场/更新已配好，模型可到 设置·模型 手动填）</span>');
    }
    bits.push('技能/组件市场与软件更新：已指向公司服务端');
    wzSetResult('ok', bits.join('<br>'));
    go.textContent = '完成';
    go.disabled = false;
    go.onclick = () => closeWizard(); // 完成即关（wizardDone 后端已写）
  } catch (e) {
    wzSetResult('bad', esc(e.message));
    go.disabled = false;
  } finally {
    wzBusy = false;
  }
}

function wzSkip() {
  // 明确跳过：写 wizardDone，之后不再自动弹（设置里可重开）
  api('/api/config', { method: 'POST', body: JSON.stringify({ wizardDone: true }) })
    .catch(() => {});
  closeWizard();
}

/* 绑定（一次） */
let wzBound = false;
function bindWizard() {
  if (wzBound) return;
  wzBound = true;
  $('wz-go').onclick = wzConnect;
  $('wz-skip').onclick = wzSkip;
  const m = $('welcome-modal');
  m.addEventListener('click', (e) => { if (e.target === m) closeWizard(); }); // 点遮罩=暂缓
  document.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape' || !wizardOpen()) return;
    // 有别的弹窗开着时不抢键（confirm 在更上层）
    const anyModal = [...document.querySelectorAll('.modal')].some((x) => x !== m && !x.classList.contains('hidden'));
    if (!anyModal) { e.stopPropagation(); closeWizard(); }
  });
  document.addEventListener('keydown', (e) => {
    if (e.key !== 'Enter' || !wizardOpen() || e.isComposing) return;
    if (['wz-company', 'wz-server', 'wz-user'].includes(document.activeElement && document.activeElement.id)) {
      e.preventDefault();
      wzConnect();
    }
  });
}
document.addEventListener('DOMContentLoaded', bindWizard);
