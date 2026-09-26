# dsh-speech —— 语音插件（朗读 + 语音输入）

自旧坚祥定制版合并迁移的第一个正式模组，由两部分组成：

## 功能

### 消息朗读（TTS）
- 每条助手消息的操作条上出现朗读/停止按钮（Web Speech API，纯浏览器实现）。
- 只朗读散文文本：代码块、推理、工具调用、图片自动剔除（cleanForSpeech）。
- 默认自动挑选中文系统语音；语速/音色可在 设置 → 语音 页调整并持久化（试听按钮可用）。

### 语音输入
- 输入框左侧麦克风按钮，Web Speech API 实时听写，转写文本实时追加进输入框草稿。
- **听写绝不覆盖用户编辑**：听写过程中手动修改草稿，编辑内容作为新基准，语音只在其后追加；引擎重放的片段按前缀去重消化（旧坚祥条目 45 的核心修正，原样保留）。
- 听写中直接发送消息/按 Enter：录音立即静默停止，转写文本随草稿一并发出。
- **标点转空格（仅浏览器听写）**：浏览器识别（微软/谷歌）的标点是按停顿猜的，最不可靠——其返回链路统一清洗：全角逗号→一个空格、句号→两个空格，其余标点（、？！；：）不变；半角逗号/句点不动（保护数字与英文）。本地引擎与云端转写保留原始标点。
- 浏览器无实时识别引擎（如 Firefox）时回退：MediaRecorder 录音 → 宿主 `jxSpeech.transcribe` 转写 → 文本追加进草稿（不产生音频附件）。
- 听写激活后 5 分钟内，系统提示词携带"文本可能含转写瑕疵"提示（旧条目 46 的 systemPrompt 块），帮助模型正确理解同音字/口语化表达。

## 宿主配置（可选，仅录音转写回退需要）

在 home 补丁层（`dsh-home\cordis.patch.yml`）的管理器行之外，为模组行添加 config，
或设置环境变量：

| config 键 | 环境变量 | 说明 | 默认 |
|---|---|---|---|
| `asrEndpoint` | `ASR_ENDPOINT` / `DSH_ASR_ENDPOINT` | OpenAI 兼容转写端点（POST multipart file+model） | 空（回退转写不可用，实时听写不受影响） |
| `asrModel` | `ASR_MODEL` | 转写模型名 | `whisper-1` |
| `apiKey` | `ASR_API_KEY` | Bearer 密钥 | 空 |

示例（home 补丁层追加）：

```yaml
- id: dsh-speech
  config:
    asrEndpoint: 'https://api.example.com/v1/audio/transcriptions'
    asrModel: 'whisper-1'
    apiKey: 'sk-...'
```

注意：模组行由模组管理器的补丁层（`插件\.jx-mods.patch.yml`）生成，直接给该行加 config
会被下次开关操作覆盖；**持久配置请写 home 补丁层**（`id: dsh-speech` 的行优先级更高，
config 整值替换生效）。

## 浏览器兼容

| 环境 | 实时听写 | 朗读 | 录音转写回退 |
|---|---|---|---|
| Chrome / Edge（Windows） | ✅ | ✅ | ✅（需 ASR 配置） |
| Firefox | ❌ | ❌（无 speechSynthesis 时整块隐藏） | ✅（需 ASR 配置） |

## 构建

```
cd 插件\dsh-speech
node build.mjs
```
