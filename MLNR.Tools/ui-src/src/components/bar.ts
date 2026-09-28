/**
 * v3 组件库 · 进度条与合并速度条
 * progressBar：单色进度条（宽度/颜色可控）
 * dualBarReadonly：只读合并速度条（绿=实际转速，蓝半透明=目标转速，
 *   与风扇控制页 dual-slider 同一视觉语言，仅展示不交互）
 *
 * 动画：页面每次状态更新都会重建 DOM，CSS transition 对新建元素无效，
 * 因此通过 animateWidthFill 做「两阶段」动画——先以缓存的上次值渲染（无过渡），
 * 强制重排后再过渡到新值，实现从旧值平滑滑向新值（如 2% → 50% 不再跳变）。
 * key 由调用方提供（如 `home-fan1`），同一 key 的宽度变化才会带动画。
 */
import { el } from '../ui'

const barLastPct = new Map<string, number>()

/** 缓存当前值（供外部如拖拽直接改宽度的场景同步，避免下次动画起点错误） */
export function barCacheSet(key: string, pct: number): void {
  barLastPct.set(key, Math.min(100, Math.max(0, pct)))
}

/**
 * 两阶段宽度动画：首次渲染直接显示（无动画）；后续变化先回到上次值，
 * 强制重排后再过渡到新值（durationSec 秒），形成平滑滑动效果。
 */
export function animateWidthFill(elmt: HTMLElement, key: string, pct: number, durationSec = 0.7): void {
  const v = Math.min(100, Math.max(0, pct))
  const prev = barLastPct.get(key)
  if (prev === undefined || Math.abs(prev - v) < 0.5) {
    elmt.style.width = `${v}%`
    barLastPct.set(key, v)
    return
  }
  // 回到旧值（无过渡）→ 强制重排 → 过渡到新值
  elmt.style.transition = 'none'
  elmt.style.width = `${prev}%`
  void elmt.offsetWidth
  elmt.style.transition = `width ${durationSec}s cubic-bezier(0.4, 0, 0.2, 1)`
  elmt.style.width = `${v}%`
  barLastPct.set(key, v)
}

/** 单色进度条 */
export function progressBar(pct: number, color: string, opts?: { heightPx?: number; key?: string }): HTMLElement {
  const track = el('div', { class: 'bar-track' })
  if (opts?.heightPx) track.style.height = `${opts.heightPx}px`
  const fill = el('div', { class: 'bar-fill' })
  if (opts?.key) {
    animateWidthFill(fill, opts.key, pct)
  } else {
    fill.style.width = `${Math.min(100, Math.max(0, pct))}%`
  }
  fill.style.background = color
  track.appendChild(fill)
  return track
}

/**
 * 只读合并速度条：绿色 = 实际转速，蓝色半透明 = 目标转速
 * （复用 dual-slider 色带语义：ds-actual 绿 / ds-target 蓝）
 * key：提供时实际/目标条均带动画（同 key 前后值平滑过渡）
 */
export function dualBarReadonly(actualPct: number, targetPct: number, key?: string): HTMLElement {
  const track = el('div', { class: 'bar-track' })
  const actual = el('div', { class: 'bar-fill' })
  if (key) {
    animateWidthFill(actual, `${key}:a`, actualPct)
  } else {
    actual.style.width = `${Math.min(100, Math.max(0, actualPct))}%`
  }
  actual.style.background = 'rgb(var(--c-success) / 0.75)'
  const target = el('div', { class: 'bar-fill-overlay' })
  if (key) {
    animateWidthFill(target, `${key}:t`, targetPct, 0.45)
  } else {
    target.style.width = `${Math.min(100, Math.max(0, targetPct))}%`
  }
  target.style.background = 'rgb(var(--c-primary) / 0.45)'
  track.append(actual, target)
  return track
}
