/**
 * 语音插件 —— 浏览器半边。
 * 顶层插件先 $mount 出 jxSpeech Remote 命名空间服务，再用一个带完整注入声明的
 * 子插件装配四块界面：
 * - conversation.chat.assistant-actions → 每条助手消息的朗读按钮（Web Speech API）；
 * - conversation.input.right           → 语音输入按钮（本地离线 / 实时听写 / 录音转写回退），靠右对齐；
 * - conversation.input.overlay         → 润色气泡（听写后出现在输入框右上角，点击 LLM 保守校对）；
 * - settings.section                   → 语音设置页（语速/音色/试听/本地引擎开关）。
 * 自旧坚祥 ui-speech + dsh-file-upload(语音部分) 合并迁移。
 */
import type { Context as ClientContext } from '@deepseek-ai/cordis'
import { TYPERT_REMOTE } from './remote-client.ts'
import { SpeechController, cleanForSpeech, speechEngine } from './speech-controller.ts'
import { TtsAction, type TtsActionInjected } from './client/TtsAction.tsx'
import { MicButton, type MicButtonInjected } from './client/MicButton.tsx'
import { PolishBubble, type PolishBubbleState } from './client/PolishBubble.tsx'
import {
  SpeechSettingsSection, type SpeechSettingsInjected, type SpeechSettingsLocalFace,
} from './client/SpeechSettingsSection.tsx'
import {
  DEFAULT_SPEECH_RATE, DEFAULT_SPEECH_VOICE, POLISH_WINDOW_MS, SPEECH_HOTKEY_FIELD, SPEECH_HOTKEY_MODE_FIELD,
  SPEECH_LOCAL_STT_FIELD, SPEECH_POLISH_CONTEXT_FIELD, SPEECH_POLISH_PROMPT_FIELD, SPEECH_POLISH_WORDS_FIELD,
  SPEECH_RATE_FIELD, SPEECH_SETTINGS_NAMESPACE, SPEECH_VOICE_FIELD, type SpeechSettings, type SpeechHotkey,
  type SpeechHotkeyMode,
} from './settings-shared.ts'
import { en, zh, type SpeechLocaleKey } from './locales.ts'

/** 词典命名空间。 */
const NS = 'dsh-speech'

declare module '@deepseek-ai/dsh-client-ui-slots' {
  interface LocaleNamespaceMap {
    /** 语音插件文案。 */
    'dsh-speech': SpeechLocaleKey
  }
}

/** 顶层插件所需服务（$mount 的载体；命名空间服务由本插件自己挂出来）。 */
export const inject = ['remote']

type RemoteResult<T> = { ok: true, value: T } | { ok: false, error: { code: string, message: string } }
type JxSpeechNamespace = {
  asrStatus(): Promise<RemoteResult<{ configured: boolean, model: string }>>
  transcribe(audioBase64: string, mimeType: string): Promise<RemoteResult<{ text: string }>>
  setVoiceActive(active: boolean): Promise<RemoteResult<{ ok: true }>>
  polish(text: string, context: string): Promise<RemoteResult<{ text: string }>>
  localStatus(): Promise<RemoteResult<{
    moduleInstalled: boolean
    modelReady: boolean
    busy: boolean
    error: string
    progress: { file: string, done: number, total: number }
    ready: boolean
  }>>
  localSetup(): Promise<RemoteResult<{ started: true }>>
  transcribePcm(pcmBase64: string): Promise<RemoteResult<{ text: string }>>
}

/** 输入框面的最小形状（ui-conversation 的 SessionInput）。 */
interface InputFace {
  state: {
    getSnapshot(): { draft: string, phase: string }
    subscribe(fn: () => void): () => void
  }
  setDraft(text: string): void
  submit(): void
  notify?(level: string, text: string): void
}

/** 冻结的偏好兜底：uSES 要求 getSnapshot 引用稳定。 */
const FALLBACK_SETTINGS: SpeechSettings = Object.freeze({
  rate: DEFAULT_SPEECH_RATE,
  voice: DEFAULT_SPEECH_VOICE,
  localStt: false,
  hotkey: 'alt',
  hotkeyMode: 'tap',
  polishPrompt: '',
  polishWords: '',
  polishContext: false,
})

const PREVIEW_SENTENCE = '您好，这是当前语速和音色的试听效果。'

/** 挂载 jxSpeech Remote，并装配朗读/语音输入/润色/设置页四块界面。 */
export async function apply(ctx: ClientContext): Promise<() => Promise<void>> {
  const disposeMount = await ctx.remote.$mount(TYPERT_REMOTE)

  const disposeUI = ctx.plugin({
    name: 'dsh-speech/ui',
    inject: ['slots', 'locale', 'remote.jxSpeech', 'settingsScope', 'sessions'],
    apply(child: ClientContext): void {
      const engine = speechEngine()
      const remote = (child as unknown as { remote: { jxSpeech: JxSpeechNamespace } }).remote
      const unwrap = <T,>(result: RemoteResult<T>, what: string): T => {
        if (!result.ok) throw new Error(`${what} failed: ${result.error.code}: ${result.error.message}`)
        return result.value
      }
      child.effect(() => child.locale.register(NS, { zh, en }), 'dsh-speech: dictionaries')

      // ---- durable 偏好（语速/音色/本地引擎开关） ------------------------
      const scope = (child as unknown as {
        settingsScope: {
          bind<T>(options: { namespace: string }): {
            getSnapshot(): { value: SpeechSettings | undefined }
            subscribe(fn: () => void): () => void
            set(field: string, value: unknown): Promise<void> | void
          }
        }
      }).settingsScope.bind<SpeechSettings>({ namespace: SPEECH_SETTINGS_NAMESPACE })
      const currentSettings = (): SpeechSettings => scope.getSnapshot().value ?? FALLBACK_SETTINGS

      // ---- 本地引擎就绪标记（惰性拉取 + 轮询下载进度共用） ----------------
      let localReady = false
      let localState: Awaited<ReturnType<JxSpeechNamespace['localStatus']>>['value'] | undefined
      const localListeners = new Set<() => void>()
      const localSource = {
        getSnapshot: (): typeof localState => localState,
        subscribe(fn: () => void): () => void {
          localListeners.add(fn)
          return () => { localListeners.delete(fn) }
        },
      }
      const pushLocal = (value: typeof localState): void => {
        localState = value
        localReady = value?.ready === true
        for (const fn of localListeners) fn()
      }
      const refreshLocal = (): void => {
        void remote.jxSpeech.localStatus()
          .then(result => { if (result.ok) pushLocal(result.value) })
          .catch(() => undefined)
      }
      refreshLocal()

      // ---- 播放状态：保留 hooks 舱室（裸 observable） -------------------
      let speakingIds: readonly string[] = []
      const speakingListeners = new Set<() => void>()
      const speakingSource = {
        getSnapshot: (): readonly string[] => speakingIds,
        subscribe(fn: () => void): () => void {
          speakingListeners.add(fn)
          return () => { speakingListeners.delete(fn) }
        },
      }
      const setSpeakingIds = (next: readonly string[]): void => {
        if (next.length === speakingIds.length && next.every((id, i) => id === speakingIds[i])) return
        speakingIds = next
        for (const fn of speakingListeners) fn()
      }

      let previewing = false
      const previewListeners = new Set<() => void>()
      const previewSource = {
        getSnapshot: (): boolean => previewing,
        subscribe(fn: () => void): () => void {
          previewListeners.add(fn)
          return () => { previewListeners.delete(fn) }
        },
      }
      const setPreviewing = (next: boolean): void => {
        if (next === previewing) return
        previewing = next
        for (const fn of previewListeners) fn()
      }

      // ---- 润色气泡状态 -------------------------------------------------
      let polishState: PolishBubbleState = { visible: false, busy: false, error: '' }
      /** 语音段在草稿中的起点（听写开始时的草稿长度）。 */
      let voiceStart: number | null = null
      let polishHideTimer: ReturnType<typeof setTimeout> | null = null
      const polishListeners = new Set<() => void>()
      const polishSource = {
        getSnapshot: (): PolishBubbleState => polishState,
        subscribe(fn: () => void): () => void {
          polishListeners.add(fn)
          return () => { polishListeners.delete(fn) }
        },
      }
      const setPolishState = (next: PolishBubbleState): void => {
        polishState = next
        for (const fn of polishListeners) fn()
      }
      /** 语音输入开启即亮气泡（告知效果），并刷新存在窗口。 */
      const showPolish = (start: number): void => {
        voiceStart = start
        setPolishState({ visible: true, busy: false, error: '' })
        if (polishHideTimer !== null) clearTimeout(polishHideTimer)
        polishHideTimer = setTimeout(hidePolish, POLISH_WINDOW_MS)
      }
      const hidePolish = (): void => {
        if (polishHideTimer !== null) {
          clearTimeout(polishHideTimer)
          polishHideTimer = null
        }
        voiceStart = null
        setPolishState({ visible: false, busy: false, error: '' })
      }

      // ---- 会话级输入面解析（麦克风 + 润色共用） -------------------------
      const inputFaceOf = (sessionId: string): InputFace | undefined => {
        const sessions = (child as unknown as { sessions?: { scope(id: string): ClientContext | undefined } }).sessions
        const actx = sessions?.scope(sessionId)
        if (actx === undefined) return undefined
        // 必须用 ctx.get(name) 访问会话作用域服务：cordis 禁止未声明注入的属性访问。
        const conversation = (actx as unknown as {
          get(name: string): { input: { for(c: ClientContext): unknown } } | undefined
        }).get('conversation')
        return conversation?.input.for(actx as ClientContext) as InputFace | undefined
      }

      // ---- 朗读（平台缺 Web Speech API 时整块跳过） ---------------------
      if (engine !== undefined) {
        const controller = new SpeechController(engine)
        child.effect(() => () => { controller.dispose() }, 'dsh-speech: playback disposal')
        engine.getVoices()

        const ttsInjected = (): TtsActionInjected => ({
          hooks: { speaking: speakingSource },
          speak: (messageId, text) => {
            controller.speak({
              rate: currentSettings().rate,
              voiceURI: currentSettings().voice,
              text: cleanForSpeech(text),
              onDone: () => { setSpeakingIds(speakingIds.filter(id => id !== messageId)) },
            })
            setSpeakingIds([...speakingIds, messageId])
          },
          stop: () => { controller.stop() },
        })

        const settingsInjected = (): SpeechSettingsInjected => ({
          hooks: {
            previewing: previewSource,
            settings: { getSnapshot: (): SpeechSettings => currentSettings(), subscribe: fn => scope.subscribe(fn) },
          },
          setRate: (rate) => { void scope.set(SPEECH_RATE_FIELD, rate) },
          setVoice: (voiceURI) => { void scope.set(SPEECH_VOICE_FIELD, voiceURI) },
          setHotkey: (key) => { void scope.set(SPEECH_HOTKEY_FIELD, key) },
          setHotkeyMode: (mode) => { void scope.set(SPEECH_HOTKEY_MODE_FIELD, mode) },
          setPolishPrompt: (text) => { void scope.set(SPEECH_POLISH_PROMPT_FIELD, text) },
          setPolishWords: (text) => { void scope.set(SPEECH_POLISH_WORDS_FIELD, text) },
          setPolishContext: (on) => { void scope.set(SPEECH_POLISH_CONTEXT_FIELD, on) },
          preview: () => {
            const settings = currentSettings()
            controller.speak({
              rate: settings.rate,
              voiceURI: settings.voice,
              text: PREVIEW_SENTENCE,
              onDone: () => { setPreviewing(false) },
            })
            setPreviewing(true)
          },
          stopPreview: () => { controller.stop() },
          engine,
        })

        const localFace = (): SpeechSettingsLocalFace => ({
          hooks: { local: localSource },
          value: (): boolean => currentSettings().localStt,
          set: (on) => {
            void scope.set(SPEECH_LOCAL_STT_FIELD, on)
            if (on) {
              void remote.jxSpeech.localSetup()
                .then(result => { if (!result.ok) throw new Error(result.error.message) })
                .catch(error => { console.warn('[dsh-speech] localSetup:', error) })
              startLocalPolling()
            }
          },
          refresh: refreshLocal,
        })

        child.slots.inject('conversation.chat.assistant-actions', () => child.slots.register(
          { name: 'conversation.chat.assistant-actions', id: 'dsh-speech-tts', order: 20, locale: NS, inject: ttsInjected },
          TtsAction as unknown as (props: never) => unknown,
        ))
        child.slots.inject('settings.section', () => child.slots.register(
          {
            name: 'settings.section', id: 'speech', order: 15, locale: NS,
            label: () => (child.locale.bind(NS))('nav'),
            inject: (): SpeechSettingsInjected & SpeechSettingsLocalFace => {
              const base = settingsInjected()
              const local = localFace()
              // hooks 舱室必须显式合并：对象展开会让后者整体覆盖前者。
              return { ...base, ...local, hooks: { ...base.hooks, ...local.hooks } }
            },
          },
          SpeechSettingsSection as unknown as (props: never) => unknown,
        ))
      }

      /** 下载中每 2 秒轮询本地状态，就绪或出错后停止。 */
      const startLocalPolling = (): void => {
        const timer = setInterval(() => {
          refreshLocal()
          if (localState !== undefined && !localState.busy) clearInterval(timer)
        }, 2000)
        child.effect(() => () => clearInterval(timer), 'dsh-speech: local polling')
      }

      // ---- 语音输入（本地引擎优先，听写为主，录音转写为回退） -------------
      child.slots.inject('conversation.input.right', () => child.slots.register(
        {
          name: 'conversation.input.right',
          id: 'dsh-speech-mic',
          order: 0,
          locale: NS,
          inject: (sessionId: string): MicButtonInjected => {
            const inputFace = (): InputFace | undefined => inputFaceOf(sessionId)
            const notifyError = (text: string): void => {
              const input = inputFace()
              if (typeof input?.notify === 'function') {
                input.notify('error', text)
              } else {
                console.warn(`[dsh-speech] ${text}`)
              }
            }
            return {
              readDraft: () => inputFace()?.state.getSnapshot().draft ?? '',
              updateDraft: (text) => { inputFace()?.setDraft(text) },
              watchSubmit: (onSubmit) => {
                const input = inputFace()
                if (input === undefined) return () => undefined
                return input.state.subscribe(() => {
                  const phase = input.state.getSnapshot().phase
                  if (phase === 'submitting' || phase === 'adjudicating') {
                    hidePolish()
                    onSubmit()
                  }
                })
              },
              submitDraft: () => { inputFace()?.submit() },
              notifyVoiceActive: (active) => {
                // 尽力而为的上报：失败静默（提示段只是体验增强）。
                void remote.jxSpeech.setVoiceActive(active)
                  .then(result => { if (result !== undefined && !result.ok) throw new Error(result.error.message) })
                  .catch(() => undefined)
              },
              transcribe: async (audioBase64, mimeType) =>
                unwrap(await remote.jxSpeech.transcribe(audioBase64, mimeType), 'jxSpeech.transcribe').text,
              transcribePcm: async (pcmBase64) =>
                unwrap(await remote.jxSpeech.transcribePcm(pcmBase64), 'jxSpeech.transcribePcm').text,
              localEngine: () => currentSettings().localStt && localReady,
              hotkey: (): SpeechHotkey => currentSettings().hotkey,
              hotkeyMode: (): SpeechHotkeyMode => currentSettings().hotkeyMode,
              onVoiceStart: (start) => { showPolish(start) },
              onVoiceEnd: (start, chars) => {
                // 结束时刷新窗口；无实际语音内容则收起气泡。
                if (chars <= 0) {
                  hidePolish()
                  return
                }
                showPolish(start)
              },
              notifyError,
              t: (child.locale.bind(NS)) as unknown as (key: string) => string,
            }
          },
        },
        MicButton as unknown as (props: never) => unknown,
      ))

      // ---- 润色气泡（输入框右上角外侧，漫画对话气泡样式） ----------------
      child.slots.inject('conversation.input.overlay', () => child.slots.register(
        {
          name: 'conversation.input.overlay',
          id: 'dsh-speech-polish',
          order: 0,
          locale: NS,
          inject: (sessionId: string) => {
            const inputFace = (): InputFace | undefined => inputFaceOf(sessionId)
            const t = (key: string): string => (child.locale.bind(NS))(key as never)
            return {
              hooks: { polish: polishSource },
              polish: async (context: string) => {
                if (voiceStart === null) return
                const input = inputFace()
                if (input === undefined) return
                setPolishState({ visible: true, busy: true, error: '' })
                try {
                  const draft = input.state.getSnapshot().draft
                  const start = Math.min(voiceStart, draft.length)
                  const text = draft.slice(start)
                  if (text.trim() === '') {
                    hidePolish()
                    return
                  }
                  const result = unwrap(await remote.jxSpeech.polish(text, context), 'jxSpeech.polish').text
                  input.setDraft(draft.slice(0, start) + result)
                  hidePolish()
                } catch (error) {
                  const message = error instanceof Error ? error.message : String(error)
                  setPolishState({ visible: true, busy: false, error: `${t('polish.failed')}: ${message.slice(0, 160)}` })
                  // 错误在气泡上显示 8 秒后自动恢复为可重试状态。
                  setTimeout(() => {
                    setPolishState(previous => previous.error !== '' ? { visible: true, busy: false, error: '' } : previous)
                  }, 8000)
                }
              },
              dismiss: hidePolish,
              useContext: () => currentSettings().polishContext,
              t,
            }
          },
        },
        PolishBubble as unknown as (props: never) => unknown,
      ))
    },
  })

  return async () => {
    await disposeUI()
    await disposeMount()
  }
}
