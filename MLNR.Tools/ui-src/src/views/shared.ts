import { store } from '../store'
import { el, svgIcon } from '../ui'
import type { TemperatureReading, TempSensorConfig, DeviceInfo } from '../types'

/**
 * 小号问号图标 + Tooltip（悬停/聚焦显示长文本说明）。
 * 用于把冗长的字段说明收敛为短标签 + 问号，鼠标悬停或键盘聚焦时展示完整描述。
 */
export function helpTip(longText: string): HTMLElement {
  const tip = el('span', { class: 'help-tip-wrap', tabindex: '0' })
  const icon = svgIcon('help', 12)
  icon.style.color = 'rgb(var(--c-ink-subtle))'
  icon.style.flexShrink = '0'
  const bubble = el('span', { class: 'help-tip-bubble' }, [longText])
  tip.append(icon, bubble)
  return tip
}

/**
 * 交互锁：拖拽等局部交互进行期间挂起全局重渲染，
 * 防止交互中的 DOM 元素（canvas 拖拽点）被整页替换。
 * main.ts 在每次渲染前检查 isInteracting()。
 */
/** 全局渲染请求器（由 main.ts 注册） */
let renderRequester: (() => void) | null = null
export function setRenderRequester(fn: () => void): void { renderRequester = fn }
export function requestRender(): void { if (renderRequester) renderRequester() }

let interactionCount = 0
export function beginInteraction(): void { interactionCount++ }
export function endInteraction(): void {
  interactionCount = Math.max(0, interactionCount - 1)
  if (interactionCount === 0) requestRender()
}
export function isInteracting(): boolean { return interactionCount > 0 }

/**
 * 控制门槛（需求 §2 重要约束 1）：
 * 只有进入 WORK_RUN 加密会话，才能执行风扇/开关控制指令。
 */
export function canControl(): boolean {
  const d = store.device
  return !!d
    && d.connectionState === 'connected'
    && d.sessionType === 'encrypted'
    && d.runState === 'WORK_RUN'
}

/** 只读指令门槛（明文会话允许 INIT/PING/GET）：仅要求已连接 */
export function isDeviceConnected(): boolean {
  return store.device?.connectionState === 'connected'
}

/** 子页面顶部导航条 */
export function pageNav(title: string, backHash = '#/settings', backLabel = '返回设置'): HTMLElement {
  const bar = el('div', { class: 'flex items-center justify-between mb-4' })
  const left = el('div', { class: 'flex items-center gap-3' })
  const back = el('button', { class: 'btn btn-sm', title: backLabel }, [svgIcon('back', 15)])
  back.onclick = () => { location.hash = backHash }
  const h = el('h2', { class: 'text-lg font-semibold' }, [title])
  h.style.color = 'rgb(var(--c-ink))'
  left.append(back, h)
  bar.appendChild(left)
  return bar
}

/** 页面标题栏（无返回按钮，靠左侧导航栏切换页面） */
export function pageTitle(title: string): HTMLElement {
  const bar = el('div', { class: 'flex items-center justify-between mb-4' })
  const left = el('div', { class: 'flex items-center gap-3' })
  const h = el('h2', { class: 'text-lg font-semibold' }, [title])
  h.style.color = 'rgb(var(--c-ink))'
  left.appendChild(h)
  bar.appendChild(left)
  return bar
}

/** 页面主容器（入场动画由 main.ts 在路由切换时统一添加，避免重渲染闪烁） */
export function pageContainer(children: HTMLElement[]): HTMLElement {
  const wrap = el('div', { class: 'mx-auto w-full max-w-5xl px-4 py-5' })
  wrap.append(...children)
  return wrap
}

/** 板块标题（主页分区用） */
export function sectionTitle(icon: 'fan' | 'hdd' | 'chart' | 'settings' | 'link' | 'log' | 'thermometer' | 'sliders' | 'info' | 'bolt' | 'curve', text: string): HTMLElement {
  const row = el('div', { class: 'flex items-center gap-2 mb-3 mt-6 first:mt-0' })
  const ic = svgIcon(icon, 17)
  ic.style.color = 'rgb(var(--c-primary))'
  const t = el('h2', { class: 'text-[15px] font-semibold' }, [text])
  t.style.color = 'rgb(var(--c-ink))'
  row.append(ic, t)
  return row
}

// ================================================================
// 温度传感器 · 设备级图标/颜色锚点
// 锚点是测温设备（分组键）而非测温点：同一设备下所有测温点共享
// 同一图标与颜色；修改任意测温点的图标/颜色即整组生效。
// ================================================================

/** 测温点所属设备分组键（与 sensors.ts / sensor-config.ts 分组一致） */
export function tempDeviceKeyOf(t: { device?: string; category?: string; id: string }): string {
  return t.device || `${t.category ?? 'other'}:${t.id}`
}

/** 设备显示名（磁盘用型号/SN，CPU/主板/热区带前缀） */
export function tempDeviceLabelOf(t: TemperatureReading): string {
  const d = t.device || ''
  if (d.startsWith('disk:')) {
    if (t.model) return t.model
    if (t.serial) return t.serial
    const dev = d.slice(5)
    return dev ? `/dev/${dev}` : '磁盘'
  }
  if (d.startsWith('cpu:')) return `CPU ${d.slice(4)}`
  if (d.startsWith('mb:')) return `主板 ${d.slice(3)}`
  if (d.startsWith('tz:')) return `热区 ${d.slice(3)}`
  return d || t.name || t.id
}

/**
 * 设备级配置：返回该测温点所属设备的图标/颜色配置。
 * 同设备任意测温点配置皆可代表设备（旧数据可能逐点不同，取第一条）；
 * 别名/显示开关仍按测温点各自配置。
 */
export function tempDeviceConfigOf(t: { device?: string; category?: string; id: string }): TempSensorConfig | undefined {
  const key = tempDeviceKeyOf(t)
  const temps = store.thermal?.temps ?? []
  const sameIds = new Set(temps.filter(x => tempDeviceKeyOf(x) === key).map(x => x.id))
  if (sameIds.size === 0) sameIds.add(t.id)
  return store.settings.tempSensors.find(s => sameIds.has(s.id))
}

/** 徽标 */
export function badge(text: string, colorVar: string, soft = false): HTMLElement {
  const b = el('span', { class: 'badge' }, [text])
  b.style.background = soft ? `rgb(var(${colorVar}-soft))` : `rgb(var(${colorVar}))`
  b.style.color = soft ? `rgb(var(${colorVar}-soft-text))` : 'rgb(var(--c-on-primary))'
  return b
}

/**
 * Gate 提示横幅（需求 §2 约束 2）：
 * UNINIT → 提示执行初始化握手；WORK_WAIT → 提示等待授权重连；
 * 未连接 → 引导前往连接设置。返回 null 表示可正常控制无需提示。
 */
export function renderGateBanner(): HTMLElement | null {
  if (canControl()) return null
  const d: DeviceInfo | null = store.device
  const state = d?.connectionState ?? 'disconnected'
  const runState = d?.runState ?? ''

  let title = ''
  let desc = ''
  let showHandshake = false
  let showConnLink = false

  if (state === 'disconnected') {
    title = '蓝牙未连接'
    desc = '控制功能不可用。请前往连接设置连接 NR_F2S4 蓝牙控制板。'
    showConnLink = true
  } else if (state === 'scanning' || state === 'connecting' || state === 'reconnecting') {
    title = state === 'scanning' ? '正在扫描设备…' : '正在建立蓝牙连接…'
    desc = '请稍候，连接完成后将自动同步硬件状态。'
  } else if (runState === 'UNINIT') {
    title = '设备未初始化'
    desc = '设备处于 UNINIT 状态，需执行初始化握手（RSA/AES 加密流程）后方可控制。'
    showHandshake = true
  } else if (runState === 'WORK_WAIT') {
    title = '设备等待授权重连'
    desc = '设备处于 WORK_WAIT 状态，正在等待授权重连，请稍候或重新执行初始化握手。'
    showHandshake = true
  } else if (d?.sessionType === 'plain') {
    title = '明文会话（只读）'
    desc = '当前为明文会话，仅允许 INIT / PING / GET 指令。请执行初始化握手进入加密会话后控制风扇与硬盘组。'
    showHandshake = true
  } else {
    title = '控制不可用'
    desc = '当前设备状态不允许下发控制指令。'
    showConnLink = true
  }

  const banner = el('div', { class: 'gate-banner' })
  const iconBox = el('div', { class: 'gate-icon' }, [svgIcon('warning', 18)])
  const text = el('div', { class: 'flex-1' })
  const t = el('div', { class: 'text-sm font-semibold mb-0.5' }, [title])
  t.style.color = 'rgb(var(--c-warning))'
  const dEl = el('div', { class: 'text-xs' }, [desc])
  dEl.style.color = 'rgb(var(--c-ink-muted))'
  text.append(t, dEl)

  const actions = el('div', { class: 'flex gap-2 shrink-0' })
  if (showHandshake) {
    const btn = el('button', { class: 'btn btn-primary btn-sm' }, [svgIcon('link', 14), ' 初始化握手'])
    btn.onclick = async () => {
      btn.disabled = true
      const ok = await store.handshake()
      btn.disabled = false
      if (ok) {
        await store.loadStatus()
      }
    }
    actions.appendChild(btn)
  }
  if (showConnLink) {
    const btn = el('button', { class: 'btn btn-sm' }, [svgIcon('link', 14), ' 前往连接设置'])
    btn.onclick = () => { location.hash = '#/connection' }
    actions.appendChild(btn)
  }
  banner.append(iconBox, text)
  if (actions.childElementCount > 0) banner.appendChild(actions)
  return banner
}

/** CSV 导出下载 */
export function downloadCSV(filename: string, rows: Array<Array<string | number>>): void {
  const esc = (v: string | number) => {
    const s = String(v)
    return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
  }
  const csv = rows.map(r => r.map(esc).join(',')).join('\n')
  const blob = new Blob(['\uFEFF' + csv], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}
