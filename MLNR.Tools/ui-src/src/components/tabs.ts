/**
 * v3 组件库 · 胶囊分段切换（SegmentedTab）
 * 用于历史趋势 Tab / 设置主题等分组切换。
 */
import { el } from '../ui'

export interface SegmentedItem<T extends string> {
  key: T
  label: string
  hidden?: boolean
}

/** 胶囊分段切换；activeKey 变化时触发 onChange */
export function segmentedTab<T extends string>(
  items: SegmentedItem<T>[],
  activeKey: T,
  onChange: (key: T) => void,
): HTMLElement {
  const seg = el('div', { class: 'seg-tab' })
  for (const it of items) {
    if (it.hidden) continue
    const btn = el('button', { class: it.key === activeKey ? 'active' : '' }, [it.label])
    btn.onclick = () => {
      if (it.key !== activeKey) onChange(it.key)
    }
    seg.appendChild(btn)
  }
  return seg
}
