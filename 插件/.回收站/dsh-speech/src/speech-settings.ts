/** 宿主侧语音偏好 schema（浏览器侧只引用 settings-shared 的常量与类型）。 */

import z from '@deepseek-ai/schemastery'
import {
  DEFAULT_SPEECH_RATE, DEFAULT_SPEECH_VOICE, SPEECH_HOTKEY_FIELD, SPEECH_HOTKEY_MODES, SPEECH_HOTKEY_MODE_FIELD,
  SPEECH_LOCAL_STT_FIELD, SPEECH_POLISH_CONTEXT_FIELD, SPEECH_POLISH_PROMPT_FIELD, SPEECH_POLISH_WORDS_FIELD,
  SPEECH_RATE_FIELD, SPEECH_RATE_MAX, SPEECH_RATE_MIN, SPEECH_VOICE_FIELD, type SpeechSettings,
} from './settings-shared.ts'

export * from './settings-shared.ts'

/** durable 语音偏好 schema；同时是浏览器 scope 校验的线格式。 */
export const SpeechSettingsSchema: z<SpeechSettings> = z.object({
  [SPEECH_RATE_FIELD]: z.number().min(SPEECH_RATE_MIN).max(SPEECH_RATE_MAX).default(DEFAULT_SPEECH_RATE),
  [SPEECH_VOICE_FIELD]: z.string().default(DEFAULT_SPEECH_VOICE),
  [SPEECH_LOCAL_STT_FIELD]: z.boolean().default(false),
  [SPEECH_HOTKEY_FIELD]: z.union(['alt', 'alt-space']).default('alt'),
  [SPEECH_HOTKEY_MODE_FIELD]: z.union([...SPEECH_HOTKEY_MODES]).default('tap'),
  [SPEECH_POLISH_PROMPT_FIELD]: z.string().max(8000).default(''),
  [SPEECH_POLISH_WORDS_FIELD]: z.string().max(4000).default(''),
  [SPEECH_POLISH_CONTEXT_FIELD]: z.boolean().default(false),
})
