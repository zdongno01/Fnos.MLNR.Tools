// 传感器页：从主页拆分，独立路由 #/sensors
// Tab 区分「系统温度(fnOS)」与「I2C 环境传感器」，两概念不再混用。

import { store } from '../store'
import { el, svgIcon, formatTemp, tabs, emptyState, IconName } from '../ui'
import { renderGateBanner, sectionTitle, tempDeviceConfigOf } from './shared'
import { sensorKindLabel } from '../types'
import type { TemperatureReading } from '../types'
import { TEMP_ICON_OPTIONS, smartDefaultIcon } from './settings'

// ===== 模块级 UI 状态 =====
const ui = {
  activeTab: 'system' as 'system' | 'env',
  // 需求 #4：已展开显示全部传感器的设备分组键
  expanded: new Set<string>(),
  // Item 4：首屏排序标记 — 仅首次渲染时按测温点(zone)排序一次，后续不重排
  initialSortDone: false,
}

// 温度设备分组（需求 #4：同一设备多温度传感器 → 分组显示最高温 + 展开全部）
interface TempGroup {
  device: string          // 分组键（TemperatureReading.device）
  order: number           // 排序权重（cpu < hdd < mb < other）
  readings: TemperatureReading[]
  max: TemperatureReading // 最高温度读数
}

// ================================================================
// 入口
// ================================================================
export function renderSensors(): HTMLElement {
  const wrap = el('div', { class: 'mx-auto w-full max-w-5xl px-4 py-5' })

  const gate = renderGateBanner()
  if (gate) wrap.appendChild(gate)

  wrap.appendChild(sectionTitle('thermometer', '传感器'))

  // Tab 切换
  const tabBar = tabs(
    [
      { key: 'system', label: '系统温度（fnOS）' },
      { key: 'env', label: 'I2C 环境传感器' },
    ],
    ui.activeTab,
    (key) => {
      ui.activeTab = key as 'system' | 'env'
      requestRenderLocal()
    },
  )
  wrap.appendChild(tabBar)

  if (ui.activeTab === 'system') {
    wrap.appendChild(renderSensorPanel())
  } else {
    const envCard = renderEnvSensorPanel()
    if (envCard) {
      wrap.appendChild(envCard)
    } else {
      wrap.appendChild(emptyState({
        icon: 'thermometer',
        title: '暂无 I2C 环境传感器数据',
        description: '请在「系统设置 · 传感器配置 · I2C传感器」中启用通道并连接设备，数据将通过 BLE 加密推送实时显示。',
      }))
    }
  }

  return wrap
}

// 本地触发重渲染（Tab 切换时）
function requestRenderLocal(): void {
  document.dispatchEvent(new CustomEvent('rerender'))
}

// ================================================================
// #6：系统温度面板 — 按设备分组显示（需求 #4：每组显示最高温 + 展开按钮）
// #10：图标用用户配置色，温度文本用自动变色
// ================================================================
function renderSensorPanel(): HTMLElement {
  const card = el('div', { class: 'card p-4' })
  const temps = store.thermal?.temps ?? []
  const cfgMap = new Map(store.settings.tempSensors.map(s => [s.id, s]))

  if (temps.length === 0) {
    const empty = el('div', { class: 'px-4 py-6 text-center text-sm' },
      ['暂无温度传感器数据。fnOS 端将周期性采集，稍后自动出现。'])
    empty.style.color = 'rgb(var(--c-ink-subtle))'
    card.appendChild(empty)
    return card
  }

  const groups = groupTempsByDevice(temps)
  const list = el('div', { class: 'flex flex-col gap-2' })
  for (const g of groups) {
    list.appendChild(renderTempGroup(g, cfgMap))
  }
  card.appendChild(list)
  return card
}

// 需求 #4：按 TemperatureReading.device 分组（hwmon 芯片 / 磁盘 SN / 热区），
// 组内取最高温，CPU → 硬盘 → 主板 → 其他 排序。
// Item 4：组内排序仅首次渲染时按测温点(zone)排序一次，后续不再按温度重排（位置稳定）。
function groupTempsByDevice(temps: TemperatureReading[]): TempGroup[] {
  const map = new Map<string, TemperatureReading[]>()
  for (const t of temps) {
    const key = t.device || `${t.category}:${t.id}`
    if (!map.has(key)) map.set(key, [])
    map.get(key)!.push(t)
  }
  const groups: TempGroup[] = []
  for (const [device, readings] of map) {
    // Item 4：首次排序按测温点(zone)顺序（temp1 → temp2 → temp3），后续不再排序
    if (!ui.initialSortDone) {
      readings.sort((a, b) => {
        const az = a.zone || a.id
        const bz = b.zone || b.id
        return az.localeCompare(bz)
      })
    }
    const max = readings.reduce((a, b) => (a.value >= b.value ? a : b), readings[0])
    const order = max.category === 'cpu' ? 0 : max.category === 'hdd' ? 1 : max.category === 'mb' ? 2 : 3
    groups.push({ device, order, readings, max })
  }
  if (!ui.initialSortDone) {
    groups.sort((a, b) => a.order - b.order || a.device.localeCompare(b.device))
    ui.initialSortDone = true
  }
  return groups
}

// 设备分组显示名（Item 1：仅用 model，移除 disk_type 前缀逻辑）
function deviceLabelOf(g: TempGroup): string {
  const d = g.max
  if (d.device.startsWith('disk:')) {
    // Item 1：移除类型前缀拼接，仅用 model 作为默认显示名
    if (d.model) return d.model
    if (d.serial) return d.serial
    const dev = d.device.slice(5)
    return dev ? `/dev/${dev}` : '磁盘'
  }
  if (d.device.startsWith('cpu:')) return `CPU ${d.device.slice(4)}`
  if (d.device.startsWith('mb:')) return `主板 ${d.device.slice(3)}`
  if (d.device.startsWith('tz:')) return `热区 ${d.device.slice(3)}`
  return d.device || d.name || d.id
}

// 需求 #4：单设备分组行 = 头部（图标 + 设备名 + 最高温 + 展开按钮）+ 可选展开区
function renderTempGroup(g: TempGroup, cfgMap: Map<string, any>): HTMLElement {
  const row = el('div', { class: 'rounded-lg' })
  row.style.background = 'rgb(var(--c-element))'
  const expanded = ui.expanded.has(g.device)

  // ---- 头部 ----
  const head = el('button', { class: 'flex items-center gap-2.5 w-full px-3 py-2.5 text-left' })
  // 图标：设备锚点（同设备所有测温点共享配置色；无配置的新硬件按类型智能默认）
  const devCfg = tempDeviceConfigOf(g.max)
  const iconColor = devCfg?.color || '#ffffff'
  const ic = svgIcon(tempIconOf(devCfg?.icon ?? smartDefaultIcon(g.max)), 15)
  ic.style.color = iconColor
  ic.classList.add('shrink-0')

  const nameBox = el('div', { class: 'flex-1 min-w-0' })
  const nameRow = el('div', { class: 'flex items-center gap-1.5 min-w-0' })
  const name = el('span', { class: 'text-sm font-medium truncate' }, [deviceLabelOf(g)])
  name.style.color = 'rgb(var(--c-ink))'
  nameRow.appendChild(name)
  if (g.readings.length > 1) {
    const cnt = el('span', { class: 'text-[10px] px-1.5 py-0.5 rounded-full shrink-0' },
      [`${g.readings.length} 个测温点`])
    cnt.style.background = 'rgb(var(--c-neutral-soft))'
    cnt.style.color = 'rgb(var(--c-neutral-soft-text))'
    nameRow.appendChild(cnt)
  }
  nameBox.appendChild(nameRow)
  // Item 6：副标题显示硬盘序列号（区分相同型号的多个硬盘）
  if (g.max.model || g.max.serial) {
    const sub = el('div', { class: 'text-[11px] truncate mt-0.5' })
    sub.style.color = 'rgb(var(--c-ink-subtle))'
    // 磁盘设备：副标题显示序列号；其他设备：显示型号/序列号
    if (g.max.device.startsWith('disk:')) {
      sub.textContent = g.max.serial ? `SN ${g.max.serial}` : (g.max.model || '')
    } else {
      sub.textContent = g.max.model || (g.max.serial ? `SN ${g.max.serial}` : '')
    }
    if (sub.textContent) nameBox.appendChild(sub)
  }

  // 最高温度（需求 #4：设备组只显示最高温）
  const maxVal = el('span', { class: 'text-sm font-semibold tabular-nums shrink-0' }, [formatTemp(g.max.value)])
  maxVal.style.color = tempAutoColor(g.max.value)

  // 展开按钮（需求9：仅多传感器设备显示，单测温点设备不渲染展开按钮）
  let expandBtn: HTMLElement | null = null
  if (g.readings.length > 1) {
    expandBtn = el('span', {
      class: 'w-7 h-7 rounded-md flex items-center justify-center shrink-0 transition-transform',
      title: expanded ? '收起该设备全部传感器' : '展开该设备全部传感器温度',
    })
    expandBtn.style.background = 'rgb(var(--c-surface))'
    expandBtn.style.color = 'rgb(var(--c-ink-muted))'
    expandBtn.appendChild(svgIcon(expanded ? 'minus' : 'plus', 14))
  }

  head.append(ic, nameBox, maxVal)
  if (expandBtn) {
    head.appendChild(expandBtn)
  } else {
    // 需求：单测温点设备隐藏展开按钮后补等宽空白占位符，防止温度数值错位
    const ph = el('span', { class: 'w-7 h-7 shrink-0' })
    head.appendChild(ph)
  }
  head.onclick = () => {
    if (g.readings.length <= 1) return
    if (ui.expanded.has(g.device)) ui.expanded.delete(g.device)
    else ui.expanded.add(g.device)
    document.dispatchEvent(new CustomEvent('rerender'))
  }
  row.appendChild(head)

  // ---- 展开区：该设备全部传感器温度 ----
  if (expanded) {
    const sub = el('div', { class: 'px-3 pb-2.5 flex flex-col gap-1' })
    for (const t of g.readings) {
      const cfg = cfgMap.get(t.id)
      // Item 3：响应式布局 — 小屏幕两行(左:图标+别名 / 右:测温点+温度+开关)，大屏幕单行
      const r = el('div', { class: 'flex flex-wrap items-center gap-2.5 px-2.5 py-1.5 rounded-md' })
      r.style.background = 'rgb(var(--c-surface))'

      // 左侧组：图标 + 别名输入框（始终同一行）；图标为设备锚点（同设备一致）
      const leftGroup = el('div', { class: 'flex items-center gap-2 min-w-0 flex-1' })
      const rowDevCfg = tempDeviceConfigOf(t)
      const sic = svgIcon(tempIconOf(rowDevCfg?.icon ?? smartDefaultIcon(t)), 13)
      sic.style.color = rowDevCfg?.color || 'rgb(var(--c-ink-muted))'
      sic.classList.add('shrink-0')
      const sName = el('span', { class: 'min-w-0 truncate text-xs' }, [cfg?.alias || t.name || t.id])
      sName.style.color = 'rgb(var(--c-ink))'
      leftGroup.append(sic, sName)

      // 右侧组：测温点 + 温度 + (可选)显示状态
      // 小屏幕 w-full → 独占第二行；大屏幕 sm:w-auto sm:ml-auto → 靠右对齐
      const rightBox = el('div', { class: 'flex items-center gap-2 w-full sm:w-auto sm:ml-auto justify-end shrink-0' })
      // 显示状态标记（若当前被隐藏）
      const shown = !cfg || cfg.show !== false
      if (!shown) {
        const hiddenTag = el('span', { class: 'text-[10px] px-1.5 py-0.5 rounded-full shrink-0' }, ['已隐藏'])
        hiddenTag.style.background = 'rgb(var(--c-neutral-soft))'
        hiddenTag.style.color = 'rgb(var(--c-neutral-soft-text))'
        rightBox.appendChild(hiddenTag)
      }
      const zoneTag = el('span', { class: 'text-[10px] px-1.5 py-0.5 rounded-full shrink-0' }, [t.zone || t.id])
      zoneTag.style.background = 'rgb(var(--c-neutral-soft))'
      zoneTag.style.color = 'rgb(var(--c-neutral-soft-text))'
      const sVal = el('span', { class: 'text-xs font-semibold tabular-nums shrink-0' }, [formatTemp(t.value)])
      sVal.style.color = tempAutoColor(t.value)
      rightBox.append(zoneTag, sVal)

      r.append(leftGroup, rightBox)
      sub.appendChild(r)
    }
    row.appendChild(sub)
  }

  return row
}

/** 温度值自动变色（正常绿 / 偏高黄 / 过热红） */
function tempAutoColor(v: number): string {
  if (v >= 60) return 'rgb(var(--c-danger))'
  if (v >= 45) return 'rgb(var(--c-warning))'
  return 'rgb(var(--c-success))'
}

function tempIconOf(key: string | undefined): IconName {
  return TEMP_ICON_OPTIONS.find(o => o.key === key)?.icon ?? 'thermometer'
}

// ================================================================
// I2C 环境传感器面板（多通道 AHT20/BMP280/LM75/HTU21D BLE 加密推送）
// ================================================================
function renderEnvSensorPanel(): HTMLElement | null {
  const chans = store.sensorChannelConfigs.length ? store.sensorChannelConfigs : store.settings.sensorChannels
  const readings = store.sensorReadings
  const okChannels: Array<{ cfg: any; rd: any }> = []
  for (let i = 0; i < Math.max(4, chans.length); i++) {
    const cfg = chans[i]
    if (!cfg || !cfg.enabled) continue
    const rd = readings[i]
    if (!rd || rd.state !== 'OK') continue
    okChannels.push({ cfg, rd })
  }
  if (okChannels.length === 0) return null

  const card = el('div', { class: 'card p-4' })

  // ---- 头部 ----
  const head = el('div', { class: 'flex items-center gap-2.5 mb-3' })
  const iconWrap = el('div', { class: 'w-9 h-9 rounded-lg flex items-center justify-center shrink-0' })
  iconWrap.style.background = 'rgb(var(--c-element))'
  iconWrap.style.color = 'rgb(var(--c-accent))'
  iconWrap.appendChild(svgIcon('thermometer', 18))
  const titleBox = el('div', { class: 'flex-1 min-w-0' })
  const nameRow = el('div', { class: 'flex items-center gap-2 flex-wrap' })
  const name = el('span', { class: 'font-semibold text-[15px] truncate' }, ['I2C 环境传感器'])
  name.style.color = 'rgb(var(--c-ink))'
  nameRow.append(name, el('span', { class: 'badge badge-soft' }, [`多通道 · ${okChannels.length} 路`]))
  titleBox.appendChild(nameRow)
  const sub = el('div', { class: 'text-xs mt-0.5' })
  sub.style.color = 'rgb(var(--c-ink-subtle))'
  sub.textContent = 'BLE 加密推送实时数据'
  titleBox.appendChild(sub)
  head.append(iconWrap, titleBox)
  card.appendChild(head)

  // ---- 每通道数据块 ----
  const grid = el('div', { class: 'flex flex-col gap-3' })
  for (const { cfg, rd } of okChannels) {
    const block = el('div', { class: 'rounded-lg p-3' })
    block.style.background = 'rgb(var(--c-element))'
    const bHead = el('div', { class: 'flex items-center justify-between mb-2' })
    const bName = el('span', { class: 'text-xs font-semibold' },
      [`${cfg.alias || (store.isDebugMode() ? sensorKindLabel(cfg.kind) : '环境传感器')} · CH${cfg.id}`])
    bName.style.color = 'rgb(var(--c-ink))'
    bHead.appendChild(bName)
    block.appendChild(bHead)
    const cells = el('div', { class: 'grid grid-cols-2 gap-2' })
    cells.appendChild(envCell('thermometer', '温度', `${rd.temperature.toFixed(1)} ℃`, tempAutoColor(rd.temperature)))
    if (rd.humidity > 0) cells.appendChild(envCell('droplet', '湿度', `${rd.humidity.toFixed(1)} %`, 'rgb(var(--c-primary))'))
    if (rd.pressure > 0) cells.appendChild(envCell('gauge', '压力', `${(rd.pressure / 1000).toFixed(2)} kPa`, 'rgb(var(--c-success))'))
    if (rd.altitude > 0) cells.appendChild(envCell('mountain', '海拔', `${rd.altitude.toFixed(1)} m`, 'rgb(var(--c-warning))'))
    block.appendChild(cells)
    grid.appendChild(block)
  }
  card.appendChild(grid)

  // ---- 更新时间 ----
  let last = ''
  for (const { rd } of okChannels) {
    if (!last || rd.updatedAt > last) last = rd.updatedAt
  }
  if (last) {
    const foot = el('div', { class: 'text-xs mt-3' })
    foot.style.color = 'rgb(var(--c-ink-subtle))'
    foot.textContent = `更新于 ${new Date(last).toLocaleString()}`
    card.appendChild(foot)
  }

  return card
}

/** 环境传感器数据单元 */
function envCell(icon: IconName, label: string, value: string, color: string): HTMLElement {
  const cell = el('div', { class: 'flex items-center gap-2.5 px-3 py-2.5 rounded-lg' })
  cell.style.background = 'rgb(var(--c-element))'
  const ic = svgIcon(icon, 15)
  ic.style.color = color
  ic.classList.add('shrink-0')
  const box = el('div', { class: 'min-w-0' })
  const l = el('div', { class: 'text-xs' }, [label])
  l.style.color = 'rgb(var(--c-ink-subtle))'
  const v = el('div', { class: 'text-sm font-semibold tabular-nums truncate' }, [value])
  v.style.color = 'rgb(var(--c-ink))'
  box.append(l, v)
  cell.append(ic, box)
  return cell
}

// 供 main.ts 在路由离开时清理
export function resetSensorsState(): void {
  ui.activeTab = 'system'
  ui.expanded.clear()
  ui.initialSortDone = false
}
