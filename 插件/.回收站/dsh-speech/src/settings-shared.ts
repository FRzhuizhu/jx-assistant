/** 语音插件的共享类型与常量（宿主与浏览器两侧共用，不携带宿主依赖）。 */

/** 设置命名空间（宿主注册、浏览器持久化）。 */
export const SPEECH_SETTINGS_NAMESPACE = 'dsh-speech'

/** 语速字段名。 */
export const SPEECH_RATE_FIELD = 'rate'
/** 音色字段名（系统语音 URI，'' 为自动挑选中文语音）。 */
export const SPEECH_VOICE_FIELD = 'voice'
/** 本地离线转写开关字段名。 */
export const SPEECH_LOCAL_STT_FIELD = 'localStt'
/** 热键选择字段名。 */
export const SPEECH_HOTKEY_FIELD = 'hotkey'
/** 热键触发模式字段名。 */
export const SPEECH_HOTKEY_MODE_FIELD = 'hotkeyMode'
/** 润色提示词字段名（'' = 使用内置默认）。 */
export const SPEECH_POLISH_PROMPT_FIELD = 'polishPrompt'
/** 润色常用词字段名（换行/顿号分隔）。 */
export const SPEECH_POLISH_WORDS_FIELD = 'polishWords'
/** 润色时是否附上最近 AI 回复作为参考。 */
export const SPEECH_POLISH_CONTEXT_FIELD = 'polishContext'

/** 语速下限。 */
export const SPEECH_RATE_MIN = 0.5
/** 语速上限。 */
export const SPEECH_RATE_MAX = 3
/** 语速滑块步进。 */
export const SPEECH_RATE_STEP = 0.1
/** 默认语速：1x 偏慢，1.3 为 WebView2 实测调校值。 */
export const DEFAULT_SPEECH_RATE = 1.3
/** 默认音色：'' 自动优先中文系统语音。 */
export const DEFAULT_SPEECH_VOICE = ''

/** 单次录音/听写时长上限（秒）。 */
export const MAX_RECORD_SEC = 60

/** 本地引擎听写时长上限（秒）——本地路径走录音+离线转写，放宽到 4 分钟。 */
export const MAX_LOCAL_RECORD_SEC = 240

/** 语音输入激活提示的生效窗口（毫秒）：停止听写后提示仍保留一段时间。 */
export const VOICE_HINT_WINDOW_MS = 5 * 60 * 1000

/** 润色按钮的出现窗口（毫秒）：听写结束后一段时间内可点。 */
export const POLISH_WINDOW_MS = 5 * 60 * 1000

/** 内置默认润色提示词（设置页"恢复默认"会填入此文本；空提示词也回退到它）。 */
export const DEFAULT_POLISH_PROMPT = [
  '以下是用户语音输入的内容，可能有语音输入时词语识别错误或者标点符号不正确。',
  '空格和单独出现的「I.」都代表用户说话中途的停顿。',
  '你需要进行词语谐音修正并合理安排标点符号，',
  '不要改写句式、不要增删内容、不要润色文风、不要解释，只输出修正后的文字本身。',
].join('')

/** 热键选项。 */
export const SPEECH_HOTKEYS = ['alt', 'alt-space'] as const
export type SpeechHotkey = typeof SPEECH_HOTKEYS[number]

/** 热键触发模式：tap = 点按切换，hold = 按住说话。 */
export const SPEECH_HOTKEY_MODES = ['tap', 'hold'] as const
export type SpeechHotkeyMode = typeof SPEECH_HOTKEY_MODES[number]

/** durable 语音偏好（宿主 schema 与浏览器 scope 共用形状）。 */
export interface SpeechSettings {
  /** 语速倍率，介于 SPEECH_RATE_MIN 与 SPEECH_RATE_MAX 之间。 */
  rate: number
  /** 选中的系统语音 URI；'' 自动挑选中文语音。 */
  voice: string
  /** 是否优先使用本地离线语音转文字（依赖就绪时）。 */
  localStt: boolean
  /** 语音输入热键。 */
  hotkey: SpeechHotkey
  /** 热键触发模式。 */
  hotkeyMode: SpeechHotkeyMode
  /** 润色提示词（'' = 内置默认；常用词会自动附在末尾）。 */
  polishPrompt: string
  /** 润色常用词（换行或顿号分隔）。 */
  polishWords: string
  /** 润色时是否把最近一次 AI 回复的文本附给模型作参考。 */
  polishContext: boolean
}
