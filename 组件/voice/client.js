/* 组件：语音（朗读与听写） —— 组件/voice/client.js
 *
 * 三块能力，都是纯浏览器半边（本组件没有宿主代码、没有后端接口）：
 *
 * 1) 听写：输入区右下角的语音输入按钮（#btn-voice / #sub-btn-voice，主对话与右栏
 *    子对话各一套）。**这套实现在本次改动前属于基础架构（web/app-chat.js），现在
 *    整体迁到本组件**——逻辑逐行保持原样：连接中/录音中三态、灰色暂定层（未定稿
 *    转写叠在输入框上显示为灰字、静置 2.5 秒落成正文）、断线退避重连、时长上限
 *    倒计时徽标、一次性轻提示。按钮 DOM 仍在 web/index.html（位置与样式不变），
 *    只是默认带 .hidden——由本组件接管显示与行为，组件关掉按钮就不出现。
 *
 * 2) 朗读：助手消息操作行的「朗读 / 停止」按钮（Web Speech TTS）。实现参考旧插件
 *    dsh-speech（原件与转译版见 插件/.回收站/dsh-speech、插件兼容层/回收站/dsh-speech）：
 *    cleanForSpeech 清洗 markdown（代码块/反引号/链接/标题/列表/表格竖线/强调符）、
 *    音色挑选（指定 URI → zh-CN → zh）、单飞播放（点新朗读停上一条、播放中变停止）。
 *
 * 3) 设置：设置 → 内置组件 → 「语音（朗读与听写）」下的偏好区（朗读语速 / 音色 /
 *    试听 / 单次听写时长上限），偏好存 localStorage（命名空间 cx:voice）。设置区经
 *    CX.registerSettings 注册，组件关掉时一并消失。
 *
 * 热开关：设置页里关掉本组件 → dispose() 完整撤销（按钮隐藏、识别停止、朗读停止、
 * 消息操作行钩子注销、设置区消失），不需要刷新页面。
 */
(function () {
  if (!window.CX) return;

  const synth = 'speechSynthesis' in window ? window.speechSynthesis : null;
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

  // 朗读语速（0.5–3，默认 1.3 为 WebView2 实测调校值）
  function spokenRate() {
    const v = Number(pref.get('rate', 1.3));
    return isFinite(v) ? Math.min(3, Math.max(0.5, v)) : 1.3;
  }
  // 单次听写时长上限（秒）：原先写死在基础架构里的 4 分钟，现在可在设置区调
  function maxDictateSec() {
    const v = Number(pref.get('maxSec', 240));
    return isFinite(v) && v >= 30 ? Math.min(600, v) : 240;
  }

  const ICON = {
    speak: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M11 5L6 9H2v6h4l5 4V5z"/><path d="M15.5 8.5a5 5 0 0 1 0 7"/><path d="M18.5 5.5a9 9 0 0 1 0 13"/></svg>',
    stop: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M11 5L6 9H2v6h4l5 4V5z"/><line x1="16" y1="9" x2="22" y2="15"/><line x1="22" y1="9" x2="16" y2="15"/></svg>',
  };

  /* ================= 一、朗读（TTS） ================= */

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

  /** 给一条 utterance 挑音色：设置里指定的 URI → 中文（zh-CN 优先）→ 引擎默认。 */
  function pickVoice(u) {
    if (!synth) return;
    const uri = pref.get('voice', '');
    const vs = synth.getVoices();
    const v = (uri && vs.find((x) => x.voiceURI === uri)) ||
      vs.find((x) => /^zh[-_]cn/i.test(x.lang || '')) ||
      vs.find((x) => /^zh/i.test(x.lang || ''));
    if (v) u.voice = v;
    else u.lang = 'zh-CN';
  }

  let playingBtn = null; // 正在朗读的那条消息的按钮（单飞：同时只有一条在播）

  function restBtn(btn) {
    btn.classList.remove('playing');
    btn.innerHTML = ICON.speak + '朗读';
    btn.title = '朗读这条消息';
  }

  function stopSpeak() {
    if (synth) synth.cancel();
    if (playingBtn) { restBtn(playingBtn); playingBtn = null; }
  }

  function speakText(text, btn) {
    if (playingBtn === btn) { stopSpeak(); return; } // 再点一次 = 停止
    stopSpeak(); // 单飞：新的朗读立即停掉上一条
    const u = new SpeechSynthesisUtterance(text);
    pickVoice(u);
    u.rate = spokenRate();
    u.onend = u.onerror = () => {
      restBtn(btn);
      if (playingBtn === btn) playingBtn = null;
    };
    playingBtn = btn;
    btn.classList.add('playing');
    btn.innerHTML = ICON.stop + '停止';
    btn.title = '停止朗读';
    synth.speak(u);
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

  /* ================= 二、听写（语音输入） =================
     以下整段迁自基础架构 web/app-chat.js 的语音输入块（本次改动前它是主程序的一部分），
     除「时长上限改读设置」外逻辑未动。四个层次：
     1) 连接：点麦克风先进「连接中」——按钮转圈，不算开始说话。连上（onstart）才
        变蓝进入录音态；VOICE_CONNECT_TIMEOUT 之内连不上就退避重试，重试 VOICE_MAX_RETRY
        次仍连不上才弹报错。首次连接往往比后续慢（要跟云端服务建链），所以这里
        的等待给得比"点了没反应"的看门狗宽，且**不打断正在建立的连接**。
     2) 识别：浏览器原生 Web Speech（Edge/Chrome）。音频由浏览器直接发给云端识别
        服务，链路抖动、静音收尾、引擎自行结束都很常见 → 录音态掉线同样退避重连。
     3) 灰字：转写先落在叠层上显示为灰字——它不在输入框的值里，所以不可编辑、
        不可选中；静置 VOICE_SETTLE_MS（2.5 秒）后落成黑色正文写进输入框，此后
        与手打文字一样可编辑。断线收尾时把已有灰字立即落定，避免白说。
     4) 节流：单次听写最长 maxDictateSec()（默认 240 秒），最后 9 秒在麦克风按钮上出倒计时徽标。 */

  const voiceOn = { main: false, sub: false };
  const voiceSess = { main: null, sub: null };
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

  /** 一次性轻提示（语音连不上等），浮在输入区上方，不打断输入。 */
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
      if (text !== '') {
        const inp = view.input;
        inp.value = voiceJoin(inp.value, text);
        autoGrow(inp);
        // 语音是程序化写入，不触发 input 事件：手动落一次当前上下文的草稿
        if (view === mainView) persistMainDraftLS(); else persistSubDraftLS();
      }
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
        // 带上最后一次的错误码（network / no-speech / aborted / language-not-supported…）：
        // 桌面端（WebView2）排查时，这一小段就是唯一的现场线索
        finish('语音识别已停止', '语音识别连接不稳定（网络），连续 ' + VOICE_MAX_RETRY + ' 次连不上，已停止听写' +
          (sess.lastErr ? '（' + sess.lastErr + '）' : ''));
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
        if (code === 'not-allowed' || code === 'service-not-allowed') { finish('麦克风未授权：请在浏览器里允许麦克风权限'); return; }
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
      if (!SR) { setHint('当前浏览器不支持语音识别', 2500); return; }
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
      btn.onclick = null;
      voiceOn[key] = false;
    };
  }

  /* ================= 三、设置区（CX.registerSettings） ================= */

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
    // 一行：标签 + 控件（控件右侧可再跟一个值标签）
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

    if (!synth && !SR) {
      note('此浏览器不支持语音合成（朗读）与语音识别（听写），本组件不可用。');
      return;
    }

    /* ----- 朗读 ----- */
    if (synth) {
      group('朗读');

      // 语速：滑块 + 当前倍率
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

      // 音色：自动（优先中文）+ 系统语音清单
      const sel = document.createElement('select');
      sel.className = 'sp-select';
      const fillVoices = () => {
        const vs = synth.getVoices().slice().sort((a, b) => {
          const az = /^zh/i.test(a.lang || '') ? 0 : 1;
          const bz = /^zh/i.test(b.lang || '') ? 0 : 1;
          if (az !== bz) return az - bz;
          return String(a.name || '').localeCompare(String(b.name || ''));
        });
        sel.innerHTML = '<option value="">自动（优先中文）</option>';
        for (const v of vs) {
          const o = document.createElement('option');
          o.value = v.voiceURI;
          o.textContent = v.name + '（' + v.lang + '）';
          sel.appendChild(o);
        }
        sel.value = pref.get('voice', '');
        // 选中的音色若已不在系统清单里，值落不到任何 option 上，浏览器会自动回落到「自动」
      };
      fillVoices();
      // 语音清单可能首帧后才到：跟随引擎的变化信号刷新（设置区已关掉就自我注销）
      const onVoices = () => {
        if (!el.isConnected) { synth.removeEventListener('voiceschanged', onVoices); return; }
        fillVoices();
      };
      if (synth.addEventListener) synth.addEventListener('voiceschanged', onVoices);
      sel.onchange = () => pref.set('voice', sel.value);
      row('朗读音色', sel);

      // 试听：播放中变「停止」
      const test = btn('试听', '用当前语速与音色试听一句');
      let testing = false;
      const setTesting = (on) => { testing = on; test.textContent = on ? '停止' : '试听'; };
      test.onclick = () => {
        if (testing) { synth.cancel(); setTesting(false); return; }
        const u = new SpeechSynthesisUtterance('你好，这是坚祥数字助理的语音试听。');
        pickVoice(u);
        u.rate = spokenRate();
        u.onend = u.onerror = () => setTesting(false);
        synth.cancel();
        setTesting(true);
        synth.speak(u);
      };
      row('试听', test);
    } else {
      note('此浏览器不支持 speechSynthesis，朗读不可用；听写不受影响。');
    }

    /* ----- 听写 ----- */
    if (SR) {
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
      note('听写用浏览器自带的语音识别（Edge / Chrome）：音频由浏览器发给它的识别服务，' +
        '链路抖动会自动退避重连；输入框上方浮出的灰字是未定稿的转写，停下约 2.5 秒才落成正文。');
    } else {
      note('此浏览器不支持 SpeechRecognition（听写不可用，建议 Edge / Chrome）。');
    }
  }

  /* ================= 注册 ================= */

  CX.register({
    id: 'voice',

    init() {
      active = true;

      // 语音输入按钮（主对话与右栏子对话各一套）：按钮 DOM 在主程序 index.html 里
      //（位置与样式不变），默认带 .hidden——按钮属于本组件，由组件揭开并接管行为；
      // 组件关掉/没加载 = 按钮整组不出现
      for (const view of [mainView, subView]) {
        const b = viewEl(view, 'btn-voice');
        if (!b) continue;
        b.classList.remove('hidden');
        bindVoiceButton(view);
      }

      // 朗读：助手消息操作行的按钮 + 会话切换/回复结束时停止朗读
      if (synth) {
        msgAction = onMessageAction;
        JX.registerMessageAction(msgAction);
        const onOpen = () => stopSpeak();
        const onDone = () => stopSpeak();
        busHooks.push({ ev: 'session:open', fn: onOpen }, { ev: 'message:done', fn: onDone });
        for (const h of busHooks) JX.on(h.ev, h.fn);
      }

      CX.registerSettings({ id: 'voice', title: '语音（朗读与听写）', mount: mountSettings });
    },

    dispose() {
      active = false;

      // 听写：会话收尾 + 按钮隐藏（按钮归组件：卸载后整组消失，等下次启用再揭开）
      for (const view of [mainView, subView]) {
        const key = view.key;
        const sess = voiceSess[key];
        if (sess && sess.teardown) sess.teardown();
        voiceSess[key] = null;
        voiceOn[key] = false;
        const b = viewEl(view, 'btn-voice');
        if (b) {
          b.classList.add('hidden');
          b.classList.remove('on', 'connecting');
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
