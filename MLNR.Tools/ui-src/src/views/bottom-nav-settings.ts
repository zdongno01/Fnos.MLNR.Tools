// 底部导航栏设置（二级弹窗）
// 入口：系统设置 → 通用设置 → 底部导航栏设置
// 功能：导航项可见性多选 + 顺序调整（按键）+ 实时预览 + 保存/取消
// 持久化：保存到后端 Settings.bottomNavKeys（settings.json，服务器端保存，所有终端统一）
import { store } from '../store'
import { el, svgIcon, toast } from '../ui'
import type { IconName } from '../ui'
import {
  BOTTOM_NAV_ALL_ITEMS, BOTTOM_NAV_DEFAULT_KEYS, BOTTOM_NAV_MAX_ITEMS,
  toggleBottomNavKey, moveBottomNavKey, resolveBottomNavItems,
} from '../bottom-nav-config'
import type { BottomNavItemDef } from '../bottom-nav-config'

let modalOverlay: HTMLElement | null = null

// ================================================================
// 底部导航栏主体行（图标 + 标签）；真实底部导航与弹窗实时预览共用
// ================================================================
export function bottomNavRow(items: BottomNavItemDef[], activeKey: string, opts?: { interactive?: boolean }): HTMLElement {
  const interactive = opts?.interactive !== false
  const row = el('div', { class: 'flex items-center justify-around py-1 overflow-x-auto' })
  for (const item of items) {
    const isActive = item.key === activeKey
    const btn = el('button', { class: 'flex flex-col items-center gap-0.5 px-3 py-1.5 rounded-lg min-w-[56px] shrink-0' })
    if (isActive) {
      btn.style.background = 'rgb(var(--c-primary-soft))'
      btn.style.color = 'rgb(var(--c-primary-soft-text))'
    } else {
      btn.style.color = 'rgb(var(--c-ink-muted))'
    }
    const ic = svgIcon(item.icon, 18)
    const label = el('span', { class: 'text-[10px]' }, [item.label])
    btn.append(ic, label)
    if (interactive) btn.onclick = () => { location.hash = item.hash }
    row.appendChild(btn)
  }
  return row
}

/** 移动端底部导航栏（fixed 底部，<lg 断点显示）；main.ts 每次渲染时按配置构建 */
export function bottomNavBar(items: BottomNavItemDef[], activeKey: string): HTMLElement {
  const nav = el('nav', { class: 'lg:hidden fixed bottom-0 left-0 right-0 z-40 border-t' })
  nav.style.background = 'rgb(var(--c-surface))'
  nav.style.borderColor = 'rgb(var(--c-line))'
  nav.appendChild(bottomNavRow(items, activeKey))
  return nav
}

/** 当前路由对应的导航高亮 key（预览用，与 main.ts navActiveKey 同规则） */
function currentActiveKey(): string {
  const h = location.hash.replace(/^#\/?/, '')
  const p = h.split('/')[0]
  if (p === 'fan') return h.includes('/settings') ? 'settings' : 'fans'
  if (p === 'disk') return h.includes('/settings') ? 'settings' : 'disks'
  if (p === 'sensor-config') return 'settings'
  if (p === 'schedule') return 'schedules'
  if (p === 'connection') return 'connection'
  return p || 'home'
}

// ================================================================
// 二级弹窗
// ================================================================
export function openBottomNavSettingsModal(): void {
  if (modalOverlay) { toast('请先关闭当前编辑窗口', 'info'); return }
  // 初始配置：使用当前已保存配置（store 已规范化：null→默认 4 项；显式保存的 [] 保留=隐藏）
  const saved = Array.isArray(store.settings.bottomNavKeys)
    ? store.settings.bottomNavKeys.slice()
    : BOTTOM_NAV_DEFAULT_KEYS.slice()
  let draft: string[] = saved

  const overlay = el('div', { class: 'fixed inset-0 z-[70] flex items-center justify-center p-4' })
  overlay.style.background = 'rgb(var(--c-overlay) / 0.6)'
  modalOverlay = overlay
  const modal = el('div', { class: 'card w-[640px] max-w-full flex flex-col p-5' })
  modal.style.maxHeight = '90vh'

  const titleEl = el('h3', { class: 'font-semibold text-base mb-1' }, ['底部导航栏设置'])
  titleEl.style.color = 'rgb(var(--c-ink))'

  const body = el('div', { class: 'overflow-y-auto pr-1 -mr-1 flex flex-col gap-4', style: 'min-height:0' })
  body.style.flex = '1'

  function renderPreview(): HTMLElement {
    const sec = el('div', { class: 'flex flex-col gap-1.5' })
    const title = el('div', { class: 'text-xs font-medium' }, ['实时预览'])
    title.style.color = 'rgb(var(--c-ink-muted))'
    const wrap = el('div', { class: 'rounded-lg border overflow-hidden' })
    wrap.style.borderColor = 'rgb(var(--c-line))'
    const items = resolveBottomNavItems(draft)
    if (items.length === 0) {
      const empty = el('div', { class: 'text-xs text-center py-3' }, ['未选择任何导航项，底部导航栏将隐藏'])
      empty.style.color = 'rgb(var(--c-ink-subtle))'
      wrap.appendChild(empty)
    } else {
      wrap.appendChild(bottomNavRow(items, currentActiveKey(), { interactive: false }))
    }
    sec.append(title, wrap)
    return sec
  }

  function renderVisibility(): HTMLElement {
    const sec = el('div', { class: 'flex flex-col gap-2' })
    const head = el('div', { class: 'flex items-center justify-between' })
    const title = el('div', { class: 'text-xs font-medium' }, ['显示项（多选；取消全部选择可隐藏导航栏）'])
    title.style.color = 'rgb(var(--c-ink-muted))'
    const count = el('span', { class: 'text-xs tabular-nums' }, [`${draft.length}/${BOTTOM_NAV_MAX_ITEMS}`])
    count.style.color = 'rgb(var(--c-ink-subtle))'
    head.append(title, count)
    sec.appendChild(head)

    const grid = el('div', { class: 'grid grid-cols-2 gap-1' })
    for (const item of BOTTOM_NAV_ALL_ITEMS) {
      const checked = draft.includes(item.key)
      const row = el('label', { class: 'flex items-center gap-2 px-2 py-1.5 rounded-lg cursor-pointer select-none' })
      row.style.background = 'rgb(var(--c-element) / 0.5)'
      const cb = el('input', { type: 'checkbox' }) as HTMLInputElement
      cb.checked = checked
      const ic = svgIcon(item.icon, 15)
      ic.style.color = checked ? 'rgb(var(--c-primary))' : 'rgb(var(--c-ink-muted))'
      const lb = el('span', { class: 'text-xs' }, [item.label])
      lb.style.color = checked ? 'rgb(var(--c-ink))' : 'rgb(var(--c-ink-muted))'
      cb.onchange = () => {
        draft = toggleBottomNavKey(draft, item.key)
        renderBody()
      }
      row.append(cb, ic, lb)
      grid.appendChild(row)
    }
    sec.appendChild(grid)
    return sec
  }

  function renderOrder(): HTMLElement {
    const sec = el('div', { class: 'flex flex-col gap-2' })
    const title = el('div', { class: 'text-xs font-medium' }, ['显示顺序（上移 / 下移调整）'])
    title.style.color = 'rgb(var(--c-ink-muted))'
    sec.appendChild(title)
    const items = resolveBottomNavItems(draft)
    if (items.length === 0) {
      const hint = el('div', { class: 'text-xs py-2' }, ['请先在「显示项」中选择要显示的导航项'])
      hint.style.color = 'rgb(var(--c-ink-subtle))'
      sec.appendChild(hint)
      return sec
    }
    const list = el('div', { class: 'flex flex-col gap-1' })
    items.forEach((item, idx) => {
      const row = el('div', { class: 'flex items-center gap-2 px-2 py-1.5 rounded-lg' })
      row.style.background = 'rgb(var(--c-element) / 0.5)'
      const num = el('span', { class: 'text-xs tabular-nums w-5 text-center shrink-0' }, [String(idx + 1)])
      num.style.color = 'rgb(var(--c-ink-subtle))'
      const ic = svgIcon(item.icon, 15)
      ic.style.color = 'rgb(var(--c-primary))'
      const lb = el('span', { class: 'text-xs flex-1 min-w-0 truncate' }, [item.label])
      lb.style.color = 'rgb(var(--c-ink))'

      const upBtn = iconBtn('chevron-up', '上移', idx === 0)
      upBtn.onclick = () => { draft = moveBottomNavKey(draft, idx, idx - 1); renderBody() }
      const downBtn = iconBtn('chevron-down', '下移', idx === items.length - 1)
      downBtn.onclick = () => { draft = moveBottomNavKey(draft, idx, idx + 1); renderBody() }

      row.append(num, ic, lb, upBtn, downBtn)
      list.appendChild(row)
    })
    sec.appendChild(list)
    return sec
  }

  function renderBody(): void {
    body.innerHTML = ''
    body.appendChild(renderPreview())
    body.appendChild(renderVisibility())
    body.appendChild(renderOrder())
  }

  const foot = el('div', { class: 'flex justify-end gap-2 pt-3 border-t mt-2' })
  foot.style.borderColor = 'rgb(var(--c-line-subtle))'
  const cancel = el('button', { class: 'btn' }, ['取消'])
  const save = el('button', { class: 'btn btn-primary' }, ['保存'])
  cancel.onclick = () => closeModal()
  save.onclick = () => void doSave()
  foot.append(cancel, save)

  function closeModal(): void {
    modalOverlay?.remove()
    modalOverlay = null
  }

  async function doSave(): Promise<void> {
    save.disabled = true
    const ok = await store.saveSettings({ ...store.settings, bottomNavKeys: draft })
    save.disabled = false
    if (ok) {
      toast('底部导航栏设置已保存', 'success')
      closeModal()
    } else {
      toast('保存失败，请重试', 'error')
    }
  }

  modal.append(titleEl, body, foot)
  overlay.appendChild(modal)
  overlay.onclick = (e) => { if (e.target === overlay) closeModal() }
  document.body.appendChild(overlay)
  renderBody()
}

/** 路由切换时关闭弹窗（供 main.ts resetViewStates 调用） */
export function closeBottomNavModalIfOpen(): void {
  modalOverlay?.remove()
  modalOverlay = null
}

/** 小图标按钮（上移/下移） */
function iconBtn(icon: IconName, title: string, disabled: boolean): HTMLButtonElement {
  const b = el('button', {
    class: 'btn btn-sm shrink-0 flex items-center justify-center',
    style: 'width:26px;height:26px;padding:0',
    title,
    disabled,
  }) as HTMLButtonElement
  b.appendChild(svgIcon(icon, 14))
  return b
}
