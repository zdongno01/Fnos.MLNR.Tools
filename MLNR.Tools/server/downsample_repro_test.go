package main

import (
	"fmt"
	"testing"
	"time"
)

// TestDownsampleSparse 复现历史趋势"散点"现象：
// 后端 Query 对超过 800 点的范围做等距下采样。
// 场景 A（startMs 失效=0）：请求全缓冲（如 3 天 4320 点）→ 下采样到 800 点 → 点间隔被拉长
// 场景 B（正常 2h 窗口）：120 点 → 不触发下采样 → 保持 1 分钟间隔
func TestDownsampleSparse(t *testing.T) {
	now := time.Now().UnixMilli()
	minute := int64(60_000)

	// 模拟 3 天缓冲（4320 点，1 分钟间隔）
	full := make([]HistoryPoint, 0, 4320)
	for i := 0; i < 4320; i++ {
		full = append(full, HistoryPoint{Time: now - int64(4320-1-i)*minute, Value: 40})
	}

	// 场景 A：startMs 失效 → filtered = 全量 → downsample(4320, 800)
	ds := downsample(full, 800)
	var gaps []int64
	for i := 1; i < len(ds); i++ {
		gaps = append(gaps, (ds[i].Time-ds[i-1].Time)/minute)
	}
	if len(ds) != 800 {
		t.Errorf("downsample points = %d, want 800", len(ds))
	}
	if len(gaps) > 0 {
		fmt.Printf("场景A(全量降采样): %d 点, 相邻点间隔约 %d 分钟\n", len(ds), gaps[len(gaps)/2])
	}

	// 场景 B：正常 2h 请求 [now-2h, now]
	start := now - 2*3600*1000
	i0 := 0
	for i0 < len(full) && full[i0].Time < start {
		i0++
	}
	filtered := full[i0:]
	dsB := downsample(filtered, 800)
	if len(dsB) != len(filtered) {
		t.Errorf("场景B 不应降采样: got %d, want %d", len(dsB), len(filtered))
	}
	fmt.Printf("场景B(正常2h窗口): %d 点, 未降采样(保持1分钟间隔)\n", len(dsB))
}
