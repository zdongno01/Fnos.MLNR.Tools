package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mlnr/logger"
)

// DiskLogAction 硬盘组操作事件类型（收敛为 4 类：上线/下线/挂载/卸载）。
// 旧子类型（自动/强制/按钮/离线）在写入与历史迁移时归并到 4 类，原因写入 Remark。
type DiskLogAction string

const (
	DiskLogPowerOn  DiskLogAction = "power_on"  // 上线
	DiskLogPowerOff DiskLogAction = "power_off" // 下线
	DiskLogMount    DiskLogAction = "mount"     // 挂载
	DiskLogUnmount  DiskLogAction = "unmount"   // 卸载

	// 以下为旧版子类型（仅历史数据迁移与兼容判断使用，不再写入新日志）
	DiskLogForceOff   DiskLogAction = "force_off"
	DiskLogAutoOn     DiskLogAction = "auto_on"
	DiskLogAutoOff    DiskLogAction = "auto_off"
	DiskLogBtnOn      DiskLogAction = "btn_on"
	DiskLogBtnOff     DiskLogAction = "btn_off"
	DiskLogOfflineOn  DiskLogAction = "offline_on"
	DiskLogOfflineOff DiskLogAction = "offline_off"
)

// DiskLogEvent 单条硬盘组操作日志事件
type DiskLogEvent struct {
	Time    time.Time     `json:"time"`
	GroupID int           `json:"groupId"`
	Alias   string        `json:"alias,omitempty"`
	Action  DiskLogAction `json:"action"`
	Content string        `json:"content,omitempty"`
	Result  string        `json:"result,omitempty"` // ok / fail / skip
	Remark  string        `json:"remark,omitempty"` // 备注（自动执行/强制下线/离线事件/按钮打开/按钮关闭 + 错误信息）
	Error   string        `json:"error,omitempty"`  // 旧版错误字段（仅历史文件读取，规范化后清空）
}

// normalizeAction 旧动作 → 收敛动作 + 备注主体
func normalizeAction(a DiskLogAction) (DiskLogAction, string) {
	switch a {
	case DiskLogAutoOn:
		return DiskLogPowerOn, "自动执行"
	case DiskLogAutoOff:
		return DiskLogPowerOff, "自动执行"
	case DiskLogForceOff:
		return DiskLogPowerOff, "强制下线"
	case DiskLogBtnOn:
		return DiskLogPowerOn, "按钮打开"
	case DiskLogBtnOff:
		return DiskLogPowerOff, "按钮关闭"
	case DiskLogOfflineOn:
		return DiskLogPowerOn, "离线事件"
	case DiskLogOfflineOff:
		return DiskLogPowerOff, "离线事件"
	default:
		return a, ""
	}
}

// normalizeEvent 规范化单条事件：动作收敛 + 备注合并（备注主体; error:"错误消息"）。
func normalizeEvent(evt DiskLogEvent) DiskLogEvent {
	na, pre := normalizeAction(evt.Action)
	evt.Action = na
	var parts []string
	if pre != "" {
		parts = append(parts, pre)
	}
	if evt.Remark != "" {
		parts = append(parts, evt.Remark)
	}
	if evt.Error != "" {
		parts = append(parts, fmt.Sprintf(`error: "%s"`, evt.Error))
	}
	evt.Remark = strings.Join(parts, "; ")
	evt.Error = ""
	return evt
}

// DiskLogQueryParams 查询参数
type DiskLogQueryParams struct {
	GroupID  int           // -1 不筛选
	Action   DiskLogAction // "" 不筛选
	From     time.Time     // 零值不筛选
	To       time.Time     // 零值不筛选
	Page     int           // 从 1 开始
	PageSize int           // 默认 20
}

// DiskLogQueryResult 查询结果
type DiskLogQueryResult struct {
	Total int64          `json:"total"`
	Page  int            `json:"page"`
	Size  int            `json:"size"`
	Items []DiskLogEvent `json:"items"`
}

// DiskGroupStats 硬盘组统计信息（需求 3.2；累计值由统计快照持久化，不随日志滚动丢失）
type DiskGroupStats struct {
	GroupID        int    `json:"groupId"`
	Alias          string `json:"alias,omitempty"`
	OnlineMinutes  int64  `json:"onlineMinutes"`  // 本次在线时长（分钟，仅在线时有值）
	Online         bool   `json:"online"`         // 当前是否在线
	SwitchCount7d  int    `json:"switchCount7d"`  // 7 天内开关次数（一次 on + 一次 off 算一次）
	SwitchCount30d int    `json:"switchCount30d"` // 30 天内开关次数
	TotalSwitchCount int64 `json:"totalSwitchCount"` // 累计开关次数（min(总 on, 总 off)）
	TotalOnlineMinutes int64 `json:"totalOnlineMinutes"` // 总在线时长（分钟，含本次会话）
	AvgOnlineMinutes int64 `json:"avgOnlineMinutes"` // 平均单次在线时长（分钟，按上线次数平均）
	ForceOffCount  int64  `json:"forceOffCount"`  // 强制下线累计次数
}

// GroupStatAgg 单组实时统计聚合（常驻内存，定期落盘 disk_stats.json 快照）。
// 7/30 天窗口按"每日计数"保存，日志滚动后依然准确；累计值不随日志滚动丢失。
type GroupStatAgg struct {
	LastEventTime time.Time           `json:"lastEventTime"` // 已统计的最后事件时间（启动增量补算用）
	TotalOn       int64               `json:"totalOn"`       // 总上线次数
	TotalOff      int64               `json:"totalOff"`      // 总下线次数
	TotalForceOff int64               `json:"totalForceOff"` // 强制下线次数（备注含"强制下线"）
	TotalOnlineSec int64              `json:"totalOnlineSec"` // 已完成会话累计在线秒数（不含当前会话）
	DailyOn       map[string]int64    `json:"dailyOn"`       // 日期(2006-01-02) → 上线次数
	DailyOff      map[string]int64    `json:"dailyOff"`      // 日期(2006-01-02) → 下线次数
}

// DiskStatsFile 统计快照落盘结构（disk_stats.json）
type DiskStatsFile struct {
	Version int                    `json:"version"`
	SavedAt time.Time              `json:"savedAt"`
	Groups  map[int]*GroupStatAgg  `json:"groups"`
}

// DiskLogger 线程安全的硬盘组操作日志记录器。
// 文件持久化：所有事件同时写入内存缓冲和 JSON 行格式文件（disk_logs.jsonl）。
// 启动时从文件加载历史事件；Add 时追加到文件末尾。
type DiskLogger struct {
	mu       sync.RWMutex
	events   []DiskLogEvent // 全量事件（时间正序）
	filePath string         // 持久化文件路径

	// 统计聚合（实时维护 + 定期落盘快照 disk_stats.json）
	stats      map[int]*GroupStatAgg // groupID → 聚合
	pendingOn  map[int]time.Time     // groupID → 当前在线起始时间（off 时累计时长）
	statsPath  string
	stopAuto   chan struct{}
	bootTime   time.Time // 上位机本次启动时刻（固件离线时未闭合会话虚拟闭合基准）
}

// duplicateOnWindow 重复 ON 报文判定窗口：与当前未闭合会话起点间隔小于该值视为蓝牙/固件
// 抖动重复上报，不计数、不闭合（避免误触发双ON补偿）。
const duplicateOnWindow = 30 * time.Second

// G1: 持久化文件大小上限 2MB（JSON 行格式，每条约 200-300 bytes → 约 7000-10000 条）。
// 超过后当前文件轮转为 disk_logs.<时间戳>.jsonl，新建空文件继续追加。
const diskLogMaxFileSize = 2 << 20 // 2MB

// G1: 内存缓冲上限（事件保留最近 20000 条，防长期运行无界增长）。
const diskLogMaxEvents = 20000

// NewDiskLogger 创建日志记录器并从文件加载历史事件与统计快照。
func NewDiskLogger(dataDir string) *DiskLogger {
	l := &DiskLogger{
		filePath:  filepath.Join(dataDir, "disk_logs.jsonl"),
		statsPath: filepath.Join(dataDir, "disk_stats.json"),
		stats:     make(map[int]*GroupStatAgg),
		pendingOn: make(map[int]time.Time),
		bootTime:  time.Now(),
	}
	l.loadFromFile()
	l.loadStatsFile()
	return l
}

// BootTime 返回上位机本次启动时刻（固件离线场景下未闭合会话的虚拟闭合基准）。
func (l *DiskLogger) BootTime() time.Time {
	return l.bootTime
}

// loadFromFile 启动时从磁盘加载历史事件到内存。
func (l *DiskLogger) loadFromFile() {
	data, err := os.ReadFile(l.filePath)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warn("disk", "read log file %s: %v", l.filePath, err)
		}
		return
	}
	// JSON 行格式：每行一个 JSON 对象
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e DiskLogEvent
		if json.Unmarshal([]byte(line), &e) == nil {
			// 历史数据迁移：动作收敛（auto/force/btn/offline → power）+ 旧 error 并入备注
			l.events = append(l.events, normalizeEvent(e))
		}
	}
	logger.Info("disk", "loaded %d disk log events from %s", len(l.events), l.filePath)
	// G1: 加载后同样截断到最近 diskLogMaxEvents 条
	if len(l.events) > diskLogMaxEvents {
		l.events = append([]DiskLogEvent(nil), l.events[len(l.events)-diskLogMaxEvents:]...)
		logger.Info("disk", "trimmed to last %d events", len(l.events))
	}
}

// Add 追加一条日志事件（同时写入内存 + 文件）。
// 写入前规范化：动作收敛为 4 类，备注与错误合并为 Remark。
func (l *DiskLogger) Add(evt DiskLogEvent) {
	if evt.Time.IsZero() {
		evt.Time = time.Now()
	}
	evt = normalizeEvent(evt)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, evt)
	// G1: 内存缓冲有界——仅保留最近 diskLogMaxEvents 条
	if len(l.events) > diskLogMaxEvents {
		n := len(l.events) - diskLogMaxEvents
		l.events = append([]DiskLogEvent(nil), l.events[n:]...)
	}
	l.appendToFile(evt)
	l.recordEvent(evt)
}

// rotateFileLocked 把当前日志文件轮转为 disk_logs.<时间戳>.jsonl，并重建空文件。
// 调用方须持写锁（appendToFile 在 Add 写锁内）。
func (l *DiskLogger) rotateFileLocked() {
	backup := fmt.Sprintf("%s.%s", l.filePath, time.Now().Format("20060102-150405"))
	if err := os.Rename(l.filePath, backup); err != nil {
		logger.Warn("disk", "rotate log file %s -> %s: %v", l.filePath, backup, err)
		return
	}
	logger.Info("disk", "rotated disk log to %s", backup)
	// 新建空文件由 appendToFile 的 O_CREATE 自动完成
}

// recordEvent 把一条事件合并进统计聚合（Add 与启动补算共用；调用方须持写锁）。
func (l *DiskLogger) recordEvent(evt DiskLogEvent) {
	agg := l.stats[evt.GroupID]
	if agg == nil {
		agg = &GroupStatAgg{
			DailyOn:  make(map[string]int64),
			DailyOff: make(map[string]int64),
		}
		l.stats[evt.GroupID] = agg
	}
	// LastEventTime 取 max（LogDiskEventAt 可能回填历史时间）
	if evt.Time.After(agg.LastEventTime) {
		agg.LastEventTime = evt.Time
	}
	switch evt.Action {
	case DiskLogPowerOn:
		l.applyOnEvent(agg, evt.GroupID, evt.Time)
	case DiskLogPowerOff:
		l.applyOffEvent(agg, evt.GroupID, evt.Time, evt.Remark)
	}
}

// applyOnEvent 处理一条上线事件（调用方持写锁）。
// 双ON场景（已有未闭合会话且间隔 ≥30s，即整机断电重启后固件重新上报上线，
// 期间真实下线事件未被记录）：虚拟闭合旧会话（补一次下线计数、时长并入总在线），
// 再正常计本次上线——与"真实上电 2 次、断电 2 次"的物理事实一致。
// 重复/抖动 ON（间隔 <30s）：不计数、不闭合，保留原会话起点。
func (l *DiskLogger) applyOnEvent(agg *GroupStatAgg, gid int, ts time.Time) {
	if start, ok := l.pendingOn[gid]; ok {
		if d := ts.Sub(start); d >= duplicateOnWindow {
			// 双ON：虚拟闭合旧会话（真实断电时刻未知，取新上线时刻近似）
			agg.TotalOff++
			agg.DailyOff[ts.Format("2006-01-02")]++
			agg.TotalOnlineSec += int64(d / time.Second)
			delete(l.pendingOn, gid)
			logger.Info("disk", "stats: group %d pending session closed virtually at %s (dup ON after %s)",
				gid, ts.Format("2006-01-02 15:04:05"), d.Round(time.Second).String())
		} else {
			// 重复报文：不计数、不闭合、不更新会话起点（避免本次在线时长被抖动拉近）
			logger.Debug("disk", "stats: group %d duplicate ON within %s ignored", gid, duplicateOnWindow)
			return
		}
	}
	agg.TotalOn++
	agg.DailyOn[ts.Format("2006-01-02")]++
	l.pendingOn[gid] = ts
}

// applyOffEvent 处理一条下线事件（调用方持写锁）。
func (l *DiskLogger) applyOffEvent(agg *GroupStatAgg, gid int, ts time.Time, remark string) {
	agg.TotalOff++
	agg.DailyOff[ts.Format("2006-01-02")]++
	if strings.Contains(remark, "强制下线") {
		agg.TotalForceOff++
	}
	// 累计已完成会话在线时长
	if start, ok := l.pendingOn[gid]; ok {
		if d := ts.Sub(start); d > 0 {
			agg.TotalOnlineSec += int64(d / time.Second)
		}
		delete(l.pendingOn, gid)
	}
}

// ClosePendingSession 把指定组的未闭合在线会话按 closeAt 虚拟闭合（仅统计内部，不写业务日志）。
// 用于：整机断电/异常断电后固件上报该组为离线（真实下线事件丢失），
// 避免该会话时长永久丢失在总在线时长之外。返回是否实际闭合（幂等）。
func (l *DiskLogger) ClosePendingSession(gid int, closeAt time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	start, ok := l.pendingOn[gid]
	if !ok {
		return false
	}
	agg := l.stats[gid]
	if agg == nil {
		delete(l.pendingOn, gid)
		return true
	}
	if d := closeAt.Sub(start); d > 0 {
		agg.TotalOnlineSec += int64(d / time.Second)
	}
	agg.TotalOff++
	agg.DailyOff[closeAt.Format("2006-01-02")]++
	delete(l.pendingOn, gid)
	logger.Info("disk", "stats: group %d pending session closed at %s (group offline, closeAt=%s)",
		gid, start.Format("2006-01-02 15:04:05"), closeAt.Format("2006-01-02 15:04:05"))
	return true
}

// GroupStats 返回指定硬盘组的统计信息（快照聚合 + 本次实时在线时长）。
// diskOnline 当前实时在线状态（来自 DiskManager/Switch）
// diskOnlineSince 当前在线的起始时间（在线时有效）
// enabled 启用的统计组件 id（空/nil = 全部）：
//
//	onlineMinutes(本次在线时长) / totalOnline(总在线时长) / avgOnline(平均单次在线) /
//	switch7d(7天开关) / switch30d(30天开关) / totalSwitch(累计开关) / forceOff(强制下线)
func (l *DiskLogger) GroupStats(groupID int, alias string, diskOnline bool, diskOnlineSince time.Time, enabled []string) DiskGroupStats {
	l.mu.RLock()
	defer l.mu.RUnlock()

	now := time.Now()
	stats := DiskGroupStats{
		GroupID: groupID,
		Alias:   alias,
		Online:  diskOnline,
	}
	on := func(id string) bool { return len(enabled) == 0 || containsStr(enabled, id) }

	// 本次在线时长（实时）
	if on("onlineMinutes") && diskOnline && !diskOnlineSince.IsZero() {
		stats.OnlineMinutes = int64(now.Sub(diskOnlineSince).Minutes())
	}

	agg := l.stats[groupID]
	if agg == nil {
		return stats
	}

	// 7/30 天窗口：按每日计数聚合（min(Σon, Σoff) 与旧口径一致）
	if on("switch7d") {
		stats.SwitchCount7d = int(min64(dailySum(agg.DailyOn, 7), dailySum(agg.DailyOff, 7)))
	}
	if on("switch30d") {
		stats.SwitchCount30d = int(min64(dailySum(agg.DailyOn, 30), dailySum(agg.DailyOff, 30)))
	}

	// 累计开关次数（对开 = min(总 on, 总 off)）
	if on("totalSwitch") {
		stats.TotalSwitchCount = min64(agg.TotalOn, agg.TotalOff)
	}

	// 总在线时长与平均单次（含当前在线会话）
	if on("totalOnline") || on("avgOnline") {
		totalSec := agg.TotalOnlineSec
		if diskOnline && !diskOnlineSince.IsZero() {
			d := now.Sub(diskOnlineSince)
			if d > 0 {
				totalSec += int64(d / time.Second)
			}
		}
		stats.TotalOnlineMinutes = totalSec / 60
		if agg.TotalOn > 0 {
			stats.AvgOnlineMinutes = totalSec / 60 / agg.TotalOn
		}
	}

	// 强制下线累计
	if on("forceOff") {
		stats.ForceOffCount = agg.TotalForceOff
	}
	return stats
}

// dailySum 统计日期 map 中近 days 天（含今天）的计数之和。
func dailySum(m map[string]int64, days int) int64 {
	if len(m) == 0 {
		return 0
	}
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	cutoff := today.AddDate(0, 0, -(days - 1))
	var sum int64
	for d := cutoff; !d.After(today); d = d.AddDate(0, 0, 1) {
		sum += m[d.Format("2006-01-02")]
	}
	return sum
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// loadStatsFile 加载统计快照；随后用日志中"快照之后"的事件增量补算
// （覆盖上次未落盘/异常退出丢失的增量）。快照不存在时从全量日志重建基线。
func (l *DiskLogger) loadStatsFile() {
	l.mu.Lock()
	defer l.mu.Unlock()

	data, err := os.ReadFile(l.statsPath)
	if err == nil {
		var f DiskStatsFile
		if json.Unmarshal(data, &f) == nil && len(f.Groups) > 0 {
			l.stats = f.Groups
			for _, agg := range l.stats {
				if agg.DailyOn == nil {
					agg.DailyOn = make(map[string]int64)
				}
				if agg.DailyOff == nil {
					agg.DailyOff = make(map[string]int64)
				}
			}
			// 增量补算（事件正序单遍）：有快照的组只处理时间 > LastEventTime 的事件；
			// 无快照的新组全量计数。pendingOn 随正序时间推进自然正确。
			var replayed int
			for _, e := range l.events {
				agg := l.stats[e.GroupID]
				if agg == nil {
					agg = &GroupStatAgg{DailyOn: make(map[string]int64), DailyOff: make(map[string]int64)}
					l.stats[e.GroupID] = agg
				} else if !e.Time.After(agg.LastEventTime) {
					continue
				}
				if e.Time.After(agg.LastEventTime) {
					agg.LastEventTime = e.Time
				}
				if e.Action == DiskLogPowerOn {
					l.applyOnEvent(agg, e.GroupID, e.Time)
				} else if e.Action == DiskLogPowerOff {
					l.applyOffEvent(agg, e.GroupID, e.Time, e.Remark)
				}
				replayed++
			}
			logger.Info("disk", "loaded stats snapshot from %s (%d groups, %d events replayed)", l.statsPath, len(l.stats), replayed)
			return
		}
	} else if !os.IsNotExist(err) {
		logger.Warn("disk", "read stats file %s: %v", l.statsPath, err)
	}

	// 快照缺失/损坏：从全量日志重建基线
	for _, e := range l.events {
		l.recordEvent(e)
	}
	logger.Info("disk", "rebuilt stats from %d log events (no snapshot)", len(l.events))
}

// SaveStats 把统计聚合落盘为快照（定时 + 停机时调用）。
func (l *DiskLogger) SaveStats() {
	l.mu.RLock()
	f := DiskStatsFile{
		Version: 1,
		SavedAt: time.Now(),
		Groups:  make(map[int]*GroupStatAgg, len(l.stats)),
	}
	for gid, agg := range l.stats {
		cp := &GroupStatAgg{
			LastEventTime:  agg.LastEventTime,
			TotalOn:        agg.TotalOn,
			TotalOff:       agg.TotalOff,
			TotalForceOff:  agg.TotalForceOff,
			TotalOnlineSec: agg.TotalOnlineSec,
			DailyOn:        make(map[string]int64, len(agg.DailyOn)),
			DailyOff:       make(map[string]int64, len(agg.DailyOff)),
		}
		// 清理 30 天前的日期，保持快照精简
		cutoff := time.Now().AddDate(0, 0, -30).Format("2006-01-02")
		for k, v := range agg.DailyOn {
			if k >= cutoff {
				cp.DailyOn[k] = v
			}
		}
		for k, v := range agg.DailyOff {
			if k >= cutoff {
				cp.DailyOff[k] = v
			}
		}
		f.Groups[gid] = cp
	}
	l.mu.RUnlock()

	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		logger.Warn("disk", "marshal stats: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(l.statsPath), 0o755); err != nil {
		logger.Warn("disk", "mkdir stats dir: %v", err)
		return
	}
	tmp := l.statsPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		logger.Warn("disk", "write stats tmp: %v", err)
		return
	}
	if err := os.Rename(tmp, l.statsPath); err != nil {
		logger.Warn("disk", "rename stats: %v", err)
		return
	}
	logger.Debug("disk", "saved stats snapshot (%d groups)", len(f.Groups))
}

// StartAutoSave 启动定期落盘（interval 建议 10 分钟）；返回停止函数。
func (l *DiskLogger) StartAutoSave(interval time.Duration) func() {
	l.mu.Lock()
	if l.stopAuto != nil {
		close(l.stopAuto)
	}
	stop := make(chan struct{})
	l.stopAuto = stop
	l.mu.Unlock()

	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				l.SaveStats()
			}
		}
	}()
	return func() {
		l.mu.Lock()
		if l.stopAuto != nil {
			close(l.stopAuto)
			l.stopAuto = nil
		}
		l.mu.Unlock()
	}
}

// appendToFile 追加一条事件到 JSON 行文件。
func (l *DiskLogger) appendToFile(evt DiskLogEvent) {
	data, err := json.Marshal(evt)
	if err != nil {
		logger.Warn("disk", "marshal log event: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(l.filePath), 0o755); err != nil {
		logger.Warn("disk", "mkdir log dir: %v", err)
		return
	}
	// G1: 写入前检查文件大小，超过 2MB 先轮转——兑现"文件大小上限 2MB"的承诺
	if fi, err := os.Stat(l.filePath); err == nil && fi.Size() >= diskLogMaxFileSize {
		l.rotateFileLocked()
	}
	f, err := os.OpenFile(l.filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		logger.Warn("disk", "open log file: %v", err)
		return
	}
	defer f.Close()
	data = append(data, '\n')
	f.Write(data)
}

// Log 快捷方法：构造并追加一条事件。
// remark 为备注（条件说明/错误信息；与旧动作备注主体由 Add 规范化合并）。
func (l *DiskLogger) Log(groupID int, alias string, action DiskLogAction, content, result, remark string) {
	l.Add(DiskLogEvent{
		Time:    time.Now(),
		GroupID: groupID,
		Alias:   alias,
		Action:  action,
		Content: content,
		Result:  result,
		Remark:  remark,
	})
}

// Query 按条件查询日志事件（时间降序，分页）。
func (l *DiskLogger) Query(params DiskLogQueryParams) DiskLogQueryResult {
	if params.Page <= 0 {
		params.Page = 1
	}
	if params.PageSize <= 0 {
		params.PageSize = 20
	}
	if params.PageSize > 200 {
		params.PageSize = 200
	}

	l.mu.RLock()
	// 筛选
	var filtered []DiskLogEvent
	for i := len(l.events) - 1; i >= 0; i-- {
		e := l.events[i]
		if params.GroupID >= 0 && e.GroupID != params.GroupID {
			continue
		}
		if params.Action != "" && e.Action != params.Action {
			continue
		}
		if !params.From.IsZero() && e.Time.Before(params.From) {
			continue
		}
		if !params.To.IsZero() && e.Time.After(params.To) {
			continue
		}
		filtered = append(filtered, e)
	}
	l.mu.RUnlock()

	total := int64(len(filtered))

	// 分页
	start := (params.Page - 1) * params.PageSize
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + params.PageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	items := filtered[start:end]

	return DiskLogQueryResult{
		Total: total,
		Page:  params.Page,
		Size:  params.PageSize,
		Items: items,
	}
}

// Count 返回当前事件总数。
func (l *DiskLogger) Count() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.events)
}

// LastOnlineSince 查找指定组最后一次"无配对 off"的 on 事件时间（用于计算本次在线时长）。
// 如果最后一个事件是 on（power_on / auto_on）且没有紧随的 off，则返回该时间。
// 如果最后一个事件是 off，返回零值。
func (l *DiskLogger) LastOnlineSince(groupID int) time.Time {
	l.mu.RLock()
	defer l.mu.RUnlock()
	// 从后往前找该组最后一个事件
	for i := len(l.events) - 1; i >= 0; i-- {
		e := l.events[i]
		if e.GroupID != groupID {
			continue
		}
		// 动作已收敛：power_on=上线、power_off=下线
		if e.Action == DiskLogPowerOn {
			return e.Time
		}
		if e.Action == DiskLogPowerOff {
			return time.Time{}
		}
	}
	return time.Time{}
}

// actionLabel 动作类型 → 显示名
func actionLabel(a DiskLogAction) string {
	switch a {
	case DiskLogPowerOn:
		return "上线"
	case DiskLogPowerOff:
		return "下线"
	case DiskLogMount:
		return "挂载"
	case DiskLogUnmount:
		return "卸载"
	default:
		return fmt.Sprintf("未知(%s)", a)
	}
}

// sortedByTimeDesc 对事件按时间倒序（辅助）
func sortedByTimeDesc(events []DiskLogEvent) []DiskLogEvent {
	out := make([]DiskLogEvent, len(events))
	copy(out, events)
	sort.Slice(out, func(i, j int) bool {
		return out[i].Time.After(out[j].Time)
	})
	return out
}

// ===== 全局 DiskLogger 实例（由 main.go 初始化） =====
var diskLoggerInstance *DiskLogger

// InitDiskLogger 初始化全局实例（main.go 启动时调用，dataDir 为 fnOS 数据目录）。
func InitDiskLogger(dataDir string) {
	diskLoggerInstance = NewDiskLogger(dataDir)
}

// GetDiskLogger 获取全局实例（可能为 nil 表示未初始化）。
func GetDiskLogger() *DiskLogger {
	return diskLoggerInstance
}

// LogDiskEvent 快捷入口：向全局记录器追加事件。
// remark 为备注/错误信息。
func LogDiskEvent(groupID int, alias string, action DiskLogAction, content, result, remark string) {
	if diskLoggerInstance == nil {
		return
	}
	diskLoggerInstance.Log(groupID, alias, action, content, result, remark)
}

// LogDiskEventAt 快捷入口：以指定时间（如离线事件反推的真实发生时间）追加事件。
// remark 为备注/错误信息。
func LogDiskEventAt(groupID int, alias string, action DiskLogAction, content, result, remark string, ts time.Time) {
	if diskLoggerInstance == nil {
		return
	}
	diskLoggerInstance.Add(DiskLogEvent{
		Time:    ts,
		GroupID: groupID,
		Alias:   alias,
		Action:  action,
		Content: content,
		Result:  result,
		Remark:  remark,
	})
}
