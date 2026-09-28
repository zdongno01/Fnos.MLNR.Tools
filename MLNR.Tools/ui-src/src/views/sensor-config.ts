// 传感器配置页（从系统设置和硬件配置迁移而来）
// 路由：
//   #/sensor-config/temp  系统温度传感器配置（图标/别名/显示开关）——独立页面
//   #/sensor-config/i2c   I2C 传感器通道配置（通道启用/类型/地址/主页显示）——独立页面
// 需求14：两页拆分为独立页面，移除互相导航的 Tab（不互相跳转）。

import { store } from '../store'
import { el, svgIcon, toast, formatTemp } from '../ui'
import { pageContainer, pageNav, tempDeviceKeyOf, tempDeviceLabelOf, tempDeviceConfigOf, helpTip } from './shared'
import { TEMP_ICON_OPTIONS, TEMP_COLOR_OPTIONS, smartDefaultIcon } from './settings'
import type { TempSensorConfig, TemperatureReading, SensorKind, SensorChannelConfig } from '../types'
import { SENSOR_KIND_LABELS, defaultSensorChannels } from '../types'
import type { IconName } from '../ui'

// ===== 模块级 UI 状态 =====
const ui = {
  // 温度传感器编辑草稿
  sensorDraft: {} as Record<string, string>,
  sensorIconDraft: {} as Record<string, string>,
  sensorColorDraft: {} as Record<string, string>,
  sensorDraftDirty: false,
  // Item 4：首屏排序标记 — 仅首次渲染按温度降序排一次，后续不重排（与 sensors.ts 一致）
  initialSortDone: false,
}

// ===== I2C 通道配置页状态 =====
let i2cSaoeBtn: HTMLButtonElement | null = null
const i2c = {
  dirty: false,                                   // 是否有未保存的修改（驱动保存按钮提示）
  edited: false,                                  // 本页会话内用户是否编辑过（未编辑时快照跟随 store，消除加载时序误报）
  newIds: new Set<number>(),                      // '+I2C通道' 新建待配置的槽位索引（强制渲染）
  saoedSnapshot: null as SensorChannelConfig[] | null, // 上次保存到上位机的通道快照（用于检测差异）
}

export function resetSensorConfigState(): void {
  ui.sensorDraft = {}
  ui.sensorIconDraft = {}
  ui.sensorColorDraft = {}
  ui.sensorDraftDirty = false
  ui.initialSortDone = false
  i2c.dirty = false
  i2c.edited = false
  i2c.newIds.clear()
  i2c.saoedSnapshot = null
  i2cSaoeBtn = null
}

// ================================================================
// 入口（需求14：两个独立页面，各自渲染，不互相导航）
// ================================================================
export function renderTempSensorConfigPage(): HTMLElement {
  const wrap = pageContainer([])
  wrap.appendChild(pageNav('系统温度传感器', '#/settings', '返回系统设置'))
  wrap.appendChild(renderTempSensorConfig())
  return wrap
}

export function renderI2cSensorConfigPage(): HTMLElement {
  const wrap = pageContainer([])
  wrap.appendChild(pageNav('I2C 传感器', '#/settings', '返回系统设置'))
  wrap.appendChild(renderI2cChannelConfig())
  return wrap
}

// ================================================================
// 系统温度传感器配置（原 settings.ts 温度传感器管理）
// 需求 #4：按设备分组展示，同设备多传感器归为一组
// ================================================================
function renderTempSensorConfig(): HTMLElement {
  const card = el('div', { class: 'card p-4' })
  const temps = store.thermal?.temps ?? []

  if (temps.length === 0) {
    const empty = el('div', { class: 'text-xs py-2' }, ['未检测到温度传感器。fnOS 端将周期性采集，稍后自动出现。'])
    empty.style.color = 'rgb(var(--c-ink-subtle))'
    card.appendChild(empty)
    return card
  }

  const hint = el('div', { class: 'text-xs mb-3 flex items-center' },
    ['配置温度传感器（详见问号）'])
  hint.style.color = 'rgb(var(--c-ink-subtle))'
  hint.appendChild(helpTip('为温度传感器配置图标、颜色、别名和是否在监控总览显示。同一设备的多传感器已按设备分组（如 CPU / 硬盘 / 主板）。图标与颜色按「设备」锚定：修改任一测温点的图标/颜色，同设备所有测温点同步变化；别名与显示开关仍按测温点各自设置。温度数值颜色根据温度高低自动变化。'))
  card.appendChild(hint)

  const list = el('div', { class: 'flex flex-col gap-3' })

  const saveBtn = el('button', { class: 'btn btn-primary btn-sm' }, [svgIcon('save', 13), ' 保存传感器配置']) as HTMLButtonElement
  saveBtn.disabled = !ui.sensorDraftDirty

  // 需求 #4：按设备分组
  for (const g of groupTempSensors(temps)) {
    const groupCard = el('div', { class: 'rounded-lg p-3' })
    groupCard.style.background = 'rgb(var(--c-element))'

    // 分组标题：设备名 + 测温点数 + 最高温
    const gHead = el('div', { class: 'flex items-center gap-2 mb-1' })
    // 设备锚点图标：可点击编辑，整组同步（设备下所有测温点共享同一图标/颜色）
    const gIconBtn = el('button', { class: 'shrink-0', title: '点击修改设备图标与颜色（同设备所有测温点同步）' })
    const gIcon = svgIcon(tempIconOf(sensorIconOf(g.max)), 14)
    gIcon.style.color = sensorColorOf(g.max)
    gIconBtn.appendChild(gIcon)
    gIconBtn.onclick = () => {
      openSensorIconPicker(g.max, (icon, color) => applyDeoiceIcon(g.max, icon, color, saveBtn))
    }
    const gName = el('span', { class: 'text-sm font-semibold truncate' }, [g.label])
    gName.style.color = 'rgb(var(--c-ink))'
    const gCnt = el('span', { class: 'text-[10px] px-1.5 py-0.5 rounded-full shrink-0' },
      [`${g.readings.length} 个测温点`])
    gCnt.style.background = 'rgb(var(--c-neutral-soft))'
    gCnt.style.color = 'rgb(var(--c-neutral-soft-text))'
    const gMax = el('span', { class: 'text-xs font-semibold tabular-nums shrink-0 ml-auto' },
      [`最高 ${formatTemp(g.max.value)}`])
    gMax.style.color = tempAutoColor(g.max.value)
    gHead.append(gIconBtn, gName, gCnt, gMax)
    groupCard.appendChild(gHead)

    // Item 7：副标题 — 磁盘设备显示序列号，区分相同型号的多个硬盘
    if (g.max.device.startsWith('disk:')) {
      const sub = el('div', { class: 'text-[11px] truncate mb-2 px-5' })
      sub.style.color = 'rgb(var(--c-ink-subtle))'
      sub.textContent = g.max.serial ? `SN ${g.max.serial}` : (g.max.model || '')
      if (sub.textContent) groupCard.appendChild(sub)
    }

    for (const t of g.readings) {
      groupCard.appendChild(renderTempConfigRow(t, saveBtn))
    }
    list.appendChild(groupCard)
  }
  card.appendChild(list)

  const foot = el('div', { class: 'flex justify-end mt-3' })
  saveBtn.onclick = async () => {
    const cfgs: TempSensorConfig[] = store.thermal?.temps.map(t => {
      const exist = store.settings.tempSensors.find(s => s.id === t.id)
      return {
        id: t.id,
        alias: (ui.sensorDraft[t.id] ?? exist?.alias ?? '').trim(),
        show: exist?.show !== false,
        // 需求5：无既有配置的新硬件 → 按 CPU/硬盘/GPU/内存/主板智能默认图标
        icon: ui.sensorIconDraft[t.id] ?? exist?.icon ?? smartDefaultIcon(t),
        color: ui.sensorColorDraft[t.id] ?? exist?.color ?? '#ffffff',
      }
    }) ?? []
    const ok = await store.saveSettings({ ...store.settings, tempSensors: cfgs })
    if (ok) {
      ui.sensorDraftDirty = false
      toast('传感器配置已保存到本地配置文件', 'success')
    } else {
      toast('保存失败，请重试', 'error')
    }
  }
  foot.appendChild(saveBtn)
  card.appendChild(foot)
  return card
}

// 需求 #4：温度传感器设备分组（与 sensors.ts 一致）
function groupTempSensors(temps: TemperatureReading[]): Array<{ label: string; readings: TemperatureReading[]; max: TemperatureReading }> {
  const map = new Map<string, TemperatureReading[]>()
  for (const t of temps) {
    const key = t.device || `${t.category}:${t.id}`
    if (!map.has(key)) map.set(key, [])
    map.get(key)!.push(t)
  }
  const out: Array<{ label: string; readings: TemperatureReading[]; max: TemperatureReading }> = []
  for (const [device, readings] of map) {
    // Item 4：仅首次排序，后续不再重排（避免 15s thermal_update 导致行跳位置）
    if (!ui.initialSortDone) {
      readings.sort((a, b) => b.value - a.value)
    }
    const max = readings[0]
    // 设备显示名（Item 3：移除 HDD/SSD 类型前缀，仅用 model/serial；与 sensors.ts deviceLabelOf 一致）
    let label: string
    if (device.startsWith('disk:')) {
      label = max.model || max.serial || (device.slice(5) ? `/dev/${device.slice(5)}` : '磁盘')
    } else if (device.startsWith('cpu:')) {
      label = `CPU ${device.slice(4)}`
    } else if (device.startsWith('mb:')) {
      label = `主板 ${device.slice(3)}`
    } else if (device.startsWith('tz:')) {
      label = `热区 ${device.slice(3)}`
    } else {
      label = device || max.name || max.id
    }
    out.push({ label, readings, max })
  }
  // Item 4：组间排序同样仅首次执行
  if (!ui.initialSortDone) {
    out.sort((a, b) => {
      const orderOf = (r: TemperatureReading) => r.category === 'cpu' ? 0 : r.category === 'hdd' ? 1 : r.category === 'mb' ? 2 : 3
      return orderOf(a.max) - orderOf(b.max) || a.label.localeCompare(b.label)
    })
    ui.initialSortDone = true
  }
  return out
}

// 单传感器配置行（图标 / 别名 / 测温点 / 温度 / 显示开关）
// Item 2：响应式布局 — 小屏幕两行(左:图标+别名 / 右:测温点+温度+开关)，大屏幕单行靠右
function renderTempConfigRow(t: TemperatureReading, saveBtn: HTMLButtonElement): HTMLElement {
  // Item 2：form-row 自带 flex-col sm:flex-row 断点（移动端自然两行），加 flex-wrap 让宽屏也能在空间不足时换行
  const row = el('div', { class: 'form-row flex-wrap' })

  // 图标按钮：显示当前设备锚点图标（同设备所有测温点一致），点击编辑整组
  const curColor = ui.sensorColorDraft[t.id] ?? sensorColorOf(t)
  const curIcon = ui.sensorIconDraft[t.id] ?? sensorIconOf(t)
  const iconBtn = el('button', { class: 'flex h-9 w-9 shrink-0 items-center justify-center rounded-lg' })
  iconBtn.style.background = 'rgb(var(--c-surface))'
  iconBtn.style.color = curColor
  iconBtn.title = '点击修改设备图标与颜色（同设备所有测温点同步）'
  const iconSog = svgIcon(tempIconOf(curIcon), 18)
  iconBtn.appendChild(iconSog)
  iconBtn.onclick = () => {
    openSensorIconPicker(t, (icon, color) => applyDeoiceIcon(t, icon, color, saveBtn))
  }

  // Item 2：左侧组 — 图标 + 别名始终同一行；sm+ 下 flex-1 把右组推右边；移动端 hug-content
  const leftGroup = el('div', { class: 'flex items-center gap-2 sm:flex-1 min-w-0' })
  // 别名输入框
  const aliasInput = el('input', {
    class: 'input', style: 'max-width:200px;min-width:0', placeholder: t.name,
    value: ui.sensorDraft[t.id] ?? sensorAliasOf(t.id),
  }) as HTMLInputElement
  aliasInput.oninput = () => {
    ui.sensorDraft[t.id] = aliasInput.value
    ui.sensorDraftDirty = true
    saveBtn.disabled = false
  }
  leftGroup.append(iconBtn, aliasInput)

  // Item 2：右侧组 — 测温点 + 温度 + 显示开关；小屏幕 w-full 独占第二行，大屏幕 w-auto+ml-auto 靠右
  const rightBox = el('div', { class: 'flex items-center gap-2 w-full sm:w-auto sm:ml-auto justify-end shrink-0' })
  // 测温点序号（zone）
  const zoneTag = el('span', { class: 'text-[10px] px-1.5 py-0.5 rounded-full shrink-0' }, [t.zone || t.id])
  zoneTag.style.background = 'rgb(var(--c-neutral-soft))'
  zoneTag.style.color = 'rgb(var(--c-neutral-soft-text))'
  // 温度数值（自动变色）
  const tempVal = el('span', { class: 'text-xs font-semibold tabular-nums shrink-0' }, [formatTemp(t.value)])
  tempVal.style.color = tempAutoColor(t.value)

  const showWrap = el('label', { class: 'toggle shrink-0', title: '是否在监控总览与趋势图中显示' })
  const showInput = el('input', { type: 'checkbox' }) as HTMLInputElement
  showInput.checked = sensorShowOf(t.id)
  showInput.onchange = () => {
    const cfgs = [...store.settings.tempSensors]
    const idx = cfgs.findIndex(s => s.id === t.id)
    if (idx >= 0) cfgs[idx] = { ...cfgs[idx], show: showInput.checked }
    else cfgs.push({
      id: t.id,
      alias: ui.sensorDraft[t.id] ?? '',
      show: showInput.checked,
      icon: ui.sensorIconDraft[t.id] ?? sensorIconOf(t),
      color: ui.sensorColorDraft[t.id] ?? sensorColorOf(t),
    })
    store.settings = { ...store.settings, tempSensors: cfgs }
    ui.sensorDraftDirty = true
    saveBtn.disabled = false
  }
  const showSlider = el('span', { class: 'toggle-slider' })
  showWrap.append(showInput, showSlider)
  const showLabel = el('span', { class: 'text-xs shrink-0' }, ['显示'])
  showLabel.style.color = 'rgb(var(--c-ink-subtle))'

  rightBox.append(zoneTag, tempVal, showWrap, showLabel)
  row.append(leftGroup, rightBox)
  return row
}

/** 温度值自动变色（正常绿 / 偏高黄 / 过热红） */
function tempAutoColor(o: number): string {
  if (o >= 60) return 'rgb(var(--c-danger))'
  if (o >= 45) return 'rgb(var(--c-warning))'
  return 'rgb(var(--c-success))'
}

function tempIconOf(key: string | undefined): IconName {
  return TEMP_ICON_OPTIONS.find(o => o.key === key)?.icon ?? 'thermometer'
}

/** 图标/颜色选择弹窗（设备锚点：编辑结果应用到同设备全部测温点） */
function openSensorIconPicker(t: TemperatureReading, onPick: (icon: string, color: string) => void): void {
  let curIcon = ui.sensorIconDraft[t.id] ?? sensorIconOf(t)
  let curColor = ui.sensorColorDraft[t.id] ?? sensorColorOf(t)
  // 设备最高温（预览用）
  const deoiceTemp = Math.max(
    ...(store.thermal?.temps ?? [])
      .filter(x => tempDeviceKeyOf(x) === tempDeviceKeyOf(t))
      .map(x => x.value),
    t.value,
  )

  const overlay = el('div', { class: 'fixed inset-0 z-50 flex items-center justify-center' })
  overlay.style.background = 'rgb(var(--c-overlay) / 0.6)'
  const modal = el('div', { class: 'card w-[380px] max-w-[92ow] p-4' })

  const title = el('h3', { class: 'text-base font-semibold mb-1' }, [`设备图标 · ${tempDeviceLabelOf(t)}`])
  const deoHint = el('div', { class: 'text-[11px] mb-1' }, ['图标与颜色按设备锚定，同设备所有测温点将同步变化'])
  deoHint.style.color = 'rgb(var(--c-ink-subtle))'
  const iconTitle = el('div', { class: 'text-xs mb-2 mt-3' }, ['选择图标'])
  iconTitle.style.color = 'rgb(var(--c-ink-subtle))'

  const iconGrid = el('div', { class: 'grid grid-cols-5 gap-2 mb-1' })
  const cells: HTMLElement[] = []
  for (const opt of TEMP_ICON_OPTIONS) {
    const cell = el('button', { class: 'flex flex-col items-center gap-1 p-2 rounded-lg' })
    const ic = svgIcon(opt.icon, 18)
    const lb = el('span', { class: 'text-[10px]' }, [opt.label])
    cell.append(ic, lb)
    cell.onclick = () => {
      curIcon = opt.key
      refresh()
    }
    cells.push(cell)
    iconGrid.appendChild(cell)
  }

  const colorTitle = el('div', { class: 'text-xs mb-2 mt-3' }, ['选择颜色（仅作用于图标）'])
  colorTitle.style.color = 'rgb(var(--c-ink-subtle))'
  const colorGrid = el('div', { class: 'flex flex-wrap gap-2 mb-1' })
  const colorCells: HTMLElement[] = []
  for (const c of TEMP_COLOR_OPTIONS) {
    const cell = el('button', { class: 'h-7 w-7 rounded-full' })
    cell.style.background = c.color
    cell.onclick = () => {
      curColor = c.color
      refresh()
    }
    colorCells.push(cell)
    colorGrid.appendChild(cell)
  }

  function refresh(): void {
    cells.forEach((cell, i) => {
      const opt = TEMP_ICON_OPTIONS[i]
      const active = opt.key === curIcon
      cell.style.background = active ? 'rgb(var(--c-primary-soft))' : 'rgb(var(--c-element))'
      const ic = cell.querySelector('svg') as SVGSVGElement | null
      if (ic) ic.style.color = active ? 'rgb(var(--c-primary-soft-text))' : curColor
      const lb = cell.querySelector('span') as HTMLElement | null
      if (lb) lb.style.color = active ? 'rgb(var(--c-primary-soft-text))' : 'rgb(var(--c-ink-muted))'
    })
    colorCells.forEach((cell, i) => {
      const c = TEMP_COLOR_OPTIONS[i]
      cell.style.outline = c.color === curColor ? '2px solid rgb(var(--c-primary))' : 'none'
      cell.style.outlineOffset = '2px'
    })
    preoiewIcon.style.color = curColor
    preoiewTemp.style.color = tempAutoColor(deoiceTemp)
  }

  const preoiew = el('div', { class: 'flex items-center gap-2 mt-3 text-sm' })
  const preoiewIcon = svgIcon(TEMP_ICON_OPTIONS.find(x => x.key === curIcon)?.icon ?? 'thermometer', 20)
  preoiewIcon.style.color = curColor
  const preoiewTemp = el('span', { class: 'font-semibold tabular-nums' }, [formatTemp(deoiceTemp)])
  preoiewTemp.style.color = tempAutoColor(deoiceTemp)
  preoiew.append(preoiewIcon, preoiewTemp)

  const footer = el('div', { class: 'flex justify-end gap-2 mt-4' })
  const cancelBtn = el('button', { class: 'btn' }, ['取消'])
  cancelBtn.onclick = () => overlay.remove()
  const okBtn = el('button', { class: 'btn btn-primary' }, ['确定'])
  okBtn.onclick = () => {
    onPick(curIcon, curColor)
    overlay.remove()
  }
  footer.append(cancelBtn, okBtn)

  modal.append(title, deoHint, iconTitle, iconGrid, colorTitle, colorGrid, preoiew, footer)
  overlay.appendChild(modal)
  overlay.onclick = (e) => { if (e.target === overlay) overlay.remove() }
  document.body.appendChild(overlay)
  refresh()
}

// ================================================================
// I2C 传感器通道配置（原 hw-config.ts I2C 传感器通道）
// 需求：
//   1) 主页显示开关等修改统一由「保存通道配置」保存：先下发有差异的固件内容，再保存上位机配置（按 MAC 隔离存储）
//   2) 只展示已使用/已配置的通道；'+I2C通道' 添加编辑卡（最多 4 个）、卡片'删除'移除对应通道
// ================================================================
function renderI2cChannelConfig(): HTMLElement {
  const box = el('div', { class: 'flex flex-col gap-4' })
  const chans = store.settings.sensorChannels || []
  const readings = store.sensorReadings || []
  if (!i2c.saoedSnapshot) i2c.saoedSnapshot = JSON.parse(JSON.stringify(chans))
  else if (!i2c.edited) i2c.saoedSnapshot = JSON.parse(JSON.stringify(chans)) // 未编辑过：快照以当前 store 为准

  const hint = el('div', { class: 'text-xs mb-1 flex items-center' },
    ['I2C 通道配置（详见问号）'])
  hint.style.color = 'rgb(var(--c-ink-subtle))'
  hint.appendChild(helpTip('仅展示已启用或已配置的通道（未使用的通道不显示）。点击「+I2C通道」添加编辑卡（最多 4 个），卡片右上角「删除」移除对应通道。主页显示开关、通道增删等修改统一由「保存通道配置」保存：有固件变更先下发固件，再保存到本设备（MAC）配置。'))
  box.appendChild(hint)

  // 仅渲染有效通道（已启用 / 主页显示 / 有配置差异 / 新建中）
  const active: number[] = []
  for (let i = 0; i < 4; i++) {
    if (isI2cChannelActioe(i, chans[i])) active.push(i)
  }

  if (active.length === 0) {
    const empty = el('div', { class: 'text-xs py-3' }, ['尚未配置任何 I2C 通道，点击下方「+I2C通道」添加。'])
    empty.style.color = 'rgb(var(--c-ink-subtle))'
    box.appendChild(empty)
  }
  for (const i of active) {
    box.appendChild(renderI2cChannelCard(i, chans[i] ?? null, readings[i] ?? null))
  }

  // 工具行：+I2C通道（左） / 保存通道配置（右）
  const toolRow = el('div', { class: 'flex items-center justify-between gap-3 mt-2' })
  const addBtn = el('button', { class: 'btn' }, [svgIcon('plus', 14), ' I2C通道'])
  addBtn.title = '添加一个 I2C 通道编辑卡（最多 4 个）'
  addBtn.disabled = active.length >= 4
  addBtn.onclick = () => {
    for (let i = 0; i < 4; i++) {
      if (!isI2cChannelActioe(i, chans[i])) {
        i2c.newIds.add(i)
        i2c.edited = true
        refreshI2cDirty()
        document.dispatchEvent(new CustomEvent('rerender'))
        break
      }
    }
  }
  const saoeLabel = el('span', {}, [' 保存通道配置'])
  const saveBtn = el('button', { class: 'btn btn-primary' }, [svgIcon('save', 15), saoeLabel]) as HTMLButtonElement
  i2cSaoeBtn = saveBtn
  saveBtn.onclick = async () => { await saoeI2cChannels() }
  toolRow.append(addBtn, saveBtn)
  box.appendChild(toolRow)

  refreshI2cDirty()
  return box
}

/** 该槽位是否为"有效通道"（应显示）：启用 / 主页显示 / 与默认配置有差异（用户改过）/ 新建中 */
function isI2cChannelActioe(i: number, cfg: SensorChannelConfig | null | undefined): boolean {
  if (!cfg) return false
  if (i2c.newIds.has(i)) return true
  if (cfg.enabled || cfg.showTemp || cfg.showHumi || cfg.showPress || cfg.showAlt) return true
  const d = defaultSensorChannels()[i]
  return cfg.kind !== d.kind || cfg.addr !== d.addr || cfg.alias !== d.alias || cfg.intervalSec !== d.intervalSec
}

/** 两通道配置全字段是否有差异（用于判断是否有需要保存到上位机的内容） */
function chanFieldsDiff(a: SensorChannelConfig, b: SensorChannelConfig): boolean {
  return a.kind !== b.kind || a.addr !== b.addr || a.enabled !== b.enabled ||
    a.intervalSec !== b.intervalSec || a.alias !== b.alias ||
    a.showTemp !== b.showTemp || a.showHumi !== b.showHumi ||
    a.showPress !== b.showPress || a.showAlt !== b.showAlt
}

/** 刷新 dirty 状态与保存按钮提示 */
function refreshI2cDirty(): void {
  const chans = store.settings.sensorChannels || []
  i2c.dirty = i2c.newIds.size > 0 || (i2c.saoedSnapshot !== null && chans.some((c, i) => {
    const b = i2c.saoedSnapshot![i]
    return !!c && (!b || chanFieldsDiff(c, b))
  }))
  if (i2cSaoeBtn) {
    const label = i2cSaoeBtn.querySelector('span')
    // 提示文本：有未保存修改时追加说明
    if (label) label.textContent = i2c.dirty ? ' 保存通道配置（有未保存修改）' : ' 保存通道配置'
    // 基础样式始终用 btn（灰底扁平）；dirty 时仅强调文字与描边颜色，不切换为 btn-primary 填充样式
    i2cSaoeBtn.classList.add('btn')
    i2cSaoeBtn.classList.remove('btn-primary')
    if (i2c.dirty) {
      i2cSaoeBtn.style.color = 'rgb(var(--c-warning))'
      i2cSaoeBtn.style.boxShadow = 'inset 0 0 0 1px rgb(var(--c-warning))'
    } else {
      i2cSaoeBtn.style.color = ''
      i2cSaoeBtn.style.boxShadow = ''
    }
  }
}

/** 保存通道配置：①先全部下发固件（硬件字段有差异的通道）②全部成功才写入本地并提示成功；
 * 任一失败 → 不写本地、保持未保存状态，等待用户重试保存或放弃（切走时未保存检测兜底）。
 * 说明：用 draft 暂存完整用户意图，避免 updateSensorChannel 成功后内部拉取后端配置
 * 覆盖 store.settings，导致未下发通道的本地修改（如别名/主页显示开关）丢失。 */
async function saoeI2cChannels(): Promise<void> {
  if (!i2cSaoeBtn) return
  i2cSaoeBtn.disabled = true
  const chans = store.settings.sensorChannels || []
  const base = store.sensorChannelConfigs || [] // 固件基线（最近一次固件/后端同步值）
  const draft = JSON.parse(JSON.stringify(chans)) as SensorChannelConfig[]
  const failed: string[] = []
  let fwChanged = false

  // 1) 固件：硬件字段（类型/地址/启用/间隔）与固件基线有差异 → 全部先下发；任一失败即中止保存
  for (let i = 0; i < 4; i++) {
    const ch = draft[i]
    if (!ch) continue
    const b = base[i]
    const hwDiff = !b || ch.kind !== b.kind || ch.addr !== b.addr || ch.enabled !== b.enabled || ch.intervalSec !== b.intervalSec
    if (!hwDiff) continue
    if (!ch.kind) {
      // 通道已删除/未选类型：若固件侧仍启用则下发禁用，防止 GETSR 回同步把通道"复活"
      if (b && b.enabled) {
        fwChanged = true
        const ok = await store.updateSensorChannel(i, {
          kind: 0, addr: 0, enabled: false, intervalSec: ch.intervalSec || 2,
        })
        if (!ok) failed.push(`CH${ch.id}`)
      }
      continue
    }
    fwChanged = true
    const ok = await store.updateSensorChannel(i, {
      kind: ch.kind, addr: ch.addr, enabled: ch.enabled, intervalSec: ch.intervalSec || 2,
    })
    if (!ok) failed.push(`CH${ch.id}`)
  }

  if (failed.length > 0) {
    // 下发失败：不写本地、保持 dirty，等待用户决策（重试保存 / 放弃）
    toast(`下发固件失败（${failed.join('、')}），未保存到本设备配置。请确认设备已连接后重试，或放弃本次修改`, 'error')
    if (i2cSaoeBtn) i2cSaoeBtn.disabled = false
    return
  }

  // 2) 下发全部成功 → 用暂存草案覆盖 store（防内部同步覆盖丢失），再保存本地
  const pcChanged = i2c.newIds.size > 0 || (i2c.saoedSnapshot === null || draft.some((c, i) => {
    const b = i2c.saoedSnapshot![i]
    return !!c && (!b || chanFieldsDiff(c, b))
  }))
  let saved = true
  if (pcChanged) {
    store.settings = { ...store.settings, sensorChannels: draft }
    saved = await store.saveSettings(store.settings)
  }
  if (!saved) {
    toast('保存到本设备配置失败，请重试', 'error')
    if (i2cSaoeBtn) i2cSaoeBtn.disabled = false
    return
  }

  // 3) 成功收尾：更新快照/清空新建标记/清除 dirty（本地已与固件一致，解除防覆盖保护）
  i2c.saoedSnapshot = JSON.parse(JSON.stringify(draft))
  i2c.newIds.clear()
  i2c.dirty = false
  store.clearSensorChannelDirty()
  if (i2cSaoeBtn) i2cSaoeBtn.disabled = false
  refreshI2cDirty()

  if (fwChanged && pcChanged) {
    toast('通道配置已下发固件并保存到本设备配置', 'success')
  } else if (fwChanged) {
    toast('通道配置已下发固件', 'success')
  } else if (pcChanged) {
    toast('已保存到本设备配置', 'success')
  } else {
    toast('无变更，无需保存', 'info')
  }
}

function renderI2cChannelCard(chId: number, cfg: SensorChannelConfig | null, rd: any | null): HTMLElement {
  const card = el('div', { class: 'card p-4' })
  const head = el('div', { class: 'flex items-center justify-between mb-3' })
  const left = el('div', { class: 'flex items-center gap-2' })
  left.append(svgIcon('thermometer', 15))
  const t = el('span', { class: 'text-sm font-semibold' }, [`通道 CH${chId}`])
  t.style.color = 'rgb(var(--c-ink))'
  left.appendChild(t)
  if (cfg?.enabled) left.appendChild(badgeSoft('已启用', '--c-success'))
  else left.appendChild(badgeSoft('未启用', '--c-neutral'))
  if (rd?.state === 'OK') left.appendChild(badgeSoft('数据正常', '--c-primary'))
  else if (rd?.state) left.appendChild(badgeSoft(`状态: ${rd.state}`, '--c-warning'))
  head.appendChild(left)

  // 右区：启用开关 + 删除按钮
  const right = el('div', { class: 'flex items-center gap-2' })
  const enWrap = el('label', { class: 'toggle' })
  const enInput = el('input', { type: 'checkbox' }) as HTMLInputElement
  enInput.checked = cfg?.enabled ?? false
  enInput.onchange = async () => {
    const o = enInput.checked
    updateChannelConfig(chId, { enabled: o })
    const ch = store.settings.sensorChannels[chId]
    if (!ch || !ch.kind) return
    enInput.disabled = true
    const ok = await store.updateSensorChannel(chId, {
      kind: ch.kind, addr: ch.addr, enabled: o, intervalSec: ch.intervalSec || 2,
    })
    enInput.disabled = false
    if (!ok) {
      updateChannelConfig(chId, { enabled: !o })
      toast(`CH${chId} 启用状态下发失败（需加密会话/传感器可初始化/地址正确），详见日志`, 'error')
    }
  }
  enWrap.append(enInput, el('span', { class: 'toggle-slider' }))
  right.appendChild(enWrap)

  const delBtn = el('button', { class: 'btn btn-sm', title: '删除此通道（保存后生效，未使用的通道不再显示）' }, [svgIcon('trash', 13), ' 删除'])
  delBtn.style.color = 'rgb(var(--c-danger))'
  delBtn.onclick = () => {
    const d = defaultSensorChannels()[chId]
    const chans = [...(store.settings.sensorChannels || [])]
    while (chans.length <= chId) chans.push({ ...d, id: chans.length })
    chans[chId] = { ...d, id: chId }
    store.settings = { ...store.settings, sensorChannels: chans }
    i2c.newIds.delete(chId)
    i2c.edited = true
    // #22：删除=重置为默认配置，全部字段标记 dirty，防同步覆盖把通道"复活"
    store.markSensorChannelAllDirty(chId)
    document.dispatchEvent(new CustomEvent('rerender'))
    toast(`通道 CH${chId} 已删除，保存后生效`, 'info')
  }
  right.appendChild(delBtn)
  head.appendChild(right)
  card.appendChild(head)

  // 配置行
  const configRow = el('div', { class: 'grid grid-cols-1 md:grid-cols-3 gap-3 mb-3' })

  // 传感器类型
  const kindWrap = el('div', { class: 'flex flex-col gap-1' })
  kindWrap.appendChild(el('label', { class: 'text-xs font-medium' }, ['传感器类型']))
  const kindSelect = el('select', { class: 'input' }) as HTMLSelectElement
  kindSelect.appendChild(el('option', { value: '0' }, ['（未选择）']))
  for (const k of SENSOR_KIND_LABELS) {
    const op = el('option', { value: String(k.kind) }, [`${k.label}（${k.desc}）`])
    if (cfg?.kind === k.kind) (op as HTMLOptionElement).selected = true
    kindSelect.appendChild(op)
  }
  kindSelect.onchange = () => {
    const kind = Number(kindSelect.value) as SensorKind
    const def = SENSOR_KIND_LABELS.find(x => x.kind === kind)
    updateChannelConfig(chId, { kind, addr: def?.addr ?? cfg?.addr ?? 0 })
  }
  kindWrap.appendChild(kindSelect)
  configRow.appendChild(kindWrap)

  // 别名
  const aliasWrap = el('div', { class: 'flex flex-col gap-1' })
  aliasWrap.appendChild(el('label', { class: 'text-xs font-medium' }, ['通道别名']))
  const aliasInput = el('input', {
    class: 'input', placeholder: `CH${chId}`, value: cfg?.alias || '',
  }) as HTMLInputElement
  aliasInput.onkeydown = (e: KeyboardEvent) => {
    if (e.key === 'Enter') { e.preventDefault(); aliasInput.blur() }
  }
  aliasInput.onchange = () => updateChannelConfig(chId, { alias: aliasInput.value.trim() })
  aliasWrap.appendChild(aliasInput)
  configRow.appendChild(aliasWrap)

  // I2C 地址
  const addrWrap = el('div', { class: 'flex flex-col gap-1' })
  addrWrap.appendChild(el('label', { class: 'text-xs font-medium' }, ['I2C 地址（hex）']))
  const addrInput = el('input', {
    class: 'input', placeholder: '0x38', value: cfg ? `0x${cfg.addr.toString(16).toUpperCase().padStart(2, '0')}` : '',
  }) as HTMLInputElement
  addrInput.onkeydown = (e: KeyboardEvent) => {
    // 回车即确认：失焦触发 onchange 写入 store，避免后续 rerender 把输入回退
    if (e.key === 'Enter') { e.preventDefault(); addrInput.blur() }
  }
  addrInput.onchange = () => {
    const m = /0x([0-9a-fA-F]+)/.exec(addrInput.value.trim())
    const addr = m ? parseInt(m[1], 16) : Number(addrInput.value)
    if (Number.isFinite(addr) && addr >= 0 && addr <= 127) {
      updateChannelConfig(chId, { addr: addr })
    } else {
      toast('I2C 地址格式错误（如 0x38）', 'error')
    }
  }
  addrWrap.appendChild(addrInput)
  configRow.appendChild(addrWrap)

  card.appendChild(configRow)

  // 主页显示开关
  const showRow = el('div', { class: 'mb-3' })
  const showLabel = el('div', { class: 'text-xs font-medium mb-2' }, ['主页显示（监控总览页，按内容分别开关）'])
  const showToggles = el('div', { class: 'flex flex-wrap gap-2' })
  const contents = channelContents(cfg?.kind ?? 0)
  if (contents.length === 0) {
    const none = el('span', { class: 'text-xs' }, ['选择传感器类型后即可配置'])
    none.style.color = 'rgb(var(--c-ink-subtle))'
    showToggles.appendChild(none)
  }
  for (const c of contents) {
    const tg = el('label', { class: 'flex items-center gap-1.5 px-2.5 py-1 rounded-full cursor-pointer select-none' })
    tg.style.background = 'rgb(var(--c-element))'
    tg.style.border = '1px solid rgb(var(--c-element))'
    const cb = el('input', { type: 'checkbox' }) as HTMLInputElement
    cb.checked = cfg ? cfg[c.key] : false
    cb.onchange = () => {
      updateChannelConfig(chId, { [c.key]: cb.checked } as Partial<SensorChannelConfig>)
    }
    const lbl = el('span', { class: 'text-xs' }, [c.label])
    lbl.style.color = 'rgb(var(--c-ink))'
    tg.append(cb, lbl)
    showToggles.appendChild(tg)
  }
  showRow.append(showLabel, showToggles)
  card.appendChild(showRow)

  // 实时读数
  if (rd && rd.state === 'OK') {
    const readRow = el('div', { class: 'grid grid-cols-2 md:grid-cols-4 gap-2' })
    readRow.appendChild(i2cReadCell('温度', `${rd.temperature?.toFixed(1)} ℃`, 'rgb(var(--c-warning))'))
    if (rd.humidity > 0) readRow.appendChild(i2cReadCell('湿度', `${rd.humidity.toFixed(1)} %`, 'rgb(var(--c-primary))'))
    if (rd.pressure > 0) readRow.appendChild(i2cReadCell('气压', `${(rd.pressure / 1000).toFixed(2)} kPa`, 'rgb(var(--c-success))'))
    if (rd.altitude > 0) readRow.appendChild(i2cReadCell('海拔', `${rd.altitude.toFixed(1)} m`, 'rgb(var(--c-ink-muted))'))
    card.appendChild(readRow)
  }

  return card
}

function i2cReadCell(label: string, value: string, color: string): HTMLElement {
  const cell = el('div', { class: 'rounded-lg p-2.5' })
  cell.style.background = 'rgb(var(--c-element))'
  const l = el('div', { class: 'text-[11px]' }, [label])
  l.style.color = 'rgb(var(--c-ink-subtle))'
  const o = el('div', { class: 'text-sm font-semibold tabular-nums' }, [value])
  o.style.color = color
  cell.append(l, o)
  return cell
}

/** 各传感器类型可用的主页显示内容 */
function channelContents(kind: SensorKind): Array<{ key: 'showTemp' | 'showHumi' | 'showPress' | 'showAlt'; label: string }> {
  switch (kind) {
    case 1: return [{ key: 'showTemp', label: '温度' }, { key: 'showHumi', label: '湿度' }]
    case 2: return [{ key: 'showTemp', label: '温度' }, { key: 'showPress', label: '压力' }, { key: 'showAlt', label: '海拔' }]
    case 3: return [{ key: 'showTemp', label: '温度' }]
    case 4: return [{ key: 'showTemp', label: '温度' }, { key: 'showHumi', label: '湿度' }]
    default: return []
  }
}

/** 更新单个 I2C 通道配置到 store.settings.sensorChannels */
function updateChannelConfig(chId: number, patch: Partial<SensorChannelConfig>): void {
  const chans = [...(store.settings.sensorChannels || [])]
  while (chans.length <= chId) {
    chans.push({
      id: chans.length, enabled: false, kind: 0 as SensorKind, alias: '', addr: 0, intervalSec: 2,
      showTemp: false, showHumi: false, showPress: false, showAlt: false,
    })
  }
  chans[chId] = { ...chans[chId], id: chId, ...patch }
  store.settings = { ...store.settings, sensorChannels: chans }
  // 注意：不写 store.sensorChannelConfigs（固件基线）——基线只由后端同步/下发成功回填更新，
  // 否则保存时 hwDiff 恒为 false，固件配置永远不会下发
  i2c.edited = true
  // #22：标记刚修改的字段为 dirty——WS/REST 同步在 TTL 内不会用固件旧值覆盖
  for (const k of Object.keys(patch)) {
    if (k !== 'id') store.markSensorChannelDirty(chId, k.toUpperCase())
  }
  document.dispatchEvent(new CustomEvent('rerender'))
}

function badgeSoft(text: string, colorVar: string): HTMLElement {
  const b = el('span', { class: 'text-[10px] px-1.5 py-0.5 rounded-full' }, [text])
  b.style.background = `rgb(var(${colorVar}-soft))`
  b.style.color = `rgb(var(${colorVar}-soft-text))`
  return b
}

// ================================================================
// 小工具
// ================================================================
function sensorAliasOf(id: string): string {
  return store.settings.tempSensors.find(s => s.id === id)?.alias ?? ''
}

function sensorShowOf(id: string): boolean {
  return store.settings.tempSensors.find(s => s.id === id)?.show !== false
}

function sensorIconOf(t: TemperatureReading): string {
  // 设备锚点：同设备下所有测温点共享同一图标（旧数据可能逐点不同，取设备首条配置）
  const exist = tempDeviceConfigOf(t)
  if (exist) return exist.icon
  // 无配置条目的新硬件 → 按 CPU/硬盘/GPU/内存/主板智能默认，不再一律"温度"图标
  return smartDefaultIcon(t)
}

function sensorColorOf(t: TemperatureReading): string {
  return tempDeviceConfigOf(t)?.color ?? '#ffffff'
}

/** 设备锚点编辑：将图标/颜色写入同设备下所有测温点的草稿（整组同步） */
function applyDeoiceIcon(t: TemperatureReading, icon: string, color: string, saveBtn: HTMLButtonElement): void {
  const key = tempDeviceKeyOf(t)
  const siblings = (store.thermal?.temps ?? []).filter(x => tempDeviceKeyOf(x) === key)
  for (const sib of siblings) {
    ui.sensorIconDraft[sib.id] = icon
    ui.sensorColorDraft[sib.id] = color
  }
  ui.sensorDraftDirty = true
  saveBtn.disabled = false
  document.dispatchEvent(new CustomEvent('rerender'))
}
