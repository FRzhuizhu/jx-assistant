# dsh-speech 适配（插件兼容层）

DSH 插件 `dsh-speech`（原件在 `插件/dsh-speech/`，未改动）在本栈的手写转译适配。
浏览器半边在本目录（client.js / client.css，由服务注入页面）；
Go 半边在主程序根目录 `adapter_dsh_speech.go`（一体编译，按名与本目录配对）。

## 能力映射

| DSH 原功能 | 本栈适配 | 实现位置 |
|---|---|---|
| TTS 朗读：speechSynthesis、cleanForSpeech 清洗（代码块/反引号/链接/标题/列表/表格竖线/强调符）、音色选择（URI→zh-CN→zh）、单飞播放、播放中变停止 | ✅ 全量转译 | client.js（消息操作行的「朗读」按钮） |
| 听写-浏览器层：SpeechRecognition 连续识别、final 追加不覆盖、去重窗口、全角标点清洗（，→1空格 。→2空格）、Enter 发送静默停止 | ✅ 转译（简化状态机：final+去重，不含编辑保护基准/断线重连缓冲） | client.js（输入框右侧麦克风） |
| 润色：转写文本交 LLM 保守纠错（同音字/标点，不改写）；失败保留原文 | ✅ 转译：DSH 走宿主 ctx.llm → 本栈走主程序已配置的模型 | client.js 气泡按钮 + Go 半边 `POST /api/plugins/dsh-speech/polish` |
| 听写激活后 N 分钟内系统提示词附「转写瑕疵」提示段 | ✅ 轻量转译 | client.js 上报 + Go 半边 `voice-active` 端点与提示词钩子 |
| 设置区：语速滑块（0.5–3，默认 1.3）/音色下拉（中文优先）/试听/润色提示词/常用词表 | ✅ 转译 | client.js（注入设置弹窗分区；偏好存 localStorage） |
| 听写-本地离线 SenseVoice（sherpa-onnx，约 230MB 模型，宿主 Node 解码） | ❌ 未适配 | Go 侧无等价物；未来可评估进程外方案 |
| 听写-云端 ASR 回退（multipart 上传 OpenAI 兼容 /audio/transcriptions） | ❌ 未适配 | 需独立 ASR 端点与 Key；如需要可在 Go 半边补端点 |
| 热键（Alt / Alt+Space，点按或按住说话） | ⏸ 暂缓 | 后续小量补充 |
| 流式逐句朗读（边生成边读） | ❌ 不做 | DSH 原版也没有；本栈朗读以完整消息为单位 |

## 行为说明

- 开关：设置 → 插件 → 「语音（朗读与听写）」。关闭后刷新页面，按钮/气泡/设置区全部消失；
  润色与 voice-active 端点立即返回 404；系统提示词不再附加转写提示段。
- 降级：无 speechSynthesis（如 Firefox）→ 朗读按钮与音色设置不出现；
  无 SpeechRecognition → 麦克风与润色气泡不出现。核心功能不受影响。
- 偏好键（localStorage，命名空间 `dsh-speech:`）：`rate`、`voice`、`polishPrompt`、`polishWords`。

## 对照版本

适配对照 `插件/dsh-speech/` 当前版本（package.json version 见原件）。
若原件升级，请按其 README 与 src/ 变化重审上表。
