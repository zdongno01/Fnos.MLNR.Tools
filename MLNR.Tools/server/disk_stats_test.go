package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// statsTestDay 返回“今天 12:00”作为测试事件基准日。
// 不用硬编码日期：7/30 天窗口聚合按 time.Now() 的“今天”滚动，
// 硬编码日期会随时间滑出窗口导致断言失败（如 switch7d=0）。
// 取当天 12:00 使事件流（基准日 +0~7h）稳定落在窗口内，不跨天。
func statsTestDay() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
}

// TestDiskStatsAggregation 验证统计聚合：累计开关/在线时长/平均单次/强制下线/7-30天窗口
func TestDiskStatsAggregation(t *testing.T) {
	dir := t.TempDir()
	l := NewDiskLogger(dir)

	day := statsTestDay()

	// 事件流：on → off（2 小时）→ on（强制下线）→ on → off（30 分钟）
	l.Add(DiskLogEvent{Time: day, GroupID: 1, Alias: "G1", Action: DiskLogPowerOn, Result: "ok"})
	l.Add(DiskLogEvent{Time: day.Add(2 * time.Hour), GroupID: 1, Alias: "G1", Action: DiskLogPowerOff, Result: "ok", Remark: "按钮关闭"})
	l.Add(DiskLogEvent{Time: day.Add(3 * time.Hour), GroupID: 1, Alias: "G1", Action: DiskLogPowerOn, Result: "ok"})
	l.Add(DiskLogEvent{Time: day.Add(4 * time.Hour), GroupID: 1, Alias: "G1", Action: DiskLogPowerOff, Result: "ok", Remark: "强制下线"})
	l.Add(DiskLogEvent{Time: day.Add(5 * time.Hour), GroupID: 1, Alias: "G1", Action: DiskLogPowerOn, Result: "ok"})
	l.Add(DiskLogEvent{Time: day.Add(5*time.Hour + 30*time.Minute), GroupID: 1, Alias: "G1", Action: DiskLogPowerOff, Result: "ok"})

	all := []string{"onlineMinutes", "totalOnline", "avgOnline", "switch7d", "switch30d", "totalSwitch", "forceOff"}
	s := l.GroupStats(1, "G1", false, time.Time{}, all)

	if s.TotalSwitchCount != 3 { // min(on=3, off=3)
		t.Errorf("TotalSwitchCount = %d, want 3", s.TotalSwitchCount)
	}
	if s.TotalOnlineMinutes != 210 { // 120 + 60 + 30 = 210 分钟
		t.Errorf("TotalOnlineMinutes = %d, want 210", s.TotalOnlineMinutes)
	}
	if s.AvgOnlineMinutes != 70 { // 210 / 3
		t.Errorf("AvgOnlineMinutes = %d, want 70", s.AvgOnlineMinutes)
	}
	if s.ForceOffCount != 1 {
		t.Errorf("ForceOffCount = %d, want 1", s.ForceOffCount)
	}
	if s.SwitchCount7d != 3 || s.SwitchCount30d != 3 {
		t.Errorf("switch windows = 7d:%d 30d:%d, want 3/3", s.SwitchCount7d, s.SwitchCount30d)
	}

	// 保存快照并重新加载：值保持不翻倍（增量补算正确性）
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	l.SaveStats()
	l2 := NewDiskLogger(dir)
	s2 := l2.GroupStats(1, "G1", false, time.Time{}, all)
	if s2.TotalSwitchCount != 3 || s2.TotalOnlineMinutes != 210 || s2.ForceOffCount != 1 {
		t.Errorf("after reload: switch=%d online=%d force=%d, want 3/210/1",
			s2.TotalSwitchCount, s2.TotalOnlineMinutes, s2.ForceOffCount)
	}

	// 增量：重新加载后新事件只加一次
	l2.Add(DiskLogEvent{Time: day.Add(6 * time.Hour), GroupID: 1, Alias: "G1", Action: DiskLogPowerOn, Result: "ok"})
	l2.Add(DiskLogEvent{Time: day.Add(7 * time.Hour), GroupID: 1, Alias: "G1", Action: DiskLogPowerOff, Result: "ok"})
	s3 := l2.GroupStats(1, "G1", false, time.Time{}, all)
	if s3.TotalSwitchCount != 4 || s3.TotalOnlineMinutes != 270 { // 210 + 60
		t.Errorf("after incremental: switch=%d online=%d, want 4/270", s3.TotalSwitchCount, s3.TotalOnlineMinutes)
	}
}

// TestDiskStatsEnabledFilter 验证按需计算：未启用组件不计算
func TestDiskStatsEnabledFilter(t *testing.T) {
	dir := t.TempDir()
	l := NewDiskLogger(dir)
	day := statsTestDay()
	l.Add(DiskLogEvent{Time: day, GroupID: 2, Action: DiskLogPowerOn, Result: "ok"})
	l.Add(DiskLogEvent{Time: day.Add(1 * time.Hour), GroupID: 2, Action: DiskLogPowerOff, Result: "ok"})

	// 只启用 forceOff：其它字段应保持零值
	s := l.GroupStats(2, "G2", false, time.Time{}, []string{"forceOff"})
	if s.ForceOffCount != 0 {
		t.Errorf("ForceOffCount = %d, want 0", s.ForceOffCount)
	}
	if s.SwitchCount7d != 0 || s.TotalSwitchCount != 0 || s.TotalOnlineMinutes != 0 {
		t.Errorf("disabled fields computed: 7d=%d total=%d online=%d", s.SwitchCount7d, s.TotalSwitchCount, s.TotalOnlineMinutes)
	}

	// 全启用（空列表 = 全部）
	s2 := l.GroupStats(2, "G2", false, time.Time{}, nil)
	if s2.TotalSwitchCount != 1 || s2.TotalOnlineMinutes != 60 {
		t.Errorf("all enabled: total=%d online=%d, want 1/60", s2.TotalSwitchCount, s2.TotalOnlineMinutes)
	}
}

var _ = filepath.Join // keep import when trimmed

// TestDiskStatsDupOnClosed 双ON场景（整机断电重启，两条上线无下线）：
// 旧会话按新上线时刻虚拟闭合（时长并入、补一次下线计数），新上线正常计数。
func TestDiskStatsDupOnClosed(t *testing.T) {
	dir := t.TempDir()
	l := NewDiskLogger(dir)
	day := statsTestDay()

	// on(A) → 断电重启 on(B)（间隔 1h ≥ 30s 判定为双ON）→ off(B)（2h 后）
	l.Add(DiskLogEvent{Time: day, GroupID: 1, Action: DiskLogPowerOn, Result: "ok"})
	l.Add(DiskLogEvent{Time: day.Add(1 * time.Hour), GroupID: 1, Action: DiskLogPowerOn, Result: "ok"})
	l.Add(DiskLogEvent{Time: day.Add(3 * time.Hour), GroupID: 1, Action: DiskLogPowerOff, Result: "ok"})

	all := []string{"onlineMinutes", "totalOnline", "avgOnline", "switch7d", "switch30d", "totalSwitch", "forceOff"}
	s := l.GroupStats(1, "G1", false, time.Time{}, all)

	// 物理：上电 2 次（A、B）、断电 2 次（虚拟 A 闭合 + 真实 B 下线）→ 2 次开关
	if s.TotalSwitchCount != 2 {
		t.Errorf("TotalSwitchCount = %d, want 2", s.TotalSwitchCount)
	}
	// 在线时长：A 1h（虚拟闭合）+ B 2h = 180 分钟
	if s.TotalOnlineMinutes != 180 {
		t.Errorf("TotalOnlineMinutes = %d, want 180", s.TotalOnlineMinutes)
	}
	if s.AvgOnlineMinutes != 90 { // 180 / 2
		t.Errorf("AvgOnlineMinutes = %d, want 90", s.AvgOnlineMinutes)
	}
	// 快照重载后不翻倍
	l.SaveStats()
	l2 := NewDiskLogger(dir)
	s2 := l2.GroupStats(1, "G1", false, time.Time{}, all)
	if s2.TotalSwitchCount != 2 || s2.TotalOnlineMinutes != 180 {
		t.Errorf("after reload: switch=%d online=%d, want 2/180", s2.TotalSwitchCount, s2.TotalOnlineMinutes)
	}
}

// TestDiskStatsDupOnFiltered 重复 ON 报文（间隔 <30s，蓝牙/固件抖动）：
// 不计数、不闭合，会话起点保留原值（本次在线时长不被缩短）。
func TestDiskStatsDupOnFiltered(t *testing.T) {
	dir := t.TempDir()
	l := NewDiskLogger(dir)
	day := statsTestDay()

	l.Add(DiskLogEvent{Time: day, GroupID: 1, Action: DiskLogPowerOn, Result: "ok"})
	l.Add(DiskLogEvent{Time: day.Add(10 * time.Second), GroupID: 1, Action: DiskLogPowerOn, Result: "ok"}) // 重复抖动
	l.Add(DiskLogEvent{Time: day.Add(1 * time.Hour), GroupID: 1, Action: DiskLogPowerOff, Result: "ok"})

	all := []string{"totalOnline", "avgOnline", "totalSwitch"}
	s := l.GroupStats(1, "G1", false, time.Time{}, all)
	if s.TotalSwitchCount != 1 { // 重复 ON 未计数
		t.Errorf("TotalSwitchCount = %d, want 1", s.TotalSwitchCount)
	}
	if s.TotalOnlineMinutes != 60 { // 起点保留 t0，时长 1h 不被缩短
		t.Errorf("TotalOnlineMinutes = %d, want 60", s.TotalOnlineMinutes)
	}
}

// TestDiskStatsPendingClose 固件离线但日志存在未闭合会话（断电未恢复）：
// ClosePendingSession 以给定时刻虚拟闭合，时长并入总在线。
func TestDiskStatsPendingClose(t *testing.T) {
	dir := t.TempDir()
	l := NewDiskLogger(dir)
	day := statsTestDay()

	l.Add(DiskLogEvent{Time: day, GroupID: 1, Action: DiskLogPowerOn, Result: "ok"})
	if !l.ClosePendingSession(1, day.Add(2*time.Hour)) {
		t.Fatal("ClosePendingSession returned false, want true")
	}
	// 幂等：再次闭合应无操作
	if l.ClosePendingSession(1, day.Add(3*time.Hour)) {
		t.Fatal("ClosePendingSession second call returned true, want false (idempotent)")
	}

	all := []string{"totalOnline", "avgOnline", "totalSwitch"}
	s := l.GroupStats(1, "G1", false, time.Time{}, all)
	if s.TotalOnlineMinutes != 120 {
		t.Errorf("TotalOnlineMinutes = %d, want 120", s.TotalOnlineMinutes)
	}
	if s.TotalSwitchCount != 1 { // on(1) + 虚拟off(1)
		t.Errorf("TotalSwitchCount = %d, want 1", s.TotalSwitchCount)
	}
	if s.AvgOnlineMinutes != 120 {
		t.Errorf("AvgOnlineMinutes = %d, want 120", s.AvgOnlineMinutes)
	}
}
