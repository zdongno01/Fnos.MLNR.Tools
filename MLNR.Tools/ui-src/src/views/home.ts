// 监控总览页（v3 重构版 · 专业监控台风格）
// 路由 #/
// 变更：删连接状态卡（顶栏已有）与 RSSI；KPI 3 卡；温度详情带临界进度条；
//       风扇状态合并双色速度条；硬盘组电源开关+挂载+温度组件；
//       历史趋势合并为 Tab 单图；新增最近事件日志摘要。

import { store } from '../store'
import { el, svgIcon, formatTemp, formatTime, drawLineChart, updateLineChart, LineSeries, toast, statusBadge, cssVarColor } from '../ui'
import type { IconName } from '../ui'
import { renderGateBanner, sectionTitle, badge, requestRender, downloadCSV, tempDeviceConfigOf } from './shared'
import { sensorKindLabel, HISTORY_RANGE_OPTIONS } from '../types'
import type {
  SensorChannelConfig, DiskGroupView, HistoryRange, HistoryPoint,
  FanChannelConfig, FanHWState, LogEntry,
  TemperatureReading, TempSensorConfig,
} from '../types'
import { TEMP_ICON_OPTIONS, smartDefaultIcon } from './settings'
import { kpiCard, panelCard, cardHeader, metricCell, dualBarReadonly, segmentedTab, progressBar } from '../components'

// ===== 历史趋势模块级状态（跨 rerender 保留） =====
// #33：无数据断线阈值（连续 3 分钟无数据则断开该段曲线）
const GAP_THRESHOLD_MS = 180_000
// 历史趋势系列色：CSS 变量名（定义于 style.css --c-series-1..8，随主题调整）
const SERIES_COLORS = [
  '--c-series-1', '--c-series-2', '--c-series-3', '--c-series-4',
  '--c-series-5', '--c-series-6', '--c-series-7', '--c-series-8',
]

type HistoryTab = 'temp' | 'speed' | 'humi'

/** 单个趋势页签的状态：全量滑动缓存 + 固定显示窗口（可拖动平移） */
interface TrendState {
  range: HistoryRange
  customStart: string
  customEnd: string
  data: Record<string, HistoryPoint[]> // 全量缓存（范围 cacheStart..cacheEnd，时间升序去重）
  hidden: Set<string>
  windowMs: number    // 固定窗口时长（custom = 0）
  refNow: number      // 窗口参照时间（加载/切范围时刻）；窗口 = [refNow-windowMs-offsetMs, refNow-offsetMs]
  offsetMs: number    // 窗口相对"当前"向左偏移（≥0，向过去），初始 0
  cacheStart: number  // 已加载缓存范围起点（ms）
  cacheEnd: number    // 已加载缓存范围终点（ms）
  lastPan: number     // 拖动越界补拉节流时间戳
  canvas: HTMLCanvasElement | null // 当前图表的 canvas（补拉后原地更新曲线）
}

const trend: Record<HistoryTab, TrendState> = {
  temp: { range: '1h', customStart: '', customEnd: '', data: {}, hidden: new Set(), windowMs: 3_600_000, refNow: 0, offsetMs: 0, cacheStart: 0, cacheEnd: 0, lastPan: 0, canvas: null },
  speed: { range: '1h', customStart: '', customEnd: '', data: {}, hidden: new Set(), windowMs: 3_600_000, refNow: 0, offsetMs: 0, cacheStart: 0, cacheEnd: 0, lastPan: 0, canvas: null },
  humi: { range: '1h', customStart: '', customEnd: '', data: {}, hidden: new Set(), windowMs: 3_600_000, refNow: 0, offsetMs: 0, cacheStart: 0, cacheEnd: 0, lastPan: 0, canvas: null },
}

const chart = {
  activeTab: 'temp' as HistoryTab,
  lastFetch: 0,
  fetching: false,
  lastSig: '',
}

// Fix #8：保存 drawLineChart 返回的 cleanup，路由离开时释放监听器
let chartCleanup: (() => void) | null = null

// ===== 监控总览页风扇实际转速轮询 =====
// 固件不主动推送风扇实际转速（仅 GETF 查询返回 Cur），若停留本页无轮询，
// 实际转速会冻结在最后一次查询值（目标转速仍会随自动调速变化）。
// 与风扇控制页一致：存在"目标≠实际"时每 500ms 调一次后端 refresh 回读。
let homeFanRefreshTimer: ReturnType<typeof setInterval> | null = null

function checkAndStartHomeFanRefresh(): void {
  const needs = store.settings.fans.some(cfg => {
    if (!cfg.enabled) return false
    const hw = store.fans.find(f => f.index === cfg.id)
    if (!hw) return false
    return Math.abs((hw.targetSpd ?? 0) - (hw.curSpd ?? 0)) > 1
  })
  if (needs && !homeFanRefreshTimer) {
    homeFanRefreshTimer = setInterval(async () => {
      // Fix #11：页面隐藏时跳过轮询，节省后端请求
      if (document.hidden) return
      const stillNeeds = store.settings.fans.some(cfg => {
        if (!cfg.enabled) return false
        const hw = store.fans.find(f => f.index === cfg.id)
        if (!hw) return false
        return Math.abs((hw.targetSpd ?? 0) - (hw.curSpd ?? 0)) > 1
      })
      if (!stillNeeds) {
        stopHomeFanRefresh()
        return
      }
      await store.refresh()
    }, 500)
  } else if (!needs && homeFanRefreshTimer) {
    stopHomeFanRefresh()
  }
}

function stopHomeFanRefresh(): void {
  if (homeFanRefreshTimer) {
    clearInterval(homeFanRefreshTimer)
    homeFanRefreshTimer = null
  }
}

// ================================================================
// 监控总览 · 可编辑布局（组件注册表 + 服务器端持久化）
// 页面级：每个组件独立 cell，尺寸（1/3、1/2、2/3、full）由用户手动设定
// 模块内部：'系统温度'/'环境传感器'/'风扇状态'组件内部的内容卡按启用数量自动均分
// ================================================================

export type CompSize = '1/3' | '1/2' | '2/3' | 'full'

export interface HomeLayoutItem {
  id: string
  size: CompSize
}

interface HomeCompDef {
  id: string
  title: string
  icon: IconName
  defaultSize: CompSize
  render: () => HTMLElement | null
}

const COMP_SIZE_ORDER: CompSize[] = ['1/3', '1/2', '2/3', 'full']

const COMP_SIZE_SPAN: Record<CompSize, string> = {
  '1/3': 'col-span-12 sm:col-span-6 lg:col-span-4',
  '1/2': 'col-span-12 lg:col-span-6',
  '2/3': 'col-span-12 sm:col-span-6 lg:col-span-8',
  'full': 'col-span-12',
}

// ===== 展示态紧凑布局（消除矮组件下方空白，实现"A 下紧贴 C"） =====
// 思路：组件高度按内容实时测量，量化为固定行高单位的 row-span；容器启用
// grid-auto-flow: row dense，由浏览器原生把后续组件回填到前面的空白行槽，
// 保持列内 DOM 顺序。仅在非编辑态应用；编辑态保持普通栅格（所见即所设）。
const DENSE_ROW_UNIT = 10 // 紧凑布局行高单位（px）：内容余量不足一行不计入，避免微跳动
// row-span 缓存（key=组件 id）：数据刷新会整树重建 grid，重建时若全部组件
// 已有缓存，直接以紧凑态创建 DOM，避免"普通布局 → 紧凑布局"来回闪烁。
const compactSpans = new Map<string, number>()

/**
 * 对监控总览 grid 校准紧凑布局（"临时普通 → 测量 → 恢复紧凑"同帧完成）：
 *  1. 先清除 span 恢复普通布局，强制 reflow 后测量各 cell 的内容真实高度；
 *  2. 再启用 dense 回填 + 固定行高并写回 row-span，同时更新缓存。
 * 清除与恢复在同一个同步块内完成（rAF 回调发生在浏览器绘制之前），
 * 浏览器不会绘制中间状态 → 无闪烁；内容高度变化也能自动收敛。
 */
function applyDenseCompact(grid: HTMLElement, keys: string[]): void {
  const cells = Array.from(grid.children) as HTMLElement[]
  if (cells.length === 0) return
  // 临时恢复普通布局（清 span），使 offsetHeight 反映内容真实高度
  grid.style.gridAutoFlow = ''
  grid.style.gridAutoRows = ''
  cells.forEach(c => { c.style.gridRow = '' })
  void grid.offsetHeight // 强制 reflow，让测量生效

  // 实际行间距（Tailwind gap-3 = 12px；从 computed style 读取，避免硬编码漂移）
  const gap = parseFloat(getComputedStyle(grid).rowGap) || 12
  const unit = DENSE_ROW_UNIT
  const spans = cells.map((cell, i) => {
    const h = cell.offsetHeight || unit
    // grid-row: span N 的实际渲染高度 = N*unit + (N-1)*gap（N-1 个行间隙也要占行高），
    // 若直接用 ceil(h/unit) 会低估行数 → cell 被拉高数倍产生大片空白。
    // 反推：N >= (h + gap) / (unit + gap)，向上取整保证完整显示、不裁切；下限 1 行
    const n = Math.max(1, Math.ceil((h + gap) / (unit + gap)))
    if (i < keys.length) compactSpans.set(keys[i], n)
    return n
  })
  grid.style.gridAutoFlow = 'row dense'
  grid.style.gridAutoRows = `${unit}px`
  cells.forEach((cell, i) => { cell.style.gridRow = `span ${spans[i]}` })
}

/** 监控总览可编辑组件注册表（简单 KPI ×3 + 详细 KPI ×3 + 详情区/状态/趋势/事件） */
const HOME_COMPONENTS: HomeCompDef[] = [
  { id: 'temp-simple', title: '系统温度·简', icon: 'thermometer', defaultSize: '1/3', render: renderTempKpiSimple },
  { id: 'fan-simple', title: '风扇转速·简', icon: 'fan', defaultSize: '1/3', render: renderFanKpiSimple },
  { id: 'disk-simple', title: '硬盘组·简', icon: 'hdd', defaultSize: '1/3', render: renderDiskKpiSimple },
  { id: 'temp-detail', title: '系统温度·详', icon: 'thermometer', defaultSize: '1/3', render: renderTempKpi },
  { id: 'fan-detail', title: '风扇转速·详', icon: 'fan', defaultSize: '1/3', render: renderFanKpi },
  { id: 'disk-detail', title: '硬盘组·详', icon: 'hdd', defaultSize: '1/3', render: renderDiskKpi },
  { id: 'temp-sensors', title: '温度传感器详情', icon: 'thermometer', defaultSize: 'full', render: renderTempSensors },
  { id: 'fan-status', title: '风扇状态', icon: 'fan', defaultSize: '1/2', render: renderFanStatus },
  { id: 'disk-status', title: '硬盘组状态', icon: 'hdd', defaultSize: '1/2', render: renderDiskGroupsDetail },
  { id: 'i2c', title: '环境传感器', icon: 'droplet', defaultSize: 'full', render: renderI2cSection },
  { id: 'trends', title: '历史趋势', icon: 'chart', defaultSize: 'full', render: renderTrendSection },
  { id: 'events', title: '最近事件', icon: 'log', defaultSize: 'full', render: renderRecentEvents },
]

const HOME_COMP_MAP: Record<string, HomeCompDef> = Object.fromEntries(HOME_COMPONENTS.map(c => [c.id, c]))

// ===== 布局持久化（服务器 settings.homeLayout 优先，localStorage 作离线兜底） =====
const LAYOUT_KEY = 'mlnr_home_layout_v1'

function defaultLayout(): HomeLayoutItem[] {
  return HOME_COMPONENTS.map(c => ({ id: c.id, size: c.defaultSize }))
}

/** 校验并规范化布局项数组 */
function sanitizeLayoutItems(rows: unknown): HomeLayoutItem[] {
  try {
    const arr = rows as Array<{ id?: unknown; size?: unknown }>
    if (!Array.isArray(arr)) return []
    const seen = new Set<string>()
    const items: HomeLayoutItem[] = []
    for (const x of arr) {
      if (!x || typeof x.id !== 'string' || !HOME_COMP_MAP[x.id]) continue
      if (seen.has(x.id)) continue
      seen.add(x.id)
      items.push({
        id: x.id,
        size: COMP_SIZE_ORDER.includes(x.size as CompSize) ? (x.size as CompSize) : HOME_COMP_MAP[x.id].defaultSize,
      })
    }
    return items
  } catch {
    return []
  }
}

/** 当前生效布局：优先服务器 settings.homeLayout，其次 localStorage 兜底，最后默认 */
function currentLayout(): HomeLayoutItem[] {
  const server = sanitizeLayoutItems(store.settings.homeLayout)
  if (server.length > 0) return server
  try {
    const raw = localStorage.getItem(LAYOUT_KEY)
    if (raw) {
      const parsed = JSON.parse(raw)
      const local = sanitizeLayoutItems(parsed?.items)
      if (local.length > 0) return local
    }
  } catch { /* 忽略 */ }
  return defaultLayout()
}

function persistLayout(items: HomeLayoutItem[]): void {
  // localStorage 兜底（离线/未连接后端时仍按上次布局显示）
  try {
    localStorage.setItem(LAYOUT_KEY, JSON.stringify({ v: 1, items }))
  } catch { /* 存储不可用时仅本次会话生效 */ }
}

/** 保存布局到服务器（所有终端统一），同时写 localStorage 兜底 */
async function saveLayoutToServer(items: HomeLayoutItem[]): Promise<boolean> {
  store.settings.homeLayout = items.map(x => ({ id: x.id, size: x.size }))
  const ok = await store.saveSettings(store.settings)
  if (ok) persistLayout(items)
  return ok
}

// ===== 编辑状态 =====
let layoutEditing = false
let layoutDraft: HomeLayoutItem[] = []

function startLayoutEdit(): void {
  layoutEditing = true
  layoutDraft = currentLayout().map(x => ({ ...x }))
  requestRender()
}

async function saveLayoutDraft(): Promise<void> {
  const items = layoutDraft.map(x => ({ ...x }))
  layoutEditing = false
  layoutDraft = []
  const ok = await saveLayoutToServer(items)
  requestRender()
  if (ok) {
    toast('监控总览布局已保存到服务器（所有终端统一显示）')
  } else {
    toast('布局保存失败：后端不可用，已保留本机兜底', 'error')
  }
}

function cancelLayoutEdit(): void {
  layoutEditing = false
  layoutDraft = []
  requestRender()
}

function moveLayoutItem(idx: number, dir: -1 | 1): void {
  const to = idx + dir
  if (to < 0 || to >= layoutDraft.length) return
  const tmp = layoutDraft[idx]
  layoutDraft[idx] = layoutDraft[to]
  layoutDraft[to] = tmp
  requestRender()
}

function removeLayoutItem(idx: number): void {
  layoutDraft.splice(idx, 1)
  requestRender()
}

function cycleLayoutSize(it: HomeLayoutItem): void {
  const i = COMP_SIZE_ORDER.indexOf(it.size)
  it.size = COMP_SIZE_ORDER[(i + 1) % COMP_SIZE_ORDER.length]
  requestRender()
}

/** 组件空数据时的占位卡（保持布局位置稳定） */
function placeholderCard(title: string): HTMLElement {
  const card = panelCard([cardHeader({ icon: 'info', title })])
  const txt = el('div', { class: 'text-xs py-1' }, ['暂无数据'])
  txt.style.color = 'rgb(var(--c-ink-subtle))'
  card.appendChild(txt)
  return card
}

// ===== 编辑 UI =====

/** 标题行：监控总览 + 编辑布局/保存/取消按钮 */
function renderHomeHeader(): HTMLElement {
  const head = el('div', { class: 'flex items-center justify-between gap-2 mb-2' })
  head.appendChild(sectionTitle('chart', '监控总览'))
  const actions = el('div', { class: 'flex items-center gap-2 shrink-0' })
  if (layoutEditing) {
    const save = el('button', { class: 'btn btn-sm btn-primary' }, ['保存布局'])
    save.onclick = saveLayoutDraft
    const cancel = el('button', { class: 'btn btn-sm' }, ['取消'])
    cancel.onclick = cancelLayoutEdit
    actions.append(save, cancel)
  } else {
    const edit = el('button', { class: 'btn btn-sm' }, [svgIcon('sliders', 13), ' 编辑布局'])
    edit.onclick = startLayoutEdit
    actions.appendChild(edit)
  }
  head.appendChild(actions)
  return head
}

/** 编辑态：添加组件面板（列出未加入布局的组件，点击追加到末尾） */
function renderAddPanel(): HTMLElement {
  const box = el('div', { class: 'card p-3 mb-4' })
  const label = el('div', { class: 'text-xs font-medium mb-2' }, ['添加组件（点击追加到末尾）'])
  label.style.color = 'rgb(var(--c-ink-muted))'
  box.appendChild(label)
  const chips = el('div', { class: 'flex flex-wrap gap-2' })
  const available = HOME_COMPONENTS.filter(c => !layoutDraft.some(i => i.id === c.id))
  if (available.length === 0) {
    const all = el('span', { class: 'text-xs' }, ['全部组件已在布局中'])
    all.style.color = 'rgb(var(--c-ink-subtle))'
    chips.appendChild(all)
  }
  for (const c of available) {
    const chip = el('button', { class: 'btn btn-sm' }, [svgIcon(c.icon, 13), ` ${c.title}`])
    chip.onclick = () => {
      layoutDraft.push({ id: c.id, size: c.defaultSize })
      requestRender()
    }
    chips.appendChild(chip)
  }
  box.appendChild(chips)
  return box
}

/** 编辑态：单个组件的工具条（上移/下移/切换大小/删除） */
function renderEditBar(def: HomeCompDef, it: HomeLayoutItem, idx: number, total: number): HTMLElement {
  const bar = el('div', { class: 'flex flex-wrap items-center gap-1 px-2 py-1.5 mb-1.5 rounded-md' })
  bar.style.background = 'rgb(var(--c-primary-soft))'
  bar.style.color = 'rgb(var(--c-primary-soft-text))'

  const title = el('span', { class: 'font-medium mr-1 text-[11px]' }, [def.title])
  bar.appendChild(title)

  const mkBtn = (text: string, tip: string, fn: () => void, disabled = false): HTMLElement => {
    const b = el('button', { class: 'text-[11px] leading-none px-1.5 py-1 rounded', title: tip, disabled })
    b.style.background = 'rgb(var(--c-element) / 0.6)'
    b.style.color = 'rgb(var(--c-ink))'
    b.style.border = '1px solid rgb(var(--c-line))'
    if (disabled) b.style.opacity = '0.4'
    b.onclick = fn
    b.append(el('span', {}, [text]))
    return b
  }

  bar.append(
    mkBtn('↑', '上移', () => moveLayoutItem(idx, -1), idx === 0),
    mkBtn('↓', '下移', () => moveLayoutItem(idx, 1), idx === total - 1),
    mkBtn(`尺寸 ${it.size}`, '切换大小：1/3 → 1/2 → 2/3 → 全宽', () => cycleLayoutSize(it)),
    mkBtn('删除', '移除该组件', () => removeLayoutItem(idx)),
  )
  return bar
}

/** 模块内部内容卡列数：1→1、2→2、3→3、≥4→2（两排各 50%） */
function innerCardCols(count: number): number {
  if (count === 1) return 1
  if (count === 3) return 3
  return 2
}

export function renderHome(): HTMLElement {
  const wrap = el('div', { class: 'mx-auto w-full max-w-5xl px-4 py-5' })

  const gate = renderGateBanner()
  if (gate) wrap.appendChild(gate)

  wrap.appendChild(renderHomeHeader())

  if (layoutEditing) wrap.appendChild(renderAddPanel())

  // ---- 按保存的布局渲染组件（编辑态渲染草稿）：每组件独立 cell，尺寸手动设定 ----
  const items = layoutEditing ? layoutDraft : currentLayout()
  // 展示态紧凑布局：若全部组件已有缓存的 row-span，重建时直接以紧凑态创建 DOM
  // （杜绝数据刷新导致"普通→紧凑"来回闪烁）；首次/布局变化时普通渲染，rAF 校准后补齐缓存。
  const compactReady = !layoutEditing && items.length > 0 && items.every(it => compactSpans.has(it.id))
  const grid = el('div', { class: 'grid grid-cols-12 gap-3 items-start' })
  if (compactReady) {
    grid.style.gridAutoFlow = 'row dense'
    grid.style.gridAutoRows = `${DENSE_ROW_UNIT}px`
  }

  if (items.length === 0) {
    const empty = el('div', { class: 'col-span-12 card p-6 text-center text-xs' })
    empty.style.color = 'rgb(var(--c-ink-subtle))'
    empty.append('布局为空，点击右上角「编辑布局」添加组件')
    grid.appendChild(empty)
  }

  items.forEach((it, idx) => {
    const def = HOME_COMP_MAP[it.id]
    if (!def) return
    const cell = el('div', { class: COMP_SIZE_SPAN[it.size] })
    if (compactReady) cell.style.gridRow = `span ${compactSpans.get(it.id) ?? 1}`
    if (layoutEditing) {
      cell.appendChild(renderEditBar(def, it, idx, items.length))
      const content = def.render() ?? placeholderCard(def.title)
      // 编辑态禁用组件自身交互（点击跳转等），只响应工具条
      content.style.pointerEvents = 'none'
      cell.appendChild(content)
    } else {
      cell.appendChild(def.render() ?? placeholderCard(def.title))
    }
    grid.appendChild(cell)
  })
  wrap.appendChild(grid)

  // 数据联动：仅当布局包含趋势图/最近事件时拉取对应数据
  const activeItems = layoutEditing ? layoutDraft : currentLayout()
  if (activeItems.some(i => i.id === 'events')) ensureLogs()
  if (activeItems.some(i => i.id === 'trends')
    && !chart.fetching && (chartSignature() !== chart.lastSig || Date.now() - chart.lastFetch > 60_000)) {
    void loadChartData()
  }

  // 展示态校准紧凑布局（rAF 回调在浏览器绘制前执行，内部"清→测→恢复"同帧完成，
  // 不产生中间绘制 → 无闪烁）；编辑态保持普通栅格
  if (!layoutEditing && items.length > 0) {
    const keys = items.map(i => i.id)
    requestAnimationFrame(() => applyDenseCompact(grid, keys))
  }

  // 监控总览停留时轮询回读风扇实际转速（目标≠实际时 500ms refresh）
  if (!layoutEditing) checkAndStartHomeFanRefresh()

  return wrap
}

// ================================================================
// KPI 放大卡（监控总览首屏：温度 / 风扇 / 硬盘组）
// ================================================================

/** 放大版 KPI 卡容器：hover 上浮 + 点击跳转（与 kpiCard 交互一致，内容更丰富） */
function bigKpiCard(onClick: () => void): HTMLElement {
  const card = el('div', { class: 'card p-4 cursor-pointer' })
  card.style.transition = 'transform .15s ease, border-color .15s ease'
  card.addEventListener('mouseenter', () => {
    card.style.transform = 'translateY(-1px)'
    card.style.borderColor = 'rgb(var(--c-primary) / 0.35)'
  })
  card.addEventListener('mouseleave', () => {
    card.style.transform = 'none'
    card.style.borderColor = ''
  })
  card.addEventListener('click', onClick)
  return card
}

/** 放大卡头部：图标 + 标题 + 右侧徽标 */
function bigKpiHead(icon: IconName, label: string, badgeEl?: HTMLElement): HTMLElement {
  const head = el('div', { class: 'flex items-center justify-between gap-2 mb-2.5' })
  const titleBox = el('div', { class: 'flex items-center gap-2 min-w-0' })
  const iconWrap = el('div', { class: 'w-7 h-7 rounded-md flex items-center justify-center shrink-0' })
  iconWrap.style.background = 'rgb(var(--c-element))'
  iconWrap.style.color = 'rgb(var(--c-primary))'
  iconWrap.appendChild(svgIcon(icon, 14))
  const labelEl = el('span', { class: 'text-sm font-medium truncate' }, [label])
  labelEl.style.color = 'rgb(var(--c-ink-muted))'
  titleBox.append(iconWrap, labelEl)
  head.appendChild(titleBox)
  if (badgeEl) head.appendChild(badgeEl)
  return head
}

/** 放大卡大数字 */
function bigKpiValue(text: string, color: string): HTMLElement {
  const v = el('div', { class: 'text-[28px] font-bold leading-none tabular-nums' }, [text])
  v.style.color = color
  return v
}

/** 迷你条形（温度对比行用） */
function miniBar(pct: number, color: string): HTMLElement {
  const track = el('div', { class: 'bar-track' })
  const fill = el('div', { class: 'bar-fill' })
  fill.style.width = `${Math.min(100, Math.max(0, pct))}%`
  fill.style.background = color
  track.appendChild(fill)
  return track
}

/** 传感器展示名：优先用户别名；无别名时拼接原始名 + 测温点，体现物理位置 */
function sensorName(cfg: TempSensorConfig | undefined, t: TemperatureReading): string {
  const alias = (cfg?.alias ?? '').trim()
  if (alias) return alias
  return t.zone && !t.name.includes(t.zone) ? `${t.name} · ${t.zone}` : t.name
}

// ================================================================
// 简单 KPI 组件（旧版三卡，与详细版并存，供布局编辑选择）
// ================================================================
function renderTempKpiSimple(): HTMLElement {
  const temps = store.thermal?.temps ?? []
  const cfgMap = new Map(store.settings.tempSensors.map(s => [s.id, s]))
  const shown = temps.filter(t => {
    const cfg = cfgMap.get(t.id)
    return !cfg || cfg.show !== false
  })

  if (shown.length === 0) {
    return kpiCard({
      icon: 'thermometer', label: '系统温度', value: '—',
      subtext: '暂无温度数据', tone: 'neutral',
      onClick: () => { location.hash = '#/sensors' },
    })
  }

  const maxTemp = Math.max(...shown.map(t => t.value))
  const crits = shown.filter(t => t.crit && t.crit > 0).map(t => t.crit as number)
  const ref = crits.length > 0 ? Math.max(...crits) : 100

  return kpiCard({
    icon: 'thermometer',
    label: '系统温度',
    value: `${maxTemp.toFixed(1)}℃`,
    valueColor: tempAutoColor(maxTemp),
    tone: maxTemp >= 60 ? 'danger' : maxTemp >= 45 ? 'warning' : 'success',
    bar: { pct: (maxTemp / ref) * 100, color: tempAutoColor(maxTemp) },
    subtext: `${shown.length} 个传感器${crits.length > 0 ? ` · 临界 ${Math.max(...crits)}℃` : ''}`,
    onClick: () => { location.hash = '#/sensors' },
  })
}

function renderFanKpiSimple(): HTMLElement {
  const activeFans = activeFanConfigs()

  if (activeFans.length === 0) {
    return kpiCard({
      icon: 'fan', label: '风扇转速', value: '—',
      subtext: '未启用风扇', tone: 'neutral',
      onClick: () => { location.hash = '#/fans' },
    })
  }

  const hws = activeFans.map(cfg => store.fans.find(f => f.index === cfg.id))
  const avg = mean(hws.map(h => h?.curSpd ?? 0))
  const targetAvg = mean(hws.map(h => h?.targetSpd ?? 0))

  return kpiCard({
    icon: 'fan',
    label: '风扇转速',
    value: `${Math.round(avg)}%`,
    tone: avg > 0 ? 'success' : 'neutral',
    bar: { pct: avg, color: 'rgb(var(--c-success))' },
    subtext: `${activeFans.length} 路风扇 · 目标 ${Math.round(targetAvg)}%`,
    onClick: () => { location.hash = '#/fans' },
  })
}

function renderDiskKpiSimple(): HTMLElement {
  const connected = store.device?.connectionState === 'connected'

  if (!connected) {
    return kpiCard({
      icon: 'hdd', label: '硬盘组', value: '—',
      subtext: '未启用硬盘组', tone: 'neutral',
      onClick: () => { location.hash = '#/disks' },
    })
  }

  const groups = store.diskGroups.filter(g => g.enabled)
  const online = groups.filter(g => g.online).length
  const powerOn = groups.filter(g =>
    store.switches.find(s => s.index === g.switchN)?.state === 1,
  ).length

  if (groups.length === 0) {
    return kpiCard({
      icon: 'hdd', label: '硬盘组', value: '0/0',
      subtext: '未配置硬盘组', tone: 'neutral',
      onClick: () => { location.hash = '#/disks' },
    })
  }

  const allOnline = online === groups.length
  return kpiCard({
    icon: 'hdd',
    label: '硬盘组',
    value: `${online}/${groups.length}`,
    tone: allOnline ? 'success' : 'warning',
    bar: {
      pct: (online / groups.length) * 100,
      color: allOnline ? 'rgb(var(--c-success))' : 'rgb(var(--c-warning))',
    },
    subtext: `${groups.length} 组 · 电源开启 ${powerOn}`,
    onClick: () => { location.hash = '#/disks' },
  })
}

// ================================================================
// 系统温度 KPI — 大数字最高温 + 位置 + Top3 温度对比
// ================================================================
function renderTempKpi(): HTMLElement {
  const card = bigKpiCard(() => { location.hash = '#/sensors' })
  const temps = store.thermal?.temps ?? []
  const cfgMap = new Map(store.settings.tempSensors.map(s => [s.id, s]))
  const shown = temps.filter(t => {
    const cfg = cfgMap.get(t.id)
    return !cfg || cfg.show !== false
  })

  if (shown.length === 0) {
    card.appendChild(bigKpiHead('thermometer', '系统温度', badge('无数据', '--c-neutral', true)))
    const value = bigKpiValue('—', 'rgb(var(--c-ink-subtle))')
    value.classList.add('my-3')
    card.appendChild(value)
    const sub = el('div', { class: 'text-[11px]' }, ['暂无温度数据'])
    sub.style.color = 'rgb(var(--c-ink-subtle))'
    card.appendChild(sub)
    return card
  }

  // 按温度降序，取最高温 + Top3
  const sorted = [...shown].sort((a, b) => b.value - a.value)
  const top = sorted.slice(0, 3)
  const maxSensor = sorted[0]
  const maxTemp = maxSensor.value
  const crits = shown.filter(t => t.crit && t.crit > 0).map(t => t.crit as number)
  const ref = crits.length > 0 ? Math.max(...crits) : 100

  card.appendChild(bigKpiHead('thermometer', '系统温度', badge(`${shown.length} 个传感器`, '--c-neutral', true)))

  // 大数字 + 最高温位置
  const valueRow = el('div', { class: 'flex items-end gap-2.5' })
  valueRow.appendChild(bigKpiValue(formatTemp(maxTemp), tempAutoColor(maxTemp)))
  const posWrap = el('div', { class: 'flex items-center gap-1 pb-1 min-w-0' })
  const pinDot = el('span', { class: 'w-1.5 h-1.5 rounded-full shrink-0' })
  pinDot.style.background = tempAutoColor(maxTemp)
  const posText = el('span', { class: 'truncate text-[11px] font-medium' }, [sensorName(cfgMap.get(maxSensor.id), maxSensor)])
  posText.style.color = 'rgb(var(--c-ink-subtle))'
  posWrap.append(pinDot, posText)
  valueRow.appendChild(posWrap)
  card.appendChild(valueRow)

  // 最高温进度条（相对临界/100℃）
  const barWrap = el('div', { class: 'mt-2.5' })
  barWrap.appendChild(progressBar((maxTemp / ref) * 100, tempAutoColor(maxTemp)))
  card.appendChild(barWrap)

  // Top3 位置温度对比（体现各位置差异）
  const list = el('div', { class: 'flex flex-col gap-1 mt-2.5' })
  for (const t of top) {
    const isMax = t.id === maxSensor.id
    const row = el('div', { class: 'flex items-center gap-2 text-xs min-w-0' })
    const nm = el('span', { class: 'truncate flex-1' }, [sensorName(cfgMap.get(t.id), t)])
    nm.style.color = isMax ? 'rgb(var(--c-ink))' : 'rgb(var(--c-ink-muted))'
    nm.style.fontWeight = isMax ? '600' : '400'
    const barBox = el('div', { class: 'w-16 shrink-0' })
    barBox.appendChild(miniBar((t.value / ref) * 100, tempAutoColor(t.value)))
    const val = el('span', { class: 'shrink-0 tabular-nums font-medium' }, [formatTemp(t.value)])
    val.style.color = tempAutoColor(t.value)
    row.append(nm, barBox, val)
    list.appendChild(row)
  }
  card.appendChild(list)

  // 副行：传感器数 + 临界
  const foot = el('div', { class: 'flex items-center gap-2 mt-2.5 text-[11px]' })
  const crit = crits.length > 0 ? Math.max(...crits) : 0
  if (crit > 0) {
    const critTxt = el('span', { class: 'tabular-nums' }, [`临界 ${Math.round(crit)}℃`])
    critTxt.style.color = 'rgb(var(--c-ink-subtle))'
    foot.appendChild(critTxt)
  }
  const hint = el('span', { class: 'ml-auto shrink-0' }, ['查看全部 →'])
  hint.style.color = 'rgb(var(--c-ink-subtle))'
  foot.appendChild(hint)
  card.appendChild(foot)

  return card
}

// ================================================================
// 风扇转速 KPI — 大数字平均 + 每路双色条（实际/目标）
// ================================================================
function renderFanKpi(): HTMLElement {
  const card = bigKpiCard(() => { location.hash = '#/fans' })
  const activeFans = activeFanConfigs()

  if (activeFans.length === 0) {
    card.appendChild(bigKpiHead('fan', '风扇转速', badge('未启用', '--c-neutral', true)))
    const value = bigKpiValue('—', 'rgb(var(--c-ink-subtle))')
    value.classList.add('my-3')
    card.appendChild(value)
    const sub = el('div', { class: 'text-[11px]' }, ['未启用风扇'])
    sub.style.color = 'rgb(var(--c-ink-subtle))'
    card.appendChild(sub)
    return card
  }

  const hws = activeFans.map(cfg => store.fans.find(f => f.index === cfg.id))
  const avg = mean(hws.map(h => h?.curSpd ?? 0))
  const targetAvg = mean(hws.map(h => h?.targetSpd ?? 0))

  card.appendChild(bigKpiHead('fan', '风扇转速', badge(`${activeFans.length} 路启用`, '--c-neutral', true)))

  // 大数字：平均实际转速
  const valueRow = el('div', { class: 'flex items-end gap-2.5' })
  valueRow.appendChild(bigKpiValue(`${Math.round(avg)}%`, avg > 0 ? 'rgb(var(--c-success))' : 'rgb(var(--c-ink-subtle))'))
  const avgLabel = el('span', { class: 'pb-1 text-[11px]' }, [`平均 · 目标 ${Math.round(targetAvg)}%`])
  avgLabel.style.color = 'rgb(var(--c-ink-subtle))'
  valueRow.appendChild(avgLabel)
  card.appendChild(valueRow)

  // 每路风扇：FANx + 别名 + 实际/目标 + 双色条
  const list = el('div', { class: 'flex flex-col gap-2 mt-2.5' })
  for (const cfg of activeFans) {
    const hw = store.fans.find(f => f.index === cfg.id)
    const cur = hw?.curSpd ?? 0
    const target = hw?.targetSpd ?? cfg.targetSpd ?? 0

    const row1 = el('div', { class: 'flex items-center gap-2 text-xs min-w-0' })
    if (store.isDebugMode()) row1.appendChild(badge(`FAN${cfg.id}`, '--c-primary', true))
    const name = el('span', { class: 'truncate flex-1 font-medium' }, [cfg.alias || `风扇 ${cfg.id}`])
    name.style.color = 'rgb(var(--c-ink))'
    const right = el('span', { class: 'shrink-0 tabular-nums' }, [`${Math.round(cur)}% · 目标 ${Math.round(target)}%`])
    right.style.color = 'rgb(var(--c-ink-subtle))'
    row1.append(name, right)
    list.appendChild(row1)
    list.appendChild(dualBarReadonly(cur, target, `home-fan${cfg.id}`))
  }
  card.appendChild(list)

  const foot = el('div', { class: 'flex items-center gap-2 mt-2.5 text-[11px]' })
  const legend = el('span', { class: 'flex items-center gap-1' })
  const g = el('span', { class: 'w-2 h-1.5 rounded-full' })
  g.style.background = 'rgb(var(--c-success) / 0.75)'
  const gt = el('span', {}, ['实际'])
  gt.style.color = 'rgb(var(--c-ink-subtle))'
  const b = el('span', { class: 'w-2 h-1.5 rounded-full' })
  b.style.background = 'rgb(var(--c-primary) / 0.45)'
  const bt = el('span', {}, ['目标'])
  bt.style.color = 'rgb(var(--c-ink-subtle))'
  legend.append(g, gt, b, bt)
  foot.appendChild(legend)
  const hint = el('span', { class: 'ml-auto shrink-0' }, ['控制风扇 →'])
  hint.style.color = 'rgb(var(--c-ink-subtle))'
  foot.appendChild(hint)
  card.appendChild(foot)

  return card
}

// ================================================================
// 硬盘组 KPI — 大数字在线数 + 每组指示器（上线/盘在线/挂载）
// ================================================================
function renderDiskKpi(): HTMLElement {
  const card = bigKpiCard(() => { location.hash = '#/disks' })
  const connected = store.device?.connectionState === 'connected'

  if (!connected) {
    card.appendChild(bigKpiHead('hdd', '硬盘组', badge('未连接', '--c-neutral', true)))
    const value = bigKpiValue('—', 'rgb(var(--c-ink-subtle))')
    value.classList.add('my-3')
    card.appendChild(value)
    const sub = el('div', { class: 'text-[11px]' }, ['蓝牙未连接 · 连接后显示硬盘组状态'])
    sub.style.color = 'rgb(var(--c-ink-subtle))'
    card.appendChild(sub)
    return card
  }

  const groups = store.diskGroups.filter(g => g.enabled)
  if (groups.length === 0) {
    card.appendChild(bigKpiHead('hdd', '硬盘组', badge('未配置', '--c-neutral', true)))
    const value = bigKpiValue('—', 'rgb(var(--c-ink-subtle))')
    value.classList.add('my-3')
    card.appendChild(value)
    const sub = el('div', { class: 'text-[11px]' }, ['未配置硬盘组'])
    sub.style.color = 'rgb(var(--c-ink-subtle))'
    card.appendChild(sub)
    return card
  }

  const powerOn = groups.filter(g =>
    store.switches.find(s => s.index === g.switchN)?.state === 1,
  ).length

  card.appendChild(bigKpiHead('hdd', '硬盘组', badge(`${groups.length} 组启用`, '--c-neutral', true)))

  // 每组一个指示器：组上线状态 + 关联硬盘在线/挂载
  const list = el('div', { class: 'flex flex-col gap-2 mt-2.5' })
  for (const g of groups) list.appendChild(diskGroupIndicator(g))
  card.appendChild(list)

  const foot = el('div', { class: 'flex items-center gap-2 mt-2.5 text-[11px]' })
  const pwr = el('span', { class: 'tabular-nums' }, [`电源开启 ${powerOn}/${groups.length}`])
  pwr.style.color = 'rgb(var(--c-ink-subtle))'
  foot.appendChild(pwr)
  const hint = el('span', { class: 'ml-auto shrink-0' }, ['管理硬盘组 →'])
  hint.style.color = 'rgb(var(--c-ink-subtle))'
  foot.appendChild(hint)
  card.appendChild(foot)

  return card
}

/** 硬盘组指示器：组行（上线/电源）+ 盘行（在线/挂载/温度） */
function diskGroupIndicator(g: DiskGroupView): HTMLElement {
  const sw = store.switches.find(s => s.index === g.switchN)
  const powerOn = sw?.state === 1

  const block = el('div', { class: 'rounded-lg px-2.5 py-2' })
  block.style.background = 'rgb(var(--c-element) / 0.4)'

  // 组行
  const row = el('div', { class: 'flex items-center gap-2 text-xs min-w-0' })
  const dot = el('span', { class: 'w-2 h-2 rounded-full shrink-0' })
  dot.style.background = g.online ? 'rgb(var(--c-success))' : 'rgb(var(--c-neutral))'
  if (g.online) {
    dot.animate(
      [{ boxShadow: '0 0 0 0 rgb(var(--c-success) / 0.4)' }, { boxShadow: '0 0 0 4px rgb(var(--c-success) / 0)' }],
      { duration: 1800, iterations: Infinity },
    )
  }
  const gname = el('span', { class: 'truncate font-medium' }, [g.alias || `硬盘组 ${g.id}`])
  gname.style.color = g.online ? 'rgb(var(--c-ink))' : 'rgb(var(--c-ink-muted))'
  row.append(dot, gname)

  const meta = el('span', { class: 'ml-auto flex items-center gap-2 shrink-0' })
  const onlineTxt = el('span', { class: 'text-[11px]' }, [g.online ? '在线' : '离线'])
  onlineTxt.style.color = g.online ? 'rgb(var(--c-success-soft-text))' : 'rgb(var(--c-ink-subtle))'
  const pwrTxt = el('span', { class: 'text-[11px]' }, [powerOn ? '电源开' : '电源关'])
  pwrTxt.style.color = powerOn ? 'rgb(var(--c-success-soft-text))' : 'rgb(var(--c-ink-subtle))'
  meta.append(onlineTxt, pwrTxt)
  row.appendChild(meta)
  block.appendChild(row)

  // 盘行：每块盘在线/挂载状态
  const disks = g.disks ?? []
  if (disks.length === 0) {
    const note = el('div', { class: 'text-[11px] mt-1 pl-4' }, [g.online ? '在线（未配置磁盘）' : '已启用 · 未上线'])
    note.style.color = 'rgb(var(--c-ink-subtle))'
    block.appendChild(note)
    return block
  }

  const dlist = el('div', { class: 'flex flex-col gap-0.5 mt-1' })
  for (const d of disks) {
    const drow = el('div', { class: 'flex items-center gap-1.5 text-[11px] min-w-0 pl-4' })
    const ddot = el('span', { class: 'w-1.5 h-1.5 rounded-full shrink-0' })
    ddot.style.background = !g.online
      ? 'rgb(var(--c-neutral))'
      : d.mounted ? 'rgb(var(--c-success))' : 'rgb(var(--c-warning))'
    const dname = el('span', { class: 'truncate' }, [d.alias || d.device])
    dname.style.color = !g.online ? 'rgb(var(--c-ink-subtle))' : 'rgb(var(--c-ink-muted))'
    const dstat = el('span', { class: 'shrink-0' }, [!g.online ? '未上线' : (d.mounted ? '已挂载' : '未挂载')])
    dstat.style.color = !g.online
      ? 'rgb(var(--c-ink-subtle))'
      : d.mounted ? 'rgb(var(--c-success-soft-text))' : 'rgb(var(--c-warning-soft-text))'
    drow.append(ddot, dname, dstat)
    if (d.temperature != null && g.online) {
      const tmp = el('span', {
        class: 'ml-auto shrink-0 font-medium tabular-nums rounded px-1.5 py-0.5',
      }, [formatTemp(d.temperature)])
      tmp.style.color = tempAutoColor(d.temperature)
      tmp.style.background = 'rgb(var(--c-element))'
      drow.appendChild(tmp)
    }
    dlist.appendChild(drow)
  }
  block.appendChild(dlist)
  return block
}

// ================================================================
// 温度传感器详情 — 图标配置色 + 温度自动变色 + 临界进度条
// ================================================================
function renderTempSensors(): HTMLElement | null {
  const temps = store.thermal?.temps ?? []
  const cfgMap = new Map(store.settings.tempSensors.map(s => [s.id, s]))
  const shown = temps.filter(t => {
    const cfg = cfgMap.get(t.id)
    return !cfg || cfg.show !== false
  })
  if (shown.length === 0) return null

  const wrap = el('div', { class: 'cursor-pointer' })
  wrap.onclick = () => { location.hash = '#/sensors' }
  wrap.appendChild(sectionTitle('thermometer', '系统温度'))
  // 内部测温点卡按显示数量自适应：1→100%、2→各50%、3→各33%、≥4→两排各50%
  const cols = innerCardCols(shown.length)
  const grid = el('div', {
    class: 'grid gap-3',
    style: `grid-template-columns:repeat(${cols},minmax(0,1fr));`,
  })
  for (const t of shown) {
    const cfg = cfgMap.get(t.id)
    const devCfg = tempDeviceConfigOf(t)
    const name = (cfg?.alias ?? '').trim() || t.name
    const iconColor = devCfg?.color || 'rgb(var(--c-on-primary))'
    const icon = tempIconOf(devCfg?.icon ?? smartDefaultIcon(t))

    const card = panelCard([])
    const head = el('div', { class: 'flex items-center gap-2 min-w-0' })
    const ic = svgIcon(icon, 18)
    ic.style.color = iconColor
    const nm = el('span', { class: 'text-sm font-medium truncate' }, [name])
    nm.style.color = 'rgb(var(--c-ink-muted))'
    head.append(ic, nm)
    const val = el('div', { class: 'text-[22px] font-bold tabular-nums leading-none' }, [formatTemp(t.value)])
    val.style.color = tempAutoColor(t.value)
    card.append(head, val)

    // 临界进度条：value / crit（无临界时以 100℃ 为满量程）
    const ref = t.crit && t.crit > 0 ? t.crit : 100
    card.appendChild(progressBar((t.value / ref) * 100, tempAutoColor(t.value)))

    const foot = el('div', { class: 'flex justify-between items-center mt-1' })
    const lim = el('span', { class: 'text-xs' }, [t.crit && t.crit > 0 ? `临界 ${t.crit}℃` : ''])
    lim.style.color = 'rgb(var(--c-ink-subtle))'
    const pct = el('span', { class: 'text-xs tabular-nums' }, [`${Math.round(Math.min(100, (t.value / ref) * 100))}%`])
    pct.style.color = 'rgb(var(--c-ink-subtle))'
    foot.append(lim, pct)
    card.appendChild(foot)
    grid.appendChild(card)
  }
  wrap.appendChild(grid)
  return wrap
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
// 风扇状态 — 合并双色速度条（绿=实际，蓝=目标）
// ================================================================
function activeFanConfigs(): FanChannelConfig[] {
  return store.settings.fans.filter(cfg => {
    if (!cfg.enabled) return false
    const hw = store.fans.find(f => f.index === cfg.id)
    return !hw || hw.enabled
  })
}

function renderFanStatus(): HTMLElement | null {
  const activeFans = activeFanConfigs()
  if (activeFans.length === 0) return null

  const wrap = el('div', { class: '' })
  wrap.appendChild(sectionTitle('fan', '风扇状态'))
  // 内部风扇卡按启用数量自适应：1→占满模块、2→各50%
  const cols = innerCardCols(activeFans.length)
  const grid = el('div', {
    class: 'grid gap-3',
    style: `grid-template-columns:repeat(${cols},minmax(0,1fr));`,
  })
  for (const cfg of activeFans) grid.appendChild(renderFanStatusCell(cfg))
  wrap.appendChild(grid)
  return wrap
}

function renderFanStatusCell(cfg: FanChannelConfig): HTMLElement {
  const hw: FanHWState | null = store.fans.find(f => f.index === cfg.id) ?? null
  const cur = hw?.curSpd ?? 0
  const target = hw?.targetSpd ?? cfg.targetSpd ?? 0
  const auto = cfg.mode === 'auto'

  const card = panelCard([])

  // 头部：带动画的风扇图标 + 别名 + 徽标
  const iconWrap = el('div', { class: 'w-8 h-8 rounded-lg flex items-center justify-center shrink-0' })
  iconWrap.style.background = 'rgb(var(--c-element))'
  iconWrap.style.color = cur > 0 ? 'rgb(var(--c-primary))' : 'rgb(var(--c-ink-subtle))'
  const fanIcon = svgIcon('fan', 16)
  if (cur > 0) fanIcon.classList.add(cur >= 50 ? 'fan-spinning-fast' : 'fan-spinning')
  iconWrap.appendChild(fanIcon)

  const badges: HTMLElement[] = []
  if (store.isDebugMode()) badges.push(badge(`FAN${cfg.id}`, '--c-neutral', true))
  badges.push(auto ? statusBadge('自动', 'info', { dot: true }) : statusBadge('手动', 'warning', { dot: true }))
  card.appendChild(cardHeader({
    iconEl: iconWrap,
    title: cfg.alias || `风扇 ${cfg.id}`,
    badges,
  }))

  // 合并双色速度条（实际条带动画，从旧值平滑过渡）
  card.appendChild(dualBarReadonly(cur, target, `status-fan${cfg.id}`))

  // 副行：参考温度 · PWM · 目标
  const ref = fanTempRef(cfg)
  const meta = el('div', { class: 'flex items-center justify-between gap-2 mt-2 text-xs flex-wrap' })
  const refSpan = el('span', { class: 'inline-flex items-center gap-1.5 min-w-0' })
  const th = svgIcon('thermometer', 12)
  th.style.color = 'rgb(var(--c-ink-subtle))'
  const refTxt = el('span', { class: 'truncate' }, [
    ref ? `${ref.name} ${formatTemp(ref.value)}` : '参考 未设置',
  ])
  refTxt.style.color = ref ? tempAutoColor(ref.value) : 'rgb(var(--c-ink-subtle))'
  refSpan.append(th, refTxt)

  const metaRight = el('span', { class: 'tabular-nums shrink-0' }, [
    `PWM ${hw?.pwmFreq ?? '—'}kHz · 目标 ${Math.round(target)}%`,
  ])
  metaRight.style.color = 'rgb(var(--c-ink-subtle))'
  meta.append(refSpan, metaRight)
  card.appendChild(meta)
  return card
}

interface TempRefInfo { name: string; value: number }

/** 解析风扇温度参考点（tempRefs 优先，兼容旧 tempRef；支持 thermal 与 i2c 通道） */
function fanTempRef(cfg: FanChannelConfig): TempRefInfo | null {
  const ids = cfg.tempRefs?.length ? cfg.tempRefs : cfg.tempRef ? [cfg.tempRef] : []
  for (const id of ids) {
    if (id.startsWith('i2c_ch_')) {
      const idx = Number(id.slice('i2c_ch_'.length))
      const rd = store.sensorReadings[idx]
      const chans = store.sensorChannelConfigs.length ? store.sensorChannelConfigs : store.settings.sensorChannels
      const chCfg = chans[idx]
      if (rd?.state === 'OK' && Number.isFinite(rd.temperature) && rd.temperature > 0) {
        return {
          name: chCfg?.alias ? `${chCfg.alias} 温度` : `CH${idx} 温度`,
          value: rd.temperature,
        }
      }
      continue
    }
    const t = store.thermal?.temps.find(x => x.id === id)
    if (t) {
      const sc = store.settings.tempSensors.find(s => s.id === id)
      return { name: sc?.alias || t.name, value: t.value }
    }
  }
  return null
}

// ================================================================
// 硬盘组状态 — 电源开关 + 在线 + 每盘挂载 + 温度
// ================================================================
function renderDiskGroupsDetail(): HTMLElement | null {
  const groups = store.diskGroups.filter(g => g.enabled)
  if (groups.length === 0) return null

  const wrap = el('div', { class: '' })
  wrap.appendChild(sectionTitle('hdd', '硬盘组状态'))
  const grid = el('div', { class: 'grid grid-cols-1 sm:grid-cols-2 gap-3' })
  for (const g of groups) grid.appendChild(renderDiskGroupStatusCell(g))
  wrap.appendChild(grid)
  return wrap
}

function renderDiskGroupStatusCell(g: DiskGroupView): HTMLElement {
  const sw = store.switches.find(s => s.index === g.switchN)
  const powerOn = sw?.state === 1

  const card = panelCard([])
  const badges: HTMLElement[] = [
    powerOn
      ? statusBadge('电源开', 'success', { dot: true })
      : statusBadge('电源关', 'neutral', { dot: true }),
    g.online
      ? statusBadge('在线', 'success', { dot: true })
      : statusBadge('离线', 'neutral'),
  ]
  card.appendChild(cardHeader({
    icon: 'hdd',
    iconColor: powerOn ? 'rgb(var(--c-success))' : 'rgb(var(--c-ink-subtle))',
    title: g.alias || `硬盘组 ${g.id}`,
    badges,
  }))

  if (!g.online) {
    const status = el('div', { class: 'text-xs' }, ['已启用 · 未上线'])
    status.style.color = 'rgb(var(--c-ink-subtle))'
    card.appendChild(status)
    return card
  }

  const disks = g.disks ?? []
  if (disks.length === 0) {
    const status = el('div', { class: 'text-xs' }, ['在线（未配置磁盘）'])
    status.style.color = 'rgb(var(--c-warning))'
    card.appendChild(status)
    return card
  }

  const list = el('div', { class: 'flex flex-col gap-1' })
  for (const d of disks) {
    const row = el('div', { class: 'flex items-center gap-2 text-xs min-w-0' })
    const dot = el('span', { class: 'w-1.5 h-1.5 rounded-full shrink-0' })
    dot.style.background = d.mounted ? 'rgb(var(--c-success))' : 'rgb(var(--c-warning))'
    const name = el('span', { class: 'truncate' }, [d.alias || d.device])
    name.style.color = 'rgb(var(--c-ink-muted))'
    const mount = el('span', { class: 'shrink-0 text-[11px]' }, [d.mounted ? '已挂载' : '未挂载'])
    mount.style.color = d.mounted ? 'rgb(var(--c-success-soft-text))' : 'rgb(var(--c-warning-soft-text))'
    row.append(dot, name, mount)
    if (d.temperature != null) {
      const tmp = el('span', {
        class: 'ml-auto shrink-0 text-[11px] font-medium tabular-nums rounded px-1.5 py-0.5',
      }, [formatTemp(d.temperature)])
      tmp.style.color = tempAutoColor(d.temperature)
      tmp.style.background = 'rgb(var(--c-element))'
      row.appendChild(tmp)
    }
    list.appendChild(row)
  }
  card.appendChild(list)
  return card
}

// ================================================================
// 环境传感器（I2C）— 按主页显示开关展示
// ================================================================
function renderI2cSection(): HTMLElement | null {
  const chans = store.sensorChannelConfigs.length ? store.sensorChannelConfigs : store.settings.sensorChannels
  const readings = store.sensorReadings

  interface Item { label: string; value: string; color: string; icon: IconName }
  const rows: Array<{ cfg: SensorChannelConfig; items: Item[] }> = []

  for (let i = 0; i < chans.length; i++) {
    const cfg = chans[i]
    const rd = readings[i]
    if (!cfg.enabled) continue
    if (!cfg.showTemp && !cfg.showHumi && !cfg.showPress && !cfg.showAlt) continue
    if (!rd || rd.state !== 'OK') continue
    const items: Item[] = []
    if (cfg.showTemp && Number.isFinite(rd.temperature) && rd.temperature !== 0) {
      items.push({ label: '温度', value: `${rd.temperature.toFixed(1)} ℃`, color: 'rgb(var(--c-warning))', icon: 'thermometer' })
    }
    if (cfg.showHumi && Number.isFinite(rd.humidity) && rd.humidity > 0) {
      items.push({ label: '湿度', value: `${rd.humidity.toFixed(1)} %`, color: 'rgb(var(--c-primary))', icon: 'droplet' })
    }
    if (cfg.showPress && Number.isFinite(rd.pressure) && rd.pressure > 0) {
      items.push({ label: '压力', value: `${(rd.pressure / 1000).toFixed(2)} kPa`, color: 'rgb(var(--c-success))', icon: 'gauge' })
    }
    if (cfg.showAlt && Number.isFinite(rd.altitude) && rd.altitude > 0) {
      items.push({ label: '海拔', value: `${rd.altitude.toFixed(1)} m`, color: 'rgb(var(--c-ink-muted))', icon: 'mountain' })
    }
    if (items.length > 0) rows.push({ cfg, items })
  }
  if (rows.length === 0) return null

  const wrap = el('div', { class: '' })
  wrap.appendChild(sectionTitle('thermometer', '环境传感器'))
  // 内部传感器卡按启用数量自适应（与系统温度/风扇状态一致）：
  // 1 个 → 整行 100%；2 个 → 各 50%；3 个 → 各 33%；4 个 → 各 50% 分两排
  const cols = innerCardCols(rows.length)
  const grid = el('div', {
    class: 'grid gap-3',
    style: `grid-template-columns:repeat(${cols},minmax(0,1fr));`,
  })
  for (const r of rows) {
    const card = panelCard([])
    const title = (r.cfg.alias ?? '').trim() || (store.isDebugMode() ? sensorKindLabel(r.cfg.kind) : '环境传感器')
    const badges: HTMLElement[] = []
    if (store.isDebugMode()) badges.push(badge(sensorKindLabel(r.cfg.kind), '--c-primary', true))
    card.appendChild(cardHeader({ icon: 'droplet', title, badges }))
    const vals = el('div', { class: 'grid grid-cols-2 gap-2' })
    for (const it of r.items) {
      vals.appendChild(metricCell({
        icon: it.icon, label: it.label, value: it.value, valueColor: it.color,
      }))
    }
    card.appendChild(vals)
    grid.appendChild(card)
  }
  wrap.appendChild(grid)
  return wrap
}

// ================================================================
// 历史趋势 — Tab 切换单图（温度 / 转速 / 湿度）
// ================================================================
function hasI2cHumidity(): boolean {
  return store.sensorReadings.some(r => r && r.state === 'OK' && r.humidity > 0)
}

function renderTrendSection(): HTMLElement {
  const wrap = el('div', { class: '' })
  wrap.appendChild(sectionTitle('chart', '历史趋势'))

  const tabs = segmentedTab<HistoryTab>([
    { key: 'temp', label: '温度' },
    { key: 'speed', label: '转速' },
    { key: 'humi', label: '湿度', hidden: !hasI2cHumidity() },
  ], chart.activeTab, (k) => {
    chart.activeTab = k
    void loadChartData()
    requestRender()
  })
  wrap.appendChild(tabs)

  const body = el('div', { class: 'mt-3' })
  if (chart.activeTab === 'temp') body.appendChild(renderTempChartCard())
  else if (chart.activeTab === 'speed') body.appendChild(renderSpeedChartCard())
  else body.appendChild(renderHumidityChartCard())
  wrap.appendChild(body)
  return wrap
}

function tempSeriesDefs(): Array<{ id: string; name: string }> {
  const temps = store.thermal?.temps ?? []
  const cfgMap = new Map(store.settings.tempSensors.map(s => [s.id, s]))
  const out: Array<{ id: string; name: string }> = []
  temps.forEach((t, i) => {
    const cfg = cfgMap.get(t.id)
    if (cfg && cfg.show === false) return
    out.push({ id: t.id, name: cfg?.alias || t.name || `传感器${i + 1}` })
  })
  const chans = store.sensorChannelConfigs.length ? store.sensorChannelConfigs : store.settings.sensorChannels
  store.sensorReadings.forEach((r, i) => {
    if (r && r.state === 'OK' && r.temperature > 0) {
      const chCfg = chans[i]
      const name = chCfg?.alias ? `${chCfg.alias} 温度` : `CH${i} 温度`
      out.push({ id: `i2c:${i}:temp`, name })
    }
  })
  return out
}

function renderTempChartCard(): HTMLElement {
  const t = trend.temp
  const view = trendView(t)
  const range = computeYRange(windowData(t.data, view), 5)
  return renderChartCard({
    title: '温度历史趋势',
    icon: 'thermometer',
    range: t.range,
    onRangeChange: (r) => { t.range = r; void loadChartData() },
    customStart: t.customStart,
    customEnd: t.customEnd,
    onCustomChange: (s, e) => {
      t.customStart = s
      t.customEnd = e
      if (s && e) void loadChartData()
    },
    seriesDefs: tempSeriesDefs(),
    data: t.data,
    hidden: t.hidden,
    yUnit: '℃',
    yMin: range.yMin,
    yMax: range.yMax,
    exportName: 'temperature_history.csv',
    viewTMin: view.viewTMin,
    viewTMax: view.viewTMax,
    maxViewT: view.maxViewT,
    onViewChange: (s, e) => handleChartViewChange('temp', s, e),
    buildSeries: () => buildSeriesFor('temp'),
    canvasRef: (c) => { t.canvas = c },
  })
}

function renderSpeedChartCard(): HTMLElement {
  const t = trend.speed
  const view = trendView(t)
  const defs: Array<{ id: string; name: string }> = store.settings.fans
    .filter(f => f.enabled)
    .map(f => ({ id: `fan:${f.id}:spd`, name: `${f.alias || `风扇${f.id}`} 实际转速` }))
  const range = computeYRange(windowData(t.data, view), 10, 0, 100)
  return renderChartCard({
    title: '风扇转速历史趋势',
    icon: 'chart',
    range: t.range,
    onRangeChange: (r) => { t.range = r; void loadChartData() },
    customStart: t.customStart,
    customEnd: t.customEnd,
    onCustomChange: (s, e) => {
      t.customStart = s
      t.customEnd = e
      if (s && e) void loadChartData()
    },
    seriesDefs: defs,
    data: t.data,
    hidden: t.hidden,
    yUnit: '%',
    yMin: range.yMin,
    yMax: range.yMax,
    exportName: 'fan_speed_history.csv',
    viewTMin: view.viewTMin,
    viewTMax: view.viewTMax,
    maxViewT: view.maxViewT,
    onViewChange: (s, e) => handleChartViewChange('speed', s, e),
    buildSeries: () => buildSeriesFor('speed'),
    canvasRef: (c) => { t.canvas = c },
  })
}

function humiSeriesDefs(): Array<{ id: string; name: string }> {
  const out: Array<{ id: string; name: string }> = []
  const chans = store.sensorChannelConfigs.length ? store.sensorChannelConfigs : store.settings.sensorChannels
  store.sensorReadings.forEach((r, i) => {
    if (r && r.state === 'OK' && r.humidity > 0) {
      const chCfg = chans[i]
      const name = chCfg?.alias ? `${chCfg.alias} 湿度` : `CH${i} 湿度`
      out.push({ id: `i2c:${i}:humi`, name })
    }
  })
  return out
}

function renderHumidityChartCard(): HTMLElement {
  const t = trend.humi
  const view = trendView(t)
  const range = computeYRange(windowData(t.data, view), 10, 0, 100)
  return renderChartCard({
    title: '湿度历史趋势',
    icon: 'droplet',
    range: t.range,
    onRangeChange: (r) => { t.range = r; void loadChartData() },
    customStart: t.customStart,
    customEnd: t.customEnd,
    onCustomChange: (s, e) => {
      t.customStart = s
      t.customEnd = e
      if (s && e) void loadChartData()
    },
    seriesDefs: humiSeriesDefs(),
    data: t.data,
    hidden: t.hidden,
    yUnit: '%',
    yMin: range.yMin,
    yMax: range.yMax,
    exportName: 'humidity_history.csv',
    viewTMin: view.viewTMin,
    viewTMax: view.viewTMax,
    maxViewT: view.maxViewT,
    onViewChange: (s, e) => handleChartViewChange('humi', s, e),
    buildSeries: () => buildSeriesFor('humi'),
    canvasRef: (c) => { t.canvas = c },
  })
}

function computeYRange(
  data: Record<string, HistoryPoint[]>,
  padding: number,
  minClamp?: number,
  maxClamp?: number,
): { yMin?: number; yMax?: number } {
  let vMin = Infinity
  let vMax = -Infinity
  let hasData = false
  for (const key of Object.keys(data)) {
    for (const p of data[key]) {
      if (!Number.isFinite(p.v)) continue
      if (p.v < vMin) vMin = p.v
      if (p.v > vMax) vMax = p.v
      hasData = true
    }
  }
  if (!hasData) return {}
  vMin -= padding
  vMax += padding
  if (minClamp !== undefined) vMin = Math.max(minClamp, vMin)
  if (maxClamp !== undefined) vMax = Math.min(maxClamp, vMax)
  if (vMin === vMax) { vMin -= 1; vMax += 1 }
  return { yMin: Math.round(vMin), yMax: Math.round(vMax) }
}

interface ChartCardOpts {
  title: string
  icon: 'thermometer' | 'chart' | 'droplet'
  range: HistoryRange
  onRangeChange: (r: HistoryRange) => void
  customStart: string
  customEnd: string
  onCustomChange: (start: string, end: string) => void
  seriesDefs: Array<{ id: string; name: string }>
  data: Record<string, HistoryPoint[]> // 全量滑动缓存（canvas 内按显示窗口裁剪）
  hidden: Set<string>
  yUnit: string
  yMin?: number
  yMax?: number
  exportName: string
  viewTMin?: number      // 固定显示窗口起点（拖动平移）
  viewTMax?: number      // 固定显示窗口终点
  maxViewT?: number      // 拖动右端上限（当前时间）
  onViewChange?: (s: number, e: number) => void
  buildSeries: () => LineSeries[]
  canvasRef?: (c: HTMLCanvasElement) => void
}

function renderChartCard(o: ChartCardOpts): HTMLElement {
  const card = el('div', { class: 'card p-4' })
  const head = el('div', { class: 'flex flex-wrap items-center gap-2 mb-3' })
  const ic = svgIcon(o.icon, 16)
  ic.style.color = 'rgb(var(--c-primary))'
  const title = el('span', { class: 'text-sm font-semibold flex-1' }, [o.title])
  title.style.color = 'rgb(var(--c-ink))'

  const sel = el('select', { class: 'input', style: 'width:auto;padding:4px 8px;font-size:12px' }) as HTMLSelectElement
  for (const opt of HISTORY_RANGE_OPTIONS) {
    const op = el('option', { value: opt.id }, [opt.label])
    if (opt.id === o.range) (op as HTMLOptionElement).selected = true
    sel.appendChild(op)
  }
  sel.onchange = () => o.onRangeChange(sel.value as HistoryRange)

  const exportBtn = el('button', { class: 'btn btn-sm' }, [svgIcon('save', 13), ' 导出 CSV'])
  exportBtn.onclick = () => {
    // 导出当前显示窗口内的点（固定窗口模式下）
    const sMin = o.viewTMin !== undefined ? o.viewTMin : -Infinity
    const sMax = o.viewTMax !== undefined ? o.viewTMax : Infinity
    const rows: Array<Array<string | number>> = [['series', 'time', 'value']]
    for (const def of o.seriesDefs) {
      const pts = (o.data[def.id] ?? []).filter(p => p.t >= sMin && p.t <= sMax)
      for (const p of pts) rows.push([def.name, new Date(p.t).toISOString(), p.v])
    }
    if (rows.length <= 1) { toast('暂无可导出的数据', 'error'); return }
    downloadCSV(o.exportName, rows)
  }

  head.append(ic, title, sel)
  if (o.range === 'custom') {
    const start = el('input', {
      type: 'datetime-local', class: 'input', style: 'width:auto;padding:4px 8px;font-size:12px', value: o.customStart,
    }) as HTMLInputElement
    const end = el('input', {
      type: 'datetime-local', class: 'input', style: 'width:auto;padding:4px 8px;font-size:12px', value: o.customEnd,
    }) as HTMLInputElement
    start.onchange = () => o.onCustomChange(start.value, end.value)
    end.onchange = () => o.onCustomChange(start.value, end.value)
    head.append(start, end)
  }
  head.appendChild(exportBtn)
  card.appendChild(head)

  const canvas = el('canvas', { class: 'w-full block' }) as HTMLCanvasElement
  o.canvasRef?.(canvas)
  card.appendChild(canvas)
  // Fix #8：先释放上一张图的监听器，再保存新 cleanup 供重置时调用
  if (chartCleanup) { chartCleanup(); chartCleanup = null }
  chartCleanup = drawLineChart(canvas, o.buildSeries(), {
    yLabel: o.yUnit,
    yMin: o.yMin,
    yMax: o.yMax,
    height: 220,
    showLegend: false,
    smooth: true,
    hoverTooltip: true,
    gapThresholdMs: GAP_THRESHOLD_MS,
    viewTMin: o.viewTMin,
    viewTMax: o.viewTMax,
    maxViewT: o.maxViewT,
    onViewChange: o.onViewChange,
  })

  if (o.seriesDefs.length > 0) {
    const legend = el('div', { class: 'flex flex-wrap gap-1.5 mt-2' })
    o.seriesDefs.forEach((def, i) => {
      const colorVar = SERIES_COLORS[i % SERIES_COLORS.length]
      const chip = el('div', { class: 'legend-chip' + (o.hidden.has(def.id) ? ' chip-hidden' : '') })
      const dot = el('span', { class: 'chip-dot' })
      dot.style.background = `rgb(var(${colorVar}))`
      chip.append(dot, def.name)
      chip.onclick = () => {
        if (o.hidden.has(def.id)) {
          o.hidden.delete(def.id)
          chip.classList.remove('chip-hidden')
        } else {
          o.hidden.add(def.id)
          chip.classList.add('chip-hidden')
        }
        updateLineChart(canvas, o.buildSeries())
      }
      legend.appendChild(chip)
    })
    card.appendChild(legend)
  }

  return card
}

function chartSignature(): string {
  const temps = tempSeriesDefs().map(d => d.id).join(',')
  const fans = store.settings.fans.filter(f => f.enabled).map(f => f.id).join(',')
  const humi = humiSeriesDefs().map(d => d.id).join(',')
  return [
    chart.activeTab, temps, fans, humi,
    trend.temp.range, trend.temp.customStart, trend.temp.customEnd,
    trend.speed.range, trend.speed.customStart, trend.speed.customEnd,
    trend.humi.range, trend.humi.customStart, trend.humi.customEnd,
  ].join('|')
}

// ===== 历史趋势：固定窗口 + 滑动缓存（拖动平移，越界节流补拉） =====

/** 固定窗口范围计算；custom 模式无窗口（保持原行为） */
function trendView(t: TrendState): { viewTMin?: number; viewTMax?: number; maxViewT?: number } {
  if (t.range === 'custom' || t.windowMs <= 0) return {}
  return {
    viewTMin: t.refNow - t.windowMs - t.offsetMs,
    viewTMax: t.refNow - t.offsetMs,
    maxViewT: t.refNow, // 不允许拖到参照时间之后
  }
}

/** 按显示窗口过滤数据（Y 轴范围计算用；custom 模式返回全量） */
function windowData(data: Record<string, HistoryPoint[]>, view: { viewTMin?: number; viewTMax?: number }): Record<string, HistoryPoint[]> {
  if (view.viewTMin === undefined || view.viewTMax === undefined) return data
  const out: Record<string, HistoryPoint[]> = {}
  for (const k in data) out[k] = data[k].filter(p => p.t >= view.viewTMin! && p.t <= view.viewTMax!)
  return out
}

/** 每个趋势页签的系列定义 */
function seriesDefsFor(tab: HistoryTab): Array<{ id: string; name: string }> {
  if (tab === 'temp') return tempSeriesDefs()
  if (tab === 'speed') return store.settings.fans
    .filter(f => f.enabled)
    .map(f => ({ id: `fan:${f.id}:spd`, name: `${f.alias || `风扇${f.id}`} 实际转速` }))
  return humiSeriesDefs()
}

/** 系列 id → 后端 series 名 */
function seriesNameFor(def: { id: string }, tab: HistoryTab): string {
  if (def.id.startsWith('i2c:')) return def.id
  if (tab === 'speed') return def.id // fan:x:spd 已是完整名
  return `temp:${def.id}`
}

/** 构造传给 drawLineChart 的 series（全量缓存；图例隐藏的过滤掉） */
function buildSeriesFor(tab: HistoryTab): LineSeries[] {
  const t = trend[tab]
  return seriesDefsFor(tab)
    .map((def, i) => ({ def, i }))
    .filter(({ def }) => !t.hidden.has(def.id))
    .map(({ def, i }) => ({
      name: def.name,
      color: cssVarColor(SERIES_COLORS[i % SERIES_COLORS.length]),
      points: t.data[def.id] ?? [],
    }))
}

/** 合并点集（按时间去重、升序；相同时间保留新值） */
function mergePoints(existing: HistoryPoint[] | undefined, incoming: HistoryPoint[]): HistoryPoint[] {
  const m = new Map<number, number>()
  for (const p of existing ?? []) m.set(p.t, p.v)
  for (const p of incoming) m.set(p.t, p.v)
  return [...m.entries()].sort((a, b) => a[0] - b[0]).map(([t, v]) => ({ t, v }))
}

/** 拉取一段历史并入缓存（replace=false 时向左合并扩展缓存范围） */
async function fetchCacheRange(tab: HistoryTab, startMs: number, endMs: number, replace = false): Promise<void> {
  const t = trend[tab]
  const defs = seriesDefsFor(tab)
  const tasks = defs.map(def => (async () => {
    try {
      const r = await store.history({ series: seriesNameFor(def, tab), startMs, endMs })
      t.data[def.id] = replace ? (r.points ?? []) : mergePoints(t.data[def.id], r.points ?? [])
    } catch {
      if (replace) t.data[def.id] = []
    }
  })())
  await Promise.allSettled(tasks)
  if (replace) {
    t.cacheStart = startMs
    t.cacheEnd = endMs
  } else {
    t.cacheStart = Math.min(t.cacheStart || startMs, startMs)
    t.cacheEnd = Math.max(t.cacheEnd, endMs)
  }
}

/** 拖动平移回调：同步窗口偏移；窗口越过缓存起点时向左节流补拉 */
function handleChartViewChange(tab: HistoryTab, s: number, e: number): void {
  const t = trend[tab]
  if (t.range === 'custom' || t.windowMs <= 0) return
  t.offsetMs = Math.max(0, t.refNow - e)
  if (s < t.cacheStart) {
    const now = Date.now()
    if (now - t.lastPan >= 250) {
      t.lastPan = now
      const fetchStart = Math.max(0, s - t.windowMs) // 向左多拉一个窗口作缓冲
      void fetchCacheRange(tab, fetchStart, t.cacheStart).then(() => {
        // 原地更新曲线（canvas 内部窗口已是拖动后的值，无需重渲染）
        if (t.canvas) updateLineChart(t.canvas, buildSeriesFor(tab))
      })
    }
  }
}

async function loadChartData(): Promise<void> {
  chart.fetching = true
  chart.lastSig = chartSignature()
  try {
    const tab = chart.activeTab
    const t = trend[tab]
    t.refNow = Date.now()
    if (t.range === 'custom') {
      // 自定义区间：保持原行为（无窗口/无拖动），直接按起止时间拉取
      t.offsetMs = 0
      t.windowMs = 0
      t.cacheStart = 0
      t.cacheEnd = 0
      const defs = seriesDefsFor(tab)
      const q = {
        customStart: t.customStart || undefined,
        customEnd: t.customEnd || undefined,
      }
      const tasks = defs.map(def => (async () => {
        try {
          const r = await store.history({ series: seriesNameFor(def, tab), ...q })
          t.data[def.id] = r.points ?? []
        } catch { t.data[def.id] = [] }
      })())
      await Promise.allSettled(tasks)
    } else {
      // 固定窗口：重置到初始（最右 = 当前时间），滑动缓存加载 2 倍窗口
      t.windowMs = HISTORY_RANGE_OPTIONS.find(o => o.id === t.range)?.windowMs ?? 3_600_000
      t.offsetMs = 0
      const end = Date.now()
      const start = end - t.windowMs * 2
      await fetchCacheRange(tab, start, end, true)
    }
    chart.lastFetch = Date.now()
    // 触发 rerender 让 chart 重绘（canvas 要重 drawLineChart）
    requestAnimationFrame(() => { document.dispatchEvent(new Event('rerender')) })
  } finally {
    chart.fetching = false
  }
}

// ================================================================
// 最近事件 — 日志摘要（点击进入运行日志页）
// ================================================================
let logsLoaded = false

/** 首次渲染时拉取一次日志（去重；resetHomeState 后允许重新拉取） */
function ensureLogs(): void {
  if (logsLoaded) return
  logsLoaded = true
  void store.loadLogs(100)
}

function renderRecentEvents(): HTMLElement | null {
  if (!store.logs || store.logs.length === 0) return null
  const sorted: LogEntry[] = [...store.logs]
    .sort((a, b) => new Date(b.time).getTime() - new Date(a.time).getTime())
    .slice(0, 5)

  const card = panelCard([])
  const more = el('a', { class: 'text-xs shrink-0', href: '#/logs' }, ['查看全部 →'])
  more.style.color = 'rgb(var(--c-primary-soft-text))'
  card.appendChild(cardHeader({
    icon: 'log',
    title: '最近事件',
    badges: [badge(`${store.logs.length} 条`, '--c-neutral', true)],
    actions: [more],
  }))

  for (const e of sorted) {
    const row = el('div', { class: 'event-row' })
    row.onclick = () => { location.hash = '#/logs' }
    const lvl = el('span', { class: 'event-level' })
    lvl.style.background = logLevelColor(e.level)
    const time = el('span', { class: 'event-time' }, [formatTime(e.time)])
    const mod = el('span', { class: 'event-module' }, [e.module])
    const msg = el('span', { class: 'event-msg' }, [e.message])
    if (e.level === 'error') msg.style.color = 'rgb(var(--c-danger-soft-text))'
    else if (e.level === 'warn') msg.style.color = 'rgb(var(--c-warning-soft-text))'
    row.append(lvl, time, mod, msg)
    card.appendChild(row)
  }

  const wrap = el('div', { class: '' })
  wrap.appendChild(sectionTitle('log', '运行日志'))
  wrap.appendChild(card)
  return wrap
}

function logLevelColor(level: string): string {
  if (level === 'error') return 'rgb(var(--c-danger))'
  if (level === 'warn') return 'rgb(var(--c-warning))'
  if (level === 'info') return 'rgb(var(--c-primary))'
  return 'rgb(var(--c-ink-subtle))'
}

// ================================================================
// 工具
// ================================================================
function mean(vals: number[]): number {
  if (vals.length === 0) return 0
  return vals.reduce((a, b) => a + b, 0) / vals.length
}

// 供 main.ts 在路由离开时清理
export function resetHomeState(): void {
  // 布局编辑状态重置（离开页面后回到只读布局）
  layoutEditing = false
  layoutDraft = []
  // 停止监控总览风扇实际转速轮询
  stopHomeFanRefresh()
  // Fix #8：释放图表监听器
  if (chartCleanup) { chartCleanup(); chartCleanup = null }
  logsLoaded = false
  chart.activeTab = 'temp'
  for (const k of ['temp', 'speed', 'humi'] as HistoryTab[]) {
    trend[k] = {
      range: '1h', customStart: '', customEnd: '', data: {},
      hidden: new Set(), windowMs: 3_600_000, refNow: 0, offsetMs: 0,
      cacheStart: 0, cacheEnd: 0, lastPan: 0, canvas: null,
    }
  }
  chart.lastFetch = 0
  chart.fetching = false
  chart.lastSig = ''
}
