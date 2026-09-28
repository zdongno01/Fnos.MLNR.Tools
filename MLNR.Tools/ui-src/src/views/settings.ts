// 系统设置页（重构后）：fnOS 软件侧配置
// 路由 #/settings
// 包含：传感器配置入口 / 通道配置入口（风扇·硬盘组本地配置）/ 硬件配置（保存落盘·恢复出厂）/ 主题 / 关于
// 需求 #7：原「硬件配置」页（#/hw-config）已删除，其 CFG 参数分散在对应详情页，SAVE/RESET 迁移至本页。
// 温度传感器管理和 I2C 通道配置已移至「传感器配置」页（#/sensor-config/temp 和 #/sensor-config/i2c）。
// 需求3：「蓝牙连接设置」按钮由连接页 #/connection 迁至本页硬件配置卡。
// 需求10：蓝牙未连接时，I2C 传感器 / 风扇 / 硬盘组「编辑」、恢复出厂、保存硬件配置全部禁用变灰。
// 需求11：风扇/硬盘组详情入口带来源标记（#/fan/1/settings），导航高亮保持在「系统设置」。

import { store } from '../store'
import { el, svgIcon, toast } from '../ui'
import { sectionTitle, badge, pageContainer, isDeviceConnected, helpTip, requestRender } from './shared'
import { openBottomNavSettingsModal } from './bottom-nav-settings'
import type { ThemeMode, TemperatureReading } from '../types'
import { setTheme, getCurrentPalette, setPalette, setCustomColor, getCustomColor, palettes, type PaletteId } from '../theme'

// FS.md 上位机#5：温度传感器可选的图标与颜色（供 sensor-config.ts 引用）
export const TEMP_ICON_OPTIONS: Array<{ key: string; label: string; icon: any }> = [
  { key: 'temp', label: '温度', icon: 'thermometer' },
  { key: 'cpu', label: 'CPU', icon: 'cpu' },
  { key: 'gpu', label: 'GPU', icon: 'gpu' },
  { key: 'memory', label: '内存', icon: 'memory' },
  { key: 'mb', label: '主板', icon: 'mb' },
  { key: 'expansion', label: '扩展板', icon: 'expansion' },
  { key: 'ssd', label: '固态硬盘', icon: 'ssd' },
  { key: 'hdd', label: '机械硬盘', icon: 'hdd' },
  { key: 'chassis', label: '机箱', icon: 'chassis' },
  { key: 'psu', label: '电源', icon: 'psu' },
]

export const TEMP_COLOR_OPTIONS: Array<{ key: string; color: string }> = [
  { key: 'orange', color: 'rgb(249, 115, 22)' },
  { key: 'red', color: 'rgb(239, 68, 68)' },
  { key: 'amber', color: 'rgb(245, 158, 11)' },
  { key: 'blue', color: 'rgb(59, 130, 246)' },
  { key: 'cyan', color: 'rgb(6, 182, 212)' },
  { key: 'green', color: 'rgb(16, 185, 129)' },
  { key: 'purple', color: 'rgb(168, 85, 247)' },
  { key: 'pink', color: 'rgb(236, 72, 153)' },
  { key: 'gray', color: 'rgb(148, 163, 184)' },
  { key: 'white', color: '#ffffff' },
]

// 需求5：传感器默认图标智能判定（仅"无配置条目"或"仍为旧默认 temp"的新硬件生效；
// 用户手动修改过的图标保留）。供 sensor-config.ts / sensors.ts / home.ts 引用。
export function smartDefaultIcon(t: TemperatureReading): string {
  const name = `${t.name || ''} ${t.model || ''} ${t.device || ''}`.toLowerCase()
  if (t.category === 'hdd') {
    // NVMe / SSD 型号 → 固态硬盘图标；其余机械硬盘
    if (name.includes('nvme') || name.includes('ssd')) return 'ssd'
    return 'hdd'
  }
  if (t.category === 'cpu') {
    // classifyHwmonChip 将 amdgpu/nouveau 归入 cpu 分类，此处按芯片名识别为 GPU
    if (name.includes('amdgpu') || name.includes('nouveau') || name.includes('gpu')) return 'gpu'
    return 'cpu'
  }
  if (t.category === 'mb') return 'mb'
  // other：按名称关键词推断内存/机箱/电源，无法判断时保留默认温度图标
  if (name.includes('dimm') || name.includes('memory') || name.includes('ram')) return 'memory'
  if (name.includes('chassis') || name.includes('case')) return 'chassis'
  if (name.includes('psu') || name.includes('power')) return 'psu'
  return 'temp'
}

export function resetSettingsState(): void {
  // 系统设置页无草稿状态
}

export function renderSettings(): HTMLElement {
  const wrap = pageContainer([])
  wrap.appendChild(pageTitle())

  const grid = el('div', { class: 'grid grid-cols-1 lg:grid-cols-2 gap-x-4 gap-y-6 items-start' })

  // ===== 左列：通用设置 + 传感器配置（连续叠放，平衡与右列"通道配置"的高度差） =====
  const sensorCol = el('div', { class: 'min-w-0' })
  sensorCol.appendChild(sectionTitle('settings', '通用设置'))
  sensorCol.appendChild(renderGeneralConfigCard())
  sensorCol.appendChild(sectionTitle('thermometer', '传感器配置'))
  sensorCol.appendChild(renderSensorConfigCard())
  grid.appendChild(sensorCol)

  // ===== 右列：通道配置入口 =====
  const channelCol = el('div', { class: 'min-w-0' })
  channelCol.appendChild(sectionTitle('sliders', '通道配置'))
  channelCol.appendChild(renderChannelConfigCard())
  grid.appendChild(channelCol)

  // ===== #16：关于（已删除文档参考） =====
  const aboutCol = el('div', { class: 'min-w-0 lg:col-span-2' })
  aboutCol.appendChild(sectionTitle('info', '关于'))
  aboutCol.appendChild(renderAboutCard())
  grid.appendChild(aboutCol)

  wrap.appendChild(grid)
  return wrap
}

function pageTitle(): HTMLElement {
  const bar = el('div', { class: 'flex items-center justify-between mb-4' })
  const left = el('div', { class: 'flex items-center gap-3' })
  const h = el('h2', { class: 'text-lg font-semibold' }, ['系统设置'])
  h.style.color = 'rgb(var(--c-ink))'
  left.appendChild(h)
  bar.appendChild(left)
  return bar
}

// ================================================================
// #14：传感器配置卡片（入口）
// ================================================================
function renderSensorConfigCard(): HTMLElement {
  const card = el('div', { class: 'card divide-y' })
  const connected = isDeviceConnected()

  // 系统温度传感器（需求10未点名，保持可用）
  card.appendChild(configEntryRow({
    icon: 'thermometer',
    name: '系统温度传感器',
    desc: '配置温度传感器',
    tagText: `${store.settings.tempSensors.length} 项`,
    tagVar: '--c-primary',
    settingHash: '#/sensor-config/temp',
    tip: '配置温度传感器的图标、颜色、别名和是否在监控总览显示。同一设备的多传感器已按设备分组（如 CPU / 硬盘 / 主板）。图标与颜色按「设备」锚定：修改任一测温点的图标/颜色，同设备所有测温点同步变化；别名与显示开关仍按测温点各自设置。温度数值颜色根据温度高低自动变化。',
  }))

  // I2C 传感器（需求10：蓝牙未连接时禁用）
  const i2cEnabled = store.settings.sensorChannels.filter(c => c.enabled).length
  card.appendChild(configEntryRow({
    icon: 'droplet',
    name: 'I2C 传感器',
    desc: connected ? '配置 I2C 通道' : '蓝牙未连接，连接设备后可配置',
    tagText: `${i2cEnabled}/4 已启用`,
    tagVar: i2cEnabled > 0 ? '--c-success' : '--c-neutral',
    settingHash: '#/sensor-config/i2c',
    btnsDisabled: !connected,
    tip: connected
      ? '配置 I2C 传感器通道的启用、类型、地址和主页显示；通道增删统一由「保存通道配置」保存到本设备（MAC）配置。'
      : '蓝牙未连接，连接设备后可配置。',
  }))

  return card
}

interface ConfigEntryRowOpts {
  icon: 'thermometer' | 'droplet' | 'fan' | 'hdd' | 'bolt' | 'settings'
  name: string
  desc: string
  tagText: string
  tagVar: string
  settingHash: string
  btnsDisabled?: boolean
  tip?: string
}

function configEntryRow(o: ConfigEntryRowOpts): HTMLElement {
  const row = el('div', { class: 'flex items-center gap-3 px-4 py-3' })
  const iconWrap = el('div', { class: 'w-9 h-9 rounded-lg flex items-center justify-center shrink-0' })
  iconWrap.style.background = 'rgb(var(--c-element))'
  iconWrap.style.color = 'rgb(var(--c-primary))'
  iconWrap.appendChild(svgIcon(o.icon, 17))

  const info = el('div', { class: 'flex-1 min-w-0' })
  const nameRow = el('div', { class: 'flex items-center gap-2' })
  const name = el('span', { class: 'text-sm font-medium truncate' }, [o.name])
  name.style.color = 'rgb(var(--c-ink))'
  nameRow.append(name, badge(o.tagText, o.tagVar, true))
  if (o.tip) nameRow.appendChild(helpTip(o.tip))
  const desc = el('div', { class: 'text-xs mt-0.5 truncate' }, [o.desc])
  desc.style.color = 'rgb(var(--c-ink-subtle))'
  info.append(nameRow, desc)
  const btn = el('button', { class: 'btn btn-sm btn-primary shrink-0' }, [svgIcon('edit', 13), '编辑'])
  if (o.btnsDisabled) {
    btn.disabled = true
    btn.style.opacity = '0.45'
    btn.style.cursor = 'not-allowed'
  }
  btn.onclick = () => { location.hash = o.settingHash }
  row.append(iconWrap, info, btn)
  return row
}

// ================================================================
// 通道配置卡片（风扇 + 硬盘组入口）
// ================================================================
function renderChannelConfigCard(): HTMLElement {
  const card = el('div', { class: 'card divide-y' })
  // 需求10：蓝牙未连接时禁用编辑按钮
  const connected = isDeviceConnected()

  // 风扇通道
  for (const f of store.settings.fans) {
    const hw = store.fans.find(x => x.index === f.id)
    card.appendChild(channelRow({
      icon: 'fan',
      name: f.alias || `风扇 ${f.id}`,
      tagText: `FAN${f.id}`,
      tagVar: '--c-primary',
      techTag: true,
      statusText: hw
        ? (hw.enabled ? `${f.mode === 'auto' ? '自动模式' : '手动模式'} · PWM ${(hw.pwmFreq / 1000).toFixed(0)}kHz` : '硬件已禁用')
        : '未连接',
      statusOk: hw ? hw.enabled : false,
      disabled: !f.enabled,
      btnsDisabled: !connected,
      // 需求11：详情页入口带来源标记，导航高亮保持在「系统设置」
      settingHash: `#/fan/${f.id}/settings`,
    }))
  }

  // 硬盘组通道
  for (const g of store.settings.diskGroups) {
    const view = store.diskGroups.find(x => x.id === g.id)
    // #Fix：启用状态以固件 SW ENABLED 为准（后端 GetViews 已同步），本地配置兜底
    const enabled = view?.enabled ?? g.enabled
    card.appendChild(channelRow({
      icon: 'hdd',
      name: g.alias || `硬盘组 ${g.id}`,
      tagText: `SW${g.switchN}`,
      tagVar: view?.conflict ? '--c-danger' : '--c-primary',
      techTag: true,
      statusText: view?.conflict
        ? '通道冲突，已禁用'
        : (enabled ? `${g.disks.length} 块硬盘 · ${view?.online ? '在线' : '离线'}` : '已停用'),
      statusOk: enabled && !view?.conflict,
      disabled: !enabled,
      btnsDisabled: !connected,
      settingHash: `#/disk/${g.id}/settings`,
    }))
  }

  return card
}

interface ChannelRowOpts {
  icon: 'fan' | 'hdd'
  name: string
  tagText: string
  tagVar: string
  techTag?: boolean  // 技术字段徽标（FANx/SWx），仅固件 Debug 模式显示
  statusText: string
  statusOk: boolean
  disabled: boolean
  btnsDisabled?: boolean
  settingHash: string
}

function channelRow(o: ChannelRowOpts): HTMLElement {
  const row = el('div', { class: 'flex items-center gap-3 px-4 py-3' })
  const iconWrap = el('div', { class: 'w-9 h-9 rounded-lg flex items-center justify-center shrink-0' })
  iconWrap.style.background = 'rgb(var(--c-element))'
  iconWrap.style.color = o.disabled ? 'rgb(var(--c-ink-subtle))' : 'rgb(var(--c-primary))'
  iconWrap.appendChild(svgIcon(o.icon, 17))

  const info = el('div', { class: 'flex-1 min-w-0' })
  const nameRow = el('div', { class: 'flex items-center gap-2' })
  const name = el('span', { class: 'text-sm font-medium truncate' }, [o.name])
  name.style.color = 'rgb(var(--c-ink))'
  // #Fix：FANx/SWx 为纯技术字段，非 Debug 模式隐藏
  if (!(o.techTag && !store.isDebugMode())) {
    nameRow.append(name, badge(o.tagText, o.tagVar, true))
  } else {
    nameRow.appendChild(name)
  }
  if (o.disabled) {
    const st = el('span', { class: 'text-xs' }, ['已停用'])
    st.style.color = 'rgb(var(--c-ink-subtle))'
    nameRow.appendChild(st)
  }
  const status = el('div', { class: 'text-xs mt-0.5 truncate' }, [o.statusText])
  status.style.color = o.statusOk ? 'rgb(var(--c-ink-muted))' : 'rgb(var(--c-ink-subtle))'
  info.append(nameRow, status)

  const btn = el('button', { class: 'btn btn-sm btn-primary shrink-0' }, [svgIcon('edit', 13), ' 编辑'])
  if (o.btnsDisabled) {
    btn.disabled = true
    btn.style.opacity = '0.45'
    btn.style.cursor = 'not-allowed'
  }
  btn.onclick = () => { location.hash = o.settingHash }
  row.append(iconWrap, info, btn)
  return row
}

// ================================================================
// 需求 4：通用设置卡片（主题下拉 + 控制板全局配置 + 保存硬件配置）
// 布局与传感器配置 / 通道配置一致（configEntryRow 模式）
// ================================================================
function renderGeneralConfigCard(): HTMLElement {
  const card = el('div', { class: 'card divide-y' })
  const connected = isDeviceConnected()

  // 主题（下拉菜单）
  card.appendChild(themeRow())

  // 直接写入 NVS 开关（本地配置：控制 CFG 下发后是否自动跟发 SAVE）
  card.appendChild(directSaveNvsRow())

  // 控制板全局配置（跳转到 #/connection/config）
  card.appendChild(configEntryRow({
    icon: 'bolt',
    name: '控制板全局配置',
    desc: connected ? '控制板全局设置' : '蓝牙未连接，连接设备后可配置',
    tagText: connected ? '可用' : '未连接',
    tagVar: connected ? '--c-success' : '--c-neutral',
    settingHash: '#/connection/config',
    btnsDisabled: !connected,
    tip: connected
      ? '进入控制板设置页：设备广播名 / 全局心跳超时阈值 / 恢复出厂等。'
      : '蓝牙未连接，连接设备后可配置。',
  }))

  // 底部导航栏设置（二级弹窗：可见性多选 + 顺序调整 + 实时预览）
  card.appendChild(bottomNavSettingsRow())

  // 保存硬件配置（CFG 参数落盘 NVS）：直接写入模式下隐藏（每次 CFG 已自动落盘，无需手动保存）
  if (!store.settings.directSaveNVS) {
  const saveRow = el('div', { class: 'flex items-center gap-3 px-4 py-3' })
  const saveIconWrap = el('div', { class: 'w-9 h-9 rounded-lg flex items-center justify-center shrink-0' })
  saveIconWrap.style.background = 'rgb(var(--c-element))'
  saveIconWrap.style.color = 'rgb(var(--c-primary))'
  saveIconWrap.appendChild(svgIcon('save', 17))

  const saveInfo = el('div', { class: 'flex-1 min-w-0' })
  const saveNameRow = el('div', { class: 'flex items-center gap-2' })
  const saveName = el('span', { class: 'text-sm font-medium truncate' }, ['保存硬件配置'])
  saveName.style.color = 'rgb(var(--c-ink))'
  saveNameRow.append(saveName, helpTip('将当前硬件内存中的 CFG 参数（风扇 / 开关 / I2C 通道）统一落盘到硬件 NVS，断电后保留。'))
  const saveDesc = el('div', { class: 'text-xs mt-0.5 truncate' }, ['CFG 参数落盘 NVS'])
  saveDesc.style.color = 'rgb(var(--c-ink-subtle))'
  saveInfo.append(saveNameRow, saveDesc)

  const saveBtn = el('button', { class: 'btn btn-sm btn-primary shrink-0' }, [svgIcon('save', 13), ' 保存'])
  if (!connected) {
    saveBtn.disabled = true
    saveBtn.style.opacity = '0.45'
    saveBtn.style.cursor = 'not-allowed'
  }
  saveBtn.onclick = async () => {
    if (!isDeviceConnected()) return
    saveBtn.disabled = true
    const r = await store.saveHardwareConfig()
    saveBtn.disabled = false
    toast(r ? '硬件配置已落盘 NVS' : '保存硬件配置失败，详见日志', r ? 'success' : 'error')
    document.dispatchEvent(new CustomEvent('rerender'))
  }

  saveRow.append(saveIconWrap, saveInfo, saveBtn)
  card.appendChild(saveRow)
  }

  return card
}

/** 底部导航栏设置行（二级弹窗入口：小屏设备底部导航栏个性化配置） */
function bottomNavSettingsRow(): HTMLElement {
  const row = el('div', { class: 'flex items-center gap-3 px-4 py-3' })
  const iconWrap = el('div', { class: 'w-9 h-9 rounded-lg flex items-center justify-center shrink-0' })
  iconWrap.style.background = 'rgb(var(--c-element))'
  iconWrap.style.color = 'rgb(var(--c-primary))'
  iconWrap.appendChild(svgIcon('nav', 17))

  const info = el('div', { class: 'flex-1 min-w-0' })
  const nameRow = el('div', { class: 'flex items-center gap-2' })
  const name = el('span', { class: 'text-sm font-medium truncate' }, ['底部导航栏设置'])
  name.style.color = 'rgb(var(--c-ink))'
  nameRow.append(name, helpTip('自定义小屏幕设备（小于桌面断点）底部的导航栏：多选要显示的导航项并调整顺序，实时预览后保存；左侧导航栏不受影响。取消全部选择可隐藏底部导航栏。'))
  const n = Array.isArray(store.settings.bottomNavKeys) ? store.settings.bottomNavKeys.length : 0
  const desc = el('div', { class: 'text-xs mt-0.5 truncate' }, [`小屏底部导航栏 · 当前 ${n} 项`])
  desc.style.color = 'rgb(var(--c-ink-subtle))'
  info.append(nameRow, desc)

  const btn = el('button', { class: 'btn btn-sm btn-primary shrink-0' }, [svgIcon('edit', 13), '编辑'])
  btn.onclick = () => openBottomNavSettingsModal()
  row.append(iconWrap, info, btn)
  return row
}

/** 直接写入 NVS 开关行（本地 fnOS 配置）：开启后每次 CFG 下发成功自动跟发 SAVE(0x0A) 落盘；关闭时经顶栏提示手动落盘 */function directSaveNvsRow(): HTMLElement {
  const row = el('div', { class: 'flex items-center gap-3 px-4 py-3' })
  const iconWrap = el('div', { class: 'w-9 h-9 rounded-lg flex items-center justify-center shrink-0' })
  iconWrap.style.background = 'rgb(var(--c-element))'
  iconWrap.style.color = 'rgb(var(--c-primary))'
  iconWrap.appendChild(svgIcon('save', 17))

  const info = el('div', { class: 'flex-1 min-w-0' })
  const nameRow = el('div', { class: 'flex items-center gap-2' })
  const name = el('span', { class: 'text-sm font-medium truncate' }, ['直接写入 NVS'])
  name.style.color = 'rgb(var(--c-ink))'
  nameRow.append(name, helpTip('开启后，每次硬件参数（CFG）下发成功自动跟发 SAVE 落盘 NVS；关闭时修改仅写入设备内存，需经顶栏提示或此页手动落盘，断电未保存会丢失。'))
  const desc = el('div', { class: 'text-xs mt-0.5' }, [
    store.settings.directSaveNVS
      ? '已开启：自动落盘硬件修改'
      : '已关闭：修改需手动落盘',
  ])
  desc.style.color = 'rgb(var(--c-ink-subtle))'
  info.append(nameRow, desc)

  const tgWrap = el('label', { class: 'toggle shrink-0' })
  const tgInput = el('input', { type: 'checkbox' }) as HTMLInputElement
  tgInput.checked = store.settings.directSaveNVS === true
  tgInput.onchange = async () => {
    const v = tgInput.checked
    tgInput.disabled = true
    const ok = await store.saveSettings({ ...store.settings, directSaveNVS: v })
    tgInput.disabled = false
    if (ok) {
      toast(v ? '已开启直接写入 NVS，后续修改自动落盘' : '已关闭直接写入 NVS，修改后需手动落盘', 'success')
      if (v && store.pendingNvsSave) {
        // 开启前存在未落盘修改：立即 SAVE 落盘，避免存量修改丢失
        const saved = await store.saveNvsNow()
        if (!saved) toast('存量修改落盘失败，可稍后重试或恢复出厂重配', 'error')
      }
      document.dispatchEvent(new CustomEvent('rerender'))
    } else {
      toast('保存失败，请重试', 'error')
      tgInput.checked = !v
    }
  }
  tgWrap.append(tgInput, el('span', { class: 'toggle-slider' }))

  row.append(iconWrap, info, tgWrap)
  return row
}

/** 主题设置行（下拉菜单，内联在通用设置组中） */
function themeRow(): HTMLElement {
  const row = el('div', { class: 'flex items-center gap-3 px-4 py-3', style: 'position:relative' })
  const iconWrap = el('div', { class: 'w-9 h-9 rounded-lg flex items-center justify-center shrink-0' })
  iconWrap.style.background = 'rgb(var(--c-element))'
  iconWrap.style.color = 'rgb(var(--c-primary))'
  iconWrap.appendChild(svgIcon('settings', 17))

  const info = el('div', { class: 'flex-1 min-w-0' })
  const nameRow = el('div', { class: 'flex items-center gap-2' })
  const name = el('span', { class: 'text-sm font-medium truncate' }, ['主题'])
  name.style.color = 'rgb(var(--c-ink))'
  nameRow.append(name, helpTip('选择界面主题模式：「自动」跟随 fnOS 系统主题切换（读 fnOS 门户主题模式，失败时回退浏览器系统偏好）。右侧调色盘图标可切换配色风格，仅叠加界面强调色，不影响明暗模式。'))
  const desc = el('div', { class: 'text-xs mt-0.5 truncate' }, ['主题模式与配色风格'])
  desc.style.color = 'rgb(var(--c-ink-subtle))'
  info.append(nameRow, desc)

  const currentTheme = store.settings.theme || 'auto'
  const select = el('select', { class: 'input shrink-0', style: 'max-width:96px' }) as HTMLSelectElement
  const options: Array<{ id: ThemeMode; label: string }> = [
    { id: 'auto', label: '自动' },
    { id: 'light', label: '亮色' },
    { id: 'dark', label: '暗色' },
    { id: 'sepia', label: '护眼暖' },
    { id: 'midnight', label: '午夜蓝' },
  ]
  for (const opt of options) {
    const op = el('option', { value: opt.id }, [opt.label])
    if (opt.id === currentTheme) (op as HTMLOptionElement).selected = true
    select.appendChild(op)
  }
  select.onchange = async () => {
    const v = select.value as ThemeMode
    setTheme(v)
    const ok = await store.saveSettings({ ...store.settings, theme: v })
    if (ok) {
      toast(`主题已切换为${options.find(o => o.id === v)?.label}`, 'success')
    } else {
      toast('主题保存失败', 'error')
    }
  }

  // 配色风格：调色盘图标 → 点击弹出配色盘选择（替代原六个色块，更省宽度）
  const paletteBtn = el('button', {
    class: 'shrink-0 flex items-center justify-center rounded-md',
    style: 'width:28px;height:28px;background:rgb(var(--c-element));color:rgb(var(--c-primary));cursor:pointer;border:1px solid rgb(var(--c-line) / 0.5);',
    title: '配色风格',
  }, [svgIcon('palette', 15)])
  // 配色弹层：body 级单例。设置页会随状态/定时刷新整页重建，弹层挂在行内会被刷新销毁
  //（表现为"点击后自动关闭"），故挂到 document.body 并复用同一元素。
  ensurePalettePop()
  const pop = palettePop as HTMLElement
  paletteBtn.onclick = (e) => {
    e.stopPropagation()
    if (palettePopOpen) {
      closePalettePop()
    } else {
      const r = paletteBtn.getBoundingClientRect()
      pop.style.left = Math.max(8, Math.min(r.right - PALETTE_POP_W - 6, window.innerWidth - PALETTE_POP_W - 8)) + 'px'
      pop.style.top = (r.bottom + 6) + 'px'
      rebuildPalettePop(pop)
      pop.style.display = 'block'
      palettePopOpen = true
    }
  }

  const right = el('div', { class: 'flex items-center gap-2 shrink-0' })
  right.append(paletteBtn, select)
  row.append(iconWrap, info, right)
  return row
}

/** 配色弹层宽度 */
const PALETTE_POP_W = 268

/** 配色弹层（body 级单例：设置页整页刷新时不被销毁，解决弹层"自动关闭"） */
let palettePop: HTMLElement | null = null
let palettePopOpen = false
let palettePopCloseInstalled = false
/** 关闭配色弹层 */
function closePalettePop(): void {
  palettePopOpen = false
  if (palettePop) palettePop.style.display = 'none'
}

function ensurePalettePop(): void {
  // 单例可能随上一轮整页重建被移出 DOM：重建后重新挂回 body
  if (!palettePop || !palettePop.isConnected) {
    const pop = el('div', { class: 'card p-2', style: 'position:fixed;display:none;z-index:120;' })
    pop.addEventListener('click', (e) => e.stopPropagation())
    document.body.appendChild(pop)
    palettePop = pop
  }
  if (!palettePopCloseInstalled) {
    palettePopCloseInstalled = true
    document.addEventListener('click', closePalettePop)
    window.addEventListener('hashchange', closePalettePop)
  }
}

/** 重建配色弹层内容（打开时调用，保证选中态/滑块值与当前配色一致） */
function rebuildPalettePop(pop: HTMLElement): void {
  pop.innerHTML = ''
  pop.style.width = PALETTE_POP_W + 'px'
  const title = el('div', { class: 'text-[11px] px-2 pt-1 pb-1.5' }, ['配色风格'])
  title.style.color = 'rgb(var(--c-ink-subtle))'
  pop.appendChild(title)

  // 推荐色
  const cur = getCurrentPalette()
  const swatchRow = el('div', { class: 'flex items-center gap-1.5 flex-wrap px-2 pb-2' })
  for (const p of palettes) {
    const dot = el('button', {
      class: 'rounded-full',
      style: `width:24px;height:24px;cursor:pointer;padding:0;border:2px solid ${p.id === cur ? 'rgb(var(--c-ink))' : 'transparent'};background:${paletteSwatch(p.id)};`,
    })
    dot.title = p.name
    dot.onclick = () => {
      setPalette(p.id)
      closePalettePop()
      toast(`配色已切换为${p.name}`, 'success')
      requestRender()
    }
    swatchRow.appendChild(dot)
  }
  pop.appendChild(swatchRow)

  const sep = el('div', { class: 'h-px mx-2 mb-1' })
  sep.style.background = 'rgb(var(--c-line-subtle))'
  pop.appendChild(sep)

  // 自定义：RGB 三个滑块实时选色
  const curHex = cur === 'custom' ? (getCustomColor() ?? '#3b82f6') : paletteSwatch(cur)
  const [r0, g0, b0] = hexToRgbArr(curHex)
  const rgb = { r: r0, g: g0, b: b0 }

  const preview = el('div', { class: 'rounded-md shrink-0', style: `width:26px;height:26px;background:${curHex};border:1px solid rgb(var(--c-line) / 0.6);` })
  const hexInp = el('input', {
    class: 'input font-mono',
    style: 'width:84px;padding:3px 6px;font-size:11px;',
    value: curHex,
  } as Record<string, string>)
  const applyCustom = () => {
    const hex = rgbToHex(rgb.r, rgb.g, rgb.b)
    setCustomColor(hex)
    hexInp.value = hex
    preview.style.background = hex
  }
  const slider = (label: string, key: 'r' | 'g' | 'b', color: string): HTMLElement => {
    const row = el('div', { class: 'flex items-center gap-2 px-2' })
    const lab = el('span', { class: 'text-[10px] font-mono w-3 shrink-0' }, [label])
    lab.style.color = color
    const inp = el('input', { type: 'range', min: '0', max: '255', value: String(rgb[key]) } as Record<string, string | boolean>)
    inp.style.flex = '1'
    inp.style.accentColor = color
    const val = el('span', { class: 'text-[10px] font-mono w-6 text-right tabular-nums shrink-0' }, [String(rgb[key])])
    val.style.color = 'rgb(var(--c-ink-muted))'
    inp.oninput = () => {
      rgb[key] = Number(inp.value)
      val.textContent = inp.value
      applyCustom()
    }
    row.append(lab, inp, val)
    return row
  }
  pop.appendChild(slider('R', 'r', '#ef4444'))
  pop.appendChild(slider('G', 'g', '#22c55e'))
  pop.appendChild(slider('B', 'b', '#3b82f6'))

  // Hex + 预览 + 确定
  const foot = el('div', { class: 'flex items-center gap-2 px-2 pt-1.5' })
  hexInp.onchange = () => {
    const m = /^#?([0-9a-fA-F]{6})$/.exec(hexInp.value.trim())
    if (!m) { hexInp.value = curHex; return }
    const hex = '#' + m[1].toLowerCase()
    const [hr, hg, hb] = hexToRgbArr(hex)
    rgb.r = hr; rgb.g = hg; rgb.b = hb
    applyCustom()
  }
  const okBtn = el('button', { class: 'btn btn-sm btn-primary ml-auto' }, ['确定'])
  okBtn.onclick = () => {
    closePalettePop()
    toast('配色已应用', 'success')
  }
  foot.append(preview, hexInp, okBtn)
  pop.appendChild(foot)
}

/** 配色风格代表色（与 style.css 中 data-palette 主色一致，仅用于色块显示） */
function paletteSwatch(id: PaletteId): string {
  switch (id) {
    case 'emerald': return '#10b981'
    case 'ocean': return '#0ea5e9'
    case 'sunset': return '#f97316'
    case 'grape': return '#8b5cf6'
    case 'rose': return '#f472b6'
    default: return '#3b82f6'
  }
}

function hexToRgbArr(hex: string): [number, number, number] {
  const n = parseInt(hex.replace('#', ''), 16)
  return [(n >> 16) & 255, (n >> 8) & 255, n & 255]
}
function rgbToHex(r: number, g: number, b: number): string {
  const p = (v: number) => Math.max(0, Math.min(255, Math.round(v))).toString(16).padStart(2, '0')
  return '#' + p(r) + p(g) + p(b)
}

// ================================================================
// #16：关于卡片（已删除"文档参考"项）
// ================================================================
function renderAboutCard(): HTMLElement {
  const card = el('div', { class: 'card p-4' })
  const grid = el('div', { class: 'grid grid-cols-2 md:grid-cols-3 gap-3' })

  const appInfo = store.appInfo
  const d = store.device

  grid.append(
    aboutItem('应用名称', 'MLNR 麻辣牛肉控制器'),
    aboutItem('前端版本', appInfo?.version ?? '—'),
    aboutItem('设备型号', d?.name ?? 'NR_F2S4'),
    aboutItem('固件版本', d?.version ?? '—'),
    aboutItem('MAC 地址', d?.address ?? '—'),
    aboutItem('API 地址', location.origin + '/app/mlnr/api'),
  )

  card.appendChild(grid)

  const hint = el('div', { class: 'text-xs mt-3 pt-3' })
  hint.style.borderTop = '1px solid rgb(var(--c-line-subtle))'
  hint.style.color = 'rgb(var(--c-ink-subtle))'
  hint.textContent = store.settings.directSaveNVS
    ? '硬件 CFG 参数（风扇/开关）在对应详情页修改，I2C 通道在「传感器配置」修改；「直接写入 NVS」已开启，保存后自动落盘。'
    : '硬件 CFG 参数（风扇/开关）在对应详情页修改，I2C 通道在「传感器配置」修改，统一在「硬件配置」卡片或顶栏提示保存落盘 NVS。'
  card.appendChild(hint)

  return card
}

function aboutItem(label: string, value: string): HTMLElement {
  const box = el('div', { class: 'flex flex-col gap-0.5' })
  const l = el('div', { class: 'text-xs' }, [label])
  l.style.color = 'rgb(var(--c-ink-subtle))'
  const v = el('div', { class: 'text-sm font-medium truncate' }, [value])
  v.style.color = 'rgb(var(--c-ink))'
  box.append(l, v)
  return box
}
