// 控制板全局设置页（#/connection/config）
// 承载控制板全局参数：设备广播名 / Debug 模式 / 全局心跳超时阈值（需求3 迁入）。
// 由「系统设置 · 硬件配置」卡中的「控制板设置」按钮进入（需求3）。
// 需求（控制板设置页重构）：
//  - 页名「控制板全局设置」，返回导航指向「系统设置」（#/settings）
//  - 「启动时自动连接上次设备」已迁回「连接设置」页（#/connection），本页不再保留
//  - 广播名 / Debug 模式 / 全局心跳超时阈值 均改为页内临时草稿，删除各自独立保存按钮；
//    页面右下角「保存全局配置」按钮统一将三者的修改下发到设备 NVS；
//    未点击保存就退出本页，修改作废（resetConnectionConfigState 清空草稿）。

import { store } from '../store'
import { api } from '../api'
import { el, svgIcon, toast, confirmDialog } from '../ui'
import { pageContainer, pageNav, badge, isDeviceConnected, helpTip } from './shared'
import type { DeviceInfo } from '../types'

// ===== 页面级状态（跨渲染保留；离开页面由 resetConnectionConfigState 清空） =====
// 统一草稿：仅本页内有效，点击「保存全局配置」才下发
let draft: {
  bleName: string          // 设备广播名草稿
  debug: boolean           // Debug 模式草稿
  hbTimeout: number        // 全局心跳超时阈值草稿（秒）
  loaded: boolean          // 初始值是否已从设备/设置读取
} | null = null

export function resetConnectionConfigState(): void {
  draft = null
}

export function renderConnectionConfig(): HTMLElement {
  // 首次进入：读取设备广播名 / Debug 模式，心跳阈值取本地设置
  if (!draft) {
    draft = { bleName: '', debug: false, hbTimeout: store.settings.hbTimeoutSec ?? 10, loaded: false }
  }
  if (!draft.loaded) {
    void api.getBleName().then(r => {
      if (!draft) return
      draft!.bleName = r.name ?? ''
      draft!.loaded = true
      document.dispatchEvent(new Event('rerender'))
    }).catch(() => { if (draft) draft!.loaded = true })
    void api.getBleDebugMode().then(r => {
      if (!draft) return
      draft!.debug = !!r.enabled
      draft!.loaded = true
      document.dispatchEvent(new Event('rerender'))
    }).catch(() => { /* 保留默认 */ })
  }

  const d = store.device
  const wrap = pageContainer([])
  wrap.appendChild(pageNav('控制板全局设置', '#/settings'))

  const card = el('div', { class: 'card p-4 mb-4' })
  const title = el('div', { class: 'flex items-center gap-2 mb-3' })
  title.append(svgIcon('settings', 15))
  const t = el('span', { class: 'text-sm font-semibold' }, ['控制板全局设置'])
  t.style.color = 'rgb(var(--c-ink))'
  title.appendChild(t)
  title.appendChild(helpTip('以下参数修改后不会立即生效，点击页面右下角【保存全局配置】后统一写入控制板。直接写入 NVS 开启时自动落盘；关闭时需经顶栏提示或系统设置页落盘 NVS；未保存退出本页，修改作废。'))
  card.appendChild(title)

  const hint = el('div', { class: 'text-xs mb-3' })
  hint.style.color = 'rgb(var(--c-ink-subtle))'
  hint.textContent = '修改后点【保存全局配置】统一下发'
  card.appendChild(hint)

  // ---- 设备广播名（草稿） ----
  card.appendChild(renderBleNameSection(d))

  // ---- Debug 模式（草稿） ----
  card.appendChild(renderDebugSection())

  // ---- 全局心跳超时阈值（草稿） ----
  card.appendChild(renderHeartbeatSection())

  // ---- 保存全局配置按钮（右下角） + 恢复出厂（左下角危险操作） ----
  const foot = el('div', { class: 'flex items-center justify-between pt-1' })
  const resetBtn = el('button', { class: 'btn btn-danger btn-sm' }, [svgIcon('reset', 14), ' 恢复出厂'])
  resetBtn.onclick = async () => {
    if (!isDeviceConnected()) {
      toast('蓝牙未连接，无法执行恢复出厂', 'error')
      return
    }
    const ok = await confirmDialog(
      '恢复出厂将清除硬件 NVS 中所有风扇/开关 CFG 配置，恢复为默认值。此操作不可撤销，确定继续？',
      '恢复出厂确认',
      true,
    )
    if (!ok) return
    resetBtn.disabled = true
    const r = await store.factoryReset()
    resetBtn.disabled = false
    if (r) {
      toast('硬件配置已恢复出厂', 'success')
      await store.loadStatus()
    } else {
      toast('恢复出厂失败，详见日志', 'error')
    }
    document.dispatchEvent(new CustomEvent('rerender'))
  }

  const saveBtn = el('button', { class: 'btn btn-primary' }, [svgIcon('save', 15), ' 保存全局配置'])
  saveBtn.disabled = !draft.loaded
  saveBtn.onclick = async () => {
    if (!draft) return
    // 校验广播名 1~20
    const name = draft.bleName.trim()
    if (name.length < 1 || name.length > 20) {
      toast('广播名长度需为 1~20 个字符', 'error')
      return
    }
    // 校验心跳阈值 3~60
    const hb = Math.round(draft.hbTimeout)
    if (!Number.isFinite(hb) || hb < 3 || hb > 60) {
      toast('心跳超时阈值取值范围 3~60 秒', 'error')
      return
    }
    saveBtn.disabled = true
    const errors: string[] = []
    // 1) 广播名 → 写入设备 NVS
    try {
      await api.setBleName(name)
    } catch (e: any) {
      errors.push(`广播名：${e?.message || '未知错误'}`)
    }
    // 2) Debug 模式 → CFG GLOBAL DEBUG_MODE（固件仅写内存；直接写入模式下 store 自动跟发 SAVE 落盘）
    const ok2 = await store.setDebugMode(draft.debug)
    if (!ok2) errors.push('Debug 模式')
    // 3) 全局心跳超时 → 写入设备 NVS
    const ok3 = await store.setGlobalConfig('HB_TIMEOUT_SEC', hb)
    if (!ok3) errors.push('心跳超时阈值')
    saveBtn.disabled = false
    if (errors.length === 0) {
      toast(store.settings.directSaveNVS ? '全局配置已写入控制板并落盘 NVS' : '全局配置已写入控制板内存，记得落盘 NVS', 'success')
      document.dispatchEvent(new Event('rerender'))
    } else {
      toast(`部分下发失败：${errors.join('；')}`, 'error')
    }
  }
  foot.append(resetBtn, saveBtn)
  card.appendChild(foot)

  wrap.appendChild(card)
  return wrap
}

// ================================================================
// 全局心跳超时阈值区块（草稿）
// ================================================================
function renderHeartbeatSection(): HTMLElement {
  const row = el('div', { class: 'mt-3 px-3 py-3 rounded-lg' })
  row.style.background = 'rgb(var(--c-element))'

  const head = el('div', { class: 'flex items-center justify-between mb-2' })
  const label = el('div', { class: 'text-sm font-semibold' }, ['全局心跳超时阈值'])
  label.style.color = 'rgb(var(--c-ink))'
  head.appendChild(label)
  if (store.isDebugMode()) head.appendChild(badge('GLOBAL', '--c-primary', true))
  row.appendChild(head)

  const curRow = el('div', { class: 'text-xs mb-2 flex items-center' })
  curRow.style.color = 'rgb(var(--c-ink-subtle))'
  curRow.textContent = `当前：${store.settings.hbTimeoutSec ?? 10} 秒`
  curRow.appendChild(helpTip('设备在此时间内未收到上位机心跳，将触发失联告警、风扇转入后备转速并断开。'))
  row.appendChild(curRow)

  const inputRow = el('div', { class: 'flex items-center gap-2' })
  const numInput = el('input', {
    type: 'number', class: 'input', style: 'max-width:160px;flex:1',
    min: '3', max: '60', value: String(draft?.hbTimeout ?? store.settings.hbTimeoutSec ?? 10),
  }) as HTMLInputElement
  numInput.oninput = () => { if (draft) draft!.hbTimeout = Number(numInput.value) }

  const unitLabel = el('span', { class: 'text-xs' }, ['秒'])
  unitLabel.style.color = 'rgb(var(--c-ink-subtle))'
  inputRow.append(numInput, unitLabel)
  row.appendChild(inputRow)

  return row
}

// ================================================================
// 设备广播名区块（草稿，不即时下发）
// ================================================================
function renderBleNameSection(d: DeviceInfo | null): HTMLElement {
  const row = el('div', { class: 'mt-3 px-3 py-3 rounded-lg' })
  row.style.background = 'rgb(var(--c-element))'

  const head = el('div', { class: 'flex items-center justify-between mb-2' })
  const label = el('div', { class: 'text-sm font-semibold' }, ['设备广播名'])
  label.style.color = 'rgb(var(--c-ink))'
  head.appendChild(label)

  const macSuffix = d?.address ? d.address.replace(/:/g, '').slice(-6).toUpperCase() : 'XXXXXX'
  const displayName = draft?.loaded
    ? (draft!.bleName || `默认（NR_F2S4_${macSuffix}）`)
    : '查询中…'
  const curBadge = badge(draft?.bleName ? '已自定义' : '默认', '--c-primary', true)
  head.appendChild(curBadge)
  row.appendChild(head)

  const curRow = el('div', { class: 'text-xs mb-2' })
  curRow.style.color = 'rgb(var(--c-ink-subtle))'
  curRow.textContent = `当前：${displayName}`
  row.appendChild(curRow)

  const inputRow = el('div', { class: 'flex items-center gap-2' })
  const nameInput = el('input', {
    class: 'input', style: 'max-width:240px;flex:1',
    placeholder: '输入新广播名（1~20 字符）',
    value: draft?.bleName ?? '',
    maxlength: '20',
  }) as HTMLInputElement
  nameInput.oninput = () => { if (draft) draft!.bleName = nameInput.value }

  inputRow.appendChild(nameInput)
  row.appendChild(inputRow)

  const hint = el('div', { class: 'text-xs mt-2 flex items-center' })
  hint.style.color = 'rgb(var(--c-ink-subtle))'
  hint.textContent = '保存后写入 NVS 并立即应用'
  hint.appendChild(helpTip('设备将以新名称广播（下次广播周期生效）。'))
  row.appendChild(hint)

  return row
}

// ================================================================
// Debug 模式开关区块（草稿，不即时下发）
// ================================================================
function renderDebugSection(): HTMLElement {
  const row = el('div', { class: 'mt-3 px-3 py-3 rounded-lg' })
  row.style.background = 'rgb(var(--c-element))'

  const head = el('div', { class: 'flex items-center justify-between' })
  const labelBox = el('div', { class: 'flex-1' })
  const label = el('div', { class: 'text-sm font-semibold' }, ['Debug 模式'])
  label.style.color = 'rgb(var(--c-ink))'
  labelBox.appendChild(label)

  const hint = el('div', { class: 'text-xs mt-1 flex items-center' })
  hint.style.color = 'rgb(var(--c-ink-subtle))'
  hint.textContent = '输出更多调试信息'
  hint.appendChild(helpTip('开启后设备 USB 串口输出更多调试信息（收发数据/心跳超时/fallback 切换）。'))
  labelBox.appendChild(hint)

  const tgWrap = el('label', { class: 'toggle' })
  const tgInput = el('input', { type: 'checkbox' }) as HTMLInputElement
  tgInput.checked = draft?.debug === true
  tgInput.disabled = !draft?.loaded
  tgInput.onchange = () => { if (draft) draft!.debug = tgInput.checked }
  tgWrap.append(tgInput, el('span', { class: 'toggle-slider' }))

  head.append(labelBox, tgWrap)
  row.appendChild(head)

  return row
}
