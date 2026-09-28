/**
 * v3 组件库 · 卡片容器
 * 模块化改造：统一面板卡片与卡片头，供各页面复用。
 */
import { el, svgIcon, IconName } from '../ui'

/** v3 面板卡片：圆角 + 描边 + 柔和阴影（样式由 .card 提供） */
export function panelCard(children: Array<Node | string>, opts?: { className?: string }): HTMLElement {
  const card = el('div', { class: `card p-4 ${opts?.className ?? ''}`.trim() })
  card.append(...children)
  return card
}

export interface CardHeaderOpts {
  icon?: IconName
  iconColor?: string      // 图标颜色（默认 --c-primary）
  iconEl?: HTMLElement    // 自定义图标元素（如带动画的风扇），优先于 icon
  title: string
  subtitle?: string       // 标题右侧说明文字
  badges?: HTMLElement[]  // 标题后徽标组
  actions?: HTMLElement[] // 右侧动作区
}

/** v3 卡片头：图标 + 标题 + 徽标 + 右侧动作 */
export function cardHeader(o: CardHeaderOpts): HTMLElement {
  const head = el('div', { class: 'flex items-center gap-2.5 mb-3 flex-wrap' })

  if (o.iconEl) {
    head.appendChild(o.iconEl)
  } else if (o.icon) {
    const iconWrap = el('div', {
      class: 'w-8 h-8 rounded-lg flex items-center justify-center shrink-0',
    })
    iconWrap.style.background = 'rgb(var(--c-element))'
    iconWrap.style.color = o.iconColor ?? 'rgb(var(--c-primary))'
    iconWrap.appendChild(svgIcon(o.icon, 16))
    head.appendChild(iconWrap)
  }

  const titleBox = el('div', { class: 'flex items-center gap-2 min-w-0 flex-1 flex-wrap' })
  const title = el('span', { class: 'font-semibold text-sm truncate' }, [o.title])
  title.style.color = 'rgb(var(--c-ink))'
  titleBox.appendChild(title)
  if (o.badges) for (const b of o.badges) titleBox.appendChild(b)

  if (o.subtitle) {
    const sub = el('span', { class: 'text-xs truncate' }, [o.subtitle])
    sub.style.color = 'rgb(var(--c-ink-subtle))'
    titleBox.appendChild(sub)
  }

  head.appendChild(titleBox)

  if (o.actions && o.actions.length > 0) {
    const act = el('div', { class: 'flex items-center gap-2' })
    act.append(...o.actions)
    head.appendChild(act)
  }

  return head
}
