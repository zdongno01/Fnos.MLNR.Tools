// 硬盘组管理页：从主页拆分，独立路由 #/disks
// 依据 FS3.1.md §4 实现。

import { store } from '../store'
import { api } from '../api'
import { el, svgIcon, toast, confirmDialog, occupyDialog } from '../ui'
import { requestRender, canControl, renderGateBanner, sectionTitle, badge, isDeviceConnected } from './shared'
import type { DiskGroupView, DiskGroupStats, DiskLogEntry, DiskLogQueryResult, CommandResult } from '../types'

// ===== 模块级 UI 状态 =====
const ui = {
  /** 每个硬盘组的操作处理中标记 */
  groupBusy: {} as Record<number, boolean>,
}

// ===== 硬盘组日志 UI 状态 =====
const logUi = {
  page: 1,
  pageSize: 20,
  groupId: -1,        // -1 = 全部
  action: '',         // '' = 全部
  from: '',           // 起始日期（datetime-local 值）
  to: '',             // 结束日期（datetime-local 值）
  result: null as DiskLogQueryResult | null,
  fetching: false,
  lastSig: '',
}

/** 构造时间范围签名（用于避免重复拉取） */
function logQuerySig(): string {
  return `${logUi.groupId}|${logUi.action}|${logUi.from}|${logUi.to}|${logUi.page}|${logUi.pageSize}`
}

/** 拉取硬盘组日志 */
async function fetchDiskLogs(): Promise<void> {
  if (logUi.fetching) return
  logUi.fetching = true
  logUi.lastSig = logQuerySig()
  try {
    const params: { page: number; pageSize: number; groupId?: number; action?: string; from?: string; to?: string } = {
      page: logUi.page, pageSize: logUi.pageSize,
    }
    if (logUi.groupId >= 0) params.groupId = logUi.groupId
    if (logUi.action) params.action = logUi.action
    // datetime-local → RFC3339
    if (logUi.from) params.from = new Date(logUi.from).toISOString()
    if (logUi.to) {
      // 结束日期补 23:59:59
      const to = new Date(logUi.to)
      to.setHours(23, 59, 59, 999)
      params.to = to.toISOString()
    }
    logUi.result = await api.getDiskLogs(params)
  } catch {
    logUi.result = { total: 0, page: 1, size: 20, items: [], actionLabels: {} }
  } finally {
    logUi.fetching = false
  }
  requestRender()
}

// ================================================================
// 入口
// ================================================================
export function renderDisks(): HTMLElement {
  const wrap = el('div', { class: 'mx-auto w-full max-w-5xl px-4 py-5' })

  const gate = renderGateBanner()
  if (gate) wrap.appendChild(gate)

  wrap.appendChild(sectionTitle('hdd', '硬盘组管理'))

  // 需求：蓝牙未连接时不展示上次连接残留的实时数据（参考「风扇控制」未连接即无数据），改为空态提示
  if (!isDeviceConnected()) {
    const empty = el('div', { class: 'card px-4 py-8 text-center text-sm' },
      ['蓝牙未连接，连接控制板后显示硬盘组状态。'])
    empty.style.color = 'rgb(var(--c-ink-subtle))'
    wrap.appendChild(empty)
    return wrap
  }

  const groups = store.diskGroups.filter(g => g.enabled)
  if (groups.length === 0) {
    const empty = el('div', { class: 'card px-4 py-8 text-center text-sm' },
      ['尚未启用任何硬盘组，请前往「系统设置 · 硬盘组控制通道」启用。'])
    empty.style.color = 'rgb(var(--c-ink-subtle))'
    wrap.appendChild(empty)
  } else {
    // 统计信息独立组件：稳定显示各硬盘组的在线时长与开关次数（0 也显示，不被隐藏）
    wrap.appendChild(renderDiskStatsPanel(groups))
    const grid = el('div', { class: 'grid grid-cols-1 lg:grid-cols-2 gap-4' })
    for (const g of groups) grid.appendChild(renderDiskGroupPanel(g))
    wrap.appendChild(grid)
  }

  // ---- 硬盘组操作日志（长期保存，支持筛选 + 分页） ----
  wrap.appendChild(sectionTitle('log', '操作日志'))
  wrap.appendChild(renderDiskLogPanel())
  // 异步首次拉取
  if (!logUi.fetching && logQuerySig() !== logUi.lastSig) {
    void fetchDiskLogs()
  }

  return wrap
}

// ================================================================
// 硬盘组面板（需求 §4）
// ================================================================
// ================================================================
// 硬盘组统计独立组件（需求：组件化，右上角编辑选择显示哪些统计项）
// 全部为单值列；服务端按 settings.diskStatsEnabled 只计算启用项（空 = 全部）。
// ================================================================
interface StatCol {
  id: string
  label: string
  fmt: (s: DiskGroupStats | undefined, g: DiskGroupView) => string
}

const STAT_COLS: StatCol[] = [
  {
    id: 'onlineMinutes', label: '本次在线时长',
    fmt: (s) => (s?.online ? (s.onlineMinutes > 0 ? formatDuration(s.onlineMinutes) : '不足 1 分钟') : '—'),
  },
  { id: 'totalOnline', label: '总在线时长', fmt: (s) => (s ? formatDuration(s.totalOnlineMinutes) : '—') },
  { id: 'aogOnline', label: '平均单次在线', fmt: (s) => (s ? formatDuration(s.avgOnlineMinutes) : '—') },
  { id: 'switch7d', label: '7 天开关', fmt: (s) => `${s?.switchCount7d ?? 0} 次` },
  { id: 'switch30d', label: '30 天开关', fmt: (s) => `${s?.switchCount30d ?? 0} 次` },
  { id: 'totalSwitch', label: '累计开关次数', fmt: (s) => `${s?.totalSwitchCount ?? 0} 次` },
  { id: 'forceOff', label: '强制下线次数', fmt: (s) => `${s?.forceOffCount ?? 0} 次` },
]

/** 当前启用的统计列（settings.diskStatsEnabled 空 = 全部） */
function actioeStatCols(): StatCol[] {
  const enabled = store.settings.diskStatsEnabled
  if (!Array.isArray(enabled) || enabled.length === 0) return STAT_COLS
  return STAT_COLS.filter(c => enabled.includes(c.id))
}

/** 统计组件选择弹窗（编辑按钮入口） */
function openStatsEditor(): void {
  const overlay = el('div', { class: 'fixed inset-0 z-50 flex items-center justify-center' })
  overlay.style.background = 'rgb(var(--c-overlay) / 0.6)'
  const modal = el('div', { class: 'card w-96 p-5' })
  const titleEl = el('h3', { class: 'font-semibold text-base mb-1' }, ['统计组件'])
  titleEl.style.color = 'rgb(var(--c-ink))'
  const hint = el('p', { class: 'text-xs mb-3' }, ['勾选要在「硬盘组统计」中显示并计算的统计项；服务端保存，所有终端一致。'])
  hint.style.color = 'rgb(var(--c-ink-muted))'
  modal.appendChild(titleEl)
  modal.appendChild(hint)

  const current = actioeStatCols().map(c => c.id)
  const checks = new Map<string, HTMLInputElement>()
  for (const c of STAT_COLS) {
    const row = el('label', { class: 'flex items-center gap-2 py-1.5 cursor-pointer text-sm' })
    const cb = el('input', { type: 'checkbox' }) as HTMLInputElement
    cb.checked = current.includes(c.id)
    checks.set(c.id, cb)
    const labelTxt = el('span', {}, [c.label])
    labelTxt.style.color = 'rgb(var(--c-ink))'
    row.append(cb, labelTxt)
    modal.appendChild(row)
  }

  const btns = el('div', { class: 'flex justify-end gap-2 mt-4' })
  const cancel = el('button', { class: 'btn' }, ['取消'])
  const ok = el('button', { class: 'btn btn-primary' }, ['保存'])
  cancel.onclick = () => { overlay.remove() }
  ok.onclick = async () => {
    const selected: string[] = []
    for (const [id, cb] of checks) if (cb.checked) selected.push(id)
    ok.disabled = true
    const saved = await store.saveSettings({ ...store.settings, diskStatsEnabled: selected })
    overlay.remove()
    toast(saved ? '统计组件已保存' : '保存失败', saved ? 'success' : 'error')
    requestRender()
  }
  btns.append(cancel, ok)
  modal.appendChild(btns)
  overlay.appendChild(modal)
  overlay.onclick = (e) => { if (e.target === overlay) overlay.remove() }
  document.body.appendChild(overlay)
}

function renderDiskStatsPanel(groups: DiskGroupView[]): HTMLElement {
  const card = el('div', { class: 'card p-4 mb-4' })

  const head = el('div', { class: 'flex items-center gap-2 mb-3' })
  head.appendChild(svgIcon('chart', 15))
  const t = el('span', { class: 'text-sm font-semibold' }, ['硬盘组统计'])
  t.style.color = 'rgb(var(--c-ink))'
  head.appendChild(t)
  const editBtn = el('button', { class: 'btn btn-sm shrink-0 ml-auto' }, [svgIcon('edit', 13), '编辑'])
  editBtn.title = '选择要统计并显示的组件'
  editBtn.onclick = () => { openStatsEditor() }
  head.appendChild(editBtn)
  card.appendChild(head)

  const actioeCols = actioeStatCols()
  if (actioeCols.length === 0) {
    const empty = el('div', { class: 'text-sm py-3 text-center' }, ['未启用任何统计组件，点击右上角「编辑」选择。'])
    empty.style.color = 'rgb(var(--c-ink-subtle))'
    card.appendChild(empty)
    return card
  }

  // 表格：组名 + 状态 + 启用列（列数随勾选变化，横向可滚动）
  const scroll = el('div', { class: 'overflow-x-auto' })
  const table = el('table', { class: 'w-full text-xs border-collapse' })
  const thead = el('thead')
  const headRow = el('tr')
  const th = (txt: string, right: boolean): HTMLTableCellElement => {
    const c = document.createElement('th')
    c.textContent = txt
    c.className = 'font-medium text-[11px] py-1 pr-3 whitespace-nowrap ' + (right ? 'text-right' : 'text-left')
    ;(c as HTMLElement).style.color = 'rgb(var(--c-ink-subtle))'
    return c
  }
  headRow.append(th('硬盘组', false), th('状态', false))
  for (const c of actioeCols) headRow.appendChild(th(c.label, true))
  thead.appendChild(headRow)

  const tbody = el('tbody')
  for (const g of groups) {
    const s = g.stats
    const tr = el('tr')
    tr.style.borderBottom = '1px solid rgb(var(--c-border,229,231,235))'
    const nameTd = el('td', { class: 'py-2 pr-3 whitespace-nowrap' })
    const nameCell = el('div', { class: 'flex items-center gap-1.5 min-w-0' })
    const name = el('span', { class: 'truncate font-medium' }, [g.alias || `硬盘组 ${g.id}`])
    name.style.color = 'rgb(var(--c-ink))'
    if (store.isDebugMode()) nameCell.append(name, badge(`SW${g.switchN}`, '--c-primary', true))
    else nameCell.appendChild(name)
    nameTd.appendChild(nameCell)
    tr.appendChild(nameTd)

    const statusTd = el('td', { class: 'py-2 pr-3 whitespace-nowrap' })
    statusTd.textContent = g.online ? '在线' : '离线'
    statusTd.style.color = g.online ? 'rgb(var(--c-success))' : 'rgb(var(--c-ink-subtle))'
    tr.appendChild(statusTd)

    for (const c of actioeCols) {
      const td = el('td', { class: 'py-2 pr-3 whitespace-nowrap text-right tabular-nums' })
      td.textContent = c.fmt(s, g)
      td.style.color = 'rgb(var(--c-ink-subtle))'
      tr.appendChild(td)
    }
    tbody.appendChild(tr)
  }
  table.append(thead, tbody)
  scroll.appendChild(table)
  card.appendChild(scroll)

  return card
}

function renderDiskGroupPanel(g: DiskGroupView): HTMLElement {
  const card = el('div', { class: 'card p-4' })
  const busy = !!ui.groupBusy[g.id]
  // #17：检查系统关键磁盘属性（通过 switchN 查找对应开关的 system 字段）
  const sw = store.switches.find(s => s.index === g.switchN)
  const isSystemDisk = sw?.system === true
  const controllable = canControl() && !g.conflict && !busy

  // ---- 头部 ----
  const head = el('div', { class: 'flex items-center gap-2.5 mb-3' })
  const iconWrap = el('div', { class: 'w-9 h-9 rounded-lg flex items-center justify-center shrink-0' })
  iconWrap.style.background = 'rgb(var(--c-element))'
  iconWrap.style.color = g.online ? 'rgb(var(--c-success))' : 'rgb(var(--c-ink-subtle))'
  iconWrap.appendChild(svgIcon('hdd', 18))

  const titleBox = el('div', { class: 'flex-1 min-w-0' })
  const nameRow = el('div', { class: 'flex items-center gap-2 flex-wrap' })
  const name = el('span', { class: 'font-semibold text-[15px] truncate' }, [g.alias || `硬盘组 ${g.id}`])
  name.style.color = 'rgb(var(--c-ink))'
  if (store.isDebugMode()) nameRow.append(name, badge(`SW${g.switchN}`, '--c-primary', true))
  else nameRow.appendChild(name)
  if (isSystemDisk) nameRow.appendChild(badge('系统关键', '--c-danger'))
  if (g.conflict) nameRow.appendChild(badge('通道冲突', '--c-danger'))
  if (g.online) nameRow.appendChild(badge('在线', '--c-success'))
  else nameRow.appendChild(badge('离线', '--c-neutral'))
  titleBox.appendChild(nameRow)

  // 上线/下线开关（#17：系统关键磁盘时禁用）
  const swWrap = el('label', { class: 'toggle', title: isSystemDisk ? '系统关键磁盘，禁止操作' : (g.online ? '下线（安全断电流程）' : '上线（硬盘上电）') })
  const swInput = el('input', { type: 'checkbox' }) as HTMLInputElement
  swInput.checked = g.online
  swInput.disabled = !controllable || isSystemDisk
  if (isSystemDisk) {
    swWrap.style.opacity = '0.5'
    swWrap.style.cursor = 'not-allowed'
  }
  swInput.onchange = async () => {
    if (swInput.checked) {
      await groupAction(g.id, '上线', () => store.diskPowerOn(g.id), true)
    } else {
      await groupAction(g.id, '下线', (act?: string) => store.diskPowerOff(g.id, act), true)
    }
  }
  const sliderEl = el('span', { class: 'toggle-slider' })
  swWrap.append(swInput, sliderEl)

  head.append(iconWrap, titleBox, swWrap)
  card.appendChild(head)

  if (g.conflict) {
    const warn = el('div', { class: 'text-xs mb-2' },
      ['该硬盘组绑定的 SW 通道被多个硬盘组占用，已被禁用。请前往设置页修正配置。'])
    warn.style.color = 'rgb(var(--c-danger))'
    card.appendChild(warn)
  }

  // ---- 硬盘列表 ----
  if (g.disks.length === 0) {
    const empty = el('div', { class: 'text-xs py-2' }, ['该硬盘组未配置硬盘，请前往设置页添加。'])
    empty.style.color = 'rgb(var(--c-ink-subtle))'
    card.appendChild(empty)
  } else {
    const list = el('div', { class: 'flex flex-col gap-1.5' })
    for (const d of g.disks) {
      list.appendChild(renderDiskRow(g.id, d, isSystemDisk))
    }
    card.appendChild(list)
  }

  // ---- 统计信息已移至页面顶部独立组件（renderDiskStatsPanel） ----

  // ---- 强制下线按钮 ----
  const foot = el('div', { class: 'flex items-center justify-between mt-3 pt-3' })
  foot.style.borderTop = '1px solid rgb(var(--c-line-subtle))'
  const footHint = el('div', { class: 'text-xs' }, [busy ? '正在执行硬盘组操作…' : '下线将依次卸载并停转硬盘后断电'])
  footHint.style.color = 'rgb(var(--c-ink-subtle))'
  const forceBtn = el('button', { class: 'btn btn-danger btn-sm' }, [svgIcon('power-off', 14), ' 强制下线'])
  // 需求8：硬盘组已离线时禁用变灰（无需再强制下线）
  forceBtn.disabled = !controllable || !g.online
  forceBtn.title = g.online ? '强制断电该硬盘组（危险操作）' : '硬盘组已离线'
  forceBtn.onclick = async () => {
    const ok = await confirmDialog(
      '⚠️ 危险操作：强制下线将直接切断硬盘硬件电源，不执行卸载、硬盘停转流程。可能造成文件系统损坏、数据丢失、硬盘物理损伤，请确认你清楚当前操作后果。',
      '强制下线确认',
      true,
    )
    if (!ok) return
    await groupAction(g.id, '强制断电', () => store.diskForcePowerOff(g.id), true)
  }
  foot.append(footHint, forceBtn)
  card.appendChild(foot)

  return card
}

function renderDiskRow(groupId: number, d: DiskGroupView['disks'][number], isSystemDisk: boolean): HTMLElement {
  const row = el('div', {
    class: 'flex items-center gap-2 px-3 py-2 rounded text-xs',
  })
  row.style.background = 'rgb(var(--c-element))'

  const dot = el('span', { class: 'status-dot shrink-0' })
  dot.className += d.mounted ? ' status-dot-connected' : ' status-dot-disconnected'

  const info = el('div', { class: 'flex-1 min-w-0' })
  const nameRow = el('div', { class: 'flex items-center gap-1.5 flex-wrap' })
  const name = el('span', { class: 'font-medium' }, [d.alias || d.device])
  name.style.color = 'rgb(var(--c-ink))'
  nameRow.appendChild(name)
  const dev = el('span', {}, [d.device])
  dev.style.color = 'rgb(var(--c-ink-subtle))'
  nameRow.appendChild(dev)
  if (d.isNvme) nameRow.appendChild(badge('NVMe', '--c-download', true))
  info.appendChild(nameRow)

  const mountRow = el('div', { class: 'mt-0.5 truncate flex items-center gap-2' })
  mountRow.style.color = 'rgb(var(--c-ink-muted))'
  const mountText = el('span', { class: 'truncate' })
  mountText.textContent = d.mounted ? `已挂载：${d.mountPath || '（未知挂载点）'}` : '未挂载'
  mountRow.appendChild(mountText)
  // 3.md：SN 关联的实时温度（DiskView.temperature 由后端按 SN 填充）
  if (typeof d.temperature === 'number' && Number.isFinite(d.temperature)) {
    const tTag = el('span', {
      class: 'shrink-0 text-[11px] font-semibold tabular-nums px-1.5 py-0.5 rounded-full',
    }, [`${d.temperature.toFixed(1)} ℃`])
    const t = d.temperature
    tTag.style.background = t >= 60
      ? 'rgb(var(--c-danger-soft))' : t >= 45
        ? 'rgb(var(--c-warning-soft))' : 'rgb(var(--c-success-soft))'
    tTag.style.color = t >= 60
      ? 'rgb(var(--c-danger-soft-text))' : t >= 45
        ? 'rgb(var(--c-warning-soft-text))' : 'rgb(var(--c-success-soft-text))'
    mountRow.appendChild(tTag)
  }
  info.appendChild(mountRow)

  const btn = el('button', { class: 'btn btn-sm shrink-0' }, [])
  if (d.mounted) {
    btn.textContent = '卸载'
    btn.appendChild(svgIcon('x', 12))
    // #17：系统关键磁盘时禁用卸载按钮
    btn.disabled = isSystemDisk
    btn.title = isSystemDisk ? '系统关键磁盘，禁止操作' : '卸载该硬盘'
    if (isSystemDisk) {
      btn.style.opacity = '0.5'
      btn.style.cursor = 'not-allowed'
    }
    btn.onclick = async () => {
      if (isSystemDisk) return
      btn.disabled = true
      let result = await store.diskUnmount(groupId, d.device)
      // 卸载失败且被进程占用：弹窗让用户选择处理方式（终止进程 / 强制卸载 / 取消）
      while (result?.decision === 'unmount_fail_occupied') {
        const choice = await occupyDialog(d.alias || d.device, result.occupied || [])
        if (choice === 'cancel') {
          toast(`卸载 ${d.alias || d.device} 已取消（存在占用进程）`, 'info')
          btn.disabled = false
          return
        }
        result = await store.diskUnmount(groupId, d.device, choice)
      }
      btn.disabled = false
      if (result?.ok) {
        toast(`卸载 ${d.alias || d.device} 成功`, 'success')
      } else {
        toast(`卸载 ${d.alias || d.device} 失败：${result?.message || '未知原因'}`, 'error')
      }
      void fetchDiskLogs()
    }
  } else {
    btn.textContent = '挂载'
    btn.classList.add('btn-primary')
    btn.appendChild(svgIcon('plus', 12))
    btn.onclick = async () => {
      btn.disabled = true
      const ok = await store.diskMount(groupId, d.device)
      btn.disabled = false
      if (!ok) toast(`挂载 ${d.alias || d.device} 失败，请检查挂载路径配置`, 'error')
      void fetchDiskLogs()
    }
  }
  row.append(dot, info, btn)
  return row
}

/** 硬盘组操作统一处理：忙态标记 + 占用决策弹窗 + 结果提示 + 自动刷新状态 & 日志 */
async function groupAction(
  groupId: number,
  actionName: string,
  fn: (action?: string) => Promise<CommandResult | null>,
  reload = false,
): Promise<void> {
  ui.groupBusy[groupId] = true
  requestRender()
  try {
    let result = await fn()
    // 下线卸载失败且被进程占用：弹窗让用户选择处理方式，然后带 action 重试
    while (result?.decision === 'unmount_fail_occupied') {
      const choice = await occupyDialog(`硬盘组 ${groupId}`, result.occupied || [])
      if (choice === 'cancel') {
        toast(`硬盘组 ${actionName} 已取消（存在占用进程）`, 'info')
        return
      }
      result = await fn(choice)
    }
    if (result?.ok) {
      toast(`硬盘组 ${actionName} 指令已执行`, 'success')
      if (reload) await store.loadStatus()
    } else {
      toast(`硬盘组 ${actionName} 失败：${result?.message || '存在未卸载硬盘或链路异常，详见日志'}`, 'error')
    }
  } finally {
    delete ui.groupBusy[groupId]
    requestRender()
    // 操作完成后自动刷新日志面板（不改变当前筛选/分页）
    void fetchDiskLogs()
  }
}

/** 将分钟数格式化为可读时长 */
function formatDuration(minutes: number): string {
  if (minutes < 60) return `${minutes} 分钟`
  if (minutes < 60 * 24) {
    const h = Math.floor(minutes / 60)
    const m = minutes % 60
    return m > 0 ? `${h} 小时 ${m} 分` : `${h} 小时`
  }
  const d = Math.floor(minutes / (60 * 24))
  const h = Math.floor((minutes % (60 * 24)) / 60)
  return h > 0 ? `${d} 天 ${h} 小时` : `${d} 天`
}

// ================================================================
// 硬盘组操作日志面板（筛选 + 表格 + 分页）
// ================================================================
function renderDiskLogPanel(): HTMLElement {
  const card = el('div', { class: 'card p-4' })

  // ---- 筛选工具栏 ----
  const toolbar = el('div', { class: 'flex flex-wrap items-center gap-2 mb-3' })

  // 硬盘组筛选
  const grpSelect = el('select', { class: 'input', style: 'width:auto;padding:4px 8px;font-size:12px' }) as HTMLSelectElement
  grpSelect.appendChild(el('option', { value: '-1' }, ['全部硬盘组']))
  store.diskGroups.forEach(g => {
    const op = el('option', { value: String(g.id) }, [g.alias || `硬盘组 ${g.id}`])
    if (g.id === logUi.groupId) (op as HTMLOptionElement).selected = true
    grpSelect.appendChild(op)
  })
  grpSelect.onchange = () => {
    logUi.groupId = Number(grpSelect.value)
    logUi.page = 1
    void fetchDiskLogs()
  }
  toolbar.appendChild(grpSelect)

  // 动作类型筛选
  const actionSelect = el('select', { class: 'input', style: 'width:auto;padding:4px 8px;font-size:12px' }) as HTMLSelectElement
  const labels = logUi.result?.actionLabels ?? {}
  const actionKeys = Object.keys(labels).length > 0 ? Object.keys(labels) : ['', 'power_on', 'power_off', 'mount', 'unmount']
  for (const k of actionKeys) {
    const op = el('option', { value: k }, [labels[k] || (k === '' ? '全部动作' : k)])
    if (k === logUi.action) (op as HTMLOptionElement).selected = true
    actionSelect.appendChild(op)
  }
  actionSelect.onchange = () => {
    logUi.action = actionSelect.value
    logUi.page = 1
    void fetchDiskLogs()
  }
  toolbar.appendChild(actionSelect)

  // 日期范围
  const fromInput = el('input', {
    type: 'date', class: 'input', style: 'width:auto;padding:4px 8px;font-size:12px', value: logUi.from,
  }) as HTMLInputElement
  const toInput = el('input', {
    type: 'date', class: 'input', style: 'width:auto;padding:4px 8px;font-size:12px', value: logUi.to,
  }) as HTMLInputElement
  const refresh = () => {
    logUi.from = fromInput.value
    logUi.to = toInput.value
    logUi.page = 1
    void fetchDiskLogs()
  }
  fromInput.onchange = refresh
  toInput.onchange = refresh
  toolbar.appendChild(fromInput)
  const dash = el('span', { class: 'text-xs' }, ['~'])
  dash.style.color = 'rgb(var(--c-ink-subtle))'
  toolbar.appendChild(dash)
  toolbar.appendChild(toInput)

  // 刷新按钮
  const refreshBtn = el('button', { class: 'btn btn-sm' }, [svgIcon('refresh', 13), ' 刷新'])
  refreshBtn.onclick = () => { void fetchDiskLogs() }
  toolbar.appendChild(refreshBtn)

  // 清除筛选
  const clearBtn = el('button', { class: 'btn btn-sm' }, ['清除筛选'])
  clearBtn.onclick = () => {
    logUi.groupId = -1
    logUi.action = ''
    logUi.from = ''
    logUi.to = ''
    fromInput.value = ''
    toInput.value = ''
    void fetchDiskLogs()
  }
  toolbar.appendChild(clearBtn)

  card.appendChild(toolbar)

  // ---- 日志表格 ----
  if (logUi.fetching) {
    const loading = el('div', { class: 'text-sm py-6 text-center' }, ['加载中…'])
    loading.style.color = 'rgb(var(--c-ink-subtle))'
    card.appendChild(loading)
    return card
  }

  const result = logUi.result
  if (!result || result.items.length === 0) {
    const empty = el('div', { class: 'text-sm py-6 text-center' }, ['暂无硬盘组操作日志'])
    empty.style.color = 'rgb(var(--c-ink-subtle))'
    card.appendChild(empty)
    return card
  }

  const tableWrap = el('div', { style: 'overflow-x:auto' })
  const table = el('table', { class: 'w-full text-xs' }) as HTMLTableElement
  table.style.borderCollapse = 'separate'
  table.style.borderSpacing = '0'

  // 表头（删除了"内容"列）
  const thead = el('thead')
  const headRow = el('tr')
  const cols = [
    { label: '时间', width: '160px' },
    { label: '硬盘组', width: '140px' },
    { label: '动作', width: '90px' },
    { label: '结果', width: '70px' },
    { label: '备注', width: undefined },
  ]
  for (const c of cols) {
    const th = el('th', { style: 'text-align:left;padding:6px 8px;font-weight:600;' })
    th.style.color = 'rgb(var(--c-ink-subtle))'
    if (c.width) th.style.width = c.width
    th.textContent = c.label
    headRow.appendChild(th)
  }
  thead.appendChild(headRow)
  table.appendChild(thead)

  // 表体
  const tbody = el('tbody')
  for (const e of result.items) {
    tbody.appendChild(renderLogRow(e, labels))
  }
  table.appendChild(tbody)
  tableWrap.appendChild(table)
  card.appendChild(tableWrap)

  // ---- 分页 ----
  const totalPages = Math.max(1, Math.ceil(result.total / result.size))
  const foot = el('div', { class: 'flex items-center justify-between gap-2 mt-3' })
  foot.style.borderTop = '1px solid rgb(var(--c-line-subtle))'
  foot.style.paddingTop = '8px'

  const totalLabel = el('span', { class: 'text-xs' }, [`共 ${result.total} 条 · 第 ${result.page}/${totalPages} 页`])
  totalLabel.style.color = 'rgb(var(--c-ink-subtle))'

  const pageBtns = el('div', { class: 'flex items-center gap-1.5' })
  const prev = el('button', { class: 'btn btn-sm', style: 'display:flex;align-items:center;gap:4px' }, [svgIcon('back', 12), ' 上一页'])
  prev.disabled = result.page <= 1
  prev.onclick = () => {
    logUi.page = result.page - 1
    void fetchDiskLogs()
  }
  const next2 = el('button', { class: 'btn btn-sm', style: 'display:flex;align-items:center;gap:4px' }, ['下一页', svgIcon('back', 12)])
  ;(next2.querySelector('svg') as SVGSVGElement).style.transform = 'scaleX(-1)'
  next2.disabled = result.page >= totalPages
  next2.onclick = () => {
    logUi.page = result.page + 1
    void fetchDiskLogs()
  }

  pageBtns.append(prev, next2)
  foot.append(totalLabel, pageBtns)
  card.appendChild(foot)

  return card
}

function renderLogRow(e: DiskLogEntry, labels: Record<string, string>): HTMLElement {
  const tr = el('tr')
  tr.style.borderTop = '1px solid rgb(var(--c-line-subtle))'

  // 时间
  const tdTime = el('td', { style: 'padding:6px 8px;white-space:nowrap' })
  tdTime.style.color = 'rgb(var(--c-ink-muted))'
  tdTime.textContent = formatLogTime(e.time)
  tr.appendChild(tdTime)

  // 硬盘组
  const tdGroup = el('td', { style: 'padding:6px 8px;white-space:nowrap' })
  tdGroup.style.color = 'rgb(var(--c-ink))'
  tdGroup.textContent = e.alias || `硬盘组 ${e.groupId}`
  tr.appendChild(tdGroup)

  // 动作
  const tdAction = el('td', { style: 'padding:6px 8px;white-space:nowrap' })
  const actionChip = el('span', { class: 'px-2 py-0.5 rounded-full text-[11px] font-medium' })
  actionChip.style.background = 'rgb(var(--c-primary-soft))'
  actionChip.style.color = 'rgb(var(--c-primary-soft-text))'
  actionChip.textContent = labels[e.action] || e.action
  tdAction.appendChild(actionChip)
  tr.appendChild(tdAction)

  // 结果（后端返回 ok / fail）
  const tdRes = el('td', { style: 'padding:6px 8px;white-space:nowrap' })
  const ok = e.result === 'ok'
  const resChip = el('span', { class: 'px-2 py-0.5 rounded-full text-[11px] font-medium' })
  if (ok) {
    resChip.style.background = 'rgb(var(--c-success-soft))'
    resChip.style.color = 'rgb(var(--c-success-soft-text))'
    resChip.textContent = '成功'
  } else {
    resChip.style.background = 'rgb(var(--c-danger-soft))'
    resChip.style.color = 'rgb(var(--c-danger-soft-text))'
    resChip.textContent = '失败'
  }
  tdRes.appendChild(resChip)
  tr.appendChild(tdRes)

  // 备注（动作原因/错误信息；成功时也有备注主体，如自动执行/按钮关闭）
  const tdErr = el('td', { style: 'padding:6px 8px' })
  if (ok) {
    if (e.remark) {
      tdErr.style.color = 'rgb(var(--c-ink-muted))'
      tdErr.textContent = e.remark
    } else {
      tdErr.style.color = 'rgb(var(--c-ink-subtle))'
      tdErr.textContent = '—'
    }
  } else {
    tdErr.style.color = 'rgb(var(--c-danger))'
    tdErr.textContent = e.remark || '操作失败'
  }
  tr.appendChild(tdErr)

  return tr
}

/** 格式化日志时间：2025-01-15T08:30:00Z → 01-15 08:30:00 */
function formatLogTime(iso: string): string {
  try {
    const d = new Date(iso)
    const mm = String(d.getMonth() + 1).padStart(2, '0')
    const dd = String(d.getDate()).padStart(2, '0')
    const hh = String(d.getHours()).padStart(2, '0')
    const mi = String(d.getMinutes()).padStart(2, '0')
    const ss = String(d.getSeconds()).padStart(2, '0')
    return `${mm}-${dd} ${hh}:${mi}:${ss}`
  } catch {
    return iso
  }
}

// 供 main.ts 在路由离开时清理
export function resetDisksState(): void {
  ui.groupBusy = {}
  logUi.page = 1
  logUi.pageSize = 20
  logUi.groupId = -1
  logUi.action = ''
  logUi.from = ''
  logUi.to = ''
  logUi.result = null
  logUi.fetching = false
  logUi.lastSig = ''
}
