// 蓝牙连接设置页（需求 §10）
// 重构后（需求 #1）：仅保留 BLE 连接相关（设备状态 / 扫描连接 / 断开 / 握手 / 刷新）
// 连接参数类设置（自动连接 / 设备广播名 / Debug 模式）已迁至独立「连接设置」页（#/connection/config），
// 由「系统设置 · 硬件配置」卡中的「蓝牙连接设置」按钮（需求3：原本页「设置」按钮迁出）进入。
// 需求3：全局心跳超时阈值已迁至「连接设置」页（#/connection/config）
// #1：已删除"连接参数（仅保存 fnOS JSON）"区块
// #2/#3：广播名查询与修改（已迁移至连接设置页）
// #12：本页不再显示"控制功能不可用"横幅（监控总览保留）
// #20：debug 模式开关（已迁移至连接设置页）
// 信号质量改造：移除 RSSI 轮询（连接后主机无法读 RSSI），改为显示固件 PING 应答推算的信号质量

import { store } from '../store'
import { el, svgIcon, toast } from '../ui'
import { pageContainer, pageTitle } from './shared'
import { STATE_LABEL, STATE_COLOR, RUN_STATE_LABEL, RUN_STATE_COLOR, SESSION_LABEL, signalQualityLabel, signalQualityColor } from '../types'
import type { DeviceInfo } from '../types'

// Fix #3：模块级引用，记录当前打开的扫描弹窗关闭函数，供路由切换时清理
let activeScanClose: (() => void) | null = null

/** 路由切换时调用：关闭可能残留的扫描弹窗（退订 + 停止扫描 + 移除 overlay） */
export function closeScanOverlay(): void {
  if (activeScanClose) {
    activeScanClose()
    activeScanClose = null
  }
}

export function renderConnection(): HTMLElement {
  const d = store.device
  const connected = d?.connectionState === 'connected'
  // 信号质量：固件按接收帧异常率（重发率近似）推算，PING 应答携带（0~100，-1=未知）
  const signalQuality = d?.signalQuality ?? -1

  const wrap = pageContainer([])
  wrap.appendChild(pageTitle('蓝牙连接设置'))

  // ---- 连接设置卡片（启动时自动连接上次设备，自「控制板全局设置」页迁回） ----
  const autoCard = el('div', { class: 'card p-4 mb-4' })
  const acTitle = el('div', { class: 'flex items-center gap-2 mb-3' })
  acTitle.append(svgIcon('settings', 15))
  const acT = el('span', { class: 'text-sm font-semibold' }, ['连接设置'])
  acT.style.color = 'rgb(var(--c-ink))'
  acTitle.appendChild(acT)
  autoCard.appendChild(acTitle)

  const acRow = el('div', { class: 'flex items-center justify-between px-3 py-2 rounded-lg' })
  acRow.style.background = 'rgb(var(--c-element))'
  const acLabel = el('div', { class: 'text-sm' }, ['启动时自动连接上次设备'])
  const acHint = el('div', { class: 'text-xs mt-1' }, [`上次成功连接：${store.settings.lastAddress || '（无）'}`])
  acHint.style.color = 'rgb(var(--c-ink-muted))'
  acLabel.appendChild(acHint)
  acRow.appendChild(acLabel)
  const acWrap = el('label', { class: 'toggle' })
  const acInput = el('input', { type: 'checkbox' }) as HTMLInputElement
  acInput.checked = store.settings.autoConnect
  acInput.onchange = async () => {
    const ok = await store.saveSettings({
      ...store.settings,
      autoConnect: acInput.checked,
    })
    toast(ok ? `已${acInput.checked ? '启用' : '关闭'}启动自动连接` : '保存失败', ok ? 'success' : 'error')
  }
  acWrap.append(acInput, el('span', { class: 'toggle-slider' }))
  acRow.appendChild(acWrap)
  autoCard.appendChild(acRow)

  // ---- 设备状态卡片（Item 7：swap 位置，放在前面更重要） ----
  const statusCard = el('div', { class: 'card p-4 mb-4' })
  const stTitle = el('div', { class: 'flex items-center gap-2 mb-3' })
  stTitle.append(svgIcon('link', 15))
  const st = el('span', { class: 'text-sm font-semibold' }, ['设备状态'])
  st.style.color = 'rgb(var(--c-ink))'
  stTitle.appendChild(st)
  statusCard.appendChild(stTitle)

  const grid = el('div', { class: 'grid grid-cols-2 md:grid-cols-4 gap-3 mb-4' })
  grid.append(
    infoCell('物理连接', STATE_LABEL[d?.connectionState ?? 'disconnected'] ?? '未知',
      d ? (STATE_COLOR[d.connectionState] ?? 'rgb(var(--c-neutral))') : undefined),
    infoCell('运行状态',
      (d?.runState === 'WORK_RUN' && store.isDebugMode())
        ? 'Debug'
        : (RUN_STATE_LABEL[d?.runState ?? ''] ?? '未知'),
      d
        ? ((d.runState === 'WORK_RUN' && store.isDebugMode())
          ? 'rgb(var(--c-encrypt))'
          : (RUN_STATE_COLOR[d.runState] ?? 'rgb(var(--c-neutral))'))
        : undefined),
    infoCell('会话类型', d ? (SESSION_LABEL[d.sessionType] ?? '—') : '—',
      d?.sessionType === 'encrypted' ? 'rgb(0,158,97)' : 'rgb(249,192,100)'),
    infoCell('信号质量',
      signalQuality >= 0 ? signalQualityLabel(signalQuality) : '—',
      signalQuality >= 0 ? signalQualityColor(signalQuality) : undefined),
    infoCell('设备名称', d?.name ?? '—'),
    infoCell('MAC 地址', d?.address ?? '—'),
    infoCell('固件版本', d?.version ?? '—'),
    infoCell('连接时间', d?.connectedAt ? new Date(d.connectedAt).toLocaleString() : '—'),
  )
  statusCard.appendChild(grid)

  // ---- 操作按钮 ----
  const btns = el('div', { class: 'flex flex-wrap gap-2' })

  // FS.md 上位机#8：扫描全部蓝牙设备 → 由用户从清单选择连接对象
  const connBtn = el('button', { class: 'btn btn-primary' }, [svgIcon('signal', 14), ' 扫描设备并连接'])
  connBtn.disabled = d?.connectionState === 'scanning' || d?.connectionState === 'connecting' || d?.connectionState === 'reconnecting'
  connBtn.onclick = async () => {
    connBtn.disabled = true
    await scanAndPickDevices()
    connBtn.disabled = false
  }

  const discBtn = el('button', { class: 'btn' }, [svgIcon('x', 14), ' 断开连接'])
  discBtn.disabled = !connected
  discBtn.onclick = async () => {
    discBtn.disabled = true
    const ok = await store.disconnect()
    toast(ok ? '已断开蓝牙连接' : '断开失败', ok ? 'success' : 'error')
  }

  // #23：初始化握手按钮仅在连接后且设备未初始化（UNINIT）时显示
  if (connected && d?.runState === 'UNINIT') {
    const hsBtn = el('button', { class: 'btn' }, [svgIcon('check', 14), ' 初始化握手'])
    hsBtn.title = 'RSA/AES 加密握手，进入 WORK_RUN 加密会话后方可控制'
    hsBtn.onclick = async () => {
      hsBtn.disabled = true
      const ok = await store.handshake()
      toast(ok ? '握手完成，已进入加密会话' : '握手失败，详见日志', ok ? 'success' : 'error')
      if (ok) await store.loadStatus()
    }
    btns.appendChild(hsBtn)
  }

  const refreshBtn = el('button', { class: 'btn' }, [svgIcon('refresh', 14), ' 刷新硬件状态'])
  refreshBtn.disabled = !connected
  refreshBtn.onclick = async () => {
    refreshBtn.disabled = true
    const ok = await store.refresh()
    refreshBtn.disabled = false
    toast(ok ? '已刷新 GET STATUS / FAN / SWITCH' : '刷新失败，详见日志', ok ? 'success' : 'error')
  }

  btns.append(connBtn, discBtn, refreshBtn)
  statusCard.appendChild(btns)

  wrap.appendChild(statusCard)
  // Item 7：交换位置 — 连接设置卡放在设备状态后面
  wrap.appendChild(autoCard)

  return wrap
}

// ================================================================
// 小工具
// ================================================================
function infoCell(label: string, value: string, color?: string): HTMLElement {
  const cell = el('div', { class: 'rounded-lg p-3' })
  cell.style.background = 'rgb(var(--c-element))'
  const l = el('div', { class: 'text-xs mb-1' }, [label])
  l.style.color = 'rgb(var(--c-ink-subtle))'
  const v = el('div', { class: 'text-sm font-semibold truncate' }, [value])
  v.style.color = color ?? 'rgb(var(--c-ink))'
  cell.append(l, v)
  return cell
}

// FS.md 上位机#8：点击"扫描设备并连接" → 立即打开弹窗 + 启动后台实时扫描
export async function scanAndPickDevices(): Promise<void> {
  showDevicePicker()
  const ok = await store.startScan()
  if (!ok) {
    toast('启动扫描失败，请检查蓝牙适配器', 'error')
  }
}

// FS.md 上位机#8：实时扫描清单弹窗
function showDevicePicker(): void {
  const overlay = el('div', { class: 'fixed inset-0 z-50 flex items-center justify-center' })
  overlay.style.background = 'rgb(var(--c-overlay) / 0.6)'
  const modal = el('div', { class: 'card flex max-h-[80vh] w-[440px] max-w-[92vw] flex-col p-4' })

  const title = el('h3', { class: 'text-base font-semibold mb-1' }, ['选择要连接的蓝牙设备'])
  const sub = el('p', { class: 'text-xs mb-2' }, ['正在扫描周围蓝牙设备…（NR_F2S4 设备高亮显示）'])
  sub.style.color = 'rgb(var(--c-ink-muted))'

  const list = el('div', { class: 'flex-1 overflow-y-auto flex flex-col gap-2 mb-3' })

  const footer = el('div', { class: 'flex justify-between items-center' })
  const statusHint = el('span', { class: 'text-xs' }, ['扫描中…'])
  statusHint.style.color = 'rgb(var(--c-ink-muted))'
  const rightBtns = el('div', { class: 'flex gap-2' })
  const stopBtn = el('button', { class: 'btn' }, ['停止扫描'])
  const closeBtn = el('button', { class: 'btn', style: 'display:none' }, ['取消'])
  rightBtns.append(stopBtn, closeBtn)
  footer.append(statusHint, rightBtns)

  modal.append(title, sub, list, footer)
  overlay.appendChild(modal)
  overlay.onclick = (e) => { if (e.target === overlay) close() }
  document.body.appendChild(overlay)

  // 排序：NR_F2S4 前缀优先，其余按 RSSI 降序
  const sortDevices = (devices: DeviceInfo[]): DeviceInfo[] => {
    const pref = store.settings.deviceName || 'NR_F2S4'
    return [...devices].sort((a, b) => {
      const pa = (a.name || '').startsWith(pref)
      const pb = (b.name || '').startsWith(pref)
      if (pa !== pb) return pa ? -1 : 1
      return b.rssi - a.rssi
    })
  }

  // 渲染设备列表
  const renderList = () => {
    const devs = store.scanResults
    list.innerHTML = ''
    if (devs.length === 0) {
      const empty = el('div', { class: 'text-center py-6 text-sm' }, [
        svgIcon('signal', 20),
        ' 暂无设备，继续扫描中…',
      ])
      empty.style.color = 'rgb(var(--c-ink-muted))'
      list.appendChild(empty)
      return
    }
    for (const dev of sortDevices(devs)) {
      const row = el('button', { class: 'flex items-center justify-between gap-2 px-3 py-2 rounded-lg text-left w-full' })
      row.style.background = 'rgb(var(--c-element))'
      row.style.border = '1px solid rgb(var(--c-element))'
      row.onmouseenter = () => { row.style.borderColor = 'rgb(var(--c-primary))' }
      row.onmouseleave = () => { row.style.borderColor = 'rgb(var(--c-element))' }
      const isOurs = (dev.name || '').startsWith(store.settings.deviceName || 'NR_F2S4')
      const left = el('div', { class: 'flex items-center gap-2 min-w-0' })
      const nm = el('div', { class: 'text-sm font-medium truncate' }, [dev.name || '(未命名设备)'])
      nm.style.color = isOurs ? 'rgb(var(--c-primary))' : 'rgb(var(--c-ink))'
      if (isOurs) {
        const tag = el('span', { class: 'text-xs px-1.5 py-0.5 rounded ml-1' }, ['推荐'])
        tag.style.background = 'rgba(var(--c-primary),0.15)'
        tag.style.color = 'rgb(var(--c-primary))'
        nm.appendChild(tag)
      }
      const addr = el('div', { class: 'text-xs truncate' }, [dev.address])
      addr.style.color = 'rgb(var(--c-ink-subtle))'
      const nmWrap = el('div', { class: 'min-w-0' })
      nmWrap.append(nm, addr)
      left.append(svgIcon(isOurs ? 'wifi' : 'signal', 15), nmWrap)
      const rssi = el('div', { class: 'text-xs shrink-0' }, [`${dev.rssi} dBm`])
      rssi.style.color = 'rgb(var(--c-ink-muted))'
      row.append(left, rssi)
      row.onclick = async () => {
        await close()
        connectToDevice(dev)
      }
      list.appendChild(row)
    }
  }

  // 订阅 store 变化
  const unsub = store.subscribe(() => {
    renderList()
    if (store.scanRunning) {
      statusHint.textContent = `扫描中… 已发现 ${store.scanResults.length} 台设备`
      stopBtn.style.display = ''
      closeBtn.style.display = 'none'
    } else {
      statusHint.textContent = `扫描结束，共发现 ${store.scanResults.length} 台设备`
      stopBtn.style.display = 'none'
      closeBtn.style.display = ''
    }
  })

  renderList()

  let closed = false
  async function close() {
    if (closed) return
    closed = true
    activeScanClose = null
    unsub()
    await store.stopScan()
    overlay.remove()
  }
  // Fix #3：注册关闭函数，供路由切换时清理
  activeScanClose = close

  stopBtn.onclick = close
  closeBtn.onclick = close
}

async function connectToDevice(dev: DeviceInfo): Promise<void> {
  const ok = await store.connectTo(dev.address, dev.name)
  if (ok) toast(`已连接 ${dev.name || dev.address}`, 'success')
  else toast('连接失败，详见日志', 'error')
}
