/**
 * 消息操作条上的朗读/停止按钮。通过 chat 快照找到该消息所在 turn-tail 的
 * 收尾文本（推理、工具调用、图片永不进入语音引擎），驱动共享播放控制器。
 * 自旧坚祥 ui-speech 迁移：取文本改为新版 useChat 快照（V3 节点结构），
 * 图标改为内联 SVG（新版 ui-primitives 无喇叭图标）。
 */
import { useEffect } from 'react'
import { Tooltip } from '@deepseek-ai/dsh-client-ui-primitives'

/** 注入的业务面：共享控制器上的播放动作。 */
export interface TtsActionInjected {
  hooks: {
    /** 正在朗读的消息 id 列表（框架绑定为 useSpeaking）。 */
    speaking: { getSnapshot(): readonly string[], subscribe(fn: () => void): () => void }
  }
  /** 朗读一条消息的清洗后文本；停止当前播放。 */
  speak: (messageId: string, text: string) => void
  /** 停止当前播放。 */
  stop: () => void
}

/** 组件完整属性：运行时份额（messageId、useChat）+ 注入面 + 文案。 */
export type TtsActionProps = {
  messageId: string
  useChat: (selector: (snapshot: unknown) => unknown) => unknown
  useSpeaking: (selector: (ids: readonly string[]) => unknown) => unknown
  t: (key: string) => string
} & TtsActionInjected

/** chat 快照节点的窄化形状（鸭子类型，避免构建期依赖 ui-chat）。 */
interface TailNode {
  kind: string
  data: {
    closing?: {
      finalNode?: { messageId?: string }
      blocks?: ReadonlyArray<{ kind: string, text?: string }>
    } | null
  } | null
}

/** 收尾块中的纯文本；只有散文会被朗读。 */
function closingText(closing: NonNullable<NonNullable<TailNode['data']>['closing']>): string {
  let prose = ''
  for (const block of closing.blocks ?? []) {
    if (block.kind === 'text' && typeof block.text === 'string') prose += block.text
  }
  return prose
}

/** 从 chat 快照中找到收尾于 messageId 的 turn-tail 散文。 */
function messageProse(snapshot: unknown, messageId: string): string {
  const nodes = (snapshot as { nodes?: { values?(): Iterable<TailNode> } } | null)?.nodes
  if (nodes === undefined || typeof nodes.values !== 'function') return ''
  for (const node of nodes.values()) {
    if (node.kind !== 'turn-tail') continue
    const closing = node.data?.closing
    if (closing === undefined || closing === null) continue
    if (closing.finalNode?.messageId !== messageId) continue
    return closingText(closing)
  }
  return ''
}

/** 喇叭图标（内联 SVG）。 */
function SpeakerIcon({ stop }: { stop: boolean }): React.ReactNode {
  return stop ? (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <path d="M8.5 2.5v11a.6.6 0 0 1-.98.46L4.2 11H2.6A1.1 1.1 0 0 1 1.5 9.9V6.1A1.1 1.1 0 0 1 2.6 5h1.6l3.32-2.96A.6.6 0 0 1 8.5 2.5Z" fill="currentColor" />
      <path d="M11 6l4 4m0-4l-4 4" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" />
    </svg>
  ) : (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
      <path d="M8.5 2.5v11a.6.6 0 0 1-.98.46L4.2 11H2.6A1.1 1.1 0 0 1 1.5 9.9V6.1A1.1 1.1 0 0 1 2.6 5h1.6l3.32-2.96A.6.6 0 0 1 8.5 2.5Z" fill="currentColor" />
      <path d="M11 5.5a3.6 3.6 0 0 1 0 5M12.8 3.6a6.1 6.1 0 0 1 0 8.8" stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" fill="none" />
    </svg>
  )
}

/**
 * 渲染一条已完成助手消息的朗读按钮；没有可朗读散文时不渲染。
 */
export function TtsAction({ messageId, useChat, useSpeaking, speak, stop, t }: TtsActionProps) {
  const text = useChat(snapshot => messageProse(snapshot, messageId)) as unknown
  const speaking = useSpeaking(ids => ids.includes(messageId)) as unknown

  // 播放中卸载（翻页/删除）时停掉共享播放。
  useEffect(() => () => { stop() }, [stop])

  if (typeof text !== 'string' || text.trim() === '') return null

  const label = speaking === true ? t('action.stop') : t('action.speak')
  const buttonStyle: React.CSSProperties = {
    display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
    width: 24, height: 24, padding: 0, border: 'none', background: 'transparent',
    cursor: 'pointer', color: 'inherit', opacity: 0.7, borderRadius: 6,
  }
  return (
    <Tooltip label={label} side="bottom">
      <button
        type="button"
        style={{ ...buttonStyle, ...(speaking === true ? { opacity: 1, color: '#e5484d' } : {}) }}
        aria-label={label}
        aria-pressed={speaking === true}
        data-speaking={speaking === true ? 'true' : undefined}
        onClick={() => {
          if (speaking === true) {
            stop()
            return
          }
          speak(messageId, text)
        }}
      >
        <SpeakerIcon stop={speaking === true} />
      </button>
    </Tooltip>
  )
}
