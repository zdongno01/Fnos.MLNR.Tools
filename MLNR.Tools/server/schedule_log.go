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

// ============================================================
// 定时计划任务执行日志（ScheduleLog）存储
//
// 模式：内存缓冲（最近 scheduleLogMaxEvents 条）+ 按天 JSON 行文件
//   <dataDir>/schedule_logs/<YYYY-MM-DD>.jsonl
//
// 保留策略：仅保留最近 retainDays（1/7/30）天的文件；启动时与
// SetRetainDays 变更后执行清理，删除过期文件并重新加载内存。
// ============================================================

const (
	// scheduleLogMaxEvents 内存缓冲上限（事件保留最近 5000 条）
	scheduleLogMaxEvents = 5000
	// scheduleLogDirName 日志目录名（位于 dataDir 下）
	scheduleLogDirName = "schedule_logs"
)

var (
	scheduleLogMu    sync.RWMutex
	scheduleLogStore *ScheduleLogStore
)

// ScheduleLogStore 定时计划任务执行日志存储（单例）。
type ScheduleLogStore struct {
	mu         sync.RWMutex
	events     []ScheduleLog // 内存缓冲（时间正序）
	dataDir    string
	retainDays int
}

// InitScheduleLogStore 初始化任务日志存储（main 启动时调用一次）。
// 加载最近 retainDays 天的历史文件，并清理过期文件。
func InitScheduleLogStore(dataDir string) *ScheduleLogStore {
	scheduleLogMu.Lock()
	defer scheduleLogMu.Unlock()
	s := &ScheduleLogStore{
		events:     make([]ScheduleLog, 0, 1024),
		dataDir:    dataDir,
		retainDays: DefaultScheduleRetainDays,
	}
	s.loadRecent()
	s.Cleanup()
	scheduleLogStore = s
	return s
}

// GetScheduleLogStore 返回任务日志存储单例（未初始化返回 nil）。
func GetScheduleLogStore() *ScheduleLogStore {
	scheduleLogMu.RLock()
	defer scheduleLogMu.RUnlock()
	return scheduleLogStore
}

// logDir 返回日志目录路径。
func (s *ScheduleLogStore) logDir() string {
	return filepath.Join(s.dataDir, scheduleLogDirName)
}

// filePathFor 返回指定日期的日志文件路径。
func (s *ScheduleLogStore) filePathFor(day time.Time) string {
	return filepath.Join(s.logDir(), day.Format("2006-01-02")+".jsonl")
}

// loadRecent 加载最近 retainDays 天的历史文件到内存（启动时调用）。
func (s *ScheduleLogStore) loadRecent() {
	dir := s.logDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logger.Warn("schedule", "mkdir %s: %v", dir, err)
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		logger.Warn("schedule", "read dir %s: %v", dir, err)
		return
	}
	cutoff := time.Now().AddDate(0, 0, -s.retainDays).Format("2006-01-02")
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		day := strings.TrimSuffix(e.Name(), ".jsonl")
		// 过期文件直接删除（保留策略）
		if day < cutoff {
			_ = os.Remove(filepath.Join(dir, e.Name()))
			continue
		}
		s.loadFile(filepath.Join(dir, e.Name()))
	}
	logger.Info("schedule", "loaded %d schedule log events (retain %d days)", len(s.events), s.retainDays)
}

// loadFile 读取单个 JSON 行文件并入内存（时间正序合并）。
func (s *ScheduleLogStore) loadFile(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warn("schedule", "read %s: %v", path, err)
		}
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e ScheduleLog
		if json.Unmarshal([]byte(line), &e) == nil && !e.Time.IsZero() {
			s.events = append(s.events, e)
		}
	}
	if len(s.events) > scheduleLogMaxEvents {
		s.events = append([]ScheduleLog(nil), s.events[len(s.events)-scheduleLogMaxEvents:]...)
	}
}

// Add 追加一条任务执行日志（内存 + 当天文件）。
func (s *ScheduleLogStore) Add(evt ScheduleLog) {
	if evt.Time.IsZero() {
		evt.Time = time.Now()
	}
	if evt.Result != ScheduleResultSuccess {
		evt.Result = ScheduleResultFailed
	}
	s.mu.Lock()
	// ID 分配：现有最大 ID + 1（内存内唯一，重启后从文件重新累计）
	id := 0
	for _, e := range s.events {
		if e.ID > id {
			id = e.ID
		}
	}
	evt.ID = id + 1
	s.events = append(s.events, evt)
	if len(s.events) > scheduleLogMaxEvents {
		n := len(s.events) - scheduleLogMaxEvents
		s.events = append([]ScheduleLog(nil), s.events[n:]...)
	}
	dayFile := s.filePathFor(evt.Time)
	s.mu.Unlock()

	s.appendToFile(dayFile, evt)
	// 惰性清理：写入时顺带清理过期文件（保留策略变更后即刻生效）
	s.Cleanup()
}

// appendToFile 追加单条事件到指定文件（JSON 行格式）。
func (s *ScheduleLogStore) appendToFile(path string, evt ScheduleLog) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		logger.Warn("schedule", "mkdir %s: %v", filepath.Dir(path), err)
		return
	}
	line, err := json.Marshal(evt)
	if err != nil {
		logger.Warn("schedule", "marshal schedule log: %v", err)
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		logger.Warn("schedule", "open %s: %v", path, err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		logger.Warn("schedule", "append %s: %v", path, err)
	}
}

// SetRetainDays 更新保留天数（1/7/30），并清理过期文件。
func (s *ScheduleLogStore) SetRetainDays(days int) {
	if days != ScheduleRetainDays1 && days != ScheduleRetainDays7 && days != ScheduleRetainDays30 {
		days = DefaultScheduleRetainDays
	}
	s.mu.Lock()
	s.retainDays = days
	s.mu.Unlock()
	s.Cleanup()
}

// RetainDays 返回当前保留天数。
func (s *ScheduleLogStore) RetainDays() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.retainDays
}

// Cleanup 删除超过保留天数的日志文件（幂等，可随时调用）。
func (s *ScheduleLogStore) Cleanup() {
	s.mu.RLock()
	dir := s.logDir()
	days := s.retainDays
	s.mu.RUnlock()
	if days <= 0 {
		days = DefaultScheduleRetainDays
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -days).Format("2006-01-02")
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		day := strings.TrimSuffix(e.Name(), ".jsonl")
		if day < cutoff {
			if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
				logger.Info("schedule", "cleaned expired log file %s (retain %d days)", e.Name(), days)
			}
		}
	}
}

// ScheduleLogQueryParams 任务日志查询参数
type ScheduleLogQueryParams struct {
	TaskName     string // 任务名称模糊匹配（"" 不筛选）
	Category     string // fan / disk（"" 不筛选，保留兼容）
	ScheduleType string // 触发方式（"" 不筛选）
	TriggerType  string // 触发类型：time/log/monitor/manual（"" 不筛选）
	Result       string // success / failed（"" 不筛选）
	From         time.Time
	To           time.Time
	Page         int // 从 1 开始
	PageSize     int // 默认 20
}

// ScheduleLogQueryResult 查询结果
type ScheduleLogQueryResult struct {
	Total int64         `json:"total"`
	Page  int           `json:"page"`
	Size  int           `json:"size"`
	Items []ScheduleLog `json:"items"`
}

// Query 按参数筛选任务日志，返回时间倒序（最新在前）分页结果。
func (s *ScheduleLogStore) Query(p ScheduleLogQueryParams) ScheduleLogQueryResult {
	if p.Page < 1 {
		p.Page = 1
	}
	if p.PageSize < 1 || p.PageSize > 200 {
		p.PageSize = 20
	}
	s.mu.RLock()
	all := append([]ScheduleLog(nil), s.events...)
	s.mu.RUnlock()

	matched := make([]ScheduleLog, 0, len(all))
	for _, e := range all {
		if p.TaskName != "" && !strings.Contains(e.TaskName, p.TaskName) {
			continue
		}
		if p.Category != "" && e.Category != p.Category {
			continue
		}
		if p.ScheduleType != "" && e.ScheduleType != p.ScheduleType {
			continue
		}
		if p.TriggerType != "" && e.TriggerType != p.TriggerType {
			continue
		}
		if p.Result != "" && e.Result != p.Result {
			continue
		}
		if !p.From.IsZero() && e.Time.Before(p.From) {
			continue
		}
		if !p.To.IsZero() && e.Time.After(p.To) {
			continue
		}
		matched = append(matched, e)
	}

	// 按执行时间倒序（最新在前），不依赖存储/插入顺序
	sort.SliceStable(matched, func(i, j int) bool {
		return matched[i].Time.After(matched[j].Time)
	})

	total := int64(len(matched))
	start := (p.Page - 1) * p.PageSize
	if start > len(matched) {
		start = len(matched)
	}
	end := start + p.PageSize
	if end > len(matched) {
		end = len(matched)
	}
	return ScheduleLogQueryResult{
		Total: total,
		Page:  p.Page,
		Size:  p.PageSize,
		Items: matched[start:end],
	}
}

// scheduleLogTypeLabels 触发方式 → 显示名（供前端筛选下拉使用）。
func scheduleLogTypeLabels() map[string]string {
	return map[string]string{
		"":         "全部",
		"one_time": "一次性",
		"daily":    "每天",
		"weekly":   "每周",
		"monthly":  "每月",
		"trigger":  "触发任务",
	}
}

// scheduleLogCategoryLabels 任务类别 → 显示名（修订版不再使用类别做日志分类，保留兼容）。
func scheduleLogCategoryLabels() map[string]string {
	return map[string]string{
		"":     "全部",
		"fan":  "风扇任务",
		"disk": "硬盘任务",
	}
}

// scheduleLogTriggerTypeLabels 触发类型 → 显示名（执行日志「触发类型」筛选/列）。
func scheduleLogTriggerTypeLabels() map[string]string {
	return map[string]string{
		"":        "全部",
		"time":    "定时事件",
		"log":     "日志事件",
		"monitor": "监控事件",
		"manual":  "手动触发",
	}
}

// scheduleLogResultLabels 执行结果 → 显示名。
func scheduleLogResultLabels() map[string]string {
	return map[string]string{
		"":        "全部",
		"success": "成功",
		"failed":  "失败",
	}
}

// channelsSummary 生成执行通道摘要（风扇："FAN1,FAN2"；硬盘："组1,组2"）。
func channelsSummary(sc Schedule) string {
	if sc.Category == ScheduleCategoryFan {
		parts := make([]string, 0, len(sc.FanChannels))
		for _, ch := range sc.FanChannels {
			parts = append(parts, fmt.Sprintf("FAN%d", ch))
		}
		return strings.Join(parts, ",")
	}
	parts := make([]string, 0, len(sc.DiskGroups))
	for _, g := range sc.DiskGroups {
		parts = append(parts, fmt.Sprintf("组%d", g))
	}
	return strings.Join(parts, ",")
}
