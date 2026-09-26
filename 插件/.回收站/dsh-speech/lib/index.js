import { createRequire } from "node:module";
import { TypertRemoteService } from "@deepseek-ai/dsh-typert-protocol";
import z from "@deepseek-ai/schemastery";
import { spawn } from "node:child_process";
import { createWriteStream, existsSync, mkdirSync, renameSync, statSync } from "node:fs";
import { cpus, homedir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { once } from "node:events";
//#region src/settings-shared.ts
/** 语音插件的共享类型与常量（宿主与浏览器两侧共用，不携带宿主依赖）。 */
/** 设置命名空间（宿主注册、浏览器持久化）。 */
const SPEECH_SETTINGS_NAMESPACE = "dsh-speech";
/** 语速字段名。 */
const SPEECH_RATE_FIELD = "rate";
/** 音色字段名（系统语音 URI，'' 为自动挑选中文语音）。 */
const SPEECH_VOICE_FIELD = "voice";
/** 本地离线转写开关字段名。 */
const SPEECH_LOCAL_STT_FIELD = "localStt";
/** 热键选择字段名。 */
const SPEECH_HOTKEY_FIELD = "hotkey";
/** 热键触发模式字段名。 */
const SPEECH_HOTKEY_MODE_FIELD = "hotkeyMode";
/** 润色提示词字段名（'' = 使用内置默认）。 */
const SPEECH_POLISH_PROMPT_FIELD = "polishPrompt";
/** 润色常用词字段名（换行/顿号分隔）。 */
const SPEECH_POLISH_WORDS_FIELD = "polishWords";
/** 润色时是否附上最近 AI 回复作为参考。 */
const SPEECH_POLISH_CONTEXT_FIELD = "polishContext";
/** 语速下限。 */
const SPEECH_RATE_MIN = .5;
/** 语速上限。 */
const SPEECH_RATE_MAX = 3;
/** 语速滑块步进。 */
const SPEECH_RATE_STEP = .1;
/** 默认语速：1x 偏慢，1.3 为 WebView2 实测调校值。 */
const DEFAULT_SPEECH_RATE = 1.3;
/** 默认音色：'' 自动优先中文系统语音。 */
const DEFAULT_SPEECH_VOICE = "";
/** 单次录音/听写时长上限（秒）。 */
const MAX_RECORD_SEC = 60;
/** 本地引擎听写时长上限（秒）——本地路径走录音+离线转写，放宽到 4 分钟。 */
const MAX_LOCAL_RECORD_SEC = 240;
/** 语音输入激活提示的生效窗口（毫秒）：停止听写后提示仍保留一段时间。 */
const VOICE_HINT_WINDOW_MS = 300 * 1e3;
/** 润色按钮的出现窗口（毫秒）：听写结束后一段时间内可点。 */
const POLISH_WINDOW_MS = 300 * 1e3;
/** 内置默认润色提示词（设置页"恢复默认"会填入此文本；空提示词也回退到它）。 */
const DEFAULT_POLISH_PROMPT = [
	"以下是用户语音输入的内容，可能有语音输入时词语识别错误或者标点符号不正确。",
	"空格和单独出现的「I.」都代表用户说话中途的停顿。",
	"你需要进行词语谐音修正并合理安排标点符号，",
	"不要改写句式、不要增删内容、不要润色文风、不要解释，只输出修正后的文字本身。"
].join("");
/** 热键选项。 */
const SPEECH_HOTKEYS = ["alt", "alt-space"];
/** 热键触发模式：tap = 点按切换，hold = 按住说话。 */
const SPEECH_HOTKEY_MODES = ["tap", "hold"];
//#endregion
//#region src/speech-settings.ts
/** 宿主侧语音偏好 schema（浏览器侧只引用 settings-shared 的常量与类型）。 */
/** durable 语音偏好 schema；同时是浏览器 scope 校验的线格式。 */
const SpeechSettingsSchema = z.object({
	[SPEECH_RATE_FIELD]: z.number().min(SPEECH_RATE_MIN).max(3).default(DEFAULT_SPEECH_RATE),
	[SPEECH_VOICE_FIELD]: z.string().default(""),
	[SPEECH_LOCAL_STT_FIELD]: z.boolean().default(false),
	[SPEECH_HOTKEY_FIELD]: z.union(["alt", "alt-space"]).default("alt"),
	[SPEECH_HOTKEY_MODE_FIELD]: z.union([...SPEECH_HOTKEY_MODES]).default("tap"),
	[SPEECH_POLISH_PROMPT_FIELD]: z.string().max(8e3).default(""),
	[SPEECH_POLISH_WORDS_FIELD]: z.string().max(4e3).default(""),
	[SPEECH_POLISH_CONTEXT_FIELD]: z.boolean().default(false)
});
//#endregion
//#region src/local-stt.ts
/**
* 本地离线语音转文字（SenseVoice int8 + sherpa-onnx-node）。
* 依赖与模型就绪后提供无状态的增量转写：每次调用解码一段 16kHz float32 PCM，
* 由客户端负责拼接。模型目录与 dsh-voice-scribe 共用（$DSH_HOME/voice/sensevoice），
* 谁先下载谁生效，互不重复下载。
* 实现参照 dsh-voice-scribe（MIT）的 local-asr.js。
*/
const require = createRequire(import.meta.url);
/** 本包目录（构建后为 lib/）。 */
const PKG_DIR = dirname(dirname(fileURLToPath(import.meta.url)));
/** 模型目录：与 dsh-voice-scribe 共用，避免重复下载 230MB。 */
function modelDir() {
	return join(process.env.DSH_HOME && process.env.DSH_HOME.length > 0 ? process.env.DSH_HOME : join(homedir(), ".dsh"), "voice", "sensevoice");
}
const MODEL_BASE = "csukuangfj/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17";
/** 国内可达的 HF 镜像优先。 */
const MODEL_MIRRORS = [`https://hf-mirror.com/${MODEL_BASE}/resolve/main`, `https://huggingface.co/${MODEL_BASE}/resolve/main`];
/** sherpa 原生模块版本（npm 安装目标）。 */
const SHERPA_SPEC = "sherpa-onnx-node@^1.13.7";
/** 下载/安装状态（localStatus Remote 的数据源）。 */
const state = {
	installing: false,
	downloading: false,
	error: "",
	progress: {
		file: "",
		done: 0,
		total: 0
	}
};
/** 当前状态快照。 */
function localStatus() {
	const moduleInstalled = sherpaModule() !== void 0;
	const modelReady = modelFilesReady();
	return {
		moduleInstalled,
		modelReady,
		busy: state.installing || state.downloading,
		error: state.error,
		progress: { ...state.progress },
		ready: moduleInstalled && modelReady
	};
}
/** 惰性加载 sherpa-onnx-node（来自本包 node_modules）。 */
let sherpaCache;
let sherpaLoadError = "";
function sherpaModule() {
	if (sherpaCache !== void 0) return sherpaCache;
	try {
		sherpaCache = require("sherpa-onnx-node");
		sherpaLoadError = "";
	} catch (error) {
		sherpaCache = void 0;
		sherpaLoadError = error instanceof Error ? `${error.message} | cause: ${String(error.cause ?? "")}` : String(error);
	}
	return sherpaCache;
}
let recognizer = null;
let recognizerLoading = null;
/** 模型文件是否齐备（int8 模型 + 词表，均非空）。 */
function modelFilesReady(dir = modelDir()) {
	try {
		const model = join(dir, "model.int8.onnx");
		const tokens = join(dir, "tokens.txt");
		return statSync(model).size > 1e8 && statSync(tokens).size > 1e3;
	} catch {
		return false;
	}
}
/** 取（或惰性构建）识别器单例；解码请求串行排队（句柄非线程安全）。 */
function ensureRecognizer() {
	if (recognizer !== null) return Promise.resolve(recognizer);
	if (recognizerLoading !== null) return recognizerLoading;
	recognizerLoading = (async () => {
		const sherpa = sherpaModule();
		if (sherpa === void 0) throw new Error("sherpa-onnx-node 未安装");
		if (!modelFilesReady()) throw new Error("本地模型未就绪");
		const dir = modelDir();
		const rec = new sherpa.OfflineRecognizer({
			modelConfig: {
				senseVoice: {
					model: join(dir, "model.int8.onnx"),
					language: "auto",
					useInverseTextNormalization: 1
				},
				tokens: join(dir, "tokens.txt"),
				provider: "cpu",
				numThreads: Math.min(4, Math.max(1, cpus().length))
			},
			featConfig: {
				sampleRate: 16e3,
				featureDim: 80
			}
		});
		recognizer = rec;
		return rec;
	})();
	recognizerLoading.catch(() => {
		recognizerLoading = null;
	});
	return recognizerLoading;
}
/** 解码队列：串行化解码，避免并发占用多核。 */
let decodeQueue = Promise.resolve();
/** 去掉 SenseVoice 输出开头的元数据 token（<|zh|><|NEUTRAL|>…）。 */
function cleanupText(text) {
	return text.replace(/<\|[^|]*\|>/g, "").replace(/\s+/g, " ").trim();
}
/**
* 转写一段 16kHz 单声道 float32 PCM（base64 小端字节）。
* 无状态：每次只解码本次传入的增量；客户端拼接各段结果。
*/
async function transcribePcmBase64(base64) {
	const buf = Buffer.from(base64, "base64");
	if (buf.byteLength < 4) return "";
	const usable = buf.byteLength - buf.byteLength % 4;
	const samples = new Float32Array(buf.buffer, buf.byteOffset, usable / 4);
	if (samples.length === 0) return "";
	const rec = await ensureRecognizer();
	const run = async () => {
		const stream = rec.createStream();
		stream.acceptWaveform({
			samples,
			sampleRate: 16e3
		});
		const result = await rec.decodeAsync(stream);
		return cleanupText(typeof result?.text === "string" ? result.text : "");
	};
	decodeQueue = decodeQueue.then(run, run);
	return decodeQueue;
}
/** 在本包目录执行 npm install sherpa-onnx-node（--no-save，不动 package.json）。 */
async function installSherpaModule() {
	state.installing = true;
	state.error = "";
	try {
		const npmCli = join(dirname(process.execPath), "node_modules", "npm", "bin", "npm-cli.js");
		const useCli = existsSync(npmCli);
		const command = useCli ? process.execPath : "npm";
		const args = [
			"install",
			SHERPA_SPEC,
			"--no-save",
			"--no-package-lock",
			"--loglevel",
			"error"
		];
		const code = await new Promise((resolve, reject) => {
			const child = useCli ? spawn(command, [npmCli, ...args], {
				cwd: PKG_DIR,
				stdio: [
					"ignore",
					"pipe",
					"pipe"
				],
				shell: false
			}) : spawn(command, args, {
				cwd: PKG_DIR,
				stdio: [
					"ignore",
					"pipe",
					"pipe"
				],
				shell: true
			});
			child.stderr?.on("data", () => void 0);
			child.on("error", reject);
			child.on("exit", (code2) => {
				resolve(code2 ?? 1);
			});
		});
		if (code !== 0) throw new Error("npm install sherpa-onnx-node 失败（退出码 " + code + "）");
		sherpaCache = void 0;
		if (sherpaModule() === void 0) throw new Error(`sherpa-onnx-node 加载失败：${sherpaLoadError.slice(0, 300)}`);
	} finally {
		state.installing = false;
	}
}
/** 流式下载单个文件到目标目录（.part 原子改名；镜像逐个尝试）。 */
async function downloadFile(filename, targetDir) {
	const target = join(targetDir, filename);
	const part = join(targetDir, `${filename}.part`);
	const errors = [];
	for (const mirror of MODEL_MIRRORS) try {
		const res = await fetch(`${mirror}/${filename}`, { signal: AbortSignal.timeout(6e5) });
		if (!res.ok) throw new Error(`HTTP ${res.status}`);
		const total = Number(res.headers.get("content-length")) || 0;
		const reader = res.body?.getReader();
		if (reader === void 0) throw new Error("no body");
		const file = createWriteStream(part);
		const writeFailed = new Promise((_, reject) => {
			file.once("error", (err) => {
				reject(err);
			});
		});
		writeFailed.catch(() => {});
		let done = 0;
		for (;;) {
			const { done: isDone, value } = await Promise.race([reader.read(), writeFailed]);
			if (isDone) break;
			if (value !== void 0 && value.byteLength > 0) {
				done += value.byteLength;
				if (!file.write(Buffer.from(value))) await Promise.race([once(file, "drain"), writeFailed]);
				state.progress = {
					file: filename,
					done,
					total
				};
			}
		}
		await new Promise((resolve, reject) => {
			file.on("error", reject);
			file.on("close", () => {
				resolve();
			});
			file.end();
		});
		renameSync(part, target);
		return;
	} catch (error) {
		errors.push(`${mirror}: ${error instanceof Error ? error.message : String(error)}`);
	}
	throw new Error(`下载 ${filename} 失败：${errors.join(" | ")}`);
}
/** 安装触发（幂等；重复调用并入进行中的任务）。 */
let setupTask = null;
/** 安装原生模块并下载模型（谁先就绪用谁；供设置页开关触发）。 */
function ensureLocalSetup() {
	if (setupTask !== null) return setupTask;
	setupTask = (async () => {
		state.error = "";
		try {
			if (sherpaModule() === void 0) await installSherpaModule();
			if (!modelFilesReady()) {
				state.downloading = true;
				try {
					mkdirSync(modelDir(), { recursive: true });
					await downloadFile("tokens.txt", modelDir());
					await downloadFile("model.int8.onnx", modelDir());
				} finally {
					state.downloading = false;
				}
			}
		} catch (error) {
			state.error = error instanceof Error ? error.message : String(error);
			console.warn("[dsh-speech] 本地引擎安装失败：", state.error);
			setupTask = null;
			throw error;
		}
		setupTask = null;
	})();
	return setupTask;
}
//#endregion
//#region src/index.ts
const REMOTE_METHODS_KEY = "@deepseek-ai/dsh-typert-protocol/remote-methods";
/** 最近一次听写激活的时间（提示是尽力而为的全局窗口）。 */
let voiceActiveAt = 0;
const VOICE_HINT_TEXT = ["（用户刚刚使用语音输入：最新消息可能包含语音转写瑕疵——同音字、错别字、", "标点缺失或口语化表达。请按语境理解真实意图，不要字面纠缠；如关键信息存疑再向用户确认。）"].join("");
/** 一次润色的超时。 */
const POLISH_TIMEOUT_MS = 3e4;
/** 润色文本长度上限。 */
const POLISH_MAX_CHARS = 12e3;
/** 依据 MIME 推断转写上传文件名扩展名。 */
function extOf(mimeType) {
	const mime = mimeType.toLowerCase();
	if (mime.includes("mp4") || mime.includes("aac")) return "m4a";
	if (mime.includes("ogg")) return "ogg";
	if (mime.includes("wav")) return "wav";
	if (mime.includes("mpeg")) return "mp3";
	return "webm";
}
/** 构造 OpenAI 兼容 /audio/transcriptions 的 multipart 表单体。 */
function buildForm(audio, mimeType, model) {
	const boundary = `----dsh-speech-${Date.now().toString(36)}`;
	const filename = `voice-${Date.now().toString(36)}.${extOf(mimeType)}`;
	const parts = [
		Buffer.from(`--${boundary}\r\n`),
		Buffer.from(`Content-Disposition: form-data; name="file"; filename="${filename}"\r\n`),
		Buffer.from(`Content-Type: ${mimeType || "application/octet-stream"}\r\n\r\n`),
		audio,
		Buffer.from(`\r\n--${boundary}\r\n`),
		Buffer.from(`Content-Disposition: form-data; name="model"\r\n\r\n${model}\r\n`),
		Buffer.from(`--${boundary}--\r\n`)
	];
	return {
		body: Buffer.concat(parts),
		boundary
	};
}
var JxSpeechGateway = class extends TypertRemoteService {
	/** 解析后的 ASR 配置（env 回退）。 */
	asr;
	/** 缓存的润色路由（provider/model），首次调用时解析。 */
	polishRoute;
	/** durable 设置读取面（settings 服务就绪后由 register 返回）。 */
	settingsScope;
	constructor(ctx, config = {}) {
		super(ctx, "jxSpeech");
		this.asr = {
			endpoint: config.asrEndpoint ?? process.env.ASR_ENDPOINT ?? process.env.DSH_ASR_ENDPOINT ?? "",
			model: config.asrModel ?? process.env.ASR_MODEL ?? "whisper-1",
			apiKey: config.apiKey ?? process.env.ASR_API_KEY ?? ""
		};
		ctx.inject(["settings"], (settingsCtx) => {
			const scope = settingsCtx.settings.register(SPEECH_SETTINGS_NAMESPACE, SpeechSettingsSchema);
			this.settingsScope = { get: () => scope.get() };
		});
		ctx.inject(["agents"], (agentsCtx) => {
			const install = (agent) => {
				agent.ctx.inject(["systemPrompt"], (scope) => {
					scope.systemPrompt.section({
						name: "jx-speech:voice-input-hint",
						order: 950,
						text: () => Date.now() - voiceActiveAt < 3e5 ? VOICE_HINT_TEXT : ""
					});
				});
			};
			const agents = agentsCtx.agents;
			for (const agent of agents.list()) install(agent);
			ctx.on("agent/created", ({ agent }) => {
				install(agent);
			});
		});
	}
	/** 转写服务是否已配置（浏览器据此决定录音回退是否可用）。 */
	async asrStatus() {
		return {
			configured: this.asr.endpoint !== "",
			model: this.asr.model
		};
	}
	/** 转写一段录音（OpenAI 兼容端点）。未配置或失败时抛错。 */
	async transcribe(audioBase64, mimeType) {
		if (this.asr.endpoint === "") throw new Error("jx-speech: ASR endpoint is not configured");
		const { body, boundary } = buildForm(Buffer.from(audioBase64, "base64"), mimeType, this.asr.model);
		const controller = new AbortController();
		const timer = setTimeout(() => controller.abort(), 6e4);
		try {
			const res = await fetch(this.asr.endpoint, {
				method: "POST",
				headers: {
					"content-type": `multipart/form-data; boundary=${boundary}`,
					...this.asr.apiKey !== "" ? { authorization: `Bearer ${this.asr.apiKey}` } : {}
				},
				body,
				signal: controller.signal
			});
			if (!res.ok) {
				const detail = (await res.text().catch(() => "")).slice(0, 200);
				throw new Error(`ASR HTTP ${res.status}: ${detail}`);
			}
			const payload = await res.json();
			const text = typeof payload.text === "string" ? payload.text : typeof payload.transcript === "string" ? payload.transcript : void 0;
			if (typeof text !== "string" || text.trim() === "") throw new Error("ASR response missing text");
			return { text: text.trim() };
		} finally {
			clearTimeout(timer);
		}
	}
	/** 浏览器听写开始/结束上报；窗口内系统提示词携带转写瑕疵提示。 */
	async setVoiceActive(active) {
		voiceActiveAt = active ? Date.now() : voiceActiveAt;
		return { ok: true };
	}
	/** 解析润色路由：优先非推理（chat 类）模型——润色要快，reasoner 会先思考几十秒。 */
	async resolvePolishRoute() {
		if (this.polishRoute !== void 0 && this.polishRoute !== null) return this.polishRoute;
		const llm = this.ctx.get("llm");
		if (llm === void 0) throw new Error("jx-speech: llm service unavailable");
		const isReasoning = (id) => /reason|think|r1|o1/i.test(id);
		for (const provider of llm.listProviders()) try {
			const models = await llm.listModels(provider.id);
			if (models.length === 0) continue;
			const chat = models.find((candidate) => !isReasoning(candidate.id));
			this.polishRoute = {
				provider: provider.id,
				model: (chat ?? models[0]).id
			};
			return this.polishRoute;
		} catch {}
		throw new Error("jx-speech: no configured LLM provider for polish");
	}
	/** 组装润色系统提示词：自定义提示词（空则默认）+ 常用词附注。 */
	composePolishPrompt() {
		const settings = this.settingsScope?.get();
		const custom = (settings?.polishPrompt ?? "").trim();
		const words = (settings?.polishWords ?? "").split(/[\n、,，;；]+/).map((word) => word.trim()).filter((word) => word !== "");
		const base = custom !== "" ? custom : DEFAULT_POLISH_PROMPT;
		if (words.length === 0) return base;
		return `${base}\n以下是用户维护的常用词，语音识别时经常出现谐音错误，请优先按这些词修正：${words.join("、")}。`;
	}
	/**
	* 保守校对一段语音转写文本：复用 DSH 已配置模型，提示词可在设置页自定义。
	* context 为可选的最近 AI 回复纯文本（设置开关控制，客户端传入），
	* 仅用于帮助模型理解上下文消歧，不参与改写语音内容本身。
	* 任何失败都抛错（客户端保留原文并提示）。
	*/
	async polish(text, context) {
		const raw = text.trim();
		if (raw === "" || raw.length > POLISH_MAX_CHARS) throw new Error("jx-speech: polish text empty or too long");
		const llm = this.ctx.get("llm");
		if (llm === void 0) throw new Error("jx-speech: llm service unavailable");
		const route = await this.resolvePolishRoute();
		const timeout = new AbortController();
		const timer = setTimeout(() => timeout.abort(), POLISH_TIMEOUT_MS);
		try {
			const prepared = await llm.prepareCall(route, timeout.signal);
			const { createUserMessage } = await import("@deepseek-ai/dsh-llm");
			const trimmedContext = context.trim().slice(0, POLISH_MAX_CHARS);
			const message = createUserMessage({
				content: [{
					type: "text",
					text: trimmedContext === "" ? raw : `${raw}\n\n（以下是本次语音输入之前、最近一次 AI 回复的文本，仅供你理解上下文以消歧谐音词，不要对它做任何修改或复述：\n${trimmedContext}\n）`
				}],
				source: { kind: "user" }
			});
			let out = "";
			for await (const chunk of prepared.stream({
				...prepared.config,
				messages: [message],
				system: this.composePolishPrompt(),
				signal: timeout.signal
			})) if (chunk.type === "text-delta" && typeof chunk.text === "string") out += chunk.text;
			else if (chunk.type === "finish" && (chunk.reason?.kind === "error" || chunk.reason?.kind === "aborted")) throw new Error("jx-speech: polish stream failed");
			const cleaned = out.trim();
			if (cleaned === "") throw new Error("jx-speech: polish returned empty");
			return { text: cleaned };
		} finally {
			clearTimeout(timer);
		}
	}
	/** 本地引擎状态（模块/模型/下载进度）。 */
	async localStatus() {
		return localStatus();
	}
	/** 触发安装（npm 装 sherpa-onnx-node + 下载 SenseVoice 模型）；后台执行，状态经 localStatus 轮询。 */
	async localSetup() {
		ensureLocalSetup().catch(() => void 0);
		return { started: true };
	}
	/** 转写一段 16kHz float32 PCM 增量（本地 SenseVoice；无状态，客户端拼接）。 */
	async transcribePcm(pcmBase64) {
		return { text: await transcribePcmBase64(pcmBase64) };
	}
};
const directMarker = (method) => Object.freeze({
	method,
	invocation: Object.freeze({ kind: "direct" })
});
Object.defineProperty(JxSpeechGateway.prototype, REMOTE_METHODS_KEY, {
	configurable: true,
	value: Object.freeze({
		version: 1,
		methods: Object.freeze([
			directMarker("asrStatus"),
			directMarker("transcribe"),
			directMarker("setVoiceActive"),
			directMarker("polish"),
			directMarker("localStatus"),
			directMarker("localSetup"),
			directMarker("transcribePcm")
		])
	})
});
//#endregion
export { DEFAULT_POLISH_PROMPT, DEFAULT_SPEECH_RATE, DEFAULT_SPEECH_VOICE, JxSpeechGateway, JxSpeechGateway as default, MAX_LOCAL_RECORD_SEC, MAX_RECORD_SEC, POLISH_WINDOW_MS, SPEECH_HOTKEYS, SPEECH_HOTKEY_FIELD, SPEECH_HOTKEY_MODES, SPEECH_HOTKEY_MODE_FIELD, SPEECH_LOCAL_STT_FIELD, SPEECH_POLISH_CONTEXT_FIELD, SPEECH_POLISH_PROMPT_FIELD, SPEECH_POLISH_WORDS_FIELD, SPEECH_RATE_FIELD, SPEECH_RATE_MAX, SPEECH_RATE_MIN, SPEECH_RATE_STEP, SPEECH_SETTINGS_NAMESPACE, SPEECH_VOICE_FIELD, SpeechSettingsSchema, VOICE_HINT_WINDOW_MS };
