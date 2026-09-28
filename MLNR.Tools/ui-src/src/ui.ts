// TF-FAN 风扇控制器轻量 UI 工具：toast / confirm / element 构造 / SVG 图标

// ===== SVG 图标（风扇控制器专用） =====
export type IconName =
  | 'fan'            // 风扇
  | 'fan-off'        // 风扇停止
  | 'settings'       // 设置
  | 'back'           // 返回
  | 'refresh'        // 刷新
  | 'power'          // 电源/连接
  | 'power-off'      // 断开
  | 'signal'         // 信号强度
  | 'check'          // 对勾
  | 'x'              // 关闭
  | 'plus'           // 加
  | 'minus'          // 减
  | 'save'           // 保存
  | 'warning'        // 警告
  | 'clock'          // 心跳/时钟
  | 'bolt'           // 闪电/PWM
  | 'wifi'           // 蓝牙/WiFi
  | 'link'           // 连接中
  | 'sliders'        // 调速
  | 'rotate-cw'      // 旋转
  | 'cpu'            // CPU 芯片
  | 'hdd'            // 硬盘
  | 'mb'             // 主板
  | 'thermometer'    // 温度计
  | 'chart'          // 折线图
  | 'curve'          // 曲线
  | 'help'           // 帮助问号
  | 'info'           // 信息圈
  | 'edit'           // 编辑
  | 'reset'          // 重置（绕圈箭头）
  | 'trash'          // 删除
  | 'plus-square'    // 加方框
  | 'log'            // 日志/文件
  | 'droplet'        // 湿度水滴（FS5 环境传感器）
  | 'gauge'          // 气压表盘（FS5 环境传感器）
  | 'mountain'       // 海拔山峰（FS5 环境传感器）
  | 'gpu'            // 显卡
  | 'memory'         // 内存条
  | 'expansion'      // 扩展板
  | 'ssd'            // 固态硬盘
  | 'chassis'        // 机箱
  | 'psu'            // 电源
  | 'palette'        // 调色盘（配色风格）
  | 'nav'            // 底部导航栏（三行菜单）
  | 'chevron-up'     // 上移
  | 'chevron-down'   // 下移

// 风扇叶片图标（单色三叶，三瓣严格按 120° 旋转对称 —— FS.md 上位机#9 拍板）
// 单片扇叶曲线：从中心伸出、带顺时针扫掠、圆润叶尖；经 120°/240° 旋转复制保证辐射对称
const FAN_BLADE =
  '<path d="M12 12 C12.5 8 14.2 4.8 17 4.2 C18.7 3.9 19.2 5.7 17.4 7.5 C15.6 9.2 13.4 11.2 12 12 Z"/>' +
  '<path d="M12 12 C12.5 8 14.2 4.8 17 4.2 C18.7 3.9 19.2 5.7 17.4 7.5 C15.6 9.2 13.4 11.2 12 12 Z" transform="rotate(120 12 12)"/>' +
  '<path d="M12 12 C12.5 8 14.2 4.8 17 4.2 C18.7 3.9 19.2 5.7 17.4 7.5 C15.6 9.2 13.4 11.2 12 12 Z" transform="rotate(240 12 12)"/>' +
  '<circle cx="12" cy="12" r="1.9"/>'

const ICON_PATHS: Record<IconName, string> = {
  // 三叶扇叶（单色实心，120° 严格对称）
  fan: `<g fill="currentColor" stroke="none">${FAN_BLADE}</g>`,
  'fan-off': `<g fill="currentColor" stroke="none">${FAN_BLADE}</g><line x1="3" y1="3" x2="21" y2="21"/>`,

  // 设置齿轮
  settings: '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1 0 2.83 2 2 0 0 1-2.83 0l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-2 2 2 2 0 0 1-2-2v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83 0 2 2 0 0 1 0-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1-2-2 2 2 0 0 1 2-2h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 0-2.83 2 2 0 0 1 2.83 0l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 2-2 2 2 0 0 1 2 2v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 0 2 2 0 0 1 0 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 2 2 2 2 0 0 1-2 2h-.09a1.65 1.65 0 0 0-1.51 1z"/>',

  // 底部导航栏（三行菜单）
  nav: '<line x1="3" y1="6" x2="21" y2="6"/><line x1="3" y1="12" x2="21" y2="12"/><line x1="3" y1="18" x2="14" y2="18"/>',

  // 上移 / 下移（顺序调整）
  'chevron-up': '<polyline points="18 15 12 9 6 15"/>',
  'chevron-down': '<polyline points="6 9 12 15 18 9"/>',

  // 返回箭头
  back: '<line x1="19" y1="12" x2="5" y2="12"/><polyline points="12 19 5 12 12 5"/>',

  // 刷新
  refresh: '<path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M3 21v-5h5"/>',

  // 电源
  power: '<path d="M18.36 6.64a9 9 0 1 1-12.73 0"/><line x1="12" y1="2" x2="12" y2="12"/>',
  'power-off': '<path d="M18.36 6.64a9 9 0 1 1-12.73 0"/><line x1="12" y1="2" x2="12" y2="12"/><line x1="3" y1="3" x2="21" y2="21"/>',

  // 信号（WiFi样式）
  signal: '<path d="M5 12.55a11 11 0 0 1 14.08 0"/><path d="M1.42 9a16 16 0 0 1 21.16 0"/><path d="M8.53 16.11a6 6 0 0 1 6.95 0"/><line x1="12" y1="20" x2="12.01" y2="20"/>',

  // 对勾
  check: '<polyline points="20 6 9 17 4 12"/>',

  // X 关闭
  x: '<line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/>',

  // 加减
  plus: '<line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/>',
  minus: '<line x1="5" y1="12" x2="19" y2="12"/>',
  'plus-square': '<rect x="3" y="3" width="18" height="18" rx="2"/><line x1="12" y1="8" x2="12" y2="16"/><line x1="8" y1="12" x2="16" y2="12"/>',

  // 保存
  save: '<path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z"/><polyline points="17 21 17 13 7 13 7 21"/><polyline points="7 3 7 8 15 8"/>',

  // 警告
  warning: '<path d="M10.29 3.86L1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/>',

  // 时钟/心跳
  clock: '<circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/>',

  // 闪电/PWM
  bolt: '<polygon points="13 2 3 14 12 14 11 22 21 10 12 10 13 2"/>',

  // wifi/蓝牙
  wifi: '<path d="M5 12.55a11 11 0 0 1 14.08 0"/><path d="M1.42 9a16 16 0 0 1 21.16 0"/><path d="M8.53 16.11a6 6 0 0 1 6.95 0"/><line x1="12" y1="20" x2="12.01" y2="20"/>',

  // 链接
  link: '<path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/>',

  // 调速滑块
  sliders: '<line x1="4" y1="21" x2="4" y2="14"/><line x1="4" y1="10" x2="4" y2="3"/><line x1="12" y1="21" x2="12" y2="12"/><line x1="12" y1="8" x2="12" y2="3"/><line x1="20" y1="21" x2="20" y2="16"/><line x1="20" y1="12" x2="20" y2="3"/><line x1="1" y1="14" x2="7" y2="14"/><line x1="9" y1="8" x2="15" y2="8"/><line x1="17" y1="16" x2="23" y2="16"/>',

  // 旋转（顺时针）
  'rotate-cw': '<polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/>',

  // CPU 芯片
  cpu: '<rect x="4" y="4" width="16" height="16" rx="2"/><rect x="9" y="9" width="6" height="6"/><line x1="9" y1="1" x2="9" y2="4"/><line x1="15" y1="1" x2="15" y2="4"/><line x1="9" y1="20" x2="9" y2="23"/><line x1="15" y1="20" x2="15" y2="23"/><line x1="1" y1="9" x2="4" y2="9"/><line x1="1" y1="15" x2="4" y2="15"/><line x1="20" y1="9" x2="23" y2="9"/><line x1="20" y1="15" x2="23" y2="15"/>',

  // 硬盘
  hdd: '<rect x="2" y="6" width="20" height="12" rx="1"/><line x1="2" y1="10" x2="22" y2="10"/><circle cx="19" cy="16" r="1" fill="currentColor" stroke="none"/><circle cx="16" cy="16" r="1" fill="currentColor" stroke="none"/>',

  // 主板
  mb: '<rect x="2" y="4" width="20" height="16" rx="1"/><rect x="6" y="8" width="5" height="4"/><rect x="14" y="8" width="4" height="4"/><circle cx="19" cy="17" r="1" fill="currentColor" stroke="none"/><line x1="2" y1="16" x2="6" y2="16"/>',

  // 温度计
  thermometer: '<path d="M14 14.76V3.5a2.5 2.5 0 0 0-5 0v11.26a4.5 4.5 0 1 0 5 0z"/>',

  // 折线图
  chart: '<line x1="18" y1="20" x2="18" y2="10"/><line x1="12" y1="20" x2="12" y2="4"/><line x1="6" y1="20" x2="6" y2="14"/><line x1="3" y1="20" x2="21" y2="20"/>',

  // 曲线
  curve: '<path d="M3 20C6 16 8 8 12 8s6 8 9 12"/>',

  // 帮助问号
  help: '<circle cx="12" cy="12" r="10"/><path d="M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3"/><line x1="12" y1="17" x2="12.01" y2="17"/>',

  // 信息圈
  info: '<circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/>',

  // 编辑
  edit: '<path d="M12 20h9"/><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4 12.5-12.5z"/>',

  // 重置
  reset: '<polyline points="23 4 23 10 17 10"/><path d="M20.49 15a9 9 0 1 1-2.12-9.36L23 10"/>',

  // 删除
  trash: '<polyline points="3 6 5 6 21 6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>',

  // 日志/文件
  log: '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/><line x1="16" y1="13" x2="8" y2="13"/><line x1="16" y1="17" x2="8" y2="17"/><line x1="10" y1="9" x2="8" y2="9"/>',

  // 湿度水滴（FS5 环境传感器）
  droplet: '<path d="M12 2.7s6.5 7 6.5 11.3a6.5 6.5 0 0 1-13 0C5.5 9.7 12 2.7 12 2.7z"/>',

  // 气压表盘（FS5 环境传感器）
  gauge: '<circle cx="12" cy="13" r="9"/><line x1="12" y1="13" x2="17" y2="10"/><line x1="12" y1="4" x2="12" y2="7"/>',

  // 海拔山峰（FS5 环境传感器）
  mountain: '<path d="M8 3l4 8 5-5 5 16H2L8 3z"/>',

  // 显卡（FS.md 上位机#5 温度传感器图标）
  gpu: '<rect x="3" y="7" width="18" height="12" rx="1.5"/><rect x="7" y="9" width="8" height="3"/><circle cx="17" cy="14.5" r="2.5"/><line x1="3" y1="13" x2="5" y2="13"/>',

  // 内存条（FS.md 上位机#5）
  memory: '<rect x="4" y="5" width="16" height="14" rx="1.5"/><line x1="8" y1="5" x2="8" y2="19"/><line x1="12" y1="5" x2="12" y2="19"/><line x1="16" y1="5" x2="16" y2="19"/>',

  // 扩展板（FS.md 上位机#5）
  expansion: '<rect x="5" y="3" width="14" height="18" rx="1.5"/><rect x="8" y="5" width="8" height="6" rx="1"/><line x1="9" y1="14" x2="15" y2="14"/><line x1="9" y1="17" x2="13" y2="17"/>',

  // 固态硬盘（FS.md 上位机#5）
  ssd: '<rect x="2" y="6" width="20" height="12" rx="1.5"/><line x1="2" y1="10" x2="22" y2="10"/><line x1="6" y1="14" x2="11" y2="14"/><line x1="6" y1="16.5" x2="9" y2="16.5"/>',

  // 机箱（FS.md 上位机#5）
  chassis: '<rect x="4" y="2" width="16" height="20" rx="1.5"/><rect x="8" y="5" width="8" height="5" rx="1"/><circle cx="9" cy="15" r="2"/><circle cx="15" cy="15" r="2"/>',

  // 电源（FS.md 上位机#5）
  psu: '<rect x="4" y="7" width="16" height="10" rx="1.5"/><circle cx="17" cy="12" r="2"/><line x1="7" y1="12" x2="13" y2="12"/>',

  // 调色盘（配色风格）
  palette: '<path d="M12 22a10 10 0 1 1 10-10c0 1.66-1.34 3-3 3h-2.5a2.5 2.5 0 0 0-2 4.06c.5.7.22 1.44.16 1.69A10 10 0 0 1 12 22z"/><circle cx="7.5" cy="11.5" r="1"/><circle cx="11" cy="7.5" r="1"/><circle cx="16" cy="8.5" r="1"/>',
}

export function svgIcon(name: IconName, size = 16): SVGSVGElement {
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg')
  svg.setAttribute('width', String(size))
  svg.setAttribute('height', String(size))
  svg.setAttribute('viewBox', '0 0 24 24')
  svg.setAttribute('fill', 'none')
  svg.setAttribute('stroke', 'currentColor')
  svg.setAttribute('stroke-width', '2')
  svg.setAttribute('stroke-linecap', 'round')
  svg.setAttribute('stroke-linejoin', 'round')
  svg.innerHTML = ICON_PATHS[name] || ''
  return svg
}

// ===== 元素构造快捷函数 =====
export function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  attrs: Record<string, string | boolean> = {},
  children: (Node | string)[] = []
): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag)
  for (const [k, v] of Object.entries(attrs)) {
    if (k === 'class') e.className = String(v)
    else if (k === 'html') e.innerHTML = String(v)
    else if (typeof v === 'boolean') {
      ;(e as Record<string, boolean>)[k] = v
    } else {
      e.setAttribute(k, v)
    }
  }
  for (const c of children) {
    if (c == null) continue // 防御：跳过 null/undefined 子节点，避免 appendChild 崩溃
    e.appendChild(typeof c === 'string' ? document.createTextNode(c) : c)
  }
  return e
}

// ===== 格式化工具 =====
export function formatTime(iso: string): string {
  if (!iso) return '-'
  const d = new Date(iso)
  if (isNaN(d.getTime())) return iso
  return d.toLocaleString('zh-CN', { hour12: false })
}

// 根据信号强度百分比返回文字等级
export function signalLabel(pct: number): string {
  if (pct >= 80) return '极强'
  if (pct >= 60) return '良好'
  if (pct >= 40) return '一般'
  if (pct >= 20) return '较弱'
  return '极弱'
}

// ===== Toast =====
let toastTimer: number | null = null
export function toast(msg: string, type: 'info' | 'error' | 'success' = 'info') {
  let box = document.getElementById('toast-box')
  if (!box) {
    box = el('div', { id: 'toast-box', class: 'fixed top-4 right-4 z-50 flex flex-col gap-2' })
    document.body.appendChild(box)
  }
  let bgClass = ''
  let textClass = ''
  switch (type) {
    case 'error':
      bgClass = 'rgb(var(--c-danger))'
      textClass = 'rgb(var(--c-on-primary))'
      break
    case 'success':
      bgClass = 'rgb(var(--c-success))'
      textClass = 'rgb(var(--c-on-primary))'
      break
    default:
      bgClass = 'rgb(var(--c-element))'
      textClass = 'rgb(var(--c-ink))'
  }
  const t = el(
    'div',
    { class: 'text-sm px-4 py-2 rounded shadow-lg transition-opacity' },
    [msg]
  )
  t.style.background = bgClass
  t.style.color = textClass
  box.appendChild(t)
  setTimeout(() => {
    t.style.opacity = '0'
    setTimeout(() => t.remove(), 300)
  }, 2500)
  void toastTimer
}

// ===== Confirm 弹窗（Promise） =====
export function confirmDialog(message: string, title = '确认操作', danger = false): Promise<boolean> {
  return new Promise((resolve) => {
    const overlay = el('div', {
      class: 'fixed inset-0 z-50 flex items-center justify-center',
    })
    overlay.style.background = 'rgb(var(--c-overlay) / 0.6)'
    const modal = el('div', { class: 'card w-80 p-5' })
    if (danger) {
      modal.style.border = '2px solid rgb(var(--c-danger))'
    }
    const titleColor = danger ? 'rgb(var(--c-danger))' : 'rgb(var(--c-ink))'
    const titleEl = el('h3', { class: 'font-semibold text-base mb-2' }, [title])
    titleEl.style.color = titleColor
    const msgEl = el('p', { class: 'text-sm mb-4' }, [message])
    msgEl.style.color = 'rgb(var(--c-ink-muted))'
    const btns = el('div', { class: 'flex justify-end gap-2' })
    const cancel = el('button', { class: 'btn' }, ['取消'])
    const ok = el('button', { class: 'btn ' + (danger ? 'btn-danger' : 'btn-primary') }, ['确定'])
    cancel.onclick = () => {
      overlay.remove()
      resolve(false)
    }
    ok.onclick = () => {
      overlay.remove()
      resolve(true)
    }
    btns.append(cancel, ok)
    modal.append(titleEl, msgEl, btns)
    overlay.appendChild(modal)
    overlay.onclick = (e) => {
      if (e.target === overlay) {
        overlay.remove()
        resolve(false)
      }
    }
    document.body.appendChild(overlay)
  })
}

// ===== 占用进程三选弹窗（终止进程 / 强制卸载 / 取消） =====
export function occupyDialog(
  device: string,
  procs: { pid: number; command: string; user: string }[],
): Promise<'kill' | 'force' | 'cancel'> {
  return new Promise((resolve) => {
    const overlay = el('div', {
      class: 'fixed inset-0 z-50 flex items-center justify-center',
    })
    overlay.style.background = 'rgb(var(--c-overlay) / 0.6)'
    const modal = el('div', { class: 'card w-96 p-5' })
    modal.style.border = '2px solid rgb(var(--c-danger))'
    const titleEl = el('h3', { class: 'font-semibold text-base mb-2' }, ['卸载失败：进程占用'])
    titleEl.style.color = 'rgb(var(--c-danger))'
    const msgEl = el('p', { class: 'text-sm mb-3' }, [
      `${device} 卸载失败，以下 ${procs.length} 个进程正在占用：`,
    ])
    msgEl.style.color = 'rgb(var(--c-ink-muted))'
    const list = el('div', { class: 'flex flex-col gap-1 mb-4 max-h-44 overflow-y-auto text-xs' })
    list.style.border = '1px solid rgb(var(--c-line-subtle))'
    list.style.borderRadius = '6px'
    list.style.padding = '6px 8px'
    list.style.background = 'rgb(var(--c-overlay) / 0.2)'
    if (procs.length === 0) {
      list.appendChild(el('span', {}, ['（无法读取进程清单）']))
    } else {
      for (const p of procs) {
        const row = el('div', { class: 'flex items-center gap-2 py-0.5' })
        row.style.fontFamily = 'var(--font-mono, monospace)'
        row.style.color = 'rgb(var(--c-ink))'
        row.append(
          el('span', {}, [`PID ${p.pid}`]),
          el('span', { class: 'flex-1 truncate' }, [p.command || '—']),
          el('span', {}, [p.user || '']),
        )
        list.appendChild(row)
      }
    }
    const hint = el('p', { class: 'text-xs mb-4' }, ['终止进程：结束后自动重试卸载；强制卸载：umount -f 强制解除挂载。'])
    hint.style.color = 'rgb(var(--c-ink-subtle))'
    const btns = el('div', { class: 'flex justify-end gap-2' })
    const cancel = el('button', { class: 'btn' }, ['取消'])
    const force = el('button', { class: 'btn btn-danger' }, ['强制卸载'])
    const kill = el('button', { class: 'btn btn-primary' }, ['终止进程'])
    const close = (v: 'kill' | 'force' | 'cancel') => {
      overlay.remove()
      resolve(v)
    }
    cancel.onclick = () => close('cancel')
    force.onclick = () => close('force')
    kill.onclick = () => close('kill')
    btns.append(cancel, force, kill)
    modal.append(titleEl, msgEl, list, hint, btns)
    overlay.appendChild(modal)
    overlay.onclick = (e) => {
      if (e.target === overlay) close('cancel')
    }
    document.body.appendChild(overlay)
  })
}

// ===== 数字/时间格式化 =====

export function formatTemp(v: number | null | undefined, digits = 1): string {
  if (v === null || v === undefined || Number.isNaN(v)) return '—'
  return `${v.toFixed(digits)}℃`
}

export function formatSpeedPct(v: number | null | undefined): string {
  if (v === null || v === undefined || Number.isNaN(v)) return '—'
  return `${Math.round(v)}%`
}

export function formatDateTimeLocal(tsMs: number): string {
  const d = new Date(tsMs)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

export function formatXAxisTime(tsMs: number, spanMs: number): string {
  const d = new Date(tsMs)
  const pad = (n: number) => String(n).padStart(2, '0')
  if (spanMs <= 6 * 3600 * 1000) {
    // 6 小时内，显示 HH:MM
    return `${pad(d.getHours())}:${pad(d.getMinutes())}`
  }
  if (spanMs <= 3 * 24 * 3600 * 1000) {
    // 3 天内：MM-DD HH:MM
    return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
  }
  // 更长：MM-DD
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

// ===== Canvas 折线图（多 series / 单 series） =====

export interface LineSeries {
  name: string
  color: string
  points: Array<{ t: number; v: number }>
}

export interface DrawLineChartOptions {
  title?: string
  yLabel?: string       // 右侧 Y 轴单位提示
  yMin?: number
  yMax?: number
  yPadding?: number     // #28/#30/#31/#32：Y 轴自动范围时的上下 padding（未指定 yMin/yMax 时生效）
  gridStepY?: number
  height?: number       // 画布高度，宽度自适应父容器
  dark?: boolean        // 暗黑配色（默认 true）
  showLegend?: boolean
  smooth?: boolean      // #26：平滑曲线（Catmull-Rom 样条），默认 true
  hoverTooltip?: boolean // #27：鼠标悬停显示最近数据点的时间和值，默认 true
  gapThresholdMs?: number // #33：数据间隔阈值（毫秒），相邻点间隔超过则断开该段曲线（不再绘制红框），默认不检测
  viewTMin?: number       // 固定显示窗口起点（ms 时间戳）：提供后 x 轴使用窗口而非数据范围
  viewTMax?: number       // 固定显示窗口终点（ms 时间戳）
  maxViewT?: number       // 拖动时窗口右端上限（ms，如当前时间），默认不限制
  onViewChange?: (tMin: number, tMax: number) => void // 拖动平移回调（拖动中节流 ~200ms + 松手必调），供外部滑动缓存补拉
}

/**
 * 将 Canvas 调整为 devicePixelRatio，并绘制折线图。
 * #26：支持平滑曲线（Catmull-Rom 样条插值）
 * #27：支持鼠标悬停显示最近数据点的时间和所有曲线值
 * #33：支持数据间隔检测，无数据区间绘制虚线框
 * #28/#30/#31/#32：支持 yPadding 自动调整 Y 轴范围
 * 返回一个 cleanup（取消 ResizeObserver + 事件监听）函数。
 */
export function drawLineChart(
  canvas: HTMLCanvasElement,
  series: LineSeries[],
  opts: DrawLineChartOptions = {}
): () => void {
  // series 可被原地替换（图例开关场景），draw 闭包读取 currentSeries
  let currentSeries = series
  const smooth = opts.smooth !== false // #26：默认开启平滑曲线
  const enableHover = opts.hoverTooltip !== false // #27：默认开启悬停提示
  // 图表配色随主题（CSS 变量）动态读取，每次 draw 重取
  const chartColors = () => ({
    bgGrid: cssVarColor('--c-chart-grid', 0.14),
    axisColor: cssVarColor('--c-chart-axis', 0.5),
    inkColor: cssVarColor('--c-chart-text'),
    cursorLine: cssVarColor('--c-chart-cursor', 0.4),
    tooltipBg: cssVarColor('--c-chart-tooltip-bg', 0.94),
    tooltipBorder: cssVarColor('--c-chart-tooltip-border', 0.9),
    tooltipTime: cssVarColor('--c-chart-tooltip-time'),
    tooltipText: cssVarColor('--c-chart-tooltip-text'),
    dotStroke: cssVarColor('--c-chart-dot-stroke'),
  })

  // #27：悬停状态（鼠标 X 坐标，相对于 canvas CSS 像素；null 表示未悬停）
  let hoverX: number | null = null

  // #36：固定显示窗口（视图可拖动平移）。提供 viewTMin/viewTMax 时启用，
  // 拖动更新 viewMin/viewMax 并重绘；曲线数据按窗口裁剪，未覆盖区留空。
  const fixedView = opts.viewTMin !== undefined && opts.viewTMax !== undefined
  let viewMin = opts.viewTMin ?? 0
  let viewMax = opts.viewTMax ?? 0

  const draw = () => {
    const cssW = canvas.clientWidth || 600
    const cssH = opts.height || 220
    const dpr = Math.max(1, window.devicePixelRatio || 1)
    canvas.width = Math.round(cssW * dpr)
    canvas.height = Math.round(cssH * dpr)
    canvas.style.height = `${cssH}px`
    const ctx = canvas.getContext('2d')
    if (!ctx) return
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    ctx.clearRect(0, 0, cssW, cssH)
    // 每次 draw 读取主题变量，保证切换主题后重绘即换色
    const colors = chartColors()

    const pad = { l: 34, r: 10, t: opts.title ? 28 : 12, b: 26 }
    const chartW = cssW - pad.l - pad.r
    const chartH = cssH - pad.t - pad.b
    if (chartW <= 10 || chartH <= 10) return

    // 曲线数据按显示窗口裁剪（固定窗口模式）；非固定窗口使用全量数据
    const windowedSeries = fixedView
      ? currentSeries.map(s => ({ ...s, points: s.points.filter(p => p.t >= viewMin && p.t <= viewMax) }))
      : currentSeries

    // 计算时间范围与 Y 范围（Y 基于窗口内可见点）
    let tMin = Infinity, tMax = -Infinity, vMin = Infinity, vMax = -Infinity
    let hasPoints = false
    for (const s of windowedSeries) {
      for (const p of s.points) {
        if (p.t < tMin) tMin = p.t
        if (p.t > tMax) tMax = p.t
        if (p.v < vMin) vMin = p.v
        if (p.v > vMax) vMax = p.v
        hasPoints = true
      }
    }
    if (fixedView) {
      tMin = viewMin
      tMax = viewMax
    }
    if (!hasPoints && !fixedView) {
      ctx.save()
      ctx.fillStyle = colors.inkColor
      ctx.globalAlpha = 0.6
      ctx.font = '12px system-ui, -apple-system, sans-serif'
      ctx.textAlign = 'center'
      ctx.fillText('暂无历史数据', cssW / 2, cssH / 2)
      ctx.restore()
      return
    }
    if (!fixedView && tMin === tMax) tMax = tMin + 1
    // #28/#30/#31/#32：Y 轴范围处理
    if (opts.yMin !== undefined) vMin = opts.yMin
    if (opts.yMax !== undefined) vMax = opts.yMax
    // 若未显式指定 yMin/yMax，且设置了 yPadding，则自动加 padding
    if (opts.yMin === undefined && opts.yMax === undefined && opts.yPadding !== undefined) {
      vMin -= opts.yPadding
      vMax += opts.yPadding
    }
    if (!Number.isFinite(vMin)) vMin = 0
    if (!Number.isFinite(vMax)) vMax = 100
    if (vMin === vMax) { vMin -= 5; vMax += 5 }
    const spanMs = tMax - tMin

    const xOf = (t: number) => pad.l + ((t - tMin) / (tMax - tMin)) * chartW
    const yOf = (v: number) => pad.t + (1 - (v - vMin) / (vMax - vMin)) * chartH

    // #33：无数据区间检测（断线用途）：相邻点间隔超过阈值 → 该段断开
    const gapThreshold = opts.gapThresholdMs && opts.gapThresholdMs > 0 ? opts.gapThresholdMs : Infinity

    // 网格（横）+ Y 轴刻度
    const steps = opts.gridStepY ? Math.max(2, Math.ceil((vMax - vMin) / opts.gridStepY)) : 5
    ctx.save()
    ctx.font = '11px system-ui, -apple-system, sans-serif'
    ctx.textAlign = 'right'
    ctx.textBaseline = 'middle'
    ctx.fillStyle = colors.inkColor
    for (let i = 0; i <= steps; i++) {
      const r = i / steps
      const y = pad.t + r * chartH
      const v = vMax - r * (vMax - vMin)
      ctx.strokeStyle = colors.bgGrid
      ctx.beginPath()
      ctx.moveTo(pad.l, y)
      ctx.lineTo(pad.l + chartW, y)
      ctx.stroke()
      ctx.fillStyle = colors.inkColor
      ctx.globalAlpha = 0.8
      let label = Number.isInteger(v) ? String(v) : v.toFixed(1)
      if (opts.yLabel) label += opts.yLabel
      ctx.fillText(label, pad.l - 4, y)
      ctx.globalAlpha = 1
    }
    // 网格（竖）+ X 轴刻度 5 个
    const xSteps = 5
    ctx.textAlign = 'center'
    ctx.textBaseline = 'top'
    for (let i = 0; i <= xSteps; i++) {
      const r = i / xSteps
      const x = pad.l + r * chartW
      const t = tMin + r * (tMax - tMin)
      ctx.strokeStyle = colors.bgGrid
      ctx.beginPath()
      ctx.moveTo(x, pad.t)
      ctx.lineTo(x, pad.t + chartH)
      ctx.stroke()
      ctx.fillStyle = colors.inkColor
      ctx.globalAlpha = 0.8
      ctx.fillText(formatXAxisTime(t, spanMs), x, pad.t + chartH + 6)
      ctx.globalAlpha = 1
    }
    // X/Y 轴
    ctx.strokeStyle = colors.axisColor
    ctx.lineWidth = 1
    ctx.strokeRect(pad.l + 0.5, pad.t + 0.5, chartW - 1, chartH - 1)

    // 固定窗口但窗口内无数据：网格/坐标已绘制，中间提示（曲线/悬停自然跳过）
    if (fixedView && windowedSeries.every(s => s.points.length === 0)) {
      ctx.save()
      ctx.fillStyle = colors.inkColor
      ctx.globalAlpha = 0.6
      ctx.font = '12px system-ui, -apple-system, sans-serif'
      ctx.textAlign = 'center'
      ctx.textBaseline = 'middle'
      ctx.fillText('该范围暂无数据', pad.l + chartW / 2, pad.t + chartH / 2)
      ctx.restore()
    }

    // 画每条 series（#33：相邻点间隔超过 gapThresholdMs 时断开该段曲线）
    for (const s of windowedSeries) {
      if (s.points.length === 0) continue

      // 按时间间隔分段（超过阈值 → 新的一段，段与段之间不连线）
      const segments: Array<Array<{ x: number; y: number }>> = []
      let curSeg: Array<{ x: number; y: number }> = []
      for (let i = 0; i < s.points.length; i++) {
        if (i > 0 && (s.points[i].t - s.points[i - 1].t) > gapThreshold && curSeg.length > 0) {
          segments.push(curSeg)
          curSeg = []
        }
        curSeg.push({ x: xOf(s.points[i].t), y: yOf(s.points[i].v) })
      }
      if (curSeg.length > 0) segments.push(curSeg)

      ctx.strokeStyle = s.color
      ctx.lineWidth = 1.8
      for (const seg of segments) {
        if (seg.length === 1) {
          // 孤立点（前后均断开）：仅绘制圆点
          ctx.save()
          ctx.fillStyle = s.color
          ctx.beginPath()
          ctx.arc(seg[0].x, seg[0].y, 2, 0, Math.PI * 2)
          ctx.fill()
          ctx.restore()
          continue
        }
        ctx.beginPath()
        if (smooth && seg.length >= 3) {
          // #26：平滑曲线 — 使用二次贝塞尔曲线（中点法）
          ctx.moveTo(seg[0].x, seg[0].y)
          for (let i = 1; i < seg.length - 1; i++) {
            const xc = (seg[i].x + seg[i + 1].x) / 2
            const yc = (seg[i].y + seg[i + 1].y) / 2
            ctx.quadraticCurveTo(seg[i].x, seg[i].y, xc, yc)
          }
          // 最后一段直线到末点
          const last = seg[seg.length - 1]
          ctx.lineTo(last.x, last.y)
        } else {
          // 直线模式（点数不足 3 时回退）
          ctx.moveTo(seg[0].x, seg[0].y)
          for (let i = 1; i < seg.length; i++) ctx.lineTo(seg[i].x, seg[i].y)
        }
        ctx.stroke()

        // 渐变色填充（按分段填充：断点处不填充，视觉上明确显示断开）
        try {
          const grad = ctx.createLinearGradient(0, pad.t, 0, pad.t + chartH)
          const rgba = hexOrCssToRgba(s.color, 0.15)
          grad.addColorStop(0, rgba)
          grad.addColorStop(1, hexOrCssToRgba(s.color, 0))
          ctx.fillStyle = grad
          ctx.lineTo(seg[seg.length - 1].x, pad.t + chartH)
          ctx.lineTo(seg[0].x, pad.t + chartH)
          ctx.closePath()
          ctx.fill()
        } catch { /* ignore */ }
      }
    }

    // #27：悬停提示 — 绘制垂直参考线、数据点标记和 tooltip
    if (enableHover && hoverX !== null && hoverX >= pad.l && hoverX <= pad.l + chartW) {
      // 找到鼠标 X 对应的时间
      const hoverT = tMin + ((hoverX - pad.l) / chartW) * (tMax - tMin)
      // 绘制垂直参考线
      ctx.save()
      ctx.strokeStyle = colors.cursorLine
      ctx.setLineDash([3, 3])
      ctx.lineWidth = 1
      ctx.beginPath()
      ctx.moveTo(hoverX, pad.t)
      ctx.lineTo(hoverX, pad.t + chartH)
      ctx.stroke()
      ctx.restore()

      // 为每条 series 找到离 hoverT 最近的数据点并绘制圆点
      const hoverValues: Array<{ name: string; color: string; v: number; t: number }> = []
      for (const s of windowedSeries) {
        if (s.points.length === 0) continue
        let nearest = s.points[0]
        let minDist = Math.abs(s.points[0].t - hoverT)
        for (const p of s.points) {
          const dist = Math.abs(p.t - hoverT)
          if (dist < minDist) { minDist = dist; nearest = p }
        }
        hoverValues.push({ name: s.name, color: s.color, v: nearest.v, t: nearest.t })
        // 绘制数据点圆点
        const px = xOf(nearest.t)
        const py = yOf(nearest.v)
        ctx.save()
        ctx.fillStyle = s.color
        ctx.beginPath()
        ctx.arc(px, py, 3.5, 0, Math.PI * 2)
        ctx.fill()
        ctx.strokeStyle = colors.dotStroke
        ctx.lineWidth = 1.5
        ctx.stroke()
        ctx.restore()
      }

      // 绘制 tooltip 框
      if (hoverValues.length > 0) {
        const tooltipTime = new Date(hoverT).toLocaleString('zh-CN', {
          month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit',
        })
        const lines: string[] = [tooltipTime]
        for (const hv of hoverValues) {
          const valStr = Number.isInteger(hv.v) ? String(hv.v) : hv.v.toFixed(1)
          lines.push(`${hv.name}: ${valStr}${opts.yLabel || ''}`)
        }
        // 计算 tooltip 尺寸
        ctx.save()
        ctx.font = '11px system-ui, -apple-system, sans-serif'
        const lineHeight = 16
        const padding = 6
        let maxW = 0
        for (const line of lines) {
          const w = ctx.measureText(line).width
          if (w > maxW) maxW = w
        }
        const tw = maxW + padding * 2
        const th = lines.length * lineHeight + padding * 2
        // tooltip 位置：优先在鼠标右侧，空间不够则在左侧
        let tx = hoverX + 10
        if (tx + tw > cssW - 4) tx = hoverX - tw - 10
        let ty = pad.t + 4
        if (ty + th > pad.t + chartH) ty = pad.t + chartH - th - 4
        // 绘制背景（手动圆角矩形，兼容旧版 Canvas API）
        ctx.fillStyle = colors.tooltipBg
        ctx.strokeStyle = colors.tooltipBorder
        ctx.lineWidth = 1
        ctx.beginPath()
        const rr = 4
        ctx.moveTo(tx + rr, ty)
        ctx.lineTo(tx + tw - rr, ty)
        ctx.quadraticCurveTo(tx + tw, ty, tx + tw, ty + rr)
        ctx.lineTo(tx + tw, ty + th - rr)
        ctx.quadraticCurveTo(tx + tw, ty + th, tx + tw - rr, ty + th)
        ctx.lineTo(tx + rr, ty + th)
        ctx.quadraticCurveTo(tx, ty + th, tx, ty + th - rr)
        ctx.lineTo(tx, ty + rr)
        ctx.quadraticCurveTo(tx, ty, tx + rr, ty)
        ctx.closePath()
        ctx.fill()
        ctx.stroke()
        // 绘制文字
        ctx.textAlign = 'left'
        ctx.textBaseline = 'top'
        for (let i = 0; i < lines.length; i++) {
          const ly = ty + padding + i * lineHeight
          if (i === 0) {
            // 时间行
            ctx.fillStyle = colors.tooltipTime
            ctx.font = '10px system-ui, -apple-system, sans-serif'
          } else {
            // 数据行 — 用对应颜色的小圆点
            const hv = hoverValues[i - 1]
            ctx.fillStyle = hv.color
            ctx.beginPath()
            ctx.arc(tx + padding + 3, ly + 5, 3, 0, Math.PI * 2)
            ctx.fill()
            ctx.fillStyle = colors.tooltipText
            ctx.font = '11px system-ui, -apple-system, sans-serif'
            ctx.fillText(lines[i], tx + padding + 10, ly)
            continue
          }
          ctx.fillText(lines[i], tx + padding, ly)
        }
        ctx.restore()
      }
    }

    // 标题
    if (opts.title) {
      ctx.save()
      ctx.fillStyle = colors.inkColor
      ctx.font = 'bold 12px system-ui, -apple-system, sans-serif'
      ctx.textAlign = 'left'
      ctx.textBaseline = 'top'
      ctx.fillText(opts.title, pad.l, 6)
      ctx.restore()
    }

    // 图例（右上）
    if (opts.showLegend !== false && currentSeries.length > 0) {
      ctx.save()
      ctx.font = '11px system-ui, -apple-system, sans-serif'
      ctx.textBaseline = 'top'
      let x = cssW - pad.r - 6
      const y = 4
      ctx.textAlign = 'right'
      for (let i = currentSeries.length - 1; i >= 0; i--) {
        const name = currentSeries[i].name
        const w = ctx.measureText(name).width + 18
        x -= w
        ctx.fillStyle = currentSeries[i].color
        ctx.fillRect(x, y + 4, 10, 10)
        ctx.fillStyle = colors.inkColor
        ctx.fillText(name, x + 14 + w - 22, y + 1)
        x -= 6
      }
      ctx.restore()
    }
    ctx.restore()
  }

  draw()
  const ro = new ResizeObserver(draw)
  ro.observe(canvas)
  // 外部也可以手动调用 redraw
  ;(canvas as any)._chartRedraw = draw
  // 原地替换 series（图例开关），不重建 DOM / 不整页重渲染
  ;(canvas as any).__lineChartSetSeries = (s: LineSeries[]) => {
    currentSeries = s
    draw()
  }

  // #36：拖动平移状态（固定窗口模式 + onViewChange 回调时启用）
  let panDownX: number | null = null
  let panDownTMin = 0
  let panDownTMax = 0
  let panning = false
  let lastViewEmit = 0

  // #27：悬停事件监听
  let hoverCleanup: (() => void) | null = null
  if (enableHover) {
    const onMove = (e: MouseEvent) => {
      if (panDownX !== null) return // 拖动平移中不触发悬停
      const rect = canvas.getBoundingClientRect()
      hoverX = e.clientX - rect.left
      draw()
    }
    const onLeave = () => {
      if (panDownX !== null) return
      hoverX = null
      draw()
    }
    canvas.addEventListener('mousemove', onMove)
    canvas.addEventListener('mouseleave', onLeave)
    hoverCleanup = () => {
      canvas.removeEventListener('mousemove', onMove)
      canvas.removeEventListener('mouseleave', onLeave)
    }
  }

  let panCleanup: (() => void) | null = null
  if (fixedView && opts.onViewChange) {
    // 触摸/指针拖动需要禁掉浏览器默认滚动与缩放（否则拖动被抢、一顿一顿）
    canvas.style.touchAction = 'none'
    // 拖动重绘合并到 rAF：触摸事件频率远高于 60fps，逐帧全量重绘会卡
    let rafPending = false
    const scheduleDraw = () => {
      if (rafPending) return
      rafPending = true
      requestAnimationFrame(() => {
        rafPending = false
        draw()
      })
    }
    const onPointerDown = (e: PointerEvent) => {
      panDownX = e.clientX
      panDownTMin = viewMin
      panDownTMax = viewMax
      panning = false
      try { canvas.setPointerCapture(e.pointerId) } catch { /* ignore */ }
    }
    const onPointerMove = (e: PointerEvent) => {
      if (panDownX === null) return
      const dx = e.clientX - panDownX
      if (!panning) {
        if (Math.abs(dx) < 3) return // 小于阈值视为点击，不进入拖动
        panning = true
        hoverX = null
      }
      const cssW = canvas.clientWidth || 600
      const chartW = Math.max(1, cssW - 34 - 10)
      const msPerPx = (panDownTMax - panDownTMin) / chartW
      // 内容跟手：向右拖(dx>0) → 窗口向过去移动(起点减小)，曲线跟随鼠标右移
      let nMin = panDownTMin - dx * msPerPx
      let nMax = panDownTMax - dx * msPerPx
      // 钳制：窗口右端不超过 maxViewT（如当前时间），避免拖入未来空白
      if (opts.maxViewT !== undefined && nMax > opts.maxViewT) {
        const over = nMax - opts.maxViewT
        nMin -= over
        nMax = opts.maxViewT
      }
      if (nMin === nMax) return
      viewMin = nMin
      viewMax = nMax
      scheduleDraw()
      const now = Date.now()
      if (now - lastViewEmit >= 200) { // 拖动中节流回调，让外部提前补拉越界数据
        lastViewEmit = now
        opts.onViewChange!(viewMin, viewMax)
      }
    }
    const onPointerUp = (e: PointerEvent) => {
      if (panDownX === null) return
      if (panning) opts.onViewChange!(viewMin, viewMax) // 松手必调一次，同步最终窗口
      panDownX = null
      panning = false
      try { canvas.releasePointerCapture(e.pointerId) } catch { /* ignore */ }
      draw()
    }
    canvas.addEventListener('pointerdown', onPointerDown)
    canvas.addEventListener('pointermove', onPointerMove)
    canvas.addEventListener('pointerup', onPointerUp)
    canvas.addEventListener('pointercancel', onPointerUp)
    panCleanup = () => {
      rafPending = false
      canvas.removeEventListener('pointerdown', onPointerDown)
      canvas.removeEventListener('pointermove', onPointerMove)
      canvas.removeEventListener('pointerup', onPointerUp)
      canvas.removeEventListener('pointercancel', onPointerUp)
    }
  }

  return () => {
    ro.disconnect()
    hoverCleanup?.()
    panCleanup?.()
  }
}

/** 原地更新折线图的 series 数据（例如图例显示/隐藏切换） */
export function updateLineChart(canvas: HTMLCanvasElement, series: LineSeries[]): void {
  const setter = (canvas as any).__lineChartSetSeries as ((s: LineSeries[]) => void) | undefined
  setter?.(series)
}

/** CSS 变量（三元组）→ Canvas 可用的数值色。Canvas 的 fillStyle/strokeStyle 不做 var() 替换，须取变量实际值 */
export function cssVarColor(varName: string, alpha?: number): string {
  let val = ''
  try {
    val = getComputedStyle(document.documentElement).getPropertyValue(varName).trim()
  } catch { /* ignore */ }
  const nums = val.split(/\s+/).filter(Boolean).slice(0, 3).join(',')
  if (!nums) return alpha === undefined ? 'rgb(59,130,246)' : `rgba(59,130,246,${alpha})`
  return alpha === undefined ? `rgb(${nums})` : `rgba(${nums},${alpha})`
}

function hexOrCssToRgba(css: string, alpha: number): string {  // 先尝试解析 css 颜色：用离屏 canvas
  try {
    const c = document.createElement('canvas').getContext('2d')!
    c.fillStyle = css
    if (!c.fillStyle || c.fillStyle === '#000000' && css.toLowerCase() !== '#000000' && css !== 'black') {
      // fallthrough
    } else {
      // fillStyle 会转成 rgb 或 #rrggbb
      const s = c.fillStyle
      if (s.startsWith('#') && s.length === 7) {
        const r = parseInt(s.slice(1, 3), 16)
        const g = parseInt(s.slice(3, 5), 16)
        const b = parseInt(s.slice(5, 7), 16)
        return `rgba(${r},${g},${b},${alpha})`
      }
      const m = /^rgb\((\d+),\s*(\d+),\s*(\d+)\)/.exec(String(s))
      if (m) {
        return `rgba(${m[1]},${m[2]},${m[3]},${alpha})`
      }
    }
  } catch { /* ignore */ }
  // 处理 rgb(var(--c-xxx)) 形式：提取变量
  const cssVar = /rgb\(var\((--[\w-]+)\)\)/.exec(css)
  if (cssVar) {
    const val = getComputedStyle(document.documentElement).getPropertyValue(cssVar[1]).trim()
    if (val) return `rgba(${val},${alpha})`
  }
  const rgb = /(\d+)[ ,]+(\d+)[ ,]+(\d+)/.exec(css)
  if (rgb) return `rgba(${rgb[1]},${rgb[2]},${rgb[3]},${alpha})`
  return `rgba(0,102,255,${alpha})`
}

// ===== 帮助对话框（ps2.md 第 7 项：已移除"使用说明"按钮与功能） =====

// ===== 重构新增：统一状态语义组件 =====
export type StatusTone = 'success' | 'warning' | 'danger' | 'info' | 'neutral'

const TONE_VAR: Record<StatusTone, string> = {
  success: '--c-success',
  warning: '--c-warning',
  danger: '--c-danger',
  info: '--c-primary',
  neutral: '--c-neutral',
}

/** 统一状态徽标：替代散落的 badge/status-dot，同一语义唯一表达 */
export function statusBadge(
  text: string,
  tone: StatusTone = 'neutral',
  opts: { soft?: boolean; dot?: boolean; pulse?: boolean } = {}
): HTMLElement {
  const soft = opts.soft !== false
  const varName = TONE_VAR[tone]
  const b = el('span', { class: 'inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-xs font-medium whitespace-nowrap' })
  b.style.background = soft ? `rgb(var(${varName}-soft))` : `rgb(var(${varName}))`
  b.style.color = soft ? `rgb(var(${varName}-soft-text))` : 'rgb(var(--c-on-primary))'
  if (opts.dot) {
    const dot = el('span', { class: 'inline-block h-1.5 w-1.5 rounded-full shrink-0' })
    dot.style.background = `rgb(var(${varName}))`
    if (opts.pulse) dot.style.animation = 'pulse 2s infinite'
    b.appendChild(dot)
  }
  b.appendChild(document.createTextNode(text))
  return b
}

/** 概览统计卡片（监控总览页用） */
export interface StatCardOptions {
  icon: IconName
  label: string
  value: string
  subtext?: string
  tone?: StatusTone
  onClick?: () => void
}

export function statCard(opts: StatCardOptions): HTMLElement {
  const tone = opts.tone || 'info'
  const varName = TONE_VAR[tone]
  const card = el('div', { class: 'card p-4 transition-all' })
  if (opts.onClick) {
    card.style.cursor = 'pointer'
    card.onclick = opts.onClick
    card.onmouseenter = () => { card.style.borderColor = `rgb(var(${varName}) / 0.5)` }
    card.onmouseleave = () => { card.style.borderColor = '' }
  }
  const header = el('div', { class: 'flex items-center justify-between mb-2' })
  const labelEl = el('span', { class: 'text-xs' }, [opts.label])
  labelEl.style.color = 'rgb(var(--c-ink-muted))'
  const iconBox = el('div', { class: 'flex h-8 w-8 items-center justify-center rounded-lg shrink-0' })
  iconBox.style.background = `rgb(var(${varName}-soft))`
  iconBox.style.color = `rgb(var(${varName}-soft-text))`
  iconBox.appendChild(svgIcon(opts.icon, 16))
  header.append(labelEl, iconBox)
  const valueEl = el('div', { class: 'text-2xl font-bold leading-tight' }, [opts.value])
  valueEl.style.color = 'rgb(var(--c-ink))'
  card.append(header, valueEl)
  if (opts.subtext) {
    const sub = el('div', { class: 'text-xs mt-1' }, [opts.subtext])
    sub.style.color = 'rgb(var(--c-ink-subtle))'
    card.appendChild(sub)
  }
  return card
}

/** 标签页组件 */
export interface TabItem {
  key: string
  label: string
}

export function tabs(items: TabItem[], activeKey: string, onChange: (key: string) => void): HTMLElement {
  const wrap = el('div', { class: 'flex gap-1 border-b mb-4 overflow-x-auto' })
  wrap.style.borderColor = 'rgb(var(--c-element))'
  for (const item of items) {
    const isActive = item.key === activeKey
    const btn = el('button', { class: 'px-4 py-2 text-sm font-medium transition-colors relative whitespace-nowrap shrink-0' }, [item.label])
    if (isActive) {
      btn.style.color = 'rgb(var(--c-primary))'
      const indicator = el('div', { class: 'absolute bottom-0 left-0 right-0 h-0.5' })
      indicator.style.background = 'rgb(var(--c-primary))'
      btn.appendChild(indicator)
    } else {
      btn.style.color = 'rgb(var(--c-ink-muted))'
      btn.onmouseenter = () => { btn.style.color = 'rgb(var(--c-ink))' }
      btn.onmouseleave = () => { btn.style.color = 'rgb(var(--c-ink-muted))' }
    }
    btn.onclick = () => onChange(item.key)
    wrap.appendChild(btn)
  }
  return wrap
}

/** 空态组件 */
export interface EmptyStateOptions {
  icon?: IconName
  title: string
  description?: string
  action?: { label: string; onClick: () => void }
}

export function emptyState(opts: EmptyStateOptions): HTMLElement {
  const wrap = el('div', { class: 'flex flex-col items-center justify-center py-10 text-center' })
  if (opts.icon) {
    const iconBox = el('div', { class: 'mb-3' })
    iconBox.style.opacity = '0.35'
    iconBox.appendChild(svgIcon(opts.icon, 40))
    wrap.appendChild(iconBox)
  }
  const title = el('div', { class: 'text-sm font-semibold mb-1' }, [opts.title])
  title.style.color = 'rgb(var(--c-ink))'
  wrap.appendChild(title)
  if (opts.description) {
    const desc = el('div', { class: 'text-xs max-w-xs' }, [opts.description])
    desc.style.color = 'rgb(var(--c-ink-muted))'
    wrap.appendChild(desc)
  }
  if (opts.action) {
    const btn = el('button', { class: 'btn btn-primary btn-sm mt-4' }, [opts.action.label])
    btn.onclick = opts.action.onClick
    wrap.appendChild(btn)
  }
  return wrap
}

/** 侧边导航组件 */
export interface NavItem {
  key: string
  label: string
  icon: IconName
  hash: string
}

export interface NavGroup {
  title?: string
  items: NavItem[]
}

export function sideNav(groups: NavGroup[], activeKey: string): HTMLElement {
  const nav = el('nav', { class: 'flex flex-col gap-0.5' })
  for (const group of groups) {
    if (group.title) {
      const title = el('div', { class: 'text-[10px] uppercase tracking-wider px-3 pt-3 pb-1 font-semibold' }, [group.title])
      title.style.color = 'rgb(var(--c-ink-subtle))'
      nav.appendChild(title)
    }
    for (const item of group.items) {
      const isActive = item.key === activeKey
      const btn = el('button', { class: 'flex items-center gap-3 px-3 py-2 rounded-lg text-sm transition-colors text-left w-full' })
      if (isActive) {
        btn.style.background = 'rgb(var(--c-primary-soft))'
        btn.style.color = 'rgb(var(--c-primary-soft-text))'
        btn.style.fontWeight = '600'
      } else {
        btn.style.color = 'rgb(var(--c-ink-muted))'
        btn.onmouseenter = () => { btn.style.background = 'rgb(var(--c-element))' }
        btn.onmouseleave = () => { btn.style.background = 'transparent' }
      }
      btn.appendChild(svgIcon(item.icon, 16))
      btn.appendChild(document.createTextNode(item.label))
      btn.onclick = () => { location.hash = item.hash }
      nav.appendChild(btn)
    }
  }
  return nav
}

/** 未保存修改三选项弹窗（修订版 2.3 页面未保存检测）：继续编辑 / 保存并关闭 / 不保存关闭 */
export function unsavedChoice(): Promise<'stay' | 'save' | 'discard'> {
  return new Promise(resolve => {
    const overlay = el('div', { class: 'fixed inset-0 z-[80] flex items-center justify-center p-4' })
    overlay.style.background = 'rgb(var(--c-overlay) / 0.6)'
    const box = el('div', { class: 'card w-96 max-w-full p-5 flex flex-col gap-3' })
    const title = el('div', { class: 'font-semibold text-sm' }, ['未保存的修改'])
    title.style.color = 'rgb(var(--c-ink))'
    const msg = el('div', { class: 'text-xs' }, ['当前表单存在未保存的修改，是否保存？'])
    msg.style.color = 'rgb(var(--c-ink-muted))'
    const btns = el('div', { class: 'flex justify-end gap-2 mt-1' })
    const stay = el('button', { class: 'btn btn-sm' }, ['继续编辑'])
    const save = el('button', { class: 'btn btn-sm btn-primary' }, ['保存并关闭'])
    const discard = el('button', { class: 'btn btn-sm btn-danger' }, ['不保存关闭'])
    const done = (v: 'stay' | 'save' | 'discard') => { overlay.remove(); resolve(v) }
    stay.onclick = () => done('stay')
    save.onclick = () => done('save')
    discard.onclick = () => done('discard')
    btns.append(stay, save, discard)
    box.append(title, msg, btns)
    overlay.appendChild(box)
    overlay.onclick = (e) => { if (e.target === overlay) done('stay') }
    document.body.appendChild(overlay)
  })
}

/** 卡片头（标题 + 徽标 + 动作区） */
export function cardHeader(
  title: string,
  opts: { badges?: HTMLElement[]; actions?: HTMLElement[]; icon?: IconName } = {}
): HTMLElement {
  const header = el('div', { class: 'flex items-center justify-between mb-3 gap-2' })
  const left = el('div', { class: 'flex items-center gap-2 flex-wrap' })
  if (opts.icon) {
    const ic = svgIcon(opts.icon, 16)
    ic.style.color = 'rgb(var(--c-primary))'
    left.appendChild(ic)
  }
  const titleEl = el('h3', { class: 'text-sm font-semibold' }, [title])
  titleEl.style.color = 'rgb(var(--c-ink))'
  left.appendChild(titleEl)
  if (opts.badges) left.append(...opts.badges)
  header.appendChild(left)
  if (opts.actions && opts.actions.length > 0) {
    const right = el('div', { class: 'flex items-center gap-2 shrink-0' })
    right.append(...opts.actions)
    header.appendChild(right)
  }
  return header
}
