/**
 * v3 组件库 · KPI 指标卡
 * 大数字 + 迷你进度条 + 副文本，用于监控总览首屏。
 */
import { el, svgIcon, IconName } from '../ui'

export type StatusTone = 'primary' | 'success' | 'warning' | 'danger' | 'accent' | 'neutral'

/** 各 tone 的图标底色 / 文字色（复用主题变量） */
const TONE_ICON_BG: Record<StatusTone, string> = {
  primary: 'rgb(var(--c-primary-soft))',
  success: 'rgb(var(--c-success-soft))',
  warning: 'rgb(var(--c-warning-soft))',
  danger: 'rgb(var(--c-danger-soft))',
  accent: 'rgb(var(--c-download-soft))',
  neutral: 'rgb(var(--c-neutral-soft))',
}
const TONE_ICON_FG: Record<StatusTone, string> = {
  primary: 'rgb(var(--c-primary-soft-text))',
  success: 'rgb(var(--c-success-soft-text))',
  warning: 'rgb(var(--c-warning-soft-text))',
  danger: 'rgb(var(--c-danger-soft-text))',
  accent: 'rgb(var(--c-download-soft-text))',
  neutral: 'rgb(var(--c-neutral-soft-text))',
}

export interface KpiBarOpts {
  pct: number        // 0~100
  color: string      // 填充色
}

export interface KpiCardOpts {
  icon: IconName
  label: string
  value: string
  valueColor?: string // 数值颜色（默认 --c-ink）
  tone?: StatusTone
  bar?: KpiBarOpts
  subtext?: string
  onClick?: () => void
}

/** v3 KPI 指标卡 */
export function kpiCard(o: KpiCardOpts): HTMLElement {
  const card = el('div', { class: 'card p-3.5 cursor-pointer' })
  card.style.transition = 'transform .15s ease, border-color .15s ease'
  card.addEventListener('mouseenter', () => {
    card.style.transform = 'translateY(-1px)'
    card.style.borderColor = 'rgb(var(--c-primary) / 0.35)'
  })
  card.addEventListener('mouseleave', () => {
    card.style.transform = 'none'
    card.style.borderColor = ''
  })
  if (o.onClick) card.addEventListener('click', o.onClick)

  // 头行：label + 图标
  const head = el('div', { class: 'flex items-center justify-between gap-2 mb-2' })
  const label = el('span', { class: 'text-xs font-medium truncate' }, [o.label])
  label.style.color = 'rgb(var(--c-ink-muted))'
  const iconBox = el('div', {
    class: 'w-7 h-7 rounded-md flex items-center justify-center shrink-0',
  })
  iconBox.style.background = TONE_ICON_BG[o.tone ?? 'primary']
  iconBox.style.color = TONE_ICON_FG[o.tone ?? 'primary']
  iconBox.appendChild(svgIcon(o.icon, 14))
  head.append(label, iconBox)
  card.appendChild(head)

  // 大数字
  const value = el('div', { class: 'text-[22px] font-bold leading-none mb-2 tabular-nums' }, [o.value])
  value.style.color = o.valueColor ?? 'rgb(var(--c-ink))'
  card.appendChild(value)

  // 迷你进度条
  if (o.bar) {
    const track = el('div', { class: 'bar-track mb-1.5' })
    const fill = el('div', { class: 'bar-fill' })
    fill.style.width = `${Math.min(100, Math.max(0, o.bar.pct))}%`
    fill.style.background = o.bar.color
    track.appendChild(fill)
    card.appendChild(track)
  }

  // 副文本
  if (o.subtext) {
    const sub = el('div', { class: 'text-[11px] truncate' }, [o.subtext])
    sub.style.color = 'rgb(var(--c-ink-subtle))'
    card.appendChild(sub)
  }

  return card
}
