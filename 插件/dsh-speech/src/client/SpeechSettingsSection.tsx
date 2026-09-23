/**
 * 语音设置页：语速、音色、试听三行。
 * 行结构/字体/间距/控件复用官方 General 设置行的规范（逐值参照
 * ui-chat TranscriptViewRow 的行结构与 CSS 变量：--dsw-alias-label-primary /
 * --dsw-alias-label-tertiary / --dsw-alias-border-l2 / --dsw-alias-bg-module-platform /
 * --dsw-alias-interactive-bg-hover），下拉选择复用官方 ui-primitives 的
 * Menu 组件（与"对话显示"行同款交互）。
 * 偏好经 settingsScope 持久化到宿主用户设置文档（命名空间 dsh-speech）。
 */
import { useEffect, useState, type CSSProperties, type ReactNode } from 'react'
import { IconChevronDownOutline14, Menu, Switch } from '@deepseek-ai/dsh-client-ui-primitives'
import {
  DEFAULT_POLISH_PROMPT, SPEECH_RATE_MAX, SPEECH_RATE_MIN, SPEECH_RATE_STEP, type SpeechSettings,
} from '../settings-shared.ts'

/** 设置页所需引擎面（语音列表与变化通知）。 */
export interface SpeechSettingsEngine {
  getVoices(): SpeechSynthesisVoice[]
  addEventListener(type: 'voiceschanged', listener: () => void): void
  removeEventListener(type: 'voiceschanged', listener: () => void): void
}

/** 本地引擎状态（宿主 localStatus 的值形状）。 */
export interface LocalSttStatus {
  moduleInstalled: boolean
  modelReady: boolean
  busy: boolean
  error: string
  progress: { file: string, done: number, total: number }
  ready: boolean
}

/** 本地离线转写的注入面。 */
export interface SpeechSettingsLocalFace {
  hooks: {
    /** 宿主本地引擎状态（框架绑定为 useLocalStatus；可能 undefined=未知）。 */
    local: { getSnapshot(): LocalSttStatus | undefined, subscribe(fn: () => void): () => void }
  }
  /** 开关当前值（durable 设置）。 */
  value: () => boolean
  /** 打开时写设置并触发依赖下载；关闭只写设置。 */
  set: (on: boolean) => void
  /** 手动刷新状态。 */
  refresh: () => void
}

/** 注入面：偏好写入与试听动作。 */
export interface SpeechSettingsInjected {
  hooks: {
    /** 试听是否正在播放（框架绑定为 usePreviewing）。 */
    previewing: { getSnapshot(): boolean, subscribe(fn: () => void): () => void }
    /** 当前持久化偏好（框架绑定为 useSettings）。 */
    settings: { getSnapshot(): SpeechSettings, subscribe(fn: () => void): () => void }
  }
  setRate: (rate: number) => void
  setVoice: (voiceURI: string) => void
  preview: () => void
  stopPreview: () => void
  /** 热键与模式写入。 */
  setHotkey: (key: 'alt' | 'alt-space') => void
  setHotkeyMode: (mode: 'tap' | 'hold') => void
  /** 润色提示词与常用词写入。 */
  setPolishPrompt: (text: string) => void
  setPolishWords: (text: string) => void
  /** 润色参考最近 AI 回复的开关写入。 */
  setPolishContext: (on: boolean) => void
  /** 引擎面（语音列表）。 */
  engine: SpeechSettingsEngine
}

/** 组件完整属性。 */
export type SpeechSettingsSectionProps = {
  t: (key: string) => string
} & SpeechSettingsInjected & SpeechSettingsLocalFace

/** 行结构样式：逐值复刻官方 TranscriptViewRow.module.css 的行规范。 */
const ROW: CSSProperties = {
  display: 'flex', alignItems: 'center', gap: 8, padding: '16px 0',
  borderBottom: '0.5px solid var(--dsw-alias-border-l2)',
}
const ROW_TEXT: CSSProperties = {
  flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', gap: 4, paddingRight: 48,
}
const TITLE: CSSProperties = {
  fontSize: 14, fontWeight: 400, lineHeight: '22px', color: 'var(--dsw-alias-label-primary)',
}
const DESC: CSSProperties = {
  fontSize: 12, fontWeight: 400, lineHeight: '18px', color: 'var(--dsw-alias-label-tertiary)',
}
/** 药丸选择钮：与官方 .selector 同规格（36px 高、18px 圆角、模块底色）。 */
const PILL: CSSProperties = {
  display: 'inline-flex', alignItems: 'center', gap: 12, height: 36, padding: '0 14px',
  flex: 'none', border: 'none', borderRadius: 18,
  background: 'var(--dsw-alias-bg-module-platform)',
  font: 'inherit', fontSize: 14, lineHeight: '22px',
  color: 'var(--dsw-alias-label-primary)', cursor: 'pointer',
  maxWidth: 280, whiteSpace: 'nowrap', overflow: 'hidden',
}
const PILL_LABEL: CSSProperties = { overflow: 'hidden', textOverflow: 'ellipsis' }
const CHEVRON: CSSProperties = { flex: 'none' }
const SECTION: CSSProperties = { display: 'flex', flexDirection: 'column', padding: '0 4px' }
/** 语速滑块组：滑块 + 当前倍率值。 */
const RATE_CONTROL: CSSProperties = { display: 'flex', alignItems: 'center', gap: 12, flex: 'none' }
const SLIDER: CSSProperties = { width: 220, accentColor: '#6b7a90', cursor: 'pointer' }
const RATE_VALUE: CSSProperties = {
  width: 44, textAlign: 'right', fontVariantNumeric: 'tabular-nums',
  fontSize: 14, lineHeight: '22px', color: 'var(--dsw-alias-label-primary)',
}

/** 行组件：官方规范的标签 + 描述 + 控件。 */
function Row({ title, desc, control }: {
  title: string
  desc: string
  control: ReactNode
}): ReactNode {
  return (
    <div style={ROW}>
      <div style={ROW_TEXT}>
        <div style={TITLE}>{title}</div>
        <div style={DESC}>{desc}</div>
      </div>
      {control}
    </div>
  )
}

/** 语速行控件：保留旧版连续滑块（0.5–3.0，步进 0.1）。 */
function RateControl({ rate, setRate, }: {
  rate: number
  setRate: (rate: number) => void
}): ReactNode {
  return (
    <div style={RATE_CONTROL}>
      <input
        type="range"
        style={SLIDER}
        min={SPEECH_RATE_MIN}
        max={SPEECH_RATE_MAX}
        step={SPEECH_RATE_STEP}
        value={rate}
        aria-label="rate"
        onChange={(event) => { setRate(Number(event.target.value)) }}
      />
      <span style={RATE_VALUE}>{`${rate.toFixed(1)}x`}</span>
    </div>
  )
}

/** 本地引擎行控件：苹果样式开关 + 状态/进度文本。 */
function LocalControl({ value, status, set, refresh, t }: {
  value: boolean
  status: LocalSttStatus | undefined
  set: (on: boolean) => void
  refresh: () => void
  t: (key: string) => string
}): ReactNode {
  const [on, setOn] = useState(value)
  if (on !== value) setOn(value)
  let detail = ''
  if (status !== undefined) {
    if (status.error !== '') {
      detail = `${t('local.error')}: ${status.error.slice(0, 40)}`
    } else if (status.busy && status.progress.total > 0) {
      const pct = Math.floor((status.progress.done / status.progress.total) * 100)
      detail = `${t('local.downloading')} ${pct}%`
    } else if (status.busy) {
      detail = t('local.installing')
    } else if (status.ready && on) {
      detail = t('local.ready')
    }
  }
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 10, flex: 'none' }}>
      {detail !== '' ? <span style={{ fontSize: 12, color: 'var(--dsw-alias-label-tertiary)' }}>{detail}</span> : null}
      <Switch
        className="jx-blue-switch"
        checked={on}
        label={t('local.title')}
        onChange={(next) => {
          setOn(next)
          set(next)
          if (next) refresh()
        }}
      />
    </div>
  )
}

/** 宽行：标签 + 描述在上，控件占满整行（文本编辑类）。 */
function WideRow({ title, desc, children }: {
  title: string
  desc: string
  children: ReactNode
}): ReactNode {
  return (
    <div style={{ ...ROW, flexDirection: 'column', alignItems: 'stretch', gap: 8 }}>
      <div style={{ ...ROW_TEXT, paddingRight: 0 }}>
        <div style={TITLE}>{title}</div>
        <div style={DESC}>{desc}</div>
      </div>
      {children}
    </div>
  )
}

const TEXTAREA: CSSProperties = {
  width: '100%', boxSizing: 'border-box', minHeight: 84, padding: '8px 10px',
  borderRadius: 10, border: '1px solid var(--dsw-alias-border-l2)',
  background: 'var(--dsw-alias-bg-module-platform)',
  color: 'var(--dsw-alias-label-primary)',
  font: 'inherit', fontSize: 13, lineHeight: '20px', resize: 'vertical',
}
const TEXTAREA_TOOLBAR: CSSProperties = { display: 'flex', justifyContent: 'flex-end', marginTop: 6 }

/** 渲染语音设置行。 */
export function SpeechSettingsSection({
  setRate, setVoice, preview, stopPreview, usePreviewing, useSettings, engine,
  setHotkey, setHotkeyMode, setPolishPrompt, setPolishWords, setPolishContext,
  useLocal, value, set, refresh, t,
}: SpeechSettingsSectionProps): ReactNode {
  const settings = useSettings(v => v) as SpeechSettings
  const previewing = usePreviewing(v => v) as unknown
  const localStatus = useLocal(v => v) as LocalSttStatus | undefined
  const { rate, voice } = settings
  const [hotkeyOpen, setHotkeyOpen] = useState(false)
  const [modeOpen, setModeOpen] = useState(false)
  const [voiceOpen, setVoiceOpen] = useState(false)
  const [promptDraft, setPromptDraft] = useState<string | null>(null)
  const [wordsDraft, setWordsDraft] = useState<string | null>(null)
  const promptValue = promptDraft ?? settings.polishPrompt
  const wordsValue = wordsDraft ?? settings.polishWords
  const [voices, setVoices] = useState<readonly SpeechSynthesisVoice[]>(() => engine.getVoices())

  // 语音列表可能在首帧后才到达；跟随引擎的变化信号。
  useEffect(() => {
    const update = (): void => { setVoices(engine.getVoices()) }
    update()
    engine.addEventListener('voiceschanged', update)
    return () => { engine.removeEventListener('voiceschanged', update) }
  }, [engine])

  const sorted = [...voices].sort((a, b) => {
    const az = /^zh/i.test(a.lang) ? 0 : 1
    const bz = /^zh/i.test(b.lang) ? 0 : 1
    if (az !== bz) return az - bz
    return a.name.localeCompare(b.name)
  })
  const voiceLabel = voice === ''
    ? t('settings.voiceAuto')
    : sorted.find(candidate => candidate.voiceURI === voice)?.name ?? voice

  // 官方 Menu 用法（同 TranscriptViewRow）：药丸钮作为 anchor 交给 Menu 渲染。
  const voiceSelector = (
    <button type="button" style={PILL} aria-haspopup="menu" aria-expanded={voiceOpen}
      title={voiceLabel}
      onClick={() => { setVoiceOpen(value => !value) }}>
      <span style={PILL_LABEL}>{voiceLabel}</span>
      <IconChevronDownOutline14 style={CHEVRON} />
    </button>
  )

  return (
    <div style={SECTION}>
      {/* 与坚祥模组页同款开关的蓝色开启态覆盖（官方 Switch 默认开启色为深色） */}
      <style>{'.jx-blue-switch[aria-checked="true"]{background:#4d6bfe}'}</style>
      <Row title={t('settings.rate')} desc={t('settings.rateHint')} control={
        <RateControl rate={rate} setRate={setRate} />
      } />
      <Row title={t('settings.voice')} desc={t('settings.voiceHint')} control={
        <Menu
          open={voiceOpen}
          onClose={() => { setVoiceOpen(false) }}
          items={[
            { id: '', label: t('settings.voiceAuto') },
            ...sorted.map(candidate => ({ id: candidate.voiceURI, label: `${candidate.name} (${candidate.lang})` })),
          ]}
          selectedId={voice}
          onSelect={(id: string) => { setVoiceOpen(false); setVoice(id) }}
          align="end"
          portal
          anchor={voiceSelector}
        />
      } />
      <Row
        title={t('settings.preview')}
        desc={t('settings.previewHint')}
        control={(
          <button type="button" style={PILL} onClick={previewing === true ? stopPreview : preview}>
            {previewing === true ? t('settings.previewStop') : t('settings.preview')}
          </button>
        )}
      />
      <Row
        title={t('local.title')}
        desc={t('local.desc')}
        control={(
          <LocalControl value={value()} status={localStatus} set={set} refresh={refresh} t={t} />
        )}
      />
      <Row title={t('hotkey.title')} desc={t('hotkey.desc')} control={
        <Menu
          open={hotkeyOpen}
          onClose={() => { setHotkeyOpen(false) }}
          items={[
            { id: 'alt', label: t('hotkey.alt') },
            { id: 'alt-space', label: t('hotkey.altSpace') },
          ]}
          selectedId={settings.hotkey}
          onSelect={(id: string) => { setHotkeyOpen(false); setHotkey(id as 'alt' | 'alt-space') }}
          align="end"
          portal
          anchor={(
            <button type="button" style={PILL} aria-haspopup="menu" aria-expanded={hotkeyOpen}
              onClick={() => { setHotkeyOpen(value => !value) }}>
              {settings.hotkey === 'alt-space' ? t('hotkey.altSpace') : t('hotkey.alt')}
              <IconChevronDownOutline14 style={CHEVRON} />
            </button>
          )}
        />
      } />
      <Row title={t('hotkeyMode.title')} desc={t('hotkeyMode.desc')} control={
        <Menu
          open={modeOpen}
          onClose={() => { setModeOpen(false) }}
          items={[
            { id: 'tap', label: t('hotkeyMode.tap') },
            { id: 'hold', label: t('hotkeyMode.hold') },
          ]}
          selectedId={settings.hotkeyMode}
          onSelect={(id: string) => { setModeOpen(false); setHotkeyMode(id as 'tap' | 'hold') }}
          align="end"
          portal
          anchor={(
            <button type="button" style={PILL} aria-haspopup="menu" aria-expanded={modeOpen}
              onClick={() => { setModeOpen(value => !value) }}>
              {settings.hotkeyMode === 'hold' ? t('hotkeyMode.hold') : t('hotkeyMode.tap')}
              <IconChevronDownOutline14 style={CHEVRON} />
            </button>
          )}
        />
      } />
      <WideRow title={t('prompt.title')} desc={t('prompt.desc')}>
        <textarea
          style={TEXTAREA}
          value={promptValue}
          placeholder={t('prompt.placeholder')}
          onChange={(event) => { setPromptDraft(event.target.value) }}
          onBlur={() => { setPolishPrompt(promptValue); setPromptDraft(null) }}
        />
        <div style={TEXTAREA_TOOLBAR}>
          <button type="button" style={PILL} onClick={() => { setPromptDraft(DEFAULT_POLISH_PROMPT); setPolishPrompt(DEFAULT_POLISH_PROMPT) }}>
            {t('prompt.reset')}
          </button>
        </div>
      </WideRow>
      <WideRow title={t('words.title')} desc={t('words.desc')}>
        <textarea
          style={{ ...TEXTAREA, minHeight: 64 }}
          value={wordsValue}
          placeholder={t('words.placeholder')}
          onChange={(event) => { setWordsDraft(event.target.value) }}
          onBlur={() => { setPolishWords(wordsValue); setWordsDraft(null) }}
        />
      </WideRow>
      <Row
        title={t('context.title')}
        desc={t('context.desc')}
        control={<Switch className="jx-blue-switch" checked={settings.polishContext} label={t('context.title')} onChange={setPolishContext} />}
      />
    </div>
  )
}
