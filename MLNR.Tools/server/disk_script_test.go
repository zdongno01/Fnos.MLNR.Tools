package main

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// 执行器自定义脚本：脚本类型规范化（需求：执行器配置-执行脚本-自定义… 新增"脚本类型"下拉）
func TestNormalizeExecutorsScriptType(t *testing.T) {
	// python / shell / sh → 规范化
	execs := []Executor{{
		Type:       ExecutorExecScript,
		ScriptCode: "print('hi')",
		ScriptType: "python",
		DelaySec:   1,
	}, {
		Type:       ExecutorExecScript,
		ScriptCode: "echo hi",
		ScriptType: "sh",
		DelaySec:   1,
	}}
	if msg := normalizeExecutors(execs); msg != "" {
		t.Fatalf("normalizeExecutors(python/sh) = %q, want empty", msg)
	}
	if execs[0].ScriptType != "python" || execs[1].ScriptType != "shell" {
		t.Fatalf("scriptType normalized wrong: %q %q", execs[0].ScriptType, execs[1].ScriptType)
	}

	// 非法类型 → 清空（空=按 shebang 自动识别）
	execs = []Executor{{
		Type:       ExecutorExecScript,
		ScriptCode: "echo hi",
		ScriptType: "bat",
		DelaySec:   1,
	}}
	if msg := normalizeExecutors(execs); msg != "" {
		t.Fatalf("normalizeExecutors(bat) = %q, want empty", msg)
	}
	if execs[0].ScriptType != "" {
		t.Fatalf("scriptType(bat) = %q, want empty", execs[0].ScriptType)
	}

	// 引用资源时类型字段不生效（按资源代码/类型执行）
	execs = []Executor{{
		Type:       ExecutorExecScript,
		ScriptID:   1,
		ScriptCode: "inline-ignored",
		ScriptType: "python",
		DelaySec:   1,
	}}
	if msg := normalizeExecutors(execs); msg != "" {
		t.Fatalf("normalizeExecutors(scriptId) = %q, want empty", msg)
	}
	if execs[0].ScriptType != "" {
		t.Fatalf("scriptType(scriptId) = %q, want empty", execs[0].ScriptType)
	}

	// 非脚本执行器：类型字段被清空
	execs = []Executor{{
		Type:       ExecutorFanControl,
		FanID:      1,
		FanManual:  true,
		FanPercent: 50,
		ScriptType: "python",
		DelaySec:   1,
	}}
	if msg := normalizeExecutors(execs); msg != "" {
		t.Fatalf("normalizeExecutors(fan) = %q, want empty", msg)
	}
	if execs[0].ScriptType != "" {
		t.Fatalf("scriptType(fan) = %q, want empty", execs[0].ScriptType)
	}
}

// 硬盘组任务配置：JSON 序列化 / 反序列化（需求：硬盘组通道设置-本地配置-任务配置）
func TestDiskGroupTaskJSON(t *testing.T) {
	g := DiskGroupConfig{
		ID:     1,
		Alias:  "组1",
		OnTask: &DiskGroupTask{TaskID: 2},
		OffTask: &DiskGroupTask{TaskID: 5},
	}
	b, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{`"onTask":{"taskId":2}`, `"offTask":{"taskId":5}`} {
		if !strings.Contains(s, want) {
			t.Fatalf("marshal missing %s; got %s", want, s)
		}
	}
	var g2 DiskGroupConfig
	if err := json.Unmarshal(b, &g2); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if g2.OnTask == nil || g2.OnTask.TaskID != 2 || g2.OffTask == nil || g2.OffTask.TaskID != 5 {
		t.Fatalf("unmarshal roundtrip wrong: %+v", g2)
	}
	// 空任务字段 → omitempty 不输出
	b2, _ := json.Marshal(DiskGroupConfig{ID: 2})
	if strings.Contains(string(b2), "onTask") || strings.Contains(string(b2), "offTask") {
		t.Fatalf("empty task fields should be omitted: %s", b2)
	}
}

// fakeTaskRunner 测试用 TaskRunner（记录同步/异步触发调用并返回固定结果）。
type fakeTaskRunner struct {
	mu        sync.Mutex
	manual    []int
	manualA   []int
	manualRet string
	manualOK  bool
	asyncRet  string
	asyncOK   bool
}

func (f *fakeTaskRunner) TriggerManual(id int) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.manual = append(f.manual, id)
	return f.manualRet, f.manualOK
}

func (f *fakeTaskRunner) TriggerManualAsync(id int) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.manualA = append(f.manualA, id)
	return f.asyncRet, f.asyncOK
}

func (f *fakeTaskRunner) calls() (manual, manualA []int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.manual...), append([]int(nil), f.manualA...)
}

// runGroupTask：未配置 / 未注入 / 同步成功失败 / 异步成功失败
func TestRunGroupTask(t *testing.T) {
	d := &DiskManager{}
	// 未配置任务（nil 或 TaskID<=0）→ 跳过，无错误
	if err := d.runGroupTask(nil, true); err != nil {
		t.Fatalf("nil task should skip: %v", err)
	}
	if err := d.runGroupTask(&DiskGroupTask{}, false); err != nil {
		t.Fatalf("empty task should skip: %v", err)
	}
	// 调度引擎未注入 → 错误（下电前调用方将终止流程）
	if err := d.runGroupTask(&DiskGroupTask{TaskID: 3}, true); err == nil {
		t.Fatal("want error when scheduler not injected")
	}

	f := &fakeTaskRunner{manualOK: true, asyncOK: true}
	d.sched = f

	// 同步触发成功（下电前等待完成）
	if err := d.runGroupTask(&DiskGroupTask{TaskID: 7}, true); err != nil {
		t.Fatalf("sync ok: %v", err)
	}
	// 同步触发失败（任务执行失败）
	f.manualOK = false
	f.manualRet = "boom"
	if err := d.runGroupTask(&DiskGroupTask{TaskID: 7}, true); err == nil {
		t.Fatal("want error on sync failure")
	}
	f.manualOK = true

	// 异步触发成功（上电后不介入）
	if err := d.runGroupTask(&DiskGroupTask{TaskID: 9}, false); err != nil {
		t.Fatalf("async ok: %v", err)
	}
	// 异步触发失败（任务丢失）
	f.asyncOK = false
	f.asyncRet = "任务不存在"
	if err := d.runGroupTask(&DiskGroupTask{TaskID: 9}, false); err == nil {
		t.Fatal("want error on async failure")
	}

	m, ma := f.calls()
	if len(m) != 2 || m[0] != 7 || m[1] != 7 {
		t.Fatalf("manual calls wrong: %v", m)
	}
	if len(ma) != 2 || ma[0] != 9 || ma[1] != 9 {
		t.Fatalf("async calls wrong: %v", ma)
	}
}

// runGroupTask 同步等待超时：超过总超时上限视为失败（终止正常下电）
func TestRunGroupTaskTimeout(t *testing.T) {
	block := &blockingTaskRunner{release: make(chan struct{})}
	d := &DiskManager{sched: block, offTaskTimeout: 80 * time.Millisecond}
	start := time.Now()
	err := d.runGroupTask(&DiskGroupTask{TaskID: 7}, true)
	if err == nil {
		t.Fatal("want timeout error")
	}
	if !strings.Contains(err.Error(), "超时") {
		t.Fatalf("error should mention timeout: %v", err)
	}
	if time.Since(start) < 80*time.Millisecond {
		t.Fatalf("returned too early: %v", time.Since(start))
	}
	close(block.release)
	time.Sleep(20 * time.Millisecond) // 让被丢弃的 goroutine 收尾
}

// blockingTaskRunner 模拟下电前任务卡死（不返回），用于超时测试。
type blockingTaskRunner struct {
	release chan struct{}
}

func (b *blockingTaskRunner) TriggerManual(id int) (string, bool) {
	<-b.release
	return "done", true
}

func (b *blockingTaskRunner) TriggerManualAsync(id int) (string, bool) {
	return "", true
}
