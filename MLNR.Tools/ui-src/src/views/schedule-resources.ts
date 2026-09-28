// 定时计划资源页（M14）：监控脚本 / 执行脚本 / 日志事件
// 三类资源均支持：列表（预制只读 + 自定义）、添加/编辑弹窗、测试按钮、删除（引用检查 → force）
import { api } from '../api'
import { el, svgIcon, toast, confirmDialog, unsavedChoice } from '../ui'
import { formSection, labelRow, checkRow, getScheduleContext, rerenderSchedulePage } from './schedules'
import type { MonitorScript, ExecScript, LogEventResource, CooldownUnit } from '../types'

// 资源弹窗未保存检测（修订版 2.3）：dirty 时关闭需三选项确认
let resOverlay: HTMLElement | null = null
let resDirty = false
let resSaveFn: (() => Promise<void>) | null = null

/** 关闭资源弹窗（若 dirty 弹未保存三选项）。返回 true 表示可离开当前页面/切换标签。 */
export async function closeResModalIfOpen(): Promise<boolean> {
  if (!resOverlay) return true
  if (!resDirty) {
    resOverlay.remove()
    resOverlay = null
    resSaveFn = null
    return true
  }
  const ch = await unsavedChoice()
  if (ch === 'stay') return false
  if (ch === 'save' && resSaveFn) {
    await resSaveFn()
    return !resOverlay // 保存成功已关闭；失败保持打开
  }
  resOverlay.remove()
  resOverlay = null
  resSaveFn = null
  return true
}

export function renderScheduleResourcesPage(
  tab: 'monitor' | 'exec' | 'logevent'
): HTMLElement {
  if (tab === 'monitor') return renderMonitorScriptsTab()
  if (tab === 'logevent') return renderLogEventsTab()
  return renderExecScriptsTab()
}

// ================================================================
// 通用：删除资源（409 引用检查 → 弹窗确认 force）
// ================================================================
type DeleteFn = (force: boolean) => Promise<{ ok: boolean; clearedReferences?: number }>

async function deleteResource(
  name: string,
  kindLabel: string,
  del: DeleteFn,
  afterDelete: () => void,
): Promise<void> {
  try {
    const r = await del(false)
    if (r.ok) {
      toast(`${kindLabel}「${name}」已删除`, 'success')
      afterDelete()
      return
    }
  } catch (err) {
    const msg = err instanceof Error ? err.message : String(err)
    // 409 引用冲突：列出被哪些任务引用
    const m = /被.*?引用|referenced/i.exec(msg)
    if (!m) { toast(`删除失败：${msg}`, 'error'); return }
    const refMatch = /\[([^\]]+)\]/.exec(msg)
    const refs = refMatch ? refMatch[1].split(',').map(s => s.trim()).filter(Boolean) : []
    const ok = await confirmDialog(
      refs.length > 0
        ? `${kindLabel}「${name}」正被以下任务引用：${refs.join('、')}。\n\n删除后将自动清除这些任务中的引用（任务本身保留），确定继续？`
        : `${kindLabel}「${name}」正被其他任务引用。\n\n删除后将自动清除引用（任务本身保留），确定继续？`,
      '资源被引用',
      true,
    )
    if (!ok) return
    try {
      await del(true)
      toast(`${kindLabel}「${name}」已删除，已清除引用`, 'success')
      afterDelete()
    } catch (err2) {
      toast('删除失败：' + (err2 instanceof Error ? err2.message : String(err2)), 'error')
    }
  }
}

// ================================================================
// 监控脚本
// ================================================================
function renderMonitorScriptsTab(): HTMLElement {
  const box = el('div', { class: 'flex flex-col gap-3' })
  const head = el('div', { class: 'flex items-center justify-between' })
  const hint = el('span', { class: 'text-xs' }, ['监控事件执行脚本（Shell / Python），按执行间隔运行，有输出即触发（间隔 1~600 秒）'])
  hint.style.color = 'rgb(var(--c-ink-muted))'
  const add = el('button', { class: 'btn btn-primary btn-sm' }, [svgIcon('plus', 14), '添加监控脚本'])
  add.onclick = () => openScriptModal('monitor', null)
  head.append(hint, add)
  box.appendChild(head)

  const ctx = getScheduleContext()
  if (ctx.monitorScripts.length === 0) {
    const empty = el('div', { class: 'card p-6 text-center text-sm' })
    empty.style.color = 'rgb(var(--c-ink-muted))'
    empty.textContent = '暂无监控脚本'
    box.appendChild(empty)
  } else {
    for (const s of ctx.monitorScripts) box.appendChild(scriptRow('monitor', s))
  }
  return box
}

function renderExecScriptsTab(): HTMLElement {
  const box = el('div', { class: 'flex flex-col gap-3' })
  const head = el('div', { class: 'flex items-center justify-between' })
  const hint = el('span', { class: 'text-xs' }, ['执行脚本（Shell / Python），可被任务执行器调用'])
  hint.style.color = 'rgb(var(--c-ink-muted))'
  const add = el('button', { class: 'btn btn-primary btn-sm' }, [svgIcon('plus', 14), '添加执行脚本'])
  add.onclick = () => openScriptModal('exec', null)
  head.append(hint, add)
  box.appendChild(head)

  const ctx = getScheduleContext()
  if (ctx.execScripts.length === 0) {
    const empty = el('div', { class: 'card p-6 text-center text-sm' })
    empty.style.color = 'rgb(var(--c-ink-muted))'
    empty.textContent = '暂无执行脚本'
    box.appendChild(empty)
  } else {
    for (const s of ctx.execScripts) box.appendChild(scriptRow('exec', s))
  }
  return box
}

function scriptRow(kind: 'monitor' | 'exec', s: MonitorScript | ExecScript): HTMLElement {
  const card = el('div', { class: 'card p-3' })
  const top = el('div', { class: 'flex items-center justify-between gap-2 mb-2' })
  const left = el('div', { class: 'flex items-center gap-2 flex-wrap min-w-0' })
  const nameEl = el('span', { class: 'font-semibold text-sm truncate' }, [s.name])
  nameEl.style.color = 'rgb(var(--c-ink))'
  left.appendChild(nameEl)
  const badge = el('span', { class: 'text-[10px] px-1.5 py-0.5 rounded font-mono shrink-0' }, [
    s.scriptType === 'python' ? 'py' : s.scriptType === 'shell' ? 'sh' : 'auto',
  ])
  badge.style.background = 'rgb(var(--c-overlay) / 0.3)'
  badge.style.color = 'rgb(var(--c-ink-muted))'
  left.appendChild(badge)
  top.appendChild(left)

  const ops = el('div', { class: 'flex items-center gap-1 shrink-0' })
  const testBtn = el('button', { class: 'btn btn-sm', title: '测试脚本' }, [svgIcon('bolt', 12), '测试'])
  testBtn.onclick = () => testScript(kind, s)
  const editBtn = el('button', { class: 'btn btn-sm', title: '编辑' }, [svgIcon('edit', 13)])
  editBtn.onclick = () => {
    openScriptModal(kind, s)
  }
  const delBtn = el('button', { class: 'btn btn-sm btn-danger', title: '删除' }, [svgIcon('trash', 13)])
  delBtn.onclick = () => {
    void deleteResource(
      s.name,
      kind === 'monitor' ? '监控脚本' : '执行脚本',
      (force) => kind === 'monitor' ? api.deleteMonitorScript(s.id, force) : api.deleteExecScript(s.id, force),
      () => { void reloadAndRender() },
    )
  }
  ops.append(testBtn, editBtn, delBtn)
  top.appendChild(ops)
  card.appendChild(top)

  const code = el('pre', { class: 'text-[11px] whitespace-pre-wrap break-all max-h-24 overflow-y-auto m-0 p-2 rounded' })
  code.style.background = 'rgb(var(--c-overlay) / 0.3)'
  code.style.color = 'rgb(var(--c-ink-muted))'
  code.style.fontFamily = 'var(--font-mono, monospace)'
  code.textContent = s.code || '（空脚本）'
  card.appendChild(code)
  return card
}

function openScriptModal(kind: 'monitor' | 'exec', s: MonitorScript | ExecScript | null): void {
  const editing = !!s
  const draft = {
    name: s?.name ?? '',
    code: s?.code ?? '',
    scriptType: s?.scriptType ?? (s && /^\s*#!.*python/i.test(s.code) ? 'python' : ''),
    args: s?.args ?? '',
  }
  const overlay = el('div', { class: 'fixed inset-0 z-50 flex items-center justify-center p-4' })
  overlay.style.background = 'rgb(var(--c-overlay) / 0.6)'
  resOverlay = overlay
  resDirty = false
  const modal = el('div', { class: 'card w-[560px] max-w-full flex flex-col p-5' })
  modal.style.maxHeight = '90vh'
  const titleEl = el('h3', { class: 'font-semibold text-base mb-3' }, [editing ? '编辑脚本' : '新建脚本'])
  titleEl.style.color = 'rgb(var(--c-ink))'
  const body = el('div', { class: 'flex flex-col gap-3 overflow-y-auto pr-1 -mr-1', style: 'min-height:0' })
  body.style.flex = '1'

  const sec = formSection(kind === 'monitor' ? '监控脚本' : '执行脚本')
  // 脚本类型：创建时选定后，编辑与调用均按此类型执行；切换时同步更新下方描述
  const typeRow = labelRow('脚本类型', el('select', { class: 'input' } as Record<string, string | boolean>))
  const typeSel = typeRow.querySelector('select') as HTMLSelectElement
  typeSel.appendChild(el('option', { value: 'shell' }, ['Shell（sh）']))
  typeSel.appendChild(el('option', { value: 'python' }, ['Python']))
  if (draft.scriptType === 'python') typeSel.value = 'python'
  sec.appendChild(typeRow)
  sec.appendChild(labelRow('脚本名称', el('input', { class: 'input w-full', placeholder: '必填', value: draft.name } as Record<string, string>)))
  const nameInput = sec.lastChild?.lastChild as HTMLInputElement
  nameInput.oninput = () => { draft.name = nameInput.value; resDirty = true }
  const ta = el('textarea', { class: 'input w-full font-mono', rows: '10' } as Record<string, string | boolean>)
  ta.value = draft.code
  const codeHint = el('div', { class: 'text-xs' })
  codeHint.style.color = 'rgb(var(--c-ink-muted))'
  const syncCodeHint = () => {
    if (draft.scriptType === 'python') {
      ta.placeholder = '# python3 -c 执行，参数用 sys.argv[1:] 引用\n# 例：import sys; print(sys.argv[1:])'
      codeHint.textContent = '按 Python（python3）执行；测试/调用时传入参数经 sys.argv[1:] 引用'
    } else {
      ta.placeholder = '#!/bin/sh\n# sh -c 执行，参数用 $1 $2 ... 引用\n# 例：echo "arg=$1"'
      codeHint.textContent = '按 Shell（sh）执行；测试/调用时传入参数经 $1 $2 ... 引用'
    }
  }
  typeSel.onchange = () => { draft.scriptType = typeSel.value; syncCodeHint(); resDirty = true }
  syncCodeHint()
  sec.appendChild(labelRow('脚本内容', ta))
  sec.appendChild(codeHint)
  ta.oninput = () => { draft.code = ta.value; resDirty = true }
  // 传入参数：测试/调用时给脚本传参（部分脚本无参无法工作）
  const argsRow = labelRow('传入参数', el('input', { class: 'input w-full font-mono', placeholder: '如: -f ssd  (sh 用 $1 $2 ...，python 用 sys.argv[1:] 引用，可留空)', value: draft.args } as Record<string, string>))
  sec.appendChild(argsRow)
  const argsInp = argsRow.lastChild as HTMLInputElement
  argsInp.oninput = () => { draft.args = argsInp.value; resDirty = true }
  body.appendChild(sec)

  // 测试区
  const testWrap = el('div', { class: 'flex flex-col gap-1' })
  const testRow = el('div', { class: 'flex items-center gap-2' })
  const testBtn = el('button', { class: 'btn btn-sm' }, [svgIcon('bolt', 12), '测试（root / 30 秒超时 / dry-run）'])
  const testOut = el('pre', { class: 'text-[11px] whitespace-pre-wrap break-all max-h-24 overflow-y-auto m-0 p-2 rounded hidden' })
  testOut.style.background = 'rgb(var(--c-overlay) / 0.3)'
  testOut.style.color = 'rgb(var(--c-ink-muted))'
  testOut.style.fontFamily = 'var(--font-mono, monospace)'
  testBtn.onclick = async () => {
    if (!draft.code.trim()) { toast('请先填写脚本内容', 'error'); return }
    testBtn.disabled = true
    try {
      const r = kind === 'monitor'
        ? await api.testMonitorScript(draft.code, draft.scriptType, draft.args.trim())
        : await api.testExecScript(draft.code, draft.scriptType, draft.args.trim())
      testOut.classList.remove('hidden')
      if (r.error) {
        testOut.textContent = '测试失败：' + r.error
        testOut.style.color = 'rgb(var(--c-danger))'
      } else {
        testOut.textContent = r.output || '（无输出）'
        testOut.style.color = 'rgb(var(--c-ink-muted))'
      }
    } catch (err) {
      testOut.classList.remove('hidden')
      testOut.textContent = '测试请求失败：' + (err instanceof Error ? err.message : String(err))
      testOut.style.color = 'rgb(var(--c-danger))'
    } finally {
      testBtn.disabled = false
    }
  }
  testRow.appendChild(testBtn)
  testWrap.append(testRow, testOut)
  body.appendChild(testWrap)

  const foot = el('div', { class: 'flex justify-end gap-2 pt-3 border-t mt-2' })
  foot.style.borderColor = 'rgb(var(--c-line-subtle))'
  const cancel = el('button', { class: 'btn' }, ['取消'])
  const save = el('button', { class: 'btn btn-primary' }, ['保存'])
  const saveFn = async (): Promise<void> => {
    if (!draft.name.trim()) { toast('脚本名称必填', 'error'); return }
    if (!draft.code.trim()) { toast('脚本内容不能为空', 'error'); return }
    save.disabled = true
    try {
      const payload = { name: draft.name.trim(), code: draft.code, scriptType: draft.scriptType, args: draft.args.trim() }
      if (editing && s) {
        if (kind === 'monitor') await api.updateMonitorScript(s.id, payload)
        else await api.updateExecScript(s.id, payload)
      } else {
        if (kind === 'monitor') await api.createMonitorScript(payload)
        else await api.createExecScript(payload)
      }
      toast('脚本已保存', 'success')
      resDirty = false
      resOverlay = null
      resSaveFn = null
      overlay.remove()
      await reloadAndRender()
    } catch (err) {
      toast('保存失败：' + (err instanceof Error ? err.message : String(err)), 'error')
    } finally {
      save.disabled = false
    }
  }
  resSaveFn = saveFn
  const close = async (): Promise<void> => {
    if (!resDirty) { resOverlay = null; resSaveFn = null; overlay.remove(); return }
    const ch = await unsavedChoice()
    if (ch === 'stay') return
    if (ch === 'save') { await saveFn(); return }
    resDirty = false
    resOverlay = null
    resSaveFn = null
    overlay.remove()
  }
  cancel.onclick = () => void close()
  save.onclick = () => void saveFn()
  foot.append(cancel, save)
  modal.append(titleEl, body, foot)
  overlay.appendChild(modal)
  overlay.onclick = (e) => { if (e.target === overlay) void close() }
  document.body.appendChild(overlay)
}

async function testScript(kind: 'monitor' | 'exec', s: MonitorScript | ExecScript): Promise<void> {
  try {
    const r = kind === 'monitor'
      ? await api.testMonitorScript(s.code, s.scriptType, s.args?.trim())
      : await api.testExecScript(s.code, s.scriptType, s.args?.trim())
    if (r.error) toast('测试失败：' + r.error, 'error')
    else toast('测试通过' + (r.output ? '（' + r.output.slice(0, 60) + '…）' : '（无输出）'), 'success')
  } catch (err) {
    toast('测试失败：' + (err instanceof Error ? err.message : String(err)), 'error')
  }
}

// ================================================================
// 日志事件
// ================================================================
function renderLogEventsTab(): HTMLElement {
  const box = el('div', { class: 'flex flex-col gap-3' })
  const head = el('div', { class: 'flex items-center justify-between' })
  const hint = el('span', { class: 'text-xs' }, ['日志事件：匹配系统日志追加行，连续命中即触发（支持轮转、冷却）'])
  hint.style.color = 'rgb(var(--c-ink-muted))'
  const add = el('button', { class: 'btn btn-primary btn-sm' }, [svgIcon('plus', 14), '添加日志事件'])
  add.onclick = () => openLogEventModal(null)
  head.append(hint, add)
  box.appendChild(head)

  const ctx = getScheduleContext()
  if (ctx.logEvents.length === 0) {
    const empty = el('div', { class: 'card p-6 text-center text-sm' })
    empty.style.color = 'rgb(var(--c-ink-muted))'
    empty.textContent = '暂无日志事件'
    box.appendChild(empty)
  } else {
    for (const r of ctx.logEvents) box.appendChild(logEventRow(r))
  }
  return box
}

function logEventRow(r: LogEventResource): HTMLElement {
  const card = el('div', { class: 'card p-3' })
  const top = el('div', { class: 'flex items-center justify-between gap-2 mb-2' })
  const left = el('div', { class: 'flex items-center gap-2 flex-wrap min-w-0' })
  const nameEl = el('span', { class: 'font-semibold text-sm truncate' }, [r.name])
  nameEl.style.color = 'rgb(var(--c-ink))'
  left.appendChild(nameEl)
  top.appendChild(left)

  const ops = el('div', { class: 'flex items-center gap-1 shrink-0' })
  const testBtn = el('button', { class: 'btn btn-sm', title: '测试匹配（仅校验，不消费日志）' }, [svgIcon('bolt', 12), '测试'])
  testBtn.onclick = () => testLogEvent(r)
  const editBtn = el('button', { class: 'btn btn-sm', title: '编辑' }, [svgIcon('edit', 13)])
  editBtn.onclick = () => {
    openLogEventModal(r)
  }
  const delBtn = el('button', { class: 'btn btn-sm btn-danger', title: '删除' }, [svgIcon('trash', 13)])
  delBtn.onclick = () => {
    void deleteResource(r.name, '日志事件', (force) => api.deleteLogEvent(r.id, force), () => { void reloadAndRender() })
  }
  ops.append(testBtn, editBtn, delBtn)
  top.appendChild(ops)
  card.appendChild(top)

  const meta = el('div', { class: 'text-[11px] flex flex-col gap-0.5' })
  meta.style.color = 'rgb(var(--c-ink-muted))'
  meta.style.fontFamily = 'var(--font-mono, monospace)'
  const cooldownTxt = r.cooldown > 0 ? `${r.cooldown}${r.cooldownUnit === 'second' ? ' 秒' : r.cooldownUnit === 'minute' ? ' 分钟' : ' 小时'}` : '无冷却'
  meta.append(
    el('div', {}, [`路径 ${r.path}`]),
    el('div', {}, [`匹配 ${r.regex}`]),
    el('div', {}, [`冷却 ${cooldownTxt} · 连续命中 ${r.consecutive} 次 · ${r.rotate ? '支持轮转' : '不轮转'}`]),
  )
  card.appendChild(meta)
  return card
}

function openLogEventModal(r: LogEventResource | null): void {
  const editing = !!r
  const d = {
    name: r?.name ?? '',
    path: r?.path ?? '',
    regex: r?.regex ?? '',
    cooldown: r?.cooldown ?? 0,
    cooldownUnit: (r?.cooldownUnit ?? 'minute') as CooldownUnit,
    consecutive: r?.consecutive ?? 1,
    rotate: r?.rotate ?? true,
  }
  const overlay = el('div', { class: 'fixed inset-0 z-50 flex items-center justify-center p-4' })
  overlay.style.background = 'rgb(var(--c-overlay) / 0.6)'
  resOverlay = overlay
  resDirty = false
  const modal = el('div', { class: 'card w-[560px] max-w-full flex flex-col p-5' })
  modal.style.maxHeight = '90vh'
  const titleEl = el('h3', { class: 'font-semibold text-base mb-3' }, [editing ? '编辑日志事件' : '新建日志事件'])
  titleEl.style.color = 'rgb(var(--c-ink))'
  const body = el('div', { class: 'flex flex-col gap-3 overflow-y-auto pr-1 -mr-1', style: 'min-height:0' })
  body.style.flex = '1'

  const sec = formSection('日志事件')
  sec.appendChild(labelRow('事件名称', el('input', { class: 'input w-full', placeholder: '必填', value: d.name } as Record<string, string>)))
  const nameInput = sec.lastChild?.lastChild as HTMLInputElement
  nameInput.oninput = () => { d.name = nameInput.value; resDirty = true }
  sec.appendChild(labelRow('日志路径', el('input', { class: 'input w-full font-mono', placeholder: '/var/log/syslog', value: d.path } as Record<string, string>)))
  const pathInput = sec.lastChild?.lastChild as HTMLInputElement
  pathInput.oninput = () => { d.path = pathInput.value; resDirty = true }
  sec.appendChild(labelRow('匹配正则', el('input', { class: 'input w-full font-mono', placeholder: '如：hdparm.*(sleeping|idle)', value: d.regex } as Record<string, string>)))
  const regexInput = sec.lastChild?.lastChild as HTMLInputElement
  regexInput.oninput = () => { d.regex = regexInput.value; resDirty = true }

  // 冷却 + 单位 + 连续命中 + 轮转
  const row1 = el('div', { class: 'flex gap-3' })
  const coolBox = el('div', { class: 'flex flex-col gap-1', style: 'flex:2' })
  const coolLabel = el('label', { class: 'text-xs font-medium' }, ['冷却时间'])
  coolLabel.style.color = 'rgb(var(--c-ink-muted))'
  const coolInp = el('input', { class: 'input w-full', type: 'number', min: '0', max: '99', value: String(d.cooldown) } as Record<string, string | boolean>)
  coolInp.oninput = () => { d.cooldown = Math.min(99, Math.max(0, Number(coolInp.value) || 0)); resDirty = true }
  coolBox.append(coolLabel, coolInp)
  const unitBox = el('div', { class: 'flex flex-col gap-1', style: 'flex:1' })
  const unitLabel = el('label', { class: 'text-xs font-medium' }, ['单位'])
  unitLabel.style.color = 'rgb(var(--c-ink-muted))'
  const unitSel = el('select', { class: 'input' } as Record<string, string | boolean>)
  for (const [v, label] of [['second', '秒'], ['minute', '分钟'], ['hour', '小时']] as Array<[CooldownUnit, string]>) {
    const o = el('option', { value: v }, [label])
    if (v === d.cooldownUnit) o.selected = true
    unitSel.appendChild(o)
  }
  unitSel.onchange = () => { d.cooldownUnit = unitSel.value as CooldownUnit; resDirty = true }
  unitBox.append(unitLabel, unitSel)
  row1.append(coolBox, unitBox)
  sec.appendChild(row1)

  const row2 = el('div', { class: 'flex gap-3 items-end' })
  const conBox = el('div', { class: 'flex flex-col gap-1', style: 'flex:1' })
  const conLabel = el('label', { class: 'text-xs font-medium' }, ['连续命中次数'])
  conLabel.style.color = 'rgb(var(--c-ink-muted))'
  const conInp = el('input', { class: 'input w-full', type: 'number', min: '1', max: '9', value: String(d.consecutive) } as Record<string, string | boolean>)
  conInp.oninput = () => { d.consecutive = Math.min(9, Math.max(1, Number(conInp.value) || 1)); resDirty = true }
  conBox.append(conLabel, conInp)
  row2.appendChild(conBox)
  const rotateWrap = el('div', { class: 'pb-1' })
  rotateWrap.appendChild(checkRow('支持日志轮转（rotate）', d.rotate, (v) => { d.rotate = v; resDirty = true }))
  row2.appendChild(rotateWrap)
  sec.appendChild(row2)

  const tip = el('div', { class: 'text-xs px-3 py-2 rounded' })
  tip.style.background = 'rgb(var(--c-overlay) / 0.3)'
  tip.style.color = 'rgb(var(--c-ink-subtle))'
  tip.textContent = '冷却 0 表示禁用冷却（连续命中强制按 1 次计）；启用时刻为读取起点，重启后跳过已有内容。'
  sec.appendChild(tip)
  body.appendChild(sec)

  // 测试区
  const testRow = el('div', { class: 'flex items-center gap-2' })
  const testBtn = el('button', { class: 'btn btn-sm' }, [svgIcon('bolt', 12), '测试匹配（仅校验，不改变读取位置）'])
  const testOut = el('pre', { class: 'text-[11px] whitespace-pre-wrap break-all max-h-24 overflow-y-auto m-0 p-2 rounded hidden' })
  testOut.style.background = 'rgb(var(--c-overlay) / 0.3)'
  testOut.style.color = 'rgb(var(--c-ink-muted))'
  testOut.style.fontFamily = 'var(--font-mono, monospace)'
  testBtn.onclick = async () => {
    if (!d.path.trim()) { toast('请填写日志路径', 'error'); return }
    if (!d.regex.trim()) { toast('请填写匹配正则', 'error'); return }
    testBtn.disabled = true
    try {
      const res = await api.testLogEvent(d.path.trim(), d.regex.trim())
      testOut.classList.remove('hidden')
      if (res.error) {
        testOut.textContent = '测试失败：' + res.error
        testOut.style.color = 'rgb(var(--c-danger))'
      } else {
        testOut.textContent = res.matches && res.matches.length > 0
          ? '命中 ' + res.matches.length + ' 行：\n' + res.matches.join('\n')
          : '（当前文件无命中，等待新日志追加）'
        testOut.style.color = 'rgb(var(--c-ink-muted))'
      }
    } catch (err) {
      testOut.classList.remove('hidden')
      testOut.textContent = '测试请求失败：' + (err instanceof Error ? err.message : String(err))
      testOut.style.color = 'rgb(var(--c-danger))'
    } finally {
      testBtn.disabled = false
    }
  }
  testRow.appendChild(testBtn)
  body.appendChild(testRow)
  body.appendChild(testOut)

  const foot = el('div', { class: 'flex justify-end gap-2 pt-3 border-t mt-2' })
  foot.style.borderColor = 'rgb(var(--c-line-subtle))'
  const cancel = el('button', { class: 'btn' }, ['取消'])
  const save = el('button', { class: 'btn btn-primary' }, ['保存'])
  const saveFn = async (): Promise<void> => {
    if (!d.name.trim()) { toast('事件名称必填', 'error'); return }
    if (!d.path.trim()) { toast('日志路径必填', 'error'); return }
    if (!d.regex.trim()) { toast('匹配正则必填', 'error'); return }
    try {
      // 校验正则
      new RegExp(d.regex.trim())
    } catch { toast('正则表达式不合法', 'error'); return }
    const payload = {
      name: d.name.trim(),
      path: d.path.trim(),
      regex: d.regex.trim(),
      cooldown: d.cooldown,
      cooldownUnit: d.cooldownUnit,
      consecutive: d.cooldown === 0 ? 1 : d.consecutive,
      rotate: d.rotate,
    }
    save.disabled = true
    try {
      if (editing && r) await api.updateLogEvent(r.id, payload)
      else await api.createLogEvent(payload)
      toast('日志事件已保存', 'success')
      resDirty = false
      resOverlay = null
      resSaveFn = null
      overlay.remove()
      await reloadAndRender()
    } catch (err) {
      toast('保存失败：' + (err instanceof Error ? err.message : String(err)), 'error')
    } finally {
      save.disabled = false
    }
  }
  resSaveFn = saveFn
  const close = async (): Promise<void> => {
    if (!resDirty) { resOverlay = null; resSaveFn = null; overlay.remove(); return }
    const ch = await unsavedChoice()
    if (ch === 'stay') return
    if (ch === 'save') { await saveFn(); return }
    resDirty = false
    resOverlay = null
    resSaveFn = null
    overlay.remove()
  }
  cancel.onclick = () => void close()
  save.onclick = () => void saveFn()
  foot.append(cancel, save)
  modal.append(titleEl, body, foot)
  overlay.appendChild(modal)
  overlay.onclick = (e) => { if (e.target === overlay) void close() }
  document.body.appendChild(overlay)
}

async function testLogEvent(r: LogEventResource): Promise<void> {
  try {
    const res = await api.testLogEvent(r.path, r.regex)
    if (res.error) toast('测试失败：' + res.error, 'error')
    else toast(res.matches && res.matches.length > 0 ? `命中 ${res.matches.length} 行` : '当前无命中（等待新日志）', res.matches && res.matches.length > 0 ? 'success' : 'info')
  } catch (err) {
    toast('测试失败：' + (err instanceof Error ? err.message : String(err)), 'error')
  }
}

// 资源变更后刷新（保留当前标签）
async function reloadAndRender(): Promise<void> {
  const ctx = getScheduleContext()
  await ctx.loadAll(true)
  rerenderSchedulePage()
}
