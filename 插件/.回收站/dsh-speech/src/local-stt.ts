/**
 * 本地离线语音转文字（SenseVoice int8 + sherpa-onnx-node）。
 * 依赖与模型就绪后提供无状态的增量转写：每次调用解码一段 16kHz float32 PCM，
 * 由客户端负责拼接。模型目录与 dsh-voice-scribe 共用（$DSH_HOME/voice/sensevoice），
 * 谁先下载谁生效，互不重复下载。
 * 实现参照 dsh-voice-scribe（MIT）的 local-asr.js。
 */
import { spawn } from 'node:child_process'
import { createWriteStream, existsSync, mkdirSync, renameSync, statSync } from 'node:fs'
import { createRequire } from 'node:module'
import { homedir, cpus } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { once } from 'node:events'

const require = createRequire(import.meta.url)

/** 本包目录（构建后为 lib/）。 */
const PKG_DIR = dirname(dirname(fileURLToPath(import.meta.url)))

/** 模型目录：与 dsh-voice-scribe 共用，避免重复下载 230MB。 */
export function modelDir(): string {
  const home = process.env.DSH_HOME && process.env.DSH_HOME.length > 0 ? process.env.DSH_HOME : join(homedir(), '.dsh')
  return join(home, 'voice', 'sensevoice')
}

const MODEL_BASE = 'csukuangfj/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17'
/** 国内可达的 HF 镜像优先。 */
const MODEL_MIRRORS = [
  `https://hf-mirror.com/${MODEL_BASE}/resolve/main`,
  `https://huggingface.co/${MODEL_BASE}/resolve/main`,
]

/** sherpa 原生模块版本（npm 安装目标）。 */
const SHERPA_SPEC = 'sherpa-onnx-node@^1.13.7'

/** 下载/安装状态（localStatus Remote 的数据源）。 */
const state = {
  installing: false,
  downloading: false,
  error: '',
  progress: { file: '', done: 0, total: 0 },
}

/** 当前状态快照。 */
export function localStatus(): {
  moduleInstalled: boolean
  modelReady: boolean
  busy: boolean
  error: string
  progress: { file: string, done: number, total: number }
  ready: boolean
} {
  const moduleInstalled = sherpaModule() !== undefined
  const modelReady = modelFilesReady()
  return {
    moduleInstalled,
    modelReady,
    busy: state.installing || state.downloading,
    error: state.error,
    progress: { ...state.progress },
    ready: moduleInstalled && modelReady,
  }
}

/** 惰性加载 sherpa-onnx-node（来自本包 node_modules）。 */
let sherpaCache: unknown
let sherpaLoadError = ''
function sherpaModule(): { OfflineRecognizer: new (config: unknown) => SherpaRecognizer } | undefined {
  if (sherpaCache !== undefined) return sherpaCache as never
  try {
    sherpaCache = require('sherpa-onnx-node') as never
    sherpaLoadError = ''
  } catch (error) {
    sherpaCache = undefined
    sherpaLoadError = error instanceof Error ? `${error.message} | cause: ${String((error as { cause?: unknown }).cause ?? '')}` : String(error)
  }
  return sherpaCache as never
}

interface SherpaRecognizer {
  createStream(): { acceptWaveform(input: { samples: Float32Array, sampleRate: number }): void }
  decodeAsync(stream: unknown): Promise<{ text?: unknown }>
}

let recognizer: SherpaRecognizer | null = null
let recognizerLoading: Promise<SherpaRecognizer> | null = null

/** 模型文件是否齐备（int8 模型 + 词表，均非空）。 */
export function modelFilesReady(dir = modelDir()): boolean {
  try {
    const model = join(dir, 'model.int8.onnx')
    const tokens = join(dir, 'tokens.txt')
    return statSync(model).size > 100_000_000 && statSync(tokens).size > 1000
  } catch {
    return false
  }
}

/** 取（或惰性构建）识别器单例；解码请求串行排队（句柄非线程安全）。 */
function ensureRecognizer(): Promise<SherpaRecognizer> {
  if (recognizer !== null) return Promise.resolve(recognizer)
  if (recognizerLoading !== null) return recognizerLoading
  recognizerLoading = (async () => {
    const sherpa = sherpaModule()
    if (sherpa === undefined) throw new Error('sherpa-onnx-node 未安装')
    if (!modelFilesReady()) throw new Error('本地模型未就绪')
    const dir = modelDir()
    const rec = new sherpa.OfflineRecognizer({
      modelConfig: {
        senseVoice: { model: join(dir, 'model.int8.onnx'), language: 'auto', useInverseTextNormalization: 1 },
        tokens: join(dir, 'tokens.txt'),
        provider: 'cpu',
        numThreads: Math.min(4, Math.max(1, cpus().length)),
      },
      featConfig: { sampleRate: 16000, featureDim: 80 },
    })
    recognizer = rec
    return rec
  })()
  recognizerLoading.catch(() => { recognizerLoading = null })
  return recognizerLoading
}

/** 解码队列：串行化解码，避免并发占用多核。 */
let decodeQueue: Promise<unknown> = Promise.resolve()

/** 去掉 SenseVoice 输出开头的元数据 token（<|zh|><|NEUTRAL|>…）。 */
function cleanupText(text: string): string {
  return text.replace(/<\|[^|]*\|>/g, '').replace(/\s+/g, ' ').trim()
}

/**
 * 转写一段 16kHz 单声道 float32 PCM（base64 小端字节）。
 * 无状态：每次只解码本次传入的增量；客户端拼接各段结果。
 */
export async function transcribePcmBase64(base64: string): Promise<string> {
  const buf = Buffer.from(base64, 'base64')
  if (buf.byteLength < 4) return ''
  const usable = buf.byteLength - (buf.byteLength % 4)
  const samples = new Float32Array(buf.buffer, buf.byteOffset, usable / 4)
  if (samples.length === 0) return ''
  const rec = await ensureRecognizer()
  const run = async (): Promise<string> => {
    const stream = rec.createStream()
    stream.acceptWaveform({ samples, sampleRate: 16000 })
    const result = await rec.decodeAsync(stream)
    return cleanupText(typeof result?.text === 'string' ? result.text : '')
  }
  decodeQueue = decodeQueue.then(run, run)
  return decodeQueue as Promise<string>
}

// ── 安装与下载 ─────────────────────────────────────────────────────────────

/** 在本包目录执行 npm install sherpa-onnx-node（--no-save，不动 package.json）。 */
async function installSherpaModule(): Promise<void> {
  state.installing = true
  state.error = ''
  try {
    // Windows 上直接 spawn npm.cmd 会因安全策略抛 EINVAL（CVE-2024-27980 后
    // 的行为）：优先用 node 直接执行 npm 的 JS 入口，找不到再退回 shell 方式。
    const npmCli = join(dirname(process.execPath), 'node_modules', 'npm', 'bin', 'npm-cli.js')
    const useCli = existsSync(npmCli)
    const command = useCli ? process.execPath : 'npm'
    const args = ['install', SHERPA_SPEC, '--no-save', '--no-package-lock', '--loglevel', 'error']
    const code = await new Promise<number>((resolve, reject) => {
      const child = useCli
        ? spawn(command, [npmCli, ...args], { cwd: PKG_DIR, stdio: ['ignore', 'pipe', 'pipe'], shell: false })
        : spawn(command, args, { cwd: PKG_DIR, stdio: ['ignore', 'pipe', 'pipe'], shell: true })
      child.stderr?.on('data', () => undefined)
      child.on('error', reject)
      child.on('exit', code2 => { resolve(code2 ?? 1) })
    })
    if (code !== 0) throw new Error('npm install sherpa-onnx-node 失败（退出码 ' + code + '）')
    sherpaCache = undefined
    if (sherpaModule() === undefined) throw new Error(`sherpa-onnx-node 加载失败：${sherpaLoadError.slice(0, 300)}`)
  } finally {
    state.installing = false
  }
}

/** 流式下载单个文件到目标目录（.part 原子改名；镜像逐个尝试）。 */
async function downloadFile(filename: string, targetDir: string): Promise<void> {
  const target = join(targetDir, filename)
  const part = join(targetDir, `${filename}.part`)
  const errors: string[] = []
  for (const mirror of MODEL_MIRRORS) {
    try {
      const res = await fetch(`${mirror}/${filename}`, { signal: AbortSignal.timeout(600_000) })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const total = Number(res.headers.get('content-length')) || 0
      const reader = res.body?.getReader()
      if (reader === undefined) throw new Error('no body')
      const file = createWriteStream(part)
      let writeError: Error | null = null
      const writeFailed = new Promise<never>((_, reject) => { file.once('error', err => { writeError = err; reject(err) }) })
      writeFailed.catch(() => {})
      let done = 0
      for (;;) {
        const { done: isDone, value } = await Promise.race([reader.read(), writeFailed])
        if (isDone) break
        if (value !== undefined && value.byteLength > 0) {
          done += value.byteLength
          if (!file.write(Buffer.from(value))) await Promise.race([once(file, 'drain'), writeFailed])
          state.progress = { file: filename, done, total }
        }
      }
      // Windows 上 end() 回调触发时 fd 可能尚未关闭，此时 rename 会 EPERM：
      // 等 close 事件（fd 确定释放）后再改名。
      await new Promise<void>((resolve, reject) => {
        file.on('error', reject)
        file.on('close', () => { resolve() })
        file.end()
      })
      renameSync(part, target)
      return
    } catch (error) {
      errors.push(`${mirror}: ${error instanceof Error ? error.message : String(error)}`)
    }
  }
  throw new Error(`下载 ${filename} 失败：${errors.join(' | ')}`)
}

/** 安装触发（幂等；重复调用并入进行中的任务）。 */
let setupTask: Promise<void> | null = null

/** 安装原生模块并下载模型（谁先就绪用谁；供设置页开关触发）。 */
export function ensureLocalSetup(): Promise<void> {
  if (setupTask !== null) return setupTask
  setupTask = (async () => {
    state.error = ''
    try {
      if (sherpaModule() === undefined) await installSherpaModule()
      if (!modelFilesReady()) {
        state.downloading = true
        try {
          mkdirSync(modelDir(), { recursive: true })
          await downloadFile('tokens.txt', modelDir())
          await downloadFile('model.int8.onnx', modelDir())
        } finally {
          state.downloading = false
        }
      }
    } catch (error) {
      state.error = error instanceof Error ? error.message : String(error)
      console.warn('[dsh-speech] 本地引擎安装失败：', state.error)
      setupTask = null
      throw error
    }
    setupTask = null
  })()
  return setupTask
}

/** 磁盘上是否已经存在模型（供状态展示，不触发下载）。 */
export function modelExists(): boolean {
  return existsSync(join(modelDir(), 'model.int8.onnx'))
}
