/* 组件：语音（朗读与听写） —— 组件/voice/client.js
 *
 * 分工（2026-10-07 定稿，用户口径）：
 *
 * 1) 朗读（TTS）：**后端实现**。前端 POST /api/voice/tts（文本+语速+音色）→
 *    后端直连微软 Edge「大声朗读」通道（speech.platform.bing.com 的 readaloud，
 *    无密钥、免配置，与 Edge/WebView2 内置朗读同一套服务；协议移植自聊天服务
 *    chat-server 的 Node 实现）→ mp3 流回 → blob 化 <audio> 播放。音色清单来自
 *    /api/voice/voices（Edge 神经音色：晓晓/云希/Aria…，中文排前）。朗读不再用
 *    浏览器的 speechSynthesis——那在远程手机端上部分浏览器没有，旧版连朗读
 *    设置区都不渲染。
 *
 * 2) 听写（STT）：**浏览器自带的 SpeechRecognition**（WebView2/Edge 内置的微软
 *    识别接口，音频由浏览器直接发给它的识别服务）——即本组件一直以来的实现，
 *    逻辑与样式原样保留：连接中/录音中三态、灰色暂定层（未定稿转写叠在输入框
 *    上显示为灰字、静置 2.5 秒落成正文）、断线退避重连、时长上限倒计时徽标、
 *    一次性轻提示。桌面 WebView2 可用（注意：该能力随运行时版本波动，153 系
 *    运行时有微软官方回归，失败提示的处置指引见 dictationFailMsg）；远程手机端
 *    受手机浏览器识别服务可达性限制（安卓 Chrome 走谷歌服务，国内不通）——后端
 *    化听写的可行性结论见 语音后端化-研究与改动说明.md（无头 Edge 假麦克风试验：
 *    音频能进 getUserMedia，但 SR 不消费假设备，路不通）。
 *
 * 3) 快捷键：默认鼠标侧键，可自定义键盘单键/组合键、甚至鼠标左右键同按
 *    （不允许单独的左键或右键）；点按切换与长按输入两种模式（设置页可切）。
 *    仅在窗口聚焦时激活（失焦自动松手）。主输入框的占位灰字会像发送快捷键
 *    一样带上语音快捷键提示（app-core 的 sendKeyHint 追加 window.JXVoiceHint，
 *    子输入框由本组件自己拼）。远程页面且视口宽度 <500（手机端）时隐藏听筒
 *    按钮（window.JX_REMOTE 区分远程；手机浏览器多不支持语音识别）。
 *
 * 全程零配置（不需要任何密钥）。按钮 DOM 在 web/index.html（位置与样式不变），
 * 默认带 .hidden——按钮属于本组件，组件关掉（设置 → 内置组件）或没加载到页面
 * = 按钮整组不出现。热开关 dispose 完整撤销（识别停止、朗读停止、钩子注销、
 * 设置页消失），不需要刷新页面。
 */
(function () {
  if (!window.CX) return;

  const SR = window.SpeechRecognition || window.webkitSpeechRecognition;

  let active = false;    // 组件是否激活：dispose 后立刻失效，兜住还在飞的异步回调
  let msgAction = null;  // 已注册的消息操作行钩子（注销用）
  const busHooks = [];   // 已挂的事件总线回调 [{ev, fn}]（注销用）

  /* ---------- 偏好（localStorage，命名空间 cx:voice） ---------- */

  const pref = {
    get(k, d) {
      try { const v = localStorage.getItem('cx:voice:' + k); return v === null ? d : JSON.parse(v); }
      catch (e) { return d; } // 取值失败（禁用/坏数据）一律回默认
    },
    set(k, v) { try { localStorage.setItem('cx:voice:' + k, JSON.stringify(v)); } catch (e) { /* 隐私模式忽略 */ } },
  };

  // 朗读语速（0.5–3，默认 1.2；后端折成 SSML 的百分比语速）
  function spokenRate() {
    const v = Number(pref.get('rate', 1.2));
    return isFinite(v) ? Math.min(3, Math.max(0.5, v)) : 1.2;
  }
  // 单次听写时长上限（秒）
  function maxDictateSec() {
    const v = Number(pref.get('maxSec', 240));
    return isFinite(v) && v >= 30 ? Math.min(600, v) : 240;
  }

  const ICON = {
    speak: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M11 5L6 9H2v6h4l5 4V5z"/><path d="M15.5 8.5a5 5 0 0 1 0 7"/><path d="M18.5 5.5a9 9 0 0 1 0 13"/></svg>',
    stop: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M11 5L6 9H2v6h4l5 4V5z"/><line x1="16" y1="9" x2="22" y2="15"/><line x1="22" y1="9" x2="16" y2="15"/></svg>',
  };

  function apiErrMsg(res, text) {
    try {
      const j = JSON.parse(text);
      if (j && j.error) return j.error;
    } catch (e) { /* 非 JSON 正文 */ }
    return 'HTTP ' + res.status;
  }

  /* ================= 语音快捷键（主副双快捷键） =================
     表示：**备选数组**——shortcutAlternatives() 返回 [[键…], …]，每个元素是一种
     可触发的组合（组合内全按住才触发，任一组合触发即可）。
     · 默认＝**主副双快捷键，永久并存**：鼠标侧键（主）+ Alt 键（副），两者都
       可触发——有侧键的用侧键，没有的自然用 Alt。
     · 用户自定义（设置页录制）：单组合，元素为 'ctrl'|'alt'|'shift'|'meta' |
       单个字母/数字 | 'f1'..'f12' | 'space' | 'mouse-side'（侧键，前后两枚都算）
       | 'mouse-left' | 'mouse-right'。
     · 清空（设置页「清空」按钮）：存空数组＝停用快捷键（只点按钮听写）。
     · 独按修饰键（如默认副键 Alt）：按住期间又按了别的键（用户在按 Alt+X
       组合）→ 解除不触发；点按模式在**独按松开**时触发，避免任何 Alt 组合误触。
     · 校验：不允许只剩单独的 mouse-left 或单独的 mouse-right（废掉正常点击/菜单）。
     · 模式：tap=点按切换；hold=长按输入（按住说话、松开/失焦结束）。
     仅窗口聚焦时激活；失焦自动清空按键状态并结束长按会话。纯字母/数字单键在
     输入框聚焦时不触发（字符要留给打字），组合键不受此限。 */

  const KEY_NAMES = {
    ctrl: 'Ctrl', alt: 'Alt', shift: 'Shift', meta: 'Win', space: '空格',
    'mouse-side': '鼠标侧键', 'mouse-left': '鼠标左键', 'mouse-right': '鼠标右键',
  };

  function isModKey(k) { return k === 'ctrl' || k === 'alt' || k === 'shift' || k === 'meta'; }

  // 主/副两个独立快捷键槽（设置页各有一个键帽框，可分别录制）：
  // 槽值＝null（跟默认：主=鼠标侧键，副=Alt）| 数组（自定义组合）| 空数组（停用）。
  // 旧版单快捷键偏好（cx:voice:shortcut）一次性迁移：非空→主槽；空（清空过）→两槽停用。
  function shortcutSlot(which) {
    if (!pref.get('scMigrated', false)) {
      const old = pref.get('shortcut', null);
      if (Array.isArray(old)) {
        pref.set('scMain', old.length ? old : []);
        pref.set('scSub', old.length ? null : []);
      }
      pref.set('scMigrated', true);
    }
    const v = pref.get(which === 'main' ? 'scMain' : 'scSub', null);
    if (Array.isArray(v)) return v.length ? v : [];
    return which === 'main' ? ['mouse-side'] : ['alt'];
  }
  function setShortcutSlot(which, v) { pref.set(which === 'main' ? 'scMain' : 'scSub', v); }

  // 触发备选＝两个槽里没被停用的那些
  function shortcutAlternatives() {
    return [shortcutSlot('main'), shortcutSlot('sub')].filter((a) => a.length > 0);
  }
  function voiceShortcutMode() {
    return pref.get('shortcutMode', 'tap') === 'hold' ? 'hold' : 'tap';
  }
  function shortcutLabel() {
    const alts = shortcutAlternatives();
    if (!alts.length) return '无（已清空）';
    return alts.map((alt) => alt.map((k) => KEY_NAMES[k] || k.toUpperCase()).join('+')).join('或');
  }
  function validShortcut(keys) {
    if (!Array.isArray(keys) || keys.length === 0) return false;
    if (keys.length === 1 && (keys[0] === 'mouse-left' || keys[0] === 'mouse-right')) return false;
    return keys.every((k) => KEY_NAMES[k] !== undefined || /^[a-z0-9]$/.test(k) || /^f([1-9]|1[0-2])$/.test(k));
  }

  // 事件 → 规范化键名
  function normKey(e) {
    const k = e.key;
    if (k === 'Control') return 'ctrl';
    if (k === 'Alt') return 'alt';
    if (k === 'Shift') return 'shift';
    if (k === 'Meta') return 'meta';
    const lk = String(k).toLowerCase();
    if (/^[a-z0-9]$/.test(lk)) return lk;
    if (/^f([1-9]|1[0-2])$/.test(lk)) return lk;
    if (lk === ' ') return 'space';
    return null; // 其他键不收（避免录出系统级/生僻组合）
  }
  function normMouse(e) {
    return { 0: 'mouse-left', 2: 'mouse-right', 3: 'mouse-side', 4: 'mouse-side' }[e.button] || null;
  }
  function isEditable(t) {
    return !!(t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable));
  }

  // 全局按键状态与触发（监听装在 document/window 上，dispose 摘除）
  const heldKey = new Set();   // 当前按着的键盘键
  const heldMouse = new Set(); // 当前按着的鼠标键
  let holdSession = false;     // 长按会话进行中
  let firedLatch = false;      // 点按已触发，等快捷键全部松开后复位
  let captureMode = false;     // 设置页录制快捷键时暂停引擎
  let captureCtl = null;       // 当前录制会话（重复点击录制按钮时作废旧会话）
  const voiceCtl = { main: null, sub: null }; // 各视图的听写控制器（快捷键驱动它们）
  let onResize = null;         // 远程窄屏隐藏的窗口尺寸联动

  function micHiddenByEnv() {
    return !!window.JX_REMOTE && window.innerWidth < 500; // 远程手机端页面
  }

  function activeVoiceCtl() {
    const ae = document.activeElement;
    if (voiceCtl.sub && ae && ae.id === 'sub-input') return voiceCtl.sub;
    return voiceCtl.main || voiceCtl.sub || null;
  }

  function altHeld(alt) { // 一个备选组合是否被完整按住
    for (const k of alt) {
      if (!heldKey.has(k) && !heldMouse.has(k)) return false;
    }
    return true;
  }

  // 独按修饰键状态（如默认备选里的 Alt）：按住期间按了别的键就解除，
  // 防止用户按 Alt+X 组合时误触语音
  let armedSolo = null; // { key, started }（started=hold 模式已开说话）

  function cancelSolo() {
    if (!armedSolo) return;
    if (armedSolo.started) {
      holdSession = false;
      const ctl = activeVoiceCtl();
      if (ctl && ctl.active()) ctl.stop();
    }
    armedSolo = null;
  }

  function shortcutFire(action) { // action: 'press' | 'release'
    if (!active || captureMode || !document.hasFocus()) return;
    const ctl = activeVoiceCtl();
    if (!ctl) return;
    const alts = shortcutAlternatives();
    const mode = voiceShortcutMode();

    if (action === 'press') {
      if (micHiddenByEnv()) return; // 远程手机端：静默——按钮已隐藏，不弹任何提示
                                     //（提示曾放在快捷键匹配之前，手机上每次点按都弹，扰人）
      // 1) 非独按修饰键的备选：完整按住即触发
      for (const alt of alts) {
        if (alt.length === 1 && isModKey(alt[0])) continue; // 独按修饰键走 2)
        if (!altHeld(alt)) continue;
        if (mode === 'hold') {
          if (!holdSession && !ctl.active()) { holdSession = true; ctl.start(); }
        } else if (!firedLatch) {
          firedLatch = true; // 一次完整按下只触发一回，全松开后复位
          ctl.toggle();
        }
        return;
      }
      // 2) 独按修饰键：按住先布防；点按模式在独按松开时触发（见 release）
      for (const alt of alts) {
        if (alt.length === 1 && isModKey(alt[0]) && altHeld(alt)) {
          if (!armedSolo) {
            armedSolo = { key: alt[0], started: false };
            if (mode === 'hold' && !ctl.active()) {
              holdSession = true;
              armedSolo.started = true;
              ctl.start();
            }
          }
          return;
        }
      }
      return;
    }

    // release
    if (armedSolo) {
      const k = armedSolo.key;
      if (!heldKey.has(k) && !heldMouse.has(k)) { // 独按松开
        if (armedSolo.started) {
          holdSession = false;
          if (ctl.active()) ctl.stop();
        } else if (mode === 'tap') {
          if (!micHiddenByEnv()) ctl.toggle();
        }
        armedSolo = null;
        firedLatch = false;
        return;
      }
    }
    if (holdSession) {
      const anyHeld = alts.some((a) => altHeld(a)) || (armedSolo && armedSolo.started);
      if (!anyHeld) {
        holdSession = false;
        if (ctl.active()) ctl.stop();
      }
    }
    if (!alts.some((a) => altHeld(a)) && !armedSolo) firedLatch = false;
  }

  let scKeyDown = null, scKeyUp = null, scMouseDown = null, scMouseUp = null, scContextMenu = null, scBlur = null;

  function clearShortcutState() {
    heldKey.clear();
    heldMouse.clear();
    firedLatch = false;
    cancelSolo();
    if (holdSession) {
      holdSession = false;
      const ctl = activeVoiceCtl();
      if (ctl && ctl.active()) ctl.stop();
    }
  }

  function installShortcutEngine() {
    scKeyDown = (e) => {
      if (!active || captureMode) return;
      const k = normKey(e);
      if (!k) return;
      // 纯字母/数字单键：目标是输入框时不触发也不拦截（字符留给打字）
      if (k.length === 1 && /^[a-z0-9]$/.test(k) && isEditable(e.target)) {
        heldKey.delete(k);
        return;
      }
      if (shortcutAlternatives().some((a) => a.includes(k))) e.preventDefault();
      if (e.repeat) return;
      if (armedSolo && k !== armedSolo.key) cancelSolo(); // 独按期间按了别的键：解除
      heldKey.add(k);
      shortcutFire('press');
    };
    scKeyUp = (e) => {
      if (!active) return;
      const k = normKey(e);
      if (k) heldKey.delete(k);
      shortcutFire('release');
    };
    scMouseDown = (e) => {
      if (!active || captureMode) return;
      const m = normMouse(e);
      if (!m) return;
      // 侧键在快捷键里时拦掉默认的后退/前进导航
      if (shortcutAlternatives().some((a) => a.includes(m))) e.preventDefault();
      if (armedSolo && m !== armedSolo.key) cancelSolo();
      heldMouse.add(m);
      shortcutFire('press');
    };
    scMouseUp = (e) => {
      if (!active) return;
      const m = normMouse(e);
      if (m) heldMouse.delete(m);
      shortcutFire('release');
    };
    // 快捷键含鼠标右键（左右同按）时，按住期间压掉右键菜单
    scContextMenu = (e) => {
      if (!active) return;
      if (shortcutAlternatives().some((a) => a.includes('mouse-right')) &&
          heldMouse.has('mouse-left') && heldMouse.has('mouse-right')) {
        e.preventDefault();
      }
    };
    scBlur = () => clearShortcutState();
    document.addEventListener('keydown', scKeyDown);
    document.addEventListener('keyup', scKeyUp);
    document.addEventListener('mousedown', scMouseDown);
    document.addEventListener('mouseup', scMouseUp);
    document.addEventListener('contextmenu', scContextMenu);
    window.addEventListener('blur', scBlur);
    // 远程窄屏隐藏的窗口尺寸联动（旋转/分屏跨过 500 阈值时即时显隐）
    onResize = () => {
      if (!active) return;
      for (const view of [mainView, subView]) {
        const b = viewEl(view, 'btn-voice');
        if (!b) continue;
        b.classList.toggle('hidden', micHiddenByEnv());
      }
    };
    window.addEventListener('resize', onResize);
  }

  function removeShortcutEngine() {
    if (scKeyDown) document.removeEventListener('keydown', scKeyDown);
    if (scKeyUp) document.removeEventListener('keyup', scKeyUp);
    if (scMouseDown) document.removeEventListener('mousedown', scMouseDown);
    if (scMouseUp) document.removeEventListener('mouseup', scMouseUp);
    if (scContextMenu) document.removeEventListener('contextmenu', scContextMenu);
    if (scBlur) window.removeEventListener('blur', scBlur);
    if (onResize) window.removeEventListener('resize', onResize);
    scKeyDown = scKeyUp = scMouseDown = scMouseUp = scContextMenu = scBlur = onResize = null;
    clearShortcutState();
  }

  /* ---------- 占位灰字：语音快捷键提示（主输入框经基座 sendKeyHint 追加
     window.JXVoiceHint；子输入框由本组件拼接，dispose 时还原） ---------- */
  let subBasePH = null; // 子输入框原始占位文案

  function voiceHintText() {
    if (window.JX_ELECTRON) return ''; // Electron 壳无听写（见按钮解禁处注释），不占位提示
    if (!shortcutAlternatives().length) return ''; // 已清空：占位提示不带语音段
    const mode = voiceShortcutMode();
    return '；语音输入：' + shortcutLabel() + (mode === 'hold' ? '（按住说话）' : '（点按）');
  }
  function refreshShortcutPlaceholders() {
    if (!active) return;
    window.JXVoiceHint = voiceHintText();
    if (typeof updateSendKeyHint === 'function') updateSendKeyHint();
    const sub = document.getElementById('sub-input');
    if (sub) {
      if (subBasePH === null) subBasePH = sub.placeholder || '向子对话发消息…';
      sub.placeholder = subBasePH + voiceHintText();
    }
  }
  function restoreSubPlaceholder() {
    const sub = document.getElementById('sub-input');
    if (sub && subBasePH !== null) sub.placeholder = subBasePH;
  }

  /* ================= 一、朗读（TTS，后端 Edge 通道，免配置） ================= */

  /** 去掉会被念出来的 markdown 语法噪声（与 dsh-speech 的 cleanForSpeech 同款）。 */
  function cleanForSpeech(src) {
    let s = String(src || '');
    s = s.replace(/(```|~~~)[\s\S]*?(```|~~~)/g, ' ');
    s = s.replace(/`([^`]*)`/g, '$1');
    s = s.replace(/!\[([^\]]*)\]\([^)]*\)/g, '$1');
    s = s.replace(/\[([^\]]*)\]\([^)]*\)/g, '$1');
    s = s.replace(/^#{1,6}\s+/gm, '');
    s = s.replace(/^\s*[-*+]\s+/gm, '');
    s = s.replace(/^\s*\d+\.\s+/gm, '');
    s = s.replace(/^\s*>\s?/gm, '');
    s = s.replace(/\|/g, ' ');
    s = s.replace(/[*_~]{1,3}/g, '');
    s = s.replace(/\n{2,}/g, '。').replace(/\n/g, ' ');
    return s.replace(/\s{2,}/g, ' ').trim();
  }

  let playingBtn = null; // 正在朗读的那条消息的按钮（单飞：同时只有一条在播）

  let playURL = null;    // blob URL（播完/停止即 revoke）
  let playEl = null;     // <audio> 元素

  function restBtn(btn) {
    btn.classList.remove('playing');
    btn.innerHTML = ICON.speak + '朗读';
    btn.title = '朗读这条消息';
  }

  function stopAudio() {
    if (playEl) {
      playEl.onended = playEl.onerror = null;
      playEl.pause();
      playEl = null;
    }
    if (playURL) { URL.revokeObjectURL(playURL); playURL = null; }
  }

  function stopSpeak() {
    stopAudio();
    if (playingBtn) { restBtn(playingBtn); playingBtn = null; }
  }

  /** 后端合成并播放一段文本：返回 Promise，播放结束/出错时落定（单飞：先停上一条）。 */
  function ttsPlay(text) {
    stopAudio();
    return fetch('/api/voice/tts', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ text, rate: spokenRate(), voice: pref.get('edgeVoice', '') }),
    }).then(async (res) => {
      if (!res.ok) throw new Error(apiErrMsg(res, await res.text().catch(() => '')));
      return res.blob();
    }).then((blob) => new Promise((resolve, reject) => {
      playURL = URL.createObjectURL(blob);
      playEl = new Audio(playURL);
      playEl.onended = () => { stopAudio(); resolve(); };
      playEl.onerror = () => { stopAudio(); reject(new Error('播放失败')); };
      playEl.play().catch(reject);
    }));
  }

  /** 朗读一条消息：按钮态驱动 + 后端合成播放。 */
  async function speakTextBackend(text, btn) {
    playingBtn = btn;
    btn.classList.add('playing');
    btn.innerHTML = ICON.stop + '停止';
    btn.title = '停止朗读';
    try {
      await ttsPlay(text);
      if (playingBtn === btn) { restBtn(btn); playingBtn = null; }
    } catch (e) {
      if (playingBtn === btn) { restBtn(btn); playingBtn = null; }
      voiceToast('朗读失败：' + (e && e.message ? e.message : e));
    }
  }

  function speakText(text, btn) {
    if (playingBtn === btn) { stopSpeak(); return; } // 再点一次 = 停止
    stopSpeak(); // 单飞：新的朗读立即停掉上一条
    speakTextBackend(text, btn);
  }

  // 消息操作行钩子：给每条助手消息挂「朗读」（钩子可注销，热关闭后新渲染的行不会再有）
  function onMessageAction(row, m) {
    if (!active || m.role !== 'assistant') return;
    const text = cleanForSpeech(m.content);
    if (!text) return;
    const btn = document.createElement('button');
    btn.className = 'act-copy act-tts';
    btn.innerHTML = ICON.speak + '朗读';
    btn.title = '朗读这条消息';
    btn.onclick = () => speakText(text, btn);
    row.appendChild(btn);
  }

  /* ================= 二、听写（语音输入，浏览器 SpeechRecognition） =================
     以下为本组件一直以来的实现，原样保留（原属基础架构 web/app-chat.js，组件化时
     迁入）。四个层次：
     1) 连接：点麦克风先进「连接中」——按钮转圈，不算开始说话。连上（onstart）才
        变蓝进入录音态；VOICE_CONNECT_TIMEOUT 之内连不上就退避重试，重试
        VOICE_MAX_RETRY 次仍连不上才弹报错。首次连接往往比后续慢（要跟云端服务
        建链），所以这里的等待给得比"点了没反应"的看门狗宽，且**不打断正在建立
        的连接**。
     2) 识别：浏览器原生 Web Speech（WebView2/Edge/Chrome——微软的识别服务）。
        音频由浏览器直接发给云端识别服务，链路抖动、静音收尾、引擎自行结束都很
        常见 → 录音态掉线同样退避重连。
     3) 灰字：转写先落在叠层上显示为灰字——它不在输入框的值里，所以不可编辑、
        不可选中；静置 VOICE_SETTLE_MS（2.5 秒）后落成黑色正文写进输入框，此后
        与手打文字一样可编辑。断线收尾时把已有灰字立即落定，避免白说。
     4) 节流：单次听写最长 maxDictateSec()（默认 240 秒），最后 9 秒在麦克风按钮
        上出倒计时徽标。 */

  const voiceOn = { main: false, sub: false };
  const voiceSess = { main: null, sub: null };

  // 桌面壳判定（runDesktop 导航的 URL 带 shell=desktop，app-core 据此给 <html>
  // 挂 desktop-shell 类）与 WebView2/Edge 运行时版本（UA 的 Edg/ 段）。听写走
  // WebView2 内置的浏览器识别，该能力随运行时版本波动（微软 153 系运行时有官方
  // 回归、152 正常），失败提示里带上版本，在出问题的机器上好对号入座。
  function desktopShell() {
    return document.documentElement.classList.contains('desktop-shell');
  }
  function edgeRuntimeVer() {
    const m = navigator.userAgent.match(/Edg\w*\/([\d.]+)/);
    return m ? m[1] : '';
  }
  function dictationFailMsg(err) {
    const code = err ? '（' + err + '）' : '';
    if (desktopShell()) {
      const v = edgeRuntimeVer();
      return '语音识别连接失败' + code + '：桌面壳的听写依赖 WebView2 运行时内置的识别通道，'
        + '部分运行时版本存在故障' + (v ? '（当前 ' + v + '）' : '')
        + '。可更新 WebView2 运行时后重试，或以 -browser 参数启动、用 Edge 浏览器打开本页使用语音';
    }
    return '语音识别连接失败（连续 ' + VOICE_MAX_RETRY + ' 次）：浏览器可能不支持或识别服务不可达，建议换用 Microsoft Edge 浏览器' + code;
  }

  const VOICE_MAX_RETRY = 3;          // 连续连不上/没结果的重试上限（任一次拿到文字即清零）
  const VOICE_RETRY_BASE = 400;       // 首次重试等待（毫秒），此后线性退避
  const VOICE_RETRY_STEP = 400;       // 每多一次重试多等这么久
  const VOICE_RETRY_MAX = 1600;       // 单次等待上限（毫秒）
  const VOICE_CONNECT_TIMEOUT = 5000; // 连接阶段等待上限（毫秒）：超时才判这次失败
  const VOICE_SETTLE_MS = 2500;       // 灰字静置多久落成正文
  const VOICE_SWEEP_MS = 400;         // 定稿巡检间隔
  const VOICE_STOP_GRACE = 1200;      // 用户按停后等引擎吐完最后一句的兜底时长（毫秒）

  /**
   * 把「上一段已有文字」与「新到文字」拼起来：中文之间不加空格（语音转写的中文
   * 分段常常只是停顿，插空格会切碎句子），英文/数字单词之间保留一个空格。
   */
  function voiceJoin(prev, next) {
    if (prev === '') return next;
    const tail = prev.charAt(prev.length - 1), head = next.charAt(0);
    return (/[A-Za-z0-9]/.test(tail) && /[A-Za-z0-9]/.test(head)) ? prev + ' ' + next : prev + next;
  }

  /**
   * 灰色暂定层：把还没定稿的转写文字叠在输入框上显示为灰字。
   * 它 pointer-events:none 且不进输入框的值 → 不可编辑、不可选中、不吃点击；
   * 已定稿正文用透明占位一起排版，保证灰字正好接在正文末尾（换行位置一致）。
   */
  function voiceShadow(inp, tail) {
    const card = inp.closest ? inp.closest('.composer-card') : null;
    if (!card) return;
    let sh = null;
    for (const child of card.children) { if (child.classList.contains('voice-shadow')) { sh = child; break; } }
    if (!sh) {
      sh = document.createElement('div');
      sh.classList.add('voice-shadow', 'hidden');
      card.appendChild(sh);
    }
    if (!tail) {
      sh.classList.add('hidden');
      sh.textContent = '';
      if (inp.classList) inp.classList.remove('voice-dictating');
      return;
    }
    if (getComputedStyle(card).position === 'static') card.style.position = 'relative';
    const cs = getComputedStyle(inp);
    sh.style.left = inp.offsetLeft + 'px';
    sh.style.top = inp.offsetTop + 'px';
    sh.style.width = inp.offsetWidth + 'px';
    sh.style.height = inp.offsetHeight + 'px';
    ['font-size', 'font-family', 'font-weight', 'line-height', 'letter-spacing',
      'padding', 'box-sizing', 'white-space', 'word-break', 'overflow-wrap', 'text-align']
      .forEach((k) => { sh.style[k] = cs[k]; });
    // 输入框内容超出可视高度而滚动时，灰字层跟着上移，保持对齐
    sh.style.transform = 'translateY(-' + (inp.scrollTop || 0) + 'px)';
    if (inp.classList) inp.classList.add('voice-dictating'); // 灰字在时隐掉 placeholder，避免两层字叠在一起
    sh.textContent = '';
    const head = document.createElement('span');
    head.classList.add('voice-shadow-head');
    head.textContent = inp.value;            // 透明占位：与正文同排版，灰字接在其后
    const rest = document.createElement('span');
    rest.classList.add('voice-shadow-tail');
    rest.textContent = tail;
    sh.append(head, rest);
    sh.classList.remove('hidden');
  }

  /** 组件卸载时连节点一起摘掉灰字层（正常运行期只隐藏，留着复用）。 */
  function removeVoiceShadow(inp) {
    if (inp.classList) inp.classList.remove('voice-dictating');
    const card = inp && inp.closest ? inp.closest('.composer-card') : null;
    if (!card) return;
    for (const child of Array.from(card.children)) {
      if (child.classList.contains('voice-shadow')) child.remove();
    }
  }

  /** 一次性轻提示（语音连不上/朗读失败等），浮在输入区上方，不打断输入。 */
  function voiceToast(text) {
    const el = document.createElement('div');
    el.classList.add('voice-toast');
    el.textContent = text;
    document.body.appendChild(el);
    requestAnimationFrame(() => el.classList.add('show'));
    setTimeout(() => {
      el.classList.remove('show');
      setTimeout(() => { if (el.remove) el.remove(); }, 300);
    }, 6000);
  }

  /** 把一段文字落进输入框正文（程序化写入不触发 input 事件：手动落草稿）。 */
  function commitText(view, text) {
    if (!text) return;
    const inp = view.input;
    inp.value = voiceJoin(inp.value, text);
    autoGrow(inp);
    if (view === mainView) persistMainDraftLS(); else persistSubDraftLS();
  }

  function bindVoiceButton(view) {
    const btn = viewEl(view, 'btn-voice');
    if (!btn) return;
    const key = view.key;
    // 一次听写的会话状态（重连全程复用同一份，只换识别实例）
    const sess = voiceSess[key] = {
      rec: null,          // 当前识别实例：每次重试换新实例（onend 之后不能可靠复用）
      retries: 0,         // 本次听写内连续「没结果」的次数
      active: false,      // 会话是否还在（true 期间按钮是连接中/录音态、灰字层工作）
      live: false,        // 当前实例是否已连上（onstart 之后为 true）
      done: false,        // 已收尾：用户点了停止、超时、或重试次数用尽
      stopping: false,    // 用户已按停：等引擎吐完最后一句就收尾，不再重试
      segs: new Map(),    // 未定稿的转写：槽位索引 → { text, final, changedAt }
      committed: new Map(), // 已落进正文的槽位：索引 → 已落定文字（引擎重报时按前缀剥离）
      maxIdx: -1,         // 引擎当前报到的槽位（比它小说明引擎重置了结果列表）
      startedAt: 0,       // 本次听写开始时刻（时长上限与倒计时用）
      count: null,        // 倒计时徽标元素
      spin: null,         // 连接中转圈元素
      restartTimer: null, // 退避重试定时器
      connectTimer: null, // 连接阶段超时定时器
      stopTimer: null,    // 按停后的兜底收尾定时器
      hintTimer: null,    // 提示文案的清除定时器
      sweepTimer: null,   // 定稿巡检定时器
      teardown: null,     // 组件卸载时的收尾（下方赋值）
    };
    let lastTail = null, lastHead = null; // 灰字层上次渲染的内容（避免每 400ms 重排）
    if (getComputedStyle(btn).position === 'static') btn.style.position = 'relative';

    const clearT = (name) => { if (sess[name]) { clearTimeout(sess[name]); sess[name] = null; } };
    // 提示文案：同一时刻只留一条；ms 为空 = 常驻到下一次提示
    const setHint = (text, ms) => {
      clearT('hintTimer');
      view.hint.textContent = text;
      if (ms) sess.hintTimer = setTimeout(() => { if (view.hint.textContent === text) view.hint.textContent = ''; }, ms);
    };
    // 进度提示（连接中/重连中）：这类文案由我们写、也由我们清；用标记区分，
    // 免得把别的提示（报错等）误清掉
    const setProgressHint = (text) => { sess.progressHint = true; setHint(text); };
    const clearProgressHint = () => { if (sess.progressHint) { sess.progressHint = false; setHint(''); } };
    // 按钮三态：空闲（白）/ 连接中（转圈）/ 录音中（蓝色脉冲）
    const setSpin = (on) => {
      if (on && !sess.spin) {
        sess.spin = document.createElement('span');
        sess.spin.classList.add('voice-spinner');
        btn.appendChild(sess.spin);
        btn.classList.add('connecting');
      } else if (!on && sess.spin) {
        sess.spin.remove();
        sess.spin = null;
        btn.classList.remove('connecting');
      }
    };
    // 倒计时徽标：最后 9 秒显示秒数，换数字时弹一下
    const setCount = (n) => {
      if (n === null) {
        if (sess.count) sess.count.remove();
        sess.count = null;
        return;
      }
      if (!sess.count) {
        sess.count = document.createElement('span');
        sess.count.classList.add('voice-count');
        btn.appendChild(sess.count);
      }
      if (sess.count.textContent !== String(n)) {
        sess.count.textContent = String(n);
        sess.count.classList.remove('pop');
        void sess.count.offsetWidth; // 触发重排，让动画能重放
        sess.count.classList.add('pop');
      }
    };
    // 灰字层渲染（tail 为空即隐藏）
    const renderShadow = () => {
      const inp = view.input;
      let tail = '';
      for (const i of Array.from(sess.segs.keys()).sort((a, b) => a - b)) tail += sess.segs.get(i).text;
      if (tail === lastTail && inp.value === lastHead) return;
      lastTail = tail;
      lastHead = inp.value;
      voiceShadow(inp, tail);
    };
    // 把指定槽位定稿：写进输入框正文（黑字），从灰字层移除
    const commitSegs = (idxs) => {
      let text = '';
      for (const i of idxs.slice().sort((a, b) => a - b)) {
        const seg = sess.segs.get(i);
        if (!seg) continue;
        sess.segs.delete(i);
        sess.committed.set(i, (sess.committed.get(i) || '') + seg.text);
        text += seg.text;
      }
      if (text !== '') commitText(view, text);
      renderShadow();
    };
    // 全部定稿（断线收尾、用户按停、引擎重置结果列表时用：先说出来的先落定，不丢字）
    const commitAll = () => {
      if (sess.segs.size > 0) commitSegs(Array.from(sess.segs.keys()));
      else renderShadow();
    };
    // 收尾：定稿残留灰字、停掉实例与定时器、按钮回空闲、灰字层收起
    const finish = (msg, toast) => {
      const rec = sess.rec;
      sess.progressHint = false;
      sess.active = false;
      sess.live = false;
      sess.done = true;
      sess.stopping = false;
      sess.rec = null;
      clearT('restartTimer');
      clearT('connectTimer');
      clearT('stopTimer');
      if (sess.sweepTimer) { clearInterval(sess.sweepTimer); sess.sweepTimer = null; }
      commitAll();
      voiceOn[key] = false;
      btn.classList.remove('on');
      setSpin(false);
      setCount(null);
      try { if (rec) rec.stop(); } catch (e) { /* 已结束 */ }
      if (msg) setHint(msg, 4000); else clearT('hintTimer');
      if (toast) voiceToast(toast);
    };
    // 用户按停（含时长到顶）：给引擎一点时间把最后一句吐完，1.2 秒兜底收尾
    const stopByUser = () => {
      if (sess.stopping || sess.done) return;
      sess.stopping = true;
      clearT('restartTimer');
      clearT('connectTimer');
      const rec = sess.rec;
      try { if (rec) rec.stop(); } catch (e) { /* 已结束 */ }
      clearT('stopTimer');
      sess.stopTimer = setTimeout(() => { finish(''); }, VOICE_STOP_GRACE);
    };
    // 退避重试；连续 VOICE_MAX_RETRY 次都没结果就放弃并弹报错。幂等——已有重试在
    // 排队时直接忽略（同一轮故障常同时给 onerror 与 onend 两个回调）。
    const scheduleRetry = () => {
      if (!sess.active || sess.done || sess.stopping || sess.restartTimer) return;
      if (sess.retries >= VOICE_MAX_RETRY) {
        // 带上最后一次的错误码（network / no-speech / aborted / language-not-supported…）；
        // 桌面壳与浏览器形态给不同指引（桌面壳指向 WebView2 运行时，见 dictationFailMsg）
        finish('语音识别已停止', dictationFailMsg(sess.lastErr));
        return;
      }
      const wait = Math.min(VOICE_RETRY_BASE + sess.retries * VOICE_RETRY_STEP, VOICE_RETRY_MAX);
      sess.retries += 1;
      sess.live = false;
      btn.classList.remove('on');
      setSpin(true); // 等待重试期间也转圈：让用户看到「还在连」，不是又白了一下
      setProgressHint('语音识别连接不上，正在重连（' + sess.retries + '/' + VOICE_MAX_RETRY + '）…');
      sess.restartTimer = setTimeout(() => { sess.restartTimer = null; startOnce(); }, wait);
    };
    // 定稿巡检：说话中不落字（引擎持续在改），停下约 2.5 秒才落成正文；
    // 顺带管倒计时与时长上限
    const sweep = () => {
      if (!sess.active) return;
      const t = Date.now();
      const ready = [];
      for (const [i, seg] of sess.segs) {
        if (t - seg.changedAt >= VOICE_SETTLE_MS) ready.push(i);
      }
      if (ready.length > 0) commitSegs(ready); else renderShadow();
      const remain = maxDictateSec() * 1000 - (t - sess.startedAt);
      if (remain <= 0) { stopByUser(); return; }
      setCount(remain <= 9000 ? Math.ceil(remain / 1000) : null);
    };
    const startOnce = () => {
      if (!sess.active || sess.done) return;
      let rec;
      try { rec = new SR(); } catch (e) { scheduleRetry(); return; }
      sess.rec = rec;
      sess.live = false;
      sess.maxIdx = -1;        // 新实例的结果列表从 0 重新计
      sess.committed.clear();  // 槽位索引跟着换了一套，清掉旧的落定记忆
      rec.lang = 'zh-CN';
      rec.interimResults = true; // 边说边出灰字
      rec.continuous = true;
      rec.onstart = () => {
        // 连上了：撤掉连接超时、转圈换蓝色。**不在这里清零重试计数**——引擎连上
        // 就断的「蓝一下就白」正是要重连的场景，清零会让它无限重连、麦克风一直
        // 占着；真正的清零在 onresult（拿到文字 = 这条链路确实通）。
        if (sess.rec !== rec) return;
        clearT('connectTimer');
        sess.live = true;
        setSpin(false);
        btn.classList.add('on');
        clearProgressHint();
      };
      rec.onresult = (e) => {
        if (sess.rec !== rec || !sess.active || sess.done) return;
        if (e.resultIndex < sess.maxIdx) commitAll(); // 引擎重置了结果列表：先落定已有的
        for (let i = e.resultIndex; i < e.results.length; i++) {
          const item = e.results[i];
          if (!item || item.length === 0) continue;
          let text = item[0].transcript || '';
          if (text === '') continue;
          // 这个槽位已经落定过（引擎把整句重报一遍）：只取新增部分，避免重复落字
          const done = sess.committed.get(i) || '';
          if (done !== '') {
            if (text === done) continue;
            if (text.indexOf(done) === 0) text = text.slice(done.length);
            if (text === '') continue;
          }
          const prev = sess.segs.get(i);
          if (prev && prev.final) continue; // 已定稿的槽位不再被 interim 覆盖
          const final = item[0].isFinal === true;
          if (!prev || prev.text !== text) sess.segs.set(i, { text, final, changedAt: Date.now() });
          sess.maxIdx = Math.max(sess.maxIdx, i);
        }
        sess.retries = 0; // 有转写进来 = 链路是通的，重试预算重新计
        clearProgressHint();
        renderShadow();
      };
      rec.onend = () => {
        if (sess.rec !== rec || !sess.active || sess.done) return;
        sess.rec = null;
        if (sess.stopping) { finish(''); return; } // 用户已按停：正常收尾
        commitAll(); // 断在半句上：先说出来的先落定，别白说
        scheduleRetry();
      };
      rec.onerror = (e) => {
        if (sess.rec !== rec || !sess.active || sess.done) return;
        const code = (e && e.error) || '';
        sess.lastErr = code; // 记下现场：3 次重试都失败时把它写进浮层提示
        // 只有重试也救不回来的错误直接收场；network / no-speech / aborted 等
        // 一律按瞬时故障处理，交给重试机制自愈
        if (code === 'not-allowed' || code === 'service-not-allowed') {
          // 桌面壳里点不出浏览器授权界面：麦克风授权来自启动参数自动放行，
          // 还失败多半是 Windows 系统级麦克风开关（隐私设置）在拦
          finish(desktopShell()
            ? '麦克风未授权：请检查 Windows 设置 → 隐私 → 麦克风（含「允许桌面应用访问麦克风」）'
            : '麦克风未授权：请在浏览器里允许麦克风权限');
          return;
        }
        if (code === 'audio-capture') { finish('没有可用的麦克风（可能被其他程序占用）'); return; }
        commitAll();
        scheduleRetry();
      };
      try {
        rec.start();
      } catch (e) { scheduleRetry(); return; }
      // 连接阶段超时：到点还没 onstart 就按这次失败处理（重试期间按钮继续转圈）
      clearT('connectTimer');
      sess.connectTimer = setTimeout(() => {
        if (sess.rec !== rec || !sess.active || sess.done || sess.live) return;
        sess.rec = null;
        try { rec.stop(); } catch (e) { /* 已结束 */ }
        commitAll();
        scheduleRetry();
      }, VOICE_CONNECT_TIMEOUT);
    };

    btn.onclick = () => {
      if (!SR) { setHint('当前浏览器不支持语音识别（建议 Edge / Chrome）', 4000); return; }
      if (micHiddenByEnv()) return; // 远程手机端：按钮已隐藏，静默
      if (sess.active) { stopByUser(); return; } // 再点一次 = 结束听写（连接中/录音中都一样）
      sess.active = true;
      sess.done = false;
      sess.stopping = false;
      sess.live = false;
      sess.retries = 0;
      sess.startedAt = Date.now();
      sess.maxIdx = -1;
      sess.segs.clear();
      sess.committed.clear();
      voiceOn[key] = true;
      btn.classList.remove('on');
      setSpin(true); // 先转圈：连上了再变蓝
      setProgressHint('正在连接语音服务…');
      if (sess.sweepTimer) clearInterval(sess.sweepTimer);
      sess.sweepTimer = setInterval(sweep, VOICE_SWEEP_MS);
      startOnce();
    };

    // 组件卸载：把这一套会话彻底收干净（定时器、识别实例、按钮附加物、灰字层、提示）
    sess.teardown = () => {
      finish('');
      sess.segs.clear();
      sess.committed.clear();
      setHint('');
      removeVoiceShadow(view.input);
      btn.classList.remove('on', 'connecting');
      voiceOn[key] = false;
    };

    // 交给快捷键引擎的控制器（点按=toggle，长按=start/stop；pauseForSend=发送前
    // 同步落定灰字并硬收尾，见 window.JXVoicePause）
    return {
      active: () => sess.active,
      toggle: () => btn.onclick(),
      start: () => { if (!sess.active) btn.onclick(); },
      stop: () => { if (sess.active) btn.onclick(); },
      pauseForSend: () => {
        if (!sess.active) return;
        commitAll(); // 未定稿转写同步落进输入框（随本条消息一起发出）
        const rec = sess.rec;
        sess.stopping = true;
        try { if (rec) rec.stop(); } catch (e) { /* 已结束 */ }
        finish(''); // 硬收尾：不等引擎尾包（迟到的定稿会在发送后污染下一条草稿）
      },
    };
  }

  /* ================= 三、设置区（独立设置页「语音」） ================= */

  function mountSettings(el) {
    el.innerHTML = '';

    const note = (text) => {
      const p = document.createElement('p');
      p.className = 'sp-note';
      p.textContent = text;
      el.appendChild(p);
    };
    const group = (title) => {
      const g = document.createElement('div');
      g.className = 'sp-group';
      g.textContent = title;
      el.appendChild(g);
    };
    const row = (label, ctrl) => {
      const r = document.createElement('div');
      r.className = 'sp-row';
      const b = document.createElement('span');
      b.className = 'sp-label';
      b.textContent = label;
      r.appendChild(b);
      r.appendChild(ctrl);
      el.appendChild(r);
      return r;
    };
    const btn = (text, title) => {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'sp-btn';
      b.textContent = text;
      if (title) b.title = title;
      return b;
    };

    /* ----- 朗读（后端 Edge 通道，免配置） ----- */
    group('朗读（后端微软 Edge 通道，免配置）');
    note('朗读由后端直连微软 Edge「大声朗读」接口（无密钥），任何浏览器、远程手机端行为一致。');

    // 语速：滑块 + 当前倍率（后端折成 SSML 百分比语速）
    const rate = document.createElement('input');
    rate.type = 'range';
    rate.min = '0.5'; rate.max = '3'; rate.step = '0.1';
    rate.value = String(spokenRate());
    rate.className = 'sp-range';
    const rateVal = document.createElement('span');
    rateVal.className = 'sp-value';
    rateVal.textContent = rate.value + '×';
    rate.oninput = () => { rateVal.textContent = rate.value + '×'; pref.set('rate', parseFloat(rate.value)); };
    row('朗读语速', rate).appendChild(rateVal);

    // 音色：Edge 神经音色清单（/api/voice/voices，中文排前）
    const sel = document.createElement('select');
    sel.className = 'sp-select';
    const fillVoices = (voices) => {
      sel.innerHTML = '';
      const auto = document.createElement('option');
      auto.value = '';
      auto.textContent = '自动（中文用晓晓）';
      sel.appendChild(auto);
      for (const v of voices) {
        const o = document.createElement('option');
        o.value = v.name;
        o.textContent = v.display;
        sel.appendChild(o);
      }
      sel.value = pref.get('edgeVoice', '');
    };
    fillVoices([]);
    fetch('/api/voice/voices').then(async (res) => {
      if (!res.ok) throw new Error(apiErrMsg(res, await res.text().catch(() => '')));
      const d = await res.json();
      if (!el.isConnected) return; // 设置区已收起：结果作废
      fillVoices(d.voices || []);
    }).catch((e) => {
      const o = document.createElement('option');
      o.value = '';
      o.textContent = '音色清单获取失败（' + (e && e.message ? e.message : e) + '）——点试听可验证服务连通';
      sel.innerHTML = '';
      sel.appendChild(o);
    });
    sel.onchange = () => pref.set('edgeVoice', sel.value);
    row('朗读音色', sel);

    // 试听：播放中变「停止」（走同一条后端通道）
    const test = btn('试听', '用当前语速与音色试听一句');
    let testing = false;
    const setTesting = (on) => { testing = on; test.textContent = on ? '停止' : '试听'; };
    test.onclick = () => {
      if (testing) { stopSpeak(); setTesting(false); return; }
      setTesting(true);
      ttsPlay('你好，这是坚祥数字助理的语音试听。')
        .catch((e) => voiceToast('试听失败：' + (e && e.message ? e.message : e)))
        .then(() => { if (testing && !playEl) setTesting(false); });
    };
    row('试听', test);

    /* ----- 听写 ----- */
    group('听写');
    const sec = document.createElement('input');
    sec.type = 'number';
    sec.min = '30'; sec.max = '600'; sec.step = '10';
    sec.className = 'sp-num';
    sec.value = String(maxDictateSec());
    sec.title = '单次听写的最长时长；到点自动收尾，最后 9 秒按钮上出倒计时';
    sec.onchange = () => {
      const v = Math.round(Number(sec.value));
      const ok = isFinite(v) && v >= 30 && v <= 600 ? v : 240;
      sec.value = String(ok);
      pref.set('maxSec', ok);
    };
    const unit = document.createElement('span');
    unit.className = 'sp-value';
    unit.textContent = '秒（30–600，默认 240）';
    row('单次听写时长上限', sec).appendChild(unit);
    note('听写用浏览器自带的语音识别（WebView2 / Edge / Chrome——微软的识别服务，'
      + '音频由浏览器直接发给它的识别服务）：链路抖动会自动退避重连；输入框上方浮出'
      + '的灰字是未定稿的转写，停下约 2.5 秒才落成正文。连接失败时多半是浏览器不支持'
      + '或识别服务不可达，换用 Microsoft Edge 通常可解；桌面壳同理——识别通道随'
      + ' WebView2 运行时版本波动（部分版本有故障），更新 WebView2 运行时'
      + '（https://go.microsoft.com/fwlink/p/?LinkId=2124703）或以 -browser 参数'
      + '启动、改用 Edge 打开可解。');

    /* ----- 快捷键 ----- */
    group('语音快捷键');

    // 触发方式：点按切换 / 长按输入
    const modeSel = document.createElement('select');
    modeSel.className = 'sp-select';
    const m1 = document.createElement('option');
    m1.value = 'tap'; m1.textContent = '点按切换（按一下开始，再按一下结束）';
    const m2 = document.createElement('option');
    m2.value = 'hold'; m2.textContent = '长按输入（按住说话，松开结束）';
    modeSel.append(m1, m2);
    modeSel.value = voiceShortcutMode();
    modeSel.onchange = () => {
      pref.set('shortcutMode', modeSel.value === 'hold' ? 'hold' : 'tap');
      firedLatch = false;
      refreshShortcutPlaceholders();
    };
    row('触发方式', modeSel);

    // 快捷键：主/副两个独立的键帽框（各自录制），外加 恢复默认 / 清空。
    // 录制：按下组合 → 全部松开时落定；Esc/失焦/硬超时取消。侧键在窗口外松开
    // 收不到 mouseup，曾有录制态永久挂死的坑——重复点击直接作废旧会话重新
    // 开始（自愈），外加 12 秒无条件硬超时兜底。
    const resetBtn = btn('恢复默认', '主=鼠标侧键、副=Alt 键');
    const clearBtn = btn('清空', '停用全部快捷键：只能点麦克风按钮听写');
    // 单个键帽框的渲染（keys 空＝该槽已停用）
    const renderScBox = (box, keys) => {
      box.innerHTML = '';
      box.classList.remove('rec');
      if (!keys || !keys.length) {
        const n = document.createElement('span');
        n.className = 'sc-none';
        n.textContent = '无（已停用，点此录制）';
        box.appendChild(n);
        return;
      }
      const combo = document.createElement('span');
      combo.className = 'sc-combo';
      keys.forEach((k, j) => {
        if (j > 0) {
          const plus = document.createElement('span');
          plus.className = 'sc-plus';
          plus.textContent = '+';
          combo.appendChild(plus);
        }
        const cap = document.createElement('kbd');
        cap.className = 'sc-key';
        cap.textContent = KEY_NAMES[k] || k.toUpperCase();
        combo.appendChild(cap);
      });
      box.appendChild(combo);
    };
    const mkScBox = (slot) => {
      const box = document.createElement('button');
      box.type = 'button';
      box.className = 'sc-box';
      box.title = '点击后按下想要的组合键（支持鼠标侧键、键盘组合、鼠标左右键同按）';
      box.dataset.slot = slot;
      renderScBox(box, shortcutSlot(slot));
      box.onclick = () => {
        if (captureCtl) captureCtl.cancel(); // 上一个会话（可能已挂死）直接作废
        const got = new Set();  // 本次录制**按过**的键（累积，不随松开删除——
                                // 落定时取它；曾只用"当前按住"集合，全部松开时
                                // 恰好清空，录出来永远是空组合，这就是录制不成功的根因）
        const held = new Set(); // 当前仍按住的键（清空即落定）
        let finished = false;
        captureMode = true;
        box.classList.add('rec');
        box.innerHTML = '';
        const tip = document.createElement('span');
        tip.className = 'sc-rec-tip';
        tip.textContent = '请按下组合键，松开后保存（Esc 取消）…';
        box.appendChild(tip);
        const ctl = {};
        captureCtl = ctl;
        const stop = () => {
          if (finished) return;
          finished = true;
          captureMode = false;
          document.removeEventListener('keydown', kd);
          document.removeEventListener('keyup', ku);
          document.removeEventListener('mousedown', md);
          document.removeEventListener('mouseup', mu);
          window.removeEventListener('blur', onBlur);
          clearTimeout(hardTimer);
          if (captureCtl === ctl) captureCtl = null;
          renderScBox(box, shortcutSlot(slot));
        };
        ctl.cancel = stop;
        const finalize = () => {
          if (finished) return;
          const keys = Array.from(got);
          stop();
          if (!keys.length) return;
          if (!validShortcut(keys)) {
            voiceToast('不支持这个组合：不能只用单独的鼠标左键或右键');
            return;
          }
          setShortcutSlot(slot, keys);
          firedLatch = false;
          cancelSolo();
          refreshShortcutPlaceholders();
          renderScBox(box, keys);
        };
        const kd = (e) => {
          if (e.key === 'Escape') { stop(); return; }
          const k = normKey(e);
          if (k) {
            if (!e.repeat) { got.add(k); held.add(k); }
            e.preventDefault();
          }
        };
        const ku = (e) => {
          const k = normKey(e);
          if (k) held.delete(k);
          if (held.size === 0) finalize();
        };
        const md = (e) => {
          const m = normMouse(e);
          if (m) { got.add(m); held.add(m); e.preventDefault(); }
        };
        const mu = (e) => {
          const m = normMouse(e);
          if (m) held.delete(m);
          if (held.size === 0) setTimeout(finalize, 60); // 键盘松完的收尾也走这里
        };
        const onBlur = () => stop();
        document.addEventListener('keydown', kd);
        document.addEventListener('keyup', ku);
        document.addEventListener('mousedown', md);
        document.addEventListener('mouseup', mu);
        window.addEventListener('blur', onBlur);
        const hardTimer = setTimeout(stop, 12000); // 无条件硬超时：不依赖 got 是否清空
      };
      return box;
    };
    const mainBox = mkScBox('main');
    const subBox = mkScBox('sub');
    const refreshAllScBoxes = () => {
      renderScBox(mainBox, shortcutSlot('main'));
      renderScBox(subBox, shortcutSlot('sub'));
    };
    row('主快捷键', mainBox);
    const subWrap = document.createElement('span');
    subWrap.className = 'sc-wrap';
    subWrap.appendChild(subBox);
    subWrap.appendChild(resetBtn);
    subWrap.appendChild(clearBtn);
    row('副快捷键', subWrap);
    note('主＝鼠标侧键（前后两枚都算）、副＝Alt 键，两个框各自点击即可**独立录制**'
      + '自定义组合（键盘单键/组合键、鼠标左右键同按；不支持单独的左键或右键）。'
      + '「清空」＝停用全部快捷键（只点按钮听写）；「恢复默认」＝主侧键、副 Alt。'
      + '仅在本窗口聚焦时生效，失焦自动松手。纯字母/数字单键在输入框里打字时不会'
      + '触发，加修饰键或换功能键即可。');

    resetBtn.onclick = () => {
      if (captureCtl) captureCtl.cancel();
      setShortcutSlot('main', null);
      setShortcutSlot('sub', null);
      firedLatch = false;
      cancelSolo();
      refreshShortcutPlaceholders();
      refreshAllScBoxes();
    };
    clearBtn.onclick = () => {
      if (captureCtl) captureCtl.cancel();
      setShortcutSlot('main', []);
      setShortcutSlot('sub', []);
      firedLatch = false;
      cancelSolo();
      refreshShortcutPlaceholders();
      refreshAllScBoxes();
    };
  }

  /* ================= 注册 ================= */

  CX.register({
    id: 'voice',

    init() {
      active = true;

      // 语音输入按钮（主对话与右栏子对话各一套）：按钮 DOM 在主程序 index.html 里
      //（位置与样式不变），默认带 .hidden——按钮属于本组件，由组件揭开并接管行为；
      // 组件关掉/没加载 = 按钮整组不出现。远程手机端（JX_REMOTE 且视口 <500）
      // 不揭开听筒按钮（手机浏览器多不支持语音识别；resize 跨过阈值时即时显隐）。
      // Electron 壳（JX_ELECTRON，app-core.js 按 URL 参数置位）同样不揭开：
      // Web Speech API 在 Electron 没有免费识别通道（缺浏览器厂商标识），
      // 听写会直接 network 失败——等本地 ASR（SenseVoice 路线）落地再放开。
      for (const view of [mainView, subView]) {
        const b = viewEl(view, 'btn-voice');
        if (!b) continue;
        voiceCtl[view.key] = bindVoiceButton(view);
        if (!micHiddenByEnv() && !window.JX_ELECTRON) b.classList.remove('hidden');
      }

      // 朗读：助手消息操作行的按钮 + 会话切换/回复结束时停止朗读（后端通道，免配置）
      msgAction = onMessageAction;
      JX.registerMessageAction(msgAction);
      const onOpen = () => stopSpeak();
      const onDone = () => stopSpeak();
      busHooks.push({ ev: 'session:open', fn: onOpen }, { ev: 'message:done', fn: onDone });
      for (const h of busHooks) JX.on(h.ev, h.fn);

      // 快捷键引擎 + 输入框占位灰字的语音提示
      installShortcutEngine();
      refreshShortcutPlaceholders();

      // 发送前先停听写：app-core 的回车发送路径调用（同步落定灰字后硬收尾，
      // 让随说的内容随本条消息一起发出）
      window.JXVoicePause = () => {
        for (const k of ['main', 'sub']) {
          const c = voiceCtl[k];
          if (c && c.pauseForSend) c.pauseForSend();
        }
      };

      // 设置区以独立分区页呈现（设置 → 语音，位于「内置组件」之后）：
      // pane 字段交给 app-settings.js 动态挂导航项与页面容器，组件关掉即撤
      CX.registerSettings({
        id: 'voice',
        title: '语音（朗读与听写）',
        pane: {
          label: '语音',
          icon: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M11 5L6 9H2v6h4l5 4V5z"/><path d="M15.5 8.5a5 5 0 0 1 0 7"/><path d="M18.5 5.5a9 9 0 0 1 0 13"/></svg>',
        },
        mount: mountSettings,
      });
    },

    dispose() {
      active = false;

      // 快捷键引擎与占位提示收尾
      removeShortcutEngine();
      try { delete window.JXVoicePause; } catch (e) { window.JXVoicePause = undefined; }
      window.JXVoiceHint = '';
      if (typeof updateSendKeyHint === 'function') updateSendKeyHint();
      restoreSubPlaceholder();

      // 听写：会话收尾 + 按钮隐藏（按钮归组件：卸载后整组消失，等下次启用再揭开）
      for (const view of [mainView, subView]) {
        const key = view.key;
        const sess = voiceSess[key];
        if (sess && sess.teardown) sess.teardown();
        voiceSess[key] = null;
        voiceCtl[key] = null;
        voiceOn[key] = false;
        const b = viewEl(view, 'btn-voice');
        if (b) {
          b.classList.add('hidden');
          b.classList.remove('on', 'connecting');
          b.onclick = null;
          if (view.hint) view.hint.textContent = '';
        }
      }

      // 朗读：停止播放、摘掉已渲染的朗读按钮、注销钩子
      stopSpeak();
      document.querySelectorAll('.act-tts').forEach((b) => b.remove());
      if (msgAction && JX.unregisterMessageAction) JX.unregisterMessageAction(msgAction);
      msgAction = null;
      if (JX.off) for (const h of busHooks) JX.off(h.ev, h.fn);
      busHooks.length = 0;

      CX.unregisterSettings('voice');
    },
  });
})();
