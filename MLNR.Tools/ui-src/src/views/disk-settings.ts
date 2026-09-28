// 硬盘组控制通道设置页
// #11：删除"绑定硬件开关通道"（switchN 由 id 1:1 对应 SW1~SW4）
// #12：硬盘路径下拉选择 + 序列号绑定
// #13：挂载方法改为分区下拉 + 挂载点输入 + 多组
// #17：开关硬件参数中添加"系统关键磁盘"属性开关

import { store } from '../store'
import { el, svgIcon, toast, confirmDialog } from '../ui'
import { canControl, pageContainer, pageNav, badge, helpTip } from './shared'
import { api } from '../api'
import type { DiskEntry, SystemDisk, DiskPartition, DiskMount, DiskGroupTask, Schedule, ScheduleExecutor } from '../types'
import { SWITCH_CONFIG_KEYS } from '../types'

// ===== 页面草稿（跨渲染保留，路由离开时重置） =====
interface DiskDraft {
  groupId: number
  alias: string
  switchN: number
  enabled: boolean // deprecated: 迁移至 hwEnabled + NVS；保留供旧配置文件回显
  disks: DiskEntry[]
  dirty: boolean
  // 需求：硬件参数草稿（修改不即时下发，「保存硬件参数」按钮统一下发）
  hwEnabled: boolean
  hwPowerOn: boolean
  hwDelayValues: Record<string, number>
  hwSystem: boolean
  hwSynced: boolean
  hwDirty: boolean
  // 本地配置：上电后等待时间 / 下线等待时间
  powerOnDelaySec: number
  forceOffDelaySec: number
  // 硬盘组任务（上电后 / 下电前）
  onTask?: DiskGroupTask
  offTask?: DiskGroupTask
}
let draft: DiskDraft | null = null

// 系统硬盘列表缓存（用于下拉选择）
let systemDisksCache: SystemDisk[] | null = null
let systemDisksLoading = false

// 各硬盘的分区缓存（device path → partitions）
const partitionCache: Record<string, DiskPartition[]> = {}

export function resetDiskSettingsState(): void {
  draft = null
  systemDisksCache = null
  systemDisksLoading = false
  // Fix #6：清空分区缓存，避免过期数据残留
  for (const k in partitionCache) delete partitionCache[k]
}

export function renderDiskSettings(groupId: number): HTMLElement {
  const cfg = store.settings.diskGroups.find(g => g.id === groupId)
  if (!cfg) {
    return pageContainer([
      pageNav('硬盘组设置', '#/settings'),
      el('div', { class: 'card p-6 text-center text-sm' }, ['无效的硬盘组']),
    ])
  }

  // 初始化/校验草稿
  if (!draft || draft.groupId !== groupId) {
    draft = {
      groupId,
      alias: cfg.alias,
      switchN: cfg.switchN,
      enabled: cfg.enabled,
      disks: cfg.disks.map(d => ({ ...d, mounts: d.mounts ? d.mounts.map(m => ({ ...m })) : [] })),
      dirty: false,
      hwEnabled: cfg.enabled, // Item 4.1：默认从本地配置回显；进入硬件草稿后会被 sw.enabled 覆盖
      hwPowerOn: false,
      hwDelayValues: { POWER_ON_DELAY: 0 },
      hwSystem: false,
      hwSynced: false,
      hwDirty: false,
      // 等待时间必填 1~300 秒（不允许 0）；旧配置缺失/为 0 时按原默认行为兜底显示
      powerOnDelaySec: cfg.powerOnDelaySec && cfg.powerOnDelaySec >= 1 ? cfg.powerOnDelaySec : 8,
      forceOffDelaySec: cfg.forceOffDelaySec && cfg.forceOffDelaySec >= 1 ? cfg.forceOffDelaySec : 2,
      // 硬盘组任务（深拷贝，保存时随本地配置写入）
      onTask: cfg.onTask ? { ...cfg.onTask } : undefined,
      offTask: cfg.offTask ? { ...cfg.offTask } : undefined,
    }
  }

  const view = store.diskGroups.find(g => g.id === groupId)
  const wrap = pageContainer([])
  wrap.appendChild(pageNav(`${cfg.alias || `硬盘组 ${groupId}`} · 通道设置`, '#/settings'))

    // ===== 硬件参数卡片（需求：修改进入草稿，「保存硬件参数」按钮统一下发 CFG） =====
  const hwCard = el('div', { class: 'card p-4 mb-4' })
  const hwTitle = el('div', { class: 'flex items-center gap-2 mb-1' })
  hwTitle.append(svgIcon('bolt', 15))
  const ht = el('span', { class: 'text-sm font-semibold' }, [`开关硬件参数（SW${draft.switchN}）`])
  ht.style.color = 'rgb(var(--c-ink))'
  hwTitle.appendChild(ht)
  hwTitle.appendChild(badge('NVS', '--c-primary', true))
  hwTitle.appendChild(helpTip('以下参数经 CFG 指令下发到硬件，点击【保存硬件参数】后统一写入。直接写入 NVS 开启时自动落盘；关闭时断电丢失，需经顶栏提示或系统设置页落盘 NVS。'))
  hwCard.appendChild(hwTitle)
  const hwHint = el('div', { class: 'text-xs mb-2' },
    ['保存后统一写入硬件（NVS 落盘见问号）'])
  hwHint.style.color = 'rgb(var(--c-ink-subtle))'
  hwCard.appendChild(hwHint)

  const sw = store.switches.find(s => s.index === draft!.switchN)

  // 首次进入时以硬件当前状态初始化硬件草稿（此后用户编辑覆盖）
  if (!draft.hwSynced) {
    draft.hwEnabled = sw?.enabled !== false // 默认 true；硬件未初始化时回退 true
    draft.hwPowerOn = sw?.powerOnState === 1
    draft.hwDelayValues = { POWER_ON_DELAY: sw?.powerOnDelay ?? 0 }
    draft.hwSystem = sw?.system === true
    draft.hwSynced = true
  }

  // ---- Item 4.1：ENABLED (CFG,SWx,ENABLED) — 从本地迁移至 NVS，立即下发 + 失败回滚 ----
  const enRow = el('div', { class: 'form-row' })
  enRow.appendChild(formLabel('启用硬盘组', `CFG,SW${draft.switchN},ENABLED · 硬件级开关；关闭时 SW 通道立即断电`))
  const enWrap = el('label', { class: 'toggle' })
  const enInput = el('input', { type: 'checkbox' }) as HTMLInputElement
  enInput.checked = draft.hwEnabled
  enInput.disabled = !canControl() || !sw
  enInput.title = !canControl() || !sw ? '蓝牙已连接且进入 WORK_RUN 加密会话后方可下发' : '立即下发到硬件'
  const enPrevValue = draft.hwEnabled // 保存修改前的值，用于失败回滚
  enInput.onchange = async () => {
    const newValue = enInput.checked
    const ok = await store.setSwitchConfig(draft!.switchN, 'ENABLED', newValue ? 1 : 0)
    if (ok) {
      draft!.hwEnabled = newValue
      draft!.hwDirty = true
      // 同步本地 settings.diskGroups.enabled 供 UI 回显
      const diskGroups = store.settings.diskGroups.map(g => g.id === draft!.groupId ? { ...g, enabled: newValue } : g)
      store.settings = { ...store.settings, diskGroups }
      // #Fix（重启后启用/停用状态显示错误）：enabled 是本地配置的一部分，
      // 必须持久化到 MAC 级 settings，否则重启加载旧文件后「系统设置-通道配置」显示与本次操作相反。
      const saved = await store.saveSettings({ ...store.settings, diskGroups })
      if (!saved) toast('已下发硬件，但本地配置保存失败', 'error')
      toast(newValue ? '硬盘组已启用' : '硬盘组已禁用', 'success')
      document.dispatchEvent(new Event('rerender'))
    } else {
      toast('下发失败，已重置开关', 'error')
      enInput.checked = enPrevValue
    }
  }
  const enStateTxt = el('span', { class: 'text-xs font-semibold' }, [draft.hwEnabled ? '已启用' : '已禁用'])
  enStateTxt.style.color = draft.hwEnabled ? 'rgb(var(--c-success))' : 'rgb(var(--c-ink-subtle))'
  enWrap.append(enInput, el('span', { class: 'toggle-slider' }))
  enRow.appendChild(controlWrapRight(enWrap, enStateTxt))
  hwCard.appendChild(enRow)

  // ---- 上电自动上线（POWER_ON_STATE，草稿，靠右对齐） ----
  const stateRow = el('div', { class: 'form-row' })
  stateRow.appendChild(formLabel('上电自动上线', `CFG,SW${draft.switchN},POWER_ON_STATE · 启用=上电后通道默认闭合(1)，禁用=默认断开(0)`))
  const stateWrap = el('label', { class: 'toggle' })
  const stateInput = el('input', { type: 'checkbox' }) as HTMLInputElement
  stateInput.checked = draft.hwPowerOn
  stateInput.onchange = () => {
    draft!.hwPowerOn = stateInput.checked
    draft!.hwDirty = true
  }
  const stateTxt = el('span', { class: 'text-xs font-semibold' }, [draft.hwPowerOn ? '已启用' : '已禁用'])
  stateTxt.style.color = draft.hwPowerOn ? 'rgb(var(--c-success))' : 'rgb(var(--c-ink-subtle))'
  stateWrap.append(stateInput, el('span', { class: 'toggle-slider' }))
  stateRow.appendChild(controlWrapRight(stateWrap, stateTxt))
  hwCard.appendChild(stateRow)

  // ---- 数值型参数（POWER_ON_DELAY 等，草稿，靠右对齐） ----
  for (const item of SWITCH_CONFIG_KEYS) {
    if (item.key === 'POWER_ON_STATE') continue
    const row = el('div', { class: 'form-row' })
    row.appendChild(formLabel(item.label, `CFG,SW${draft.switchN},${item.key} · 取值 ${item.min}-${item.max} ${item.unit}`))
    const num = el('input', {
      type: 'number', class: 'input', style: 'max-width:180px',
      min: String(item.min), max: String(item.max),
      value: String(draft.hwDelayValues[item.key] ?? 0),
    }) as HTMLInputElement
    num.onchange = () => {
      const v = Number(num.value)
      if (!Number.isFinite(v) || v < item.min || v > item.max) {
        toast(`${item.label} 取值范围 ${item.min}-${item.max} ${item.unit}`, 'error')
        num.value = String(draft!.hwDelayValues[item.key] ?? 0)
        return
      }
      draft!.hwDelayValues[item.key] = Math.round(v)
      draft!.hwDirty = true
    }
    const unit = el('span', { class: 'text-xs' }, [item.unit])
    unit.style.color = 'rgb(var(--c-ink-subtle))'
    row.appendChild(controlWrapRight(num, unit))
    hwCard.appendChild(row)
  }

  // ---- 系统关键磁盘（SYSTEM，草稿；保存时若打开将二次确认，靠右对齐） ----
  const systemRow = el('div', { class: 'form-row' })
  systemRow.appendChild(formLabel('系统关键磁盘', 'CFG,SW,SYSTEM · 标记为系统关键磁盘后，硬盘组管理页的"关闭"和"卸载"按钮将被禁用'))
  const sysWrap = el('label', { class: 'toggle' })
  const sysInput = el('input', { type: 'checkbox' }) as HTMLInputElement
  sysInput.checked = draft.hwSystem
  sysInput.onchange = () => {
    draft!.hwSystem = sysInput.checked
    draft!.hwDirty = true
  }
  const sysStateTxt = el('span', { class: 'text-xs font-semibold' }, [draft.hwSystem ? '已启用' : '未启用'])
  sysStateTxt.style.color = draft.hwSystem ? 'rgb(var(--c-danger))' : 'rgb(var(--c-ink-subtle))'
  sysWrap.append(sysInput, el('span', { class: 'toggle-slider' }))
  systemRow.appendChild(controlWrapRight(sysWrap, sysStateTxt))
  hwCard.appendChild(systemRow)

  // ---- 保存硬件参数按钮（统一下发） ----
  const hwFoot = el('div', { class: 'flex justify-end mt-2' })
  const hwSaveBtn = el('button', { class: 'btn btn-primary' }, [svgIcon('save', 14), ' 保存硬件参数'])
  hwSaveBtn.disabled = !draft.hwDirty || !canControl() || !sw
  hwSaveBtn.title = !canControl() || !sw ? '蓝牙已连接且进入 WORK_RUN 加密会话后方可下发' : '下发 CFG 指令到硬件内存'
  hwSaveBtn.onclick = async () => {
    // 打开「系统关键磁盘」为高风险操作：保存时二次确认
    if (draft!.hwSystem && !(sw?.system === true)) {
      const confirmed = await confirmDialog(
        '确认打开「系统关键磁盘」开关？启用后该硬盘组的关闭和卸载操作将被禁止。',
        '打开系统关键磁盘确认',
        true,
      )
      if (!confirmed) return
    }
    hwSaveBtn.disabled = true
    const errors: string[] = []
    // ENABLED 已在开关 onchange 时立即下发，此处跳过
    const ok1 = await store.setSwitchConfig(draft!.switchN, 'POWER_ON_STATE', draft!.hwPowerOn ? 1 : 0)
    if (!ok1) errors.push('上电自动上线')
    for (const item of SWITCH_CONFIG_KEYS) {
      if (item.key === 'POWER_ON_STATE') continue
      const ok = await store.setSwitchConfig(draft!.switchN, item.key, draft!.hwDelayValues[item.key])
      if (!ok) errors.push(item.label)
    }
    const okS = await store.setSwitchConfig(draft!.switchN, 'SYSTEM', draft!.hwSystem ? 1 : 0)
    if (!okS) errors.push('系统关键磁盘')
    hwSaveBtn.disabled = false
    if (errors.length === 0) {
      draft!.hwDirty = false
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
  aliasRow.appendChild(formLabel('硬盘组别名', '仅保存到 fnOS 本地配置文件'))
  const aliasInput = el('input', {
    class: 'input', style: 'max-width:240px', placeholder: `硬盘组 ${groupId}`,
    value: draft.alias,
  }) as HTMLInputElement
  aliasInput.oninput = () => { draft!.alias = aliasInput.value; draft!.dirty = true }
  aliasRow.appendChild(controlWrapRight(aliasInput))
  localCard.appendChild(aliasRow)

  // ---- 任务配置入口（上电后 / 下电前任务） ----
  const taskRow = el('div', { class: 'form-row' })
  taskRow.appendChild(formLabel('任务配置', '配置硬盘组上电完成后异步触发的上电后任务，以及下电前必须执行成功才能继续的下电前任务（失败将终止正常下电）。'))
  const taskBtn = el('button', { class: 'btn' }, [
    svgIcon('settings', 15),
    draft.onTask || draft.offTask ? ' 任务配置（已设置）' : ' 任务配置',
  ])
  taskBtn.onclick = () => { void openTaskConfigModal() }
  taskRow.appendChild(controlWrapRight(taskBtn))
  localCard.appendChild(taskRow)

  // ---- 上电后等待时间（秒，必填 1~300） ----
  const powerDelayRow = el('div', { class: 'form-row' })
  powerDelayRow.appendChild(formLabel('上电后等待时间',
    '发送 SW ON 上线命令后，等待该秒数再检查硬盘是否就绪。机械盘上电到就绪可能较慢，取值 1-300 秒。'))
  const powerDelayInput = el('input', {
    type: 'number', class: 'input', style: 'max-width:180px', min: '1', max: '300',
    value: String(draft.powerOnDelaySec),
  }) as HTMLInputElement
  powerDelayInput.onchange = () => {
    const v = Number(powerDelayInput.value)
    if (!Number.isFinite(v) || v < 1 || v > 300) {
      toast('上电后等待时间取值 1-300 秒', 'error')
      powerDelayInput.value = String(draft!.powerOnDelaySec)
      return
    }
    draft!.powerOnDelaySec = Math.round(v)
    draft!.dirty = true
  }
  const pDelayUnit = el('span', { class: 'text-xs' }, ['秒'])
  pDelayUnit.style.color = 'rgb(var(--c-ink-subtle))'
  powerDelayRow.appendChild(controlWrapRight(powerDelayInput, pDelayUnit))
  localCard.appendChild(powerDelayRow)

  // ---- 下线等待时间（秒，必填 1~300） ----
  const forceDelayRow = el('div', { class: 'form-row' })
  forceDelayRow.appendChild(formLabel('下线等待时间',
    '卸载（umount）后等待该秒数再继续断电流程：安全下线在 umount 成功后等待；强制下线在 umount -f 发送后等待（不要求成功）。取值 1-300 秒。'))
  const forceDelayInput = el('input', {
    type: 'number', class: 'input', style: 'max-width:180px', min: '1', max: '300',
    value: String(draft.forceOffDelaySec),
  }) as HTMLInputElement
  forceDelayInput.onchange = () => {
    const v = Number(forceDelayInput.value)
    if (!Number.isFinite(v) || v < 1 || v > 300) {
      toast('下线等待时间取值 1-300 秒', 'error')
      forceDelayInput.value = String(draft!.forceOffDelaySec)
      return
    }
    draft!.forceOffDelaySec = Math.round(v)
    draft!.dirty = true
  }
  const fDelayUnit = el('span', { class: 'text-xs' }, ['秒'])
  fDelayUnit.style.color = 'rgb(var(--c-ink-subtle))'
  forceDelayRow.appendChild(controlWrapRight(forceDelayInput, fDelayUnit))
  localCard.appendChild(forceDelayRow)

  // #11：已删除"绑定硬件开关通道"配置项（switchN 由 id 1:1 对应 SW1~SW4）
  // 该说明无实际意义，按需求移除。

  // ---- 硬盘列表管理（#12 + #13） ----
  localCard.appendChild(renderDiskList())

  // ---- 保存按钮 ----
  const foot = el('div', { class: 'flex justify-end mt-3' })
  const saveBtn = el('button', { class: 'btn btn-primary' }, [svgIcon('save', 15), ' 保存本地配置'])
  saveBtn.disabled = !draft.dirty
  saveBtn.onclick = async () => {
    // #13：保存前校验挂载点
    for (const d of draft!.disks) {
      const seen = new Set<string>()
      for (const m of d.mounts) {
        if (!m.mountPoint.trim()) {
          toast('挂载点不能为空', 'error')
          return
        }
        if (seen.has(m.mountPoint.trim())) {
          toast(`挂载点重复：${m.mountPoint}（同一硬盘组内不能重复）`, 'error')
          return
        }
        seen.add(m.mountPoint.trim())
      }
    }
    const diskGroups = store.settings.diskGroups.map(g => g.id === groupId ? {
      ...g,
      alias: draft!.alias.trim() || `硬盘组 ${groupId}`,
      switchN: draft!.switchN,
      // enabled 已迁移至 NVS，在保存硬件参数时同步更新 store.settings.diskGroups.enabled
      disks: draft!.disks.map(d => ({ ...d, mounts: d.mounts.map(m => ({ ...m })) })),
      powerOnDelaySec: draft!.powerOnDelaySec,
      forceOffDelaySec: draft!.forceOffDelaySec,
      onTask: draft!.onTask ? { ...draft!.onTask } : undefined,
      offTask: draft!.offTask ? { ...draft!.offTask } : undefined,
    } : g)
    // 上电后/下电前任务校验：任务不存在或直接执行器含同组下电 → 不允许保存（服务端同样校验兜底）
    const issue = findTaskRefIssue(groupId, draft!.onTask, '上电后')
      ?? findTaskRefIssue(groupId, draft!.offTask, '下电前')
    if (issue) { toast(issue, 'error'); return }
    const ok = await store.saveSettings({ ...store.settings, diskGroups })
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

  // 运行状态提示
  if (view) {
    const status = el('div', { class: 'text-xs mt-2 px-1' }, [
      `当前状态：${view.conflict ? '通道冲突（已禁用）' : view.online ? '在线' : '离线'} · ${(view.disks ?? []).filter(d => d.mounted).length}/${(view.disks ?? []).length} 块已挂载`,
    ])
    status.style.color = 'rgb(var(--c-ink-subtle))'
    wrap.appendChild(status)
  }

  return wrap
}

// ================================================================
// #12 + #13：硬盘列表编辑（下拉选择硬盘 + 序列号 + 多组挂载）
// ================================================================
function renderDiskList(): HTMLElement {
  const box = el('div', { class: 'mt-1' })
  const title = el('div', { class: 'form-label' }, ['硬盘列表'])
  title.appendChild(helpTip('从系统硬盘列表中选择块设备，以序列号作为绑定标识。每个硬盘可配置多组「分区→挂载点」。安全下线流程会先卸载全部挂载点、向 HDD 发送停转指令（NVMe 跳过），再断开开关供电。'))
  box.appendChild(title)

  if (draft!.disks.length === 0) {
    const empty = el('div', { class: 'text-xs py-2' }, ['尚未添加硬盘。点击下方按钮从系统硬盘列表中选择。'])
    empty.style.color = 'rgb(var(--c-ink-subtle))'
    box.appendChild(empty)
  }

  draft!.disks.forEach((d, i) => {
    const card = el('div', { class: 'card p-3 mb-2' })
    card.style.background = 'rgb(var(--c-element))'

    // ---- 第一行：硬盘下拉选择 + 别名 + 删除 ----
    const row1 = el('div', { class: 'flex flex-wrap gap-2 mb-2 items-center' })

    // #12：块设备路径改为下拉选择
    const devSelect = el('select', {
      class: 'input', style: 'flex:1;min-width:180px', title: '选择系统硬盘',
    }) as HTMLSelectElement
    devSelect.appendChild(el('option', { value: '' }, ['（选择硬盘…）']))
    // 已配置的硬盘（即使不在系统列表中也显示）
    if (d.device && !systemDisksCache?.find(sd => sd.path === d.device)) {
      const opt = el('option', { value: d.device }, [`${d.device}（已配置）`])
      ;(opt as HTMLOptionElement).selected = true
      devSelect.appendChild(opt)
    }
    // 系统硬盘列表（排除已被其它通道绑定的）
    const boundSerials = new Set<string>()
    for (const g of store.settings.diskGroups) {
      if (g.id === draft!.groupId) continue
      for (const gd of g.disks) {
        if (gd.serial) boundSerials.add(gd.serial)
      }
    }
    for (const sd of systemDisksCache ?? []) {
      if (sd.serial && boundSerials.has(sd.serial)) continue // 排除已绑定
      const opt = el('option', { value: sd.path },
        [`${sd.path} ${sd.model ? '· ' + sd.model : ''}${sd.bound ? '（已绑定）' : ''}`])
      if (sd.path === d.device) (opt as HTMLOptionElement).selected = true
      devSelect.appendChild(opt)
    }
    devSelect.value = d.device || ''
    devSelect.onchange = async () => {
      const path = devSelect.value
      if (!path) {
        draft!.disks[i].device = ''
        draft!.disks[i].serial = ''
        draft!.dirty = true
        document.dispatchEvent(new Event('rerender'))
        return
      }
      const sd = systemDisksCache?.find(x => x.path === path)
      // #12：序列号绑定 — 如果读取不到序列号，报错并禁止保存
      if (!sd || !sd.serial) {
        toast(`无法读取硬盘 ${path} 的序列号，无法绑定。请检查硬盘是否正常连接。`, 'error')
        devSelect.value = d.device || ''
        return
      }
      draft!.disks[i].device = path
      draft!.disks[i].serial = sd.serial
      draft!.dirty = true
      // 清空旧的挂载配置（换了硬盘）
      draft!.disks[i].mounts = []
      // 加载新硬盘的分区
      await loadPartitions(path)
      document.dispatchEvent(new Event('rerender'))
    }

    const aliasInput = el('input', {
      class: 'input', style: 'flex:1;min-width:120px', placeholder: '硬盘别名（可选）', value: d.alias,
    }) as HTMLInputElement
    aliasInput.oninput = () => { draft!.disks[i].alias = aliasInput.value; draft!.dirty = true }

    const delBtn = el('button', { class: 'btn btn-sm btn-danger shrink-0', title: '移除该硬盘' }, [svgIcon('trash', 13)])
    delBtn.onclick = () => {
      draft!.disks.splice(i, 1)
      draft!.dirty = true
      document.dispatchEvent(new Event('rerender'))
    }
    row1.append(devSelect, aliasInput, delBtn)

    // #12：显示序列号
    if (d.serial) {
      const serialRow = el('div', { class: 'text-[11px] mb-2 px-1' })
      serialRow.style.color = 'rgb(var(--c-ink-subtle))'
      serialRow.textContent = `序列号：${d.serial}`
      card.append(row1, serialRow)
    } else {
      card.appendChild(row1)
    }

    // #13：多组挂载（分区下拉 + 挂载点输入）
    // 视觉分隔：与上方硬盘选择区用细分割线 + 留白隔开，避免混淆
    const mountsBox = el('div', { class: 'flex flex-col gap-2 mb-2 mt-1 pt-3' })
    mountsBox.style.borderTop = '1px solid rgb(var(--c-line-subtle))'
    mountsBox.style.background = 'rgb(var(--c-surface) / 0.35)'
    mountsBox.style.paddingLeft = '10px'
    mountsBox.style.paddingRight = '10px'
    mountsBox.style.borderRadius = '8px'
    const mountsLabel = el('div', { class: 'text-xs font-semibold' }, ['挂载配置（分区 → 挂载点）'])
    mountsLabel.style.color = 'rgb(var(--c-ink-muted))'
    mountsBox.appendChild(mountsLabel)

    d.mounts.forEach((m, mi) => {
      mountsBox.appendChild(renderMountRow(d, i, m, mi))
    })

    // 添加挂载组按钮（#Fix：与下方卡片边框保持间距，避免视觉融为一体）
    const addMountBtn = el('button', { class: 'btn btn-sm' }, [svgIcon('plus', 12), '添加挂载'])
    addMountBtn.style.marginBottom = '8px'
    addMountBtn.disabled = !d.device
    addMountBtn.title = d.device ? '添加一组分区+挂载点' : '请先选择硬盘'
    addMountBtn.onclick = async () => {
      if (!d.device) return
      await loadPartitions(d.device)
      draft!.disks[i].mounts.push({ partition: '', mountPoint: '' })
      draft!.dirty = true
      document.dispatchEvent(new Event('rerender'))
    }
    mountsBox.appendChild(addMountBtn)
    card.appendChild(mountsBox)

    // 自动挂载 + NVMe 开关
    const row2 = el('div', { class: 'flex flex-wrap items-center gap-4' })
    const amWrap = el('label', { class: 'toggle', title: '硬盘组上线后自动挂载到上述挂载点' })
    const amInput = el('input', { type: 'checkbox' }) as HTMLInputElement
    amInput.checked = d.autoMount
    amInput.onchange = () => { draft!.disks[i].autoMount = amInput.checked; draft!.dirty = true }
    amWrap.append(amInput, el('span', { class: 'toggle-slider' }))
    const amLabel = el('span', { class: 'text-xs' }, ['自动挂载'])
    amLabel.style.color = 'rgb(var(--c-ink-muted))'

    const nvWrap = el('label', { class: 'toggle', title: 'NVMe SSD 无停转指令，安全下线时跳过' })
    const nvInput = el('input', { type: 'checkbox' }) as HTMLInputElement
    nvInput.checked = d.isNvme
    nvInput.onchange = () => { draft!.disks[i].isNvme = nvInput.checked; draft!.dirty = true }
    nvWrap.append(nvInput, el('span', { class: 'toggle-slider' }))
    const nvLabel = el('span', { class: 'text-xs' }, ['NVMe（跳过停转）'])
    nvLabel.style.color = 'rgb(var(--c-ink-muted))'

    row2.append(amWrap, amLabel, nvWrap, nvLabel)
    card.appendChild(row2)

    box.appendChild(card)
  })

  // 添加硬盘按钮
  const addBtnRow = el('div', { class: 'flex gap-2 mt-1' })
  const addBtn = el('button', { class: 'btn btn-sm' }, [svgIcon('plus', 13), '添加硬盘'])
  addBtn.onclick = async () => {
    await loadSystemDisks()
    draft!.disks.push({ device: '', alias: '', serial: '', mounts: [], autoMount: true, isNvme: false })
    draft!.dirty = true
    document.dispatchEvent(new Event('rerender'))
  }
  const refreshBtn = el('button', { class: 'btn btn-sm' }, [svgIcon('refresh', 13), ' 刷新硬盘列表'])
  refreshBtn.onclick = async () => {
    refreshBtn.disabled = true
    systemDisksCache = null
    await loadSystemDisks(true) // refresh=true：后端强制重新执行 lsblk 并更新缓存
    refreshBtn.disabled = false
    document.dispatchEvent(new Event('rerender'))
  }
  addBtnRow.append(addBtn, refreshBtn)
  box.appendChild(addBtnRow)

  // 首次加载系统硬盘列表
  if (systemDisksCache === null && !systemDisksLoading) {
    void loadSystemDisks()
  }

  return box
}

/** #13：渲染单组挂载行（分区下拉 + 挂载点输入 + 删除） */
function renderMountRow(disk: DiskEntry, diskIdx: number, m: DiskMount, mountIdx: number): HTMLElement {
  const row = el('div', { class: 'flex flex-wrap gap-2 items-center' })
  // 前向声明 mountInput，供下方 partSelect.onchange 闭包直接引用（避免 fragile DOM query）
  let mountInput!: HTMLInputElement

  // 分区下拉
  const partSelect = el('select', {
    class: 'input', style: 'flex:1;min-width:160px', title: '选择磁盘分区',
  }) as HTMLSelectElement
  partSelect.appendChild(el('option', { value: '' }, ['（选择分区…）']))
  const partitions = partitionCache[disk.device] ?? []
  // 已配置的分区（即使不在列表中也显示）
  if (m.partition && !partitions.find(p => p.path === m.partition)) {
    const opt = el('option', { value: m.partition }, [`${m.partition}（已配置）`])
    ;(opt as HTMLOptionElement).selected = true
    partSelect.appendChild(opt)
  }
  for (const p of partitions) {
    const sizeStr = formatSize(p.size)
    const mountedStr = p.mountpoints && p.mountpoints.length > 0 ? ` · 已挂载到 ${p.mountpoints.join(',')}` : ''
    const opt = el('option', { value: p.path }, [`${p.path}（${sizeStr}${mountedStr}）`])
    if (p.path === m.partition) (opt as HTMLOptionElement).selected = true
    partSelect.appendChild(opt)
  }
  partSelect.value = m.partition
  // Item 4：选分区后自动填充已挂载的路径（若当前挂载点为空）
  partSelect.onchange = () => {
    const partPath = partSelect.value
    draft!.disks[diskIdx].mounts[mountIdx].partition = partPath
    // 查分区缓存中的挂载点，自动填充（仅当当前 mountPoint 为空时）
    if (partPath) {
      const part = (partitionCache[disk.device] ?? []).find(p => p.path === partPath)
      if (part && part.mountpoints && part.mountpoints.length > 0) {
        const first = part.mountpoints[0]
        const cur = draft!.disks[diskIdx].mounts[mountIdx].mountPoint
        if (!cur || cur === '') {
          draft!.disks[diskIdx].mounts[mountIdx].mountPoint = first
          // Item 4：同步更新 DOM input 显示，无需等待 rerender
          mountInput.value = first
        }
      }
    }
    draft!.dirty = true
  }

  // 挂载点输入（赋值给前向声明的 mountInput）
  mountInput = el('input', {
    class: 'input', style: 'flex:1;min-width:180px', placeholder: '挂载点，如 /vol1/1000/archive',
    value: m.mountPoint,
  }) as HTMLInputElement
  mountInput.oninput = () => {
    draft!.disks[diskIdx].mounts[mountIdx].mountPoint = mountInput.value.trim()
    draft!.dirty = true
  }

  // 删除挂载组
  const delBtn = el('button', { class: 'btn btn-sm btn-danger shrink-0', title: '删除此挂载组' }, [svgIcon('x', 12)])
  delBtn.onclick = () => {
    draft!.disks[diskIdx].mounts.splice(mountIdx, 1)
    draft!.dirty = true
    document.dispatchEvent(new Event('rerender'))
  }

  row.append(partSelect, mountInput, delBtn)
  return row
}

/** 加载系统硬盘列表（带缓存；refresh=true 时强制后端重新收集并更新缓存） */
async function loadSystemDisks(refresh = false): Promise<void> {
  if (systemDisksLoading) return
  systemDisksLoading = true
  try {
    systemDisksCache = await store.loadDisksList(refresh)
  } catch {
    systemDisksCache = []
  } finally {
    systemDisksLoading = false
  }
  document.dispatchEvent(new Event('rerender'))
}

/** 加载指定硬盘的分区列表（带缓存） */
async function loadPartitions(device: string): Promise<void> {
  if (!device || partitionCache[device]) return
  try {
    partitionCache[device] = await store.loadDiskPartitions(device)
  } catch {
    partitionCache[device] = []
  }
}

/** 格式化字节大小 */
function formatSize(bytes: number): string {
  if (!bytes || bytes <= 0) return '未知'
  if (bytes >= 1099511627776) return `${(bytes / 1099511627776).toFixed(1)} TB`
  if (bytes >= 1073741824) return `${(bytes / 1073741824).toFixed(1)} GB`
  if (bytes >= 1048576) return `${(bytes / 1048576).toFixed(1)} MB`
  return `${bytes} B`
}

// ================================================================
// 小工具
// ================================================================
function formLabel(text: string, hint?: string): HTMLElement {
  const label = el('div', { class: 'form-label' }, [text])
  if (hint) label.appendChild(helpTip(hint))
  return label
}

// Item 4.3：右对齐 control wrap — 接受多个子元素，整体靠右显示
function controlWrapRight(...children: HTMLElement[]): HTMLElement {
  const w = el('div', { class: 'flex-1 flex items-center gap-3 justify-end' })
  for (const c of children) w.appendChild(c)
  return w
}


// ================================================================
// 硬盘组脚本配置（上电后 / 下电前；本地配置-脚本配置入口）
// ================================================================
let groupSchedules: Schedule[] | null = null

async function loadGroupSchedules(force = false): Promise<Schedule[]> {
  if (groupSchedules && !force) return groupSchedules
  try {
    const r = await api.getSchedules()
    groupSchedules = r.schedules ?? []
  } catch {
    groupSchedules = []
  }
  return groupSchedules
}

/** 执行器类型中文摘要（任务配置区展示用） */
function executorLabel(e: ScheduleExecutor): string {
  switch (e.type) {
    case 'fan_control': return '风扇控制'
    case 'disk_group_control': return `硬盘组${e.diskGroupId}·${e.diskAction === 'online' ? '上电' : '下电'}`
    case 'control_task': return '控制任务'
    case 'exec_script': return '执行脚本'
    default: return e.type
  }
}

/** 上电后/下电前任务引用校验：任务不存在，或直接执行器含对同一硬盘组的下电操作 → 返回错误消息（null=通过）。 */
function findTaskRefIssue(groupId: number, t: DiskGroupTask | undefined, kind: string): string | null {
  if (!t || !t.taskId || !groupSchedules) return null
  const sc = groupSchedules.find(x => x.id === t.taskId)
  if (!sc) return `${kind}任务引用的任务 #${t.taskId} 不存在`
  for (const e of sc.executors ?? []) {
    if (e.type === 'disk_group_control' && e.diskGroupId === groupId && e.diskAction === 'offline') {
      return `${kind}任务「${sc.name}」包含对同一硬盘组的下电操作，不允许保存`
    }
  }
  return null
}

/** 单个任务配置区（上电后 / 下电前）；任务下拉仅引用模式（无自定义内联） */
function taskSection(
  key: 'onTask' | 'offTask',
  title: string,
  desc: string,
  tasks: Schedule[],
  rebuild: () => void,
): HTMLElement {
  const sec = el('div', { class: 'rounded-lg border p-3 flex flex-col gap-3' })
  sec.style.borderColor = 'rgb(var(--c-line-subtle))'
  const head = el('div', { class: 'text-sm font-semibold' }, [title])
  head.style.color = 'rgb(var(--c-ink))'
  const hint = el('div', { class: 'text-[11px] mb-1' }, [desc])
  hint.style.color = 'rgb(var(--c-ink-muted))'
  sec.append(head, hint)

  const cur = draft![key]

  // ---- 计划任务下拉（引用计划任务，触发执行其全部执行器） ----
  const modeRow = el('div', { class: 'flex flex-col gap-1' })
  const modeL = el('label', { class: 'text-xs font-medium' }, ['计划任务'])
  modeL.style.color = 'rgb(var(--c-ink-muted))'
  const mSel = el('select', { class: 'input' }) as HTMLSelectElement
  mSel.appendChild(el('option', { value: '' }, ['— 不运行任务 —']))
  for (const r of tasks) mSel.appendChild(el('option', { value: String(r.id) }, [r.name]))
  if (cur?.taskId != null && tasks.some(r => r.id === cur.taskId)) mSel.value = String(cur.taskId)
  else mSel.value = ''
  modeRow.append(modeL, mSel)
  sec.appendChild(modeRow)

  const setTask = (t: DiskGroupTask | undefined) => {
    if (t) draft![key] = t
    else delete draft![key]
    draft!.dirty = true
  }

  if (mSel.value !== '') {
    const r = tasks.find(x => x.id === Number(mSel.value))
    if (r) {
      const tip = el('div', { class: 'text-[11px] whitespace-pre-wrap break-all p-2 rounded' })
      tip.style.background = 'rgb(var(--c-overlay) / 0.3)'
      tip.style.color = 'rgb(var(--c-ink-muted))'
      const exTxt = (r.executors?.length ?? 0) > 0
        ? r.executors!.map(e => executorLabel(e)).join('、')
        : '（无执行器）'
      const triTxt = (r.triggers?.length ?? 0) > 0 ? `触发器 ${r.triggers!.length} 个 · ` : ''
      tip.textContent = `${triTxt}执行器：${exTxt}`
      sec.appendChild(tip)
    }
  }

  mSel.onchange = () => {
    const v = mSel.value
    if (v === '') setTask(undefined)
    else setTask({ taskId: Number(v) })
    rebuild()
  }
  return sec
}

/** 硬盘组任务配置弹窗（上电后 / 下电前；保存/取消） */
async function openTaskConfigModal(): Promise<void> {
  const tasks = await loadGroupSchedules()
  const overlay = el('div', { class: 'fixed inset-0 z-50 flex items-center justify-center p-4' })
  overlay.style.background = 'rgb(var(--c-overlay) / 0.6)'
  const modal = el('div', { class: 'card w-[640px] max-w-full flex flex-col p-5' })
  modal.style.maxHeight = '90vh'
  const titleEl = el('h3', { class: 'font-semibold text-base mb-1' }, ['硬盘组任务配置'])
  titleEl.style.color = 'rgb(var(--c-ink))'
  const sub = el('div', { class: 'text-xs mb-3' }, ['上电后任务在硬盘组上电流程完成后异步触发执行（触发后交由调度引擎管理）；下电前任务在下电流程开始前同步触发执行，必须执行成功才继续正常下电，失败/任务丢失将终止正常下电（强制下电不受限）。任务由「定时计划」页配置。'])
  sub.style.color = 'rgb(var(--c-ink-muted))'

  const body = el('div', { class: 'overflow-y-auto pr-1 -mr-1 flex flex-col gap-4', style: 'min-height:0' })
  body.style.flex = '1'
  const rebuild = () => {
    body.innerHTML = ''
    body.append(
      taskSection('onTask', '上电后任务', '硬盘组上电（SW ON 上线流程完成）后异步触发执行', tasks, rebuild),
      taskSection('offTask', '下电前任务', '硬盘组下电流程开始前同步触发执行，必须成功才继续下电（强制下电跳过）', tasks, rebuild),
    )
  }
  rebuild()

  const foot = el('div', { class: 'flex justify-end gap-2 mt-3' })
  const esc = (ev: KeyboardEvent) => { if (ev.key === 'Escape') close() }
  const close = () => { overlay.remove(); document.removeEventListener('keydown', esc) }
  const cancelBtn = el('button', { class: 'btn' }, ['取消'])
  cancelBtn.onclick = close
  const saveBtn = el('button', { class: 'btn btn-primary' }, ['保存'])
  saveBtn.onclick = close
  document.addEventListener('keydown', esc)
  foot.append(cancelBtn, saveBtn)
  modal.append(titleEl, sub, body, foot)
  overlay.appendChild(modal)
  document.body.appendChild(overlay)
}
