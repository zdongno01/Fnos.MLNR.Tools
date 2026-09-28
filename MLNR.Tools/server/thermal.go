package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"mlnr/logger"
)

// 预编译正则：用于解析 hwmon device 真实路径中的块设备名。
// 参考自 dome_hwmon.go，比字符串 split 更准确。
var (
	reHwmonNvmeDev = regexp.MustCompile(`nvme/nvme(\d+)$`)
	reHwmonScsiTgt = regexp.MustCompile(`(\d+:\d+:\d+:\d+)$`)
)

// ===== 温度相关数据结构 =====

// TemperatureReading 单个温度读数
type TemperatureReading struct {
	ID       string  `json:"id"`       // 唯一 ID
	Name     string  `json:"name"`     // 展示名称（CPU Package / MB / HDD sda 等）
	Category string  `json:"category"` // "cpu" | "mb" | "hdd" | "other"
	Value    float64 `json:"value"`    // 摄氏度
	Updated  string  `json:"updated"`  // 采集时间
	// 3.md：设备级元数据（前端按 Device 分组展示，SN 用于磁盘关联）
	Device string   `json:"device"`         // 所属设备分组键（hwmon 芯片名 / 磁盘 SN / thermal zone 名）
	Model  string   `json:"model"`          // 设备型号（如磁盘型号，可能为空）
	Serial string   `json:"serial"`         // 设备序列号 SN（磁盘关联 key，可能为空）
	Zone   string   `json:"zone"`           // hwmon 测温点序号（temp1/temp2…），用于同设备多传感器区分
	Crit   *float64 `json:"crit,omitempty"` // 3.md：临界温度（temp*_crit，℃），可能为空
}

// ThermalSnapshot 温度快照（一次采集全部结果）
type ThermalSnapshot struct {
	Temps []TemperatureReading `json:"temps"`
	Time  string               `json:"time"`
}

// ===== 历史存储（环形缓冲 + 持久化） =====

// HistoryPoint 单个历史数据点（温度或转速）
type HistoryPoint struct {
	Time  int64   `json:"t"` // 毫秒时间戳
	Value float64 `json:"v"` // 值（℃ 或 %）
}

// HistoryStore 管理多个 series 的历史数据
type HistoryStore struct {
	mu      sync.RWMutex
	dataDir string
	// seriesName -> points（按时间升序，保留上限）
	buckets map[string][]HistoryPoint
	// 每个 series 最大保留点数（约一周 @ 1 分钟 = 10080 点）
	maxPoints int
}

// NewHistoryStore 创建历史存储。
func NewHistoryStore(dataDir string) *HistoryStore {
	return &HistoryStore{
		dataDir:   dataDir,
		buckets:   make(map[string][]HistoryPoint),
		maxPoints: 14 * 24 * 60, // 14 天，每分钟 1 点
	}
}

// Load 从持久化目录读取历史。
func (h *HistoryStore) Load() error {
	if h.dataDir == "" {
		return nil
	}
	if err := os.MkdirAll(h.dataDir, 0o755); err != nil {
		return err
	}
	// 每个 series 对应一个文件：hist_<name>.json
	files, err := filepath.Glob(filepath.Join(h.dataDir, "hist_*.json"))
	if err != nil {
		return nil // 没有历史也不算错误
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		name = strings.TrimPrefix(name, "hist_")
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var pts []HistoryPoint
		if err := jsonUnmarshal(data, &pts); err != nil {
			continue
		}
		if len(pts) > h.maxPoints {
			pts = pts[len(pts)-h.maxPoints:]
		}
		h.buckets[name] = pts
	}
	return nil
}

// SaveAll 保存所有 series 到磁盘（后台定时调用）。
func (h *HistoryStore) SaveAll() error {
	if h.dataDir == "" {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for name, pts := range h.buckets {
		path := filepath.Join(h.dataDir, fmt.Sprintf("hist_%s.json", name))
		data, err := jsonMarshal(pts)
		if err != nil {
			logger.Warn("history", "marshal %s: %v", name, err)
			continue
		}
		if err := writeAtomic(path, data); err != nil {
			logger.Warn("history", "save %s: %v", name, err)
		}
	}
	return nil
}

// Append 追加一个数据点，超出上限时截断最旧数据。
func (h *HistoryStore) Append(series string, p HistoryPoint) {
	h.mu.Lock()
	defer h.mu.Unlock()
	arr := h.buckets[series]
	// 避免同一毫秒重复（可选：若时间倒退/相等，也追加，不阻塞）
	arr = append(arr, p)
	if len(arr) > h.maxPoints {
		arr = arr[len(arr)-h.maxPoints:]
	}
	h.buckets[series] = arr
}

// Query 返回 [start, end] 区间内的点。start/end 为毫秒时间戳，0 表示不限制。
// 为了前端图表性能，超过 800 点时进行等距下采样。
func (h *HistoryStore) Query(series string, startMs, endMs int64) []HistoryPoint {
	h.mu.RLock()
	defer h.mu.RUnlock()
	arr, ok := h.buckets[series]
	if !ok {
		return nil
	}
	// 1) 先按时间筛选
	i0, i1 := 0, len(arr)
	if startMs > 0 {
		for i0 < len(arr) && arr[i0].Time < startMs {
			i0++
		}
	}
	if endMs > 0 {
		for i1 > i0 && arr[i1-1].Time > endMs {
			i1--
		}
	}
	if i0 >= i1 {
		return nil
	}
	filtered := arr[i0:i1]
	return downsample(filtered, 800)
}

// downsample 按最大点数等距下采样，保留第一个和最后一个点。
func downsample(in []HistoryPoint, maxN int) []HistoryPoint {
	if len(in) <= maxN || maxN <= 2 {
		return in
	}
	out := make([]HistoryPoint, 0, maxN)
	step := float64(len(in)-1) / float64(maxN-1)
	for i := 0; i < maxN-1; i++ {
		idx := int(float64(i) * step)
		if idx < len(in) {
			out = append(out, in[idx])
		}
	}
	out = append(out, in[len(in)-1])
	return out
}

// ===== 温度采集器 =====

// ThermalManager 管理温度采集、历史存储与自动调速。
type ThermalManager struct {
	mu       sync.RWMutex
	snapshot ThermalSnapshot
	history  *HistoryStore
	stopCh   chan struct{}
	stopping bool
	hub      *Hub
	ble      *BLEManager
	disk     *DiskManager
	// 模块化磁盘温度读取器（hwmon 优先 → smartctl 兜底，方法缓存 + 失败熔断）
	diskTemp *DiskTempReader
	// 故障日志限频：fanID → 上次记录的故障状态签名（"" = 正常/无记录）
	faultLogMu  sync.Mutex
	faultLogged map[int]string
	// 硬盘上电过渡容错窗口：SW 刚 ON（组在线）但温度/缓存尚未就绪期间，
	// 不触发故障转速，保持当前转速等待温度恢复（默认 60s）。
	powerUpGrace time.Duration
	// mockMode 演示模式（mlnr_BLE_MOCK=1）：跳过真实采集与自动调速下发，
	// 温度快照/历史由 demo_mode.go 注入，避免定时任务覆盖演示数据或触发真实下发。
	mockMode bool
}

// NewThermalManager 创建温度管理器。
// ble 用于读取风扇状态与下发自动调速指令（可传 nil）。
// disk 用于硬盘组温度关联（可传 nil）。
func NewThermalManager(dataDir string, hub *Hub, ble *BLEManager, disk *DiskManager) *ThermalManager {
	return &ThermalManager{
		history:     NewHistoryStore(dataDir),
		hub:         hub,
		ble:         ble,
		disk:        disk,
		diskTemp:    NewDiskTempReader(),
		faultLogged: make(map[int]string),
		powerUpGrace: 60 * time.Second,
	}
}

// Start 启动：加载历史，开始 1 分钟周期采集 + 每 15 秒一次的快采（写入内存）。
// 历史按 1 分钟粒度落盘；温度 UI 快照每 15 秒推送一次。
func (t *ThermalManager) Start() error {
	if err := t.history.Load(); err != nil {
		logger.Warn("thermal", "load history: %v", err)
	}
	// 初始化磁盘温度读取器：构建 hwmon→disk 映射（EvalSymlinks 昂贵，仅启动时做一次）
	t.diskTemp.Init()
	t.stopCh = make(chan struct{})
	// 立即采集一次
	t.collect(true)

	go func() {
		fastTicker := time.NewTicker(15 * time.Second)
		autoSpdTicker := time.NewTicker(10 * time.Second) // 自动调速周期
		saveTicker := time.NewTicker(10 * time.Minute)
		defer fastTicker.Stop()
		defer autoSpdTicker.Stop()
		defer saveTicker.Stop()
		count := 0
		for {
			select {
			case <-t.stopCh:
				return
			case <-fastTicker.C:
				count++
				// 每 4 次（60 秒）写入一次历史
				writeHistory := (count % 4) == 0
				t.collect(writeHistory)
			case <-autoSpdTicker.C:
				// 自动调速：对 auto 模式风扇按曲线计算并下发（演示模式跳过，避免真实下发）
				if !t.mockMode {
					t.evalAutoFanSpeed()
				}
			case <-saveTicker.C:
				if err := t.history.SaveAll(); err != nil {
					logger.Warn("thermal", "save history: %v", err)
				}
			}
		}
	}()
	logger.Info("thermal", "started (15s snapshot, 10s auto-fan, 1min history)")
	return nil
}

// evalAutoFanSpeed 对所有 auto 模式风扇按温度曲线计算目标转速并下发。
// 仅在加密会话(WORK_RUN)下执行；手动模式风扇跳过。
// 调速判定（参考温度列表 TempRefs，任一在线即可正常调温）：
//
//	① 任一参考在线 → 取最高参考温度按曲线调温（部分参考离线不影响）
//	② 全部离线 → CPU 分类温度兜底调温
//	③ 完全离线且无 CPU 兜底 → 故障路径：磁盘参考且组已下线 → 跳过（预期行为，限频日志）；
//	   否则触发 FaultSpd（限频日志）
func (t *ThermalManager) evalAutoFanSpeed() {
	if t.ble == nil || !t.ble.IsEncryptedSession() {
		return
	}
	snap := t.Snapshot()
	// 磁盘元数据（设备名 → SN 映射，用于旧 hdd_<设备名> 参考点兼容解析）
	var disks []lsblkDisk
	if t.disk != nil {
		disks = diskManagerBlockDisks(t.disk)
	}
	tempMap := make(map[string]float64, len(snap.Temps))
	for _, r := range snap.Temps {
		tempMap[r.ID] = r.Value
	}
	// I2C 传感器通道温度（供 i2c_ch_<id> 参考点使用）：仅启用且有读数的通道
	// （不加入 snapshot.Temps，避免污染监控总览温度列表；仅服务风扇调速与参考点查询）
	if t.ble != nil {
		sSet := t.ble.GetSettings()
		for i, rd := range t.ble.GetSensorChannels() {
			if rd == nil || rd.State != "OK" || rd.Temperature <= 0 {
				continue
			}
			if i < len(sSet.SensorChannels) && !sSet.SensorChannels[i].Enabled {
				continue
			}
			tempMap[fmt.Sprintf("i2c_ch_%d", i)] = rd.Temperature
		}
	}

	s := t.ble.GetSettings()
	for _, fc := range s.Fans {
		if !fc.Enabled || fc.Mode != ModeAuto {
			continue
		}
		// 解析参考温度列表：优先使用 TempRefs，回退到旧 TempRef，再回退到 CPU Package
		refIDs := fc.TempRefs
		if len(refIDs) == 0 && fc.TempRef != "" {
			refIDs = []string{fc.TempRef}
		}
		if len(refIDs) == 0 {
			refIDs = []string{"cpu_package_0"}
		}

		// 从 Curves 读取当前生效曲线
		curve := fc.SpeedCurve
		if active, ok := fc.Curves[fc.ActiveCurve]; ok && len(active) >= 2 {
			curve = active
		} else if len(curve) < 2 {
			curve = DefaultSpeedCurve()
		}

		var target int

		// ① 任一参考温度在线 → 取最高参考温度正常调温（部分离线不影响；不受 CPU 兜底/故障转速干扰）
		var (
			maxTemp  float64
			anyFound bool
		)
		for _, refID := range refIDs {
			if tempC, ok := tempMap[refID]; ok {
				if !anyFound || tempC > maxTemp {
					maxTemp = tempC
					anyFound = true
				}
			}
		}
		if anyFound {
			target = EvalSpeedCurve(curve, maxTemp)
			t.logFanFaultOnce(fc.ID, "") // 恢复正常：清除故障日志记录
			if _, err := t.ble.SetFanSpeed(fc.ID, target); err != nil {
				logger.Debug("thermal", "auto fan %d spd=%d failed: %v", fc.ID, target, err)
			}
			continue
		}

		// ②' 硬盘离线转速（优先级：手动 > 故障 > 硬盘离线 > 温度曲线）：
		// 温度参考点全部为硬盘类型，且所有绑定硬盘均正常下线或本次运行从未上线时，
		// 温度缺失是预期状态（非故障），应用预设"硬盘离线转速"。
		// 必须优先于 CPU 兜底：纯硬盘参考点不应在硬盘离线时按 CPU 温度调温。
		if fc.DiskOfflineSpd > 0 && allRefsAreDisk(refIDs) {
			allDiskOffline := true
			for _, refID := range refIDs {
				serial := resolveHddSerial(refID, snap, disks)
				if serial == "" {
					// 参考点无法解析出 SN：保守视为不满足，避免误用离线转速
					allDiskOffline = false
					break
				}
				// 绑定硬盘所属启用组当前在线 → 仍有硬盘运行，不满足离线场景
				if t.disk != nil && t.disk.IsDiskGroupOnlineByDiskSerial(serial) {
					allDiskOffline = false
					break
				}
			}
			if allDiskOffline {
				target = fc.DiskOfflineSpd
				t.logFanFaultOnce(fc.ID, "") // 非故障场景：清除故障日志记录
				if _, err := t.ble.SetFanSpeed(fc.ID, target); err != nil {
					logger.Debug("thermal", "auto fan %d spd=%d (disk offline) failed: %v", fc.ID, target, err)
				}
				continue
			}
		}

		// ② 全部参考离线 → 尝试 CPU 分类兜底（仅适用于非纯硬盘参考点，如 CPU/I2C；
		// 纯硬盘参考点已在②'处理——若仍走 CPU 兜底，硬盘离线时会被错误地按 CPU 温度调温）
		cpuFallback := false
		var cpuTemp float64
		if !allRefsAreDisk(refIDs) {
			for _, r := range snap.Temps {
				if r.Category == "cpu" {
					cpuTemp = r.Value
					cpuFallback = true
					break
				}
			}
		}
		if cpuFallback {
			target = EvalSpeedCurve(curve, cpuTemp)
			t.logFanFaultOnce(fc.ID, "") // CPU 兜底视为可调温：清除故障日志记录
			if _, err := t.ble.SetFanSpeed(fc.ID, target); err != nil {
				logger.Debug("thermal", "auto fan %d spd=%d (cpu fallback) failed: %v", fc.ID, target, err)
			}
			continue
		}

		// ③ 完全离线且无 CPU 兜底 → 故障路径（仅在此处触发 FaultSpd / skip）
		if fc.FaultSpd <= 0 {
			continue
		}
		// 磁盘参考判断：任一 refID 以 "hdd_" 开头；key 解析为真实 SN（兼容旧设备名 ID）
		isDiskRef := false
		var diskSerial string
		for _, refID := range refIDs {
			if strings.HasPrefix(refID, "hdd_") {
				isDiskRef = true
				diskSerial = resolveHddSerial(refID, snap, disks)
				break
			}
		}
		// 磁盘组已手动下线：温度缺失是预期行为，冻结不触发故障转速（限频日志）
		if isDiskRef && diskSerial != "" && t.disk != nil && !t.disk.IsDiskGroupOnlineByDiskSerial(diskSerial) {
			if t.logFanFaultOnce(fc.ID, "skip-disk-"+diskSerial) {
				logger.Info("thermal", "fan %d: ref sensors offline (disk SN=%s, group offline) → skip fault",
					fc.ID, diskSerial)
			}
			continue
		}
		// ③' 上电过渡容错：组在线但温度缺失，且硬盘刚上线（SW ON 后宽限期内）——
		// 磁盘 spin-up、分区出现、缓存刷新、温度可读通常需数秒～数十秒，
		// 此窗口内保持当前转速（不触发 FaultSpd，也不主动调低），温度就绪后自然恢复曲线调速。
		if isDiskRef && diskSerial != "" && t.disk != nil && t.disk.IsDiskGroupPoweringUp(diskSerial, t.powerUpGrace) {
			if t.logFanFaultOnce(fc.ID, "powering-up-"+diskSerial) {
				logger.Info("thermal", "fan %d: ref disk SN=%s powering up, hold current speed (grace %ds)",
					fc.ID, diskSerial, int(t.powerUpGrace.Seconds()))
			}
			continue
		}
		target = fc.FaultSpd
		if t.logFanFaultOnce(fc.ID, "fault") {
			if isDiskRef && diskSerial != "" {
				logger.Warn("thermal", "fan %d: ref sensors offline (disk SN=%s, group online) → FaultSpd=%d",
					fc.ID, diskSerial, fc.FaultSpd)
			} else {
				logger.Warn("thermal", "fan %d: ref sensors offline → FaultSpd=%d", fc.ID, fc.FaultSpd)
			}
		}
		if _, err := t.ble.SetFanSpeed(fc.ID, target); err != nil {
			logger.Debug("thermal", "auto fan %d spd=%d (fault) failed: %v", fc.ID, target, err)
		}
	}
}

// allRefsAreDisk 判断温度参考点是否全部为硬盘类型（hdd_ 前缀）。
func allRefsAreDisk(refIDs []string) bool {
	if len(refIDs) == 0 {
		return false
	}
	for _, id := range refIDs {
		if !strings.HasPrefix(id, "hdd_") {
			return false
		}
	}
	return true
}

// resolveHddSerial 从 hdd_ 前缀的 ref ID 提取磁盘序列号（SN）。
// 新约定：ID 为 hdd_<SN>_<zone>（SN 优先）；旧数据/旧绑定可能为 hdd_<设备名>_<zone>。
//   - 快照中匹配到同盘记录（ID 前缀或 Serial 相同）→ 返回该盘真实 Serial
//   - 磁盘缓存中设备名映射到 SN → 返回 SN
//   - 都无法解析 → 回退 key 原样（尽力而为）
func resolveHddSerial(refID string, snap ThermalSnapshot, disks []lsblkDisk) string {
	if !strings.HasPrefix(refID, "hdd_") {
		return ""
	}
	rest := refID[4:] // 去掉 "hdd_"
	key := rest
	if idx := strings.Index(rest, "_"); idx > 0 {
		key = rest[:idx]
	}
	if key == "" {
		return ""
	}
	// 1) 快照 hdd 记录：ID 前缀匹配（旧 hdd_<设备名>_ / 新 hdd_<SN>_）→ 取其真实 Serial
	for _, r := range snap.Temps {
		if r.Category != "hdd" || r.Serial == "" {
			continue
		}
		if strings.HasPrefix(r.ID, "hdd_"+key+"_") {
			return r.Serial
		}
	}
	// 2) 磁盘缓存：设备名 → SN；本身就是 SN 直接返回
	for _, d := range disks {
		if d.Serial != "" && d.Name == key {
			return d.Serial
		}
		if d.Serial != "" && d.Serial == key {
			return key
		}
	}
	return key
}

// logFanFaultOnce 故障/跳过日志限频：同一风扇同一状态签名只记录一次。
// sig=="" 表示恢复正常（清除记录，不记录日志）；返回 true 表示本次应当记录日志。
func (t *ThermalManager) logFanFaultOnce(fanID int, sig string) bool {
	t.faultLogMu.Lock()
	defer t.faultLogMu.Unlock()
	if sig == "" {
		delete(t.faultLogged, fanID)
		return false
	}
	if prev, ok := t.faultLogged[fanID]; ok && prev == sig {
		return false
	}
	t.faultLogged[fanID] = sig
	return true
}

// Stop 停止采集并保存一次历史。
func (t *ThermalManager) Stop() {
	close(t.stopCh)
	_ = t.history.SaveAll()
}

// Snapshot 返回当前温度快照副本。
func (t *ThermalManager) Snapshot() ThermalSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	snap := t.snapshot
	// 深拷贝 temps
	cp := make([]TemperatureReading, len(snap.Temps))
	copy(cp, snap.Temps)
	snap.Temps = cp
	return snap
}

// History 返回某个 series 的历史数据（温度 ID："temp:<id>"；转速："fan:spd"）。
func (t *ThermalManager) History(series string, startMs, endMs int64) []HistoryPoint {
	return t.history.Query(series, startMs, endMs)
}

// DiskTemperatureMap 返回磁盘 SN → 最高温度（3.md：SN 为关联 key；同盘多测温点取最高值）。
// 供 DiskManager.GetViews 填充硬盘组视图温度。
func (t *ThermalManager) DiskTemperatureMap() map[string]float64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	m := make(map[string]float64)
	for _, r := range t.snapshot.Temps {
		if r.Category != "hdd" || r.Serial == "" {
			continue
		}
		if cur, ok := m[r.Serial]; !ok || r.Value > cur {
			m[r.Serial] = r.Value
		}
	}
	return m
}

// RecordFanSpeed 记录单路风扇转速历史（由 BLE 层调用）。
func (t *ThermalManager) RecordFanSpeed(fanID int, spd int) {
	t.history.Append(fmt.Sprintf("fan:%d:spd", fanID), HistoryPoint{
		Time:  time.Now().UnixMilli(),
		Value: float64(spd),
	})
}

// ===== 采集实现 =====

// collect 采集温度传感器；writeHistory=true 时写入历史。
// 采集策略（重构后）：
//  1. 磁盘温度：DiskTempReader.ReadAll —— 每块盘独立读取，hwmon 优先 → smartctl 兜底，
//     成功后缓存读取方法（hwmon/smartctl），后续命中缓存直接读 sysfs/跳过，
//     全部失败后熔断 30 分钟不重试（避免权限问题反复触发）。
//  2. CPU/MB/其他：collectHwmon 全量扫描 hwmon 芯片，过滤掉 hdd（已由 DiskTempReader 覆盖）。
//  3. 如果 hwmon 整体无数据，回退到 thermal zones（取主板/CPU 兜底）。
//
// 性能：DiskTempReader 命中缓存时只读 sysfs 几个文件，几乎零开销；
//
//	collectHwmon 同样只读 sysfs，每 15 秒一次完全可接受。
func (t *ThermalManager) collect(writeHistory bool) {
	// 演示模式：跳过真实采集，快照/历史由 demo_mode.go 注入
	if t.mockMode {
		return
	}
	var readings []TemperatureReading
	now := time.Now().Format(time.RFC3339)
	ts := time.Now().UnixMilli()

	// 从 DiskManager 缓存获取磁盘列表（启动时已全量收集，零开销）
	var disks []lsblkDisk
	if t.disk != nil {
		disks = diskManagerBlockDisks(t.disk)
	}

	// 1) 磁盘温度（每盘独立读取 + 方法缓存 + 熔断）
	if t.diskTemp != nil && len(disks) > 0 {
		readings = t.diskTemp.ReadAll(disks, now)
	}

	// 2) collectHwmon：CPU / MB / 其他芯片温度（全量扫描但过滤掉 hdd，已由 DiskTempReader 覆盖）
	hwmonAll := collectHwmon(now, disks)
	var hwmonNonDisk []TemperatureReading
	for _, r := range hwmonAll {
		if r.Category != "hdd" {
			hwmonNonDisk = append(hwmonNonDisk, r)
		}
	}
	readings = append(readings, hwmonNonDisk...)

	// 3) hwmon 完全无数据（含非磁盘也没数据）→ thermal zones 兜底
	if len(readings) == 0 {
		logger.Debug("thermal", "no hwmon data at all, falling back to thermal zones")
		readings = collectThermalZones(now)
	}

	// 4) 规范化命名与分类（不去重）
	readings = normalizeReadings(readings, now)

	snap := ThermalSnapshot{
		Temps: readings,
		Time:  now,
	}
	t.mu.Lock()
	t.snapshot = snap
	t.mu.Unlock()

	// 写入历史
	if writeHistory {
		for _, r := range readings {
			t.history.Append("temp:"+r.ID, HistoryPoint{Time: ts, Value: r.Value})
		}
		// 同时记录各路风扇转速历史（1 分钟粒度，与温度同步）
		if t.ble != nil {
			for _, fs := range t.ble.GetFanStates() {
				t.history.Append(fmt.Sprintf("fan:%d:spd", fs.Index), HistoryPoint{
					Time:  ts,
					Value: float64(fs.CurSpd),
				})
			}
			// I2C 传感器历史（1 分钟粒度，与温度同步）：
			// 前端系列名 i2c:<通道号>:temp / i2c:<通道号>:humi（历史趋势图温度/湿度 Tab）
			for i, rd := range t.ble.GetSensorChannels() {
				if rd == nil || rd.State != "OK" {
					continue
				}
				if rd.Temperature > 0 {
					t.history.Append(fmt.Sprintf("i2c:%d:temp", i), HistoryPoint{Time: ts, Value: rd.Temperature})
				}
				if rd.Humidity > 0 {
					t.history.Append(fmt.Sprintf("i2c:%d:humi", i), HistoryPoint{Time: ts, Value: rd.Humidity})
				}
			}
		}
	}

	// WS 推送
	if t.hub != nil {
		t.hub.BroadcastThermal(snap)
	}
}

// diskManagerBlockDisks 从 DiskManager 的 DiskInfoCache 提取磁盘列表（等价于 listBlockDisks 返回结构）。
func diskManagerBlockDisks(dm *DiskManager) []lsblkDisk {
	cache := dm.GetDiskCache()
	if cache == nil || !cache.IsLoaded() {
		return nil
	}
	disks := cache.Disks()
	out := make([]lsblkDisk, 0, len(disks))
	for _, sd := range disks {
		out = append(out, lsblkDisk{
			Name:     sd.Name,
			Serial:   sd.Serial,
			Model:    sd.Model,
			DiskType: sd.DiskType,
		})
	}
	return out
}

// collectThermalZones 读取 /sys/class/thermal/thermal_zone*
func collectThermalZones(now string) []TemperatureReading {
	var out []TemperatureReading
	entries, err := os.ReadDir("/sys/class/thermal")
	if err != nil {
		return nil
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "thermal_zone") {
			continue
		}
		base := filepath.Join("/sys/class/thermal", name)
		typeB, err := os.ReadFile(filepath.Join(base, "type"))
		if err != nil {
			continue
		}
		tType := strings.TrimSpace(string(typeB))
		tempB, err := os.ReadFile(filepath.Join(base, "temp"))
		if err != nil {
			continue
		}
		tempMilli, err := strconv.ParseInt(strings.TrimSpace(string(tempB)), 10, 64)
		if err != nil || tempMilli == 0 {
			continue
		}
		celsius := float64(tempMilli) / 1000.0
		out = append(out, TemperatureReading{
			ID:       "tz_" + name,
			Name:     fmt.Sprintf("%s (%s)", tType, name),
			Category: categoryFromThermalType(tType, name),
			Value:    celsius,
			Updated:  now,
			Device:   "tz:" + name,
		})
	}
	return out
}

// categoryFromThermalType 粗略按 type/thermal_zoneX 判断分类。
func categoryFromThermalType(tType, tzName string) string {
	switch {
	case strings.HasPrefix(tType, "x86_pkg_temp"):
		return "cpu"
	case tType == "acpitz":
		return "mb"
	case strings.Contains(tType, "cpu") || strings.Contains(tType, "coretemp") || strings.Contains(tType, "k10temp"):
		return "cpu"
	default:
		return "other"
	}
}

// ===== 3.md：hwmon 全设备扫描 + lsblk 磁盘 SN 关联 =====

// hwmonChip 单个 hwmon 芯片的元数据（3.md：name / device/model / device/serial）
type hwmonChip struct {
	base    string // /sys/class/hwmon/hwmonX
	name    string // name 文件内容（coretemp / drivetemp / nvme / nct6775 等）
	model   string // device/model（磁盘型号等）
	serial  string // device/serial（磁盘序列号 SN，可能为空）
	devName string // 解析出的块设备名（sda / nvme0n1），非磁盘为空
}

// scanHwmonChips 遍历 /sys/class/hwmon/hwmon*，收集全部芯片元数据。
// 3.md：hwmon 全设备扫描（不只 nvme/drivetemp），风扇/电压控制器芯片同样保留其温度点。
func scanHwmonChips() []hwmonChip {
	var chips []hwmonChip
	entries, err := os.ReadDir("/sys/class/hwmon")
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "hwmon") {
			continue
		}
		base := filepath.Join("/sys/class/hwmon", e.Name())
		nameB, err := os.ReadFile(filepath.Join(base, "name"))
		if err != nil {
			continue
		}
		chip := hwmonChip{
			base: base,
			name: strings.TrimSpace(string(nameB)),
		}
		// 3.md：device/model 与 device/serial（去换行）
		if b, err := os.ReadFile(filepath.Join(base, "device", "model")); err == nil {
			chip.model = strings.TrimSpace(string(b))
		}
		if b, err := os.ReadFile(filepath.Join(base, "device", "serial")); err == nil {
			chip.serial = strings.TrimSpace(string(b))
		}
		// 解析块设备名（磁盘 hwmon：SATA drivetemp / NVMe）
		chip.devName = resolveDriveName(base)
		chips = append(chips, chip)
	}
	return chips
}

// lsblkDisk lsblk 输出的物理磁盘（3.md：type=disk，SN 为关联 key）
type lsblkDisk struct {
	Name     string
	Serial   string
	Model    string
	DiskType string // "SSD" / "HDD" / ""（未知）
}

// listBlockDisks 通过 lsblk -J -o NAME,SERIAL,MODEL,TYPE 获取全部物理磁盘，
// 排除 loop/ram/zram 虚拟设备。
// lsblk 缺失/失败时回退纯 sysfs 读取（syscmd.go，SN 缺失时经 /dev/disk/by-id 反推）。
func listBlockDisks() []lsblkDisk {
	lsblk := findLsblk()
	if lsblk == "" {
		logger.Debug("thermal", "lsblk not found, fallback to sysfs")
		return sysfsListDisks()
	}
	out, err := runCmdOutput(lsblk, 5*time.Second, "-J", "-o", "NAME,SERIAL,MODEL,TYPE")
	if err != nil {
		logger.Debug("thermal", "lsblk failed: %v, fallback to sysfs", err)
		return sysfsListDisks()
	}
	var parsed struct {
		BlockDevices []struct {
			Name   string `json:"name"`
			Serial string `json:"serial"`
			Model  string `json:"model"`
			Type   string `json:"type"`
		} `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil
	}
	var disks []lsblkDisk
	for _, bd := range parsed.BlockDevices {
		if bd.Type != "disk" {
			continue
		}
		if strings.HasPrefix(bd.Name, "loop") || strings.HasPrefix(bd.Name, "ram") || strings.HasPrefix(bd.Name, "zram") {
			continue
		}
		serial := strings.TrimSpace(bd.Serial)
		model := strings.TrimSpace(bd.Model)
		// SATA 盘 lsblk 的 SERIAL 列常见为空：经 /dev/disk/by-id 的 ata-<MODEL>_<SERIAL> 反推补齐，
		// 保证温度兜底与硬盘组按 SN 关联可用（需求：SATA 硬盘信息读取增强）。
		if serial == "" {
			serial = diskByIDSerial(bd.Name, model)
		}
		disks = append(disks, lsblkDisk{
			Name:   bd.Name,
			Serial: serial,
			Model:  model,
		})
	}
	return disks
}

// collectHwmon 按 3.md 扫描 /sys/class/hwmon 全部测温点，并以序列号 SN 为关联 key 合并磁盘：
//   - hwmon 芯片读 name、device/model、device/serial、temp*_input、temp*_crit
//   - 磁盘芯片（drivetemp/nvme）优先用 device/serial；读不到时按块设备名匹配 lsblk 补 SN（SATA）
//   - SN 相同非空 → 温度记录归属磁盘（ID hdd_<SN>）；无 SN → 各自单独保留
//   - disks 参数：DiskManager 缓存的磁盘列表（可选，不为 nil 时优先使用，避免重复 lsblk）
func collectHwmon(now string, disks []lsblkDisk) []TemperatureReading {
	chips := scanHwmonChips()
	if len(chips) == 0 {
		return nil
	}

	// 磁盘列表：优先使用传入的缓存列表，否则调用 lsblk/sysfs
	if disks == nil {
		disks = listBlockDisks()
	}
	serialByName := make(map[string]string, len(disks))
	modelByName := make(map[string]string, len(disks))
	for _, d := range disks {
		if d.Serial != "" {
			serialByName[d.Name] = d.Serial
		}
		modelByName[d.Name] = d.Model
	}

	var out []TemperatureReading
	for _, chip := range chips {
		// 磁盘芯片补 SN：优先 device/serial，否则按块设备名匹配 lsblk（SATA drivetemp 无 device/serial）
		if chip.devName != "" && chip.serial == "" {
			if sn, ok := serialByName[chip.devName]; ok {
				chip.serial = sn
			}
		}
		if chip.devName != "" && chip.model == "" {
			if m, ok := modelByName[chip.devName]; ok {
				chip.model = m
			}
		}

		// 分类 + 设备分组键（3.md：全设备扫描保留）
		category, deviceKey, _ := classifyHwmonChip(chip)

		// 遍历 temp*_input（3.md：全部测温点）
		files, err := filepath.Glob(filepath.Join(chip.base, "temp*_input"))
		if err != nil {
			continue
		}
		for _, inputFile := range files {
			dir := filepath.Dir(inputFile)
			prefix := strings.TrimSuffix(filepath.Base(inputFile), "_input") // e.g. "temp1"

			label := prefix
			if b, err := os.ReadFile(filepath.Join(dir, prefix+"_label")); err == nil {
				if t := strings.TrimSpace(string(b)); t != "" {
					label = t
				}
			}

			milli, err := os.ReadFile(inputFile)
			if err != nil {
				continue
			}
			v, err := strconv.ParseInt(strings.TrimSpace(string(milli)), 10, 64)
			if err != nil || v == 0 {
				continue
			}
			celsius := float64(v) / 1000.0

			// 3.md：temp*_crit（临界温度，可选）
			var crit *float64
			if b, err := os.ReadFile(filepath.Join(dir, prefix+"_crit")); err == nil {
				if cv, err2 := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err2 == nil && cv != 0 {
					cc := float64(cv) / 1000.0
					crit = &cc
				}
			}

			id, displayName := hwmonReadingID(chip, category, prefix, label)
			out = append(out, TemperatureReading{
				ID:       id,
				Name:     displayName,
				Category: category,
				Value:    celsius,
				Updated:  now,
				Device:   deviceKey,
				Model:    chip.model,
				Serial:   chip.serial,
				Zone:     prefix,
				Crit:     crit,
			})
		}
	}
	return out
}

// classifyHwmonChip 判定测温点分类（cpu/mb/hdd/other）与设备分组键。
// 3.md：全设备扫描——drivetemp/nvme 为磁盘；coretemp/k10temp 为 CPU；nct/it/f71/aspeed/w83 为主板；
// 其余芯片按 other 保留（不丢弃）。
func classifyHwmonChip(chip hwmonChip) (category, deviceKey, chipLabel string) {
	switch {
	case chip.name == "drivetemp" || chip.name == "nvme" || (chip.devName != "" && chip.serial != ""):
		// 磁盘：SN 优先（3.md），无 SN 回退块设备名
		if chip.serial != "" {
			return "hdd", "disk:" + chip.serial, "HDD " + chip.serial
		}
		if chip.devName != "" {
			return "hdd", "disk:" + chip.devName, "HDD /dev/" + chip.devName
		}
		return "hdd", "disk:" + chip.name, "HDD " + chip.name
	case chip.name == "coretemp" || chip.name == "k10temp" || chip.name == "zenpower" || chip.name == "amdgpu":
		return "cpu", "cpu:" + chip.name, "CPU " + chip.name
	case strings.HasPrefix(chip.name, "nct") || strings.HasPrefix(chip.name, "it") || strings.HasPrefix(chip.name, "f71") ||
		chip.name == "aspeed" || strings.HasPrefix(chip.name, "w83"):
		return "mb", "mb:" + chip.name, "MB " + chip.name
	default:
		// 3.md：其他芯片（风扇/电压控制器等）的温度点也保留
		return "other", "other:" + chip.name, chip.name
	}
}

// hwmonReadingID 生成测温点 ID 与展示名（兼容旧 ID 约定：CPU Package id 0 / hdd_<dev>）。
func hwmonReadingID(chip hwmonChip, category, prefix, label string) (string, string) {
	switch category {
	case "hdd":
		// 磁盘：SN 优先（3.md），无 SN 回退块设备名。
		// 需求4：同一块盘可能含多个测温点（temp1/temp2…），ID 必须带 zone 后缀，
		// 否则同盘所有测温点 ID 相同，前端配置（别名/显示/图标）会互相联动。
		if chip.serial != "" {
			return "hdd_" + chip.serial + "_" + prefix, fmt.Sprintf("HDD %s · %s", chip.serial, label)
		}
		if chip.devName != "" {
			return "hdd_" + chip.devName + "_" + prefix, fmt.Sprintf("HDD /dev/%s · %s", chip.devName, label)
		}
		return "hdd_" + chip.name + "_" + prefix, fmt.Sprintf("HDD %s/%s", chip.name, label)
	case "cpu":
		// ps1.md 指定 CPU Package id 0
		if strings.Contains(strings.ToLower(label), "package") || strings.Contains(label, "Tdie") || label == "Tctl" || prefix == "temp1" {
			return "cpu_package_0", "CPU (Package id 0)"
		}
		return "cpu_" + chip.name + "_" + prefix, fmt.Sprintf("CPU %s", label)
	default:
		return "hw_" + chip.name + "_" + prefix, fmt.Sprintf("%s/%s", chip.name, label)
	}
}

// resolveDriveName 通过 hwmon device 软链接解析块设备名（如 sda / nvme0n1，不含 /dev/ 前缀）。
// 支持 SATA drivetemp（scsi target）和 NVMe（nvme/nvmeN）两种路径形态。
func resolveDriveName(hwmonDir string) string {
	devLink := filepath.Join(hwmonDir, "device")
	real, err := filepath.EvalSymlinks(devLink)
	if err != nil {
		return ""
	}

	// NVMe: 真实路径形如 /sys/devices/.../nvme/nvme0
	if m := reHwmonNvmeDev.FindStringSubmatch(real); m != nil {
		return "nvme" + m[1] + "n1"
	}

	// SATA drivetemp scsi target: 真实路径形如 .../0:0:0:0
	// 优先读 block 子目录的第一个块设备项，比字符串 split 更稳健
	if reHwmonScsiTgt.MatchString(real) {
		blockDir := filepath.Join(real, "block")
		if entries, err := os.ReadDir(blockDir); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					return e.Name()
				}
			}
		}
	}

	// 兜底：路径中查找 "block/" 段（兼容老格式 hwmon 嵌套）
	parts := strings.Split(real, string(filepath.Separator))
	for i, p := range parts {
		if p == "block" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// normalizeReadings 对传感器做规范化命名与分类，不去重。
// CPU Package id 0 / MB / HDD 等优先采用约定名称。
func normalizeReadings(in []TemperatureReading, now string) []TemperatureReading {
	result := make([]TemperatureReading, 0, len(in))
	var foundCPU, foundMB bool
	for _, r := range in {
		updated := r
		if updated.Category == "" {
			updated.Category = "other"
		}
		// CPU Package id 0 命名
		if r.ID == "cpu_package_0" || (r.Category == "cpu" && !foundCPU && strings.Contains(r.Name, "Package")) {
			updated.Name = "CPU (Package id 0)"
			foundCPU = true
		}
		// 主板 acpitz thermal_zone0 命名
		if strings.HasPrefix(r.ID, "tz_thermal_zone0") && strings.Contains(r.Name, "acpitz") {
			updated.Name = "主板 (acpitz · thermal_zone0)"
			updated.Category = "mb"
			foundMB = true
		}
		result = append(result, updated)
	}
	// 兜底：仍没找到 CPU Package 时，挑分类为 cpu 的第一条重命名
	if !foundCPU {
		for i, r := range result {
			if r.Category == "cpu" {
				result[i].Name = "CPU (Package id 0)"
				foundCPU = true
				break
			}
		}
	}
	// 兜底：还没 MB，挑分类为 mb 的第一条
	if !foundMB {
		for i, r := range result {
			if r.Category == "mb" {
				result[i].Name = "主板 (" + result[i].Name + ")"
				foundMB = true
				break
			}
		}
	}
	_ = foundCPU
	_ = foundMB
	return result
}

// ===== 温度-转速曲线求值 =====

// EvalSpeedCurve 根据曲线点返回对应温度下的目标转速百分比。
// 温度低于第一点：返回第一点 spd
// 温度高于最后一点：返回最后一点 spd
// 区间内：线性插值
// 曲线为 nil/空：回退到 DefaultSettings().TargetSpd
func EvalSpeedCurve(curve []CurvePoint, tempC float64) int {
	if len(curve) == 0 {
		curve = DefaultSpeedCurve()
	}
	if len(curve) == 0 {
		return 30
	}
	// 升序拷贝并按 temp 排序
	sorted := make([]CurvePoint, len(curve))
	copy(sorted, curve)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].Temp < sorted[j-1].Temp; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	if tempC <= sorted[0].Temp {
		return clampInt(sorted[0].Spd, 0, 100)
	}
	if tempC >= sorted[len(sorted)-1].Temp {
		return clampInt(sorted[len(sorted)-1].Spd, 0, 100)
	}
	for i := 0; i < len(sorted)-1; i++ {
		a, b := sorted[i], sorted[i+1]
		if tempC >= a.Temp && tempC <= b.Temp {
			if b.Temp == a.Temp {
				return clampInt(b.Spd, 0, 100)
			}
			ratio := (tempC - a.Temp) / (b.Temp - a.Temp)
			spd := float64(a.Spd) + ratio*float64(b.Spd-a.Spd)
			return clampInt(int(spd+0.5), 0, 100)
		}
	}
	return clampInt(sorted[len(sorted)-1].Spd, 0, 100)
}

// clampInt 夹紧
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ===== 序列化辅助（同包 thermal 内部使用） =====

func jsonMarshal(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}

func jsonUnmarshal(b []byte, v any) error {
	return json.Unmarshal(b, v)
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := tmpFile.Name()
	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		os.Remove(tmp)
		return err
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
