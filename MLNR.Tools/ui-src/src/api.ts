import type {
  FanHWState, SwitchHWState, DiskGroupView, Settings, AppInfo, DeviceInfo,
  FanMode, FanConfigKey, SwitchConfigKey, GlobalConfigKey, CommandResult, SensorData,
  ThermalSnapshot, HistoryResponse, LogEntry, HistoryFrom,
  SensorChannelConfig, SensorChannelReading,
  SystemDisk, DiskPartition,
  DiskLogQueryParams, DiskLogQueryResult,
  Schedule, ScheduleLogQueryParams, ScheduleLogQueryResult,
  MonitorScript, ExecScript, LogEventResource,
} from './types'

const BASE = '/app/mlnr/api'

// F2: 请求超时——BLE 操作可能持续数秒，但网络层不应无限挂起；
//     超时后中止 fetch 并抛出明确错误，UI 不再因设备无响应而永久卡死。
const REQUEST_TIMEOUT_MS = 30000

async function request<T>(path: string, opts?: RequestInit): Promise<T> {
  const ctrl = new AbortController()
  const timer = window.setTimeout(() => ctrl.abort(), REQUEST_TIMEOUT_MS)
  // 若调用方已传 signal，联动中止（任一中止即中止本次请求）
  if (opts?.signal) {
    if (opts.signal.aborted) ctrl.abort()
    else opts.signal.addEventListener('abort', () => ctrl.abort())
  }
  let resp: Response
  try {
    resp = await fetch(BASE + path, {
      headers: { 'Content-Type': 'application/json' },
      ...opts,
      signal: ctrl.signal,
    })
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') {
      throw new Error(`请求超时（${REQUEST_TIMEOUT_MS / 1000}s），设备可能未响应，请重试`)
    }
    throw err
  } finally {
    clearTimeout(timer)
  }
  if (!resp.ok) {
    let msg = resp.statusText
    try {
      const body = await resp.json()
      msg = body.error || msg
    } catch {
      /* ignore */
    }
    throw new Error(msg)
  }
  return resp.json() as Promise<T>
}

export interface HistoryQuery {
  series: string
  from?: HistoryFrom
  startMs?: number
  endMs?: number
  customStart?: string // yyyy-mm-ddThh:mm
  customEnd?: string   // yyyy-mm-ddThh:mm
}

function buildHistoryQuery(q: HistoryQuery): string {
  const params = new URLSearchParams()
  params.set('series', q.series)
  if (q.from) params.set('from', q.from)
  // #Fix：拖动时 viewMin 是浮点毫秒戳（panDownTMin - dx*msPerPx，dx 含小数），
  // 直接 String() 会带小数点（如 "1758096400000.234"），后端 ParseInt 解析失败 → startMs=0
  // → 全量降采样成散点。这里统一取整为整数毫秒戳。
  if (q.startMs !== undefined && Number.isFinite(q.startMs)) params.set('startMs', String(Math.trunc(q.startMs)))
  if (q.endMs !== undefined && Number.isFinite(q.endMs)) params.set('endMs', String(Math.trunc(q.endMs)))
  if (q.customStart) params.set('customStart', q.customStart)
  if (q.customEnd) params.set('customEnd', q.customEnd)
  const s = params.toString()
  return s ? `?${s}` : ''
}

// /api/status 返回结构
export interface StatusResponse {
  device: DeviceInfo
  fans: FanHWState[]
  switches: SwitchHWState[]
  diskGroups?: DiskGroupView[]
  sensor?: SensorData | null
  sensorChannels?: (SensorChannelReading | null)[]
  settings: Settings
}

// /api/sensor/channels 返回结构
export interface SensorChannelsResponse {
  channels: SensorChannelConfig[]
  readings: (SensorChannelReading | null)[]
}

// /api/connect 返回结构
export interface ConnectResponse {
  ok: boolean
  device: DeviceInfo
  fans: FanHWState[]
}

// /api/scan 返回结构（FS.md 上位机#8：扫描全部设备清单）
export interface ScanResponse {
  devices: DeviceInfo[]
}

// /api/refresh 返回结构
export interface RefreshResponse {
  ok: boolean
  fans: FanHWState[]
  switches: SwitchHWState[]
}

// 硬件配置下发通用返回
export interface ConfigResponse {
  ok: boolean
  result: CommandResult
}

export const api = {
  info: () => request<AppInfo>('/info'),
  getStatus: () => request<StatusResponse>('/status'),

  // 连接管理
  connect: (address?: string, name?: string) =>
    request<ConnectResponse>('/connect', {
      method: 'POST',
      body: JSON.stringify(address ? { address, name: name || '' } : {}),
    }),
  disconnect: () => request<{ ok: boolean }>('/disconnect', { method: 'POST' }),
  scan: () => request<ScanResponse>('/scan', { method: 'POST' }),
  scanStart: () => request<{ ok: boolean }>('/scan/start', { method: 'POST' }),
  scanStop: () => request<{ ok: boolean }>('/scan/stop', { method: 'POST' }),
  handshake: () => request<{ ok: boolean; device: DeviceInfo }>('/handshake', { method: 'POST' }),
  refresh: () => request<RefreshResponse>('/refresh', { method: 'POST' }),

  // 风扇控制
  setFanSpeed: (fanId: number, speed: number) =>
    request<ConfigResponse>('/fan/speed', { method: 'POST', body: JSON.stringify({ fanId, speed }) }),
  setFanMode: (fanId: number, mode: FanMode) =>
    request<{ ok: boolean; mode: FanMode }>('/fan/mode', { method: 'POST', body: JSON.stringify({ fanId, mode }) }),

  // 开关控制
  setSwitch: (swId: number, state: number) =>
    request<ConfigResponse>('/switch', { method: 'POST', body: JSON.stringify({ swId, state }) }),

  // 硬盘组管理
  diskPowerOn: (groupId: number) =>
    request<ConfigResponse>(`/disk/group/${groupId}/poweron`, { method: 'POST' }),
  diskPowerOff: (groupId: number, action?: string) =>
    request<ConfigResponse>(`/disk/group/${groupId}/poweroff`, {
      method: 'POST',
      body: JSON.stringify(action ? { action } : {}),
    }),
  diskForcePowerOff: (groupId: number) =>
    request<ConfigResponse>(`/disk/group/${groupId}/forceoff`, { method: 'POST' }),
  diskMount: (groupId: number, device: string) =>
    request<ConfigResponse>(`/disk/group/${groupId}/mount`, { method: 'POST', body: JSON.stringify({ device }) }),
  diskUnmount: (groupId: number, device: string, action?: string) =>
    request<ConfigResponse>(`/disk/group/${groupId}/unmount`, {
      method: 'POST',
      body: JSON.stringify(action ? { device, action } : { device }),
    }),

  // 硬件配置下发
  setFanConfig: (fanId: number, key: FanConfigKey, value: number) =>
    request<ConfigResponse>('/fan/config', { method: 'POST', body: JSON.stringify({ fanId, key, value }) }),
  setSwitchConfig: (swId: number, key: SwitchConfigKey, value: number) =>
    request<ConfigResponse>('/switch/config', { method: 'POST', body: JSON.stringify({ swId, key, value }) }),
  setGlobalConfig: (key: GlobalConfigKey, value: number) =>
    request<ConfigResponse>('/global/config', { method: 'POST', body: JSON.stringify({ key, value }) }),
  setSensorEnabled: (enabled: boolean) =>
    request<ConfigResponse>('/sensor/enabled', { method: 'POST', body: JSON.stringify({ enabled }) }),
  setSensorInterval: (intervalSec: number) =>
    request<ConfigResponse>('/sensor/interval', { method: 'POST', body: JSON.stringify({ intervalSec }) }),
  getSensorChannels: () => request<SensorChannelsResponse>('/sensor/channels'),
  updateSensorChannel: (id: number, patch: Partial<SensorChannelConfig>) =>
    request<ConfigResponse>(`/sensor/channels/${id}`, { method: 'POST', body: JSON.stringify(patch) }),
  saveHardwareConfig: () => request<ConfigResponse>('/hw/save', { method: 'POST' }),
  factoryReset: () => request<ConfigResponse>('/hw/reset', { method: 'POST' }),

  // 上位机设置
  getSettings: () => request<{ settings: Settings }>('/settings'),
  saveSettings: (s: Settings) =>
    request<{ settings: Settings }>('/settings', { method: 'PUT', body: JSON.stringify(s) }),

  // 温度与历史
  getTemperature: () => request<ThermalSnapshot>('/temperature'),
  getHistory: (q: HistoryQuery) => request<HistoryResponse>(`/history${buildHistoryQuery(q)}`),

  // 日志
  getLogs: (count = 200, level?: string, history = true) =>
    request<{ logs: LogEntry[]; count: number }>(
      `/logs?count=${count}${level ? `&level=${level}` : ''}&history=${history ? 1 : 0}`),

  // 日志配置
  getLogConfig: () => request<{ level: string; maxSizeMB: number; keepDays: number }>('/logconfig'),
  saveLogConfig: (level: string, maxSizeMB: number) =>
    request<{ level: string; maxSizeMB: number; keepDays: number }>('/logconfig', {
      method: 'PUT', body: JSON.stringify({ level, maxSizeMB }),
    }),

  // 系统硬盘列表（GET /api/disks/list；refresh=true 时后端强制重新收集）
  getDisksList: (refresh = false) =>
    request<{ disks: SystemDisk[] }>(`/disks/list${refresh ? '?refresh=1' : ''}`),

  // 硬盘分区列表（GET /api/disks/partitions?device=/dev/sda）
  getDiskPartitions: (device: string) =>
    request<{ partitions: DiskPartition[] }>(`/disks/partitions?device=${encodeURIComponent(device)}`),

  // BLE 广播名
  getBleName: () => request<{ name: string }>('/ble/name'),
  setBleName: (name: string) =>
    request<{ ok: boolean }>('/ble/name', { method: 'PUT', body: JSON.stringify({ name }) }),

  // BLE 调试模式
  getBleDebugMode: () => request<{ enabled: boolean }>('/ble/debug-mode'),
  setBleDebugMode: (enabled: boolean) =>
    request<{ ok: boolean }>('/ble/debug-mode', { method: 'PUT', body: JSON.stringify({ enabled }) }),

  // 全局配置（GET /api/global/config）
  getGlobalConfig: () =>
    request<{ hb_timeout: number; debug_mode: boolean; ble_name: string }>('/global/config'),

  // fnOS 系统主题（需求 #6：上位机"自动"主题数据源）
  getSystemTheme: () => request<{ theme: 'light' | 'dark'; source: string }>('/system/theme'),

  // 硬盘组操作日志
  getDiskLogs: (p: DiskLogQueryParams) => {
    const qs = new URLSearchParams()
    if (p.groupId !== undefined && p.groupId >= 0) qs.set('groupId', String(p.groupId))
    if (p.action) qs.set('action', p.action)
    if (p.from) qs.set('from', p.from)
    if (p.to) qs.set('to', p.to)
    qs.set('page', String(p.page ?? 1))
    qs.set('pageSize', String(p.pageSize ?? 20))
    const q = qs.toString()
    return request<DiskLogQueryResult>(`/disk/logs${q ? '?' + q : ''}`)
  },
  // 定时计划（M1~M4）
  getSchedules: () => request<{ schedules: Schedule[] }>('/schedules'),
  createSchedule: (s: Omit<Partial<Schedule>, 'id'>) =>
    request<{ schedule: Schedule }>('/schedules', { method: 'POST', body: JSON.stringify(s) }),
  updateSchedule: (id: number, s: Omit<Partial<Schedule>, 'id'>) =>
    request<{ schedule: Schedule }>('/schedules/' + id, { method: 'PUT', body: JSON.stringify(s) }),
  deleteSchedule: (id: number) =>
    request<{ ok: boolean }>('/schedules/' + id, { method: 'DELETE' }),
  // M12：手动触发任务（全部执行器执行，不消费触发器）
  runSchedule: (id: number) =>
    request<{ ok: boolean }>('/schedules/' + id + '/run', { method: 'POST' }),

  // 定时计划资源（M13：监控脚本 / 执行脚本 / 日志事件）
  getMonitorScripts: () => request<{ monitorScripts: MonitorScript[] }>('/monitor-scripts'),
  createMonitorScript: (s: Omit<MonitorScript, 'id'>) =>
    request<{ monitorScript: MonitorScript }>('/monitor-scripts', { method: 'POST', body: JSON.stringify(s) }),
  updateMonitorScript: (id: number, s: Omit<MonitorScript, 'id'>) =>
    request<{ monitorScript: MonitorScript }>('/monitor-scripts/' + id, { method: 'PUT', body: JSON.stringify(s) }),
  deleteMonitorScript: (id: number, force = false) =>
    request<{ ok: boolean; clearedReferences?: number }>('/monitor-scripts/' + id + (force ? '?force=1' : ''), { method: 'DELETE' }),
  testMonitorScript: (code: string, type?: string, args?: string) =>
    request<{ output?: string; error?: string }>('/monitor-scripts/test', { method: 'POST', body: JSON.stringify({ code, type, args }) }),
  getExecScripts: () => request<{ execScripts: ExecScript[] }>('/exec-scripts'),
  createExecScript: (s: Omit<ExecScript, 'id'>) =>
    request<{ execScript: ExecScript }>('/exec-scripts', { method: 'POST', body: JSON.stringify(s) }),
  updateExecScript: (id: number, s: Omit<ExecScript, 'id'>) =>
    request<{ execScript: ExecScript }>('/exec-scripts/' + id, { method: 'PUT', body: JSON.stringify(s) }),
  deleteExecScript: (id: number, force = false) =>
    request<{ ok: boolean; clearedReferences?: number }>('/exec-scripts/' + id + (force ? '?force=1' : ''), { method: 'DELETE' }),
  testExecScript: (code: string, type?: string, args?: string) =>
    request<{ output?: string; error?: string }>('/exec-scripts/test', { method: 'POST', body: JSON.stringify({ code, type, args }) }),
  getLogEvents: () => request<{ logEvents: LogEventResource[] }>('/log-events'),
  createLogEvent: (s: Omit<LogEventResource, 'id'>) =>
    request<{ logEvent: LogEventResource }>('/log-events', { method: 'POST', body: JSON.stringify(s) }),
  updateLogEvent: (id: number, s: Omit<LogEventResource, 'id'>) =>
    request<{ logEvent: LogEventResource }>('/log-events/' + id, { method: 'PUT', body: JSON.stringify(s) }),
  deleteLogEvent: (id: number, force = false) =>
    request<{ ok: boolean; clearedReferences?: number }>('/log-events/' + id + (force ? '?force=1' : ''), { method: 'DELETE' }),
  testLogEvent: (path: string, regex: string) =>
    request<{ matches?: string[]; error?: string }>('/log-events/test', { method: 'POST', body: JSON.stringify({ path, regex }) }),

  getScheduleLogs: (p: ScheduleLogQueryParams) => {
    const qs = new URLSearchParams()
    if (p.name) qs.set('name', p.name)
    if (p.category) qs.set('category', p.category)
    if (p.type) qs.set('type', p.type)
    if (p.triggerType) qs.set('triggerType', p.triggerType)
    if (p.result) qs.set('result', p.result)
    if (p.from) qs.set('from', p.from)
    if (p.to) qs.set('to', p.to)
    qs.set('page', String(p.page ?? 1))
    qs.set('pageSize', String(p.pageSize ?? 20))
    const q = qs.toString()
    return request<ScheduleLogQueryResult>('/schedule-logs' + (q ? '?' + q : ''))
  },
  updateScheduleLogConfig: (retainDays: number) =>
    request<{ retainDays: number }>('/schedule-log-config', {
      method: 'PUT', body: JSON.stringify({ retainDays }),
    }),
}