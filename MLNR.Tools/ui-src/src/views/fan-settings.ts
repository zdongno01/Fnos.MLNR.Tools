// 风扇控制通道设置页（Phase 3 升级：3 曲线 + 故障转速 + 控件靠右）
// CFG 类硬件参数：修改立即写入硬件内存（CFG 指令），需在设置中枢页 SAVE 落盘 NVS。
// 别名 / 温度参考点 / 曲线 / 故障转速：仅保存 fnOS 本地配置。

import { store } from '../store'
import { el, svgIcon, toast, cssVarColor } from '../ui'
import { canControl, pageContainer, pageNav, badge, requestRender, beginInteraction, endInteraction, helpTip } from './shared'
import type { CurvePoint, FanConfigKey, FanHWState } from '../types'
import { FAN_CONFIG_KEYS, defaultSpeedCurve, sensorKindLabel } from '../types'

// ===== 页面草稿（跨渲染保留，路由离开时重置） =====
interface FanDraft {
  fanId: number
  alias: string
  tempRefs: string[]
  curves: Record<string, CurvePoint[]>
  activeCurve: string
  faultSpd: number
  diskOfflineSpd: number
  dirty: boolean
  // 硬件参数草稿（修改不即时下发，「保存硬件参数」按钮统一下发）
  hwEnabled: boolean
  hwValues: Record<string, number>
  hwDirty: boolean
}
let draft: FanDraft | null = null

/** 曲线编辑器拖拽状态 */
let curveDrag: { index: number } | null = null

/** 曲线 tab key → 显示名 */
const CURVE_TABS: Array<{ key: string; label: string }> = [
  { key: 'efficient', label: '高效' },
  { key: 'daily', label: '日常' },
  { key: 'quiet', label: '静音' },
]

export function resetFanSettingsState(): void {
  draft = null
  curveDrag = null
}

export function renderFanSettings(fanId: number): HTMLElement {
  const cfg = store.settings.fans.find(f => f.id === fanId)
  if (!cfg) {
    return pageContainer([
      pageNav('风扇设置', '#/settings'),
      el('div', { class: 'card p-6 text-center text-sm' }, ['无效的风扇通道']),
    ])
  }
  const hw = store.fans.find(f => f.index === fanId) ?? null

  // 初始化/校验草稿（兼容旧数据）
  if (!draft || draft.fanId !== fanId) {
    const hwInit: Record<string, number> = {}
    for (const item of FAN_CONFIG_KEYS) hwInit[item.key] = hwValue(item.key, hw)
    // 迁移 Curves：旧数据只有 speedCurve → 填充 3 条
    let curves = cfg.curves
    if (!curves || Object.keys(curves).length === 0) {
      const src = cfg.speedCurve?.length >= 2 ? cfg.speedCurve : defaultSpeedCurve()
      curves = {
        efficient: src.map(p => ({ ...p })),
        daily: src.map(p => ({ ...p })),
        quiet: src.map(p => ({ ...p })),
      }
    }
    const activeCurve = cfg.activeCurve && curves[cfg.activeCurve] ? cfg.activeCurve : 'daily'
    // TempRefs 迁移：优先 tempRefs，回退旧 tempRef
    let tempRefs: string[] = Array.isArray(cfg.tempRefs) ? cfg.tempRefs.filter(Boolean) : []
    if (tempRefs.length === 0 && cfg.tempRef) {
      tempRefs = [cfg.tempRef]
    }
    draft = {
      fanId,
      alias: cfg.alias,
      tempRefs,
      curves,
      activeCurve,
      faultSpd: cfg.faultSpd ?? 50,
      diskOfflineSpd: cfg.diskOfflineSpd ?? 0,
      dirty: false,
      hwEnabled: hw?.enabled ?? true,
      hwValues: hwInit,
      hwDirty: false,
    }
  }

  const wrap = pageContainer([])
  wrap.appendChild(pageNav(`${cfg.alias || `风扇 ${fanId}`} · 通道设置`, '#/settings'))

  // ---- 硬件当前状态摘要 ----
  const statusCard = el('div', { class: 'card p-3 mb-4 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs' })
  statusCard.style.color = 'rgb(var(--c-ink-muted))'
  const stTitle = el('span', { class: 'font-semibold' }, ['硬件状态：'])
  stTitle.style.color = 'rgb(var(--c-ink))'
  statusCard.appendChild(stTitle)
  if (hw) {
    statusCard.append(
      el('span', {}, [hw.enabled ? '已启用' : '已禁用']),
      el('span', {}, [`目标 ${hw.targetSpd}% / 实际 ${hw.curSpd}%`]),
      el('span', {}, [`PWM ${hw.pwmFreq} Hz`]),
      el('span', {}, [`全局心跳超时 ${store.settings.hbTimeoutSec ?? 10}s`]),
      el('span', {}, [`上电转速 ${hw.powerSpd}%`]),
    )
  } else {
    statusCard.appendChild(el('span', {}, ['未连接硬件，仅可编辑本地配置']))
  }
  wrap.appendChild(statusCard)

  // ===== 硬件参数卡片 =====
  const hwCard = el('div', { class: 'card p-4 mb-4' })
  const hwTitle = el('div', { class: 'flex items-center gap-2 mb-1' })
  hwTitle.append(svgIcon('bolt', 15))
  const ht = el('span', { class: 'text-sm font-semibold' }, ['硬件参数（CFG 指令）'])
  ht.style.color = 'rgb(var(--c-ink))'
  hwTitle.appendChild(ht)
  hwTitle.appendChild(badge('NVS', '--c-primary', true))
  hwTitle.appendChild(helpTip('修改以下参数不会立即下发，点击【保存硬件参数】后统一写入硬件内存（CFG 指令）。直接写入 NVS 开启时自动落盘；关闭时断电丢失，需经顶栏提示或系统设置页落盘 NVS。'))
  hwCard.appendChild(hwTitle)
  const hwHint = el('div', { class: 'text-xs mb-2' },
    ['保存后统一写入硬件（NVS 落盘见问号）'])
  hwHint.style.color = 'rgb(var(--c-ink-subtle))'
  hwCard.appendChild(hwHint)

  // ---- 启用/禁用开关（草稿）靠右对齐 ----
  const enableRow = el('div', { class: 'form-row' })
  enableRow.appendChild(formLabel('启用/禁用风扇', 'CFG,FNx,ENABLED — 禁用会硬件立即停止风扇输出'))
  const enWrap = el('label', { class: 'toggle' })
  const enInput = el('input', { type: 'checkbox' }) as HTMLInputElement
  enInput.checked = draft.hwEnabled
  enInput.onchange = () => {
    draft!.hwEnabled = enInput.checked
    draft!.hwDirty = true
  }
  enWrap.append(enInput, el('span', { class: 'toggle-slider' }))
  enableRow.appendChild(controlWrapRight(enWrap))
  hwCard.appendChild(enableRow)

  // ---- 数值型 CFG 参数（草稿）靠右对齐 ----
  for (const item of FAN_CONFIG_KEYS) {
    const row = el('div', { class: 'form-row' })
    row.appendChild(formLabel(item.label, `CFG,FN${fanId},${item.key} · 取值 ${item.min}-${item.max} ${item.unit}`))
    const num = el('input', {
      type: 'number', class: 'input', style: 'max-width:160px',
      min: String(item.min), max: String(item.max),
      value: String(draft.hwValues[item.key] ?? 0),
    }) as HTMLInputElement
    num.onchange = () => {
      const v = Number(num.value)
      if (!Number.isFinite(v) || v < item.min || v > item.max) {
        toast(`${item.label} 取值范围 ${item.min}-${item.max} ${item.unit}`, 'error')
        num.value = String(draft!.hwValues[item.key] ?? 0)
        return
      }
      draft!.hwValues[item.key] = Math.round(v)
      draft!.hwDirty = true
    }
    const unit = el('span', { class: 'text-xs' }, [item.unit])
    unit.style.color = 'rgb(var(--c-ink-subtle))'
    const cw = el('div', { class: 'flex items-center gap-2' })
    cw.append(num, unit)
    row.appendChild(controlWrapRight(cw))
    hwCard.appendChild(row)
  }

  // ---- 保存硬件参数按钮（统一下发） ----
  const hwFoot = el('div', { class: 'flex justify-end mt-2' })
  const hwSaveBtn = el('button', { class: 'btn btn-primary' }, [svgIcon('save', 14), ' 保存硬件参数'])
  hwSaveBtn.disabled = !draft.hwDirty || !canControl() || !hw
  hwSaveBtn.title = !canControl() || !hw ? '蓝牙已连接且进入 WORK_RUN 加密会话后方可下发' : '下发 CFG 指令到硬件内存'
  hwSaveBtn.onclick = async () => {
    for (const item of FAN_CONFIG_KEYS) {
      const v = draft!.hwValues[item.key]
      if (!Number.isFinite(v) || v < item.min || v > item.max) {
        toast(`${item.label} 取值范围 ${item.min}-${item.max} ${item.unit}`, 'error')
        return
      }
    }
    hwSaveBtn.disabled = true
    const errors: string[] = []
    const okE = await store.setFanConfig(fanId, 'ENABLED', draft!.hwEnabled ? 1 : 0)
    if (!okE) errors.push('启用/禁用')
    for (const item of FAN_CONFIG_KEYS) {
      const ok = await store.setFanConfig(fanId, item.key, draft!.hwValues[item.key])
      if (!ok) errors.push(item.label)
    }
    hwSaveBtn.disabled = false
    if (errors.length === 0) {
      draft!.hwDirty = false
      const fans = store.settings.fans.map(f => f.id === fanId ? { ...f, enabled: draft!.hwEnabled } : f)
      store.settings = { ...store.settings, fans }
      toast('硬件参数已下发', 'success')
      document.dispatchEvent(new Event('rerender'))
    } else {
      toast(`下发失败：${errors.join('；')}`, 'error')
    }
  }
  hwFoot.appendChild(hwSaveBtn)
  hwCard.appendChild(hwFoot)
  wrap.appendChild(hwCard)

  // ===== 本地配置卡片 =====
  const localCard = el('div', { class: 'card p-4 mb-4' })
  const localTitle = el('div', { class: 'flex items-center gap-2 mb-2' })
  localTitle.append(svgIcon('settings', 15))
  const lt = el('span', { class: 'text-sm font-semibold' }, ['本地配置（仅保存 fnOS JSON，不写入硬件）'])
  lt.style.color = 'rgb(var(--c-ink))'
  localTitle.appendChild(lt)
  localTitle.appendChild(badge('本地', '--c-success', true))
  localCard.appendChild(localTitle)

  // ---- 别名 ----
  const aliasRow = el('div', { class: 'form-row' })
  aliasRow.appendChild(formLabel('风扇别名', '仅保存到 fnOS 本地配置文件，不写入硬件'))
  const aliasInput = el('input', {
    class: 'input', style: 'max-width:240px', placeholder: `风扇 ${fanId}`,
    value: draft.alias,
  }) as HTMLInputElement
  aliasInput.oninput = () => {
    draft!.alias = aliasInput.value
    draft!.dirty = true
  }
  aliasRow.appendChild(controlWrapRight(aliasInput))
  localCard.appendChild(aliasRow)

  // ---- 温度参考点（多列表，可添加/删除，fnOS 取列表中温度最高者调速）靠右对齐 ----
  const refRow = el('div', { class: 'form-row', style: 'flex-direction:column;align-items:stretch;' })
  const refTop = el('div', { class: 'flex items-center justify-between gap-4' })
  refTop.appendChild(formLabel('温度参考点', 'fnOS 自动调速时会取此列表中温度最高的传感器与曲线匹配。支持系统温度传感器和已启用的 I2C 温度传感器'))
  refTop.appendChild(controlWrapRight(buildTempRefSelect()))
  refRow.appendChild(refTop)
  refRow.appendChild(renderTempRefsChips())
  localCard.appendChild(refRow)

  // ---- 硬盘离线转速（参考点全为硬盘且全部硬盘下线时触发）靠右对齐 ----
  const offlineRow = el('div', { class: 'form-row' })
  offlineRow.appendChild(formLabel('硬盘离线转速', '温度参考点全部为硬盘，且所有绑定硬盘均正常下线或本次运行从未上线时，应用此转速（正常预期状态，非故障）。设为 0 关闭'))
  const offlineWrap = el('div', { class: 'flex items-center gap-2' })
  const offlineNum = el('input', {
    type: 'number', class: 'input', style: 'max-width:120px',
    min: '0', max: '100', value: String(draft.diskOfflineSpd),
  }) as HTMLInputElement
  offlineNum.onchange = () => {
    const v = Math.round(clamp(Number(offlineNum.value) || 0, 0, 100))
    draft!.diskOfflineSpd = v
    offlineNum.value = String(v)
    draft!.dirty = true
  }
  const offlineUnit = el('span', { class: 'text-xs' }, ['%'])
  offlineUnit.style.color = 'rgb(var(--c-ink-subtle))'
  offlineWrap.append(offlineNum, offlineUnit)
  offlineRow.appendChild(controlWrapRight(offlineWrap))
  localCard.appendChild(offlineRow)

  // ---- 故障转速（离线时触发）靠右对齐 ----
  const faultRow = el('div', { class: 'form-row' })
  faultRow.appendChild(formLabel('故障转速', '温度参考点离线（读不到值）时触发此转速；硬盘组手动下线时冻结不触发。设为 0 关闭'))
  const faultWrap = el('div', { class: 'flex items-center gap-2' })
  const faultNum = el('input', {
    type: 'number', class: 'input', style: 'max-width:120px',
    min: '0', max: '100', value: String(draft.faultSpd),
  }) as HTMLInputElement
  faultNum.onchange = () => {
    const v = Math.round(clamp(Number(faultNum.value) || 0, 0, 100))
    draft!.faultSpd = v
    faultNum.value = String(v)
    draft!.dirty = true
  }
  const faultUnit = el('span', { class: 'text-xs' }, ['%'])
  faultUnit.style.color = 'rgb(var(--c-ink-subtle))'
  faultWrap.append(faultNum, faultUnit)
  faultRow.appendChild(controlWrapRight(faultWrap))
  localCard.appendChild(faultRow)

  // ---- 曲线编辑器（3 曲线 tab + canvas） ----
  localCard.appendChild(renderCurveTabsAndEditor())

  // ---- 保存按钮 ----
  const foot = el('div', { class: 'flex justify-end mt-3' })
  const saveBtn = el('button', { class: 'btn btn-primary' }, [svgIcon('save', 15), ' 保存本地配置'])
  saveBtn.disabled = !draft.dirty
  saveBtn.onclick = async () => {
    const cur = draft!.curves[draft!.activeCurve]
    const fans = store.settings.fans.map(f => f.id === fanId ? {
      ...f,
      alias: draft!.alias.trim() || `风扇 ${fanId}`,
      tempRefs: draft!.tempRefs,
      // 清掉旧 tempRef（向后兼容字段）
      tempRef: undefined,
      curves: draft!.curves,
      activeCurve: draft!.activeCurve,
      faultSpd: draft!.faultSpd,
      diskOfflineSpd: draft!.diskOfflineSpd,
      // 同步 speedCurve 为 activeCurve（向后兼容）
      speedCurve: cur.map(p => ({ ...p })),
    } : f)
    const ok = await store.saveSettings({ ...store.settings, fans })
    if (ok) {
      draft!.dirty = false
      toast('本地配置已保存', 'success')
    } else {
      toast('保存失败，请重试', 'error')
    }
  }
  foot.appendChild(saveBtn)
  localCard.appendChild(foot)
  wrap.appendChild(localCard)

  return wrap
}

// ================================================================
// 3 曲线 tab + 曲线编辑器
// ================================================================
function renderCurveTabsAndEditor(): HTMLElement {
  const wrap = el('div', { class: 'form-row' })
  wrap.style.flexDirection = 'column'
  wrap.style.alignItems = 'stretch'

  const label = el('div', { class: 'form-label' }, ['自动调速曲线'])
  label.appendChild(helpTip('fnOS 自动模式下依据此曲线计算目标转速并周期下发。拖动圆点调整；点击空白处添加点；双击圆点删除（至少保留 2 个点）。'))
  wrap.appendChild(label)

  // ---- Active 曲线 tab 切换 ----
  const tabBar = el('div', { class: 'flex gap-1 mb-2' })
  for (const tab of CURVE_TABS) {
    const isActive = draft!.activeCurve === tab.key
    const btn = el('button', {
      class: 'btn btn-sm',
      style: isActive
        ? 'background:rgb(var(--c-primary));color:#fff;border-color:rgb(var(--c-primary));'
        : 'opacity:0.7;',
    }, [tab.label])
    btn.onclick = () => {
      draft!.activeCurve = tab.key
      draft!.dirty = true
      requestRender()
    }
    tabBar.appendChild(btn)
  }
  wrap.appendChild(tabBar)

  // const hint = el('div', { class: 'form-hint mb-2' },
  //   [`当前编辑：${CURVE_TABS.find(t => t.key === draft!.activeCurve)?.label}（操作见问号）`])
  // wrap.appendChild(hint)

  // 曲线内容（复用 renderCurveEditor，但从 draft!.curves[activeCurve] 读取）
  wrap.appendChild(renderCurveEditor())
  return wrap
}

function renderCurveEditor(): HTMLElement {
  const box = el('div')
  const canvas = el('canvas', { class: 'w-full block rounded' }) as HTMLCanvasElement
  canvas.style.background = 'rgb(var(--c-element))'
  canvas.style.height = '240px'
  canvas.style.cursor = 'crosshair'
  canvas.style.touchAction = 'none'

  // 编辑的是当前 active 曲线的拷贝
  const curveRef = (): CurvePoint[] => draft!.curves[draft!.activeCurve]

  drawCurve(canvas, curveRef())

  // ---- 交互：拖拽 / 添加 / 删除 ----
  const ptOf = (e: PointerEvent) => {
    const r = canvas.getBoundingClientRect()
    const x = e.clientX - r.left, y = e.clientY - r.top
    const temp = clamp(Math.round(((x - PAD.l) / (r.width - PAD.l - PAD.r)) * 100), 0, 100)
    const spd = clamp(Math.round((1 - (y - PAD.t) / (r.height - PAD.t - PAD.b)) * 100), 0, 100)
    return { x, y, temp, spd }
  }
  const hitTest = (x: number, y: number): number => {
    const r = canvas.getBoundingClientRect()
    let best = -1, bestDist = 14
    curveRef().forEach((p, i) => {
      const px = PAD.l + (p.temp / 100) * (r.width - PAD.l - PAD.r)
      const py = PAD.t + (1 - p.spd / 100) * (r.height - PAD.t - PAD.b)
      const d = Math.hypot(px - x, py - y)
      if (d < bestDist) { bestDist = d; best = i }
    })
    return best
  }

  canvas.addEventListener('pointerdown', (e) => {
    const r = canvas.getBoundingClientRect()
    const x = e.clientX - r.left, y = e.clientY - r.top
    const hit = hitTest(x, y)
    if (hit >= 0) {
      curveDrag = { index: hit }
      beginInteraction()
      canvas.setPointerCapture(e.pointerId)
    } else {
      const p = ptOf(e)
      const curve = curveRef()
      curve.push({ temp: p.temp, spd: p.spd })
      curve.sort((a, b) => a.temp - b.temp)
      draft!.dirty = true
      requestRender()
    }
  })
  canvas.addEventListener('pointermove', (e) => {
    if (!curveDrag) return
    const p = ptOf(e)
    const curve = curveRef()
    curve[curveDrag.index] = { temp: p.temp, spd: p.spd }
    curve.sort((a, b) => a.temp - b.temp)
    curveDrag.index = curve.findIndex(pt => pt.temp === p.temp && pt.spd === p.spd)
    drawCurve(canvas, curve)
  })
  const endDrag = () => {
    if (!curveDrag) return
    curveDrag = null
    endInteraction()
    draft!.dirty = true
  }
  canvas.addEventListener('pointerup', endDrag)
  canvas.addEventListener('pointercancel', endDrag)
  canvas.addEventListener('dblclick', (e) => {
    const r = canvas.getBoundingClientRect()
    const hit = hitTest(e.clientX - r.left, e.clientY - r.top)
    if (hit >= 0 && curveRef().length > 2) {
      curveRef().splice(hit, 1)
      draft!.dirty = true
      requestRender()
    }
  })

  // ---- 数值列表编辑 ----
  const table = el('div', { class: 'flex flex-col gap-1 mt-2' })
  curveRef()
    .map((_, i) => i)
    .forEach(i => {
      const p = curveRef()[i]
      const row = el('div', { class: 'flex items-center gap-2 text-xs' })
      const tempInput = el('input', {
        type: 'number', class: 'input', style: 'width:90px', min: '0', max: '100', value: String(p.temp),
      }) as HTMLInputElement
      const spdInput = el('input', {
        type: 'number', class: 'input', style: 'width:90px', min: '0', max: '100', value: String(p.spd),
      }) as HTMLInputElement
      const apply = () => {
        const t = clamp(Math.round(Number(tempInput.value) || 0), 0, 100)
        const s = clamp(Math.round(Number(spdInput.value) || 0), 0, 100)
        curveRef()[i] = { temp: t, spd: s }
        curveRef().sort((a, b) => a.temp - b.temp)
        draft!.dirty = true
        requestRender()
      }
      tempInput.onchange = apply
      spdInput.onchange = apply
      const xLabel = el('span', { class: 'w-14 text-right' }, ['温度 ℃'])
      xLabel.style.color = 'rgb(var(--c-ink-subtle))'
      const yLabel = el('span', { class: 'w-12 text-right' }, ['转速 %'])
      yLabel.style.color = 'rgb(var(--c-ink-subtle))'
      const delBtn = el('button', { class: 'btn btn-sm', title: '删除该点' }, [svgIcon('trash', 12)])
      delBtn.disabled = curveRef().length <= 2
      delBtn.onclick = () => {
        curveRef().splice(i, 1)
        draft!.dirty = true
        requestRender()
      }
      row.append(xLabel, tempInput, yLabel, spdInput, delBtn)
      table.appendChild(row)
    })
  const addBtn = el('button', { class: 'btn btn-sm mt-1', style: 'align-self:flex-start' },
    [svgIcon('plus', 12), ' 添加曲线点'])
  addBtn.onclick = () => {
    const curve = curveRef()
    const last = curve[curve.length - 1] ?? { temp: 50, spd: 50 }
    curve.push({ temp: clamp(last.temp + 5, 0, 100), spd: last.spd })
    curve.sort((a, b) => a.temp - b.temp)
    draft!.dirty = true
    requestRender()
  }

  box.append(canvas, table, addBtn)
  return box
}

const PAD = { l: 36, r: 12, t: 14, b: 26 }

function drawCurve(canvas: HTMLCanvasElement, curve: CurvePoint[]): void {
  const draw = () => {
    const cssW = canvas.clientWidth || 600
    const cssH = 240
    const dpr = Math.max(1, window.devicePixelRatio || 1)
    canvas.width = Math.round(cssW * dpr)
    canvas.height = Math.round(cssH * dpr)
    const ctx = canvas.getContext('2d')
    if (!ctx) return
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    ctx.clearRect(0, 0, cssW, cssH)

    // 图表配色随主题（CSS 变量，每次绘制重取）
    const ink = cssVarColor('--c-chart-text')
    const grid = cssVarColor('--c-chart-grid', 0.25)
    const series = cssVarColor('--c-primary')
    const chartW = cssW - PAD.l - PAD.r
    const chartH = cssH - PAD.t - PAD.b

    // 网格
    ctx.font = '10px system-ui, sans-serif'
    ctx.fillStyle = ink
    ctx.strokeStyle = grid
    for (let v = 0; v <= 100; v += 25) {
      const y = PAD.t + (1 - v / 100) * chartH
      ctx.beginPath(); ctx.moveTo(PAD.l, y); ctx.lineTo(PAD.l + chartW, y); ctx.stroke()
      ctx.textAlign = 'right'; ctx.textBaseline = 'middle'
      ctx.fillText(String(v), PAD.l - 5, y)
    }
    for (let v = 0; v <= 100; v += 25) {
      const x = PAD.l + (v / 100) * chartW
      ctx.beginPath(); ctx.moveTo(x, PAD.t); ctx.lineTo(x, PAD.t + chartH); ctx.stroke()
      ctx.textAlign = 'center'; ctx.textBaseline = 'top'
      ctx.fillText(String(v), x, PAD.t + chartH + 4)
    }
    ctx.textAlign = 'left'
    ctx.fillText('℃', PAD.l + chartW - 4, PAD.t + chartH + 4)
    ctx.save()
    ctx.translate(10, PAD.t + 8)
    ctx.fillText('%', 0, 0)
    ctx.restore()

    if (curve.length === 0) return
    // 折线
    ctx.strokeStyle = series
    ctx.lineWidth = 2
    ctx.beginPath()
    curve.forEach((p, i) => {
      const x = PAD.l + (p.temp / 100) * chartW
      const y = PAD.t + (1 - p.spd / 100) * chartH
      if (i === 0) ctx.moveTo(x, y)
      else ctx.lineTo(x, y)
    })
    ctx.stroke()
    // 渐变填充
    try {
      const grad = ctx.createLinearGradient(0, PAD.t, 0, PAD.t + chartH)
      grad.addColorStop(0, cssVarColor('--c-primary', 0.18))
      grad.addColorStop(1, cssVarColor('--c-primary', 0))
      ctx.fillStyle = grad
      ctx.lineTo(PAD.l + (curve[curve.length - 1].temp / 100) * chartW, PAD.t + chartH)
      ctx.lineTo(PAD.l + (curve[0].temp / 100) * chartW, PAD.t + chartH)
      ctx.closePath()
      ctx.fill()
    } catch { /* ignore */ }
    // 数据点
    for (const p of curve) {
      const x = PAD.l + (p.temp / 100) * chartW
      const y = PAD.t + (1 - p.spd / 100) * chartH
      ctx.beginPath()
      ctx.arc(x, y, 5, 0, Math.PI * 2)
      ctx.fillStyle = cssVarColor('--c-on-primary')
      ctx.fill()
      ctx.lineWidth = 2.5
      ctx.strokeStyle = series
      ctx.stroke()
    }
  }
  draw()
  const ro = new ResizeObserver(draw)
  ro.observe(canvas)
  ;(canvas as any)._redraw = draw
}

// ================================================================
// 辅助
// ================================================================

// ================================================================
// 温度参考点多选列表（需求 1.1：可添加/删除，fnOS 取温度最高者调速）
// ================================================================

/** 构建所有可选温度参考点选项（thermal + 已启用 I2C） */
function availableTempRefOptions(): Array<{ id: string; label: string; temp?: number }> {
  const list: Array<{ id: string; label: string; temp?: number }> = []
  // 1) thermal 传感器
  for (const t of store.thermal?.temps ?? []) {
    const alias = store.settings.tempSensors.find(s => s.id === t.id)?.alias
    const label = alias || t.name
    list.push({ id: t.id, label, temp: t.value })
  }
  // 2) 已启用的 I2C 传感器通道（取温度读数）
  const i2cChannels = store.settings.sensorChannels.filter(c => c.enabled)
  for (const ch of i2cChannels) {
    const rd = store.sensorReadings[ch.id]
    const temp = rd?.temperature
    const alias = ch.alias || sensorKindLabel(ch.kind)
    const label = `${alias}（CH${ch.id}）`
    // I2C 传感器的 tempRef ID 约定："i2c_ch_<id>"
    const id = `i2c_ch_${ch.id}`
    list.push({ id, label, temp })
  }
  return list
}

function renderTempRefsChips(): HTMLElement {
  const chipWrap = el('div', { class: 'flex flex-wrap items-center gap-2' })
  const options = availableTempRefOptions()

  if (draft!.tempRefs.length === 0) {
    const empty = el('div', { class: 'text-xs' }, ['（未设置，默认 CPU Package）'])
    empty.style.color = 'rgb(var(--c-ink-subtle))'
    chipWrap.appendChild(empty)
  } else {
    for (const refID of draft!.tempRefs) {
      const opt = options.find(o => o.id === refID)
      // 盘下线后 thermal 列表可能无该测温点：回退用持久化配置的别名（别名与在线状态无关）
      const cfgAlias = store.settings.tempSensors.find(sc => sc.id === refID)?.alias
      const label = opt ? opt.label : (cfgAlias || refID)
      const tempStr = opt?.temp !== undefined && opt.temp !== null ? `${opt.temp.toFixed(1)}℃` : '无读数'
      const chip = el('span', {
        class: 'inline-flex items-center gap-1 px-2.5 py-1 rounded-md text-xs',
        style: 'background:rgb(var(--c-primary-soft));color:rgb(var(--c-primary-soft-text));',
      }, [
        el('span', {}, [label]),
        el('span', { class: 'opacity-70' }, [`· ${tempStr}`]),
      ])
      const delBtn = el('button', {
        class: 'ml-1 opacity-60 hover:opacity-100',
        style: 'color:rgb(var(--c-primary-soft-text));border:none;background:none;cursor:pointer;font-size:14px;line-height:1;padding:0;',
        title: '移除此温度参考点',
      }, ['×'])
      delBtn.onclick = () => {
        draft!.tempRefs = draft!.tempRefs.filter(id => id !== refID)
        draft!.dirty = true
        requestRender()
      }
      chip.appendChild(delBtn)
      chipWrap.appendChild(chip)
    }
  }
  return chipWrap
}

function buildTempRefSelect(): HTMLSelectElement {
  const options = availableTempRefOptions()
  const remaining = options.filter(o => !draft!.tempRefs.includes(o.id))
  const select = el('select', {
    class: 'input', style: 'max-width:320px',
    title: '添加温度参考点',
  }) as HTMLSelectElement
  select.appendChild(el('option', { value: '' }, ['（选择要添加的参考点…）']))
  for (const opt of remaining) {
    const tempStr = opt.temp !== undefined && opt.temp !== null ? ` ${opt.temp.toFixed(1)}℃` : ''
    const label = `${opt.label}${tempStr}`
    select.appendChild(el('option', { value: opt.id }, [label]))
  }
  select.onchange = () => {
    const id = select.value
    if (!id) return
    if (!draft!.tempRefs.includes(id)) {
      draft!.tempRefs.push(id)
      draft!.dirty = true
    }
    select.value = ''
    requestRender()
  }
  return select
}

function hwValue(key: FanConfigKey, hw: FanHWState | null): number {
  if (!hw) return 0
  switch (key) {
    case 'POWER_SPD': return hw.powerSpd
    case 'HB_FALLBACK_SPD': return hw.hbFallback
    case 'PWM_FREQ': return hw.pwmFreq
    default: return 0
  }
}

function formLabel(text: string, hint?: string): HTMLElement {
  const label = el('div', { class: 'form-label' }, [text])
  if (hint) label.appendChild(helpTip(hint))
  return label
}

/** 靠右对齐的控件容器（Phase 3 统一靠右对齐） */
function controlWrapRight(...children: HTMLElement[]): HTMLElement {
  const w = el('div', { class: 'flex-1 flex items-center justify-end gap-3' })
  for (const c of children) w.appendChild(c)
  return w
}

function clamp(v: number, lo: number, hi: number): number {
  return Math.min(hi, Math.max(lo, v))
}
