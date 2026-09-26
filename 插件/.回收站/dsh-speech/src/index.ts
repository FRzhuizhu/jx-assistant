/**
 * 语音插件 —— 宿主半边。
 * - 注册 durable 语音偏好命名空间（语速/音色/本地引擎开关）；
 * - jxSpeech Remote：
 *   - asrStatus / transcribe（云端 OpenAI 兼容转写，浏览器无识别引擎时的回退）
 *   - setVoiceActive（听写激活窗口 → 系统提示词转写瑕疵提示）
 *   - polish（复用 DSH 已配置模型做保守校对：只修错字/标点，不改写）
 *   - localStatus / localSetup / transcribePcm（本地离线 SenseVoice 转写）
 *
 * 注：不用 @Remote 装饰器语法（外部包构建管线不转换装饰器），改为按
 * typert-protocol 的原型描述符协议直接标注，与装饰器运行时效果等价。
 */
import type { Context } from '@deepseek-ai/cordis'
import { TypertRemoteService } from '@deepseek-ai/dsh-typert-protocol'
import { DEFAULT_POLISH_PROMPT, SPEECH_SETTINGS_NAMESPACE, SpeechSettingsSchema, VOICE_HINT_WINDOW_MS, type SpeechSettings } from './speech-settings.ts'
import { ensureLocalSetup, localStatus, transcribePcmBase64 } from './local-stt.ts'

export * from './speech-settings.ts'

/** 宿主配置（模组挂载行的 config，或环境变量回退）。 */
export interface JxSpeechConfig {
  /** OpenAI 兼容转写端点（POST {endpoint}, multipart file+model）。 */
  asrEndpoint?: string
  /** 转写模型名。 */
  asrModel?: string
  /** Bearer 密钥。 */
  apiKey?: string
}

const REMOTE_METHODS_KEY = '@deepseek-ai/dsh-typert-protocol/remote-methods'

/** 最近一次听写激活的时间（提示是尽力而为的全局窗口）。 */
let voiceActiveAt = 0

const VOICE_HINT_TEXT = [
  '（用户刚刚使用语音输入：最新消息可能包含语音转写瑕疵——同音字、错别字、',
  '标点缺失或口语化表达。请按语境理解真实意图，不要字面纠缠；如关键信息存疑再向用户确认。）',
].join('')

/** 宿主侧 settings 读取面（register 返回的 scope）。 */
interface SettingsScopeFace {
  get(): SpeechSettings | undefined
}

/** 一次润色的超时。 */
const POLISH_TIMEOUT_MS = 30_000
/** 润色文本长度上限。 */
const POLISH_MAX_CHARS = 12_000

/** 依据 MIME 推断转写上传文件名扩展名。 */
function extOf(mimeType: string): string {
  const mime = mimeType.toLowerCase()
  if (mime.includes('mp4') || mime.includes('aac')) return 'm4a'
  if (mime.includes('ogg')) return 'ogg'
  if (mime.includes('wav')) return 'wav'
  if (mime.includes('mpeg')) return 'mp3'
  return 'webm'
}

/** 构造 OpenAI 兼容 /audio/transcriptions 的 multipart 表单体。 */
function buildForm(audio: Buffer, mimeType: string, model: string): { body: Buffer, boundary: string } {
  const boundary = `----dsh-speech-${Date.now().toString(36)}`
  const filename = `voice-${Date.now().toString(36)}.${extOf(mimeType)}`
  const parts = [
    Buffer.from(`--${boundary}\r\n`),
    Buffer.from(`Content-Disposition: form-data; name="file"; filename="${filename}"\r\n`),
    Buffer.from(`Content-Type: ${mimeType || 'application/octet-stream'}\r\n\r\n`),
    audio,
    Buffer.from(`\r\n--${boundary}\r\n`),
    Buffer.from(`Content-Disposition: form-data; name="model"\r\n\r\n${model}\r\n`),
    Buffer.from(`--${boundary}--\r\n`),
  ]
  return { body: Buffer.concat(parts), boundary }
}

/** LLM 面的最小形状（@deepseek-ai/dsh-llm 的 ctx.llm）。 */
interface LlmFace {
  listProviders(): Array<{ id: string }>
  listModels(provider: string): Promise<Array<{ id: string }>>
  prepareCall(route: { provider: string, model: string }, signal: AbortSignal): Promise<{
    config: unknown
    stream(input: { messages: unknown[], system: string, signal: AbortSignal } & Record<string, unknown>): AsyncIterable<{ type: string, text?: string, reason?: { kind: string } }>
  }>
}

export class JxSpeechGateway extends TypertRemoteService {
  /** 解析后的 ASR 配置（env 回退）。 */
  readonly asr: { endpoint: string, model: string, apiKey: string }
  /** 缓存的润色路由（provider/model），首次调用时解析。 */
  private polishRoute: { provider: string, model: string } | null | undefined
  /** durable 设置读取面（settings 服务就绪后由 register 返回）。 */
  private settingsScope: SettingsScopeFace | undefined

  constructor(ctx: Context, config: JxSpeechConfig = {}) {
    super(ctx, 'jxSpeech')
    this.asr = {
      endpoint: config.asrEndpoint ?? process.env.ASR_ENDPOINT ?? process.env.DSH_ASR_ENDPOINT ?? '',
      model: config.asrModel ?? process.env.ASR_MODEL ?? 'whisper-1',
      apiKey: config.apiKey ?? process.env.ASR_API_KEY ?? '',
    }

    // durable 语音偏好：宿主 settings 文档（语速/音色/热键/润色提示词等）。
    ctx.inject(['settings'], (settingsCtx) => {
      const scope = settingsCtx.settings.register(SPEECH_SETTINGS_NAMESPACE, SpeechSettingsSchema)
      this.settingsScope = { get: () => scope.get() }
    })

    // 语音输入提示段：agents/systemPrompt 就绪时按 agent 注册。
    ctx.inject(['agents'], (agentsCtx) => {
      const install = (agent: { ctx: Context }): void => {
        agent.ctx.inject(['systemPrompt'], (scope) => {
          scope.systemPrompt.section({
            name: 'jx-speech:voice-input-hint',
            order: 950,
            text: () => Date.now() - voiceActiveAt < VOICE_HINT_WINDOW_MS ? VOICE_HINT_TEXT : '',
          })
        })
      }
      const agents = agentsCtx.agents as unknown as {
        list(): Array<{ ctx: Context }>
      }
      for (const agent of agents.list()) install(agent)
      ctx.on('agent/created', ({ agent }) => { install(agent as { ctx: Context }) })
    })
  }

  /** 转写服务是否已配置（浏览器据此决定录音回退是否可用）。 */
  async asrStatus(): Promise<{ configured: boolean, model: string }> {
    return { configured: this.asr.endpoint !== '', model: this.asr.model }
  }

  /** 转写一段录音（OpenAI 兼容端点）。未配置或失败时抛错。 */
  async transcribe(audioBase64: string, mimeType: string): Promise<{ text: string }> {
    if (this.asr.endpoint === '') throw new Error('jx-speech: ASR endpoint is not configured')
    const audio = Buffer.from(audioBase64, 'base64')
    const { body, boundary } = buildForm(audio, mimeType, this.asr.model)
    const controller = new AbortController()
    const timer = setTimeout(() => controller.abort(), 60_000)
    try {
      const res = await fetch(this.asr.endpoint, {
        method: 'POST',
        headers: {
          'content-type': `multipart/form-data; boundary=${boundary}`,
          ...(this.asr.apiKey !== '' ? { authorization: `Bearer ${this.asr.apiKey}` } : {}),
        },
        body,
        signal: controller.signal,
      })
      if (!res.ok) {
        const detail = (await res.text().catch(() => '')).slice(0, 200)
        throw new Error(`ASR HTTP ${res.status}: ${detail}`)
      }
      const payload = (await res.json()) as { text?: unknown, transcript?: unknown }
      const text = typeof payload.text === 'string' ? payload.text
        : typeof payload.transcript === 'string' ? payload.transcript : undefined
      if (typeof text !== 'string' || text.trim() === '') throw new Error('ASR response missing text')
      return { text: text.trim() }
    } finally {
      clearTimeout(timer)
    }
  }

  /** 浏览器听写开始/结束上报；窗口内系统提示词携带转写瑕疵提示。 */
  async setVoiceActive(active: boolean): Promise<{ ok: true }> {
    voiceActiveAt = active ? Date.now() : voiceActiveAt
    return { ok: true }
  }

  /** 解析润色路由：优先非推理（chat 类）模型——润色要快，reasoner 会先思考几十秒。 */
  private async resolvePolishRoute(): Promise<{ provider: string, model: string }> {
    if (this.polishRoute !== undefined && this.polishRoute !== null) return this.polishRoute
    const llm = (this.ctx.get('llm') as unknown as LlmFace | undefined)
    if (llm === undefined) throw new Error('jx-speech: llm service unavailable')
    const isReasoning = (id: string): boolean => /reason|think|r1|o1/i.test(id)
    for (const provider of llm.listProviders()) {
      try {
        const models = await llm.listModels(provider.id)
        if (models.length === 0) continue
        const chat = models.find(candidate => !isReasoning(candidate.id))
        this.polishRoute = { provider: provider.id, model: (chat ?? models[0]).id }
        return this.polishRoute
      } catch { /* 下一个 provider */ }
    }
    throw new Error('jx-speech: no configured LLM provider for polish')
  }

  /** 组装润色系统提示词：自定义提示词（空则默认）+ 常用词附注。 */
  private composePolishPrompt(): string {
    const settings = this.settingsScope?.get()
    const custom = (settings?.polishPrompt ?? '').trim()
    const words = (settings?.polishWords ?? '')
      .split(/[\n、,，;；]+/)
      .map(word => word.trim())
      .filter(word => word !== '')
    const base = custom !== '' ? custom : DEFAULT_POLISH_PROMPT
    if (words.length === 0) return base
    return `${base}\n以下是用户维护的常用词，语音识别时经常出现谐音错误，请优先按这些词修正：${words.join('、')}。`
  }

  /**
   * 保守校对一段语音转写文本：复用 DSH 已配置模型，提示词可在设置页自定义。
   * context 为可选的最近 AI 回复纯文本（设置开关控制，客户端传入），
   * 仅用于帮助模型理解上下文消歧，不参与改写语音内容本身。
   * 任何失败都抛错（客户端保留原文并提示）。
   */
  async polish(text: string, context: string): Promise<{ text: string }> {
    const raw = text.trim()
    if (raw === '' || raw.length > POLISH_MAX_CHARS) throw new Error('jx-speech: polish text empty or too long')
    const llm = (this.ctx.get('llm') as unknown as LlmFace | undefined)
    if (llm === undefined) throw new Error('jx-speech: llm service unavailable')
    const route = await this.resolvePolishRoute()
    const timeout = new AbortController()
    const timer = setTimeout(() => timeout.abort(), POLISH_TIMEOUT_MS)
    try {
      const prepared = await llm.prepareCall(route, timeout.signal)
      const { createUserMessage } = await import('@deepseek-ai/dsh-llm')
      const trimmedContext = context.trim().slice(0, POLISH_MAX_CHARS)
      const userText = trimmedContext === ''
        ? raw
        : `${raw}\n\n（以下是本次语音输入之前、最近一次 AI 回复的文本，仅供你理解上下文以消歧谐音词，不要对它做任何修改或复述：\n${trimmedContext}\n）`
      const message = createUserMessage({
        content: [{ type: 'text', text: userText }],
        source: { kind: 'user' },
      })
      let out = ''
      for await (const chunk of prepared.stream({
        ...(prepared.config as Record<string, unknown>),
        messages: [message],
        system: this.composePolishPrompt(),
        signal: timeout.signal,
      })) {
        if (chunk.type === 'text-delta' && typeof chunk.text === 'string') out += chunk.text
        else if (chunk.type === 'finish' && (chunk.reason?.kind === 'error' || chunk.reason?.kind === 'aborted')) {
          throw new Error('jx-speech: polish stream failed')
        }
      }
      const cleaned = out.trim()
      if (cleaned === '') throw new Error('jx-speech: polish returned empty')
      return { text: cleaned }
    } finally {
      clearTimeout(timer)
    }
  }

  /** 本地引擎状态（模块/模型/下载进度）。 */
  async localStatus(): Promise<ReturnType<typeof localStatus>> {
    return localStatus()
  }

  /** 触发安装（npm 装 sherpa-onnx-node + 下载 SenseVoice 模型）；后台执行，状态经 localStatus 轮询。 */
  async localSetup(): Promise<{ started: true }> {
    void ensureLocalSetup().catch(() => undefined)
    return { started: true }
  }

  /** 转写一段 16kHz float32 PCM 增量（本地 SenseVoice；无状态，客户端拼接）。 */
  async transcribePcm(pcmBase64: string): Promise<{ text: string }> {
    return { text: await transcribePcmBase64(pcmBase64) }
  }
}

// typert-protocol 远程方法标记（等价于每个方法上的 @Remote('...')）。
const directMarker = (method: string) => Object.freeze({
  method,
  invocation: Object.freeze({ kind: 'direct' }),
})
Object.defineProperty(JxSpeechGateway.prototype, REMOTE_METHODS_KEY, {
  configurable: true,
  value: Object.freeze({
    version: 1,
    methods: Object.freeze([
      directMarker('asrStatus'),
      directMarker('transcribe'),
      directMarker('setVoiceActive'),
      directMarker('polish'),
      directMarker('localStatus'),
      directMarker('localSetup'),
      directMarker('transcribePcm'),
    ]),
  }),
})

export default JxSpeechGateway
