/* 语音组件朗读链竞态桩测 —— 纯 node，不用起服务。
 *
 * 背景（2026-11-03 用户报障）：桌面客户端（WebView2）「有时点朗读键没声音 / 朗读中断」，
 * 远程端正常。根因定位在 组件/voice/client.js 的 ttsPlay：旧链 play() 在 pending 中被
 * stopAudio() 的 pause() 打断时，浏览器会以 AbortError 异步 reject 该 play promise；
 * 这个 reject 排队成微任务，执行时新一条 ttsPlay 往往已启动（用户点停止后立刻再点朗读），
 * 旧链 catch 里无守卫的 stopAudio() 会把新链的 ttsAbort / ttsStopChain 一并杀掉——
 * 新链预取被 abort、整条安静落定，表现为「点了朗读没声音」。桌面端鼠标连点（几百毫秒内
 * 完成停止→再点）最易踩中 pending 窗口；手机远程端两次点击间隔长，踩不中。
 *
 * 本文件复刻修复前/后两版 ttsPlay 核心（口径标注 client.js 行号），用可控行动器还原
 * 浏览器微任务时序，断言：修复前新链被误杀、修复后新链正常出声。
 */
'use strict';

// ---------- 桩环境：URL / Audio / fetch ----------
let urlSeq = 0;
const revoked = [];
const URL = {
  createObjectURL: () => 'blob:fake-' + (++urlSeq),
  revokeObjectURL: (u) => revoked.push(u),
};

/** 模拟 <audio>：play() 返回 pending promise；pause() 若在 play 未落定前调用，
 * 以 AbortError reject 它——与 Chromium/WebView2 的真实行为一致。 */
class FakeAudio {
  constructor() { this.src = ''; this.onended = null; this.onerror = null; this._playP = null; }
  play() {
    this._playP = new Promise((res, rej) => { this._res = res; this._rej = rej; });
    return this._playP;
  }
  pause() {
    if (this._playP && this._rej) { const r = this._rej; this._res = this._rej = null; r(new Error('AbortError')); }
  }
  removeAttribute() { this.src = ''; }
  load() {}
  // 测试侧模拟「音频真的开始播了」（play promise 正常落定）
  started() { if (this._res) { const r = this._res; this._res = this._rej = null; r(); } }
}

const flush = () => new Promise((r) => setTimeout(r, 0));

/** 跑一版 ttsPlay 逻辑（fixed=修复后），还原用户时序：点朗读 A → A 首块 play() pending →
 * 点停止 → 立刻再点朗读 B → 微任务里旧链 play reject 落地。返回观测结果。 */
async function runScenario(fixed) {
  // —— 与 client.js 同构的全局态（模块级单变量）——
  let playEl = null, playURL = null, ttsAbort = null, ttsStopChain = null;
  const plays = [];               // 每次 play() 的观测（src + 归属链标记）
  let curTag = '?';               // 当前启动的链标记（A/B），观测 play 归属用
  const fetches = {};             // tag → controllable resolve

  function stopAudio() {
    if (ttsAbort) { ttsAbort.abort(); ttsAbort = null; }
    if (ttsStopChain) { try { ttsStopChain(); } catch (e) {} ttsStopChain = null; }
    if (playEl) {
      playEl.onended = playEl.onerror = null;
      playEl.pause();
      playEl.removeAttribute('src');
      try { playEl.load(); } catch (e) {}
    }
    if (playURL) { URL.revokeObjectURL(playURL); playURL = null; }
  }

  function ttsFetchBlob(tag) { // tag 仅桩测观测用
    return new Promise((res, rej) => { fetches[tag] = { res, rej }; });
  }

  // 复刻 client.js ttsPlay（625-666 行；fixed=修复后版本，662 行前的守卫差异处标注）
  function ttsPlay(text, tag) {
    stopAudio();
    ttsAbort = new AbortController();
    const signal = ttsAbort.signal;
    const chunks = text; // 桩测直接给块数组
    const blobs = chunks.map(() => null);
    let idx = 0, stopped = false;
    const prefetch = () => {
      for (let i = idx; i < Math.min(idx + 3, chunks.length); i++) {
        if (!blobs[i]) blobs[i] = ttsFetchBlob(tag + '#' + i);
      }
    };
    prefetch();
    return new Promise((resolve, reject) => {
      const playChunk = () => {
        if (stopped) return;
        if (idx >= chunks.length) { stopAudio(); resolve(); return; }
        const i = idx;
        blobs[i].then((blob) => {
          if (stopped || signal.aborted) return;
          playURL = URL.createObjectURL(blob);
          if (!playEl) playEl = new FakeAudio();
          playEl.src = playURL;
          playEl.onended = () => {
            URL.revokeObjectURL(playURL); playURL = null;
            idx += 1; prefetch(); playChunk();
          };
          if (fixed) { // —— 修复：旧链收尾回调先验本链（client.js 现行实现）——
            const gone = () => stopped || signal.aborted;
            playEl.onerror = () => { if (gone()) return; stopAudio(); reject(new Error('播放失败')); };
            playEl.play().catch((e) => { if (gone()) return; stopAudio(); reject(e); });
          } else {     // —— 修复前：无守卫（旧实现）——
            playEl.onerror = () => { stopAudio(); reject(new Error('播放失败')); };
            playEl.play().catch((e) => { stopAudio(); reject(e); });
          }
          plays.push({ tag, src: playURL });
        }).catch((e) => {
          if (signal.aborted || stopped) return;
          stopAudio();
          reject(e);
        });
      };
      ttsStopChain = () => { stopped = true; resolve(); };
      playChunk();
    });
  }

  const obs = { plays, bAborted: null, aSettled: false, bSettled: false, bResult: 'pending' };

  // ===== 用户时序 =====
  // 1) 点朗读 A（一块短文本），首块 fetch 立刻完成，play() 挂起 pending（WebView2 起声前的窗口）
  curTag = 'A';
  const pA = ttsPlay(['第一句。'], 'A');
  pA.then(() => { obs.aSettled = true; }).catch(() => { obs.aSettled = true; });
  fetches['A#0'].res(new Blob(['mp3-a']));
  await flush(); // 进入 then 回调：src 已设、play() 已调且 pending

  // 2) 用户点「停止」：stopSpeak → stopAudio（pause 使 A 的 play promise 排队 reject——微任务）
  stopAudio();

  // 3) 用户【立刻】再点朗读 B（同一同步段启动，先于旧 play reject 微任务执行——与真实一致）
  curTag = 'B';
  const pB = ttsPlay(['第二句。'], 'B');
  pB.then(() => { obs.bSettled = true; obs.bResult = 'resolved'; })
    .catch((e) => { obs.bSettled = true; obs.bResult = 'rejected: ' + e.message; });

  // 4) 微任务落 地：旧链 A 的 play() AbortError reject 此刻才执行（修复前会在此杀 B）
  await flush();
  obs.bAborted = ttsAbort ? ttsAbort.signal.aborted : null; // ttsAbort 此刻归 B：aborted 即被误杀

  // 5) B 的首块 fetch 正常完成（后端无恙）——修复后 B 应开播；修复前 B 已被停
  if (fetches['B#0']) fetches['B#0'].res(new Blob(['mp3-b']));
  await flush();
  if (playEl && playEl._res) playEl.started(); // 音频设备就绪，B 的 play() 落定
  await flush();
  return obs;
}

/** 场景二：settle 按钮态竞态。连点「停止→立刻再点朗读」时新旧链的按钮与 seq
 * 完全相同（同一条消息）：旧链 speakTextBackend 的 settle 微任务迟到于新链同步段，
 * 旧守卫（playingBtn/playingSeq 比对）失配——把新链刚挂的「停止」态摘掉，朗读在播、
 * 按钮却显示「朗读」，用户再点一次即把在播链当「再点停止」砍掉（观感=朗读中断）。
 * 修复（ttsGen 链代数）后旧链 settle 按 gen 辨认让路。fixed=修复后版。 */
async function runSettleScenario(fixed) {
  let playingBtn = null, playingSeq = null, ttsGen = 0;
  const makeBtn = (seq) => ({ seq, playing: false });
  const restBtn = (b) => { b.playing = false; };

  function stopSpeak() {
    if (playingBtn) { restBtn(playingBtn); playingBtn = null; }
    playingSeq = null;
  }

  function speakTextBackend(text, btn, chainP) { // chainP: 本链 ttsPlay 的 promise（停止即落定；在播则挂起）
    const mySeq = btn.seq;
    if (fixed) {
      const gen = ++ttsGen;
      playingBtn = btn; playingSeq = mySeq; btn.playing = true;
      const settle = () => {
        if (gen !== ttsGen) return; // 修复：只认链代数
        if (playingBtn) restBtn(playingBtn);
        playingBtn = null; playingSeq = null;
      };
      return Promise.resolve(chainP).then(() => { settle(); });
    } else {
      playingBtn = btn; playingSeq = mySeq; btn.playing = true;
      const settle = () => {
        if (playingBtn !== btn && playingSeq !== mySeq) return; // 旧守卫：按钮+seq 比对
        if (playingBtn) restBtn(playingBtn);
        playingBtn = null; playingSeq = null;
      };
      return Promise.resolve(chainP).then(() => { settle(); });
    }
  }

  const btn = makeBtn(14); // 同一条消息的同一个按钮
  // 1) 链 A 播放中（playing ✓），用户点停止：stopSpeak 复位 + 链 A（立即落定）settle 排队微任务
  const pA = speakTextBackend('甲', btn, Promise.resolve());
  stopSpeak();
  // 2) 用户立刻再点朗读：链 B 同步段把按钮挂回「停止」态，链 B 在播（promise 挂起不落定）
  const pB = speakTextBackend('乙', btn, new Promise(() => {}));
  // 3) 微任务落地：链 A 的迟到 settle 执行（链 B 的 settle 因 promise 挂起不会跑）
  await pA; await new Promise((r) => setTimeout(r, 0));
  return { btnPlaying: btn.playing, chainBActive: playingBtn === btn, pB };
}

(async () => {
  let fail = 0;
  const t = (name, ok, detail) => {
    console.log((ok ? '  ok  ' : '  FAIL') + ' ' + name + (ok ? '' : '  <- ' + detail));
    if (!ok) fail++;
  };

  const before = await runScenario(false);
  console.log('[修复前] 场景一 play 竞态 → 新链 B 的观测：', JSON.stringify(before));
  t('A（被停）安静落定', before.aSettled === true, 'aSettled=' + before.aSettled);
  t('B 的 AbortController 被旧链误杀（被 abort 或被 stopAudio 清空）', before.bAborted === true || before.bAborted === null, 'bAborted=' + before.bAborted);
  t('B 从未出声（复现「点了朗读没声音」）', !before.plays.some((p) => p.tag === 'B'), 'plays=' + JSON.stringify(before.plays));
  t('B 被安静砍掉（无报错、resolved 收场）', before.bResult === 'resolved', 'bResult=' + before.bResult);

  const after = await runScenario(true);
  console.log('[修复后] 场景一同时序 → 新链 B 的观测：', JSON.stringify(after));
  t('A（被停）仍安静落定', after.aSettled === true, 'aSettled=' + after.aSettled);
  t('B 的 AbortController 未被误杀', after.bAborted === false, 'bAborted=' + after.bAborted);
  t('B 正常出声（play 已调用）', after.plays.some((p) => p.tag === 'B'), 'plays=' + JSON.stringify(after.plays));
  t('B 仍在播放（未落定）', after.bResult === 'pending', 'bResult=' + after.bResult);

  const sb = await runSettleScenario(false);
  console.log('[修复前] 场景二 settle 按钮态 → 观测：', JSON.stringify({ btnPlaying: sb.btnPlaying, chainBActive: sb.chainBActive }));
  t('新链 B 的「停止」态被旧链迟到 settle 摘掉（复现按钮态错乱）', sb.btnPlaying === false && sb.chainBActive === false, 'btnPlaying=' + sb.btnPlaying + ' chainBActive=' + sb.chainBActive);

  const sa = await runSettleScenario(true);
  console.log('[修复后] 场景二同时序 → 观测：', JSON.stringify({ btnPlaying: sa.btnPlaying, chainBActive: sa.chainBActive }));
  t('新链 B 的「停止」态保持、链 B 仍在播（链代数让路）', sa.btnPlaying === true && sa.chainBActive === true, 'btnPlaying=' + sa.btnPlaying + ' chainBActive=' + sa.chainBActive);

  console.log(fail === 0 ? '\n全部断言通过：修复前复现两处竞态，修复后均正常。' : '\n' + fail + ' 条断言失败');
  process.exit(fail === 0 ? 0 : 1);
})();
