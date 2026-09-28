package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// TestGetHistoryParams 验证 getHistory 对 startMs/endMs 的解析：
// 补拉请求带 startMs/endMs 时应精确返回窗口数据（不触发全量降采样）。
func TestGetHistoryParams(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 构造一个带 3 天历史（1 分钟间隔）的 HistoryStore
	store := NewHistoryStore(t.TempDir())
	now := time.Now().UnixMilli()
	minute := int64(60_000)
	// 多写一点（5000 点）确保触发 downsample 阈值
	for i := 0; i < 5000; i++ {
		store.Append("temp:test", HistoryPoint{Time: now - int64(5000-1-i)*minute, Value: 40})
	}

	tm := &ThermalManager{history: store}
	h := &Handlers{thermal: tm}

	// 场景 1：正常补拉（带 startMs/endMs）
	startMs := now - 2*3600*1000 // 2h 前
	endMs := now - 3600*1000     // 1h 前
	req := httptest.NewRequest("GET", "/api/history?series=temp:test&startMs="+itoa(startMs)+"&endMs="+itoa(endMs), nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	h.getHistory(c)

	var resp struct {
		Points []HistoryPoint `json:"points"`
		Start  int64          `json:"start"`
		End    int64          `json:"end"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	fmt.Printf("场景1(带startMs/endMs): start=%d end=%d points=%d\n", resp.Start, resp.End, len(resp.Points))
	if len(resp.Points) != 61 {
		t.Errorf("场景1 points = %d, want 61（1小时@1分钟）", len(resp.Points))
	}
	if resp.Start != startMs {
		t.Errorf("场景1 start 回显 = %d, want %d", resp.Start, startMs)
	}

	// 场景 2：startMs 缺失（模拟参数丢失）→ 兜底应返回最近 1 小时（61 点），而非全量降采样
	req2 := httptest.NewRequest("GET", "/api/history?series=temp:test", nil)
	w2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = req2
	h.getHistory(c2)
	var resp2 struct {
		Points []HistoryPoint `json:"points"`
		Start  int64          `json:"start"`
	}
	_ = json.Unmarshal(w2.Body.Bytes(), &resp2)
	fmt.Printf("场景2(startMs缺失): points=%d start=%d\n", len(resp2.Points), resp2.Start)
	if len(resp2.Points) > 120 {
		t.Errorf("场景2 points = %d, want <=120（兜底最近1小时，不得全量降采样）", len(resp2.Points))
	}
	// 场景 3：浮点 startMs（模拟前端拖动产生的 viewMin 小数，如 1758096400000.234）
	// 修复前 ParseInt 失败 → startMs=0 → 全量降采样；修复后应容错解析为整数毫秒戳。
	fStart := float64(startMs) + 0.234
	req3 := httptest.NewRequest("GET", "/api/history?series=temp:test&startMs="+fmt.Sprintf("%.3f", fStart)+"&endMs="+itoa(endMs), nil)
	w3 := httptest.NewRecorder()
	c3, _ := gin.CreateTestContext(w3)
	c3.Request = req3
	h.getHistory(c3)
	var resp3 struct {
		Points []HistoryPoint `json:"points"`
		Start  int64          `json:"start"`
	}
	_ = json.Unmarshal(w3.Body.Bytes(), &resp3)
	fmt.Printf("场景3(浮点startMs): start=%d points=%d\n", resp3.Start, len(resp3.Points))
	if resp3.Start != startMs {
		t.Errorf("场景3 start = %d, want %d（浮点应容错取整，不得为 0）", resp3.Start, startMs)
	}
	if len(resp3.Points) != 61 {
		t.Errorf("场景3 points = %d, want 61", len(resp3.Points))
	}
}

func itoa(v int64) string { return fmt.Sprintf("%d", v) }

var _ = url.QueryEscape
var _ = strings.TrimSpace
var _ = http.StatusOK
