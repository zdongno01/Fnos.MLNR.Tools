package main

import (
	"strings"
	"time"
)

// ============================================================
// NR_F2S4 蓝牙控制板数据模型
// 依据 FS3.1.md / BT_RE.md：
//   - 硬件提供 2 路风扇通道(FAN1/FAN2)、4 路开关通道(SW1~SW4)
//   - 硬盘组为 fnOS 软件概念，复用 SW1~SW4 开关通道控制硬盘电源
//   - 配置分两层：fnOS 本地 JSON 配置 / 硬件 NVS(经 SAVE 落盘)
// ============================================================

// 硬件通道上限（配置超限项在加载时被忽略并输出告警日志）
const (
	MaxFanChannels    = 2 // FAN1 / FAN2
	MaxSwitchChannels = 4 // SW1 ~ SW4
	MaxDiskGroups     = 4 // 硬盘组复用 SW1~SW4
	MaxSensorChannels = 4 // I2C 传感器通道 CH0~CH3（与固件 SENSOR_CH_MAX 一致）

	defaultDeviceName = "NR_F2S4" // 默认广播名前缀（FS.md 上位机#8：广播名 NR_F2S4_{MAC 后 6 位大写}）
)

// ===== 连接状态 =====

// ConnectionState BLE 物理连接状态
type ConnectionState string

const (
	StateDisconnected ConnectionState = "disconnected" // 未连接
	StateScanning     ConnectionState = "scanning"     // 扫描中
	StateConnecting   ConnectionState = "connecting"   // 连接中
	StateConnected    ConnectionState = "connected"    // 已连接
	StateReconnecting ConnectionState = "reconnecting" // 重连中
)

// DeviceRunState 设备运行状态（FS4 安全模型，来自握手与 GET/事件解析）
type DeviceRunState string

const (
	RunStateUnknown  DeviceRunState = ""          // 未知（未连接）
	RunStateUninit   DeviceRunState = "UNINIT"    // 未初始化：任意设备可连，仅接受握手
	RunStateWorkWait DeviceRunState = "WORK_WAIT" // 等待授权：仅授权设备可连接
	RunStateWorkRun  DeviceRunState = "WORK_RUN"  // 运行：执行业务指令
)

// SessionType 会话加密类型
type SessionType string

const (
	SessionPlain     SessionType = "plain"     // 明文会话（未完成初始化握手）
	SessionEncrypted SessionType = "encrypted" // 加密会话（AES-128-GCM）
)

// ===== 运行模式 =====

// FanMode 风扇运行模式（fnOS 软件层概念，硬件无感知）
type FanMode string

const (
	ModeAuto   FanMode = "auto"   // 自动：按温度-转速曲线计算并周期下发
	ModeManual FanMode = "manual" // 手动：仅用户操作时下发
)

// ===== 硬件实时状态（解析自 GET FANn / GET SWITCHn） =====

// FanHWState 单路风扇硬件状态
type FanHWState struct {
	Index      int    `json:"index"`      // 1=FAN1(GPIO21), 2=FAN2(GPIO20)
	Pin        string `json:"pin"`        // PWM 输出引脚
	Enabled    bool   `json:"enabled"`    // 硬件启用状态
	PWMFreq    int    `json:"pwmFreq"`    // 当前 PWM 载波频率（Hz）
	PowerSpd   int    `json:"powerSpd"`   // 上电默认转速配置值（%）
	HBFallback int    `json:"hbFallback"` // 心跳失联后备转速（%）
	TargetSpd  int    `json:"targetSpd"`  // 硬件侧目标转速（下发指令目标值）
	CurSpd     int    `json:"curSpd"`     // 当前实际输出转速（平滑逼近值）
	Heartbeat  string `json:"heartbeat"`  // 心跳状态：OK / TIMEOUT
	UpdatedAt  string `json:"updatedAt"`  // 状态更新时间
}

// SwitchHWState 单路开关硬件状态
type SwitchHWState struct {
	Index        int    `json:"index"`        // 1~4 = SW1~SW4
	Pin          string `json:"pin"`          // GPIO 引脚
	Enabled      bool   `json:"enabled"`      // 硬件启用状态
	PowerOnState int    `json:"powerOnState"` // 开机默认电平 0/1
	PowerOnDelay int    `json:"powerOnDelay"` // 开机延迟（毫秒）
	State        int    `json:"state"`        // 当前电平 0=低(关) / 1=高(开)
	System       bool   `json:"system"`       // 系统开关标志（GETS 第 9 字节，CFG key=SYSTEM）
	UpdatedAt    string `json:"updatedAt"`    // 状态更新时间
}

// GlobalConfig 全局配置（GETG 应答解析，对应固件 GLOBAL target NVS 命名空间）
type GlobalConfig struct {
	HbTimeout uint16 `json:"hb_timeout"` // 心跳超时阈值（秒）
	DebugMode bool   `json:"debug_mode"` // 调试模式开关
	BleName   string `json:"ble_name"`   // BLE 广播名（1~20 字节可打印 ASCII）
}

// ===== I2C 传感器（多通道：AHT20 / BMP280 / LM75 / HTU21D，帧带 CH_ID/CH_TYPE） =====

// SensorKind 传感器类型（与固件 hw_config.h SENSOR_CH_TYPE 枚举一致）
type SensorKind uint8

const (
	SensorNone   SensorKind = 0 // 未配置
	SensorAHT20  SensorKind = 1 // 温湿度（I2C 0x38）
	SensorBMP280 SensorKind = 2 // 气压+海拔（I2C 0x76/0x77）
	SensorLM75   SensorKind = 3 // 温度（I2C 0x48~0x4F）
	SensorHTU21D SensorKind = 4 // 温湿度（I2C 0x40，与 SI7021 同族）
)

// SensorKindName 传感器类型显示名
func SensorKindName(k SensorKind) string {
	switch k {
	case SensorAHT20:
		return "AHT20"
	case SensorBMP280:
		return "BMP280"
	case SensorLM75:
		return "LM75"
	case SensorHTU21D:
		return "HTU21D"
	default:
		return "未配置"
	}
}

// SensorKindDefaultAddr 传感器默认 I2C 地址（十进制，与固件默认一致）
func SensorKindDefaultAddr(k SensorKind) int {
	switch k {
	case SensorAHT20:
		return 0x38
	case SensorBMP280:
		return 0x77
	case SensorLM75:
		return 0x48
	case SensorHTU21D:
		return 0x40
	default:
		return 0
	}
}

// SensorChannelConfig 单通道 I2C 传感器配置（本地 JSON + 下发硬件 NVS）
type SensorChannelConfig struct {
	ID          int        `json:"id"`          // 0~3
	Alias       string     `json:"alias"`       // 别名（仅本地显示）
	Kind        SensorKind `json:"kind"`        // 传感器类型
	Addr        int        `json:"addr"`        // I2C 地址（十进制）
	Enabled     bool       `json:"enabled"`     // 是否启用采集
	IntervalSec int        `json:"intervalSec"` // 采集间隔（秒，1~3600）
	// FS.md 上位机#6：按内容分别控制是否在监控总览页（主页）显示
	ShowTemp  bool `json:"showTemp"`  // 温度
	ShowHumi  bool `json:"showHumi"`  // 湿度
	ShowPress bool `json:"showPress"` // 压力
	ShowAlt   bool `json:"showAlt"`   // 海拔
}

// SensorChannelReading 单通道传感器实时读数（推送帧解析或 GETSR 快照）
type SensorChannelReading struct {
	ChID        int        `json:"chId"`
	Kind        SensorKind `json:"kind"`
	State       string     `json:"state"` // OK / FAIL
	Temperature float64    `json:"temperature"`
	Humidity    float64    `json:"humidity"`
	Pressure    float64    `json:"pressure"`
	Altitude    float64    `json:"altitude"`
	UpdatedAt   string     `json:"updatedAt"`
}

// DefaultSensorChannels 返回默认 4 通道（全部停用，型号/地址与固件默认一致）
func DefaultSensorChannels() []SensorChannelConfig {
	kinds := []SensorKind{SensorAHT20, SensorBMP280, SensorLM75, SensorHTU21D}
	out := make([]SensorChannelConfig, 0, len(kinds))
	for i, k := range kinds {
		out = append(out, SensorChannelConfig{
			ID:          i,
			Alias:       SensorKindName(k),
			Kind:        k,
			Addr:        SensorKindDefaultAddr(k),
			Enabled:     false,
			IntervalSec: 2,
		})
	}
	return out
}

// SensorData 单通道传感器数据（保留兼容：帧解析载荷与旧接口）
type SensorData struct {
	Temperature float64 `json:"temperature"` // 环境温度 ℃
	Humidity    float64 `json:"humidity"`    // 湿度 %
	Pressure    float64 `json:"pressure"`    // 压力 Pa
	Altitude    float64 `json:"altitude"`    // 海拔 m
	UpdatedAt   string  `json:"updatedAt"`   // 收到推送的时间
}

// ===== 设备连接信息 =====

// DeviceInfo 设备连接信息（页面全局状态栏数据源）
type DeviceInfo struct {
	Name            string          `json:"name"`            // 设备广播名 "NR_F2S4"
	Address         string          `json:"address"`         // BLE MAC 地址
	RSSI            int             `json:"rssi"`            // 信号强度（dBm，仅扫描发现时有效）
	SignalPercent   int             `json:"signalPercent"`   // 扫描信号强度百分比
	SignalQuality   int             `json:"signalQuality"`   // 固件推算信号质量（0~100，-1=未知；PING 应答携带，替代连接后 RSSI）
	ConnectionState ConnectionState `json:"connectionState"` // 物理连接状态
	SessionType     SessionType     `json:"sessionType"`     // 会话类型：明文/加密
	RunState        DeviceRunState  `json:"runState"`        // 设备运行状态 UNINIT/WORK_WAIT/WORK_RUN
	Version         string          `json:"version"`         // GET VERSION 结果
	ConnectedAt     string          `json:"connectedAt"`     // 连接建立时间
}

// ===== fnOS 本地配置（JSON 持久化，不上传硬件） =====

// FanChannelConfig 风扇通道本地配置
type FanChannelConfig struct {
	ID             int                     `json:"id"`                       // 1/2
	Alias          string                  `json:"alias"`                    // 风扇别名（仅本地）
	Enabled        bool                    `json:"enabled"`                  // 通道启用（本地概念，下发 CFG ENABLED）
	TempRef        string                  `json:"tempRef,omitempty"`        // [向后兼容] 旧单温度参考点，迁移后使用 TempRefs
	TempRefs       []string                `json:"tempRefs,omitempty"`       // 温度参考点传感器 ID 列表（fnOS 取列表中温度最高者调速）
	Mode           FanMode                 `json:"mode"`                     // auto/manual
	TargetSpd      int                     `json:"targetSpd"`                // 手动模式需求转速（下发目标）
	SpeedCurve     []CurvePoint            `json:"speedCurve,omitempty"`     // [向后兼容] 旧单曲线字段，Curves 初始化时迁移
	Curves         map[string][]CurvePoint `json:"curves,omitempty"`         // 3 曲线：efficient(高效) / daily(日常) / quiet(静音)
	ActiveCurve    string                  `json:"activeCurve,omitempty"`    // 当前生效曲线 key，默认 "daily"
	FaultSpd       int                     `json:"faultSpd,omitempty"`       // 故障转速：TempRefs 全部离线时触发（%，0 表示关闭）
	DiskOfflineSpd int                     `json:"diskOfflineSpd,omitempty"` // 硬盘离线转速：温度参考点全部为硬盘类型且全部绑定硬盘正常下线/从未上线时触发（%，0 表示关闭）
}

// DiskMount 单组挂载项（分区 + 挂载点）
type DiskMount struct {
	Partition  string `json:"partition"`  // 分区路径，如 /dev/sda1
	MountPoint string `json:"mountPoint"` // 挂载点，如 /vol1/1000/archive
}

// DiskEntry 硬盘条目
// 依据 3.md：以序列号 SN 为绑定主体（比较同盘用 Serial 而非 Device 路径），SN 为空时回退 Device。
type DiskEntry struct {
	Device    string      `json:"device"`    // 块设备路径 /dev/sda
	Alias     string      `json:"alias"`     // 硬盘别名
	Serial    string      `json:"serial"`    // 硬盘序列号（SN，绑定主体；可能为空）
	Mounts    []DiskMount `json:"mounts"`    // 多组挂载（分区→挂载点）
	MountPath string      `json:"mountPath"` // [已废弃兼容] 旧单挂载点字段，新数据使用 Mounts
	AutoMount bool        `json:"autoMount"` // 上线后自动挂载
	IsNVMe    bool        `json:"isNvme"`    // NVMe SSD（跳过停转指令）
}

// DiskGroupTask 硬盘组任务配置（上电后 / 下电前）。
// taskId>0 引用计划任务；触发后执行该任务的全部执行器（由定时计划调度引擎执行）。
// 旧脚本配置（onScript/offScript）已移除，脚本能力由任务内的"执行脚本"执行器承担。
type DiskGroupTask struct {
	TaskID int `json:"taskId,omitempty"` // 计划任务引用（>0）
}

// DiskGroupConfig 硬盘组本地配置（1 组绑定 1 路硬件开关）
type DiskGroupConfig struct {
	ID         int         `json:"id"`         // 1~4
	Alias      string      `json:"alias"`      // 硬盘组别名
	SwitchN    int         `json:"switchN"`    // 绑定硬件开关编号 SW1~SW4
	Enabled    bool        `json:"enabled"`    // 硬盘组启用
	AutoOnline bool        `json:"autoOnline"` // 程序启动后延时自动上线
	Disks      []DiskEntry `json:"disks"`      // 硬盘列表
	// 上电后等待时间（秒，1~300）：SW ON 下发固件后，等待该时长再检查硬盘是否就绪。
	// 前端必填 1~300；旧配置为 0 时回退默认 8 秒轮询窗口。
	PowerOnDelaySec int `json:"powerOnDelaySec,omitempty"`
	// 下线等待时间（秒，1~300）：卸载（umount）后等待该时长再继续断电流程。
	//   - 安全下线：umount 成功（全部挂载点已卸载）后等待该时长，再执行停转与 SW OFF；
	//   - 强制下线：umount -f 已发送（不要求成功）后等待该时长，再直接 SW OFF。
	// 前端必填 1~300；旧配置为 0 时回退默认 2 秒。
	// 注：JSON 键保留旧名 forceOffDelaySec 以兼容存量配置（旧名"强制下线等待时间"）。
	OfflineDelaySec int `json:"forceOffDelaySec,omitempty"`
	// 上电后任务：硬盘组上电（SW ON 上线流程完成）后异步触发执行；触发失败仅记日志，不阻断上线。
	OnTask *DiskGroupTask `json:"onTask,omitempty"`
	// 下电前任务：硬盘组下电流程开始前同步触发执行；必须执行成功才继续正常下电，
	// 执行失败/任务丢失终止正常下电（强制下电不受限，跳过下电前任务）。
	OffTask *DiskGroupTask `json:"offTask,omitempty"`
}

// TempSensorConfig 温度传感器显示配置
type TempSensorConfig struct {
	ID    string `json:"id"`    // 传感器唯一 ID（与 TemperatureReading.ID 一致）
	Alias string `json:"alias"` // 用户自定义别名（空则使用原始名称）
	Show  bool   `json:"show"`  // 是否在主页显示
	// FS.md 上位机#5：自定义图标与颜色（空则使用默认温度图标）
	Icon  string `json:"icon"`  // 图标名：temp/cpu/gpu/memory/mb/expansion/ssd/hdd/chassis/psu
	Color string `json:"color"` // 图标颜色（CSS 颜色字符串）
}

// Settings fnOS 本地运行设置
type Settings struct {
	DeviceName            string                `json:"deviceName"`                      // BLE 扫描过滤广播名
	AutoConnect           bool                  `json:"autoConnect"`                     // 启动时自动连接上次设备
	LastAddress           string                `json:"lastAddress"`                     // 上次成功连接的 BLE MAC
	BleKey                []byte                `json:"bleKey"`                          // [已废弃] 旧 RSA/固定密钥时代的握手密钥，仅向后兼容保留，不再使用
	DeviceKeys            map[string][]byte     `json:"deviceKeys,omitempty"`            // MAC(大写) → 16B AES-128 会话密钥（RSA-OAEP 密钥分发后持久化；固件重置后作废）
	HbTimeoutSec          int                   `json:"hbTimeoutSec"`                    // FS.md 上位机#7：全局心跳超时阈值（秒，3~60），下发 CFG GLOBAL HB_TIMEOUT_SEC
	SensorEnabled         bool                  `json:"sensorEnabled"`                   // 兼容旧字段：总采集开关（是否任一通道启用）
	SensorIntervalSec     int                   `json:"sensorIntervalSec"`               // 兼容旧字段：全局采集间隔（秒，1~30）
	SensorChannels        []SensorChannelConfig `json:"sensorChannels"`                  // 多通道 I2C 传感器配置（≤4）
	Fans                  []FanChannelConfig    `json:"fans"`                            // 风扇通道配置（≤2）
	DiskGroups            []DiskGroupConfig     `json:"diskGroups"`                      // 硬盘组配置（≤4）
	TempSensors           []TempSensorConfig    `json:"tempSensors"`                     // 温度传感器显示配置
	Theme                 string                `json:"theme"`                           // 上位机主题：light / dark / auto（auto 跟随 fnOS 系统主题，需求 #6）
	DirectSaveNVS         bool                  `json:"directSaveNVS"`                   // 直接写入 NVS：开启后前端每次 CFG 下发成功即自动跟发 SAVE(0x0A) 落盘；关闭时 CFG 仅写硬件内存，由用户在顶栏提示或设置页手动 SAVE
	HomeLayout            []HomeLayoutItem      `json:"homeLayout,omitempty"`            // 监控总览页可编辑布局（服务器端保存，所有访问终端统一显示）
	DiskStatsEnabled      []string              `json:"diskStatsEnabled,omitempty"`      // 硬盘组统计启用的组件 id（空=全部显示）
	ScheduleLogRetainDays int                   `json:"scheduleLogRetainDays,omitempty"` // 任务执行日志保留天数（1/7/30，默认 7）
	BottomNavKeys         []string              `json:"bottomNavKeys"`                   // 移动端底部导航栏显示的导航项 key（有序；空数组=隐藏底部导航；null/缺省=未配置，前端回退默认 总览/风扇/硬盘/设置）
}

// normalizeBottomNavKeys 规范化底部导航栏 key 列表：去空白、去重、限 8 项（与左侧导航栏条目数一致）。
func normalizeBottomNavKeys(keys []string) []string {
	seen := make(map[string]bool, len(keys))
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] || len(out) >= 8 {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

// normalizeScriptType 规范化脚本类型："python" → python；"shell"/"sh" → shell；
// 其余（含空串=存量数据）→ ""，执行时按 shebang 自动识别（sh 默认 / python shebang 识别）。
func normalizeScriptType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "python":
		return "python"
	case "shell", "sh":
		return "shell"
	default:
		return ""
	}
}

// ===== 定时计划（Schedule） =====

// Schedule 定时计划任务（风扇曲线切换 / 硬盘通道上下线）。
// 触发方式为下拉五选一：one_time / daily / weekly / monthly / trigger，
// 按类型只使用对应字段（其余字段保存时被归一化清空）。
type Schedule struct {
	ID           int       `json:"id"`
	Category     string    `json:"category"`     // "fan" | "disk"
	Name         string    `json:"name"`         // 任务名称（必填，≤50）
	Description  string    `json:"description"`  // 任务描述（可选，≤200）
	Enabled      bool      `json:"enabled"`      // 启用/禁用
	ScheduleType string    `json:"scheduleType"` // one_time | daily | weekly | monthly | trigger
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`

	// 风扇任务字段
	FanChannels []int  `json:"fanChannels,omitempty"` // FAN1~FAN2（1~2），多选
	FanMode     string `json:"fanMode,omitempty"`     // efficient(高效) / daily(日常) / quiet(静音)

	// 硬盘任务字段
	DiskGroups []int  `json:"diskGroups,omitempty"` // 硬盘组 1~4，多选
	DiskAction string `json:"diskAction,omitempty"` // online(上线) / offline(下线)
	ForceOff   bool   `json:"forceOff,omitempty"`   // 强制开关：下线失败时强制下线（仅下线动作时生效；上线时隐藏）

	// 一次性任务（scheduleType=one_time）
	RunAt *time.Time `json:"runAt,omitempty"`

	// 每天 / 每周 / 每月任务字段（Time 必填；weekly 用 Weekdays；monthly 用 MonthDays）
	Time      string `json:"time,omitempty"`      // "10:00"（24 小时制）
	Weekdays  []int  `json:"weekdays,omitempty"`  // 1=周一 ~ 7=周日（weekly）
	MonthDays []int  `json:"monthDays,omitempty"` // 1~31（monthly，多条；只有指定日期生效不顺延）

	// 触发任务规则字段（触发/被调用的日期规则，respectTimeRule 遵循用）
	TriggerRule *TriggerRule `json:"triggerRule,omitempty"`

	// 触发器清单（M8 规则引擎：可多张，任一命中即触发）。
	// Triggers 为空时由上层便捷字段（ScheduleType/RunAt/Time/Weekdays/MonthDays/
	// TriggerRule）迁移（见 handlers.normalizeSchedule / scheduler.triggersFor）。
	// 便捷字段保留用于存量兼容与展示，执行与防重复以 Triggers 为准。
	// 旧 sleep/idle 硬盘事件型（triggerKind）已由 log/monitor 预制事件替代，旧配置直接抛弃。
	Triggers []Trigger `json:"triggers,omitempty"`

	// 动作清单（M6 任务串联：Actions 为空时回退顶层便捷动作字段，见 scheduleActionsFor）
	Actions        []ScheduleAction `json:"actions,omitempty"`        // 成功时执行的动作清单
	FailureActions []ScheduleAction `json:"failureActions,omitempty"` // 失败时执行的动作清单（补救/告警/串联）

	// 执行器清单（M9 修订：原动作改名为执行器；任务级成功/失败分支移除，
	// 串联改为执行器级"成功后/失败后执行任务"）。Executors 为空时由旧
	// Actions/FailureActions 迁移（见 handlers.normalizeSchedule）。
	Executors []Executor `json:"executors,omitempty"`
}

// Trigger 定时计划触发器（M8 多触发器清单）。
// 类型：time（定时）/ log（日志事件）/ monitor（监控事件）。
// 旧格式（one_time/daily/weekly/monthly）在 normalize 时自动迁移；
// 旧 sleep/idle 硬盘事件型已由 log/monitor 事件替代（旧配置直接抛弃）。
type Trigger struct {
	// Type 触发器类型（M9 修订）：
	//   time    定时事件（周期 Period：once/daily/weekly/monthly）
	//   log     日志事件（LogEventID 引用全局日志事件资源，或 LogEventPath/LogEventRegex 内联配置）
	//   monitor 监控事件（MonitorPrebuilt="idle" 预制监控硬盘AB闲置，或 MonitorScriptID 引用自定义监控脚本）
	// 旧格式（one_time/daily/weekly/monthly/sleep/idle）在 normalize 时自动迁移。
	Type string `json:"type"`

	// ---- 定时事件（type=time）----
	Period       string     `json:"period,omitempty"`       // once / daily / weekly / monthly / loop
	RunAt        *time.Time `json:"runAt,omitempty"`        // period=once 执行时刻
	Time         string     `json:"time,omitempty"`         // period=daily/weekly/monthly "HH:mm"
	Weekdays     []int      `json:"weekdays,omitempty"`     // period=weekly 1=周一~7=周日
	MonthDays    []int      `json:"monthDays,omitempty"`    // period=monthly 1~31
	LoopInterval string     `json:"loopInterval,omitempty"` // period=loop "HH:MM:SS"

	// ---- 日志事件（type=log）----
	LogEventID int `json:"logEventId,omitempty"` // 全局日志事件资源引用（>0）；0=内联配置

	// 内联日志事件（不创建全局资源时直接存 Trigger 内）
	// 日志读取：LogEventPath + LogEventRegex；读取后的结果处理：冷却/连续匹配/轮转。
	LogEventPath         string `json:"logEventPath,omitempty"`
	LogEventRegex        string `json:"logEventRegex,omitempty"`
	LogEventCooldown     int    `json:"logEventCooldown,omitempty"`     // 0~99，0=不冷却
	LogEventCooldownUnit string `json:"logEventCooldownUnit,omitempty"` // second/minute/hour
	LogEventConsecutive  int    `json:"logEventConsecutive,omitempty"`  // 连续匹配 1~9
	LogEventRotate       bool   `json:"logEventRotate,omitempty"`       // 轮转日志模式

	// ---- 监控事件（type=monitor）----
	MonitorPrebuilt    string `json:"monitorPrebuilt,omitempty"`    // "idle"=监控硬盘AB闲置（预制只读）
	MonitorScriptID    int    `json:"monitorScriptId,omitempty"`    // 自定义监控脚本引用（>0）
	MonitorCode        string `json:"monitorCode,omitempty"`        // 内联监控脚本代码（不创建全局资源）
	MonitorScriptType  string `json:"monitorScriptType,omitempty"`  // 内联监控脚本类型 "shell" / "python"；空=按 shebang 自动识别（仅内联代码生效）
	MonitorScriptArgs  string `json:"monitorScriptArgs,omitempty"`  // 脚本命令行参数（sh 用 $1 $2 …，python 用 sys.argv[1:] 引用）
	MonitorDurationMin int    `json:"monitorDurationMin,omitempty"` // [已废弃] 监控持续时间，保留供存量数据兼容
	MonitorIntervalSec int    `json:"monitorIntervalSec,omitempty"` // 监控执行间隔 1~600 秒

	// ---- 旧格式兼容字段（sleep/idle 事件型；迁移后仅保留供展示/回填）----
	DiskGroups    []int  `json:"diskGroups,omitempty"`
	AllDay        bool   `json:"allDay,omitempty"`        // 全天生效（true 时 TimeRange 无效）
	TimeRange     string `json:"timeRange,omitempty"`     // "08:00-20:00"
	WeekLimit     []int  `json:"weekLimit,omitempty"`     // 周限制 1~7（与 MonthDayLimit 互斥）
	MonthDayLimit []int  `json:"monthDayLimit,omitempty"` // 每月特定日限制 1~31（与 WeekLimit 互斥）
	ThresholdMin  int    `json:"thresholdMin,omitempty"`  // 生效阈值（分钟，1~60）
}

// TriggerRule 触发任务生效规则
type TriggerRule struct {
	AllDay        bool   `json:"allDay"`                  // 全天生效；为 true 时 TimeRange 无效
	TimeRange     string `json:"timeRange,omitempty"`     // 时间范围生效 "08:00-20:00"
	WeekLimit     []int  `json:"weekLimit,omitempty"`     // 周限制（1~7）；与 MonthDayLimit 互斥
	MonthDayLimit []int  `json:"monthDayLimit,omitempty"` // 每月特定日限制（1~31）；与 WeekLimit 互斥
	ThresholdMin  int    `json:"thresholdMin"`            // 生效阈值（分钟，1~60）
}

// ScheduleAction 定时计划动作（M6：动作清单 + 任务串联）。
// 类型：
//   - fan_curve   风扇曲线切换（FanChannels + FanMode）
//   - disk_power  硬盘上下线（DiskGroups + DiskAction + ForceOff）
//   - run_task    投递目标任务到待执行队列（TaskID + DelaySec + 遵循开关）——任务串联
//   - enable_task / disable_task  直接启用/禁用目标任务（不改其触发器）
type ScheduleAction struct {
	Type string `json:"type"` // fan_curve / disk_power / run_task / enable_task / disable_task

	// fan_curve
	FanChannels []int  `json:"fanChannels,omitempty"`
	FanMode     string `json:"fanMode,omitempty"`

	// disk_power
	DiskGroups []int  `json:"diskGroups,omitempty"`
	DiskAction string `json:"diskAction,omitempty"`
	ForceOff   bool   `json:"forceOff,omitempty"`

	// run_task / enable_task / disable_task
	TaskID int `json:"taskId,omitempty"`
	// run_task：投递延迟（秒，0=立即投递，下一调度 tick 执行）
	DelaySec int `json:"delaySec,omitempty"`
	// run_task：遵循目标任务启用状态（目标被禁用则不执行）
	RespectEnabled bool `json:"respectEnabled,omitempty"`
	// run_task：遵循目标任务时间规则（仅其时间窗/周期命中当前时刻才执行）
	RespectTimeRule bool `json:"respectTimeRule,omitempty"`
}

// Executor 执行器（M9 修订：原动作改名为执行器）。
// 类型：fan_control(风扇控制) / disk_group_control(硬盘组控制) /
//
//	control_task(控制任务) / exec_script(执行脚本)。
//
// 每个执行器独立延迟 DelaySec（1~3600，0=立即）；硬盘组控制与执行脚本支持
// 失败重试（RetryEnabled/MaxRetries/RetryIntervalSec）与成功/失败后执行任务串联。
type Executor struct {
	Type string `json:"type"` // fan_control / disk_group_control / control_task / exec_script

	// 通用：独立延迟（秒，0=立即执行；范围 1~3600）
	DelaySec int `json:"delaySec,omitempty"`

	// ---- 风扇控制（fan_control）----
	FanID      int    `json:"fanId,omitempty"`      // 风扇通道（全部风扇清单单选）
	FanEnabled *bool  `json:"fanEnabled,omitempty"` // 启用/禁用风扇（nil=不改变）
	FanManual  bool   `json:"fanManual,omitempty"`  // true=手动控制 / false=自动控制
	FanPercent int    `json:"fanPercent,omitempty"` // 手动转速 1~100（手动控制时）
	FanCurve   string `json:"fanCurve,omitempty"`   // 自动曲线：efficient/daily/quiet（空=不切换，曲线维持不变）

	// ---- 硬盘组控制（disk_group_control）----
	DiskGroupID      int    `json:"diskGroupId,omitempty"`
	DiskAction       string `json:"diskAction,omitempty"`       // online / offline
	KillOccupied     bool   `json:"killOccupied,omitempty"`     // 自动终止占用（下线时）
	ForceOff         bool   `json:"forceOff,omitempty"`         // 强制下线（下线时）
	RetryEnabled     bool   `json:"retryEnabled,omitempty"`     // 失败重复执行开关
	MaxRetries       int    `json:"maxRetries,omitempty"`       // 最大重试次数 1~10（默认 3）
	RetryIntervalSec int    `json:"retryIntervalSec,omitempty"` // 重试间隔 1~3600 秒（默认 10）

	// ---- 控制任务（control_task）----
	TaskID          int    `json:"taskId,omitempty"`
	TaskAction      string `json:"taskAction,omitempty"`      // enable / disable / run
	RespectEnabled  bool   `json:"respectEnabled,omitempty"`  // 遵循目标任务启用状态（默认关闭）
	RespectTimeRule bool   `json:"respectTimeRule,omitempty"` // 遵循目标任务日期规则（默认关闭）

	// ---- 执行脚本（exec_script）----
	ScriptPrebuilt string `json:"scriptPrebuilt,omitempty"` // 预制脚本标识（V1 暂无预制执行脚本）
	ScriptID       int    `json:"scriptId,omitempty"`       // 自定义执行脚本引用（>0）
	ScriptCode     string `json:"scriptCode,omitempty"`     // 自定义内联脚本（sh / python，未引用资源时使用）
	ScriptType     string `json:"scriptType,omitempty"`     // 内联脚本类型 "shell" / "python"；空=按 shebang 自动识别（仅内联代码生效）
	ScriptArgs     string `json:"scriptArgs,omitempty"`     // 脚本命令行参数（sh 用 $1 $2 …，python 用 sys.argv[1:] 引用）

	// ---- 通用串联：成功/失败后执行任务（可选）----
	SuccessTaskID int `json:"successTaskId,omitempty"`
	FailureTaskID int `json:"failureTaskId,omitempty"`
}

// MonitorScript 监控脚本资源（监控事件触发器数据源）。
type MonitorScript struct {
	ID         int       `json:"id"`
	Name       string    `json:"name"`                 // 监控脚本名称（必填，≤50）
	Code       string    `json:"code"`                 // 脚本内容（持久化到 scripts/monitor-<id>.sh）
	ScriptType string    `json:"scriptType,omitempty"` // "shell" / "python"；空=按 shebang 自动识别（存量兼容）
	Args       string    `json:"args,omitempty"`       // 脚本默认命令行参数（测试与触发器未显式传参时使用）
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// ExecScript 执行脚本资源（执行脚本执行器数据源）。
type ExecScript struct {
	ID         int       `json:"id"`
	Name       string    `json:"name"`                 // 执行脚本名称（必填，≤50）
	Code       string    `json:"code"`                 // 脚本内容（持久化到 scripts/exec-<id>.sh）
	ScriptType string    `json:"scriptType,omitempty"` // "shell" / "python"；空=按 shebang 自动识别（存量兼容）
	Args       string    `json:"args,omitempty"`       // 脚本默认命令行参数（测试与执行器未显式传参时使用）
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// LogEvent 日志事件资源（日志事件触发器数据源；触发器可引用资源或内联配置）。
type LogEvent struct {
	ID           int       `json:"id"`
	Name         string    `json:"name"`         // 日志事件名称（必填，≤50）
	Path         string    `json:"path"`         // 日志路径（仅限文本日志文件）
	Regex        string    `json:"regex"`        // 日志监控内容（正则表达式）
	Cooldown     int       `json:"cooldown"`     // 冷却时间 0~99（0=不冷却）
	CooldownUnit string    `json:"cooldownUnit"` // second(默认) / minute / hour
	Consecutive  int       `json:"consecutive"`  // 连续匹配次数 1~9（冷却=0 时禁用，视为 1）
	Rotate       bool      `json:"rotate"`       // 轮转日志模式开关（默认关闭）
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// ScheduleLog 定时计划任务执行日志
type ScheduleLog struct {
	ID           int       `json:"id"`
	ScheduleID   int       `json:"scheduleId"`
	TaskName     string    `json:"taskName"`     // 任务名称（保存时快照）
	Category     string    `json:"category"`     // fan | disk
	ScheduleType string    `json:"scheduleType"` // 触发方式
	Channels     string    `json:"channels"`     // 执行通道摘要："FAN1,FAN2" / "组1,组2"
	Result       string    `json:"result"`       // success / failed
	Detail       string    `json:"detail"`       // 结果详情（失败原因；硬盘下线注明是否强制）
	Time         time.Time `json:"time"`         // 执行时间

	// 串联来源（M6：由其他任务投递执行时记录；0/"" = 常规触发）
	SourceScheduleID int    `json:"sourceScheduleId,omitempty"` // 来源任务 ID
	SourceResult     string `json:"sourceResult,omitempty"`     // 来源任务结果（success/failed）

	// 命中的触发器摘要（M8 per-trigger 防重复）：如 "daily:18:00" / "sleep" / "one_time:<RFC3339>"。
	// 串联投递执行（无触发器消费）为空。
	TriggerKey string `json:"triggerKey,omitempty"`

	// 触发类型（修订版日志展示/筛选）：time 定时事件 / log 日志事件 / monitor 监控事件 /
	// manual 手动触发；串联投递（被其他任务调用）为空。
	TriggerType string `json:"triggerType,omitempty"`
}

// ScheduleLog 触发类型（执行日志「触发类型」筛选与列展示）
const (
	ScheduleLogTriggerTime    = "time"    // 定时事件
	ScheduleLogTriggerLog     = "log"     // 日志事件
	ScheduleLogTriggerMonitor = "monitor" // 监控事件
	ScheduleLogTriggerManual  = "manual"  // 手动触发
)

// 定时计划常量
const (
	// Schedule.Category
	ScheduleCategoryFan  = "fan"
	ScheduleCategoryDisk = "disk"

	// Schedule.ScheduleType（触发方式下拉五选一）
	ScheduleTypeOneTime = "one_time" // 一次性
	ScheduleTypeDaily   = "daily"    // 每天
	ScheduleTypeWeekly  = "weekly"   // 每周
	ScheduleTypeMonthly = "monthly"  // 每月
	ScheduleTypeTrigger = "trigger"  // 触发任务

	// Schedule.FanMode（对齐 FanChannelConfig.Curves key）
	FanCurveEfficient = "efficient" // 高效
	FanCurveDaily     = "daily"     // 日常
	FanCurveQuiet     = "quiet"     // 静音

	// Schedule.DiskAction
	DiskActionOnline  = "online"  // 上线
	DiskActionOffline = "offline" // 下线

	// Trigger.Type（M9 修订：定时事件 / 日志事件 / 监控事件）
	TriggerTypeTime    = "time"    // 定时事件
	TriggerTypeLog     = "log"     // 日志事件
	TriggerTypeMonitor = "monitor" // 监控事件

	// Trigger.Period（定时事件周期）
	TriggerPeriodOnce    = "once"    // 一次
	TriggerPeriodDaily   = "daily"   // 每天
	TriggerPeriodWeekly  = "weekly"  // 每周
	TriggerPeriodMonthly = "monthly" // 每月
	TriggerPeriodLoop    = "loop"    // 循环（每 HH:MM:SS 执行一次，从首次命中起算）

	// 预制监控资源标识
	PrebuiltMonitorIdle = "idle" // 监控事件预制：监控硬盘AB闲置

	// Executor.Type（M9 修订：原动作改名执行器）
	ExecutorFanControl       = "fan_control"        // 风扇控制
	ExecutorDiskGroupControl = "disk_group_control" // 硬盘组控制
	ExecutorControlTask      = "control_task"       // 控制任务
	ExecutorExecScript       = "exec_script"        // 执行脚本

	// Executor.TaskAction（控制任务操作）
	TaskActionEnable  = "enable"  // 启用任务
	TaskActionDisable = "disable" // 禁用任务
	TaskActionRun     = "run"     // 执行任务

	// LogEvent.CooldownUnit（冷却时间单位）
	CooldownUnitSecond = "second" // 秒（默认）
	CooldownUnitMinute = "minute" // 分钟
	CooldownUnitHour   = "hour"   // 小时

	// Trigger.Type（M8 触发器清单）：时间型复用 ScheduleTypeOneTime~Monthly；
	// 旧 sleep/idle 硬盘事件型已由 log/monitor 预制事件替代（旧配置直接抛弃）

	// ScheduleLog.Result
	ScheduleResultSuccess = "success"
	ScheduleResultFailed  = "failed"

	// ScheduleLogRetainDays 合法取值
	ScheduleRetainDays1  = 1
	ScheduleRetainDays7  = 7
	ScheduleRetainDays30 = 30

	DefaultScheduleRetainDays = 7

	// ScheduleAction.Type（M6）
	ActionFanCurve    = "fan_curve"
	ActionDiskPower   = "disk_power"
	ActionRunTask     = "run_task"
	ActionEnableTask  = "enable_task"
	ActionDisableTask = "disable_task"

	// 任务串联限制（M6）
	// maxScheduleChainDepth 串联链最大深度：超过则拒绝投递（防级联风暴）
	maxScheduleChainDepth = 10
	// maxScheduleActionDelaySec 投递延迟上限（秒）= 24 小时
	maxScheduleActionDelaySec = 86400
)

// HomeLayoutItem 监控总览布局项（组件 id + 历史尺寸字段，尺寸现由模块内启用数量自动分配）
type HomeLayoutItem struct {
	ID   string `json:"id"`
	Size string `json:"size"`
}

// sanitizeSettings 返回剥离敏感字段（DeviceKeys：各设备 AES 会话密钥）后的设置副本。
// 所有对外序列化出口（GET /api/settings、WS 快照、saveSettings 回显等）必须经过此函数，
// 防止 AES 会话密钥经 API/WS 明文泄露。落盘仍使用带 DeviceKeys 的原始 Settings（见 store.go）。
func sanitizeSettings(s Settings) Settings {
	s.DeviceKeys = nil
	return s
}

// ===== 温控-转速曲线 =====

// CurvePoint 转速曲线控制点
type CurvePoint struct {
	Temp float64 `json:"temp"` // 温度，单位：℃
	Spd  int     `json:"spd"`  // 转速，单位：%
}

// DefaultSpeedCurve 默认的温度-转速曲线
func DefaultSpeedCurve() []CurvePoint {
	return []CurvePoint{
		{Temp: 30, Spd: 30},
		{Temp: 45, Spd: 40},
		{Temp: 60, Spd: 65},
		{Temp: 75, Spd: 85},
		{Temp: 90, Spd: 100},
	}
}

// cloneCurve 深拷贝曲线点
func cloneCurve(c []CurvePoint) []CurvePoint {
	out := make([]CurvePoint, len(c))
	copy(out, c)
	return out
}

// DefaultSettings 返回默认设置（2 风扇 + 4 硬盘组骨架）。
func DefaultSettings() Settings {
	defCurve := DefaultSpeedCurve()
	fans := make([]FanChannelConfig, 0, MaxFanChannels)
	for i := 1; i <= MaxFanChannels; i++ {
		fans = append(fans, FanChannelConfig{
			ID:         i,
			Alias:      "风扇 " + string(rune('0'+i)),
			Enabled:    true,
			Mode:       ModeAuto,
			TargetSpd:  30,
			SpeedCurve: cloneCurve(defCurve),
			Curves: map[string][]CurvePoint{
				"efficient": cloneCurve(defCurve),
				"daily":     cloneCurve(defCurve),
				"quiet":     cloneCurve(defCurve),
			},
			ActiveCurve: "daily",
			FaultSpd:    50,
		})
	}
	groups := make([]DiskGroupConfig, 0, MaxDiskGroups)
	for i := 1; i <= MaxDiskGroups; i++ {
		groups = append(groups, DiskGroupConfig{
			ID:      i,
			Alias:   "硬盘组 " + string(rune('0'+i)),
			SwitchN: i,
			Enabled: false,
			Disks:   []DiskEntry{},
		})
	}
	return Settings{
		DeviceName:            defaultDeviceName,
		AutoConnect:           true,
		HbTimeoutSec:          10,
		SensorEnabled:         false,
		SensorIntervalSec:     2,
		SensorChannels:        DefaultSensorChannels(),
		Fans:                  fans,
		DiskGroups:            groups,
		TempSensors:           []TempSensorConfig{},
		Theme:                 "auto",
		ScheduleLogRetainDays: DefaultScheduleRetainDays,
	}
}

// ===== 硬盘组运行视图（硬盘挂载状态 + 开关电平合并） =====

// DiskView 硬盘实时视图（含 fnOS 端挂载状态）
type DiskView struct {
	Device      string   `json:"device"`
	Alias       string   `json:"alias"`
	Serial      string   `json:"serial"` // 硬盘序列号（3.md：SN 优先标识）
	MountPath   string   `json:"mountPath"`
	AutoMount   bool     `json:"autoMount"`
	IsNVMe      bool     `json:"isNvme"`
	Mounted     bool     `json:"mounted"`     // fnOS 端读取系统挂载点获得
	Temperature *float64 `json:"temperature"` // 由 ThermalManager 按 SN 关联填充的实时温度（nil=无温度读数）
}

// DiskGroupView 硬盘组实时视图
type DiskGroupView struct {
	ID         int             `json:"id"`
	Alias      string          `json:"alias"`
	SwitchN    int             `json:"switchN"`
	Enabled    bool            `json:"enabled"` // 本地配置启用
	AutoOnline bool            `json:"autoOnline"`
	Online     bool            `json:"online"`   // 对应 SW 通道电平=1
	Conflict   bool            `json:"conflict"` // SW 通道被多组绑定冲突
	Disks      []DiskView      `json:"disks"`
	Stats      *DiskGroupStats `json:"stats,omitempty"` // 操作统计（日志驱动）
}

// ===== 指令应答 =====

// OccupyProcess 占用挂载点/设备的进程
type OccupyProcess struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
	User    string `json:"user"`
}

// CommandResult 指令执行结果
type CommandResult struct {
	OK      bool   `json:"ok"`
	Raw     string `json:"raw"`     // 设备原始应答
	Message string `json:"message"` // 解析后的提示信息
	// 卸载失败且检测到占用进程时：Decision="unmount_fail_occupied"，Occupied 为占用进程清单，
	// 前端需弹出选择（终止进程 / 强制卸载 / 取消），并以 action 参数重试
	Decision string          `json:"decision,omitempty"`
	Occupied []OccupyProcess `json:"occupied,omitempty"`
}

// ===== WebSocket 推送消息 =====

// StateUpdateMsg 推送给前端的状态更新消息
type StateUpdateMsg struct {
	Type           string                  `json:"type"` // "state_update"
	Device         DeviceInfo              `json:"device"`
	Fans           []FanHWState            `json:"fans,omitempty"`
	Switches       []SwitchHWState         `json:"switches,omitempty"`
	DiskGroups     []DiskGroupView         `json:"diskGroups,omitempty"`
	Sensor         *SensorData             `json:"sensor,omitempty"`         // 兼容旧字段：首通道 I2C 数据
	SensorChannels []*SensorChannelReading `json:"sensorChannels,omitempty"` // 多通道 I2C 传感器读数
	Setting        *Settings               `json:"setting,omitempty"`
	Time           string                  `json:"time"`
}

// LogMsg 推送给前端的日志消息
type LogMsg struct {
	Type    string `json:"type"`    // "log"
	Level   string `json:"level"`   // DEBUG/INFO/WARN/ERROR
	Module  string `json:"module"`  // 模块
	Message string `json:"message"` // 内容
	Time    string `json:"time"`
}

// ===== 配置文件结构 =====

// SettingsFile 设置持久化文件
type SettingsFile struct {
	Version  int      `json:"version"`
	Settings Settings `json:"settings"`
}

// LogEntryRecord 日志记录（用于前端展示历史日志）
type LogEntryRecord struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Module  string    `json:"module"`
	Message string    `json:"message"`
}
