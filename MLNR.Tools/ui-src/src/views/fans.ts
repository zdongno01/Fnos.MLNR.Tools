// 风扇控制页：从主页拆分，独立路由 #/fans
// 依据 FS3.1.md §3 实现。

import { store } from '../store'
import { el, svgIcon, toast, formatTemp } from '../ui'
import { requestRender } from './shared'
import { animateWidthFill } from '../components'
import { canControl, renderGateBanner, sectionTitle, badge } from './shared'
import type { FanHWState, FanChannelConfig } from '../types'

// ===== 模块级 UI 状态（跨渲染保留） =====
const ui = {
  /** 拖动中的风扇目标转速（fanId -> 本地值，避免回显跳变） */
  fanDragTarget: {} as Record<number, number>,
  /** 是否有交互进行中（拖动滑条时暂停整页重渲染） */
  interacting: false,
}

// #21：自动刷新实际转速的定时器（目标转速 != 实际转速时每 500ms 刷新）
let speedRefreshTimer: ReturnType<typeof setInterval> | null = null

let cachedEl: HTMLElement | null = null

// ================================================================
// 入口
// ================================================================
export function renderFans(): HTMLElement {
  if (ui.interacting) {
    setTimeout(() => requestRender(), 200)
    return cachedEl ?? el('div')
  }

  const wrap = el('div', { class: 'mx-auto w-full max-w-5xl px-4 py-5' })

  const gate = renderGateBanner()
  if (gate) wrap.appendChild(gate)

  wrap.appendChild(sectionTitle('fan', '风扇控制'))

  const activeFans = store.settings.fans.filter(cfg => {
    if (!cfg.enabled) return false
    const hw = store.fans.find(f => f.index === cfg.id)
    return !hw || hw.enabled
  })

  if (activeFans.length === 0) {
    const empty = el('div', { class: 'card px-4 py-8 text-center text-sm' },
      ['尚未启用任何风扇通道，请前往「系统设置 · 风扇控制通道」启用。'])
    empty.style.color = 'rgb(var(--c-ink-subtle))'
    wrap.appendChild(empty)
  } else {
    const grid = el('div', { class: 'grid grid-cols-1 lg:grid-cols-2 gap-4' })
    for (const cfg of activeFans) {
      const hw = store.fans.find(f => f.index === cfg.id)
      grid.appendChild(renderFanPanel(cfg, hw ?? null))
    }
    wrap.appendChild(grid)
  }

  cachedEl = wrap

  // #21：检查是否有风扇目标转速与实际转速不一致，若有则启动 500ms 自动刷新定时器
  checkAndStartSpeedRefresh()

  return wrap
}

// #21：检查所有已启用风扇，若存在 targetSpd != curSpd 则启动定时刷新
function checkAndStartSpeedRefresh(): void {
  const needsRefresh = store.settings.fans.some(cfg => {
    if (!cfg.enabled) return false
    const hw = store.fans.find(f => f.index === cfg.id)
    if (!hw) return false
    // 目标转速与实际转速差异超过 1% 时认为需要刷新
    return Math.abs((hw.targetSpd ?? 0) - (hw.curSpd ?? 0)) > 1
  })

  if (needsRefresh && !speedRefreshTimer) {
    speedRefreshTimer = setInterval(async () => {
      // Fix #11：页面隐藏时跳过轮询，节省后端请求
      if (document.hidden) return
      // 刷新前再次检查是否仍有不一致
      const stillNeeds = store.settings.fans.some(cfg => {
        if (!cfg.enabled) return false
        const hw = store.fans.find(f => f.index === cfg.id)
        if (!hw) return false
        return Math.abs((hw.targetSpd ?? 0) - (hw.curSpd ?? 0)) > 1
      })
      if (!stillNeeds) {
        stopSpeedRefresh()
        return
      }
      await store.refresh()
    }, 500)
  } else if (!needsRefresh && speedRefreshTimer) {
    stopSpeedRefresh()
  }
}

function stopSpeedRefresh(): void {
  if (speedRefreshTimer) {
    clearInterval(speedRefreshTimer)
    speedRefreshTimer = null
  }
}

// ================================================================
// 风扇面板（需求 §3）
// ================================================================
function renderFanPanel(cfg: FanChannelConfig, hw: FanHWState | null): HTMLElement {
  const card = el('div', { class: 'card p-4' })
  const hwEnabled = hw?.enabled ?? true
  const fanOn = (hw?.curSpd ?? 0) > 0
  const controllable = canControl() && hwEnabled && hw !== null

  // ---- 头部：图标 + 别名 + 徽标 ----
  const head = el('div', { class: 'flex items-center gap-2.5 mb-3' })
  const iconWrap = el('div', { class: 'w-9 h-9 rounded-lg flex items-center justify-center shrink-0' })
  iconWrap.style.background = 'rgb(var(--c-element))'
  iconWrap.style.color = fanOn ? 'rgb(var(--c-primary))' : 'rgb(var(--c-ink-subtle))'
  const fanIcon = svgIcon('fan', 19)
  if (fanOn && (hw?.curSpd ?? 0) >= 50) fanIcon.classList.add('fan-spinning-fast')
  else if (fanOn) fanIcon.classList.add('fan-spinning')
  iconWrap.appendChild(fanIcon)

  const titleBox = el('div', { class: 'flex-1 min-w-0' })
  const nameRow = el('div', { class: 'flex items-center gap-2 flex-wrap' })
  const name = el('span', { class: 'font-semibold text-[15px] truncate' }, [cfg.alias || `风扇 ${cfg.id}`])
  name.style.color = 'rgb(var(--c-ink))'
  if (store.isDebugMode()) nameRow.append(name, badge(`FAN${cfg.id}`, '--c-primary', true))
  else nameRow.appendChild(name)
  if (!hwEnabled) nameRow.appendChild(badge('已禁用', '--c-neutral'))
  if (hw?.heartbeat === 'TIMEOUT') nameRow.appendChild(badge('心跳超时', '--c-danger'))
  titleBox.appendChild(nameRow)

  const sub = el('div', { class: 'text-xs mt-0.5' })
  sub.style.color = 'rgb(var(--c-ink-subtle))'
  sub.textContent = `PWM ${hw ? (hw.pwmFreq / 1000).toFixed(0) : '—'} kHz · 后备转速 ${hw?.hbFallback ?? '—'}%`
  titleBox.appendChild(sub)

  // ---- 模式切换（分段控件） ----
  const modeSeg = el('div', { class: 'seg' })
  const autoBtn = el('button', {}, ['自动'])
  const manualBtn = el('button', {}, ['手动'])
  if (cfg.mode === 'auto') autoBtn.classList.add('seg-active')
  else manualBtn.classList.add('seg-active')
  autoBtn.disabled = !controllable
  manualBtn.disabled = !controllable
  autoBtn.onclick = async () => {
    if (await store.setFanMode(cfg.id, 'auto')) toast(`${cfg.alias || '风扇'} 已切换为自动模式`, 'success')
  }
  manualBtn.onclick = async () => {
    if (await store.setFanMode(cfg.id, 'manual')) toast(`${cfg.alias || '风扇'} 已切换为手动模式`, 'success')
  }
  modeSeg.append(autoBtn, manualBtn)

  head.append(iconWrap, titleBox, modeSeg)
  card.appendChild(head)

  // ---- 温度参考点（tempRefs 优先，兼容旧 tempRef；支持 thermal 与已启用 I2C） ----
  const refRow = el('div', { class: 'flex items-center justify-between gap-2 text-xs mb-2.5' })
  refRow.style.color = 'rgb(var(--c-ink-muted))'
  const refLabel = el('span', { class: 'shrink-0' }, ['温度参考点：'])
  const refIds = (cfg.tempRefs?.length ? cfg.tempRefs : cfg.tempRef ? [cfg.tempRef] : []) as string[]
  if (refIds.length === 0) {
    const none = el('span', {}, ['未设置 —'])
    none.style.color = 'rgb(var(--c-ink-subtle))'
    refRow.append(refLabel, none)
  } else {
    const refChips = el('div', { class: 'flex flex-wrap items-center gap-1.5' })
    for (const id of refIds) {
      const info = tempRefInfo(id)
      const chip = el('span', {
        class: 'inline-flex items-center gap-1 px-2.5 py-1 rounded-md text-xs',
        style: 'background:rgb(var(--c-primary-soft));color:rgb(var(--c-primary-soft-text));',
      }, [
        el('span', {}, [info ? info.name : id]),
        el('span', { class: 'font-semibold tabular-nums opacity-70' }, [info && info.value !== null ? formatTemp(info.value) : '—']),
      ])
      refChips.appendChild(chip)
    }
    refRow.append(refLabel, refChips)
  }
  card.appendChild(refRow)

  // ---- 双转速滑条 ----
  const targetVal = ui.fanDragTarget[cfg.id] ?? hw?.targetSpd ?? cfg.targetSpd ?? 0
  const actualVal = hw?.curSpd ?? 0
  const sliderDisabled = !controllable || cfg.mode === 'auto'

  const labelRow = el('div', { class: 'flex items-center justify-between text-xs mb-1' })
  const tgtLabel = el('span', {}, [])
  tgtLabel.innerHTML = `目标 <b style="color:rgb(var(--c-primary));font-size:13px">${Math.round(targetVal)}%</b>`
  const actLabel = el('span', {}, [])
  actLabel.innerHTML = `实际 <b style="color:rgb(var(--c-success));font-size:13px">${Math.round(actualVal)}%</b>`
  labelRow.append(tgtLabel, actLabel)
  card.appendChild(labelRow)

  const slider = el('div', { class: 'dual-slider' })
  const track = el('div', { class: 'ds-track' })
  const actualFill = el('div', { class: 'ds-actual' })
  // 实际条带动画：从上次值平滑过渡到新值（大跨度如 2%→50% 不再跳变）
  animateWidthFill(actualFill, `panel-fan${cfg.id}`, actualVal)
  const targetFill = el('div', { class: 'ds-target' })
  targetFill.style.width = `${clampPct(targetVal)}%`
  const input = el('input', {
    type: 'range', min: '0', max: '100', step: '1', value: String(Math.round(targetVal)),
  }) as HTMLInputElement
  input.disabled = sliderDisabled
  input.title = sliderDisabled
    ? (cfg.mode === 'auto' ? '自动模式下转速由温度曲线控制' : '当前状态不允许调速')
    : '拖动设定目标转速（松开后下发 SPD 指令）'

  input.addEventListener('input', () => {
    ui.fanDragTarget[cfg.id] = Number(input.value)
    targetFill.style.width = `${input.value}%`
    tgtLabel.innerHTML = `目标 <b style="color:rgb(var(--c-primary));font-size:13px">${input.value}%</b>`
  })
  input.addEventListener('pointerdown', () => { ui.interacting = true })
  const endDrag = () => {
    if (!ui.interacting) return
    ui.interacting = false
    requestRender()
  }
  input.addEventListener('pointerup', endDrag)
  input.addEventListener('pointercancel', endDrag)
  input.addEventListener('change', async () => {
    const v = Number(input.value)
    delete ui.fanDragTarget[cfg.id]
    input.disabled = true
    const ok = await store.setFanSpeed(cfg.id, v)
    input.disabled = false
    if (!ok) toast('调速指令下发失败，请检查连接状态', 'error')
  })

  track.append(targetFill, actualFill)
  slider.append(track, input)
  card.appendChild(slider)

  // ---- 模式提示 ----
  const hint = el('div', { class: 'text-xs mt-2' })
  hint.style.color = 'rgb(var(--c-ink-subtle))'
  hint.textContent = cfg.mode === 'auto'
    ? '自动模式：fnOS 依据温度-转速曲线自动计算并下发转速'
    : '手动模式：拖动滑块设定转速，松开后下发 SPD 指令'
  card.appendChild(hint)

  return card
}

// ================================================================
// 小工具
// ================================================================
/** 解析单个温度参考点（thermal 或 I2C 通道），返回显示名与当前值 */
function tempRefInfo(id: string): { name: string; value: number | null } | null {
  if (id.startsWith('i2c_ch_')) {
    const idx = Number(id.slice('i2c_ch_'.length))
    const rd = store.sensorReadings[idx]
    const chans = store.sensorChannelConfigs.length ? store.sensorChannelConfigs : store.settings.sensorChannels
    const chCfg = chans[idx]
    if (rd?.state === 'OK' && Number.isFinite(rd.temperature) && rd.temperature > 0) {
      return { name: chCfg?.alias ? `${chCfg.alias} 温度` : `CH${idx} 温度`, value: rd.temperature }
    }
    return { name: chCfg?.alias ? `${chCfg.alias} 温度` : `CH${idx} 温度`, value: null }
  }
  const t = store.thermal?.temps.find(x => x.id === id)
  if (t) {
    const cfg = store.settings.tempSensors.find(s => s.id === id)
    return { name: cfg?.alias || t.name, value: t.value }
  }
  // 盘下线等场景：thermal 列表无此测温点，但持久化配置（别名）仍可按 id 取到
  const cfgById = store.settings.tempSensors.find(s => s.id === id)
  if (cfgById?.alias) {
    return { name: cfgById.alias, value: null }
  }
  // 兼容旧绑定：磁盘测温点 ID 曾为 hdd_<设备名>_<zone>（如 hdd_sdb_temp1），
  // 现统一为 hdd_<SN>_<zone>。按同盘（ID 前缀 / Serial）匹配新 ID 显示温度。
  if (id.startsWith('hdd_')) {
    const key = id.slice(4, id.indexOf('_', 4) > 0 ? id.indexOf('_', 4) : undefined)
    const alt = store.thermal?.temps.find(x =>
      x.category === 'hdd' && (x.id.startsWith(`hdd_${key}_`) || x.serial === key))
    if (alt) {
      const cfg = store.settings.tempSensors.find(s => s.id === alt.id)
      return { name: cfg?.alias || alt.name, value: alt.value }
    }
    return { name: id, value: null }
  }
  return null
}

function clampPct(v: number): number {
  return Math.min(100, Math.max(0, v))
}

// 供 main.ts 在路由离开时清理
export function resetFansState(): void {
  ui.fanDragTarget = {}
  ui.interacting = false
  // #21：离开页面时清除自动刷新定时器
  stopSpeedRefresh()
}
