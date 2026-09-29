// MLNR 麻辣牛肉控制器前端主入口
// 重构后：哈希路由 + 全局顶栏（连接状态）+ 侧边导航 + 内容区
//
// 路由表：
//   #/            监控总览（只读聚合）
//   #/fans        风扇控制
//   #/disks       硬盘组管理
//   #/sensors     传感器（系统温度 / I2C 环境）
//   #/trends      历史趋势
//   #/settings    系统设置（传感器配置 / 通道配置 / 主题 / 关于）
//   #/fan/:id     风扇通道详情（本地配置 + 硬件 CFG）
//   #/disk/:id    硬盘组详情（本地配置 + 硬件 CFG）
//   #/connection  蓝牙连接设置
//   #/connection/config  连接设置（自动连接 / 广播名 / Debug 模式，需求 #1）
//   #/logs        运行日志
//   #/sensor-config/temp  系统温度传感器配置
//   #/sensor-config/i2c   I2C 传感器通道配置

import './theme'
import './style.css'

import { store } from './store'
import { el, svgIcon, sideNav, NavGroup, toast } from './ui'
import { BOTTOM_NAV_DEFAULT_KEYS, resolveBottomNavItems } from './bottom-nav-config'
import type { BottomNavItemDef } from './bottom-nav-config'
import { bottomNavBar, closeBottomNavModalIfOpen } from './views/bottom-nav-settings'
import { setRenderRequester, isInteracting } from './views/shared'
import { renderHome, resetHomeState } from './views/home'
import { renderFans, resetFansState } from './views/fans'
import { renderDisks, resetDisksState } from './views/disks'
import { renderSensors, resetSensorsState } from './views/sensors'
import { renderSettings, resetSettingsState } from './views/settings'
import { renderFanSettings, resetFanSettingsState } from './views/fan-settings'
import { renderDiskSettings, resetDiskSettingsState } from './views/disk-settings'
import { renderConnection, closeScanOverlay } from './views/connection'
import { renderConnectionConfig, resetConnectionConfigState } from './views/connection-config'
import { renderLogs, resetLogsState, isLogAutoFollow } from './views/logs'
import { renderTempSensorConfigPage, renderI2cSensorConfigPage, resetSensorConfigState } from './views/sensor-config'
import { renderSchedules, resetSchedulesState } from './views/schedules'
import { renderScheduleForm, resetScheduleFormState } from './views/schedules'
import { renderScheduleLogs, resetScheduleLogsState } from './views/schedule-logs'
import { initTheme, setTheme } from './theme'
import {
  STATE_LABEL, STATE_COLOR, signalQualityLabel, signalQualityColor,
} from './types'

// ===== 路由解析 =====
type RouteName = 'home' | 'fans' | 'disks' | 'sensors' | 'settings' | 'fan' | 'disk' | 'connection' | 'connection-config' | 'logs' | 'sensor-config' | 'schedules' | 'schedule'

interface Route {
  name: RouteName
  id?: number
  sub?: string // fan/disk 的来源标记（settings=从系统设置进入）；sensor-config 子页已拆分独立
}

function parseHash(): Route {
  const h = (location.hash || '#/').replace(/^#\/?/, '')
  const parts = h.split('/').filter(Boolean)
  if (parts.length === 0) return { name: 'home' }
  switch (parts[0]) {
    case 'fans': return { name: 'fans' }
    case 'disks': return { name: 'disks' }
    case 'sensors': return { name: 'sensors' }
    case 'trends': return { name: 'home' }  // 旧书签兼容：#/trends → 监控总览（历史趋势已迁入）
    case 'settings': return { name: 'settings' }
    case 'fan': {
      const id = Number(parts[1])
      if (!Number.isFinite(id) || id < 1 || id > 2) return { name: 'settings' }
      // 需求11：从系统设置进入的详情页带来源标记（#/fan/1/settings），导航高亮保持在「系统设置」
      return parts[2] === 'settings' ? { name: 'fan', id, sub: 'settings' } : { name: 'fan', id }
    }
    case 'disk': {
      const id = Number(parts[1])
      if (!Number.isFinite(id) || id < 1 || id > 4) return { name: 'settings' }
      return parts[2] === 'settings' ? { name: 'disk', id, sub: 'settings' } : { name: 'disk', id }
    }
    case 'connection': return { name: parts[1] === 'config' ? 'connection-config' : 'connection' }
    case 'logs': return { name: 'logs' }
    case 'schedules': return { name: 'schedules', sub: parts[1] === 'logs' ? 'logs' : undefined }
    case 'schedule': {
      if (parts[1] === 'new') return { name: 'schedule' }
      const sid = Number(parts[1])
      if (!Number.isFinite(sid) || sid < 1) return { name: 'schedules' }
      return { name: 'schedule', id: sid }
    }
    case 'sensor-config': return { name: 'sensor-config', sub: parts[1] || 'temp' }
    default: return { name: 'home' }
  }
}

// 路由切换时清理各视图的临时 UI 状态
function resetViewStates(from: Route, to: Route): void {
  if (from.name !== to.name || from.id !== to.id || from.sub !== to.sub) {
    resetHomeState()
    resetFansState()
    resetDisksState()
    resetSensorsState()
    resetSettingsState()
    resetFanSettingsState()
    resetDiskSettingsState()
    resetConnectionConfigState()
    resetLogsState()
    resetSchedulesState()
    resetScheduleFormState()
    resetScheduleLogsState()
    resetSensorConfigState()
    // Fix #3：路由切换时关闭可能残留的扫描弹窗（退订 + 停止扫描 + 移除 overlay）
    closeScanOverlay()
    // 底部导航栏设置弹窗：路由切换时一并关闭
    closeBottomNavModalIfOpen()
  }
}

// ===== 导航分组定义 =====
const NAV_GROUPS: NavGroup[] = [
  {
    title: '监控与控制',
    items: [
      { key: 'home', label: '监控总览', icon: 'chart', hash: '#/' },
      { key: 'fans', label: '风扇控制', icon: 'fan', hash: '#/fans' },
      { key: 'disks', label: '硬盘组管理', icon: 'hdd', hash: '#/disks' },
      { key: 'sensors', label: '传感器', icon: 'thermometer', hash: '#/sensors' },
    ],
  },
  {
    title: '配置与诊断',
    items: [
      { key: 'connection', label: '连接设置', icon: 'link', hash: '#/connection' },
      { key: 'settings', label: '系统设置', icon: 'settings', hash: '#/settings' },
      { key: 'schedules', label: '定时计划', icon: 'clock', hash: '#/schedules' },
      { key: 'logs', label: '运行日志', icon: 'log', hash: '#/logs' },
    ],
  },
]

// 路由名 → 导航高亮 key（详情页高亮对应父级；需求11：从系统设置进入的详情页高亮「系统设置」）
function navActiveKey(route: Route): string {
  if (route.name === 'fan') return route.sub === 'settings' ? 'settings' : 'fans'
  if (route.name === 'disk') return route.sub === 'settings' ? 'settings' : 'disks'
  if (route.name === 'sensor-config') return 'settings'
  if (route.name === 'schedule') return 'schedules'
  return route.name
}

// ===== 渲染管线 =====
let currentRoute: Route = parseHash()
let renderScheduled = false

function render(): void {
  if (renderScheduled) return
  renderScheduled = true
  requestAnimationFrame(() => {
    renderScheduled = false
    doRender()
  })
}

function forceRender(): void {
  requestAnimationFrame(doRender)
}

let lastRouteKey: string | null = null

function doRender(): void {
  const root = document.getElementById('app')
  if (!root) return
  if (isInteracting()) {
    setTimeout(render, 150)
    return
  }
  // Fix #13：渲染前保存滚动位置，渲染后恢复（避免整页重建丢失滚动位置）
  const scrollY = window.scrollY
  const route = parseHash()
  const routeKey = `${route.name}:${route.id ?? ''}:${route.sub ?? ''}`
  const wrap = el('div', { class: 'min-h-screen flex flex-col' })
  if (routeKey !== lastRouteKey) wrap.classList.add('animate-fade-in')
  lastRouteKey = routeKey

  wrap.appendChild(renderTopBar())
  wrap.appendChild(renderBody(route))
  root.replaceChildren(wrap)
  window.scrollTo(0, scrollY)
}

function renderBody(route: Route): HTMLElement {
  const body = el('div', { class: 'flex flex-1' })

  // 桌面侧边栏（lg+ 显示）
  const sidebar = el('aside', { class: 'hidden lg:block w-56 shrink-0 border-r' })
  sidebar.style.background = 'rgb(var(--c-surface))'
  sidebar.style.borderColor = 'rgb(var(--c-line))'
  const sidebarInner = el('div', { class: 'p-3 sticky top-14' })
  sidebarInner.appendChild(sideNav(NAV_GROUPS, navActiveKey(route)))
  sidebar.appendChild(sidebarInner)
  body.appendChild(sidebar)

  // 底部导航项：配置驱动（store.settings.bottomNavKeys 已规范化：null→默认 4 项，[]→隐藏）
  const bottomItems = resolveBottomNavItems(
    Array.isArray(store.settings.bottomNavKeys) ? store.settings.bottomNavKeys : BOTTOM_NAV_DEFAULT_KEYS,
  )

  // 内容区（有底部导航时在移动端预留底部空间）
  const content = el('main', { class: `flex-1 min-w-0 ${bottomItems.length > 0 ? 'pb-20' : ''} lg:pb-5` })
  content.appendChild(renderRouteContent(route))
  body.appendChild(content)

  // 移动端底部 Tab（<lg 显示；配置为空时隐藏）
  if (bottomItems.length > 0) body.appendChild(renderBottomNav(route, bottomItems))

  return body
}

function renderRouteContent(route: Route): HTMLElement {
  switch (route.name) {
    case 'fans': return renderFans()
    case 'disks': return renderDisks()
    case 'sensors': return renderSensors()
    case 'settings': return renderSettings()
    case 'fan': return renderFanSettings(route.id!)
    case 'disk': return renderDiskSettings(route.id!)
    case 'connection': return renderConnection()
    case 'connection-config': return renderConnectionConfig()
    case 'logs': return renderLogs()
    case 'schedules': return route.sub === 'logs' ? renderScheduleLogs() : renderSchedules()
    case 'schedule': return renderScheduleForm(route.id)
    case 'sensor-config': return route.sub === 'i2c' ? renderI2cSensorConfigPage() : renderTempSensorConfigPage()
    default: return renderHome()
  }
}

// ================================================================
// 全局顶栏：Logo + 标题 + 连接状态（简化后仅保留连接状态）
// ================================================================
function renderTopBar(): HTMLElement {
  const bar = el('header', { class: 'flex items-center justify-between gap-3 px-4 py-2.5 sticky top-0 z-40' })
  bar.style.background = 'rgb(var(--c-surface))'
  bar.style.borderBottom = '1px solid rgb(var(--c-line))'

  // ---- 左侧：Logo + 标题 ----
  const left = el('div', { class: 'flex items-center gap-2.5 min-w-0' })
  const logoWrap = el('button', {
    class: 'w-9 h-9 rounded-lg flex items-center justify-center shrink-0',
    title: '返回监控总览',
  })
  logoWrap.style.background = 'rgb(var(--c-element))'
  logoWrap.style.color = 'rgb(var(--c-primary))'
  logoWrap.appendChild(svgIcon('fan', 20))
  logoWrap.onclick = () => { location.hash = '#/' }

  const titleBox = el('div', { class: 'min-w-0 hidden sm:block' })
  const title = el('div', { class: 'font-semibold text-[15px] leading-tight truncate' }, ['MLNR 蓝牙控制器'])
  title.style.color = 'rgb(var(--c-ink))'
  const subtitle = el('div', { class: 'text-[11px] leading-tight truncate' },
    [store.appInfo ? `NR_F2S4 · v${store.appInfo.version}` : 'NR_F2S4 蓝牙控制板'])
  subtitle.style.color = 'rgb(var(--c-ink-subtle))'
  titleBox.append(title, subtitle)
  left.append(logoWrap, titleBox)

  // ---- 右侧：连接状态 + 信号强度（v3：仅顶栏保留） ----
  const right = el('div', { class: 'flex items-center gap-2' })

  const d = store.device
  const conn = d?.connectionState ?? 'disconnected'

  // 连接状态 pill（常驻，唯一状态显示；胶囊化）
  const connPill = el('div', { class: 'flex items-center gap-1.5 h-8 px-3 rounded-full text-xs font-medium' })
  connPill.style.background = 'rgb(var(--c-element))'
  connPill.style.border = '1px solid rgb(var(--c-line))'
  const dot = el('span', { class: 'w-2 h-2 rounded-full shrink-0' })
  dot.style.background = STATE_COLOR[conn] ?? STATE_COLOR.disconnected
  if (conn === 'connected') {
    // 呼吸光圈（随主题变量 --c-success）
    dot.animate(
      [{ boxShadow: '0 0 0 0 rgb(var(--c-success) / 0.45)' }, { boxShadow: '0 0 0 6px rgb(var(--c-success) / 0)' }],
      { duration: 1800, iterations: Infinity },
    )
  }
  const connText = el('span', {}, [STATE_LABEL[conn]])
  connText.style.color = 'rgb(var(--c-ink))'
  connPill.append(dot, connText)
  // 点击连接状态 pill → 跳转蓝牙连接设置页
  connPill.style.cursor = 'pointer'
  connPill.title = '点击进入蓝牙连接设置'
  connPill.onclick = () => { location.hash = '#/connection' }
  right.appendChild(connPill)

  // Fix #1：后端不可达时显示红色警告徽标
  if (!store.backendOnline) {
    const warnPill = el('div', { class: 'flex items-center gap-1.5 h-8 px-3 rounded-full text-xs font-semibold' })
    warnPill.style.background = 'rgb(var(--c-danger) / 0.12)'
    warnPill.style.border = '1px solid rgb(var(--c-danger) / 0.4)'
    warnPill.style.color = 'rgb(var(--c-danger))'
    const warnDot = el('span', { class: 'w-2 h-2 rounded-full shrink-0' })
    warnDot.style.background = 'rgb(var(--c-danger))'
    warnDot.animate(
      [{ opacity: 1 }, { opacity: 0.3 }],
      { duration: 900, iterations: Infinity },
    )
    warnPill.append(warnDot, el('span', {}, ['后端断开']))
    warnPill.title = '后端服务不可达，数据可能陈旧，请检查后端进程'
    right.appendChild(warnPill)
  }

  // 待落盘 NVS 提示：有 CFG 已写入硬件内存但未 SAVE（直接写入模式关闭时）
  if (store.pendingNvsSave && !store.settings.directSaveNVS) {
    const nvsPill = el('div', { class: 'flex items-center gap-1.5 h-8 pl-3 pr-1.5 rounded-full text-xs font-semibold' })
    nvsPill.style.background = 'rgb(var(--c-warning) / 0.12)'
    nvsPill.style.border = '1px solid rgb(var(--c-warning) / 0.4)'
    nvsPill.style.color = 'rgb(var(--c-warning))'
    const nvsDot = el('span', { class: 'w-2 h-2 rounded-full shrink-0' })
    nvsDot.style.background = 'rgb(var(--c-warning))'
    nvsDot.animate(
      [{ opacity: 1 }, { opacity: 0.3 }],
      { duration: 900, iterations: Infinity },
    )
    nvsPill.append(nvsDot, el('span', {}, ['硬件配置未落盘']))
    const nvsBtn = el('button', { class: 'btn btn-sm btn-primary shrink-0', style: 'height:24px;padding:0 10px;font-size:11px' }, ['落盘 NVS'])
    nvsBtn.title = '将硬件内存中的 CFG 配置通过 SAVE(0x0A) 写入 NVS，断电后保留'
    nvsBtn.onclick = async () => {
      nvsBtn.disabled = true
      const ok = await store.saveNvsNow()
      nvsBtn.disabled = false
      toast(ok ? '硬件配置已落盘 NVS' : '落盘 NVS 失败，详见日志', ok ? 'success' : 'error')
      document.dispatchEvent(new CustomEvent('rerender'))
    }
    nvsPill.appendChild(nvsBtn)
    right.appendChild(nvsPill)
  }

  // 信号质量 chip（已连接且固件 PING 已返回推算质量时显示；分级展示，不显示数值）
  if (conn === 'connected' && d && d.signalQuality !== undefined && d.signalQuality >= 0) {
    const qPill = el('div', { class: 'flex items-center gap-1.5 h-8 px-2.5 rounded-full text-xs tabular-nums' })
    qPill.style.background = 'rgb(var(--c-element))'
    qPill.style.border = '1px solid rgb(var(--c-line))'
    const sig = svgIcon('signal', 13)
    sig.style.color = 'rgb(var(--c-signal))'
    const qText = el('span', {}, [signalQualityLabel(d.signalQuality)])
    qText.style.color = signalQualityColor(d.signalQuality)
    qPill.append(sig, qText)
    qPill.title = `信号质量 ${signalQualityLabel(d.signalQuality)}`
    right.appendChild(qPill)
  }

  bar.append(left, right)
  return bar
}

// ================================================================
// 移动端底部 Tab 栏（配置驱动：可见性与顺序来自 系统设置 → 底部导航栏设置）
// ================================================================
function renderBottomNav(route: Route, items: BottomNavItemDef[]): HTMLElement {
  return bottomNavBar(items, navActiveKey(route))
}

// ================================================================
// 启动逻辑
// ================================================================
function boot(): void {
  // 初始化主题（从 localStorage 读取）
  initTheme()

  setRenderRequester(render)

  window.addEventListener('hashchange', () => {
    const to = parseHash()
    resetViewStates(currentRoute, to)
    currentRoute = to
    forceRender()
  })

  document.addEventListener('rerender', () => forceRender())
  // 需求13：输入框失焦延迟渲染（150ms），避免点击「保存」时输入框先失焦触发整页重建、
  // 导致本次 click 落在被移除的旧按钮上（需点两次才生效）。
  // Fix #4：增加与 store.subscribe 相同的焦点守卫，输入框间切换不触发整页重建。
  document.addEventListener('focusout', () => {
    setTimeout(() => {
      const ae = document.activeElement
      if (ae && (ae.tagName === 'INPUT' || ae.tagName === 'TEXTAREA' || ae.tagName === 'SELECT')) return
      render()
    }, 150)
  })

  // 日志相关页面（运行日志 / 计划任务-日志事件）：只对日志数据或连接状态变化渲染，
  // 忽略 thermal_update/state_update 等无关推送触发的整页重建 —— 否则每次推送都会重建列表 DOM，
  // 把正在查看历史的用户强制拉回顶部（scrollTop 归零）。
  let prevLogVersion = store.logVersion
  let prevDevState = store.device?.connectionState
  let prevSess = store.device?.sessionType
  let prevRun = store.device?.runState
  let prevBackend = store.backendOnline
  let prevNvs = store.pendingNvsSave
  const isLogsRoute = () => currentRoute.name === 'logs'
    || (currentRoute.name === 'schedules' && currentRoute.sub === 'logs')

  store.subscribe(() => {
    const ae = document.activeElement
    // 输入框中正在编辑：跳过 render，防止失焦触发整页重建导致点击丢失
    if (ae && (ae.tagName === 'INPUT' || ae.tagName === 'TEXTAREA' || ae.tagName === 'SELECT')) {
      return
    }
    // Item 3：用户正在选中文本（跨 15s thermal_update 周期）：跳过 render，防止选中状态丢失
    try {
      const sel = window.getSelection()
      if (sel && sel.toString().length > 0) return
    } catch {
      // ignore selection check errors
    }
    // Fix：日志相关页面忽略无关推送（日志未变且连接/后端/落盘状态未变 → 不渲染）
    if (isLogsRoute()) {
      // 自动跟随状态（第一页 + 未查看历史）才随 WS 推送实时渲染；查看历史/翻页时内容静止
      if (!isLogAutoFollow()) return
      const d = store.device
      const logChanged = store.logVersion !== prevLogVersion
      const connChanged = d?.connectionState !== prevDevState || d?.sessionType !== prevSess || d?.runState !== prevRun
      const stateChanged = store.backendOnline !== prevBackend || store.pendingNvsSave !== prevNvs
      if (!logChanged && !connChanged && !stateChanged) return
    }
    prevLogVersion = store.logVersion
    prevDevState = store.device?.connectionState
    prevSess = store.device?.sessionType
    prevRun = store.device?.runState
    prevBackend = store.backendOnline
    prevNvs = store.pendingNvsSave
    render()
  })

  // Fix：软键盘弹出会触发 window resize（Android 上打开键盘时布局视口收缩），
  // 若此时整页重建 #app 会销毁聚焦的输入框 → 输入失焦 → 软键盘立即收起。
  // 与 store.subscribe / focusout 相同的输入框守卫：编辑期间跳过渲染，键盘收起后再恢复渲染。
  window.addEventListener('resize', () => {
    const ae = document.activeElement
    if (ae && (ae.tagName === 'INPUT' || ae.tagName === 'TEXTAREA' || ae.tagName === 'SELECT')) return
    render()
  })

  forceRender()

  void store.loadInfo()
  void store.loadAll().then(() => {
    // 设置加载完成后，应用配置中的主题
    if (store.settings.theme) {
      setTheme(store.settings.theme)
    }
  })
  store.startWS()
}

boot()
