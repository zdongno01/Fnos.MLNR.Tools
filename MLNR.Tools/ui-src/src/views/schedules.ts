// 定时计划（修订版 M9~M14）
// 四标签页：计划任务 / 监控脚本 / 执行脚本 / 日志事件
// 计划任务表单：触发器清单（可空）+ 执行器清单（至少 1），全部二级弹窗配置
import { api } from '../api'
import { el, svgIcon, toast, confirmDialog, tabs, statusBadge, unsavedChoice } from '../ui'
import { pageTitle, pageContainer, badge } from './shared'
import { renderScheduleResourcesPage, closeResModalIfOpen } from './schedule-resources'
import type {
  Schedule, ScheduleTrigger, ScheduleExecutor, MonitorScript, ExecScript, LogEventResource,
  ScheduleTriggerType, TriggerPeriod, ScheduleExecutorType, FanCurveMode, DiskAction, TaskAction, CooldownUnit,
} from '../types'

// ===== 跨渲染状态 =====
let schedules: Schedule[] = []
let monitorScripts: MonitorScript[] = []
let execScripts: ExecScript[] = []
let logEvents: LogEventResource[] = []
let loadPromise: Promise<void> | null = null
let fillToken = 0
let activeTab: 'tasks' | 'monitor' | 'exec' | 'logevent' = 'tasks'
let dirty = false

export function resetSchedulesState(): void {
  schedules = []
  monitorScripts = []
  execScripts = []
  logEvents = []
  loadPromise = null
  activeTab = 'tasks'
  dirty = false
  fillToken++
}

async function loadAll(force = false): Promise<void> {
  if (loadPromise && !force) return loadPromise
  loadPromise = (async () => {
    try {
      const [s, m, e, l] = await Promise.all([
        api.getSchedules(),
        api.getMonitorScripts(),
        api.getExecScripts(),
        api.getLogEvents(),
      ])
      schedules = s.schedules ?? []
      monitorScripts = m.monitorScripts ?? []
      execScripts = e.execScripts ?? []
      logEvents = l.logEvents ?? []
    } catch (err) {
      toast('加载定时计划数据失败：' + (err instanceof Error ? err.message : String(err)), 'error')
    }
  })()
  return loadPromise
}

const WEEK_LABEL = ['周一', '周二', '周三', '周四', '周五', '周六', '周日']

// ================================================================
// 名称映射（真实名称优先，资源未加载时回退编号）
// ================================================================
function fanName(id: number | null | undefined): string {
  return id === undefined ? '—' : `FAN${id}`
}
function diskGroupName(id: number | null | undefined): string {
  return id === undefined ? '—' : `组${id}`
}
function logEventName(id: number | null | undefined): string {
  if (id === undefined || id === null) return '—'
  const r = logEvents.find(x => x.id === id)
  return r ? r.name : `日志事件 #${id}`
}
function monitorScriptName(id: number | null | undefined): string {
  if (id === undefined) return '—'
  const r = monitorScripts.find(x => x.id === id)
  return r ? r.name : `监控脚本 #${id}`
}
function execScriptName(id: number | null | undefined): string {
  if (id === undefined) return '—'
  const r = execScripts.find(x => x.id === id)
  return r ? r.name : `执行脚本 #${id}`
}
function taskName(id: number | null | undefined): string {
  if (id === undefined) return '—'
  const r = schedules.find(x => x.id === id)
  return r ? r.name : `任务 #${id}`
}
function fanCurveLabel(c: FanCurveMode | undefined): string {
  if (c === 'efficient') return '高效'
  if (c === 'daily') return '日常'
  if (c === 'quiet') return '静音'
  return '—'
}

// ================================================================
// 触发器 / 执行器 摘要
// ================================================================
function triggerSummary(t: ScheduleTrigger): string {
  if (t.type === 'time') {
    switch (t.period) {
      case 'once': return `一次性 · ${t.runAt ? new Date(t.runAt).toLocaleString('zh-CN', { hour12: false }) : '—'}`
      case 'daily': return `每天 ${t.time || '—'}`
      case 'weekly': return `每周${(t.weekdays || []).map(d => WEEK_LABEL[d - 1]).join('/')} ${t.time || '—'}`
      case 'monthly': return `每月${(t.monthDays || []).map(d => `${d}日`).join('/')} ${t.time || '—'}`
      case 'loop': return `循环 · 每 ${t.loopInterval || '—'}`
      default: return '定时事件'
    }
  }
  if (t.type === 'log') {
    if (t.logEventId != null) return `日志事件 · ${logEventName(t.logEventId)}`
    if (t.logEventPath && t.logEventRegex) return `日志事件 · 内联（${t.logEventPath}）`
    return '日志事件 · 未配置'
  }
  if (t.type === 'monitor') {
    let base = t.monitorPrebuilt === 'idle' ? '监控硬盘AB闲置（预制）' : monitorScriptName(t.monitorScriptId)
    if (!t.monitorPrebuilt && t.monitorScriptId == null && t.monitorCode) {
      base = '内联' + (t.monitorScriptType === 'python' ? ' · python' : t.monitorScriptType === 'shell' ? ' · sh' : '')
    }
    const args = t.monitorScriptArgs ? ` $ ${t.monitorScriptArgs}` : ''
    return `监控事件 · ${base}${args}（每 ${t.monitorIntervalSec ?? '—'} 秒）`
  }
  return t.type
}

function executorSummary(e: ScheduleExecutor): string {
  const parts: string[] = []
  if (e.delaySec) parts.push(`延迟 ${e.delaySec}s`)
  switch (e.type) {
    case 'fan_control': {
      let s = `风扇 ${fanName(e.fanId)} → `
      if (e.fanEnabled === true) s += '启用'
      else if (e.fanEnabled === false) s += '禁用'
      else s += '不改变开关'
      if (e.fanManual) s += ` · 手动 ${e.fanPercent ?? '—'}%`
      else if (e.fanCurve) s += ` · 自动 ${fanCurveLabel(e.fanCurve)}`
      else s += ' · 自动（曲线维持）'
      parts.push(s)
      break
    }
    case 'disk_group_control': {
      let s = `硬盘 ${diskGroupName(e.diskGroupId)} → ${e.diskAction === 'online' ? '上线' : '下线'}`
      if (e.diskAction === 'offline') {
        if (e.killOccupied) s += ' · 自动终止占用'
        if (e.forceOff) s += ' · 强制'
      }
      if (e.retryEnabled) s += ` · 失败重试×${e.maxRetries ?? 3}`
      parts.push(s)
      break
    }
    case 'control_task': {
      const act = e.taskAction === 'enable' ? '启用' : e.taskAction === 'disable' ? '禁用' : '执行'
      let s = `任务 ${taskName(e.taskId)} → ${act}`
      if (e.respectEnabled) s += ' · 遵循启用'
      if (e.respectTimeRule) s += ' · 遵循时间'
      parts.push(s)
      break
    }
    case 'exec_script': {
      let s = '脚本'
      if (e.scriptId) s += ` ${execScriptName(e.scriptId)}`
      else if (e.scriptCode) s += '（内联）'
      if (e.scriptArgs) s += ` ${e.scriptArgs}`
      if (e.retryEnabled) s += ` · 失败重试×${e.maxRetries ?? 3}`
      parts.push(s)
      break
    }
  }
  if (e.successTaskId) parts.push(`成功→${taskName(e.successTaskId)}`)
  if (e.failureTaskId) parts.push(`失败→${taskName(e.failureTaskId)}`)
  return parts.join(' · ')
}

// 兼容旧任务：后端返回的 triggers/executors 可能为空（后端已迁移，但前端兜底展示）
function effectiveTriggers(sc: Schedule): ScheduleTrigger[] {
  return sc.triggers ?? []
}
function effectiveExecutors(sc: Schedule): ScheduleExecutor[] {
  if (sc.executors && sc.executors.length > 0) return sc.executors
  return []
}

// ================================================================
// 页面主入口（三标签）
// ================================================================
export function renderSchedules(): HTMLElement {
  const root = pageContainer([])
  const content = el('div', { class: 'flex flex-col gap-4' })
  root.appendChild(content)
  const token = ++fillToken
  void loadAll().then(() => {
    if (token !== fillToken) return
    content.innerHTML = ''
    content.appendChild(buildTabPage())
  })
  return root
}

function buildTabPage(): HTMLElement {
  const wrap = el('div', { class: 'flex flex-col gap-4' })
  const titleRow = pageTitle('定时计划')
  // 在标题前加 clock 图标
  const titleLeft = titleRow.firstChild as HTMLElement
  titleLeft.insertBefore(svgIcon('clock', 18), titleLeft.firstChild)
  const logsBtn = el('button', { class: 'btn btn-sm' }, [svgIcon('log', 14), ' 执行日志'])
  logsBtn.onclick = () => { location.hash = '#/schedules/logs' }
  titleRow.querySelector('.flex.items-center.gap-3')?.appendChild(logsBtn)

  const tabBar = tabs(
    [
      { key: 'tasks', label: '计划任务' },
      { key: 'monitor', label: '监控脚本' },
      { key: 'exec', label: '执行脚本' },
      { key: 'logevent', label: '日志事件' },
    ],
    activeTab,
    async (key) => {
      if (key === activeTab) return
      if (!(await closeResModalIfOpen())) return // 资源弹窗未保存：继续编辑/保存失败则停留
      if (dirty) {
        const ch = await unsavedChoice()
        if (ch === 'stay') return
        if (ch === 'save') {
          if (modalDraft && modalOverlay) await saveTaskForm(modalDraft, modalEditSc, modalOverlay)
          if (modalDraft) return // 保存失败，留在当前页
        }
      }
      dirty = false
      activeTab = key as typeof activeTab
      const token = ++fillToken
      void loadAll().then(() => {
        if (token !== fillToken) return
        wrap.innerHTML = ''
        wrap.appendChild(buildTabPage())
      })
    }
  )

  wrap.appendChild(titleRow)
  wrap.appendChild(tabBar)
  if (activeTab === 'tasks') wrap.appendChild(renderTasksTab())
  else wrap.appendChild(renderScheduleResourcesPage(activeTab))
  return wrap
}

// ================================================================
// 计划任务标签页
// ================================================================
function renderTasksTab(): HTMLElement {
  const box = el('div', { class: 'flex flex-col gap-3' })
  const head = el('div', { class: 'flex items-center justify-between' })
  const hint = el('span', { class: 'text-xs' }, [`共 ${schedules.length} 个任务`])
  hint.style.color = 'rgb(var(--c-ink-muted))'
  const add = el('button', { class: 'btn btn-primary btn-sm' }, [svgIcon('plus', 14), ' 新建任务'])
  add.onclick = () => openTaskForm(null)
  head.append(hint, add)
  box.appendChild(head)

  if (schedules.length === 0) {
    const empty = el('div', { class: 'card p-6 text-center text-sm' })
    empty.style.color = 'rgb(var(--c-ink-muted))'
    empty.textContent = '暂无定时任务，点击「新建任务」创建'
    box.appendChild(empty)
  } else {
    for (const sc of [...schedules].sort((a, b) => a.id - b.id)) {
      box.appendChild(renderTaskCard(sc))
    }
  }
  return box
}

function renderTaskCard(sc: Schedule): HTMLElement {
  const card = el('div', { class: 'card p-4' })
  const top = el('div', { class: 'flex items-center justify-between gap-2 mb-2' })
  const left = el('div', { class: 'flex items-center gap-2 flex-wrap min-w-0' })
  const nameEl = el('span', { class: 'font-semibold text-sm truncate' }, [sc.name || `任务 #${sc.id}`])
  nameEl.style.color = 'rgb(var(--c-ink))'
  left.appendChild(nameEl)
  left.appendChild(badge(sc.enabled ? '已启用' : '已停用', sc.enabled ? '--c-success' : '--c-neutral', true))
  if (sc.description) {
    const desc = el('span', { class: 'text-xs truncate' }, [sc.description])
    desc.style.color = 'rgb(var(--c-ink-subtle))'
    left.appendChild(desc)
  }
  top.appendChild(left)

  const ops = el('div', { class: 'flex items-center gap-1 shrink-0' })
  const runBtn = el('button', { class: 'btn btn-sm', title: '手动触发（不消费触发器）' }, [svgIcon('bolt', 13), ' 手动触发'])
  runBtn.onclick = async () => {
    runBtn.disabled = true
    try {
      await api.runSchedule(sc.id)
      toast(`任务「${sc.name}」已投递执行`, 'success')
    } catch (err) {
      toast('触发失败：' + (err instanceof Error ? err.message : String(err)), 'error')
    } finally {
      runBtn.disabled = false
    }
  }
  const editBtn = el('button', { class: 'btn btn-sm', title: '编辑' }, [svgIcon('edit', 13)])
  editBtn.onclick = () => openTaskForm(sc)
  const delBtn = el('button', { class: 'btn btn-sm btn-danger', title: '删除' }, [svgIcon('trash', 13)])
  delBtn.onclick = async () => {
    const ok = await confirmDialog(`确定删除任务「${sc.name}」？该操作不可恢复。`, '删除任务', true)
    if (!ok) return
    try {
      await api.deleteSchedule(sc.id)
      toast('任务已删除', 'success')
      refreshTasks()
    } catch (err) {
      toast('删除失败：' + (err instanceof Error ? err.message : String(err)), 'error')
    }
  }
  ops.append(runBtn, editBtn, delBtn)
  top.appendChild(ops)
  card.appendChild(top)

  // 触发器摘要
  const trigs = effectiveTriggers(sc)
  const trigBlock = el('div', { class: 'text-xs mb-2' })
  trigBlock.style.color = 'rgb(var(--c-ink-muted))'
  const trigTitle = el('span', { class: 'font-medium mr-1' }, ['触发器：'])
  trigBlock.appendChild(trigTitle)
  if (trigs.length === 0) {
    const tip = el('span', {}, ['无触发器，不会自动触发；支持手动触发或被其他任务调用执行'])
    tip.style.color = 'rgb(var(--c-ink-subtle))'
    trigBlock.appendChild(tip)
  } else {
    for (const t of trigs) trigBlock.appendChild(el('div', { class: 'pl-2' }, ['· ' + triggerSummary(t)]))
  }
  card.appendChild(trigBlock)

  // 执行器摘要
  const exes = effectiveExecutors(sc)
  const exeBlock = el('div', { class: 'text-xs' })
  exeBlock.style.color = 'rgb(var(--c-ink-muted))'
  const exeTitle = el('span', { class: 'font-medium mr-1' }, ['执行器：'])
  exeBlock.appendChild(exeTitle)
  if (exes.length === 0) {
    const tip = el('span', {}, ['无执行器'])
    tip.style.color = 'rgb(var(--c-ink-subtle))'
    exeBlock.appendChild(tip)
  } else {
    for (const e of exes) exeBlock.appendChild(el('div', { class: 'pl-2' }, ['· ' + executorSummary(e)]))
  }
  card.appendChild(exeBlock)

  const foot = el('div', { class: 'mt-2 flex items-center gap-2' })
  if (sc.updatedAt) {
    const t = el('span', { class: 'text-[11px]' }, ['更新于 ' + new Date(sc.updatedAt).toLocaleString('zh-CN', { hour12: false })])
    t.style.color = 'rgb(var(--c-ink-subtle))'
    foot.appendChild(t)
  }
  card.appendChild(foot)
  return card
}

function refreshTasks(): void {
  loadPromise = null
  void loadAll(true).then(() => rerenderSchedulePage())
}

// ================================================================
// 任务表单（二级弹窗）
// ================================================================
interface TriggerDraft {
  type: ScheduleTriggerType
  period: TriggerPeriod
  runAt: string
  time: string
  weekdays: number[]
  monthDays: number[]
  loopInterval: string
  monitorPrebuilt: '' | 'idle'
  monitorScriptId: number | null
  monitorScriptArgs: string
  monitorIntervalSec: number
  // 日志事件：引用全局资源（logEventId>0）或内联（logEventPath/Regex + 结果处理字段）
  logEventId: number | null
  logEventPath: string
  logEventRegex: string
  logEventCooldown: number
  logEventCooldownUnit: CooldownUnit
  logEventConsecutive: number
  logEventRotate: boolean
  // 内联监控脚本（不创建全局资源，直接存 Trigger 内）
  monitorCode: string
  monitorScriptType: string
  // 旧事件型字段（存量迁移）
  diskGroups: number[]
  allDay: boolean
  timeRange: string
  weekLimit: number[]
  monthDayLimit: number[]
  thresholdMin: number
}

interface ExecutorDraft {
  type: ScheduleExecutorType
  delaySec: number
  // fan_control
  fanId: number | null
  fanEnabled: boolean | null
  fanManual: boolean
  fanPercent: number
  fanCurve: FanCurveMode | ''
  // disk_group_control
  diskGroupId: number | null
  diskAction: DiskAction
  killOccupied: boolean
  forceOff: boolean
  // 重试（disk + script）
  retryEnabled: boolean
  maxRetries: number
  retryIntervalSec: number
  // control_task
  taskId: number | null
  taskAction: TaskAction
  respectEnabled: boolean
  respectTimeRule: boolean
  // exec_script
  scriptPrebuilt: string
  scriptId: number | null
  scriptCode: string
  scriptType: string
  scriptArgs: string
  // 串联
  successTaskId: number | null
  failureTaskId: number | null
}

interface TaskFormDraft {
  name: string
  description: string
  enabled: boolean
  triggers: TriggerDraft[]
  executors: ExecutorDraft[]
}

function defaultTrigger(type: ScheduleTriggerType): TriggerDraft {
  const now = new Date()
  const pad = (n: number) => String(n).padStart(2, '0')
  const runAt = `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}T${pad(now.getHours())}:${pad(now.getMinutes())}`
  return {
    type,
    period: 'once',
    runAt,
    time: '08:00',
    weekdays: [1],
    monthDays: [1],
    loopInterval: '00:10:00',
    monitorPrebuilt: '' as const,
    monitorScriptId: null,
    monitorScriptArgs: '',
    monitorIntervalSec: 30,
    logEventId: null,
    logEventPath: '',
    logEventRegex: '',
    logEventCooldown: 0,
    logEventCooldownUnit: 'minute' as CooldownUnit,
    logEventConsecutive: 1,
    logEventRotate: true,
    monitorCode: '',
    monitorScriptType: '',
    diskGroups: [],
    allDay: true,
    timeRange: '08:00-20:00',
    weekLimit: [],
    monthDayLimit: [],
    thresholdMin: 15,
  }
}

function defaultExecutor(type: ScheduleExecutorType): ExecutorDraft {
  return {
    type,
    delaySec: 0,
    fanId: null,
    fanEnabled: null,
    fanManual: false,
    fanPercent: 50,
    fanCurve: '',
    diskGroupId: null,
    diskAction: 'offline',
    killOccupied: false,
    forceOff: false,
    retryEnabled: false,
    maxRetries: 3,
    retryIntervalSec: 10,
    taskId: null,
    taskAction: 'run',
    respectEnabled: false,
    respectTimeRule: false,
    scriptPrebuilt: '',
    scriptId: null,
    scriptCode: '',
    scriptType: '',
    scriptArgs: '',
    successTaskId: null,
    failureTaskId: null,
  }
}

function emptyDraft(): TaskFormDraft {
  return { name: '', description: '', enabled: true, triggers: [], executors: [] }
}

// 弹窗内编辑副本（保存/取消）
let modalDraft: TaskFormDraft | null = null
let modalEditSc: Schedule | null = null
let modalOverlay: HTMLElement | null = null
let modalCleanup: (() => void) | null = null

function setModalDirty(v: boolean): void { dirty = v }

function openTaskForm(sc: Schedule | null): void {
  if (modalDraft) { toast('请先关闭当前编辑窗口', 'info'); return }
  const editing = !!sc
  const draft = sc ? draftFromSchedule(sc) : emptyDraft()
  modalDraft = draft
  modalEditSc = sc
  dirty = false

  const overlay = el('div', { class: 'fixed inset-0 z-50 flex items-center justify-center p-4' })
  overlay.style.background = 'rgb(var(--c-overlay) / 0.6)'
  modalOverlay = overlay
  const modal = el('div', { class: 'card w-[720px] max-w-full flex flex-col p-5' })
  modal.id = 'task-form-modal'
  modal.style.maxHeight = '90vh'
  const titleEl = el('h3', { class: 'font-semibold text-base mb-1' }, [editing ? '编辑任务' : '新建任务'])
  titleEl.style.color = 'rgb(var(--c-ink))'

  const body = el('div', { class: 'overflow-y-auto pr-1 -mr-1 flex flex-col gap-4', style: 'min-height:0' })
  body.style.flex = '1'
  body.appendChild(renderBasicFields(draft))
  body.appendChild(renderTriggerSection(draft))
  body.appendChild(renderExecutorSection(draft))

  const foot = el('div', { class: 'flex justify-end gap-2 pt-3 border-t mt-2' })
  foot.style.borderColor = 'rgb(var(--c-line-subtle))'
  const cancel = el('button', { class: 'btn' }, ['取消'])
  const save = el('button', { class: 'btn btn-primary' }, ['保存'])
  cancel.onclick = () => void closeTaskForm()
  save.onclick = () => void saveTaskForm(draft, sc, overlay)
  foot.append(cancel, save)

  modal.append(titleEl, body, foot)
  overlay.appendChild(modal)
  overlay.onclick = (e) => { if (e.target === overlay) void closeTaskForm() }
  document.body.appendChild(overlay)
  modalCleanup = () => { overlay.remove(); modalCleanup = null }
}

async function closeTaskForm(): Promise<void> {
  if (!modalOverlay) return
  if (dirty) {
    const ch = await unsavedChoice()
    if (ch === 'stay') return
    if (ch === 'save') {
      if (modalDraft) await saveTaskForm(modalDraft, modalEditSc, modalOverlay)
      return // 保存成功已关闭；失败则停留在窗口
    }
  }
  dirty = false
  modalDraft = null
  modalEditSc = null
  modalOverlay.remove()
  modalOverlay = null
  modalCleanup = null
}

async function saveTaskForm(draft: TaskFormDraft, sc: Schedule | null, overlay: HTMLElement): Promise<void> {
  const err = validateDraft(draft)
  if (err) { toast(err, 'error'); return }
  const body = buildScheduleBody(draft)
  try {
    if (sc) await api.updateSchedule(sc.id, body)
    else await api.createSchedule(body)
    toast(sc ? '任务已保存' : '任务已创建', 'success')
    dirty = false
    modalDraft = null
    modalEditSc = null
    overlay.remove()
    modalOverlay = null
    modalCleanup = null
    loadPromise = null
    await loadAll(true)
    rerenderSchedulePage()
  } catch (err2) {
    toast('保存失败：' + (err2 instanceof Error ? err2.message : String(err2)), 'error')
  }
}

// 基本信息：任务名称（后方启用开关）+ 备注（修订版：启用移至名称后，UI 为开关组件）
function renderBasicFields(d: TaskFormDraft): HTMLElement {
  const sec = formSection('基本信息')
  const nameWrap = el('div', { class: 'flex items-start gap-3' })
  const nameBox = el('div', { class: 'flex flex-col gap-1', style: 'flex:1' })
  const nameL = el('label', { class: 'text-xs font-medium' }, ['任务名称'])
  nameL.style.color = 'rgb(var(--c-ink-muted))'
  const nameInput = el('input', { class: 'input w-full', placeholder: '必填，如：上班自动切高效曲线', value: d.name }) as HTMLInputElement
  nameInput.oninput = () => { d.name = nameInput.value; setModalDirty(true) }
  nameBox.append(nameL, nameInput)
  const enBox = el('div', { class: 'flex flex-col gap-1' })
  const enL = el('label', { class: 'text-xs font-medium' }, ['启用'])
  enL.style.color = 'rgb(var(--c-ink-muted))'
  const toggle = el('label', { class: 'toggle' })
  const en = el('input', { type: 'checkbox', checked: d.enabled } as Record<string, string | boolean>)
  en.onchange = () => { d.enabled = en.checked; setModalDirty(true) }
  toggle.append(en, el('span', { class: 'toggle-slider' }))
  enBox.append(enL, toggle)
  nameWrap.append(nameBox, enBox)
  sec.appendChild(nameWrap)

  const descRow = labelRow('备注', el('input', {
    class: 'input w-full', placeholder: '可选', value: d.description || '',
  } as Record<string, string>))
  sec.appendChild(descRow)
  const descInput = descRow.lastChild as HTMLInputElement
  descInput.oninput = () => { d.description = descInput.value; setModalDirty(true) }
  return sec
}

// ================================================================
// 触发器清单
// ================================================================
function renderTriggerSection(d: TaskFormDraft): HTMLElement {
  const sec = formSection('触发器清单')
  const hint = el('div', { class: 'text-xs mb-2' }, ['无触发器不会自动触发；支持手动触发或被其他任务调用执行。'])
  hint.style.color = 'rgb(var(--c-ink-subtle))'
  sec.appendChild(hint)

  const list = el('div', { class: 'flex flex-col gap-2 mb-2' })
  const add = el('button', { class: 'btn btn-sm', style: 'align-self:flex-start' } as Record<string, string | boolean>, [svgIcon('plus', 13), ' 添加触发器'])
  add.onclick = () => {
    d.triggers.push(defaultTrigger('time'))
    setModalDirty(true)
    openTriggerModal(d, d.triggers.length - 1)
  }

  const group = el('div', { class: 'flex flex-col gap-2' })
  const renderList = () => {
    group.innerHTML = ''
    if (d.triggers.length === 0) {
      const tip = el('div', { class: 'text-xs py-2 px-3 rounded' }, ['（无触发器）'])
      tip.style.background = 'rgb(var(--c-overlay) / 0.3)'
      tip.style.color = 'rgb(var(--c-ink-subtle))'
      group.appendChild(tip)
    } else {
      d.triggers.forEach((t, i) => {
        const row = el('div', { class: 'flex items-center gap-2 px-3 py-2 rounded border text-xs' })
        row.style.borderColor = 'rgb(var(--c-line-subtle))'
        row.style.background = 'rgb(var(--c-overlay) / 0.2)'
        const sum = el('span', { class: 'flex-1 truncate' }, [triggerSummaryDraft(t)])
        sum.style.color = 'rgb(var(--c-ink))'
        const cfg = el('button', { class: 'btn btn-sm', title: '配置' }, [svgIcon('settings', 12)])
        cfg.onclick = () => openTriggerModal(d, i)
        const del = el('button', { class: 'btn btn-sm btn-danger', title: '删除' }, [svgIcon('trash', 12)])
        del.onclick = () => {
          d.triggers.splice(i, 1)
          setModalDirty(true)
          renderList()
        }
        row.append(sum, cfg, del)
        group.appendChild(row)
      })
    }
  }
  renderList()
  sec.append(list, group, add)
  return sec
}

function triggerSummaryDraft(t: TriggerDraft): string {
  if (t.type === 'time') {
    switch (t.period) {
      case 'once': return `一次性 · ${t.runAt || '—'}`
      case 'daily': return `每天 ${t.time || '—'}`
      case 'weekly': return `每周${(t.weekdays || []).map(d => WEEK_LABEL[d - 1]).join('/')} ${t.time || '—'}`
      case 'monthly': return `每月${(t.monthDays || []).map(d => `${d}日`).join('/')} ${t.time || '—'}`
      case 'loop': return `循环 · 每 ${t.loopInterval || '—'}`
      default: return '定时事件'
    }
  }
  if (t.type === 'log') {
    if (t.logEventId != null) return `日志事件 · ${logEventName(t.logEventId)}`
    if (t.logEventPath && t.logEventRegex) return `日志事件 · 内联（${t.logEventPath}）`
    return '日志事件 · 未配置'
  }
  if (t.type === 'monitor') {
    const argsTag = t.monitorScriptArgs ? ` $ ${t.monitorScriptArgs}` : ''
    if (t.monitorPrebuilt === 'idle') return `监控事件 · 监控硬盘AB闲置（预制）${argsTag}（每 ${t.monitorIntervalSec} 秒）`
    if (t.monitorScriptId != null) return `监控事件 · ${monitorScriptName(t.monitorScriptId)}${argsTag}（每 ${t.monitorIntervalSec} 秒）`
    if (t.monitorCode?.trim()) {
      const typeTag = t.monitorScriptType === 'python' ? ' · python' : t.monitorScriptType === 'shell' ? ' · sh' : ''
      return `监控事件 · 内联${typeTag}${argsTag}（每 ${t.monitorIntervalSec} 秒）`
    }
    return '监控事件 · 未配置'
  }
  return t.type
}

// 触发器配置弹窗
function openTriggerModal(d: TaskFormDraft, idx: number): void {
  const t = d.triggers[idx]
  const overlay = el('div', { class: 'fixed inset-0 z-[60] flex items-center justify-center p-4' })
  overlay.style.background = 'rgb(var(--c-overlay) / 0.6)'
  const box = el('div', { class: 'card w-[480px] max-w-full flex flex-col p-5' })
  box.style.maxHeight = '88vh'
  const titleEl = el('h3', { class: 'font-semibold text-base mb-3' }, ['触发器配置'])
  titleEl.style.color = 'rgb(var(--c-ink))'
  const body = el('div', { class: 'overflow-y-auto pr-1 -mr-1 flex flex-col gap-3', style: 'min-height:0' })
  body.style.flex = '1'
  body.appendChild(renderTriggerConfig(t, body))

  const foot = el('div', { class: 'flex justify-end gap-2 pt-3 border-t mt-2' })
  foot.style.borderColor = 'rgb(var(--c-line-subtle))'
  const cancel = el('button', { class: 'btn' }, ['取消'])
  const okBtn = el('button', { class: 'btn btn-primary' }, ['保存'])
  const close = () => { overlay.remove() }
  cancel.onclick = close
  okBtn.onclick = () => {
    const err = validateTriggerDraft(t)
    if (err) { toast(err, 'error'); return }
    setModalDirty(true)
    close()
    rerenderModalBody()
  }
  foot.append(cancel, okBtn)
  box.append(titleEl, body, foot)
  overlay.appendChild(box)
  overlay.onclick = (e) => { if (e.target === overlay) close() }
  document.body.appendChild(overlay)
}

// 触发器配置内容（类型联动显隐）
function renderTriggerConfig(t: TriggerDraft, body: HTMLElement): HTMLElement {
  const sec = el('div', { class: 'flex flex-col gap-3' })

  // 类型
  const typeRow = labelRow('触发类型', el('select', { class: 'input' } as Record<string, string | boolean>))
  const typeSel = typeRow.querySelector('select') as HTMLSelectElement
  const opts: Array<[ScheduleTriggerType, string]> = [
    ['time', '定时事件'],
    ['log', '日志事件'],
    ['monitor', '监控事件'],
  ]
  for (const [v, label] of opts) {
    const o = el('option', { value: v }, [label])
    if (v === t.type) o.selected = true
    typeSel.appendChild(o)
  }
  typeSel.onchange = () => {
    t.type = typeSel.value as ScheduleTriggerType
    setModalDirty(true)
    rerenderConfig(t, body)
  }
  sec.appendChild(typeRow)

  sec.appendChild(renderTriggerFields(t, body))
  return sec
}

function renderTriggerFields(t: TriggerDraft, body: HTMLElement): HTMLElement {
  const sec = el('div', { class: 'flex flex-col gap-3' })
  if (t.type === 'time') {
    // 周期
    const periodRow = labelRow('周期', el('select', { class: 'input' } as Record<string, string | boolean>))
    const periodSel = periodRow.querySelector('select') as HTMLSelectElement
    const pops: Array<[TriggerPeriod, string]> = [
      ['once', '一次性'], ['daily', '每天'], ['weekly', '每周'], ['monthly', '每月'], ['loop', '循环'],
    ]
    for (const [v, label] of pops) {
      const o = el('option', { value: v }, [label])
      if (v === t.period) o.selected = true
      periodSel.appendChild(o)
    }
    periodSel.onchange = () => { t.period = periodSel.value as TriggerPeriod; setModalDirty(true); rerenderConfig(t, body) }
    sec.appendChild(periodRow)

    if (t.period === 'once') {
      sec.appendChild(labelRow('执行时刻', el('input', { class: 'input w-full', type: 'datetime-local', value: t.runAt } as Record<string, string | boolean>)))
      const inp = sec.lastChild?.lastChild as HTMLInputElement
      inp.onchange = () => { t.runAt = inp.value; setModalDirty(true) }
    } else if (t.period === 'loop') {
      const row = labelRow('循环时间', el('input', {
        class: 'input w-full font-mono',
        placeholder: 'HH:MM:SS（如 00:10:00）',
        value: t.loopInterval,
        pattern: '\\d{2}:\\d{2}:\\d{2}',
        maxlength: '8',
      } as Record<string, string>))
      sec.appendChild(row)
      const inp = row.lastChild as HTMLInputElement
      inp.oninput = () => {
        t.loopInterval = inp.value
        const valid = /^\d{2}:\d{2}:\d{2}$/.test(inp.value) && inp.value !== '00:00:00'
        inp.style.borderColor = valid ? '' : 'rgb(var(--c-danger, #f56565))'
        setModalDirty(true)
      }
      const hint = el('div', { class: 'text-[11px] leading-tight -mt-1 px-3 py-1 rounded' })
      hint.style.color = 'rgb(var(--c-ink-muted))'
      hint.textContent = '启动后每隔该时长执行一次；首次执行 = 启动时刻 + 循环时间'
      sec.appendChild(hint)
    } else {
      sec.appendChild(labelRow('执行时间', el('input', { class: 'input w-full', type: 'time', value: t.time } as Record<string, string | boolean>)))
      const inp = sec.lastChild?.lastChild as HTMLInputElement
      inp.onchange = () => { t.time = inp.value; setModalDirty(true) }
      if (t.period === 'weekly') {
        sec.appendChild(labelRow('星期', weekCheckboxRow(t.weekdays, () => setModalDirty(true))))
      } else if (t.period === 'monthly') {
        sec.appendChild(labelRow('日期', dayCheckboxRow(t.monthDays, () => setModalDirty(true))))
      }
    }
  } else if (t.type === 'log') {
    const modeRow = labelRow('日志事件', el('select', { class: 'input' } as Record<string, string | boolean>))
    const sel = modeRow.querySelector('select') as HTMLSelectElement
    sel.appendChild(el('option', { value: '' }, ['— 请选择 —']))
    for (const r of logEvents) sel.appendChild(el('option', { value: String(r.id) }, [r.name]))
    sel.appendChild(el('option', { value: 'new' }, ['自定义…']))
    if (t.logEventId != null && logEvents.some(r => r.id === t.logEventId)) sel.value = String(t.logEventId)
    else sel.value = 'new'
    sel.onchange = () => {
      const v = sel.value
      if (v === 'new') { t.logEventId = null }
      else if (v === '') { /* no-op */ }
      else { t.logEventId = Number(v) }
      setModalDirty(true)
      rerenderConfig(t, body)
    }
    sec.appendChild(modeRow)
    if (sel.value === 'new') {
      sec.appendChild(inlineLogEventForm(t))
    } else if (t.logEventId != null) {
      const tip = el('div', { class: 'text-xs px-3 py-2 rounded' })
      tip.style.background = 'rgb(var(--c-overlay) / 0.3)'
      tip.style.color = 'rgb(var(--c-ink-muted))'
      const r = logEvents.find(x => x.id === t.logEventId)
      tip.textContent = r ? `路径 ${r.path} · 匹配 ${r.regex}` : ''
      sec.appendChild(tip)
    }
  } else if (t.type === 'monitor') {
    const modeRow = labelRow('监控事件', el('select', { class: 'input' } as Record<string, string | boolean>))
    const sel = modeRow.querySelector('select') as HTMLSelectElement
    sel.appendChild(el('option', { value: '' }, ['— 请选择 —']))
    for (const r of monitorScripts) sel.appendChild(el('option', { value: String(r.id) }, [r.name]))
    sel.appendChild(el('option', { value: 'new' }, ['自定义…']))
    if (t.monitorScriptId != null && monitorScripts.some(r => r.id === t.monitorScriptId)) sel.value = String(t.monitorScriptId)
    else sel.value = 'new'
    sel.onchange = () => {
      const v = sel.value
      if (v === 'new') { t.monitorPrebuilt = ''; t.monitorScriptId = null }
      else if (v === '') { /* no-op */ }
      else { t.monitorPrebuilt = ''; t.monitorScriptId = Number(v) }
      setModalDirty(true)
      rerenderConfig(t, body)
    }
    sec.appendChild(modeRow)
    if (sel.value === 'new') {
      sec.appendChild(inlineMonitorForm(t))
    } else if (t.monitorScriptId != null) {
      const tip = el('div', { class: 'text-xs px-3 py-2 rounded' })
      tip.style.background = 'rgb(var(--c-overlay) / 0.3)'
      tip.style.color = 'rgb(var(--c-ink-muted))'
      tip.textContent = monitorScriptName(t.monitorScriptId)
      sec.appendChild(tip)
    }
    const argsRow = labelRow('脚本传入值', el('input', {
      class: 'input w-full font-mono',
      placeholder: '如: -f ssd  (sh 用 $1 $2 ...，python 用 sys.argv[1:] 引用)',
      value: t.monitorScriptArgs,
    } as Record<string, string>))
    sec.appendChild(argsRow)
    const argsInp = argsRow.lastChild as HTMLInputElement
    argsInp.oninput = () => { t.monitorScriptArgs = argsInp.value; setModalDirty(true) }
    const ivRow = labelRow('执行间隔（秒）', el('input', { class: 'input w-full', type: 'number', min: '1', max: '600', value: String(t.monitorIntervalSec) } as Record<string, string | boolean>))
    sec.appendChild(ivRow)
    const ivInp = ivRow.lastChild as HTMLInputElement
    ivInp.oninput = () => { t.monitorIntervalSec = Number(ivInp.value); setModalDirty(true) }
  }
  return sec
}

function inlineMonitorForm(t: TriggerDraft): HTMLElement {
  const sec = el('div', { class: 'flex flex-col gap-3 rounded-lg border p-3' })
  sec.style.borderColor = 'rgb(var(--c-line-subtle))'
  // 脚本类型：指定本段内联自定义代码是 Shell 还是 Python；切换时同步更新描述
  const typeRow = labelRow('脚本类型', el('select', { class: 'input' } as Record<string, string | boolean>))
  const typeSel = typeRow.querySelector('select') as HTMLSelectElement
  typeSel.appendChild(el('option', { value: 'shell' }, ['Shell（sh）']))
  typeSel.appendChild(el('option', { value: 'python' }, ['Python']))
  if (t.monitorScriptType === 'python') typeSel.value = 'python'
  sec.appendChild(typeRow)
  sec.appendChild(labelRow('脚本代码', el('textarea', { class: 'input w-full font-mono', rows: '6' } as Record<string, string | boolean>)))
  const ta = sec.lastChild?.lastChild as HTMLTextAreaElement
  ta.value = t.monitorCode
  const hint = el('div', { class: 'text-xs' })
  hint.style.color = 'rgb(var(--c-ink-muted))'
  const syncHint = () => {
    if (t.monitorScriptType === 'python') {
      ta.placeholder = '# python3 -c 执行，参数用 sys.argv[1:] 引用；按执行间隔运行，有输出即触发'
      hint.textContent = '按 Python（python3）执行，传入参数经 sys.argv[1:] 引用'
    } else {
      ta.placeholder = '#!/bin/sh\n# sh -c 执行，参数用 $1 $2 ... 引用；按执行间隔运行，有输出即触发'
      hint.textContent = '按 Shell（sh）执行，传入参数经 $1 $2 ... 引用'
    }
  }
  typeSel.onchange = () => { t.monitorScriptType = typeSel.value; syncHint(); setModalDirty(true) }
  syncHint()
  sec.appendChild(hint)
  ta.oninput = () => { t.monitorCode = ta.value; setModalDirty(true) }
  return sec
}

function rerenderConfig(t: TriggerDraft, body: HTMLElement): void {
  body.innerHTML = ''
  body.appendChild(renderTriggerConfig(t, body))
}

// 触发器内联日志事件表单（不创建全局资源，直接存 Trigger 内）
function inlineLogEventForm(t: TriggerDraft): HTMLElement {
  const sec = el('div', { class: 'flex flex-col gap-3 rounded-lg border p-3' })
  sec.style.borderColor = 'rgb(var(--c-line-subtle))'
  sec.appendChild(labelRow('日志路径', el('input', { class: 'input w-full font-mono', placeholder: '/var/log/syslog', value: t.logEventPath })))
  const pathI = sec.lastChild?.lastChild as HTMLInputElement
  pathI.oninput = () => { t.logEventPath = pathI.value; setModalDirty(true) }
  sec.appendChild(labelRow('匹配正则', el('input', { class: 'input w-full font-mono', placeholder: '如：hdparm.*(sleeping|idle)', value: t.logEventRegex })))
  const reI = sec.lastChild?.lastChild as HTMLInputElement
  reI.oninput = () => { t.logEventRegex = reI.value; setModalDirty(true) }

  const row1 = el('div', { class: 'flex gap-3' })
  const coolBox = el('div', { class: 'flex flex-col gap-1', style: 'flex:2' })
  const coolL = el('label', { class: 'text-xs font-medium' }, ['冷却时间（0=不冷却）'])
  coolL.style.color = 'rgb(var(--c-ink-muted))'
  const coolI = el('input', { class: 'input w-full', type: 'number', min: '0', max: '99', value: String(t.logEventCooldown) } as Record<string, string | boolean>)
  coolI.oninput = () => { t.logEventCooldown = Math.min(99, Math.max(0, Number(coolI.value) || 0)); setModalDirty(true) }
  coolBox.append(coolL, coolI)
  const unitBox = el('div', { class: 'flex flex-col gap-1', style: 'flex:1' })
  const unitL = el('label', { class: 'text-xs font-medium' }, ['单位'])
  unitL.style.color = 'rgb(var(--c-ink-muted))'
  const unitSel = el('select', { class: 'input' } as Record<string, string | boolean>)
  for (const [v, label] of [['second', '秒'], ['minute', '分钟'], ['hour', '小时']] as Array<[CooldownUnit, string]>) {
    const o = el('option', { value: v }, [label])
    if (v === t.logEventCooldownUnit) o.selected = true
    unitSel.appendChild(o)
  }
  unitSel.onchange = () => { t.logEventCooldownUnit = unitSel.value as CooldownUnit; setModalDirty(true) }
  unitBox.append(unitL, unitSel)
  row1.append(coolBox, unitBox)
  sec.appendChild(row1)

  const row2 = el('div', { class: 'flex gap-3 items-end' })
  const conBox = el('div', { class: 'flex flex-col gap-1', style: 'flex:1' })
  const conL = el('label', { class: 'text-xs font-medium' }, ['连续命中次数'])
  conL.style.color = 'rgb(var(--c-ink-muted))'
  const conI = el('input', { class: 'input w-full', type: 'number', min: '1', max: '9', value: String(t.logEventConsecutive) } as Record<string, string | boolean>)
  conI.oninput = () => { t.logEventConsecutive = Math.min(9, Math.max(1, Number(conI.value) || 1)); setModalDirty(true) }
  conBox.append(conL, conI)
  row2.appendChild(conBox)
  const rotWrap = el('div', { class: 'pb-1' })
  rotWrap.appendChild(checkRow('支持日志轮转（rotate）', t.logEventRotate, (v) => { t.logEventRotate = v; setModalDirty(true) }))
  row2.appendChild(rotWrap)
  sec.appendChild(row2)

  return sec
}

// ================================================================
// 执行器清单
// ================================================================
function renderExecutorSection(d: TaskFormDraft): HTMLElement {
  const sec = formSection('执行器清单')
  const hint = el('div', { class: 'text-xs mb-2' }, ['至少 1 个；触发时按顺序执行。独立延迟 1~3600 秒，0 为立即。'])
  hint.style.color = 'rgb(var(--c-ink-subtle))'
  sec.appendChild(hint)

  const group = el('div', { class: 'flex flex-col gap-2 mb-2' })
  const add = el('button', { class: 'btn btn-sm', style: 'align-self:flex-start' } as Record<string, string | boolean>, [svgIcon('plus', 13), '添加执行器'])
  add.onclick = () => {
    d.executors.push(defaultExecutor('fan_control'))
    setModalDirty(true)
    openExecutorModal(d, d.executors.length - 1)
  }

  const renderList = () => {
    group.innerHTML = ''
    d.executors.forEach((e, i) => {
      const row = el('div', { class: 'flex items-center gap-2 px-3 py-2 rounded border text-xs' })
      row.style.borderColor = 'rgb(var(--c-line-subtle))'
      row.style.background = 'rgb(var(--c-overlay) / 0.2)'
      const sum = el('span', { class: 'flex-1 truncate' }, [executorSummaryDraft(e)])
      sum.style.color = 'rgb(var(--c-ink))'
      const cfg = el('button', { class: 'btn btn-sm', title: '配置' }, [svgIcon('settings', 12)])
      cfg.onclick = () => openExecutorModal(d, i)
      const del = el('button', { class: 'btn btn-sm btn-danger', title: '删除' }, [svgIcon('trash', 12)])
      del.onclick = () => {
        d.executors.splice(i, 1)
        setModalDirty(true)
        renderList()
      }
      row.append(sum, cfg, del)
      group.appendChild(row)
    })
  }
  renderList()
  sec.append(group, add)
  return sec
}

function executorSummaryDraft(e: ExecutorDraft): string {
  const parts: string[] = []
  if (e.delaySec) parts.push(`延迟 ${e.delaySec}s`)
  switch (e.type) {
    case 'fan_control': {
      let s = `风扇 ${fanName(e.fanId ?? undefined)} → `
      if (e.fanEnabled === true) s += '启用'
      else if (e.fanEnabled === false) s += '禁用'
      else s += '不改变开关'
      if (e.fanManual) s += ` · 手动 ${e.fanPercent ?? '—'}%`
      else if (e.fanCurve) s += ` · 自动 ${fanCurveLabel(e.fanCurve)}`
      else s += ' · 自动（曲线维持）'
      parts.push(s)
      break
    }
    case 'disk_group_control': {
      let s = `硬盘 ${diskGroupName(e.diskGroupId ?? undefined)} → ${e.diskAction === 'online' ? '上线' : '下线'}`
      if (e.diskAction === 'offline') {
        if (e.killOccupied) s += ' · 自动终止占用'
        if (e.forceOff) s += ' · 强制'
      }
      if (e.retryEnabled) s += ` · 失败重试×${e.maxRetries ?? 3}`
      parts.push(s)
      break
    }
    case 'control_task': {
      const act = e.taskAction === 'enable' ? '启用' : e.taskAction === 'disable' ? '禁用' : '执行'
      let s = `任务 ${taskName(e.taskId ?? undefined)} → ${act}`
      if (e.respectEnabled) s += ' · 遵循启用'
      if (e.respectTimeRule) s += ' · 遵循时间'
      parts.push(s)
      break
    }
    case 'exec_script': {
      let s = '脚本'
      if (e.scriptId) s += ` ${execScriptName(e.scriptId)}`
      else if (e.scriptCode) s += '（内联）'
      if (e.scriptArgs) s += ` ${e.scriptArgs}`
      if (e.retryEnabled) s += ` · 失败重试×${e.maxRetries ?? 3}`
      parts.push(s)
      break
    }
  }
  if (e.successTaskId) parts.push(`成功→${taskName(e.successTaskId)}`)
  if (e.failureTaskId) parts.push(`失败→${taskName(e.failureTaskId)}`)
  return parts.join(' · ')
}

function openExecutorModal(d: TaskFormDraft, idx: number): void {
  const e = d.executors[idx]
  const overlay = el('div', { class: 'fixed inset-0 z-[60] flex items-center justify-center p-4' })
  overlay.style.background = 'rgb(var(--c-overlay) / 0.6)'
  const box = el('div', { class: 'card w-[520px] max-w-full flex flex-col p-5' })
  box.style.maxHeight = '88vh'
  const titleEl = el('h3', { class: 'font-semibold text-base mb-3' }, ['执行器配置'])
  titleEl.style.color = 'rgb(var(--c-ink))'
  const body = el('div', { class: 'overflow-y-auto pr-1 -mr-1 flex flex-col gap-3', style: 'min-height:0' })
  body.style.flex = '1'
  body.appendChild(renderExecutorConfig(d, idx, body))

  const foot = el('div', { class: 'flex justify-end gap-2 pt-3 border-t mt-2' })
  foot.style.borderColor = 'rgb(var(--c-line-subtle))'
  const cancel = el('button', { class: 'btn' }, ['取消'])
  const okBtn = el('button', { class: 'btn btn-primary' }, ['保存'])
  const close = () => { overlay.remove() }
  cancel.onclick = close
  okBtn.onclick = () => {
    const err = validateExecutorDraft(e)
    if (err) { toast(err, 'error'); return }
    setModalDirty(true)
    close()
    rerenderModalBody()
  }
  foot.append(cancel, okBtn)
  box.append(titleEl, body, foot)
  overlay.appendChild(box)
  overlay.onclick = (ev) => { if (ev.target === overlay) close() }
  document.body.appendChild(overlay)
}

function renderExecutorConfig(d: TaskFormDraft, idx: number, body: HTMLElement): HTMLElement {
  const e = d.executors[idx]
  const sec = el('div', { class: 'flex flex-col gap-3' })

  // 类型
  const typeRow = labelRow('类型', el('select', { class: 'input' } as Record<string, string | boolean>))
  const typeSel = typeRow.querySelector('select') as HTMLSelectElement
  const eopts: Array<[ScheduleExecutorType, string]> = [
    ['fan_control', '风扇控制'],
    ['disk_group_control', '硬盘组控制'],
    ['control_task', '控制任务'],
    ['exec_script', '执行脚本'],
  ]
  for (const [v, label] of eopts) {
    const o = el('option', { value: v }, [label])
    if (v === e.type) o.selected = true
    typeSel.appendChild(o)
  }
  typeSel.onchange = () => {
    e.type = typeSel.value as ScheduleExecutorType
    setModalDirty(true)
    rerenderExecutorConfig(d, idx, body)
  }
  sec.appendChild(typeRow)

  // 独立延迟
  const delayRow = labelRow('延迟执行（秒）', el('input', { class: 'input w-full', type: 'number', min: '0', max: '3600', value: String(e.delaySec) } as Record<string, string | boolean>))
  sec.appendChild(delayRow)
  const delayInp = delayRow.lastChild as HTMLInputElement
  delayInp.oninput = () => { e.delaySec = Math.max(0, Number(delayInp.value) || 0); setModalDirty(true) }

  // 类型联动字段
  if (e.type === 'fan_control') {
    sec.appendChild(renderFanFields(e, d, idx, body))
  } else if (e.type === 'disk_group_control') {
    sec.appendChild(renderDiskFields(e, d, idx, body))
  } else if (e.type === 'control_task') {
    sec.appendChild(renderControlTaskFields(e, d))
  } else {
    sec.appendChild(renderScriptFields(e, d, idx, body))
  }
  return sec
}

function rerenderExecutorConfig(d: TaskFormDraft, idx: number, body: HTMLElement): void {
  body.innerHTML = ''
  body.appendChild(renderExecutorConfig(d, idx, body))
}

function renderFanFields(e: ExecutorDraft, d: TaskFormDraft, idx: number, body: HTMLElement): HTMLElement {
  const sec = el('div', { class: 'flex flex-col gap-3' })
  // 风扇
  const fanRow = labelRow('风扇', el('select', { class: 'input' } as Record<string, string | boolean>))
  const fanSel = fanRow.querySelector('select') as HTMLSelectElement
  fanSel.appendChild(el('option', { value: '0' }, ['— 请选择 —']))
  for (let i = 1; i <= 2; i++) {
    const o = el('option', { value: String(i) }, [fanName(i)])
    if (e.fanId === i) o.selected = true
    fanSel.appendChild(o)
  }
  fanSel.onchange = () => { e.fanId = Number(fanSel.value) || null; setModalDirty(true) }
  sec.appendChild(fanRow)

  // 启用开关三态
  const enRow = labelRow('电源开关', el('select', { class: 'input' } as Record<string, string | boolean>))
  const enSel = enRow.querySelector('select') as HTMLSelectElement
  const enOpts: Array<[string, string]> = [['', '不改变'], ['1', '启用'], ['0', '禁用']]
  for (const [v, label] of enOpts) {
    const o = el('option', { value: v }, [label])
    if (e.fanEnabled === true && v === '1') o.selected = true
    if (e.fanEnabled === false && v === '0') o.selected = true
    if (e.fanEnabled == null && v === '') o.selected = true
    enSel.appendChild(o)
  }
  enSel.onchange = () => {
    e.fanEnabled = enSel.value === '1' ? true : enSel.value === '0' ? false : null
    setModalDirty(true)
  }
  sec.appendChild(enRow)

  // 自动 / 手动
  const modeRow = labelRow('控制模式', el('select', { class: 'input' } as Record<string, string | boolean>))
  const modeSel = modeRow.querySelector('select') as HTMLSelectElement
  const mAuto = el('option', { value: 'auto' }, ['自动'])
  const mManual = el('option', { value: 'manual' }, ['手动'])
  if (e.fanManual) mManual.selected = true
  else mAuto.selected = true
  modeSel.append(mAuto, mManual)
  modeSel.onchange = () => {
    e.fanManual = modeSel.value === 'manual'
    setModalDirty(true)
    rerenderExecutorConfig(d, idx, body)
  }
  sec.appendChild(modeRow)

  if (e.fanManual) {
    const pctRow = labelRow('手动转速（%）', el('input', { class: 'input w-full', type: 'number', min: '1', max: '100', value: String(e.fanPercent) } as Record<string, string | boolean>))
    sec.appendChild(pctRow)
    const pctInp = pctRow.lastChild as HTMLInputElement
    pctInp.oninput = () => { e.fanPercent = Math.min(100, Math.max(1, Number(pctInp.value) || 1)); setModalDirty(true) }
  } else {
    // 自动三选一按钮组（修订版：重复点击已选中的按钮取消选择，维持原曲线）
    const curveRow = labelRow('自动曲线（重复点击已选项取消，维持原曲线）', el('div', { class: 'flex gap-2' }))
    const curveBox = curveRow.lastChild as HTMLElement
    const curves: Array<[FanCurveMode, string]> = [['efficient', '高效'], ['daily', '日常'], ['quiet', '静音']]
    for (const [v, label] of curves) {
      const b = el('button', { class: `btn btn-sm ${e.fanCurve === v ? 'btn-primary' : ''}` }, [label])
      b.onclick = () => {
        e.fanCurve = e.fanCurve === v ? '' : v
        setModalDirty(true)
        rerenderExecutorConfig(d, idx, body)
      }
      curveBox.appendChild(b)
    }
    sec.appendChild(curveRow)
  }
  return sec
}

function renderDiskFields(e: ExecutorDraft, d: TaskFormDraft, idx: number, body: HTMLElement): HTMLElement {
  const sec = el('div', { class: 'flex flex-col gap-3' })
  const groupRow = labelRow('硬盘组', el('select', { class: 'input' } as Record<string, string | boolean>))
  const gSel = groupRow.querySelector('select') as HTMLSelectElement
  gSel.appendChild(el('option', { value: '0' }, ['— 请选择 —']))
  for (let i = 1; i <= 4; i++) {
    const o = el('option', { value: String(i) }, [diskGroupName(i)])
    if (e.diskGroupId === i) o.selected = true
    gSel.appendChild(o)
  }
  gSel.onchange = () => { e.diskGroupId = Number(gSel.value) || null; setModalDirty(true) }
  sec.appendChild(groupRow)

  const actRow = labelRow('动作', el('select', { class: 'input' } as Record<string, string | boolean>))
  const aSel = actRow.querySelector('select') as HTMLSelectElement
  const aOff = el('option', { value: 'offline' }, ['下线'])
  const aOn = el('option', { value: 'online' }, ['上线'])
  if (e.diskAction === 'online') aOn.selected = true
  else aOff.selected = true
  aSel.append(aOff, aOn)
  aSel.onchange = () => { e.diskAction = aSel.value as DiskAction; setModalDirty(true) }
  sec.appendChild(actRow)

  if (e.diskAction === 'offline') {
    sec.appendChild(checkRow('自动终止占用进程（卸载失败时）', e.killOccupied, (v) => { e.killOccupied = v; setModalDirty(true) }))
    sec.appendChild(checkRow('强制下线（umount -f）', e.forceOff, (v) => { e.forceOff = v; setModalDirty(true) }))
  }

  // 失败重试
  sec.appendChild(retryFields(e, () => rerenderExecutorConfig(d, idx, body)))

  // 成功 / 失败后执行任务
  sec.appendChild(followTaskFields(e, d))
  return sec
}

function retryFields(e: ExecutorDraft, rerender?: () => void): HTMLElement {
  const sec = el('div', { class: 'flex flex-col gap-2 p-3 rounded' })
  sec.style.background = 'rgb(var(--c-overlay) / 0.3)'
  const head = checkRow('失败重复执行', e.retryEnabled, (v) => {
    e.retryEnabled = v
    setModalDirty(true)
    if (rerender) rerender()
  })
  sec.appendChild(head)
  if (e.retryEnabled) {
    const nRow = labelRow('最大重试次数', el('input', { class: 'input w-full', type: 'number', min: '1', max: '10', value: String(e.maxRetries) } as Record<string, string | boolean>))
    sec.appendChild(nRow)
    const nInp = nRow.lastChild as HTMLInputElement
    nInp.oninput = () => { e.maxRetries = Math.min(10, Math.max(1, Number(nInp.value) || 3)); setModalDirty(true) }
    const ivRow = labelRow('重试间隔（秒）', el('input', { class: 'input w-full', type: 'number', min: '1', max: '3600', value: String(e.retryIntervalSec) } as Record<string, string | boolean>))
    sec.appendChild(ivRow)
    const ivInp = ivRow.lastChild as HTMLInputElement
    ivInp.oninput = () => { e.retryIntervalSec = Math.min(3600, Math.max(1, Number(ivInp.value) || 10)); setModalDirty(true) }
  }
  return sec
}

function followTaskFields(e: ExecutorDraft, d: TaskFormDraft): HTMLElement {
  const sec = el('div', { class: 'flex flex-col gap-2 p-3 rounded' })
  sec.style.background = 'rgb(var(--c-overlay) / 0.3)'
  const title = el('div', { class: 'text-xs font-medium mb-1' }, ['任务串联'])
  title.style.color = 'rgb(var(--c-ink))'
  sec.appendChild(title)
  sec.appendChild(taskSelectRow('成功后执行任务', e, 'successTaskId', d))
  sec.appendChild(taskSelectRow('失败后执行任务', e, 'failureTaskId', d))
  return sec
}

function taskSelectRow(label: string, e: ExecutorDraft, key: 'successTaskId' | 'failureTaskId', d: TaskFormDraft): HTMLElement {
  const row = labelRow(label, taskSelect(key === 'successTaskId' ? e.successTaskId : e.failureTaskId, d, key, e))
  return row
}

function taskSelect(current: number | null, d: TaskFormDraft, key: 'successTaskId' | 'failureTaskId', e: ExecutorDraft): HTMLSelectElement {
  const sel = el('select', { class: 'input' } as Record<string, string | boolean>)
  sel.appendChild(el('option', { value: '0' }, ['— 不执行 —']))
  // 弹窗内的候选任务：已保存任务 + 当前编辑中新增执行器目标（避免自引用环）
  const savedIds = schedules.map(s => s.id)
  const used = (id: number | null) => {
    if (!id) return false
    return d.executors.some(x => x !== e && (x.taskId === id || x.successTaskId === id || x.failureTaskId === id))
  }
  for (const s of [...schedules].sort((a, b) => a.id - b.id)) {
    if (used(s.id)) continue
    const o = el('option', { value: String(s.id) }, [s.name || `任务 #${s.id}`])
    if (current === s.id) o.selected = true
    sel.appendChild(o)
  }
  if (current && !savedIds.includes(current)) {
    sel.appendChild(el('option', { value: String(current), selected: true }, [`任务 #${current}（已删除）`]))
  }
  sel.onchange = () => { e[key] = Number(sel.value) || null; setModalDirty(true) }
  return sel
}

function renderControlTaskFields(e: ExecutorDraft, d: TaskFormDraft): HTMLElement {
  const sec = el('div', { class: 'flex flex-col gap-3' })
  const taskRow = labelRow('目标任务', el('select', { class: 'input' } as Record<string, string | boolean>))
  const tSel = taskRow.querySelector('select') as HTMLSelectElement
  tSel.appendChild(el('option', { value: '0' }, ['— 请选择 —']))
  for (const s of [...schedules].sort((a, b) => a.id - b.id)) {
    const o = el('option', { value: String(s.id) }, [s.name || `任务 #${s.id}`])
    if (e.taskId === s.id) o.selected = true
    tSel.appendChild(o)
  }
  tSel.onchange = () => { e.taskId = Number(tSel.value) || null; setModalDirty(true) }
  sec.appendChild(taskRow)

  const actRow = labelRow('操作', el('select', { class: 'input' } as Record<string, string | boolean>))
  const aSel = actRow.querySelector('select') as HTMLSelectElement
  const acts: Array<[TaskAction, string]> = [['run', '执行'], ['enable', '启用'], ['disable', '禁用']]
  for (const [v, label] of acts) {
    const o = el('option', { value: v }, [label])
    if (e.taskAction === v) o.selected = true
    aSel.appendChild(o)
  }
  aSel.onchange = () => { e.taskAction = aSel.value as TaskAction; setModalDirty(true) }
  sec.appendChild(actRow)

  sec.appendChild(checkRow('遵循目标任务启用状态（默认关闭：不受目标 enabled 影响）', e.respectEnabled, (v) => { e.respectEnabled = v; setModalDirty(true) }))
  sec.appendChild(checkRow('遵循目标任务日期规则（默认关闭：不受每天/每周/每月影响）', e.respectTimeRule, (v) => { e.respectTimeRule = v; setModalDirty(true) }))
  void d
  return sec
}

function renderScriptFields(e: ExecutorDraft, d: TaskFormDraft, idx: number, body: HTMLElement): HTMLElement {
  const sec = el('div', { class: 'flex flex-col gap-3' })
  const modeRow = labelRow('执行脚本', el('select', { class: 'input' } as Record<string, string | boolean>))
  const mSel = modeRow.querySelector('select') as HTMLSelectElement
  mSel.appendChild(el('option', { value: '' }, ['— 请选择 —']))
  for (const r of execScripts) mSel.appendChild(el('option', { value: String(r.id) }, [r.name]))
  mSel.appendChild(el('option', { value: 'new' }, ['自定义…']))
  if (e.scriptId != null && execScripts.some(r => r.id === e.scriptId)) mSel.value = String(e.scriptId)
  else mSel.value = 'new'
  mSel.onchange = () => {
    const v = mSel.value
    if (v === 'new') { e.scriptPrebuilt = ''; e.scriptId = null }
    else if (v === '') { /* no-op */ }
    else { e.scriptPrebuilt = ''; e.scriptId = Number(v); e.scriptCode = '' }
    setModalDirty(true)
    rerenderExecutorConfig(d, idx, body)
  }
  sec.appendChild(modeRow)

  if (mSel.value === 'new') {
    // 脚本类型：指定本段内联自定义代码是 Shell 还是 Python；切换时同步更新描述
    const typeRow = labelRow('脚本类型', el('select', { class: 'input' } as Record<string, string | boolean>))
    const typeSel = typeRow.querySelector('select') as HTMLSelectElement
    typeSel.appendChild(el('option', { value: 'shell' }, ['Shell（sh）']))
    typeSel.appendChild(el('option', { value: 'python' }, ['Python']))
    if (e.scriptType === 'python') typeSel.value = 'python'
    sec.appendChild(typeRow)
    const ta = el('textarea', { class: 'input w-full font-mono', rows: '5' } as Record<string, string | boolean>)
    ta.value = e.scriptCode || ''
    const codeRow = labelRow('脚本代码', ta)
    sec.appendChild(codeRow)
    const hint = el('div', { class: 'text-xs' })
    hint.style.color = 'rgb(var(--c-ink-muted))'
    const syncHint = () => {
      if (e.scriptType === 'python') {
        ta.placeholder = '# python3 -c 执行，传入参数经 sys.argv[1:] 引用'
        hint.textContent = '按 Python（python3）执行，传入参数经 sys.argv[1:] 引用'
      } else {
        ta.placeholder = '#!/bin/sh\n# sh -c 执行，传入参数经 $1 $2 ... 引用'
        hint.textContent = '按 Shell（sh）执行，传入参数经 $1 $2 ... 引用'
      }
    }
    typeSel.onchange = () => { e.scriptType = typeSel.value; syncHint(); setModalDirty(true) }
    syncHint()
    sec.appendChild(hint)
    ta.oninput = () => { e.scriptCode = ta.value; e.scriptId = null; setModalDirty(true) }
  } else if (e.scriptId != null) {
    const r = execScripts.find(x => x.id === e.scriptId)
    if (r?.code) {
      const tip = el('div', { class: 'text-[11px] whitespace-pre-wrap break-all max-h-24 overflow-y-auto m-0 p-2 rounded' })
      tip.style.background = 'rgb(var(--c-overlay) / 0.3)'
      tip.style.color = 'rgb(var(--c-ink-muted))'
      tip.style.fontFamily = 'var(--font-mono, monospace)'
      tip.textContent = r.code
      sec.appendChild(tip)
    }
  }

  const argsRow = labelRow('脚本传入值', el('input', {
    class: 'input w-full font-mono',
    placeholder: '如: -f ssd  (sh 用 $1 $2 ...，python 用 sys.argv[1:] 引用)',
    value: e.scriptArgs,
  } as Record<string, string>))
  sec.appendChild(argsRow)
  const argsInp = argsRow.lastChild as HTMLInputElement
  argsInp.oninput = () => { e.scriptArgs = argsInp.value; setModalDirty(true) }

  sec.appendChild(retryFields(e, () => rerenderExecutorConfig(d, idx, body)))
  sec.appendChild(followTaskFields(e, d))
  return sec
}

// ================================================================
// 表单控件
// ================================================================
export function formSection(text: string): HTMLElement {
  // 带边框卡片式分区：基本信息 / 触发器清单 / 执行器清单 明显视觉分界（修订版 1.1）
  const sec = el('div', { class: 'rounded-lg border p-3 flex flex-col gap-2' })
  sec.style.borderColor = 'rgb(var(--c-line-subtle))'
  sec.style.background = 'rgb(var(--c-overlay) / 0.15)'
  const title = el('div', { class: 'text-sm font-semibold mb-1' }, [text])
  title.style.color = 'rgb(var(--c-ink))'
  sec.appendChild(title)
  return sec
}

export function labelRow(text: string, input: HTMLElement): HTMLElement {
  const row = el('div', { class: 'flex flex-col gap-1' })
  const label = el('label', { class: 'text-xs font-medium' }, [text])
  label.style.color = 'rgb(var(--c-ink-muted))'
  row.append(label, input)
  return row
}

export function checkRow(text: string, checked: boolean, onChange: (v: boolean) => void): HTMLElement {
  const row = el('label', { class: 'flex items-center gap-2 text-xs cursor-pointer' })
  row.style.color = 'rgb(var(--c-ink))'
  const cb = el('input', { type: 'checkbox', checked } as Record<string, string | boolean>)
  cb.onchange = () => onChange(cb.checked)
  row.append(cb, el('span', {}, [text]))
  return row
}

export function weekCheckboxRow(target: number[], onDirty: () => void): HTMLElement {
  const row = el('div', { class: 'flex flex-wrap gap-2' })
  for (let d = 1; d <= 7; d++) {
    const label = el('label', { class: 'flex items-center gap-1 text-xs cursor-pointer' })
    label.style.color = 'rgb(var(--c-ink))'
    const cb = el('input', { type: 'checkbox', checked: target.includes(d) } as Record<string, string | boolean>)
    cb.onchange = () => {
      const i = target.indexOf(d)
      if (cb.checked && i < 0) target.push(d)
      if (!cb.checked && i >= 0) target.splice(i, 1)
      onDirty()
    }
    label.append(cb, el('span', {}, [WEEK_LABEL[d - 1]]))
    row.appendChild(label)
  }
  return row
}

export function dayCheckboxRow(target: number[], onDirty: () => void): HTMLElement {
  const row = el('div', { class: 'flex flex-wrap gap-1' })
  for (let d = 1; d <= 31; d++) {
    const label = el('label', { class: 'flex items-center gap-0.5 text-xs cursor-pointer' })
    label.style.color = 'rgb(var(--c-ink))'
    const cb = el('input', { type: 'checkbox', checked: target.includes(d) } as Record<string, string | boolean>)
    cb.onchange = () => {
      const i = target.indexOf(d)
      if (cb.checked && i < 0) target.push(d)
      if (!cb.checked && i >= 0) target.splice(i, 1)
      onDirty()
    }
    label.append(cb, el('span', {}, [String(d)]))
    row.appendChild(label)
  }
  return row
}

// 弹窗 body 重渲染（触发器/执行器增删后刷新摘要列表）
function rerenderModalBody(): void {
  if (!modalDraft) return
  const modal = document.getElementById('task-form-modal')
  if (!modal) return
  const draft = modalDraft
  // 重建 body（保留 title 与 footer）
  const foot = modal.lastChild as HTMLElement
  const oldBody = modal.querySelector('div.overflow-y-auto')
  if (oldBody) oldBody.remove()
  const body = el('div', { class: 'overflow-y-auto pr-1 -mr-1 flex flex-col gap-4', style: 'min-height:0' })
  body.style.flex = '1'
  body.appendChild(renderBasicFields(draft))
  body.appendChild(renderTriggerSection(draft))
  body.appendChild(renderExecutorSection(draft))
  modal.insertBefore(body, foot)
}

// ================================================================
// 校验 / 序列化
// ================================================================
function validateTriggerDraft(t: TriggerDraft): string | null {
  if (t.type === 'time') {
    if (t.period === 'once') {
      if (!t.runAt) return '一次性触发需设置执行时刻'
    } else if (t.period === 'loop') {
      const re = /^(\d{2}):(\d{2}):(\d{2})$/
      const m = t.loopInterval.trim().match(re)
      if (!m) return '循环时间格式应为 HH:MM:SS'
      const h = parseInt(m[1], 10), mm = parseInt(m[2], 10), ss = parseInt(m[3], 10)
      if (h > 23 || mm > 59 || ss > 59) return '循环时间范围 HH≤23, MM≤59, SS≤59'
      if (h === 0 && mm === 0 && ss === 0) return '循环时间不能为 00:00:00'
    } else {
      if (!t.time) return '请设置执行时间'
      if (t.period === 'weekly' && t.weekdays.length === 0) return '每周触发至少选择 1 天'
      if (t.period === 'monthly' && t.monthDays.length === 0) return '每月触发至少选择 1 个日期'
    }
  } else if (t.type === 'log') {
    if (t.logEventId != null) return null // 引用全局日志事件资源 OK
    // 内联日志事件
    if (!t.logEventPath.trim()) return '日志路径必填'
    if (!t.logEventRegex.trim()) return '匹配正则必填'
    try { new RegExp(t.logEventRegex.trim()) } catch { return '正则表达式不合法' }
  } else if (t.type === 'monitor') {
    if (t.monitorPrebuilt === 'idle') return null // 预制 OK
    if (t.monitorScriptId != null) return null // 引用资源 OK
    // 内联监控脚本
    if (!t.monitorCode.trim()) return '监控脚本代码不能为空'
    if (t.monitorIntervalSec < 1 || t.monitorIntervalSec > 600) return '监控执行间隔需在 1~600 秒'
  }
  return null
}

function validateExecutorDraft(e: ExecutorDraft): string | null {
  if (e.type === 'fan_control') {
    if (!e.fanId) return '请选择风扇'
    if (e.fanManual && (e.fanPercent < 1 || e.fanPercent > 100)) return '手动转速需在 1~100%'
  } else if (e.type === 'disk_group_control') {
    if (!e.diskGroupId) return '请选择硬盘组'
    if (e.retryEnabled && (e.maxRetries < 1 || e.maxRetries > 10)) return '重试次数需在 1~10'
    if (e.retryEnabled && (e.retryIntervalSec < 1 || e.retryIntervalSec > 3600)) return '重试间隔需在 1~3600 秒'
  } else if (e.type === 'control_task') {
    if (!e.taskId) return '请选择目标任务'
  } else if (e.type === 'exec_script') {
    if (!e.scriptId && !e.scriptCode) return '请选择执行脚本或填写内联代码'
    if (e.retryEnabled && (e.maxRetries < 1 || e.maxRetries > 10)) return '重试次数需在 1~10'
  }
  if (e.delaySec < 0 || e.delaySec > 3600) return '延迟需在 0~3600 秒'
  return null
}

function validateDraft(d: TaskFormDraft): string | null {
  if (!d.name.trim()) return '任务名称必填'
  for (const t of d.triggers) {
    const err = validateTriggerDraft(t)
    if (err) return `触发器「${triggerSummaryDraft(t)}」：${err}`
  }
  if (d.executors.length === 0) return '至少需要 1 个执行器'
  for (const e of d.executors) {
    const err = validateExecutorDraft(e)
    if (err) return `执行器「${executorSummaryDraft(e)}」：${err}`
  }
  return null
}

// 后端会再次迁移/校验；前端按新格式提交
function buildScheduleBody(d: TaskFormDraft): Record<string, unknown> {
  const triggers: ScheduleTrigger[] = d.triggers.map(t => {
    const base: Record<string, unknown> = { type: t.type }
    if (t.type === 'time') {
      base.period = t.period
      if (t.period === 'once') {
        // datetime-local 输出 "YYYY-MM-DDTHH:MM"，Go time.Time JSON 需要 RFC3339
        if (t.runAt) {
          const d = new Date(t.runAt)
          base.runAt = isNaN(d.getTime()) ? t.runAt : d.toISOString()
        }
      } else if (t.period === 'loop') {
        base.loopInterval = t.loopInterval.trim()
      } else {
        base.time = t.time
        if (t.period === 'weekly') base.weekdays = [...t.weekdays]
        if (t.period === 'monthly') base.monthDays = [...t.monthDays]
      }
    } else if (t.type === 'log') {
      if (t.logEventId != null) base.logEventId = t.logEventId
      else {
        base.logEventPath = t.logEventPath.trim()
        base.logEventRegex = t.logEventRegex.trim()
        base.logEventCooldown = t.logEventCooldown
        base.logEventCooldownUnit = t.logEventCooldownUnit
        base.logEventConsecutive = t.logEventCooldown === 0 ? 1 : t.logEventConsecutive
        base.logEventRotate = t.logEventRotate
      }
    } else if (t.type === 'monitor') {
      if (t.monitorPrebuilt === 'idle') base.monitorPrebuilt = 'idle'
      else if (t.monitorScriptId != null) base.monitorScriptId = t.monitorScriptId
      else {
        base.monitorCode = t.monitorCode
        if (t.monitorScriptType) base.monitorScriptType = t.monitorScriptType
      }
      base.monitorScriptArgs = t.monitorScriptArgs.trim()
      base.monitorIntervalSec = t.monitorIntervalSec
    }
    return base as unknown as ScheduleTrigger
  })

  const executors: ScheduleExecutor[] = d.executors.map(e => {
    const ex: Record<string, unknown> = { type: e.type, delaySec: e.delaySec }
    if (e.type === 'fan_control') {
      ex.fanId = e.fanId
      if (e.fanEnabled != null) ex.fanEnabled = e.fanEnabled
      ex.fanManual = e.fanManual
      if (e.fanManual) ex.fanPercent = e.fanPercent
      else if (e.fanCurve) ex.fanCurve = e.fanCurve
    } else if (e.type === 'disk_group_control') {
      ex.diskGroupId = e.diskGroupId
      ex.diskAction = e.diskAction
      if (e.diskAction === 'offline') {
        if (e.killOccupied) ex.killOccupied = true
        if (e.forceOff) ex.forceOff = true
      }
      if (e.retryEnabled) {
        ex.retryEnabled = true
        ex.maxRetries = e.maxRetries
        ex.retryIntervalSec = e.retryIntervalSec
      }
    } else if (e.type === 'control_task') {
      ex.taskId = e.taskId
      ex.taskAction = e.taskAction
      if (e.respectEnabled) ex.respectEnabled = true
      if (e.respectTimeRule) ex.respectTimeRule = true
    } else if (e.type === 'exec_script') {
      if (e.scriptId) ex.scriptId = e.scriptId
      else {
        ex.scriptCode = e.scriptCode
        if (e.scriptType) ex.scriptType = e.scriptType
      }
      ex.scriptArgs = e.scriptArgs.trim()
      if (e.retryEnabled) {
        ex.retryEnabled = true
        ex.maxRetries = e.maxRetries
        ex.retryIntervalSec = e.retryIntervalSec
      }
    }
    if (e.successTaskId) ex.successTaskId = e.successTaskId
    if (e.failureTaskId) ex.failureTaskId = e.failureTaskId
    return ex as unknown as ScheduleExecutor
  })

  return {
    name: d.name.trim(),
    description: d.description.trim(),
    enabled: d.enabled,
    triggers,
    executors,
  }
}

// 存量任务 → 草稿（后端已迁移，前端兜底旧字段）
function draftFromSchedule(sc: Schedule): TaskFormDraft {
  const d = emptyDraft()
  d.name = sc.name
  d.description = sc.description || ''
  d.enabled = sc.enabled

  const trigs = effectiveTriggers(sc)
  if (trigs.length > 0) {
    d.triggers = trigs.map(t => {
      const td = defaultTrigger(t.type === 'log' ? 'log' : t.type === 'monitor' ? 'monitor' : 'time')
      td.type = (t.type === 'log' ? 'log' : t.type === 'monitor' ? 'monitor' : 'time') as ScheduleTriggerType
      if (t.period) td.period = t.period
      if (t.runAt) td.runAt = toLocalInput(t.runAt)
      if (t.time) td.time = t.time
      if (t.weekdays) td.weekdays = [...t.weekdays]
      if (t.monthDays) td.monthDays = [...t.monthDays]
      if (t.loopInterval) td.loopInterval = t.loopInterval
      if (t.logEventId != null) td.logEventId = t.logEventId
      else if (t.logEventPath) {
        // 内联日志事件
        td.logEventPath = t.logEventPath
        td.logEventRegex = t.logEventRegex || ''
        td.logEventCooldown = t.logEventCooldown ?? 0
        td.logEventCooldownUnit = (t.logEventCooldownUnit as CooldownUnit) || 'minute'
        td.logEventConsecutive = t.logEventConsecutive ?? 1
        td.logEventRotate = t.logEventRotate ?? true
      }
      if (t.monitorPrebuilt) {
        const matched = monitorScripts.find(r => r.name.includes('闲置') || r.name.includes('预制'))
        if (matched) { td.monitorScriptId = matched.id }
        else { td.monitorPrebuilt = 'idle' }
      }
      else if (t.monitorScriptId != null) td.monitorScriptId = t.monitorScriptId
      else if (t.monitorCode) {
        td.monitorCode = t.monitorCode
        td.monitorScriptType = t.monitorScriptType || (/^\s*#!.*python/i.test(t.monitorCode) ? 'python' : '')
      }
      if (t.monitorScriptArgs) td.monitorScriptArgs = t.monitorScriptArgs
      if (t.monitorIntervalSec) td.monitorIntervalSec = t.monitorIntervalSec
      // 旧事件型
      if (t.diskGroups) td.diskGroups = [...t.diskGroups]
      if (t.allDay != null) td.allDay = t.allDay
      if (t.timeRange) td.timeRange = t.timeRange
      return td
    })
  } else {
    // 顶层便捷字段迁移
    if (sc.scheduleType === 'one_time' && sc.runAt) {
      const td = defaultTrigger('time')
      td.period = 'once'
      td.runAt = toLocalInput(sc.runAt)
      d.triggers.push(td)
    } else if (sc.scheduleType === 'daily' && sc.time) {
      const td = defaultTrigger('time')
      td.period = 'daily'
      td.time = sc.time
      d.triggers.push(td)
    } else if (sc.scheduleType === 'weekly' && sc.time) {
      const td = defaultTrigger('time')
      td.period = 'weekly'
      td.time = sc.time
      td.weekdays = sc.weekdays ? [...sc.weekdays] : [1]
      d.triggers.push(td)
    } else if (sc.scheduleType === 'monthly' && sc.time) {
      const td = defaultTrigger('time')
      td.period = 'monthly'
      td.time = sc.time
      td.monthDays = sc.monthDays ? [...sc.monthDays] : [1]
      d.triggers.push(td)
    }
    // 旧 sleep/idle 硬盘事件型触发任务（scheduleType=trigger + triggerKind）已废弃：
    // 编辑时不回填为 log/monitor 预制触发器，旧配置直接抛弃。
  }

  // 执行器
  const exes = effectiveExecutors(sc)
  if (exes.length > 0) {
    d.executors = exes.map(e => {
      const ed = defaultExecutor(e.type)
      ed.type = e.type
      if (e.delaySec) ed.delaySec = e.delaySec
      ed.fanId = e.fanId ?? null
      ed.fanEnabled = e.fanEnabled ?? null
      ed.fanManual = !!e.fanManual
      ed.fanPercent = e.fanPercent ?? 50
      ed.fanCurve = (e.fanCurve as FanCurveMode | '') || ''
      ed.diskGroupId = e.diskGroupId ?? null
      ed.diskAction = e.diskAction || 'offline'
      ed.killOccupied = !!e.killOccupied
      ed.forceOff = !!e.forceOff
      ed.retryEnabled = !!e.retryEnabled
      ed.maxRetries = e.maxRetries ?? 3
      ed.retryIntervalSec = e.retryIntervalSec ?? 10
      ed.taskId = e.taskId ?? null
      ed.taskAction = e.taskAction || 'run'
      ed.respectEnabled = !!e.respectEnabled
      ed.respectTimeRule = !!e.respectTimeRule
      // scriptPrebuilt 无实际运行时效果，尝试匹配 execScripts 预制条目
      if (e.scriptPrebuilt) {
        const matched = execScripts.find(r => r.name.includes('预制'))
        if (matched) ed.scriptId = matched.id
      } else {
        ed.scriptId = e.scriptId ?? null
      }
      ed.scriptCode = e.scriptCode || ''
      ed.scriptType = e.scriptType || (/^\s*#!.*python/i.test(e.scriptCode || '') ? 'python' : '')
      ed.scriptArgs = e.scriptArgs || ''
      ed.successTaskId = e.successTaskId ?? null
      ed.failureTaskId = e.failureTaskId ?? null
      return ed
    })
  } else {
    // 旧 Actions / 顶层字段迁移
    const fromActions = (as: Schedule['actions'], type: 'success' | 'failure'): ExecutorDraft[] => {
      if (!as || as.length === 0) return []
      return as.map(a => {
        const ed = defaultExecutor(
          a.type === 'fan_curve' ? 'fan_control' : a.type === 'disk_power' ? 'disk_group_control' : 'control_task'
        )
        if (a.type === 'fan_curve') {
          ed.fanId = (a.fanChannels && a.fanChannels[0]) ?? null
          ed.fanCurve = (a.fanMode as FanCurveMode) || ''
        } else if (a.type === 'disk_power') {
          ed.diskGroupId = (a.diskGroups && a.diskGroups[0]) ?? null
          ed.diskAction = a.diskAction || 'offline'
          ed.forceOff = !!a.forceOff
        } else {
          ed.taskId = a.taskId ?? null
          ed.taskAction = a.type === 'enable_task' ? 'enable' : a.type === 'disable_task' ? 'disable' : 'run'
          ed.respectEnabled = !!a.respectEnabled
          ed.respectTimeRule = !!a.respectTimeRule
          ed.delaySec = a.delaySec ?? 0
        }
        if (type === 'success') ed.successTaskId = a.taskId ?? null
        return ed
      })
    }
    d.executors = fromActions(sc.actions, 'success')
    if (d.executors.length === 0 && sc.category === 'fan') {
      const ed = defaultExecutor('fan_control')
      ed.fanId = (sc.fanChannels && sc.fanChannels[0]) ?? null
      ed.fanCurve = sc.fanMode || ''
      d.executors.push(ed)
    } else if (d.executors.length === 0 && sc.category === 'disk') {
      const ed = defaultExecutor('disk_group_control')
      ed.diskGroupId = (sc.diskGroups && sc.diskGroups[0]) ?? null
      ed.diskAction = sc.diskAction || 'offline'
      ed.forceOff = !!sc.forceOff
      d.executors.push(ed)
    }
  }
  return d
}

function toLocalInput(iso: string): string {
  const d = new Date(iso)
  if (isNaN(d.getTime())) return iso
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

// ================================================================
// 旧书签兼容：#/schedule/new、#/schedule/:id（渲染列表页 + 自动打开弹窗）
// ================================================================
export function renderScheduleForm(id?: number): HTMLElement {
  const root = renderSchedules()
  void loadAll().then(() => {
    if (id === undefined) openTaskForm(null)
    else {
      const sc = schedules.find(s => s.id === id)
      if (sc) openTaskForm(sc)
      else toast('任务不存在', 'error')
    }
  })
  return root
}

export function resetScheduleFormState(): void {
  // 弹窗挂在 body 上，重置时一并关闭
  if (modalCleanup) {
    if (dirty) void confirmDialog('有未保存的修改，将丢弃。', '未保存的修改', true).then(ok => {
      if (ok) { dirty = false; modalCleanup?.(); modalDraft = null }
    })
    else { modalCleanup(); modalDraft = null }
  }
  dirty = false
}

// 供 schedule-resources.ts 引用（页面重渲染：触发 main.ts 的 rerender 事件全量重建）
export function rerenderSchedulePage(): void {
  document.dispatchEvent(new Event('rerender'))
}

// 导出让 schedule-resources.ts 复用
export function getScheduleContext(): {
  schedules: Schedule[]
  monitorScripts: MonitorScript[]
  execScripts: ExecScript[]
  logEvents: LogEventResource[]
  loadAll: (force?: boolean) => Promise<void>
} {
  return { schedules, monitorScripts, execScripts, logEvents, loadAll }
}

// badge 导出兼容（列表页内部使用）
void statusBadge
void badge
