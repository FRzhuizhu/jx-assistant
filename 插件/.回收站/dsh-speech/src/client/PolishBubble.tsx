/**
 * 润色浮窗按钮：语音输入开启的瞬间在输入框上方靠中位置出现（漫画对话气泡
 * 样式，兼作"语音已开启"的告知）；点击后把语音段落交给宿主 LLM 做保守
 * 校对（只修同音错字/标点，不改写）并替换输入框中对应的语音文本。
 * 润色完成/发送消息/超时后自动隐藏。
 */
import { type CSSProperties, type ReactNode } from 'react'

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

/** 收尾块中的纯文本。 */
function closingText(closing: NonNullable<NonNullable<TailNode['data']>['closing']>): string {
  let prose = ''
  for (const block of closing.blocks ?? []) {
    if (block.kind === 'text' && typeof block.text === 'string') prose += block.text
  }
  return prose
}

/** 从 chat 快照取最后一条助手消息的纯文本（无则空串）。 */
export function lastAssistantText(snapshot: unknown): string {
  const nodes = (snapshot as { nodes?: { values?(): Iterable<TailNode> } } | null)?.nodes
  if (nodes === undefined || typeof nodes.values !== 'function') return ''
  let last = ''
  for (const node of nodes.values()) {
    if (node.kind !== 'turn-tail') continue
    const closing = node.data?.closing
    if (closing === undefined || closing === null) continue
    const text = closingText(closing)
    if (text.trim() !== '') last = text
  }
  return last
}

/** 注入面：语音段落状态与润色动作。 */
export interface PolishBubbleInjected {
  hooks: {
    /** 气泡可见性与状态（框架绑定为 usePolish）。 */
    polish: {
      getSnapshot(): PolishBubbleState
      subscribe(fn: () => void): () => void
    }
  }
  /** 对语音段落文本做保守校对（context = 最近 AI 回复纯文本，可为空）。 */
  polish: (context: string) => Promise<void>
  /** 关闭气泡（本次不再提示）。 */
  dismiss: () => void
  /** 是否把最近 AI 回复附给模型（读取最新设置）。 */
  useContext: () => boolean
  t: (key: string) => string
}

export interface PolishBubbleState {
  visible: boolean
  busy: boolean
  error: string
}

export type PolishBubbleProps = PolishBubbleInjected & {
  useChat: (selector: (snapshot: unknown) => unknown) => unknown
}

const WRAP: CSSProperties = {
  position: 'absolute', right: '22%', top: -40, zIndex: 30,
  display: 'flex', alignItems: 'flex-end', flexDirection: 'column',
}
const BUBBLE: CSSProperties = {
  position: 'relative',
  display: 'inline-flex', alignItems: 'center', gap: 6,
  padding: '5px 12px',
  borderRadius: '12px 12px 12px 4px',
  border: '1px solid var(--dsw-alias-border-l2)',
  background: 'var(--dsw-alias-bg-module-platform)',
  color: 'var(--dsw-alias-label-primary)',
  boxShadow: '0 4px 14px rgba(0,0,0,0.12)',
  font: 'inherit', fontSize: 13, lineHeight: '20px',
  cursor: 'pointer', whiteSpace: 'nowrap',
}
const TAIL: CSSProperties = {
  position: 'absolute', left: -5, bottom: 8,
  width: 10, height: 10,
  background: 'var(--dsw-alias-bg-module-platform)',
  borderLeft: '1px solid var(--dsw-alias-border-l2)',
  borderBottom: '1px solid var(--dsw-alias-border-l2)',
  transform: 'rotate(45deg)',
  borderRadius: '0 0 0 3px',
}
const DOT: CSSProperties = {
  width: 6, height: 6, borderRadius: 999, flex: 'none',
  background: 'var(--dsw-alias-label-primary)',
  animation: 'jxsp-polish-blink 1.2s ease-in-out infinite',
}

/** 渲染漫画气泡润色按钮。 */
export function PolishBubble({ usePolish, polish, dismiss, useContext, useChat, t }: PolishBubbleProps): ReactNode {
  const state = usePolish(value => value) as PolishBubbleState
  const snapshot = useChat(s => s)
  if (state.visible !== true) return null
  const runPolish = (): void => {
    const context = useContext() ? lastAssistantText(snapshot) : ''
    void polish(context).catch(() => undefined)
  }
  const label = state.error !== ''
    ? state.error
    : state.busy === true ? t('polish.busy') : t('polish.button')
  return (
    <div style={WRAP}>
      <button
        type="button"
        style={{ ...BUBBLE, ...(state.error !== '' ? { borderColor: '#e5484d', color: '#e5484d' } : {}) }}
        disabled={state.busy}
        title={state.error !== '' ? state.error : undefined}
        onClick={runPolish}
      >
        {/* 气泡尾巴（指向输入框） */}
        <span style={TAIL} aria-hidden="true" />
        {state.busy === true && <span style={DOT} />}
        <span style={{ whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{label}</span>
      </button>
      <style>{'@keyframes jxsp-polish-blink{0%,100%{opacity:.25}50%{opacity:1}}'}</style>
      {/* 点空白处区域关闭（不拦截气泡本身） */}
      <button
        type="button"
        aria-label="dismiss"
        style={{
          position: 'absolute', inset: -12, zIndex: -1,
          background: 'transparent', border: 'none', cursor: 'default',
        }}
        onClick={dismiss}
        tabIndex={-1}
      />
    </div>
  )
}
