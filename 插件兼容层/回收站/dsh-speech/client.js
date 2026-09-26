/* dsh-speech 浏览器半边适配（坚祥数字助理 · 插件兼容层）
 *
 * 转译自 DSH 插件 dsh-speech（原件在 插件/dsh-speech/，未改动）：
 * - TTS 朗读：speechSynthesis + cleanForSpeech 清洗（围栏代码块/反引号/链接/
 *   标题/列表/表格竖线/强调符 → 纯散文），音色选择（指定 URI → zh-CN → zh），
 *   单飞播放：点新朗读立即停上一条，播放中的按钮变「停止」。
 * - 听写（浏览器层）：SpeechRecognition 连续识别，final 结果追加进输入框
 *   （不覆盖用户编辑，去重窗口消化引擎重放；全角逗号→1空格、句号→2空格）；
 *   Enter 发送或切换会话时静默停止。
 * - 润色：把输入框文本交 Go 半边端点做保守纠错（同音字/标点），失败保留原文。
 * - 设置区：语速/音色/试听/润色提示词/常用词表，存 localStorage。
 * 未适配（见 README.md）：本地离线 SenseVoice、云端 ASR、热键。
 */
(function () {
  if (!window.JX) return;

  /* ---------- 偏好（localStorage，命名空间 dsh-speech） ---------- */
  const pref = {
    get(k, d) {
      try { const v = localStorage.getItem('dsh-speech:' + k); return v === null ? d : JSON.parse(v); }
      catch (e) { return d; }
    },
    set(k, v) { try { localStorage.setItem('dsh-speech:' + k, JSON.stringify(v)); } catch (e) { /* 忽略 */ } },
  };

  const synth = 'speechSynthesis' in window ? window.speechSynthesis : null;

  const ICON = {
    speak: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M11 5L6 9H2v6h4l5 4V5z"/><path d="M15.5 8.5a5 5 0 0 1 0 7"/><path d="M18.5 5.5a9 9 0 0 1 0 13"/></svg>朗读',
    stop: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M11 5L6 9H2v6h4l5 4V5z"/><line x1="16" y1="9" x2="22" y2="15"/><line x1="22" y1="9" x2="16" y2="15"/></svg>停止',
    mic: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 1a3 3 0 0 0-3 3v8a3 3 0 0 0 6 0V4a3 3 0 0 0-3-3z"/><path d="M19 10v2a7 7 0 0 1-14 0v-2"/><line x1="12" y1="19" x2="12" y2="23"/></svg>',
  };

  /* ---------- TTS：清洗与朗读（转译自 speech-controller.ts / cleanForSpeech） ---------- */

  function cleanForSpeech(src) {
    let s = String(src || '');
    s = s.replace(/```[\s\S]*?```/g, '（代码）');
    s = s.replace(/`([^`]*)`/g, '$1');
    s = s.replace(/!\[([^\]]*)\]\([^)]*\)/g, '$1');
    s = s.replace(/\[([^\]]*)\]\([^)]*\)/g, '$1');
    s = s.replace(/^#{1,6}\s+/gm, '');
    s = s.replace(/^\s*[-*+]\s+/gm, '');
    s = s.replace(/^\s*>\s?/gm, '');
    s = s.replace(/\|/g, ' ');
    s = s.replace(/[*_~]{1,3}/g, '');
    s = s.replace(/\r/g, '');
    s = s.replace(/\n{2,}/g, '。').replace(/\n/g, ' ');
    return s.replace(/\s{2,}/g, ' ').trim();
  }

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

  let playingBtn = null;

  function stopSpeak() {
    if (synth) synth.cancel();
    if (playingBtn) {
      playingBtn.classList.remove('playing');
      playingBtn.innerHTML = ICON.speak;
      playingBtn.title = '朗读这条消息';
      playingBtn = null;
    }
  }

  function speak(text, btn) {
    if (playingBtn === btn) { stopSpeak(); return; }
    stopSpeak(); // 单飞：新朗读立即停掉上一条
    const u = new SpeechSynthesisUtterance(text);
    pickVoice(u);
    u.rate = pref.get('rate', 1.3);
    u.onend = u.onerror = () => {
      btn.classList.remove('playing');
      btn.innerHTML = ICON.speak;
      btn.title = '朗读这条消息';
      if (playingBtn === btn) playingBtn = null;
    };
    playingBtn = btn;
    btn.classList.add('playing');
    btn.innerHTML = ICON.stop;
    btn.title = '停止朗读';
    synth.speak(u);
  }

  if (synth) {
    JX.registerMessageAction((row, m) => {
      if (m.role !== 'assistant') return;
      const text = cleanForSpeech(m.content);
      if (!text) return;
      const btn = document.createElement('button');
      btn.className = 'act-copy act-tts';
      btn.innerHTML = ICON.speak;
      btn.title = '朗读这条消息';
      btn.onclick = () => speak(text, btn);
      row.appendChild(btn);
    });
    JX.on('session:open', stopSpeak);
    JX.on('message:done', stopSpeak);
  }

  /* ---------- 听写（浏览器层，转译自 MicButton 的 final 追加 + 去重） ---------- */

  const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
  let rec = null;
  let recActive = false;
  let lastFinal = '';
  let micBtn = null;
  let polishWrap = null;

  function notifyActive() {
    fetch('/api/plugins/dsh-speech/voice-active', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}',
    }).catch(() => { /* 提示段尽力而为 */ });
  }

  function appendToInput(clean) {
    const input = document.getElementById('input');
    if (!input) return;
    input.value = (input.value ? input.value.replace(/\s+$/, '') + ' ' : '') + clean.trim();
    input.dispatchEvent(new Event('input', { bubbles: true }));
  }

  function startMic() {
    const input = document.getElementById('input');
    if (!input) return;
    rec = new SR();
    rec.lang = navigator.language || 'zh-CN';
    rec.continuous = true;
    rec.interimResults = true;
    rec.onresult = (e) => {
      let finalText = '';
      for (let i = e.resultIndex; i < e.results.length; i++) {
        if (e.results[i].isFinal) finalText += e.results[i][0].transcript;
      }
      if (!finalText) return;
      // 全角标点清洗（仅听写文本，保护数字/英文），与 DSH 同款
      const clean = finalText.replace(/，/g, ' ').replace(/。/g, '  ').trim();
      if (!clean || clean === lastFinal) return; // 去重窗口：引擎重放上一段
      lastFinal = clean;
      appendToInput(clean);
    };
    rec.onend = () => {
      if (recActive) { try { rec.start(); } catch (e) { stopMic(); } } // 引擎自停则续听
    };
    rec.onerror = (e) => {
      if (e.error === 'not-allowed' || e.error === 'service-not-allowed') stopMic();
    };
    try { rec.start(); } catch (e) { return; }
    recActive = true;
    lastFinal = '';
    if (micBtn) { micBtn.classList.add('active'); micBtn.title = '停止听写'; }
    if (polishWrap) polishWrap.classList.remove('hidden');
    notifyActive();
  }

  function stopMic() {
    recActive = false;
    try { if (rec) rec.stop(); } catch (e) { /* 已停 */ }
    if (micBtn) { micBtn.classList.remove('active'); micBtn.title = '语音输入（浏览器听写）'; }
    if (polishWrap) polishWrap.classList.add('hidden');
  }

  /* ---------- 润色（转写文本交 Go 半边做保守纠错） ---------- */

  function polishNote(text, ms) {
    const n = polishWrap && polishWrap.querySelector('.pb-note');
    if (!n) return;
    n.textContent = text;
    if (ms) setTimeout(() => { if (n.textContent === text) n.textContent = ''; }, ms);
  }

  async function runPolish() {
    const btn = polishWrap && polishWrap.querySelector('.pb-btn');
    const input = document.getElementById('input');
    const text = input && input.value.trim();
    if (!btn || !text) return;
    btn.disabled = true;
    btn.textContent = '润色中…';
    try {
      const res = await fetch('/api/plugins/dsh-speech/polish', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ text, prompt: pref.get('polishPrompt', ''), words: pref.get('polishWords', '') }),
      });
      const data = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(data.error || res.statusText);
      input.value = data.text;
      input.dispatchEvent(new Event('input', { bubbles: true }));
      polishNote('已润色', 2500);
    } catch (e) {
      polishNote('失败：' + e.message, 6000); // 失败保留原文（对齐 DSH）
    }
    btn.disabled = false;
    btn.textContent = '✎ 润色';
  }

  if (SR) {
    micBtn = document.createElement('button');
    micBtn.className = 'mic-btn';
    micBtn.title = '语音输入（浏览器听写）';
    micBtn.innerHTML = ICON.mic;
    micBtn.onclick = () => (recActive ? stopMic() : startMic());
    JX.addComposerControl(micBtn);

    polishWrap = document.createElement('div');
    polishWrap.className = 'polish-bubble hidden';
    polishWrap.innerHTML = '<button class="pb-btn" title="把输入框内容交本机模型做保守纠错（同音字/标点）">✎ 润色</button><span class="pb-note"></span>';
    polishWrap.querySelector('.pb-btn').onclick = runPolish;
    const cw = document.getElementById('composer-wrap');
    if (cw) cw.appendChild(polishWrap);

    JX.on('send', stopMic);       // Enter 发送时静默停止并随草稿发出
    JX.on('session:open', stopMic);
  }

  /* ---------- 设置区（转译自 SpeechSettingsSection，偏好存 localStorage） ---------- */

  JX.registerSettingsSection({
    title: '语音（dsh-speech 适配）',
    mount(el) {
      el.innerHTML = '';

      if (!synth && !SR) {
        el.innerHTML = '<p class="sp-note">此浏览器不支持语音合成与语音识别，本插件功能不可用。</p>';
        return;
      }

      const row = (label, ctrl) => {
        const r = document.createElement('label');
        r.className = 'sp-row';
        const b = document.createElement('span');
        b.className = 'sp-label';
        b.textContent = label;
        r.appendChild(b);
        r.appendChild(ctrl);
        el.appendChild(r);
        return r;
      };

      // 语速
      if (synth) {
        const rate = document.createElement('input');
        rate.type = 'range';
        rate.min = '0.5'; rate.max = '3'; rate.step = '0.1';
        rate.value = String(pref.get('rate', 1.3));
        const lab = document.createElement('span');
        lab.className = 'sp-value';
        lab.textContent = rate.value + '×';
        rate.oninput = () => { lab.textContent = rate.value + '×'; pref.set('rate', parseFloat(rate.value)); };
        const r = row('朗读语速', rate);
        r.appendChild(lab);

        // 音色
        const sel = document.createElement('select');
        const fillVoices = () => {
          const vs = synth.getVoices().slice().sort((a, b) => {
            const az = /^zh/i.test(a.lang || '') ? 0 : 1;
            const bz = /^zh/i.test(b.lang || '') ? 0 : 1;
            return az - bz;
          });
          sel.innerHTML = '<option value="">自动（优先中文）</option>';
          for (const v of vs) {
            const o = document.createElement('option');
            o.value = v.voiceURI;
            o.textContent = v.name + '（' + v.lang + '）';
            sel.appendChild(o);
          }
          sel.value = pref.get('voice', '');
        };
        fillVoices();
        if (synth.onvoiceschanged !== undefined) synth.onvoiceschanged = fillVoices;
        sel.onchange = () => pref.set('voice', sel.value);
        row('朗读音色', sel);

        // 试听
        const t = document.createElement('button');
        t.className = 'sp-btn';
        t.textContent = '试听';
        t.onclick = () => {
          const u = new SpeechSynthesisUtterance('你好，这是坚祥数字助理的语音试听。');
          pickVoice(u);
          u.rate = pref.get('rate', 1.3);
          synth.cancel();
          synth.speak(u);
        };
        row('', t);
      } else {
        const n = document.createElement('p');
        n.className = 'sp-note';
        n.textContent = '此浏览器不支持 speechSynthesis，朗读不可用；听写功能不受影响。';
        el.appendChild(n);
      }

      if (!SR) {
        const n = document.createElement('p');
        n.className = 'sp-note';
        n.textContent = '此浏览器不支持 SpeechRecognition（建议 Chrome / Edge），听写不可用。';
        el.appendChild(n);
      }

      // 润色提示词
      const ta = document.createElement('textarea');
      ta.className = 'sp-area';
      ta.rows = 3;
      ta.placeholder = '留空使用内置默认（只修同音错字与标点，不改写句意）';
      ta.value = pref.get('polishPrompt', '');
      ta.oninput = () => pref.set('polishPrompt', ta.value);
      row('润色提示词', ta);
      const rst = document.createElement('button');
      rst.className = 'sp-btn';
      rst.textContent = '恢复默认';
      rst.onclick = () => { ta.value = ''; pref.set('polishPrompt', ''); };
      row('', rst);

      // 常用词表
      const wt = document.createElement('textarea');
      wt.className = 'sp-area';
      wt.rows = 2;
      wt.placeholder = '顿号或换行分隔，如：坚祥、RustDesk、软著';
      wt.value = pref.get('polishWords', '');
      wt.oninput = () => pref.set('polishWords', wt.value);
      row('润色常用词', wt);
    },
  });
})();
