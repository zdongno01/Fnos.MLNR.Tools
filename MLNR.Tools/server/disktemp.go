package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"mlnr/logger"
)

// ============================================================
// DiskTempReader —— 模块化磁盘温度读取（每盘独立、多级尝试、方法缓存、失败熔断）
//
// 设计目标：
//  1. 每块磁盘温度读取独立执行，避免相互依赖和阻塞
//  2. 首次读取：hwmon 优先（drivetemp/nvme）→ smartctl 兜底
//  3. 成功后记录读取方法（hwmon / smartctl）到内存缓存，后续直接命中
//  4. 全部方法均失败：标记熔断（circuit-break），30 分钟内不再尝试
//  5. 高频低开销：缓存命中时只读 sysfs 文件或跳过，几乎零开销
//  6. 多测温点：hwmon 方式返回该盘所有 temp*_input（Composite / Sensor1 / Sensor2 …）
//  7. HDD/SSD 类型与显示名：disk_type 决定前缀，model 替代 serial 作为默认名
// ============================================================

// tempMethod 记录每块盘当前使用的温度读取方法
type tempMethod int

const (
	methodUnknown  tempMethod = iota // 未尝试过
	methodHwmon                      // hwmon 成功（drivetemp / nvme hwmon）
	methodSmartctl                   // smartctl JSON 成功
	methodFailed                     // 全部方法失败（熔断中）
)

// diskTempState 每块盘的温度读取状态
type diskTempState struct {
	method   tempMethod // 当前使用的方法
	hwmonDir string     // hwmon 目录（hwmon 方法缓存）
	lastOK   time.Time  // 上次成功时间
	lastTry  time.Time  // 上次尝试时间
	failAt   time.Time  // 熔断起始时间（method=failed 时有效）
}

// DiskTempReader 磁盘温度读取器
type DiskTempReader struct {
	mu sync.RWMutex

	// 每块盘的状态：key = 块设备名（sda / nvme0n1）
	state map[string]*diskTempState

	// 一次性构建的 hwmon 映射（EvalSymlinks 昂贵，只构建一次）
	// key = 块设备名 → hwmon 目录
	hwmonDiskMap map[string]string

	// smartctl 路径（缓存）
	smartctlBin string

	// 熔断时间
	failCooldown time.Duration // 30 分钟
}

// NewDiskTempReader 创建读取器
func NewDiskTempReader() *DiskTempReader {
	return &DiskTempReader{
		state:        make(map[string]*diskTempState),
		hwmonDiskMap: make(map[string]string),
		failCooldown: 30 * time.Minute,
	}
}

// Init 启动时调一次，构建 hwmon→disk 映射（EvalSymlinks 昂贵，不放入热路径）
func (r *DiskTempReader) Init() {
	r.buildHwmonDiskMap()
	r.smartctlBin = findSmartctl()
	logger.Info("disktemp", "init: hwmon disk=%d, smartctl=%s",
		len(r.hwmonDiskMap), r.smartctlBin)
}

// buildHwmonDiskMap 扫描 hwmon 芯片，建立 "块设备名 → hwmon 目录" 映射。
// 只包含磁盘类芯片（drivetemp / nvme）。EvalSymlinks 昂贵，Init 时一次性完成。
func (r *DiskTempReader) buildHwmonDiskMap() {
	chips := scanHwmonChips()
	for _, c := range chips {
		switch c.name {
		case "drivetemp", "nvme":
			if c.devName != "" {
				r.hwmonDiskMap[c.devName] = c.base
			}
		}
	}
}

// ReadAll 批量读取所有磁盘温度（供 ThermalManager.collect 调用）
// disks 来自 DiskManager 缓存或 lsblk。now 是采集时间。
// 返回 []TemperatureReading：每块盘可能多条（每个 temp*_input 一条）。
func (r *DiskTempReader) ReadAll(disks []lsblkDisk, now string) []TemperatureReading {
	if len(disks) == 0 {
		return nil
	}
	var out []TemperatureReading
	for _, d := range disks {
		if d.Name == "" {
			continue
		}
		readings := r.buildReadings(d, now)
		out = append(out, readings...)
	}
	return out
}

// buildReadings 读取单块盘所有温度点（hwmon 返回多传感器，smartctl 返回单一 Composite）
func (r *DiskTempReader) buildReadings(d lsblkDisk, now string) []TemperatureReading {
	devName := d.Name

	// 确保 state 存在
	r.mu.RLock()
	st, ok := r.state[devName]
	r.mu.RUnlock()
	if !ok {
		st = &diskTempState{method: methodUnknown}
		r.mu.Lock()
		r.state[devName] = st
		r.mu.Unlock()
	}

	// 检查熔断：全部方法失败后 cooldown 内不再尝试
	if st.method == methodFailed {
		if time.Since(st.failAt) < r.failCooldown {
			return nil
		}
		st.method = methodUnknown
	}

	// 按缓存的 method 直接读（命中路径）
	var raw []sensorRaw
	var err error
	switch st.method {
	case methodHwmon:
		raw, err = r.readHwmonCached(st)
	case methodSmartctl:
		raw, err = r.readSmartctlSensors(devName)
	case methodUnknown, methodFailed:
		raw, err = r.readMultiLevel(st, devName)
	}

	if err != nil || len(raw) == 0 {
		logger.Debug("disktemp", "%s all methods failed: %v", devName, err)
		st.method = methodFailed
		st.failAt = time.Now()
		st.lastTry = time.Now()
		return nil
	}

	// 成功
	st.lastOK = time.Now()
	st.lastTry = time.Now()

	// 构造输出（多传感器 → 多 TemperatureReading）
	return buildDiskReadings(d, raw, now)
}

// sensorRaw 单次读取的原始传感器数据
type sensorRaw struct {
	zone    string  // "temp1" / "temp2" …
	label   string  // "Composite" / "Sensor 1" …
	celsius float64 // 温度值℃
	crit    float64 // 临界温度℃（0 = 未知）
}

// readMultiLevel 多级尝试：hwmon → smartctl，成功后写入 st.method
func (r *DiskTempReader) readMultiLevel(st *diskTempState, devName string) ([]sensorRaw, error) {
	// 1) hwmon 优先（返回多传感器）
	if hwmonDir, ok := r.hwmonDiskMap[devName]; ok {
		st.hwmonDir = hwmonDir
		if sensors, err := readHwmonDiskSensors(hwmonDir); err == nil && len(sensors) > 0 {
			st.method = methodHwmon
			return sensors, nil
		}
		logger.Debug("disktemp", "%s hwmon failed, trying smartctl", devName)
	}

	// 2) smartctl 兜底（只返回 Composite）
	if r.smartctlBin != "" {
		if sensors, err := r.readSmartctlSensors(devName); err == nil && len(sensors) > 0 {
			st.method = methodSmartctl
			return sensors, nil
		}
	}

	return nil, fmt.Errorf("hwmon and smartctl both unavailable")
}

// readHwmonCached 直接从缓存的 hwmon 目录读取（热路径）
func (r *DiskTempReader) readHwmonCached(st *diskTempState) ([]sensorRaw, error) {
	if st.hwmonDir == "" {
		return nil, fmt.Errorf("no hwmon dir cached")
	}
	return readHwmonDiskSensors(st.hwmonDir)
}

// readSmartctlSensors smartctl -Aj 读取单块盘温度（复用 diskinfo.go smartctlJSONDiskTemp 底层 JSON 解析）
func (r *DiskTempReader) readSmartctlSensors(devName string) ([]sensorRaw, error) {
	if r.smartctlBin == "" {
		return nil, fmt.Errorf("smartctl not available")
	}
	v, err := smartctlJSONDiskTemp(devName)
	if err != nil {
		return nil, err
	}
	return []sensorRaw{{zone: "temp1", label: "Composite", celsius: v}}, nil
}

// ============================================================
// hwmon 磁盘传感器批量读取（返回该 hwmon 芯片的 **全部** temp*_input）
// 修复回归：旧 collectHwmon 对每个磁盘芯片遍历全部 temp*_input，
// 之前的 disktemp 重构只取 Composite 一个，导致多传感器盘只显示一条。
// ============================================================

// readHwmonDiskSensors 从 hwmon 目录读取磁盘全部温度点（temp1/Composite、temp2/Sensor1、temp3/Sensor2…）
func readHwmonDiskSensors(hwmonDir string) ([]sensorRaw, error) {
	files, err := filepath.Glob(filepath.Join(hwmonDir, "temp*_input"))
	if err != nil || len(files) == 0 {
		return nil, fmt.Errorf("no temp*_input in %s", hwmonDir)
	}
	var out []sensorRaw
	for _, inputFile := range files {
		prefix := strings.TrimSuffix(filepath.Base(inputFile), "_input")

		milli, err := os.ReadFile(inputFile)
		if err != nil {
			continue
		}
		v, err := strconv.ParseInt(strings.TrimSpace(string(milli)), 10, 64)
		if err != nil || v <= 0 || v >= 150000 {
			continue
		}
		celsius := float64(v) / 1000.0

		label := prefix // 默认 temp1 / temp2
		if b, err := os.ReadFile(filepath.Join(hwmonDir, prefix+"_label")); err == nil {
			if t := strings.TrimSpace(string(b)); t != "" {
				label = t
			}
		}
		var crit float64
		if b, err := os.ReadFile(filepath.Join(hwmonDir, prefix+"_crit")); err == nil {
			if cv, err2 := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err2 == nil && cv > 0 {
				crit = float64(cv) / 1000.0
			}
		}
		out = append(out, sensorRaw{
			zone:    prefix,
			label:   label,
			celsius: celsius,
			crit:    crit,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("all temps are zero or invalid in %s", hwmonDir)
	}
	return out, nil
}

// ============================================================
// buildDiskReadings 根据 lsblkDisk 元数据 + sensorRaw 列表构造最终输出。
// Item 2-3 修复：
//   - 磁盘类型前缀用 disk_type（"SSD" / "HDD" / 未知回退 "Disk"）
//   - 默认显示名用 model 替代 serial（用户要求 TSC3AN128E2-F1T50S 而非 TTSFA25ANX03980）
//   - 但 serial 仍保留用于前端磁盘组关联（Serial 字段不变）
// ============================================================

func buildDiskReadings(d lsblkDisk, sensors []sensorRaw, now string) []TemperatureReading {
	// Item 1：显示名仅用 model（用户明确要求移除 disk_type 前缀拼接）
	displayName := d.Model
	if displayName == "" {
		displayName = d.Serial
	}
	if displayName == "" {
		displayName = d.Name // 最后回退块设备名
	}

	// Device key：SN 优先用于前端分组
	devKey := "disk:" + d.Name
	if d.Serial != "" {
		devKey = "disk:" + d.Serial
	}

	var out []TemperatureReading
	for _, s := range sensors {
		var critPtr *float64
		if s.crit > 0 {
			c := s.crit
			critPtr = &c
		}
		// 测温点 ID：SN 优先（与 collectHwmon 的 hdd_<SN>_<zone> 约定一致，
		// 保证风扇参考点/硬盘组关联按真实序列号匹配），无 SN 回退块设备名。
		idKey := d.Name
		if d.Serial != "" {
			idKey = d.Serial
		}
		out = append(out, TemperatureReading{
			ID:       "hdd_" + idKey + "_" + s.zone,
			Name:     fmt.Sprintf("%s · %s", displayName, s.label),
			Category: "hdd",
			Value:    s.celsius,
			Updated:  now,
			Device:   devKey,
			Model:    d.Model,
			Serial:   d.Serial,
			Zone:     s.zone,
			Crit:     critPtr,
		})
	}
	return out
}
