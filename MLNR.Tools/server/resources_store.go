package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mlnr/logger"
)

// ===== 资源文件布局 =====
//
// 资源（定时计划 / 监控脚本 / 执行脚本）不跟随 BLE MAC 隔离，
// 存储在 dataDir 顶层：
//
//	dataDir/
//	  resources.json             ← schedules + logEvents
//	  monitor-scripts.json       ← monitorScripts 元数据（不含 code）
//	  exec-scripts.json          ← execScripts 元数据（不含 code）
//	  scripts/
//	    monitor-<id>.sh          ← 监控脚本正文（纯文本）
//	    exec-<id>.sh             ← 执行脚本正文（纯文本）
//
// 说明：
//   - MonitorScript.Code / ExecScript.Code 在 JSON 里为空字符串，正文单独存 .sh
//   - 内联脚本 Trigger.MonitorCode / Executor.ScriptCode 保留在 Schedule 里（随任务走）
//   - 日志事件作为全局资源（页签 CRUD，触发器按 LogEventID 引用；也支持触发器内联配置）
//   - 所有资源变更通过 Store 加锁原子读写，持久化用 write-temp-then-rename

// ResourcesFile resources.json 的顶层结构。
type ResourcesFile struct {
	Version   int        `json:"version"`
	Schedules []Schedule `json:"schedules"`
	LogEvents []LogEvent `json:"logEvents"`
}

// MonitorScriptsFile monitor-scripts.json 的顶层结构（Code 不序列化）。
type MonitorScriptsFile struct {
	Version        int             `json:"version"`
	MonitorScripts []MonitorScript `json:"monitorScripts"`
}

// ExecScriptsFile exec-scripts.json 的顶层结构（Code 不序列化）。
type ExecScriptsFile struct {
	Version     int          `json:"version"`
	ExecScripts []ExecScript `json:"execScripts"`
}

const (
	resourcesFileName       = "resources.json"
	monitorScriptsFileName  = "monitor-scripts.json"
	execScriptsFileName     = "exec-scripts.json"
	scriptsDirName          = "scripts"
	monitorScriptFilePrefix = "monitor-"
	execScriptFilePrefix    = "exec-"
)

// ===== 文件路径助手 =====

func (s *Store) scriptsDir() string { return filepath.Join(s.dataDir, scriptsDirName) }

func (s *Store) resourcesPath() string      { return filepath.Join(s.dataDir, resourcesFileName) }
func (s *Store) monitorScriptsPath() string { return filepath.Join(s.dataDir, monitorScriptsFileName) }
func (s *Store) execScriptsPath() string    { return filepath.Join(s.dataDir, execScriptsFileName) }

// monitorScriptFilePath 返回指定 ID 的监控脚本正文路径。
func (s *Store) monitorScriptFilePath(id int) string {
	return filepath.Join(s.scriptsDir(), fmt.Sprintf("%s%d.sh", monitorScriptFilePrefix, id))
}

// execScriptFilePath 返回指定 ID 的执行脚本正文路径。
func (s *Store) execScriptFilePath(id int) string {
	return filepath.Join(s.scriptsDir(), fmt.Sprintf("%s%d.sh", execScriptFilePrefix, id))
}

// ===== 资源加载（Load 时调用一次）=====

func (s *Store) loadResources() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 确保 scripts 目录存在
	if err := os.MkdirAll(s.scriptsDir(), 0o755); err != nil {
		logger.Warn("store", "mkdir scripts dir: %v", err)
	}

	// resources.json
	res := loadJSON[ResourcesFile](s.resourcesPath(), ResourcesFile{Version: 1})
	s.schedules = res.Schedules
	if s.schedules == nil {
		s.schedules = []Schedule{}
	}
	s.logEvents = res.LogEvents
	if s.logEvents == nil {
		s.logEvents = []LogEvent{}
	}

	// monitor-scripts.json（Code 字段从 JSON 读出来是空的，正文按需读 .sh）
	msf := loadJSON[MonitorScriptsFile](s.monitorScriptsPath(), MonitorScriptsFile{Version: 1})
	s.monitorScripts = msf.MonitorScripts
	if s.monitorScripts == nil {
		s.monitorScripts = []MonitorScript{}
	}

	// exec-scripts.json
	esf := loadJSON[ExecScriptsFile](s.execScriptsPath(), ExecScriptsFile{Version: 1})
	s.execScripts = esf.ExecScripts
	if s.execScripts == nil {
		s.execScripts = []ExecScript{}
	}

	// 首次启动填充预制资源（幂等：有预制就跳过）
	s.seedPrebuiltIfMissing()

	logger.Info("store", "loaded resources: %d schedules, %d log events, %d monitor scripts, %d exec scripts",
		len(s.schedules), len(s.logEvents), len(s.monitorScripts), len(s.execScripts))
}

// ===== 资源持久化 =====

// persistResources 持久化所有资源到各自的 JSON 文件（不含脚本正文）。
func (s *Store) persistResources() {
	res := ResourcesFile{Version: 1, Schedules: s.schedules, LogEvents: s.logEvents}
	saveJSON(s.resourcesPath(), res)
}

// persistMonitorScripts 持久化监控脚本元数据（Code 清空，正文在 .sh）。
func (s *Store) persistMonitorScripts() {
	out := make([]MonitorScript, len(s.monitorScripts))
	for i, ms := range s.monitorScripts {
		ms.Code = "" // 确保不把正文写进 JSON
		out[i] = ms
	}
	saveJSON(s.monitorScriptsPath(), MonitorScriptsFile{Version: 1, MonitorScripts: out})
}

// persistExecScripts 持久化执行脚本元数据（Code 清空，正文在 .sh）。
func (s *Store) persistExecScripts() {
	out := make([]ExecScript, len(s.execScripts))
	for i, es := range s.execScripts {
		es.Code = ""
		out[i] = es
	}
	saveJSON(s.execScriptsPath(), ExecScriptsFile{Version: 1, ExecScripts: out})
}

// ===== 调度计划（schedules）=====

// GetSchedules 返回定时计划任务列表。
func (s *Store) GetSchedules() []Schedule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneSchedules(s.schedules)
}

// SaveSchedules 原子替换定时计划任务列表。
func (s *Store) SaveSchedules(list []Schedule) {
	s.mu.Lock()
	s.schedules = cloneSchedules(list)
	s.mu.Unlock()
	s.persistResources()
}

// AddSchedule 新增定时计划任务并分配自增 ID。
func (s *Store) AddSchedule(sc Schedule) int {
	s.mu.Lock()
	id := 0
	for _, e := range s.schedules {
		if e.ID > id {
			id = e.ID
		}
	}
	id++
	sc.ID = id
	now := time.Now()
	if sc.CreatedAt.IsZero() {
		sc.CreatedAt = now
	}
	sc.UpdatedAt = now
	s.schedules = append(s.schedules, sc)
	s.mu.Unlock()
	s.persistResources()
	return id
}

// UpdateSchedule 原子更新指定任务；不存在返回 false。
func (s *Store) UpdateSchedule(id int, sc Schedule) bool {
	s.mu.Lock()
	found := false
	for i := range s.schedules {
		if s.schedules[i].ID == id {
			sc.ID = id
			sc.CreatedAt = s.schedules[i].CreatedAt
			sc.UpdatedAt = time.Now()
			s.schedules[i] = sc
			found = true
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return false
	}
	s.persistResources()
	return true
}

// DeleteSchedule 删除指定任务；不存在返回 false。
func (s *Store) DeleteSchedule(id int) bool {
	s.mu.Lock()
	out := s.schedules[:0]
	found := false
	for _, e := range s.schedules {
		if e.ID == id {
			found = true
			continue
		}
		out = append(out, e)
	}
	s.schedules = out
	s.mu.Unlock()
	return found
}

// ===== 日志事件（logEvents）=====

// GetLogEvents 返回日志事件列表。
func (s *Store) GetLogEvents() []LogEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]LogEvent, len(s.logEvents))
	copy(out, s.logEvents)
	return out
}

// GetLogEventByID 返回指定日志事件；不存在返回空结构。
func (s *Store) GetLogEventByID(id int) LogEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ev := range s.logEvents {
		if ev.ID == id {
			return ev
		}
	}
	return LogEvent{}
}

// AddLogEvent 新增日志事件并分配自增 ID。
func (s *Store) AddLogEvent(ev LogEvent) int {
	s.mu.Lock()
	id := 0
	for _, e := range s.logEvents {
		if e.ID > id {
			id = e.ID
		}
	}
	id++
	ev.ID = id
	now := time.Now()
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = now
	}
	ev.UpdatedAt = now
	s.logEvents = append(s.logEvents, ev)
	s.mu.Unlock()
	s.persistResources()
	return id
}

// UpdateLogEvent 原子更新指定日志事件；不存在返回 false。
func (s *Store) UpdateLogEvent(id int, ev LogEvent) bool {
	s.mu.Lock()
	found := false
	for i := range s.logEvents {
		if s.logEvents[i].ID == id {
			ev.ID = id
			ev.CreatedAt = s.logEvents[i].CreatedAt
			ev.UpdatedAt = time.Now()
			s.logEvents[i] = ev
			found = true
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return false
	}
	s.persistResources()
	return true
}

// DeleteLogEvent 删除指定日志事件；不存在返回 false。
func (s *Store) DeleteLogEvent(id int) bool {
	s.mu.Lock()
	out := s.logEvents[:0]
	found := false
	for _, e := range s.logEvents {
		if e.ID == id {
			found = true
			continue
		}
		out = append(out, e)
	}
	s.logEvents = out
	s.mu.Unlock()
	return found
}

// ===== 监控脚本（monitorScripts）=====

// GetMonitorScripts 返回监控脚本元数据列表（不含 Code 正文）。
func (s *Store) GetMonitorScripts() []MonitorScript {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]MonitorScript, len(s.monitorScripts))
	copy(out, s.monitorScripts)
	return out
}

// GetMonitorScriptsWithCode 返回含完整脚本正文的监控脚本列表（供 handler API 使用）。
func (s *Store) GetMonitorScriptsWithCode() []MonitorScript {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]MonitorScript, len(s.monitorScripts))
	for i, ms := range s.monitorScripts {
		code, err := os.ReadFile(s.monitorScriptFilePath(ms.ID))
		if err != nil {
			logger.Warn("store", "read monitor script code #%d: %v", ms.ID, err)
		}
		ms.Code = string(code)
		out[i] = ms
	}
	return out
}

// GetMonitorScriptCode 读取指定监控脚本的正文（从 .sh 文件）。
func (s *Store) GetMonitorScriptCode(id int) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readScriptFile(s.monitorScriptFilePath(id))
}

// GetMonitorScriptByID 返回含完整正文的单个监控脚本。
func (s *Store) GetMonitorScriptByID(id int) MonitorScript {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ms := range s.monitorScripts {
		if ms.ID == id {
			code, err := os.ReadFile(s.monitorScriptFilePath(id))
			if err != nil {
				logger.Warn("store", "read monitor script code #%d: %v", id, err)
			}
			ms.Code = string(code)
			return ms
		}
	}
	return MonitorScript{}
}

// AddMonitorScript 新增监控脚本并分配自增 ID；code 存入 .sh 文件。
func (s *Store) AddMonitorScript(ms MonitorScript, code string) int {
	s.mu.Lock()
	id := 0
	for _, e := range s.monitorScripts {
		if e.ID > id {
			id = e.ID
		}
	}
	id++
	ms.ID = id
	now := time.Now()
	if ms.CreatedAt.IsZero() {
		ms.CreatedAt = now
	}
	ms.UpdatedAt = now
	ms.Code = "" // JSON 元数据不含正文
	s.monitorScripts = append(s.monitorScripts, ms)
	s.mu.Unlock()

	s.persistMonitorScripts()
	if err := s.writeScriptFile(s.monitorScriptFilePath(id), code); err != nil {
		logger.Warn("store", "write monitor script code #%d: %v", id, err)
	}
	return id
}

// UpdateMonitorScript 原子更新指定监控脚本的元数据和正文；不存在返回 false。
// 如果 code == "" 则不更新正文（只改元数据）。
func (s *Store) UpdateMonitorScript(id int, ms MonitorScript, code string) bool {
	s.mu.Lock()
	found := false
	for i := range s.monitorScripts {
		if s.monitorScripts[i].ID == id {
			ms.ID = id
			ms.CreatedAt = s.monitorScripts[i].CreatedAt
			ms.UpdatedAt = time.Now()
			ms.Code = ""
			s.monitorScripts[i] = ms
			found = true
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return false
	}

	s.persistMonitorScripts()
	if code != "" {
		if err := s.writeScriptFile(s.monitorScriptFilePath(id), code); err != nil {
			logger.Warn("store", "update monitor script code #%d: %v", id, err)
		}
	}
	return true
}

// DeleteMonitorScript 删除指定监控脚本（同时清理 .sh 正文文件）；不存在返回 false。
func (s *Store) DeleteMonitorScript(id int) bool {
	s.mu.Lock()
	out := s.monitorScripts[:0]
	found := false
	for _, e := range s.monitorScripts {
		if e.ID == id {
			found = true
			continue
		}
		out = append(out, e)
	}
	s.monitorScripts = out
	s.mu.Unlock()
	if !found {
		return false
	}

	s.persistMonitorScripts()
	if err := os.Remove(s.monitorScriptFilePath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Warn("store", "remove monitor script code #%d: %v", id, err)
	}
	return true
}

// ===== 执行脚本（execScripts）=====

// GetExecScripts 返回执行脚本元数据列表（不含 Code 正文）。
func (s *Store) GetExecScripts() []ExecScript {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ExecScript, len(s.execScripts))
	copy(out, s.execScripts)
	return out
}

// GetExecScriptsWithCode 返回含完整脚本正文的执行脚本列表（供 handler API 使用）。
func (s *Store) GetExecScriptsWithCode() []ExecScript {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ExecScript, len(s.execScripts))
	for i, es := range s.execScripts {
		code, err := os.ReadFile(s.execScriptFilePath(es.ID))
		if err != nil {
			logger.Warn("store", "read exec script code #%d: %v", es.ID, err)
		}
		es.Code = string(code)
		out[i] = es
	}
	return out
}

// GetExecScriptCode 读取指定执行脚本的正文（从 .sh 文件）。
func (s *Store) GetExecScriptCode(id int) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readScriptFile(s.execScriptFilePath(id))
}

// GetExecScriptByID 返回含完整正文的单个执行脚本。
func (s *Store) GetExecScriptByID(id int) ExecScript {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, es := range s.execScripts {
		if es.ID == id {
			code, err := os.ReadFile(s.execScriptFilePath(id))
			if err != nil {
				logger.Warn("store", "read exec script code #%d: %v", id, err)
			}
			es.Code = string(code)
			return es
		}
	}
	return ExecScript{}
}

// AddExecScript 新增执行脚本并分配自增 ID；code 存入 .sh 文件。
func (s *Store) AddExecScript(es ExecScript, code string) int {
	s.mu.Lock()
	id := 0
	for _, e := range s.execScripts {
		if e.ID > id {
			id = e.ID
		}
	}
	id++
	es.ID = id
	now := time.Now()
	if es.CreatedAt.IsZero() {
		es.CreatedAt = now
	}
	es.UpdatedAt = now
	es.Code = ""
	s.execScripts = append(s.execScripts, es)
	s.mu.Unlock()

	s.persistExecScripts()
	if err := s.writeScriptFile(s.execScriptFilePath(id), code); err != nil {
		logger.Warn("store", "write exec script code #%d: %v", id, err)
	}
	return id
}

// UpdateExecScript 原子更新指定执行脚本的元数据和正文；不存在返回 false。
func (s *Store) UpdateExecScript(id int, es ExecScript, code string) bool {
	s.mu.Lock()
	found := false
	for i := range s.execScripts {
		if s.execScripts[i].ID == id {
			es.ID = id
			es.CreatedAt = s.execScripts[i].CreatedAt
			es.UpdatedAt = time.Now()
			es.Code = ""
			s.execScripts[i] = es
			found = true
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return false
	}

	s.persistExecScripts()
	if code != "" {
		if err := s.writeScriptFile(s.execScriptFilePath(id), code); err != nil {
			logger.Warn("store", "update exec script code #%d: %v", id, err)
		}
	}
	return true
}

// DeleteExecScript 删除指定执行脚本（同时清理 .sh 正文文件）；不存在返回 false。
func (s *Store) DeleteExecScript(id int) bool {
	s.mu.Lock()
	out := s.execScripts[:0]
	found := false
	for _, e := range s.execScripts {
		if e.ID == id {
			found = true
			continue
		}
		out = append(out, e)
	}
	s.execScripts = out
	s.mu.Unlock()
	if !found {
		return false
	}

	s.persistExecScripts()
	if err := os.Remove(s.execScriptFilePath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Warn("store", "remove exec script code #%d: %v", id, err)
	}
	return true
}

// ===== 脚本正文文件 I/O =====

func (s *Store) readScriptFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *Store) writeScriptFile(path, code string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// write-temp-then-rename 原子写入
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(code); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// ===== 工具 =====

func cloneSchedules(src []Schedule) []Schedule {
	if src == nil {
		return []Schedule{}
	}
	out := make([]Schedule, len(src))
	copy(out, src)
	// deep copy slices inside Schedule
	for i := range out {
		if out[i].FanChannels != nil {
			out[i].FanChannels = append([]int(nil), out[i].FanChannels...)
		}
		if out[i].DiskGroups != nil {
			out[i].DiskGroups = append([]int(nil), out[i].DiskGroups...)
		}
		if out[i].Weekdays != nil {
			out[i].Weekdays = append([]int(nil), out[i].Weekdays...)
		}
		if out[i].MonthDays != nil {
			out[i].MonthDays = append([]int(nil), out[i].MonthDays...)
		}
		if out[i].Triggers != nil {
			out[i].Triggers = append([]Trigger(nil), out[i].Triggers...)
		}
		if out[i].Executors != nil {
			out[i].Executors = append([]Executor(nil), out[i].Executors...)
		}
	}
	return out
}

// sanitizeScriptFileName 保护脚本文件名，拒绝路径分隔符和非法字符。
// （理论上 ID 是 int 不会出问题，但防御性检查）
func sanitizeScriptFileName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "..", "_")
	return name
}
