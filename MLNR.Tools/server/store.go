package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"mlnr/logger"
)

// Store fnOS 本地设置持久化存储。
//
// 硬件配置（Settings）：按 BLE MAC 隔离 → settings.json / settings_<MAC>.json
// 资源（定时计划 / 脚本）：不按 MAC 隔离 → dataDir 顶层独立文件
//   - resources.json（schedules + logEvents）
//   - monitor-scripts.json + scripts/monitor-<id>.sh
//   - exec-scripts.json + scripts/exec-<id>.sh
type Store struct {
	mu         sync.RWMutex
	dataDir    string
	settings   Settings
	currentMAC string // 当前关联的 BLE MAC（空表示未连接，使用 settings.json）

	// 全局资源（不按 MAC 隔离）
	schedules      []Schedule
	logEvents      []LogEvent
	monitorScripts []MonitorScript // Code 字段始终为空，正文在 .sh 文件
	execScripts    []ExecScript    // Code 字段始终为空，正文在 .sh 文件
}

// NewStore 创建存储实例。
func NewStore(dataDir string) *Store {
	return &Store{dataDir: dataDir}
}

// Load 从磁盘加载初始设置（settings.json 或当前 MAC 文件，首次启动时调用一次）。
// 同时加载全局资源（不按 MAC 隔离）。
func (s *Store) Load() error {
	if err := os.MkdirAll(s.dataDir, 0o755); err != nil {
		return err
	}

	// 首次启动无 MAC 上下文 → 从 settings.json 加载
	s.mu.Lock()
	loaded := loadJSON[SettingsFile](s.filePathForMAC(""), SettingsFile{Version: 1, Settings: DefaultSettings()})
	s.settings = loaded.Settings
	s.applyDefaultsLocked()
	s.mu.Unlock()

	// 加载全局资源（不按 MAC 隔离，独立加锁）
	s.loadResources()

	s.mu.RLock()
	defer s.mu.RUnlock()
	logger.Info("store", "loaded initial settings from %s", s.filePathForMAC(""))
	return nil
}

// SwitchDeviceMAC 切换关联的 BLE 设备 MAC 地址。
// 调用时机：Connect/ConnectTo 成功后传入 MAC；Disconnect 后传入 ""（空）。
//   - MAC 变化时：持久化旧 MAC 配置 → 加载新 MAC 配置（首次从 settings.json 迁移）
//   - MAC 不变或首次设置：直接加载对应文件
func (s *Store) SwitchDeviceMAC(mac string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 清洗 MAC（冒号 → 下划线，兼容不同蓝牙栈格式）
	mac = strings.TrimSpace(mac)
	sanitized := sanitizeMAC(mac)

	if sanitized == s.currentMAC {
		// MAC 未变（可能重连同一设备），仅记录不重载
		return
	}

	// 1. 持久化旧 MAC 的配置（如果之前有 MAC）
	if s.currentMAC != "" {
		oldPath := s.filePathForMAC(s.currentMAC)
		saveJSON(oldPath, SettingsFile{Version: 1, Settings: s.settings})
		logger.Info("store", "persisted settings for MAC %s → %s", s.currentMAC, oldPath)
	}

	oldMAC := s.currentMAC
	s.currentMAC = sanitized

	// 2. 加载新 MAC 配置
	newPath := s.filePathForMAC(sanitized)
	if sanitized == "" {
		// 空 MAC → 断开，使用默认 settings.json
		loaded := loadJSON[SettingsFile](newPath, SettingsFile{Version: 1, Settings: DefaultSettings()})
		s.settings = loaded.Settings
		s.applyDefaultsLocked()
		logger.Info("store", "switched to default settings (MAC empty), loaded %s", newPath)
		return
	}

	// MAC 非空：优先加载 MAC 专属文件
	if data, err := os.ReadFile(newPath); err == nil {
		var f SettingsFile
		if json.Unmarshal(data, &f) == nil {
			s.settings = f.Settings
			s.applyDefaultsLocked()
			logger.Info("store", "loaded settings for MAC %s → %s", sanitized, newPath)
			return
		}
		logger.Warn("store", "parse %s failed, fallback to migration", newPath)
	}

	// MAC 专属文件不存在 → 从 settings.json 迁移
	fallback := loadJSON[SettingsFile](s.filePathForMAC(""), SettingsFile{Version: 1, Settings: DefaultSettings()})
	s.settings = fallback.Settings
	s.applyDefaultsLocked()
	// 先保留 LastAddress 为当前连接地址（方便后续自动重连），再落盘，
	// 否则迁移写出的 MAC 专属文件不含 LastAddress，重启后丢失上次地址。
	if oldMAC == "" && mac != "" {
		s.settings.LastAddress = mac
	}
	// 写入 MAC 专属文件，后续直接加载
	saveJSON(newPath, SettingsFile{Version: 1, Settings: s.settings})
	logger.Info("store", "migrated settings for new MAC %s from %s → %s",
		sanitized, s.filePathForMAC(""), newPath)
}

// sanitizeMAC 将 MAC 地址清洗为合法文件名组件（冒号 → 下划线，大写统一）。
// e.g. "AA:BB:CC:DD:EE:FF" → "AA_BB_CC_DD_EE_FF"
// G2: 同时校验 MAC 基本格式（6 组 2 位十六进制，含分隔符共 17 字符）。
//
//	非法输入返回空串——防止以拼接文件名方式形成路径遍历（如 "../"、绝对路径），
//	并避免把无法解析的地址当作新设备落盘。
func sanitizeMAC(mac string) string {
	mac = strings.ToUpper(strings.TrimSpace(mac))
	mac = strings.ReplaceAll(mac, ":", "_")
	if mac == "" {
		return ""
	}
	if len(mac) != 17 {
		return ""
	}
	for i := 0; i < 17; i++ {
		c := mac[i]
		if i%3 == 2 {
			if c != '_' {
				return ""
			}
		} else {
			if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'F')) {
				return ""
			}
		}
	}
	return mac
}

// filePathForMAC 返回指定 MAC 对应的 settings 文件路径。
//   - MAC 为空 → settings.json
//   - MAC 非空 → settings_<sanitizedMAC>.json
func (s *Store) filePathForMAC(mac string) string {
	sanitized := sanitizeMAC(mac)
	name := "settings.json"
	if sanitized != "" {
		name = "settings_" + sanitized + ".json"
	}
	return filepath.Join(s.dataDir, name)
}

// applyDefaultsLocked 补齐缺失字段与硬件通道上限校验（s.mu 必须已持有）。
func (s *Store) applyDefaultsLocked() {
	def := DefaultSettings()
	if s.settings.DeviceName == "" {
		s.settings.DeviceName = def.DeviceName
	}

	// 风扇配置补齐：确保至少有 MaxFanChannels 个条目
	if len(s.settings.Fans) == 0 {
		s.settings.Fans = def.Fans
	} else {
		// 校验每个风扇的曲线
		for i := range s.settings.Fans {
			f := &s.settings.Fans[i]
			// 曲线：Curves 为空时从旧 SpeedCurve 迁移（向后兼容）
			if len(f.Curves) == 0 {
				var src []CurvePoint
				if len(f.SpeedCurve) >= 2 {
					src = cloneCurve(f.SpeedCurve)
				} else {
					src = DefaultSpeedCurve()
				}
				f.Curves = map[string][]CurvePoint{
					"efficient": cloneCurve(src),
					"daily":     cloneCurve(src),
					"quiet":     cloneCurve(src),
				}
				// 同步写回 SpeedCurve（取 daily 为新的 SpeedCurve，保持双向兼容）
				f.SpeedCurve = cloneCurve(src)
				logger.Info("store", "fan %d migrated old SpeedCurve → Curves", f.ID)
			}
			// 确保每条曲线至少 2 个点
			for k, curve := range f.Curves {
				if len(curve) < 2 {
					f.Curves[k] = DefaultSpeedCurve()
				}
			}
			// ActiveCurve 未设置或非法 → 默认 daily
			if f.ActiveCurve == "" || f.Curves[f.ActiveCurve] == nil {
				f.ActiveCurve = "daily"
			}
			// FaultSpd 未设置（0 表示关闭，所以 -1 才需要默认值）
			if f.FaultSpd < 0 || f.FaultSpd > 100 {
				f.FaultSpd = 50
			}
			// TempRefs 迁移：TempRefs 为空时从旧 TempRef 迁移到 TempRefs[0]
			if len(f.TempRefs) == 0 {
				if f.TempRef != "" {
					f.TempRefs = []string{f.TempRef}
					f.TempRef = "" // 清掉旧字段，后续不再使用
					logger.Info("store", "fan %d migrated old TempRef → TempRefs", f.ID)
				}
			} else if f.TempRef != "" {
				// TempRefs 已有值但 TempRef 还残留 → 清掉
				f.TempRef = ""
			}
			if f.Mode != ModeAuto && f.Mode != ModeManual {
				f.Mode = ModeAuto
			}
			if f.TargetSpd < 0 || f.TargetSpd > 100 {
				f.TargetSpd = 30
			}
			// 同步 SpeedCurve 为当前 ActiveCurve 的值（供 thermal.go 读取，保持双向兼容）
			if active, ok := f.Curves[f.ActiveCurve]; ok {
				f.SpeedCurve = cloneCurve(active)
			}
		}
		// 通道数超限告警
		if len(s.settings.Fans) > MaxFanChannels {
			logger.Warn("store", "fans config count %d exceeds hardware max %d, truncating", len(s.settings.Fans), MaxFanChannels)
			s.settings.Fans = s.settings.Fans[:MaxFanChannels]
		}
	}

	// 硬盘组配置补齐
	if len(s.settings.DiskGroups) == 0 {
		s.settings.DiskGroups = def.DiskGroups
	} else if len(s.settings.DiskGroups) > MaxDiskGroups {
		logger.Warn("store", "diskGroups config count %d exceeds max %d, truncating", len(s.settings.DiskGroups), MaxDiskGroups)
		s.settings.DiskGroups = s.settings.DiskGroups[:MaxDiskGroups]
	}

	// 校验硬盘组 SwitchN 范围与冲突
	swUsed := make(map[int]bool)
	for i := range s.settings.DiskGroups {
		g := &s.settings.DiskGroups[i]
		// #Fix（无默认自动绑定）：过滤"空硬盘条目"（device 与 serial 均空），
		// 防止历史/异常数据残留"未绑定的硬盘"条目
		kept := g.Disks[:0]
		for _, d := range g.Disks {
			if d.Device == "" && d.Serial == "" {
				continue
			}
			kept = append(kept, d)
		}
		g.Disks = kept
		if g.SwitchN < 1 || g.SwitchN > MaxSwitchChannels {
			logger.Warn("store", "diskGroup %d switchN %d out of range, reset to %d", g.ID, g.SwitchN, g.ID)
			g.SwitchN = g.ID
		}
		if g.Enabled && swUsed[g.SwitchN] {
			logger.Warn("store", "diskGroup %d reuses switchN %d (conflict with another group)", g.ID, g.SwitchN)
		}
		swUsed[g.SwitchN] = true
	}

	// 定时计划运行参数补齐
	// 日志保留天数：仅接受 1/7/30，其余（含 0）回退默认 7
	if s.settings.ScheduleLogRetainDays != ScheduleRetainDays1 &&
		s.settings.ScheduleLogRetainDays != ScheduleRetainDays7 &&
		s.settings.ScheduleLogRetainDays != ScheduleRetainDays30 {
		s.settings.ScheduleLogRetainDays = DefaultScheduleRetainDays
	}
}

// GetSettings 返回当前设置的拷贝（线程安全）。
func (s *Store) GetSettings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// GetCurrentMAC 返回当前关联的 BLE MAC（空表示未关联）。
func (s *Store) GetCurrentMAC() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentMAC
}

// SaveSettings 保存设置到内存并持久化。
func (s *Store) SaveSettings(v Settings) {
	s.mu.Lock()
	// 会话密钥保护：deviceKeys 是上位机内部维护的安全字段（前端保存/局部更新对象不携带该字段），
	// 传入为空时从当前设置继承，防止任意设置保存操作把各 MAC 的会话密钥覆盖丢失
	// （此前前端保存设置 → 整对象替换 → deviceKeys=nil → 落盘 → 重启后"本地无会话密钥"）。
	if len(v.DeviceKeys) == 0 {
		v.DeviceKeys = s.settings.DeviceKeys
	}
	s.settings = v
	s.mu.Unlock()
	s.persistSettings()
}

// UpdateFanConfig 原子更新单路风扇配置。
func (s *Store) UpdateFanConfig(fanID int, cfg FanChannelConfig) {
	s.mu.Lock()
	for i := range s.settings.Fans {
		if s.settings.Fans[i].ID == fanID {
			s.settings.Fans[i] = cfg
			break
		}
	}
	s.mu.Unlock()
	s.persistSettings()
}

// UpdateDiskGroup 原子更新单个硬盘组配置。
func (s *Store) UpdateDiskGroup(groupID int, cfg DiskGroupConfig) {
	s.mu.Lock()
	for i := range s.settings.DiskGroups {
		if s.settings.DiskGroups[i].ID == groupID {
			s.settings.DiskGroups[i] = cfg
			break
		}
	}
	s.mu.Unlock()
	s.persistSettings()
}

func (s *Store) persistSettings() {
	s.mu.RLock()
	data := SettingsFile{Version: 1, Settings: s.settings}
	mac := s.currentMAC
	s.mu.RUnlock()
	saveJSON(s.filePathForMAC(mac), data)
}

// ===== 通用 JSON 持久化 =====

func loadJSON[T any](path string, def T) T {
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logger.Warn("store", "read %s: %v", path, err)
		}
		return def
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		logger.Warn("store", "parse %s: %v, using default", path, err)
		return def
	}
	return v
}

func saveJSON(path string, v any) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		logger.Warn("store", "marshal %s: %v", path, err)
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.Warn("store", "mkdir %s: %v", dir, err)
		return
	}
	tmpFile, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		logger.Warn("store", "create temp file in %s: %v", dir, err)
		return
	}
	tmp := tmpFile.Name()
	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		os.Remove(tmp)
		logger.Warn("store", "write tmp %s: %v", tmp, err)
		return
	}
	tmpFile.Close()
	if err := os.Rename(tmp, path); err != nil {
		logger.Warn("store", "rename %s -> %s: %v", tmp, path, err)
		os.Remove(tmp)
	}
}
