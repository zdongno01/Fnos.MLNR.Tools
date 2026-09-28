package main

import (
	"errors"
	"mlnr/logger"
	"os"
	"time"
)

// ============================================================
// 预制资源初始化（仅首次启动时填充）
//
// 规则：仅当对应资源列表为空时才填充，之后与用户创建的资源完全等同——
// 没有"预制"标记，可以自由编辑和删除。幂等。
//
// 填充内容请在下面 prebuiltMonitorScripts / prebuiltExecScripts 变量中修改。
// 重置方式：删除 dataDir 下的资源 JSON 和 scripts/ 目录即可重新 seed。
// ============================================================

// ---- 监控脚本预制 ----

var prebuiltMonitorScripts = []struct {
	Name string
	Code string // 完整 sh 脚本（写进 scripts/monitor-<id>.sh）
}{
	{
		Name: "监控硬盘AB闲置（预制）",
		Code: prebuiltMonitorIdleCode,
	},
}

// ---- 执行脚本预制 ----

var prebuiltExecScripts = []struct {
	Name string
	Code string
}{
	// 执行脚本暂无预制条目（留空）
}

// ---- Store 层：首次填充 ----

// seedPrebuiltIfMissing 在 loadResources 末尾调用：
// 仅当对应的 JSON 资源文件**不存在**时才写入预制元数据 + 创建 .sh 正文。
// 一旦文件被创建，后续启动永远跳过——不管列表是否被用户清空过。
func (s *Store) seedPrebuiltIfMissing() {
	now := time.Now()

	// 监控脚本
	if _, err := os.Stat(s.monitorScriptsPath()); errors.Is(err, os.ErrNotExist) {
		id := 1
		for _, p := range prebuiltMonitorScripts {
			ms := MonitorScript{
				ID:        id,
				Name:      p.Name,
				CreatedAt: now,
				UpdatedAt: now,
			}
			s.monitorScripts = append(s.monitorScripts, ms)
			id++
		}
		s.persistMonitorScripts()
		for i, p := range prebuiltMonitorScripts {
			if err := s.writeScriptFile(s.monitorScriptFilePath(s.monitorScripts[i].ID), p.Code); err != nil {
				logger.Warn("store", "seed prebuilt monitor script #%d: %v", s.monitorScripts[i].ID, err)
			}
		}
		logger.Info("store", "seeded %d prebuilt monitor scripts", len(prebuiltMonitorScripts))
	}

	// 执行脚本
	if _, err := os.Stat(s.execScriptsPath()); errors.Is(err, os.ErrNotExist) {
		id := 1
		for _, p := range prebuiltExecScripts {
			es := ExecScript{
				ID:        id,
				Name:      p.Name,
				CreatedAt: now,
				UpdatedAt: now,
			}
			s.execScripts = append(s.execScripts, es)
			id++
		}
		s.persistExecScripts()
		for i, p := range prebuiltExecScripts {
			if err := s.writeScriptFile(s.execScriptFilePath(s.execScripts[i].ID), p.Code); err != nil {
				logger.Warn("store", "seed prebuilt exec script #%d: %v", s.execScripts[i].ID, err)
			}
		}
		logger.Info("store", "seeded %d prebuilt exec scripts", len(prebuiltExecScripts))
	}
}
