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
let paused = false          // 暂停实时滚动
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

export function resetLogsState(): void {
  paused = false
  userScrolledAway = false
  draft = null
  currentPage = 1
  expandedHexRows = new Set()
}

export function renderLogs(): HTMLElement {
  // 首次进入拉取 1000 条日志用于前端分页（#25）
  if (store.logs.length === 0) void store.loadLogs(1000)
  if (!configLoaded) {
    void store.getLogConfig().then(c => {
      logConfig = c
      configLoaded = true
      draft = c ? { level: c.level, maxSize: String(c.maxSizeMB) } : null
    })
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

  // 自动滚动开关
  const scrollWrap = el('label', { class: 'toggle', title: '新日志到达时自动滚动到底部' })
  const scrollInput = el('input', { type: 'checkbox' }) as HTMLInputElement
  scrollInput.checked = !paused
  scrollInput.onchange = () => {
    paused = !scrollInput.checked
    toast(paused ? '已暂停自动滚动' : '已恢复自动滚动', 'info')
  }
  scrollWrap.append(scrollInput, el('span', { class: 'toggle-slider' }))
  const scrollLabel = el('span', { class: 'text-xs' }, ['自动滚动'])
  scrollLabel.style.color = 'rgb(var(--c-ink-muted))'

  // 刷新
  const refreshBtn = el('button', { class: 'btn btn-sm' }, [svgIcon('refresh', 13), ' 刷新'])
  refreshBtn.onclick = async () => {
    refreshBtn.disabled = true
    await store.loadLogs(1000, levelFilter || undefined)
    refreshBtn.disabled = false
    currentPage = 1
    document.dispatchEvent(new Event('rerender'))
  }

  // 清空显示（仅本地缓冲）
  const clearBtn = el('button', { class: 'btn btn-sm' }, [svgIcon('trash', 13), ' 清空显示'])
  clearBtn.onclick = () => {
    store.logs = []
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

  bar.append(levelSelect, count, scrollWrap, scrollLabel, refreshBtn, clearBtn, exportBtn)
  wrap.appendChild(bar)

  // ===== 日志列表 =====
  const list = el('div', { class: 'card p-0 overflow-hidden' })
  const box = el('div', {
    class: 'overflow-y-auto px-3 py-2 font-mono',
    style: 'height: calc(100vh - 380px); min-height: 300px; font-size: 12px;',
  })

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
  list.appendChild(box)
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

  // #需求4：跟踪用户滚动位置——离开顶部视为查看历史，重渲染时不再强制回顶；滚回顶部恢复自动跟随
  box.onscroll = () => {
    if (box.scrollTop > 8) userScrolledAway = true
    else if (box.scrollTop <= 2) userScrolledAway = false
  }

  // 自动滚动到底部（仅在第一页、未暂停、且用户未手动查看历史时）
  if (!paused && currentPage === 1 && !userScrolledAway) {
    requestAnimationFrame(() => { box.scrollTop = 0 })
  }

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
