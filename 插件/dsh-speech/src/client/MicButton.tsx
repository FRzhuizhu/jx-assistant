/**
 * 语音输入按钮：Web Speech API 实时听写，直接写入输入框草稿。
 * 识别服务瞬时故障（network 等）不立即结束会话：短窗口内退避自动重连，
 * 期间用 MediaRecorder 缓冲录音，重连成功后把断线语音补写进草稿
 * （云端 ASR 优先；本地 SenseVoice 仅在用户开关开启时作后备，关闭时
 * 绝不加载，避免拖垮低配机器）。重连超过 3 秒仍无任何文字输出则报错
 * 并丢弃录音，结束本次听写——不让用户说了半天才发现没转写。
 * 浏览器完全无识别引擎时回退为 MediaRecorder 录音 → 宿主 ASR 转写。
 *
 * 自旧坚祥 dsh-file-upload(jx1.0) 的 MicButton 迁移，关键改动：
 * - 与文件上传完全解耦（回退路径改走 jxSpeech.transcribe，不再产生音频附件）；
 * - 错误提示经输入框 notice 通道（不再依赖上传组件的错误状态）。
 * "听写不覆盖用户编辑"的去重/采纳逻辑原样保留。
 */
import { useEffect, useRef, useState } from 'react'
import { Tooltip } from '@deepseek-ai/dsh-client-ui-primitives'
import { MAX_LOCAL_RECORD_SEC, MAX_RECORD_SEC } from '../settings-shared.ts'

/** 注入面：草稿读写、提交观察、宿主上报与转写。 */
export interface MicButtonInjected {
  hooks: Record<string, never>
  /** 当前输入框草稿全文。 */
  readDraft: () => string
  /** 整体替换输入框草稿（官方 setDraft 通道，与用户编辑同一事件路径）。 */
  updateDraft: (text: string) => void
  /** 观察提交动作（用户在听写中发送消息）；返回退订函数。 */
  watchSubmit: (onSubmit: () => void) => () => void
  /** 提交当前草稿（听写中按 Enter 时调用）。 */
  submitDraft: () => void
  /** 上报听写激活/结束（宿主据此在系统提示词中携带转写瑕疵提示）。 */
  notifyVoiceActive: (active: boolean) => void
  /** 录音转写回退：base64 音频 → 文本；失败 reject。 */
  transcribe: (audioBase64: string, mimeType: string) => Promise<string>
  /** 本地离线增量转写：16kHz float32 PCM（base64 小端）→ 该段文本。 */
  transcribePcm: (pcmBase64: string) => Promise<string>
  /** 本次听写是否使用本地离线引擎（读取最新持久化值 + 就绪状态）。 */
  localEngine: () => boolean
  /** 听写结束回调（含语音段在草稿中的起点），供润色气泡使用。 */
  onVoiceEnd: (start: number, chars: number) => void
  /** 输入框 notice 通道（错误提示）。 */
  notifyError: (text: string) => void
  /** 当前热键配置（读取最新持久化值）。 */
  hotkey: () => 'alt' | 'alt-space'
  /** 热键触发模式（读取最新持久化值）。 */
  hotkeyMode: () => 'tap' | 'hold'
  /** 本地化文案。 */
  t: (key: string) => string
}

export type MicButtonProps = MicButtonInjected

/** 样式：官方 .add 圆钮规范 + 录音态红/前景色硬切交替 + 倒计时数字弹出缩放。 */
const MIC_STYLE = `
.dsh-mic-btn{display:grid;place-items:center;flex:none;width:28px;height:28px;border:none;border-radius:999px;background:var(--dsw-specific-selector);color:var(--dsw-alias-label-primary);cursor:pointer;position:relative;overflow:visible;transition:background .15s ease}
.dsh-mic-btn:hover{background:var(--dsw-alias-interactive-bg-hover-solid)}
.dsh-mic-btn.recording{animation:jxsp-mic-blink 1s infinite}
@keyframes jxsp-mic-blink{0%,49.99%{color:#e5484d}50%,100%{color:var(--dsw-alias-label-primary)}}
.dsh-mic-count{animation:jxsp-mic-pop .9s ease-out both}
@keyframes jxsp-mic-pop{0%{transform:scale(1.6)}100%{transform:scale(1)}}
`

/** 两个字符串的最长公共前缀长度。 */
function commonPrefixLen(a: string, b: string): number {
  const n = Math.min(a.length, b.length)
  let i = 0
  while (i < n && a[i] === b[i]) i += 1
  return i
}

/**
 * 浏览器听写（Web Speech）返回链路的标点清洗：全角逗号→一个空格，
 * 句号→两个空格，其余标点（、？！；：等）保持不变。只动全角标点——
 * 半角逗号/句点常出现在数字（3.14、1,000）与英文文本里，替换会破坏内容。
 * 仅对浏览器听写生效（其标点由微软/谷歌服务按停顿猜测，最不可靠）；
 * 本地引擎与云端转写回退保留原始标点。
 */
function cleanDictationPunctuation(text: string): string {
  return text.replace(/，/g, ' ').replace(/。/g, '  ')
}

/** ArrayBuffer → base64（分块 btoa，避免大文件栈溢出）。 */
function arrayBufferToBase64(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer)
  let binary = ''
  const chunk = 0x8000
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode(...bytes.subarray(i, i + chunk))
  }
  return btoa(binary)
}

/** Float32Array → 小端 base64 字节（本地引擎上传格式）。 */
function f32ToBase64(samples: Float32Array): string {
  const bytes = new Uint8Array(samples.length * 4)
  const view = new DataView(bytes.buffer)
  for (let i = 0; i < samples.length; i++) view.setFloat32(i * 4, samples[i], true)
  return arrayBufferToBase64(bytes.buffer)
}

/** 录音 blob → 16kHz 单声道 float32 PCM（浏览器侧解码+重采样，30 秒硬超时防挂起）。 */
async function blobToPcm16k(blob: Blob): Promise<Float32Array> {
  const Ctx = (window as unknown as { AudioContext?: new () => AudioContext }).AudioContext
  if (Ctx === undefined) throw new Error('此浏览器不支持音频解码')
  const ctx = new Ctx()
  const timer = setTimeout(() => { void ctx.close().catch(() => undefined) }, 30_000)
  try {
    const buffer = await ctx.decodeAudioData(await blob.arrayBuffer())
    const src = buffer.getChannelData(0)
    if (buffer.sampleRate === 16000) return new Float32Array(src)
    const ratio = buffer.sampleRate / 16000
    const out = new Float32Array(Math.round(src.length / ratio))
    for (let i = 0; i < out.length; i++) {
      const pos = i * ratio
      const i0 = Math.floor(pos)
      const i1 = Math.min(i0 + 1, src.length - 1)
      const frac = pos - i0
      out[i] = src[i0] + (src[i1] - src[i0]) * frac
    }
    return out
  } finally {
    clearTimeout(timer)
    void ctx.close().catch(() => undefined)
  }
}

/** 听写会话的内部状态（见旧版注释：增量结果按索引缓存、用户编辑权威）。 */
interface DictationSession {
  finalText: string
  segments: Map<number, string>
  finalized: Set<number>
  maxIndex: number
  baseDraft: string
  lastRendered: string
  dedupLeft: string
  dedupPos: number
  dedupConsumed: Map<number, number>
  userStopped: boolean
  silent: boolean
  pendingSubmit: boolean
  /** 连续重启失败次数（onstart 清零）；驱动退避间隔与"重连中"提示。 */
  netRetries: number
}

type SpeechRecognitionLike = {
  lang: string
  continuous: boolean
  interimResults: boolean
  start(): void
  stop(): void
  onstart: (() => void) | null
  onresult: ((event: { resultIndex: number, results: ArrayLike<ArrayLike<{ transcript: string, isFinal?: boolean }>> }) => void) | null
  onend: (() => void) | null
  onerror: ((error: { error?: string }) => void) | null
}

/**
 * 本地离线听写：MediaRecorder 录 webm，每 3 秒把「头块 + 未转写增量」解码成
 * 16kHz PCM 发宿主 SenseVoice 增量转写，文本实时追加进草稿；停止时把剩余
 * 尾段转写完。各段文本独立解码、客户端拼接（与 scribe 同策略）。
 */
/** 模块级活跃标志：组件重挂载后旧闭包不可达，用它挡住二次启动与孤儿进程。 */
let localDictationActive = false

async function runLocalDictationImpl(args: {
  transcribePcm: (b64: string) => Promise<string>
  notifyError: (text: string) => void
  updateDraft: (text: string) => void
  readDraft: () => string
  notifyVoiceActive: (active: boolean) => void
  /** 听写真正开始时回调（草稿起点），供润色气泡立即出现（告知语音已开启）。 */
  onVoiceStart: (start: number) => void
  /** 听写结束回调（含语音段在草稿中的起点），供润色气泡刷新窗口。 */
  onVoiceEnd: (start: number, chars: number) => void
  onState: (recording: boolean) => void
  registerStop: (stop: () => void) => void
  onTeardown: () => void
  /** getUserMedia 解析前用户是否已按停止（权限弹窗期间松开热键的场景）。 */
  isEarlyCancelled: () => boolean
  maxSec: number
  t: (key: string) => string
}): Promise<void> {
  const { transcribePcm, notifyError, updateDraft, readDraft, notifyVoiceActive, onVoiceStart, onVoiceEnd, onState, registerStop, onTeardown, isEarlyCancelled, maxSec, t } = args
  if (localDictationActive) return
  localDictationActive = true
  let stopped = false
  let recorder: MediaRecorder | null = null
  let previewTimer: ReturnType<typeof setInterval> | null = null
  let capTimer: ReturnType<typeof setTimeout> | null = null
  const chunks: Blob[] = []
  let previewIndex = 1 // chunks[0] 是容器头块，每次预览都要带上
  let baseDraft = ''
  let text = ''
  let lastWritten: string | null = null
  let busy = false

  const appendText = (delta: string): void => {
    if (delta === '' || stopped) return
    const cur = readDraft()
    // 用户在听写中手动编辑过草稿：编辑内容优先，作为新基准重新追加。
    if (lastWritten !== null && cur !== lastWritten) {
      baseDraft = cur
      text = ''
    }
    text = text === '' ? delta : `${text} ${delta}`
    lastWritten = baseDraft + text
    updateDraft(lastWritten)
  }

  const transcribeDelta = async (): Promise<void> => {
    if (busy || stopped || chunks.length <= previewIndex) return
    busy = true
    try {
      const blob = new Blob([chunks[0], ...chunks.slice(previewIndex)])
      const pcm = await blobToPcm16k(blob)
      previewIndex = chunks.length
      const delta = await transcribePcm(f32ToBase64(pcm))
      appendText(delta)
    } catch (error) {
      const detail = error instanceof Error ? error.message : String(error)
      notifyError(`${t('mic.transcribeFailed')}: ${detail.slice(0, 160)}`)
    } finally {
      busy = false
    }
  }

  const teardown = (): void => {
    stopped = true
    localDictationActive = false
    if (previewTimer !== null) clearInterval(previewTimer)
    if (capTimer !== null) clearTimeout(capTimer)
    previewTimer = null
    capTimer = null
    recorder = null
    onState(false)
    notifyVoiceActive(false)
    onTeardown()
  }

  try {
    const stream = await navigator.mediaDevices.getUserMedia({ audio: true })
    if (stopped || isEarlyCancelled()) {
      stream.getTracks().forEach(track => { track.stop() })
      teardown()
      return
    }
    const mime = MediaRecorder.isTypeSupported?.('audio/webm') === true ? 'audio/webm' : ''
    recorder = new MediaRecorder(stream, mime !== '' ? { mimeType: mime } : undefined)
    recorder.ondataavailable = e => { if (e.data.size > 0) chunks.push(e.data) }
    recorder.onstop = () => {
      stream.getTracks().forEach(track => { track.stop() })
      // 先拆 UI（立即解除录音态），尾段转写异步补写，语音段在转写完成后汇报。
      teardown()
      void (async () => {
        try {
          if (chunks.length > previewIndex) await transcribeDelta()
        } catch { /* 尾段失败：已有文本保留 */ }
        if (text.length > 0 || chunks.length > 1) onVoiceEnd(baseDraft.length, text.length)
      })()
    }
    // 停止柄必须幂等：对已停止的 recorder 再 stop() 会同步抛 InvalidStateError。
    registerStop(() => {
      try {
        if (recorder !== null && recorder.state !== 'inactive') recorder.stop()
      } catch { /* 已停止 */ }
    })
    baseDraft = readDraft()
    onState(true)
    notifyVoiceActive(true)
    onVoiceStart(baseDraft.length)
    recorder.start(1000)
    previewTimer = setInterval(() => { void transcribeDelta() }, 3000)
    capTimer = setTimeout(() => { recorder?.stop() }, maxSec * 1000)
  } catch (error) {
    const detail = error instanceof Error ? error.message : String(error)
    notifyError(`${t('mic.transcribeFailed')}: ${detail.slice(0, 160)}`)
    teardown()
  }
}

/** 录音并转写回退：MediaRecorder → base64 → 宿主 ASR → 文本追加进草稿。 */
async function recordAndTranscribe(
  transcribe: MicButtonInjected['transcribe'],
  notifyError: (text: string) => void,
  updateDraft: (text: string) => void,
  readDraft: () => string,
  maxSec: number,
  t: (key: string) => string,
): Promise<void> {
  let recorder: MediaRecorder | null = null
  try {
    const stream = await navigator.mediaDevices.getUserMedia({ audio: true })
    const mime = MediaRecorder.isTypeSupported?.('audio/webm') === true ? 'audio/webm' : ''
    recorder = new MediaRecorder(stream, mime !== '' ? { mimeType: mime } : undefined)
    const chunks: Blob[] = []
    recorder.ondataavailable = e => { if (e.data.size > 0) chunks.push(e.data) }
    recorder.onstop = () => {
      stream.getTracks().forEach(track => { track.stop() })
      const blob = new Blob(chunks, { type: mime || 'audio/webm' })
      void blob.arrayBuffer()
        .then(buffer => transcribe(arrayBufferToBase64(buffer), blob.type))
        .then(text => { updateDraft(readDraft() + text) })
        .catch((error: unknown) => {
          const detail = error instanceof Error ? error.message : String(error)
          notifyError(`${t('mic.transcribeFailed')}: ${detail.slice(0, 160)}`)
        })
    }
    recorder.start()
    setTimeout(() => { recorder?.stop() }, maxSec * 1000)
  } catch (error) {
    recorder = null
    const detail = error instanceof Error ? error.message : String(error)
    notifyError(`${t('mic.transcribeFailed')}: ${detail.slice(0, 160)}`)
  }
}

/** 渲染语音输入按钮（含录音脉冲与收尾倒计时）。 */
export function MicButton({
  readDraft, updateDraft, watchSubmit, submitDraft, notifyVoiceActive, transcribe, transcribePcm,
  localEngine, onVoiceStart, onVoiceEnd, notifyError, hotkey, hotkeyMode, t,
}: MicButtonProps) {
  const [recording, setRecording] = useState(false)
  const recRef = useRef<{ stop: () => void } | null>(null)
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const [countdown, setCountdown] = useState<number | null>(null)
  const countdownRef = useRef<ReturnType<typeof setInterval> | null>(null)
  const sessionRef = useRef<DictationSession | null>(null)
  // 断线缓冲录音的停止柄（听写会话内部登记，stop() 兜底调用）。
  const gapStopRef = useRef<(() => void) | null>(null)
  // 本地引擎听写的控制柄（MediaRecorder + 定时增量转写）。
  const localRef = useRef<{ stop: () => void } | null>(null)

  const stop = (silent = false): void => {
    const session = sessionRef.current
    if (session !== null && !silent) session.userStopped = true
    recRef.current?.stop()
    recRef.current = null
    localRef.current?.stop()
    localRef.current = null
    gapStopRef.current?.()
    gapStopRef.current = null
    setRecording(false)
    if (timeoutRef.current !== null) {
      clearTimeout(timeoutRef.current)
      timeoutRef.current = null
    }
    if (countdownRef.current !== null) {
      clearInterval(countdownRef.current)
      countdownRef.current = null
    }
    setCountdown(null)
  }

  const stopRef = useRef(stop)
  stopRef.current = stop
  // 热键监听用的稳定引用（在热键 effect 之前声明，避免 TDZ）。
  const stopRef2 = useRef(() => undefined)
  stopRef2.current = () => { stopRef.current() }
  const toggleRef = useRef<() => void>(() => undefined)
  const recordingRef = useRef(false)
  useEffect(() => { recordingRef.current = recording }, [recording])

  // 用户发送消息的瞬间停止听写（草稿已带实时预览的转写文本）。
  // 热键：点按 = 按一下开始/再按结束；按住说话 = 按下开始、松开结束。
  // Alt 单键不与 Ctrl/Shift/Meta 组合才生效（避开系统组合键）；Alt+Tab 切窗
  // 会先触发 blur——录音直接停止。250ms 防抖避免长按键盘重复触发。
  const lastHotkeyAt = useRef(0)
  useEffect(() => {
    const matches = (e: KeyboardEvent): boolean => {
      const key = hotkey()
      if (key === 'alt-space') return e.code === 'Space' && e.altKey && !e.ctrlKey && !e.metaKey
      return e.key === 'Alt' && !e.ctrlKey && !e.shiftKey && !e.metaKey
    }
    const onKeyDown = (e: KeyboardEvent): void => {
      if (e.repeat || !matches(e)) return
      e.preventDefault()
      const now = Date.now()
      if (now - lastHotkeyAt.current < 250) return
      lastHotkeyAt.current = now
      if (hotkeyMode() === 'hold') {
        if (!recordingRef.current) toggleRef.current()
      } else {
        toggleRef.current()
      }
    }
    const onKeyUp = (e: KeyboardEvent): void => {
      if (hotkeyMode() !== 'hold') return
      const key = hotkey()
      const isUp = key === 'alt-space' ? e.code === 'Space' : e.key === 'Alt'
      if (isUp && recordingRef.current) stopRef2.current()
    }
    const onBlur = (): void => {
      if (recordingRef.current) stopRef2.current()
    }
    window.addEventListener('keydown', onKeyDown)
    window.addEventListener('keyup', onKeyUp)
    window.addEventListener('blur', onBlur)
    return () => {
      window.removeEventListener('keydown', onKeyDown)
      window.removeEventListener('keyup', onKeyUp)
      window.removeEventListener('blur', onBlur)
    }
  }, [hotkey, hotkeyMode])

  useEffect(() => watchSubmit(() => {
    const session = sessionRef.current
    if (session !== null) session.silent = true
    stopRef.current(true)
  }), [watchSubmit])

  const toggle = (): void => {
    if (recording) {
      stop()
      return
    }
    if (localEngine()) {
      // 占位停止柄 + 早取消标记：覆盖 getUserMedia 权限弹窗期间按停的场景；
      // 真正的录音柄在 MediaRecorder 创建后由 registerStop 替换。
      let earlyCancelled = false
      localRef.current = { stop: () => { earlyCancelled = true } }
      void runLocalDictationImpl({
        transcribePcm, notifyError, updateDraft, readDraft, notifyVoiceActive, onVoiceStart, onVoiceEnd,
        onState: setRecording,
        registerStop: handle => { localRef.current = { stop: handle } },
        onTeardown: () => { localRef.current = null },
        isEarlyCancelled: () => earlyCancelled,
        maxSec: MAX_LOCAL_RECORD_SEC,
        t,
      })
      startCountdown(MAX_LOCAL_RECORD_SEC)
      return
    }
    const SR = (window as unknown as { SpeechRecognition?: unknown, webkitSpeechRecognition?: unknown }).SpeechRecognition
      ?? (window as unknown as { webkitSpeechRecognition?: unknown }).webkitSpeechRecognition
    if (SR === undefined || SR === null) {
      // 无实时识别引擎：录音转写回退（需宿主已配置 ASR）。
      void recordAndTranscribe(transcribe, notifyError, updateDraft, readDraft, MAX_RECORD_SEC, t)
      return
    }
    const rec = new (SR as new () => SpeechRecognitionLike)()
    rec.lang = navigator.language || 'zh-CN'
    rec.continuous = true
    rec.interimResults = true
    const session: DictationSession = {
      finalText: '',
      segments: new Map(),
      finalized: new Set(),
      maxIndex: -1,
      baseDraft: readDraft(),
      lastRendered: readDraft(),
      dedupLeft: '',
      dedupPos: 0,
      dedupConsumed: new Map(),
      userStopped: false,
      silent: false,
      pendingSubmit: false,
      netRetries: 0,
    }
    sessionRef.current = session

    const flushSegments = (): void => {
      const keys = Array.from(session.segments.keys()).sort((a, b) => a - b)
      for (const k of keys) session.finalText += session.segments.get(k) ?? ''
      session.segments.clear()
      session.finalized.clear()
      session.maxIndex = -1
    }

    const renderLive = (): void => {
      const cur = readDraft()
      if (cur !== session.lastRendered) {
        // 输入框内容偏离了我们上次写入的文本——用户在听写中手动编辑了。
        // 采纳编辑后的文本为新基准：语音只会追加在用户所见之后。引擎可能
        // 重放已在屏上的语音（interim 定格 / 索引重置），把它留作去重窗口
        // 消化掉，绝不重复追加。
        session.dedupLeft = session.lastRendered.slice(session.baseDraft.length)
        session.dedupPos = 0
        session.dedupConsumed.clear()
        session.baseDraft = cur
        session.lastRendered = cur
        session.finalText = ''
        session.segments.clear()
        session.finalized.clear()
      }
      let text = session.finalText
      const keys = Array.from(session.segments.keys()).sort((a, b) => a - b)
      for (const k of keys) text += session.segments.get(k) ?? ''
      const next = session.baseDraft + text
      if (next === cur) return
      session.lastRendered = next
      updateDraft(next)
    }

    // ---- 断线缓冲：识别服务中断期间用 MediaRecorder 兜底录音 ----------------
    // Web Speech 的网络错误只影响识别不影响采集；缓冲录音在服务恢复
    // （onstart）后延迟补写进草稿。补写引擎云端 ASR 优先；本地 SenseVoice
    // 仅在用户开关开启（localEngine()）时作云端失败的后备——开关关闭时
    // 绝不加载本地模型，避免拖垮低配机器。两者都不可用则静默丢弃，不弹错。
    let gapStream: MediaStream | null = null
    let gapRecorder: MediaRecorder | null = null
    const gapChunks: Blob[] = []
    const startGapBuffer = (): void => {
      if (gapRecorder !== null) return
      void navigator.mediaDevices.getUserMedia({ audio: true })
        .then(stream => {
          if (gapRecorder !== null || sessionRef.current !== session) {
            stream.getTracks().forEach(track => { track.stop() })
            return
          }
          gapStream = stream
          const mime = MediaRecorder.isTypeSupported?.('audio/webm') === true ? 'audio/webm' : ''
          const recorder = new MediaRecorder(stream, mime !== '' ? { mimeType: mime } : undefined)
          recorder.ondataavailable = e => { if (e.data.size > 0) gapChunks.push(e.data) }
          recorder.start(1000)
          gapRecorder = recorder
        })
        .catch(() => undefined)
    }
    /** 断线缓冲转写：云端优先；本地仅在开关开启时后备；失败返回 ''。 */
    const transcribeGap = async (blob: Blob): Promise<string> => {
      try {
        return await transcribe(arrayBufferToBase64(await blob.arrayBuffer()), blob.type)
      } catch {
        if (!localEngine()) return ''
        try {
          return await transcribePcm(f32ToBase64(await blobToPcm16k(blob)))
        } catch { return '' }
      }
    }
    /** 停止缓冲；discard = true 直接丢弃录音（放弃场景），否则延迟补写。 */
    const stopGapBuffer = (discard = false): void => {
      const recorder = gapRecorder
      gapRecorder = null
      if (recorder === null) return
      recorder.onstop = () => {
        gapStream?.getTracks().forEach(track => { track.stop() })
        gapStream = null
        const blob = new Blob(gapChunks, { type: recorder.mimeType || 'audio/webm' })
        gapChunks.length = 0
        if (discard || blob.size === 0) return
        void transcribeGap(blob)
          .then(text => {
            if (text === '') return
            if (sessionRef.current === session) {
              session.finalText += text
              renderLive()
            } else {
              // 会话已收尾（用户已停止）：延迟补写在当前草稿末尾。
              updateDraft(readDraft() + text)
            }
          })
          .catch(() => undefined)
      }
      try {
        if (recorder.state !== 'inactive') recorder.stop()
        else recorder.onstop?.(new Event('stop'))
      } catch { /* 已停止 */ }
    }
    gapStopRef.current = stopGapBuffer

    // ---- 重连与放弃：短窗口退避重连，超时无输出则报错收场 -------------------
    /** 断线容忍窗口：持续重连但超过该时长仍无文字输出即放弃（毫秒）。 */
    const GIVE_UP_MS = 3000
    let outageStart: number | null = null
    /** 放弃：丢弃缓冲录音、拆除会话、报错告知（避免用户白说）。 */
    const giveUp = (): void => {
      stopGapBuffer(true)
      recRef.current = null
      setRecording(false)
      sessionRef.current = null
      if (timeoutRef.current !== null) {
        clearTimeout(timeoutRef.current)
        timeoutRef.current = null
      }
      notifyError(t('mic.giveup'))
    }
    /** 退避重启识别（300ms 起、1.2s 封顶），窗口内持续重连。 */
    const restartWithBackoff = (): void => {
      if (outageStart === null) outageStart = Date.now()
      if (Date.now() - outageStart >= GIVE_UP_MS) {
        giveUp()
        return
      }
      const delay = Math.min(300 * session.netRetries + 300, 1200)
      session.netRetries += 1
      startGapBuffer()
      setTimeout(() => {
        if (sessionRef.current !== session) return
        try {
          rec.start()
        } catch {
          restartWithBackoff()
        }
      }, delay)
    }

    rec.onresult = (event) => {
      // 引擎重置了内部结果列表（resultIndex 回退）：把已听到的段落先落定再重来。
      if (event.resultIndex < session.maxIndex) {
        flushSegments()
      }
      for (let i = event.resultIndex; i < event.results.length; i += 1) {
        const item = event.results[i]
        if (item.length === 0) continue
        // 标点清洗在入口做：下游的段落缓存/去重/渲染全部基于清洗后文本，
        // interim 与 final（标点随定格出现）天然一致。
        let transcript = cleanDictationPunctuation(item[0].transcript)
        if (transcript === '') continue
        if (session.dedupLeft.length > 0) {
          // 编辑锁窗口激活：跳过已在屏上的语音。增长的 interim 每次都会重报
          // 之前的词，所以按索引记录已消费量，只对增量做去重。
          const seen = session.dedupConsumed.get(i) ?? 0
          if (seen >= transcript.length) continue
          let growth = transcript.slice(seen)
          const overlap = commonPrefixLen(growth, session.dedupLeft.slice(session.dedupPos))
          session.dedupPos += overlap
          session.dedupConsumed.set(i, seen + overlap)
          growth = growth.slice(overlap)
          if (growth === '') continue
          transcript = growth
        }
        if (item[0].isFinal === true) {
          // 已确认：锁定该槽位，后续 interim 不能覆盖。
          session.segments.set(i, transcript)
          session.finalized.add(i)
        } else if (!session.finalized.has(i)) {
          // 实时 interim：只更新未锁定的槽位。
          session.segments.set(i, transcript)
        }
        session.maxIndex = Math.max(session.maxIndex, i)
      }
      renderLive()
    }

    rec.onstart = () => {
      // （重）连成功：清失败计数与断线计时，停止断线缓冲并补写其中语音。
      session.netRetries = 0
      outageStart = null
      stopGapBuffer()
    }

    rec.onend = () => {
      const s = sessionRef.current
      if (s === null) return
      if (timeoutRef.current !== null) {
        clearTimeout(timeoutRef.current)
        timeoutRef.current = null
      }
      if (s.userStopped) {
        // 用户主动停止：实时预览已把转写写进草稿，不动用户编辑；
        // 断线缓冲（若在录音）补写完再收尾。
        stopGapBuffer()
        recRef.current = null
        setRecording(false)
        sessionRef.current = null
        onVoiceEnd(s.baseDraft.length, s.finalText.length + s.segments.size)
      } else if (s.silent) {
        // 听写中消息已发送：拆除会话；Enter 请求的提交在此之后执行。
        stopGapBuffer()
        recRef.current = null
        setRecording(false)
        sessionRef.current = null
        onVoiceEnd(s.baseDraft.length, s.finalText.length + s.segments.size)
        if (s.pendingSubmit) {
          submitDraft()
        }
      } else {
        // 引擎自行结束（网络错误/静音/长语音边界）：折叠段落，退避重启。
        const keys = Array.from(s.segments.keys()).sort((a, b) => a - b)
        for (const k of keys) s.finalText += s.segments.get(k) ?? ''
        s.segments.clear()
        s.finalized.clear()
        s.maxIndex = -1
        restartWithBackoff()
      }
    }

    rec.onerror = (error) => {
      const code = error?.error ?? ''
      const s = sessionRef.current
      if (code === 'not-allowed' || code === 'service-not-allowed') {
        stopGapBuffer()
        recRef.current = null
        setRecording(false)
        sessionRef.current = null
        if (timeoutRef.current !== null) {
          clearTimeout(timeoutRef.current)
          timeoutRef.current = null
        }
        notifyError(t('mic.denied'))
        return
      }
      // 其余错误（network/no-speech/aborted 等）按瞬时故障处理：不结束
      // 会话、不弹错，交给随后的 onend 退避重启自动重连；缓冲录音保住
      // 中断期间的语音。s 为 null 说明会话已被拆除，忽略本次错误。
      if (s === null) return
      startGapBuffer()
    }

    recRef.current = { stop: () => { rec.stop() } }
    setRecording(true)
    // 通知宿主听写已激活：系统提示词将携带转写瑕疵提示。
    notifyVoiceActive(true)
    // 润色气泡立即出现（告知语音输入已开启）。
    onVoiceStart(session.baseDraft.length)
    try {
      rec.start()
    } catch {
      // 启动失败同样按瞬时故障退避重试（此路径 onend 不会触发）。
      restartWithBackoff()
    }
    timeoutRef.current = setTimeout(() => { stopRef.current() }, MAX_RECORD_SEC * 1000)
    // 自动停止前最后 9 秒倒计时。
    startCountdown(MAX_RECORD_SEC)
  }

  toggleRef.current = toggle

  /** 录音时长倒计时（最后 9 秒在按钮上显示数字）。 */
  const startCountdown = (maxSec: number): void => {
    const startedAt = Date.now()
    countdownRef.current = setInterval(() => {
      if (sessionRef.current === null && localRef.current === null) {
        if (countdownRef.current !== null) {
          clearInterval(countdownRef.current)
          countdownRef.current = null
        }
        setCountdown(null)
        return
      }
      const remaining = maxSec * 1000 - (Date.now() - startedAt)
      if (remaining <= 0) {
        if (countdownRef.current !== null) {
          clearInterval(countdownRef.current)
          countdownRef.current = null
        }
        setCountdown(null)
        stopRef.current()
        return
      }
      const sec = Math.ceil(remaining / 1000)
      setCountdown(sec <= 9 ? sec : null)
    }, 200)
  }

  return (
    <>
      {/* 官方输入框工具钮规范（InputBar.module.css 的 .add）：28px 圆形、
          selector 底色、label-primary 前景；录音态红/前景色硬切交替。 */}
      <style>{MIC_STYLE}</style>
      <Tooltip label={recording ? t('mic.stop') : t('mic.start')} side="top">
        <button
          type="button"
          className={`dsh-mic-btn${recording ? ' recording' : ''}`}
          aria-label={recording ? t('mic.stop') : t('mic.start')}
          aria-pressed={recording}
          onClick={toggle}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && sessionRef.current !== null) {
            // 听写中按 Enter：不让按钮默认点击变成普通停止；静默停止后提交草稿。
            e.preventDefault()
            const session = sessionRef.current
            session.pendingSubmit = true
            session.silent = true
            stopRef.current(true)
          }
        }}
      >
        <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
          <path
            d="M8 1.5a2.5 2.5 0 0 0-2.5 2.5v4a2.5 2.5 0 0 0 5 0V4A2.5 2.5 0 0 0 8 1.5Z"
            fill="currentColor"
          />
          <path
            d="M3.5 7.5a.75.75 0 0 1 1.5 0 2.5 2.5 0 0 0 5 0 .75.75 0 0 1 1.5 0 4 4 0 0 1-3.25 3.94V13H10a.75.75 0 0 1 0 1.5H6A.75.75 0 0 1 6 13h1.75v-1.56A4 4 0 0 1 4.5 8a.75.75 0 0 1 .5-.75.75.75 0 0 1 .5-.5Z"
            fill="currentColor"
            transform="translate(0 -1)"
          />
        </svg>
          {countdown !== null && (
            <span
              key={countdown}
              className="dsh-mic-count"
              style={{
                position: 'absolute', top: -5, right: -8, minWidth: 16, height: 16,
                lineHeight: '16px', textAlign: 'center', fontSize: 11, fontWeight: 700,
                borderRadius: 8, background: '#e5484d', color: '#fff', padding: '0 3px',
                boxSizing: 'border-box',
              }}
            >
              {countdown}
            </span>
          )}
        </button>
      </Tooltip>
    </>
  )
}
