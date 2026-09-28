// 定时计划执行日志页（M4）
// - 筛选：任务名称 / 类别 / 触发方式 / 结果 / 执行时间范围
// - 分页表格 + 保留天数配置（1/7/30 天）

import { api } from '../api'
import { el, svgIcon, toast } from '../ui'
import { pageContainer, pageTitle, badge } from './shared'
import type { ScheduleLogQueryResult } from '../types'

const PAGE_SIZE = 20

// ===== 跨渲染状态 =====
interface LogFilter {
  name: string
  triggerType: string
  type: string
  result: string
  from: string
  to: string
}
let filter: LogFilter = { name: '', triggerType: '', type: '', result: '', from: '', to: '' }
let page = 1
let data: ScheduleLogQueryResult | null = null
let loading = false

export function resetScheduleLogsState(): void {
  filter = { name: '', triggerType: '', type: '', result: '', from: '', to: '' }
  page = 1
  data = null
  loading = false
}

async function loadLogs(): Promise<void> {
  if (loading) return
  loading = true
  try {
    data = await api.getScheduleLogs({
      name: filter.name || undefined,
      triggerType: filter.triggerType || undefined,
      type: filter.type || undefined,
      result: filter.result || undefined,
      from: toISO(filter.from),
      to: toISO(filter.to),
      page,
      pageSize: PAGE_SIZE,
    })
  } catch (err) {
    toast(`加载日志失败：${(err as Error).message}`, 'error')
  } finally {
    loading = false
  }
}

function toISO(v: string): string | undefined {
  return v ? new Date(v).toISOString() : undefined
}

export function renderScheduleLogs(): HTMLElement {
  void loadLogs()

  const wrap = pageContainer([])
  const back = el('button', { class: 'btn btn-sm mb-3' }, [svgIcon('back', 12), ' 返回任务列表'])
  back.onclick = () => { location.hash = '#/schedules' }
  wrap.appendChild(back)
  wrap.appendChild(pageTitle('执行日志'))

  // ===== 筛选工具栏 =====
  const bar = el('div', { class: 'card p-3 mb-3' })
  const row1 = el('div', { class: 'flex flex-wrap items-end gap-3' })

  const nameW = el('div', {})
  const nameL = el('div', { class: 'text-xs mb-1' }, ['任务名称'])
  nameL.style.color = 'rgb(var(--c-ink-muted))'
  const nameI = el('input', { class: 'input', style: 'width:160px;', value: filter.name, placeholder: '模糊匹配' }) as HTMLInputElement
  nameI.oninput = () => { filter.name = nameI.value }
  nameW.append(nameL, nameI)

  const sel = (label: string, key: keyof LogFilter, labels: Record<string, string>, style: string) => {
    const w = el('div', {})
    const l = el('div', { class: 'text-xs mb-1' }, [label])
    l.style.color = 'rgb(var(--c-ink-muted))'
    const s = el('select', { class: 'input', style }) as HTMLSelectElement
    for (const [k, v] of Object.entries(labels)) {
      s.appendChild(el('option', { value: k }, [v]))
    }
    s.value = filter[key] || ''
    s.onchange = () => { filter[key] = s.value }
    w.append(l, s)
    return w
  }

  // 触发类型（修订版：定时事件 / 日志事件 / 监控事件 / 手动触发）
  const ttLabels = data?.triggerTypeLabels ?? {
    '': '全部', time: '定时事件', log: '日志事件', monitor: '监控事件', manual: '手动触发',
  }
  const typeLabels = data?.typeLabels ?? {
    '': '全部', one_time: '一次性', daily: '每天', weekly: '每周', monthly: '每月', trigger: '触发任务',
  }
  const resultLabels = data?.resultLabels ?? { '': '全部', success: '成功', failed: '失败' }

  const timeW = el('div', { class: 'flex items-end gap-2' })
  const fromL = el('div', { class: 'text-xs mb-1' }, ['开始时间'])
  fromL.style.color = 'rgb(var(--c-ink-muted))'
  const fromI = el('input', { type: 'datetime-local', class: 'input', style: 'width:170px;', value: filter.from }) as HTMLInputElement
  fromI.oninput = () => { filter.from = fromI.value }
  const toL = el('div', { class: 'text-xs mb-1' }, ['结束时间'])
  toL.style.color = 'rgb(var(--c-ink-muted))'
  const toI = el('input', { type: 'datetime-local', class: 'input', style: 'width:170px;', value: filter.to }) as HTMLInputElement
  toI.oninput = () => { filter.to = toI.value }
  timeW.append(el('div', {}, [fromL, fromI]), el('div', {}, [toL, toI]))

  const btns = el('div', { class: 'flex gap-2' })
  const searchBtn = el('button', { class: 'btn btn-primary btn-sm' }, [svgIcon('refresh', 13), ' 筛选'])
  searchBtn.onclick = () => {
    page = 1
    data = null
    document.dispatchEvent(new Event('rerender'))
  }
  const resetBtn = el('button', { class: 'btn btn-sm' }, ['重置'])
  resetBtn.onclick = () => {
    filter = { name: '', triggerType: '', type: '', result: '', from: '', to: '' }
    page = 1
    data = null
    document.dispatchEvent(new Event('rerender'))
  }
  btns.append(searchBtn, resetBtn)

  row1.append(nameW, sel('触发类型', 'triggerType', ttLabels, 'width:120px;'), sel('触发方式', 'type', typeLabels, 'width:120px;'), sel('结果', 'result', resultLabels, 'width:110px;'), timeW, btns)
  bar.appendChild(row1)
  wrap.appendChild(bar)

  // ===== 日志表格 =====
  const tableCard = el('div', { class: 'card p-0 overflow-hidden' })
  const rows = data?.items ?? []
  if (rows.length === 0) {
    const empty = el('div', { class: 'py-10 text-center' })
    empty.style.color = 'rgb(var(--c-ink-subtle))'
    empty.textContent = data === null ? '加载中…' : '暂无执行日志'
    tableCard.appendChild(empty)
  } else {
    const table = el('table', { class: 'w-full text-sm' })
    table.style.borderCollapse = 'collapse'
    const thead = el('thead', {})
    const hr = el('tr', {})
    hr.style.background = 'rgb(var(--c-element))'
    for (const h of ['时间', '任务名称', '触发类型', '触发方式', '通道', '结果', '详情']) {
      const th = el('th', { class: 'text-left px-3 py-2 font-medium whitespace-nowrap' })
      th.style.color = 'rgb(var(--c-ink-muted))'
      th.textContent = h
      hr.appendChild(th)
    }
    thead.appendChild(hr)
    table.appendChild(thead)

    const tbody = el('tbody', {})
    for (const l of rows) {
      const tr = el('tr', {})
      tr.style.borderTop = '1px solid rgb(var(--c-line) / 0.5)'
      const td = (content: HTMLElement | string, cls = '') => {
        const c = el('td', { class: `px-3 py-2 align-top ${cls}` })
        if (typeof content === 'string') c.textContent = content
        else c.appendChild(content)
        return c
      }
      const time = el('span', { class: 'whitespace-nowrap text-xs' })
      time.style.color = 'rgb(var(--c-ink-subtle))'
      time.textContent = new Date(l.time).toLocaleString()
      tr.appendChild(td(time))
      tr.appendChild(td(l.taskName, 'whitespace-nowrap'))
      const ttLabel = l.triggerType
        ? (ttLabels[l.triggerType] ?? l.triggerType)
        : (l.sourceScheduleId ? '被调用' : '—')
      tr.appendChild(td(badge(ttLabel, l.triggerType ? '--c-accent' : '--c-neutral', true)))
      tr.appendChild(td(badge(typeLabels[l.scheduleType] ?? l.scheduleType, '--c-primary-soft-text', true)))
      tr.appendChild(td(l.channels || '—', 'whitespace-nowrap'))
      tr.appendChild(td(badge(resultLabels[l.result] ?? l.result, l.result === 'success' ? '--c-success' : '--c-danger', true)))
      const det = el('span', { class: 'text-xs' }, [l.detail || '—'])
      det.style.color = l.result === 'failed' ? 'rgb(var(--c-danger))' : 'rgb(var(--c-ink-muted))'
      tr.appendChild(td(det))
      tbody.appendChild(tr)
    }
    table.appendChild(tbody)
    tableCard.appendChild(table)
  }
  wrap.appendChild(tableCard)

  // ===== 分页 + 保留天数 =====
  const footer = el('div', { class: 'card p-3 mt-3 flex flex-wrap items-center justify-between gap-3' })
  const totalPages = data ? Math.max(1, Math.ceil(data.total / data.size)) : 1
  if (page > totalPages) page = totalPages
  const info = el('span', { class: 'text-xs' })
  info.style.color = 'rgb(var(--c-ink-subtle))'
  info.textContent = data ? `共 ${data.total} 条 · 第 ${page} / ${totalPages} 页` : '加载中…'
  footer.appendChild(info)

  const pager = el('div', { class: 'flex items-center gap-2' })
  const prev = el('button', { class: 'btn btn-sm' }, ['上一页'])
  prev.disabled = !data || page <= 1
  prev.onclick = () => { if (page > 1) { page--; data = null; document.dispatchEvent(new Event('rerender')) } }
  const next = el('button', { class: 'btn btn-sm' }, ['下一页'])
  next.disabled = !data || page >= totalPages
  next.onclick = () => { if (page < totalPages) { page++; data = null; document.dispatchEvent(new Event('rerender')) } }
  pager.append(prev, next)
  footer.appendChild(pager)

  const retainW = el('div', { class: 'flex items-center gap-2' })
  const rl = el('span', { class: 'text-xs' }, ['保留'])
  rl.style.color = 'rgb(var(--c-ink-muted))'
  const retainSel = el('select', { class: 'input', style: 'width:100px;' }) as HTMLSelectElement
  for (const d of [1, 7, 30]) retainSel.appendChild(el('option', { value: String(d) }, [`${d} 天`]))
  if (data) retainSel.value = String(data.retainDays)
  const saveRetain = el('button', { class: 'btn btn-sm' }, ['保存'])
  saveRetain.onclick = async () => {
    saveRetain.disabled = true
    try {
      const r = await api.updateScheduleLogConfig(Number(retainSel.value))
      toast(`日志保留天数已设为 ${r.retainDays} 天`, 'success')
      data = null
    } catch (err) {
      toast(`保存失败：${(err as Error).message}`, 'error')
    }
    saveRetain.disabled = false
    document.dispatchEvent(new Event('rerender'))
  }
  retainW.append(rl, retainSel, saveRetain)
  footer.appendChild(retainW)

  wrap.appendChild(footer)
  return wrap
}
