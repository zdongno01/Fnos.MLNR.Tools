// 与后端 models.go 对齐的数据类型（NR_F2S4 蓝牙控制板）
import { BOTTOM_NAV_DEFAULT_KEYS } from './bottom-nav-config'

// ===== 硬件通道上限 =====
export const MAX_FAN_CHANNELS = 2 // FAN1 / FAN2
export const MAX_SWITCH_CHANNELS = 4 // SW1 ~ SW4
export const MAX_DISK_GROUPS = 4 // 硬盘组复用 SW1~SW4

// ===== 连接状态 =====

// BLE 物理连接状态
export type ConnectionState =
  | 'disconnected' // 未连接
  | 'scanning'     // 扫描中
  | 'connecting'   // 连接中
  | 'connected'    // 已连接
  | 'reconnecting' // 重连中

// 设备运行状态（FS4 安全模型，来自握手与 GET/事件解析）
export type DeviceRunState = '' | 'UNINIT' | 'WORK_WAIT' | 'WORK_RUN'

// 会话加密类型
export type SessionType = 'plain' | 'encrypted'

// ===== 运行模式 =====
export type FanMode = 'auto' | 'manual'
export const ModeAuto: FanMode = 'auto'
export const ModeManual: FanMode = 'manual'

// ===== 硬件实时状态（解析自 GET FANn / GET SWITCHn） =====

// 单路风扇硬件状态
export interface FanHWState {
  index: number      // 1=FAN1(GPIO21), 2=FAN2(GPIO20)
  pin: string        // PWM 输出引脚
  enabled: boolean   // 硬件启用状态
  pwmFreq: number    // 当前 PWM 载波频率（Hz）
  powerSpd: number   // 上电默认转速配置值（%）
  hbFallback: number // 心跳失联后备转速（%）
  targetSpd: number  // 硬件侧目标转速（下发指令目标值）
  curSpd: number     // 当前实际输出转速（平滑逼近值）
  heartbeat: string  // 心跳状态：OK / TIMEOUT
  updatedAt: string  // 状态更新时间
}

// 单路开关硬件状态
export interface SwitchHWState {
  index: number         // 1~4 = SW1~SW4
  pin: string           // GPIO 引脚
  enabled: boolean      // 硬件启用状态
  powerOnState: number // 开机默认电平 0/1
  powerOnDelay: number // 开机延迟（毫秒）
  state: number         // 当前电平 0=低(关) / 1=高(开)
  system: boolean       // 系统关键磁盘属性（CFG SW SYSTEM）
  updatedAt: string     // 状态更新时间
}

// ===== 设备连接信息 =====
export interface DeviceInfo {
  name: string               // 设备广播名 "NR_F2S4"
  address: string            // BLE MAC 地址
  rssi: number               // 信号强度（dBm，仅扫描发现时有效）
  signalPercent: number      // 扫描信号强度百分比
  signalQuality?: number     // 固件推算信号质量（0~100，-1=未知；PING 应答携带，替代连接后 RSSI）
  connectionState: ConnectionState // 物理连接状态
  sessionType: SessionType   // 会话类型：明文/加密
  runState: DeviceRunState   // 设备运行状态 UNINIT/WORK_WAIT/WORK_RUN
  version: string            // GET VERSION 结果
  connectedAt: string        // 连接建立时间
}

// ===== fnOS 本地配置（JSON 持久化，不上传硬件） =====

// 风扇通道本地配置
export interface FanChannelConfig {
  id: number                // 1/2
  alias: string             // 风扇别名（仅本地）
  enabled: boolean          // 通道启用（本地概念，下发 CFG ENABLED）
  tempRef?: string          // [向后兼容] 旧单温度参考点，迁移后使用 tempRefs
  tempRefs?: string[]       // 温度参考点传感器 ID 列表（fnOS 取列表中温度最高者调速）
  mode: FanMode             // auto/manual
  targetSpd: number         // 手动模式需求转速（下发目标）
  speedCurve: CurvePoint[]  // [向后兼容] 旧单曲线字段，前端 UI 不再直接读写
  curves?: Record<string, CurvePoint[]> // 3 曲线：efficient(高效) / daily(日常) / quiet(静音)
  activeCurve?: string      // 当前生效曲线 key，默认 "daily"
  faultSpd?: number         // 故障转速：TempRefs 全部离线时触发（%，0 表示关闭）
  diskOfflineSpd?: number   // 硬盘离线转速：参考点全为硬盘且全部绑定硬盘正常下线/从未上线时触发（%，0 表示关闭）
}

// 单组挂载项（分区 + 挂载点）
export interface DiskMount {
  partition: string   // 分区路径，如 /dev/sda1
  mountPoint: string  // 挂载点，如 /vol1/1000/archive
}

// 硬盘条目
export interface DiskEntry {
  device: string       // 块设备路径 /dev/sda
  alias: string        // 硬盘别名
  serial: string       // 硬盘序列号（绑定主体，比较同盘用 serial 而非 device path）
  mounts: DiskMount[]  // 多组挂载（分区→挂载点）
  autoMount: boolean   // 上线后自动挂载
  isNvme: boolean      // NVMe SSD（跳过停转指令）
}

// 硬盘组任务配置（上电后 / 下电前）：引用计划任务，触发执行任务的全部执行器。
// 旧脚本配置（onScript/offScript）已移除，脚本能力由任务内的"执行脚本"执行器承担。
export interface DiskGroupTask {
  taskId?: number // 计划任务引用
}

// 硬盘组本地配置（1 组绑定 1 路硬件开关）
export interface DiskGroupConfig {
  id: number          // 1~4
  alias: string       // 硬盘组别名
  switchN: number     // 绑定硬件开关编号 SW1~SW4
  enabled: boolean     // 硬盘组启用
  autoOnline: boolean  // 程序启动后延时自动上线
  disks: DiskEntry[]   // 硬盘列表
  // 上电后等待时间（秒）：SW ON 后等待该时长再检查硬盘就绪；0=默认 8s
  powerOnDelaySec?: number
  // 下线等待时间（秒）：卸载（umount）后等待该时长再继续断电流程
  // （安全下线 umount 成功后等待，强制下线 umount -f 发送后等待）；0=默认 2s
  forceOffDelaySec?: number
  // 上电后任务：硬盘组上电（SW ON 上线流程完成）后异步触发执行；触发失败仅记日志，不阻断上线
  onTask?: DiskGroupTask
  // 下电前任务：硬盘组下电流程开始前同步触发执行；必须执行成功才继续正常下电，
  // 执行失败/任务丢失终止正常下电（强制下电不受限，跳过下电前任务）
  offTask?: DiskGroupTask
}

// 温度传感器显示配置
export interface TempSensorConfig {
  id: string    // 传感器唯一 ID（与 TemperatureReading.id 一致）
  alias: string // 用户自定义别名（空则使用原始名称）
  show: boolean // 是否在主页显示
  icon: string  // 图标名（temp/cpu/gpu/memory/mb/expansion/ssd/hdd/chassis/psu）
  color: string // 图标颜色（CSS 颜色字符串）
}

// ===== I2C 传感器实时数据（多通道，FS5-B5） =====

// 传感器类型（与固件/服务端枚举一致）
export type SensorKind = 0 | 1 | 2 | 3 | 4
export const SensorKindNone = 0
export const SensorKindAHT20 = 1
export const SensorKindBMP280 = 2
export const SensorKindLM75 = 3
export const SensorKindHTU21D = 4

export const SENSOR_KIND_LABELS: Array<{ kind: SensorKind; label: string; addr: number; desc: string }> = [
  { kind: SensorKindAHT20, label: 'AHT20', addr: 0x38, desc: '温湿度' },
  { kind: SensorKindBMP280, label: 'BMP280', addr: 0x77, desc: '温度/气压/海拔' },
  { kind: SensorKindLM75, label: 'LM75', addr: 0x48, desc: '温度' },
  { kind: SensorKindHTU21D, label: 'HTU21D', addr: 0x40, desc: '温湿度' },
]

export function sensorKindLabel(k: SensorKind): string {
  const hit = SENSOR_KIND_LABELS.find(x => x.kind === k)
  return hit ? hit.label : '未配置'
}

// 单通道配置（本地 JSON + 下发硬件）
export interface SensorChannelConfig {
  id: number        // 0~3
  alias: string     // 别名（仅本地显示）
  kind: SensorKind  // 传感器类型
  addr: number      // I2C 地址（十进制）
  enabled: boolean  // 是否启用采集
  intervalSec: number // 采集间隔（秒）
  // FS.md 上位机#6：按内容分别控制是否在监控总览页（主页）显示
  showTemp: boolean  // 温度
  showHumi: boolean  // 湿度
  showPress: boolean // 压力
  showAlt: boolean   // 海拔
}

// 单通道实时读数
export interface SensorChannelReading {
  chId: number
  kind: SensorKind
  state: string      // OK / FAIL
  temperature: number
  humidity: number
  pressure: number
  altitude: number
  updatedAt: string
}

export function defaultSensorChannels(): SensorChannelConfig[] {
  return SENSOR_KIND_LABELS.map((x, i) => ({
    id: i,
    alias: x.label,
    kind: x.kind,
    addr: x.addr,
    enabled: false,
    intervalSec: 2,
    showTemp: false,
    showHumi: false,
    showPress: false,
    showAlt: false,
  }))
}

// ===== 单通道兼容视图（旧字段，服务端仍返回首通道） =====
export interface SensorData {
  temperature: number // 环境温度 ℃
  humidity: number    // 湿度 %
  pressure: number    // 压力 Pa
  altitude: number    // 海拔 m
  updatedAt: string   // 收到推送的时间
}

// 主题模式（light/dark/auto 之外新增两种整体色调主题：护眼暖/午夜蓝）
export type ThemeMode = 'light' | 'dark' | 'auto' | 'sepia' | 'midnight'

// fnOS 本地运行设置
export interface Settings {
  deviceName: string             // BLE 扫描过滤广播名
  autoConnect: boolean           // 启动时自动连接上次设备
  lastAddress: string            // 上次成功连接的 BLE MAC
  bleKey: number[]               // 握手协商的 AES-128 密钥
  hbTimeoutSec: number           // FS.md 上位机#7：全局心跳超时阈值（秒，3~60）
  sensorEnabled: boolean           // 兼容旧字段：总采集开关
  sensorIntervalSec: number        // 兼容旧字段：全局采集间隔（秒，1~30）
  sensorChannels: SensorChannelConfig[] // 多通道 I2C 传感器配置（≤4）
  fans: FanChannelConfig[]         // 风扇通道配置（≤2）
  diskGroups: DiskGroupConfig[]  // 硬盘组配置（≤4）
  tempSensors: TempSensorConfig[] // 温度传感器显示配置
  theme: ThemeMode              // 主题：亮/暗/自动
  directSaveNVS: boolean        // 直接写入 NVS：开启后每次 CFG 下发成功自动跟发 SAVE(0x0A)；关闭时 CFG 仅写硬件内存，由顶栏提示/设置页手动落盘
  homeLayout: HomeLayoutItem[]  // 监控总览页可编辑布局（服务器端保存，所有访问终端统一显示）
  diskStatsEnabled: string[]    // 硬盘组统计启用的组件 id（空 = 全部显示；服务端保存，所有终端统一）
  bottomNavKeys: string[]       // 移动端底部导航栏显示的导航项 key（有序；空数组 = 隐藏底部导航；未配置时回退默认 总览/风扇/硬盘/设置）
}

/** 监控总览布局项（id + 历史 size 字段；宽度现由模块内启用组件数量自动分配） */
export interface HomeLayoutItem {
  id: string
  size: string
}

// ===== 温控-转速曲线 =====
export interface CurvePoint {
  temp: number // 温度，℃
  spd: number  // 转速，%
}

export function defaultSpeedCurve(): CurvePoint[] {
  return [
    { temp: 30, spd: 30 },
    { temp: 45, spd: 40 },
    { temp: 60, spd: 65 },
    { temp: 75, spd: 85 },
    { temp: 90, spd: 100 },
  ]
}

export function defaultSettings(): Settings {
  const fans: FanChannelConfig[] = []
  for (let i = 1; i <= MAX_FAN_CHANNELS; i++) {
    fans.push({
      id: i,
      alias: `风扇 ${i}`,
      enabled: true,
      tempRef: '',
      mode: ModeAuto,
      targetSpd: 30,
      speedCurve: defaultSpeedCurve(),
      curves: {
        efficient: defaultSpeedCurve(),
        daily: defaultSpeedCurve(),
        quiet: defaultSpeedCurve(),
      },
      activeCurve: 'daily',
      faultSpd: 50,
      diskOfflineSpd: 0,
    })
  }
  const diskGroups: DiskGroupConfig[] = []
  for (let i = 1; i <= MAX_DISK_GROUPS; i++) {
    diskGroups.push({
      id: i,
      alias: `硬盘组 ${i}`,
      switchN: i,
      enabled: false,
      autoOnline: false,
      disks: [],
    })
  }
  return {
    deviceName: 'NR_F2S4',
    autoConnect: true,
    lastAddress: '',
    bleKey: [],
    hbTimeoutSec: 10,
    sensorEnabled: false,
    sensorIntervalSec: 2,
    sensorChannels: defaultSensorChannels(),
    fans,
    diskGroups,
    tempSensors: [],
    theme: 'auto',
    directSaveNVS: false,
    homeLayout: [],
    diskStatsEnabled: [],
    bottomNavKeys: BOTTOM_NAV_DEFAULT_KEYS.slice(),
  }
}

// ===== 硬盘组运行视图 =====
export interface DiskView {
  device: string
  alias: string
  serial: string            // 硬盘序列号（3.md：SN 优先标识）
  mountPath: string
  autoMount: boolean
  isNvme: boolean
  mounted: boolean
  temperature?: number | null // 按 SN 关联的实时温度（nil=无读数）
}

export interface DiskGroupStats {
  groupId: number
  alias?: string
  onlineMinutes: number
  online: boolean
  switchCount7d: number
  switchCount30d: number
  totalSwitchCount: number  // 累计开关次数（min(总 on, 总 off)）
  totalOnlineMinutes: number // 总在线时长（分钟，含本次会话）
  avgOnlineMinutes: number   // 平均单次在线时长（分钟）
  forceOffCount: number      // 强制下线累计次数
}

export interface DiskGroupView {
  id: number
  alias: string
  switchN: number
  enabled: boolean
  autoOnline: boolean
  online: boolean
  conflict: boolean
  disks: DiskView[]
  stats?: DiskGroupStats
}

// ===== 硬盘组操作日志 =====
export interface DiskLogEntry {
  time: string         // RFC3339
  groupId: number
  alias: string
  action: string       // power_on/power_off/mount/unmount（已收敛为 4 类）
  content: string
  result: string       // success/failed
  remark: string       // 备注（自动执行/强制下线/离线事件/按钮打开/按钮关闭 + 错误信息）
}

export interface DiskLogQueryParams {
  groupId?: number
  action?: string
  from?: string        // RFC3339
  to?: string          // RFC3339
  page?: number
  pageSize?: number
}

export interface DiskLogQueryResult {
  total: number
  page: number
  size: number
  items: DiskLogEntry[]
  actionLabels: Record<string, string>
}

// ===== 指令应答 =====
export interface OccupyProcess {
  pid: number
  command: string
  user: string
}

export interface CommandResult {
  ok: boolean
  raw: string
  message: string
  /** 卸载失败且检测到占用进程：'unmount_fail_occupied'（需前端弹窗选择处理方式） */
  decision?: string
  occupied?: OccupyProcess[]
}

// ===== 温度 =====
export interface TemperatureReading {
  id: string
  name: string
  category: 'cpu' | 'mb' | 'hdd' | 'other'
  value: number
  updated: string
  // 3.md：设备级元数据（Device 为分组键，SN 用于磁盘关联）
  device: string   // 所属设备分组键（hwmon 芯片 / 磁盘 SN / thermal zone）
  model: string    // 设备型号（如磁盘型号，可空）
  serial: string   // 设备序列号 SN（磁盘关联 key，可空）
  zone: string     // hwmon 测温点序号（temp1/temp2…），同设备多传感器区分
  crit?: number    // 临界温度（temp*_crit，℃），可空
}

export interface ThermalSnapshot {
  temps: TemperatureReading[]
  time: string
}

// ===== 历史 =====
export interface HistoryPoint {
  t: number // 毫秒时间戳
  v: number // 值
}

export interface HistoryResponse {
  series: string
  start: number
  end: number
  points: HistoryPoint[]
}

// ===== 日志 =====
export interface LogEntry {
  time: string
  level: string
  module: string
  message: string
}

// ===== 系统硬盘列表（GET /api/disks/list） =====
export interface SystemDisk {
  name: string       // 设备名，如 sda
  path: string       // 块设备路径，如 /dev/sda
  model: string      // 硬盘型号
  size: number       // 容量（字节）
  serial: string     // 序列号（空表示读取不到）
  mountpoint: string // 当前挂载点（空表示未挂载）
  bound: boolean     // 是否已被其它硬盘组通道绑定
  isNvme: boolean    // NVMe SSD（3.md：传输类型判定）
}

// ===== 硬盘分区列表（GET /api/disks/partitions） =====
export interface DiskPartition {
  name: string        // 分区名，如 sda1
  path: string        // 分区路径，如 /dev/sda1
  size: number        // 分区容量（字节）
  mountpoints: string[] // 已挂载点列表
}

export type HistoryRange = '1h' | '12h' | '1d' | '1w' | 'custom'
export type HistoryFrom = '1h' | '12h' | '1d' | '1w' | '1m' | 'all'

export const HISTORY_RANGE_OPTIONS: Array<{ id: HistoryRange; label: string; alias?: HistoryFrom; windowMs: number }> = [
  { id: '1h', label: '1 小时', alias: '1h', windowMs: 3_600_000 },
  { id: '12h', label: '12 小时', alias: '12h', windowMs: 43_200_000 },
  { id: '1d', label: '1 天', alias: '1d', windowMs: 86_400_000 },
  { id: '1w', label: '1 周', alias: '1w', windowMs: 604_800_000 },
  { id: 'custom', label: '自定义', windowMs: 0 },
]

// ===== WebSocket 推送消息 =====
export interface StateUpdateMsg {
  type: 'state_update'
  device: DeviceInfo
  fans?: FanHWState[]
  switches?: SwitchHWState[]
  diskGroups?: DiskGroupView[]
  sensor?: SensorData | null // I2C 传感器最新数据（FS5）
  sensorChannels?: SensorChannelReading[] // 多通道 I2C 传感器读数
  setting?: Settings
  time: string
}

export interface LogMsg {
  type: 'log'
  level: string
  module: string
  message: string
  time: string
}

// 应用信息
export interface AppInfo {
  app: string
  version: string
  runtime: string
  serverTime: string
  gatewayUser: { uid: string; username: string; isAdmin: boolean; present: boolean }
}

// ===== 硬件配置键（CFG 指令下发用） =====
export type FanConfigKey =
  | 'ENABLED' | 'POWER_SPD'
  | 'HB_FALLBACK_SPD' | 'PWM_FREQ'

export type SwitchConfigKey =
  | 'ENABLED' | 'POWER_ON_STATE' | 'POWER_ON_DELAY' | 'SYSTEM'

export type GlobalConfigKey = 'HB_TIMEOUT_SEC'

export const FAN_CONFIG_KEYS: Array<{ key: FanConfigKey; label: string; min: number; max: number; unit: string }> = [
  { key: 'POWER_SPD', label: '上电默认转速', min: 0, max: 100, unit: '%' },
  { key: 'HB_FALLBACK_SPD', label: '心跳失联后备转速', min: 0, max: 100, unit: '%' },
  { key: 'PWM_FREQ', label: 'PWM 载波频率', min: 10000, max: 30000, unit: 'Hz' },
]

export const GLOBAL_CONFIG_KEYS: Array<{ key: GlobalConfigKey; label: string; min: number; max: number; unit: string }> = [
  { key: 'HB_TIMEOUT_SEC', label: '全局心跳超时阈值', min: 3, max: 60, unit: '秒' },
]

export const SWITCH_CONFIG_KEYS: Array<{ key: SwitchConfigKey; label: string; min: number; max: number; unit: string }> = [
  { key: 'POWER_ON_STATE', label: '开机默认电平', min: 0, max: 1, unit: '0/1' },
  { key: 'POWER_ON_DELAY', label: '上线开机延迟', min: 0, max: 600000, unit: 'ms' },
]

// ===== 状态标签与颜色 =====
export const STATE_LABEL: Record<ConnectionState, string> = {
  disconnected: '未连接',
  scanning: '扫描中',
  connecting: '连接中',
  connected: '已连接',
  reconnecting: '重连中',
}

export const STATE_COLOR: Record<ConnectionState, string> = {
  disconnected: 'rgb(var(--c-danger))',
  scanning: 'rgb(var(--c-warning))',
  connecting: 'rgb(var(--c-warning))',
  connected: 'rgb(var(--c-success))',
  reconnecting: 'rgb(var(--c-warning))',
}

export const RUN_STATE_LABEL: Record<DeviceRunState, string> = {
  '': '未知',
  'UNINIT': '未初始化',
  'WORK_WAIT': '等待授权',
  'WORK_RUN': '运行中',
}

export const RUN_STATE_COLOR: Record<DeviceRunState, string> = {
  '': 'rgb(var(--c-neutral))',
  'UNINIT': 'rgb(var(--c-danger))',
  'WORK_WAIT': 'rgb(var(--c-warning))',
  'WORK_RUN': 'rgb(var(--c-success))',
}

export const SESSION_LABEL: Record<SessionType, string> = {
  plain: '明文',
  encrypted: '加密',
}

// 信号质量分级（固件推算 0~100，前端只展示分级不展示数值）
// 阈值：≥90 优秀 / ≥75 良好 / ≥50 一般 / ≥25 差 / <25 极差
export function signalQualityLabel(q: number): string {
  if (q >= 90) return '优秀'
  if (q >= 75) return '良好'
  if (q >= 50) return '一般'
  if (q >= 25) return '差'
  return '极差'
}

export function signalQualityColor(q: number): string {
  if (q >= 90) return 'rgb(var(--c-health-good))'
  if (q >= 75) return 'rgb(var(--c-health-fair))'
  if (q >= 50) return 'rgb(var(--c-health-warn))'
  if (q >= 25) return 'rgb(var(--c-health-poor))'
  return 'rgb(var(--c-health-bad))'
}

export const MODE_LABEL: Record<FanMode, string> = {
  auto: '自动模式',
  manual: '手动模式',
}

// ===== 定时计划（M1~M4） =====
export type ScheduleCategory = 'fan' | 'disk'
export type ScheduleType = 'one_time' | 'daily' | 'weekly' | 'monthly' | 'trigger'
export type FanCurveMode = 'efficient' | 'daily' | 'quiet'
export type DiskAction = 'online' | 'offline'
// M8 触发器类型：时间型（one_time/daily/weekly/monthly）直接复用 ScheduleType；
// 旧 sleep/idle 硬盘事件型已由 log/monitor 预制事件替代（旧配置直接抛弃）。
export type ScheduleResult = 'success' | 'failed'

// ===== 定时计划修订版（M9：三类型触发器 + 四类执行器 + 三类资源） =====
export type ScheduleTriggerType = 'time' | 'log' | 'monitor'
export type TriggerPeriod = 'once' | 'daily' | 'weekly' | 'monthly' | 'loop'
export type ScheduleExecutorType = 'fan_control' | 'disk_group_control' | 'control_task' | 'exec_script'
export type TaskAction = 'enable' | 'disable' | 'run'
export type CooldownUnit = 'second' | 'minute' | 'hour'

// 触发器（新格式 time/log/monitor；旧 one_time/daily/weekly/monthly 由后端迁移；
// 旧 sleep/idle 硬盘事件型直接抛弃）
export interface ScheduleTrigger {
  type: string
  // ---- 定时事件（type=time / 旧 one_time/daily/weekly/monthly） ----
  period?: TriggerPeriod
  runAt?: string // 一次执行时刻（ISO）
  time?: string // "HH:mm"
  weekdays?: number[] // weekly 1=周一 ~ 7=周日
  monthDays?: number[] // monthly 1~31
  loopInterval?: string // "HH:MM:SS"（period=loop，循环执行间隔）
  // ---- 日志事件（type=log） ----
  // 配置来源：引用全局日志事件资源（logEventId>0，资源页 CRUD），或内联字段
  // （日志读取 logEventPath + logEventRegex；读取后的结果处理：冷却/连续匹配/轮转）。
  logEventId?: number // 全局日志事件资源引用（>0）；0=内联配置
  logEventPath?: string
  logEventRegex?: string
  logEventCooldown?: number
  logEventCooldownUnit?: CooldownUnit
  logEventConsecutive?: number
  logEventRotate?: boolean
  // ---- 监控事件（type=monitor） ----
  monitorPrebuilt?: string // "idle"=监控硬盘AB闲置（预制只读）
  monitorScriptId?: number // 自定义监控脚本引用
  monitorCode?: string // 内联监控脚本代码（不创建全局资源）
  monitorScriptType?: string // 内联监控脚本类型 shell / python；空=按 shebang 自动识别
  monitorScriptArgs?: string // 脚本命令行参数（sh 用 $1 $2 …，python 用 sys.argv[1:] 引用）
  monitorDurationMin?: number // [已废弃] 监控持续时间，保留供存量兼容
  monitorIntervalSec?: number // 监控执行间隔 1~600 秒
  // ---- 事件触发器生效规则（log/monitor 共用；旧 sleep/idle 直接抛弃） ----
  diskGroups?: number[]
  allDay?: boolean
  timeRange?: string // "08:00-20:00"（allDay=true 时忽略）
  weekLimit?: number[] // 1~7（与 monthDayLimit 互斥）
  monthDayLimit?: number[] // 1~31（与 weekLimit 互斥）
  thresholdMin?: number // 生效阈值（分钟，1~60）
}

// 执行器（M9 修订：原动作改名执行器）
export interface ScheduleExecutor {
  type: ScheduleExecutorType
  delaySec?: number // 独立延迟 1~3600，0=立即
  // ---- 风扇控制（fan_control） ----
  fanId?: number
  fanEnabled?: boolean | null // null=不改变
  fanManual?: boolean // true=手动 / false=自动
  fanPercent?: number // 手动转速 1~100
  fanCurve?: FanCurveMode // 自动曲线（空=不切换，曲线维持不变）
  // ---- 硬盘组控制（disk_group_control） ----
  diskGroupId?: number
  diskAction?: DiskAction
  killOccupied?: boolean // 自动终止占用（下线时）
  forceOff?: boolean // 强制下线（下线时）
  retryEnabled?: boolean // 失败重复执行开关
  maxRetries?: number // 最大重试次数 1~10（默认 3）
  retryIntervalSec?: number // 重试间隔 1~3600（默认 10）
  // ---- 控制任务（control_task） ----
  taskId?: number
  taskAction?: TaskAction
  respectEnabled?: boolean // 遵循目标任务启用状态（默认关闭）
  respectTimeRule?: boolean // 遵循目标任务日期规则（默认关闭）
  // ---- 执行脚本（exec_script） ----
  scriptPrebuilt?: string // 预制脚本标识（V1 暂无）
  scriptId?: number // 自定义执行脚本引用
  scriptCode?: string // 自定义内联脚本（sh / python）
  scriptType?: string // 内联脚本类型 "shell" / "python"；空=按 shebang 自动识别（仅内联代码生效）
  scriptArgs?: string // 脚本命令行参数（sh 用 $1 $2 …，python 用 sys.argv[1:] 引用）
  // ---- 通用串联：成功/失败后执行任务 ----
  successTaskId?: number
  failureTaskId?: number
}

// 监控脚本资源
export interface MonitorScript {
  id: number
  name: string
  code: string
  scriptType?: string // "shell" / "python"；空=存量数据按 shebang 自动识别
  args?: string // 脚本默认命令行参数（测试与触发器未显式传参时使用）
  createdAt?: string
  updatedAt?: string
}

// 执行脚本资源
export interface ExecScript {
  id: number
  name: string
  code: string
  scriptType?: string // "shell" / "python"；空=存量数据按 shebang 自动识别
  args?: string // 脚本默认命令行参数（测试与执行器未显式传参时使用）
  createdAt?: string
  updatedAt?: string
}

// 日志事件资源（日志事件触发器数据源；触发器按 logEventId 引用，也支持内联配置）
export interface LogEventResource {
  id: number
  name: string // 日志事件名称（必填，≤50）
  path: string // 日志路径（仅限文本日志文件）
  regex: string // 日志监控内容（正则表达式）
  cooldown: number // 冷却时间 0~99（0=不冷却）
  cooldownUnit: CooldownUnit // second / minute / hour
  consecutive: number // 连续匹配次数 1~9（冷却=0 时强制 1）
  rotate: boolean // 轮转日志模式开关
  createdAt?: string
  updatedAt?: string
}

export interface TriggerRule {
  allDay: boolean
  timeRange?: string // "08:00-20:00"（allDay=true 时忽略）
  weekLimit?: number[] // 1=周一 ~ 7=周日（与 monthDayLimit 互斥）
  monthDayLimit?: number[] // 1~31（与 weekLimit 互斥）
  thresholdMin: number // 生效阈值（分钟，1~60）
}

export interface Schedule {
  id: number
  name: string
  description?: string
  enabled: boolean
  category?: ScheduleCategory
  scheduleType: ScheduleType
  // 触发器清单（M9）：可空（仅手动触发/被调用）；后端旧便捷字段自动迁移
  triggers?: ScheduleTrigger[]
  // 执行器清单（M9 修订）：为空时后端由旧 Actions/FailureActions/顶层字段迁移
  executors?: ScheduleExecutor[]
  createdAt?: string
  updatedAt?: string
  // 风扇任务（旧字段，存量兼容）
  fanChannels?: number[] // FAN1~FAN2
  fanMode?: FanCurveMode
  // 硬盘任务（旧字段）
  diskGroups?: number[] // 组 1~4
  diskAction?: DiskAction
  forceOff?: boolean // 强制开关（仅下线）
  // 一次性
  runAt?: string
  // 每天 / 每周 / 每月
  time?: string // "10:00"
  weekdays?: number[]
  monthDays?: number[]
  // 触发任务规则字段（respectTimeRule 遵循用；旧 triggerKind 已废弃移除）
  triggerRule?: TriggerRule
  // 动作清单（旧字段，存量兼容）
  actions?: ScheduleAction[]
  failureActions?: ScheduleAction[]
}

// 定时计划动作（M6）
export type ScheduleActionType = 'fan_curve' | 'disk_power' | 'run_task' | 'enable_task' | 'disable_task'

export interface ScheduleAction {
  type: ScheduleActionType
  // fan_curve
  fanChannels?: number[]
  fanMode?: FanCurveMode
  // disk_power
  diskGroups?: number[]
  diskAction?: DiskAction
  forceOff?: boolean
  // run_task / enable_task / disable_task
  taskId?: number
  delaySec?: number
  respectEnabled?: boolean
  respectTimeRule?: boolean
}

export interface ScheduleLog {
  id: number
  scheduleId: number
  taskName: string
  category: ScheduleCategory
  scheduleType: ScheduleType
  channels: string
  result: ScheduleResult
  detail: string
  time: string
  sourceScheduleId?: number
  sourceResult?: string
  triggerKey?: string
  // 触发类型：time 定时事件 / log 日志事件 / monitor 监控事件 / manual 手动触发；串联投递为空
  triggerType?: string
}

export interface ScheduleLogQueryParams {
  name?: string
  category?: string
  type?: string
  triggerType?: string
  result?: string
  from?: string
  to?: string
  page?: number
  pageSize?: number
}

export interface ScheduleLogQueryResult {
  items: ScheduleLog[]
  total: number
  page: number
  size: number
  retainDays: number
  categoryLabels: Record<string, string>
  typeLabels: Record<string, string>
  triggerTypeLabels?: Record<string, string>
  resultLabels: Record<string, string>
}
