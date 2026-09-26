/**
 * 浏览器语音播放控制器：Web Speech API 之上的单飞会话。
 * 新朗读会停止当前播放；试听与消息播放共享同一互斥量；插件卸载即取消。
 * 自旧坚祥 ui-speech 包原样迁移。
 */

/** 控制器所需的窄化语音引擎面（测试可用桩替换）。 */
export interface SpeechEngine {
  /** 系统语音列表；首次调用后可能异步填充。 */
  getVoices(): SpeechSynthesisVoice[]
  /** 取消当前发音后朗读一条语句。 */
  speak(utterance: SpeechSynthesisUtterance): void
  /** 停止当前发音，触发其 end/error 回调。 */
  cancel(): void
  /** 语音列表变化通知（设置页使用）。 */
  addEventListener(type: 'voiceschanged', listener: () => void): void
  removeEventListener(type: 'voiceschanged', listener: () => void): void
}

/** 一次播放请求；字段均为纯数据。 */
export interface SpeakRequest {
  /** 已清洗为可朗读形态的文本。 */
  text: string
  /** 语速倍率。 */
  rate: number
  /** 首选语音 URI；'' 自动挑选中文语音。 */
  voiceURI: string
  /** 本条发音结束/出错/被停止时恰好调用一次。 */
  onDone: () => void
}

/** 读取浏览器引擎；平台缺失时返回 undefined。 */
export function speechEngine(): SpeechEngine | undefined {
  const synthesis = globalThis.speechSynthesis
  const utteranceCtor = globalThis.SpeechSynthesisUtterance
  if (synthesis === undefined || utteranceCtor === undefined) return undefined
  return synthesis as unknown as SpeechEngine
}

/** 语言标签是否优先简体中文朗读。 */
function isChineseVoice(lang: string): boolean {
  return /^zh([-_]cn)?/i.test(lang)
}

/**
 * 为一次播放挑选语音。
 * @returns 匹配的选择、第一个中文语音、第一个 zh 语音，或 undefined（引擎默认）。
 */
export function pickVoice(voices: readonly SpeechSynthesisVoice[], voiceURI: string): SpeechSynthesisVoice | undefined {
  if (voiceURI !== '') {
    const selected = voices.find(voice => voice.voiceURI === voiceURI)
    if (selected !== undefined) return selected
  }
  return voices.find(voice => isChineseVoice(voice.lang))
    ?? voices.find(voice => /^zh/i.test(voice.lang))
}

/**
 * 去除会被念出来的 markdown 语法噪声。
 * @param markdown - 渲染态的助手文本。
 * @returns 交给语音引擎的文本。
 */
export function cleanForSpeech(markdown: string): string {
  return markdown
    .replace(/(```|~~~)[\s\S]*?(```|~~~)/g, ' ')
    .replace(/`([^`]*)`/g, '$1')
    .replace(/!\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/^#{1,6}\s+/gm, '')
    .replace(/^\s*[-*+]\s+/gm, '')
    .replace(/^\s*\d+\.\s+/gm, '')
    .replace(/^\s*>\s?/gm, '')
    .replace(/\|/g, ' ')
    .replace(/[*_~]{1,3}/g, '')
    .replace(/\n{2,}/g, '\n')
    .trim()
}

/**
 * 单飞播放会话：同时最多一条发音在播，开始下一条即停掉上一条；
 * dispose 取消正在播放的发音。
 */
export class SpeechController {
  private readonly engine: SpeechEngine
  private stopActive: (() => void) | null = null
  private disposed = false

  constructor(engine: SpeechEngine) {
    this.engine = engine
  }

  /** 播放一条请求；先停止任何正在进行的播放。 */
  speak(request: SpeakRequest): void {
    if (this.disposed) return
    this.stop()
    const utterance = new globalThis.SpeechSynthesisUtterance(request.text)
    const voice = pickVoice(this.engine.getVoices(), request.voiceURI)
    if (voice !== undefined) {
      utterance.voice = voice
      utterance.lang = voice.lang
    } else {
      utterance.lang = 'zh-CN'
    }
    utterance.rate = request.rate
    let done = false
    const finish = (): void => {
      if (done) return
      done = true
      this.stopActive = null
      request.onDone()
    }
    utterance.onend = finish
    utterance.onerror = finish
    this.stopActive = (): void => {
      this.engine.cancel()
      finish()
    }
    this.engine.speak(utterance)
  }

  /** 停止当前播放而不开始新的。 */
  stop(): void {
    if (this.stopActive !== null) this.stopActive()
  }

  /** 停止播放并拒绝后续所有请求；所属 fiber 卸载时调用。 */
  dispose(): void {
    this.stop()
    this.disposed = true
  }
}
