/**
 * 手写的 Typert Remote 客户端贡献（与官方 typert.remote-client.js 生成工件同构）。
 * 由客户端半边经 ctx.remote.$mount() 挂载，得到 ctx.remote.jxSpeech。
 */
import { z } from 'zod'

const asrStatusSchema = z.object({
  configured: z.boolean(),
  model: z.string(),
})

const transcribeResultSchema = z.object({
  text: z.string(),
})

const voiceActiveResultSchema = z.object({
  ok: z.literal(true),
})

const localProgressSchema = z.object({
  file: z.string(),
  done: z.number(),
  total: z.number(),
})

const localStatusSchema = z.object({
  moduleInstalled: z.boolean(),
  modelReady: z.boolean(),
  busy: z.boolean(),
  error: z.string(),
  progress: localProgressSchema,
  ready: z.boolean(),
})

const localSetupResultSchema = z.object({
  started: z.literal(true),
})

const pcmTranscribeResultSchema = z.object({
  text: z.string(),
})

const strParam = (name: string, symbol: string) => ({
  name,
  wire: name,
  source: 'json',
  codec: { mode: 'strict' as const, typeSymbol: symbol, create: () => z.string() },
})

export const TYPERT_REMOTE = {
  package: 'dsh-speech',
  descriptors: [
    {
      id: 'dsh-speech#jxSpeech/asrStatus',
      service: 'jxSpeech',
      namespace: 'jxSpeech',
      method: 'asrStatus',
      invocation: { kind: 'direct' },
      parameters: [],
      result: { mode: 'strict', typeSymbol: 'dsh-speech#jxSpeech/asrStatus:result', create: () => asrStatusSchema },
      sourceLocation: { file: 'src/index.ts', line: 1, column: 3 },
    },
    {
      id: 'dsh-speech#jxSpeech/transcribe',
      service: 'jxSpeech',
      namespace: 'jxSpeech',
      method: 'transcribe',
      invocation: { kind: 'direct' },
      parameters: [
        strParam('audioBase64', 'dsh-speech#jxSpeech/transcribe:audioBase64'),
        strParam('mimeType', 'dsh-speech#jxSpeech/transcribe:mimeType'),
      ],
      result: { mode: 'strict', typeSymbol: 'dsh-speech#jxSpeech/transcribe:result', create: () => transcribeResultSchema },
      sourceLocation: { file: 'src/index.ts', line: 1, column: 3 },
    },
    {
      id: 'dsh-speech#jxSpeech/setVoiceActive',
      service: 'jxSpeech',
      namespace: 'jxSpeech',
      method: 'setVoiceActive',
      invocation: { kind: 'direct' },
      parameters: [
        {
          name: 'active',
          wire: 'active',
          source: 'json',
          codec: { mode: 'strict', typeSymbol: 'dsh-speech#jxSpeech/setVoiceActive:active', create: () => z.boolean() },
        },
      ],
      result: { mode: 'strict', typeSymbol: 'dsh-speech#jxSpeech/setVoiceActive:result', create: () => voiceActiveResultSchema },
      sourceLocation: { file: 'src/index.ts', line: 1, column: 3 },
    },
    {
      id: 'dsh-speech#jxSpeech/polish',
      service: 'jxSpeech',
      namespace: 'jxSpeech',
      method: 'polish',
      invocation: { kind: 'direct' },
      parameters: [
        strParam('text', 'dsh-speech#jxSpeech/polish:text'),
        strParam('context', 'dsh-speech#jxSpeech/polish:context'),
      ],
      result: { mode: 'strict', typeSymbol: 'dsh-speech#jxSpeech/polish:result', create: () => transcribeResultSchema },
      sourceLocation: { file: 'src/index.ts', line: 1, column: 3 },
    },
    {
      id: 'dsh-speech#jxSpeech/localStatus',
      service: 'jxSpeech',
      namespace: 'jxSpeech',
      method: 'localStatus',
      invocation: { kind: 'direct' },
      parameters: [],
      result: { mode: 'strict', typeSymbol: 'dsh-speech#jxSpeech/localStatus:result', create: () => localStatusSchema },
      sourceLocation: { file: 'src/index.ts', line: 1, column: 3 },
    },
    {
      id: 'dsh-speech#jxSpeech/localSetup',
      service: 'jxSpeech',
      namespace: 'jxSpeech',
      method: 'localSetup',
      invocation: { kind: 'direct' },
      parameters: [],
      result: { mode: 'strict', typeSymbol: 'dsh-speech#jxSpeech/localSetup:result', create: () => localSetupResultSchema },
      sourceLocation: { file: 'src/index.ts', line: 1, column: 3 },
    },
    {
      id: 'dsh-speech#jxSpeech/transcribePcm',
      service: 'jxSpeech',
      namespace: 'jxSpeech',
      method: 'transcribePcm',
      invocation: { kind: 'direct' },
      parameters: [strParam('pcmBase64', 'dsh-speech#jxSpeech/transcribePcm:pcmBase64')],
      result: { mode: 'strict', typeSymbol: 'dsh-speech#jxSpeech/transcribePcm:result', create: () => pcmTranscribeResultSchema },
      sourceLocation: { file: 'src/index.ts', line: 1, column: 3 },
    },
  ],
}

export default TYPERT_REMOTE
