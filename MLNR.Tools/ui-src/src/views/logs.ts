// 日志面板（需求 §12）：BLE 通信 / 指令收发 / 硬件错误 / 心跳事件 / 控制流程
// - API 拉取 + WS 实时推送合并展示
// - 级别过滤 / 自动滚动 / 暂停 / CSV 导出
// - 运行日志配置（级别 + 单文件上限）
// #24：二进制帧日志解码文本显示（hex 灰色折叠，解码文本正常显示）
// #25：日志倒序分页显示（最新在最上，每页 100 条）

import { store } from '../store'
import { el, svgIcon, toast, formatTime } from '../ui'
import { pageContainer, pageTitle, downloadCSV } from './shared'
import type { LogEntry } from '../types'

// ===== 面板状态（跨渲染保留） =====
let levelFilter = ''        // ''=全部
// 查看状态：用户向下滚动离开顶部后进入，WS 自动推送停止渲染、内容静止；
// 滚回顶部或点击右上角回到底部按钮退出，恢复自动跟随最新。
let viewingHistory = false
// 防抖定时器：滚动停止 200ms 后才判定进入/退出查看状态（避免惯性滚动中途误判）
let scrollSettleTimer: number | undefined
// #需求4：用户手动滚动离开顶部（最新日志）后，WS 推送重渲染时不再强制回顶
let userScrolledAway = false
let logConfig: { level: string; maxSizeMB: number; keepDays: number } | null = null
let configLoaded = false
// 草稿：跨渲染保留用户未保存的编辑
let draft: { level: string; maxSize: string } | null = null

// #25：分页状态
let currentPage = 1
const PAGE_SIZE = 100
// #24：展开的原始二进制行索引集合（按日志唯一标识）
let expandedHexRows = new Set<string>()
// Fix：日志列表内部滚动位置（跨整页重建保留）——
// WS 推送（state_update/thermal_update 等）会触发 store.subscribe 全局重渲染，
// 整页重建生成全新列表元素 scrollTop 归零，会把正在查看历史的用户强制拉回顶部。
let savedScrollTop = 0
// 回到顶部/刷新/清空等回到最新操作的意图标志：本次渲染强制滚动到顶部，
// 并跳过渲染前从旧 DOM 捕获滚动位置（否则会把位置拉回点击前的历史位置）
let jumpToLatest = false

// 供 main.ts 门控使用：仅第一页且未查看历史的自动跟随状态才随 WS 推送实时渲染；
// 查看历史或翻页时内容静止（主动操作仍通过 rerender 事件渲染）。
export function isLogAutoFollow(): boolean {
  return currentPage === 1 && !viewingHistory
}

// 自动跟随状态的实时轮询：仅第一页且未查看历史时拉取最新日志（第一页实时更新最新条目）。
// 查看历史 / 翻页时停止轮询，内容静止。
let pollTimer: number | undefined
const POLL_INTERVAL = 3000
function startPolling(): void {
  if (pollTimer !== undefined) return
  pollTimer = window.setInterval(async () => {
    if (!document.getElementById('log-list-box')) return
    if (viewingHistory || currentPage !== 1) return
    try {
      await store.loadLogs(1000, levelFilter || undefined)
    } catch {
      // 轮询失败静默，下一轮重试
    }
  }, POLL_INTERVAL)
}
function stopPolling(): void {
  if (pollTimer !== undefined) {
    clearInterval(pollTimer)
    pollTimer = undefined
  }
}

export function resetLogsState(): void {
  stopPolling()
  jumpToLatest = false
  viewingHistory = false
  userScrolledAway = false
  if (scrollSettleTimer !== undefined) {
    clearTimeout(scrollSettleTimer)
    scrollSettleTimer = undefined
  }
  draft = null
  currentPage = 1
  expandedHexRows = new Set()
  savedScrollTop = 0
}

export function renderLogs(): HTMLElement {
  // 首次进入拉取 1000 条日志用于前端分页（#25）
  if (store.logs.length === 0) void store.loadLogs(1000)
  if (!configLoaded) {
    void store.getLogConfig().then(c => {
      logConfig = c
      configLoaded = true
      draft = c ? { level: c.level, maxSize: String(c.maxSizeMB) } : null
      // Fix：日志页已忽略无关 store 推送，配置加载完成后需显式触发渲染以显示配置卡片
      document.dispatchEvent(new Event('rerender'))
    })
  }

  // Fix：渲染前从当前 DOM 读取日志列表真实滚动位置（重建发生前旧列表仍在文档中），
  // 解决刷新/WS 重建后 savedScrollTop/userScrolledAway 与真实位置脱节导致的强制回顶。
  const oldBox = document.getElementById('log-list-box')
  if (oldBox && !jumpToLatest) {
    savedScrollTop = oldBox.scrollTop
    if (oldBox.scrollTop > 8) userScrolledAway = true
    else if (oldBox.scrollTop <= 2) userScrolledAway = false
  }

  const wrap = pageContainer([])
  wrap.appendChild(pageTitle('运行日志'))

  // ===== 工具栏 =====
  const bar = el('div', { class: 'card p-3 mb-3 flex flex-wrap items-center gap-2' })

  // 级别过滤
  const levelSelect = el('select', { class: 'input', style: 'width:130px' }) as HTMLSelectElement
  levelSelect.appendChild(el('option', { value: '' }, ['全部级别']))
  for (const lv of ['DEBUG', 'INFO', 'WARN', 'ERROR']) {
    levelSelect.appendChild(el('option', { value: lv }, [lv]))
  }
  levelSelect.value = levelFilter
  levelSelect.onchange = () => {
    levelFilter = levelSelect.value
    currentPage = 1 // 切换过滤时重置到第一页
    document.dispatchEvent(new Event('rerender'))
  }

  // 条数显示
  const allLogs = filtered()
  const totalPages = Math.max(1, Math.ceil(allLogs.length / PAGE_SIZE))
  if (currentPage > totalPages) currentPage = totalPages
  const count = el('span', { class: 'text-xs' }, [`共 ${allLogs.length} 条`])
  count.style.color = 'rgb(var(--c-ink-subtle))'

  // 刷新
  const refreshBtn = el('button', { class: 'btn btn-sm' }, [svgIcon('refresh', 13), ' 刷新'])
  refreshBtn.onclick = async () => {
    refreshBtn.disabled = true
    // 刷新语义=获取最新：退出查看状态并回到最新
    clearTimeout(scrollSettleTimer)
    scrollSettleTimer = undefined
    viewingHistory = false
    userScrolledAway = false
    jumpToLatest = true
    await store.loadLogs(1000, levelFilter || undefined)
    refreshBtn.disabled = false
    currentPage = 1
    document.dispatchEvent(new Event('rerender'))
  }

  // 导出 CSV
  const exportBtn = el('button', { class: 'btn btn-sm' }, [svgIcon('save', 13), ' 导出 CSV'])
  exportBtn.onclick = () => {
    const rows: Array<Array<string | number>> = [['时间', '级别', '模块', '内容']]
    for (const l of allLogs) {
      rows.push([formatTime(l.time), l.level, l.module, l.message])
    }
    downloadCSV(`mlnr-logs-${Date.now()}.csv`, rows)
  }

  bar.append(levelSelect, count, refreshBtn, exportBtn)
  wrap.appendChild(bar)

  // ===== 日志列表 =====
  const list = el('div', { class: 'card p-0 overflow-hidden relative' })
  const box = el('div', {
    id: 'log-list-box',
    class: 'overflow-y-auto px-3 py-2 font-mono',
    style: 'height: calc(100vh - 380px); min-height: 300px; font-size: 12px;',
  })
  // 查看状态下出现在右上角的回到顶部按钮（直接操作 DOM 显隐，不依赖重渲染）
  const jumpBtn = el('button', {
    class: 'btn btn-primary absolute top-2 right-2',
    style: 'display:none; z-index:20; box-shadow: 0 2px 8px rgb(0 0 0 / 0.3); font-size: 16px;',
  }, [svgIcon('chevron-up', 14), ' 回到顶部'])
  jumpBtn.onclick = () => {
    clearTimeout(scrollSettleTimer)
    scrollSettleTimer = undefined
    viewingHistory = false
    userScrolledAway = false
    jumpToLatest = true
    jumpBtn.style.display = 'none'
    document.dispatchEvent(new Event('rerender'))
  }

  if (allLogs.length === 0) {
    const empty = el('div', { class: 'py-8 text-center' }, ['暂无日志'])
    empty.style.color = 'rgb(var(--c-ink-subtle))'
    box.appendChild(empty)
  } else {
    // #25：倒序显示（最新在最上），按当前页切片
    const startIdx = (currentPage - 1) * PAGE_SIZE
    const pageLogs = allLogs.slice(startIdx, startIdx + PAGE_SIZE)

    for (const l of pageLogs) {
      box.appendChild(renderLogRow(l))
    }
  }
  list.append(box, jumpBtn)
  wrap.appendChild(list)

  // #25：分页控制栏
  const pager = el('div', { class: 'card p-3 mt-3 flex items-center justify-between' })
  const pageInfo = el('span', { class: 'text-xs' },
    [`第 ${currentPage} / ${totalPages} 页 · 每页 ${PAGE_SIZE} 条`])
  pageInfo.style.color = 'rgb(var(--c-ink-subtle))'

  const pagerBtns = el('div', { class: 'flex gap-2' })
  const prevBtn = el('button', { class: 'btn btn-sm' }, [svgIcon('back', 12), ' 上一页'])
  prevBtn.disabled = currentPage <= 1
  prevBtn.onclick = () => {
    if (currentPage > 1) {
      currentPage--
      document.dispatchEvent(new Event('rerender'))
    }
  }
  const nextBtn = el('button', { class: 'btn btn-sm' }, ['下一页'])
  nextBtn.disabled = currentPage >= totalPages
  nextBtn.onclick = () => {
    if (currentPage < totalPages) {
      currentPage++
      document.dispatchEvent(new Event('rerender'))
    }
  }
  pagerBtns.append(prevBtn, nextBtn)
  pager.append(pageInfo, pagerBtns)
  wrap.appendChild(pager)

  // 跟踪用户滚动：离开顶部视为查看历史，重渲染时不再强制回顶；滚回顶部恢复自动跟随。
  // 防抖：滚动停止 200ms 后才进入/退出查看状态（进入→内容静止+显示回到底部按钮；
  // 手动滚回顶部→退出并刷新到最新）。box.isConnected 防止重建后旧定时器误触发。
  box.onscroll = () => {
    savedScrollTop = box.scrollTop
    if (box.scrollTop > 8) userScrolledAway = true
    else if (box.scrollTop <= 2) userScrolledAway = false
    clearTimeout(scrollSettleTimer)
    scrollSettleTimer = window.setTimeout(() => {
      if (!box.isConnected) return
      if (box.scrollTop > 8) {
        if (!viewingHistory) {
          viewingHistory = true
          stopPolling() // 查看历史：停止实时轮询
          if (currentPage === 1) {
            jumpBtn.style.display = ''
            // toast('正在查看历史日志，点击右上角回到顶部按钮回到最新', 'info')
          }
        }
      } else if (box.scrollTop <= 2) {
        const wasViewing = viewingHistory
        viewingHistory = false
        jumpBtn.style.display = 'none'
        if (wasViewing) {
          // 手动滚回顶部：退出查看状态并刷新内容到最新
          document.dispatchEvent(new Event('rerender'))
        }
      }
    }, 200)
  }

  // 滚动位置处理：整页重建会产生全新列表元素（scrollTop 归零），若不处理会被强制拉回顶部。
  // - 自动跟随模式（第一页 + 未查看历史）：保持在顶部（最新条目）
  // - 用户正在查看历史 / 查看状态：恢复重建前的滚动位置
  if (jumpToLatest || (currentPage === 1 && !viewingHistory && !userScrolledAway)) {
    jumpToLatest = false
    requestAnimationFrame(() => { box.scrollTop = 0 })
  } else {
    requestAnimationFrame(() => { box.scrollTop = savedScrollTop })
  }

  // 自动跟随（第一页 + 未查看历史）才实时轮询最新日志；翻页/查看历史停止轮询
  if (currentPage === 1 && !viewingHistory) startPolling()
  else stopPolling()

  // ===== 日志配置卡片 =====
  if (logConfig) {
    wrap.appendChild(renderLogConfig())
  }

  return wrap
}

// ================================================================
// #24：渲染单行日志（支持二进制帧解码文本分离显示）
// ================================================================
function renderLogRow(l: LogEntry): HTMLElement {
  const row = el('div', { class: 'flex gap-2 py-0.5 border-b', style: 'border-color: rgb(var(--c-line) / 0.4);' })
  const time = el('span', { class: 'shrink-0' }, [formatTime(l.time)])
  time.style.color = 'rgb(var(--c-ink-subtle))'
  const level = el('span', { class: 'shrink-0 w-12 font-semibold' }, [l.level])
  level.style.color = LEVEL_COLOR[l.level] ?? 'rgb(var(--c-ink-muted))'
  const module = el('span', { class: 'shrink-0 w-20 truncate' }, [l.module])
  module.style.color = 'rgb(var(--c-accent))'

  // #24：检测是否为二进制帧日志（包含 | 分隔符）
  const pipeIdx = l.message.indexOf('|')
  const isBinaryFrame = pipeIdx !== -1
  // 检测是否包含"(解析失败)"
  const isParseFailed = l.message.includes('(解析失败)') || l.message.includes('解析失败')

  if (isParseFailed) {
    // 解析失败的日志行整体标红
    row.style.background = 'rgb(var(--c-danger) / 0.08)'
  }

  if (isBinaryFrame) {
    // #24：二进制帧日志 — hex 部分折叠，解码文本正常显示
    const hexPart = l.message.slice(0, pipeIdx).trim()
    const decodedPart = l.message.slice(pipeIdx + 1).trim()
    const rowKey = `${l.time}_${l.module}_${hexPart.slice(0, 20)}`
    const isExpanded = expandedHexRows.has(rowKey)

    const msgBox = el('span', { class: 'flex-1 break-all' })

    // 解码文本（正常颜色显示）
    const decodedSpan = el('span', {})
    decodedSpan.style.color = isParseFailed ? 'rgb(239,68,68)' : 'rgb(var(--c-ink))'
    decodedSpan.textContent = decodedPart
    msgBox.appendChild(decodedSpan)

    // 展开/折叠切换按钮
    const toggleBtn = el('span', {
      class: 'cursor-pointer ml-1 text-xs',
      style: 'color: rgb(var(--c-ink-subtle)); user-select: none;',
    })
    toggleBtn.textContent = isExpanded ? ' [折叠hex]' : ' [展开hex]'
    toggleBtn.onclick = (e) => {
      e.stopPropagation()
      if (expandedHexRows.has(rowKey)) {
        expandedHexRows.delete(rowKey)
      } else {
        expandedHexRows.add(rowKey)
      }
      document.dispatchEvent(new Event('rerender'))
    }
    msgBox.appendChild(toggleBtn)

    // 原始二进制 hex 部分（灰色/小字，默认折叠）
    if (isExpanded) {
      const hexSpan = el('div', { class: 'text-xs mt-0.5 pl-2' })
      hexSpan.style.color = 'rgb(var(--c-ink-subtle))'
      hexSpan.style.opacity = '0.7'
      hexSpan.textContent = hexPart
      msgBox.appendChild(hexSpan)
    }

    row.append(time, level, module, msgBox)
  } else {
    // 非二进制帧日志 — 正常显示
    const msg = el('span', { class: 'flex-1 break-all' }, [l.message])
    msg.style.color = isParseFailed ? 'rgb(239,68,68)' : 'rgb(var(--c-ink))'
    row.append(time, level, module, msg)
  }

  return row
}

function renderLogConfig(): HTMLElement {
  const card = el('div', { class: 'card p-4 mt-3' })
  const title = el('div', { class: 'flex items-center gap-2 mb-2' })
  title.append(svgIcon('settings', 15))
  const t = el('span', { class: 'text-sm font-semibold' }, ['日志配置（后端 logrotate 参数）'])
  t.style.color = 'rgb(var(--c-ink))'
  title.appendChild(t)
  card.appendChild(title)
  const hint = el('div', { class: 'text-xs mb-3' },
    [`当前：级别 ${logConfig!.level} · 单文件上限 ${logConfig!.maxSizeMB}MB · 保留 ${logConfig!.keepDays} 天。修改级别与单文件上限后点击保存。`])
  hint.style.color = 'rgb(var(--c-ink-subtle))'
  card.appendChild(hint)

  const row = el('div', { class: 'form-row' })
  const label = el('div', { class: 'form-label' }, ['日志级别 / 单文件上限'])
  row.appendChild(label)
  const cw = el('div', { class: 'flex items-center gap-3 flex-1' })
  if (!draft) draft = { level: logConfig!.level, maxSize: String(logConfig!.maxSizeMB) }
  const levelSel = el('select', { class: 'input', style: 'width:130px' }) as HTMLSelectElement
  for (const lv of ['DEBUG', 'INFO', 'WARN', 'ERROR']) {
    levelSel.appendChild(el('option', { value: lv }, [lv]))
  }
  levelSel.value = draft.level
  levelSel.onchange = () => { draft!.level = levelSel.value }
  const sizeInput = el('input', {
    type: 'number', class: 'input', style: 'width:110px',
    min: '1', max: '100', value: draft.maxSize,
  }) as HTMLInputElement
  sizeInput.oninput = () => { draft!.maxSize = sizeInput.value }
  const mbLabel = el('span', { class: 'text-xs' }, ['MB'])
  mbLabel.style.color = 'rgb(var(--c-ink-subtle))'
  const saveBtn = el('button', { class: 'btn btn-primary btn-sm' }, [svgIcon('save', 13), ' 保存'])
  saveBtn.onclick = async () => {
    const size = Number(sizeInput.value)
    if (!Number.isFinite(size) || size < 1 || size > 100) {
      toast('单文件上限取值 1-100 MB', 'error')
      return
    }
    saveBtn.disabled = true
    const r = await store.saveLogConfig(levelSel.value, Math.round(size))
    saveBtn.disabled = false
    if (r) {
      logConfig = r
      draft = { level: r.level, maxSize: String(r.maxSizeMB) }
      toast('日志配置已保存', 'success')
      document.dispatchEvent(new Event('rerender'))
    } else {
      toast('保存失败，请重试', 'error')
    }
  }
  cw.append(levelSel, sizeInput, mbLabel, saveBtn)
  row.appendChild(cw)
  card.appendChild(row)
  return card
}

// #25：过滤后按时间倒序排列（最新在最上）
function filtered(): LogEntry[] {
  let logs = store.logs
  if (levelFilter) {
    logs = logs.filter(l => l.level === levelFilter)
  }
  // 按时间倒序：新的在前
  return [...logs].sort((a, b) => {
    const ta = new Date(a.time).getTime()
    const tb = new Date(b.time).getTime()
    return tb - ta
  })
}

const LEVEL_COLOR: Record<string, string> = {
  DEBUG: 'rgb(var(--c-ink-subtle))',
  INFO: 'rgb(var(--c-success))',
  WARN: 'rgb(var(--c-warning))',
  ERROR: 'rgb(var(--c-danger))',
}
