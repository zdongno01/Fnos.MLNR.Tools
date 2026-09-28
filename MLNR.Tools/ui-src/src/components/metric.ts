/**
 * v3 组件库 · 度量单元与键值网格
 * MetricCell：图标 + 标签 + 数值（传感器/环境读数）
 * kvGrid：label/value 网格（设备信息等）
 */
import { el, svgIcon, IconName } from '../ui'

export interface MetricCellOpts {
  icon: IconName
  label: string
  value: string
  valueColor?: string
  iconColor?: string
}

/** v3 度量单元（小图标 + 标签 + 数值） */
export function metricCell(o: MetricCellOpts): HTMLElement {
  const cell = el('div', { class: 'card px-3 py-2.5 flex items-center gap-2.5 min-w-0' })
  const iconBox = el('div', {
    class: 'w-7 h-7 rounded-md flex items-center justify-center shrink-0',
  })
  iconBox.style.background = 'rgb(var(--c-element))'
  iconBox.style.color = o.iconColor ?? 'rgb(var(--c-primary))'
  iconBox.appendChild(svgIcon(o.icon, 14))
  cell.appendChild(iconBox)

  const box = el('div', { class: 'min-w-0' })
  const label = el('div', { class: 'text-[11px] truncate' }, [o.label])
  label.style.color = 'rgb(var(--c-ink-subtle))'
  const value = el('div', { class: 'text-sm font-semibold tabular-nums truncate' }, [o.value])
  value.style.color = o.valueColor ?? 'rgb(var(--c-ink))'
  box.append(label, value)
  cell.appendChild(box)
  return cell
}

export interface KVItem {
  label: string
  value: string
  valueColor?: string
}

/** v3 键值网格（2 列），用于设备状态/关于信息 */
export function kvGrid(items: KVItem[]): HTMLElement {
  const grid = el('div', { class: 'grid grid-cols-2 gap-2' })
  for (const it of items) {
    const cell = el('div', { class: 'rounded-lg px-3 py-2 min-w-0' })
    cell.style.background = 'rgb(var(--c-element) / 0.5)'
    const label = el('div', { class: 'text-[11px] truncate' }, [it.label])
    label.style.color = 'rgb(var(--c-ink-subtle))'
    const value = el('div', { class: 'text-sm font-medium tabular-nums truncate' }, [it.value])
    value.style.color = it.valueColor ?? 'rgb(var(--c-ink))'
    cell.append(label, value)
    grid.appendChild(cell)
  }
  return grid
}
