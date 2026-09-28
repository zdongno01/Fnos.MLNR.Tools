package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// 底部导航栏配置：JSON 往返保持 key 列表（含空数组=隐藏底部导航；字段必须可持久化）。
func TestSettingsBottomNavKeysRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		keys []string
		want []string
	}{
		{"default four", []string{"home", "fans", "disks", "settings"}, []string{"home", "fans", "disks", "settings"}},
		{"all eight", []string{"home", "fans", "disks", "sensors", "connection", "settings", "schedules", "logs"}, []string{"home", "fans", "disks", "sensors", "connection", "settings", "schedules", "logs"}},
		{"empty persists hidden", []string{}, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Settings{BottomNavKeys: c.keys}
			data, err := json.Marshal(s)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var s2 Settings
			if err := json.Unmarshal(data, &s2); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if !reflect.DeepEqual(s2.BottomNavKeys, c.want) {
				t.Fatalf("round trip = %v, want %v", s2.BottomNavKeys, c.want)
			}
		})
	}

	// 空数组必须落盘为 []（无 omitempty），否则"全部取消=隐藏"无法持久化
	var s3 Settings
	if err := json.Unmarshal([]byte(`{"bottomNavKeys":[]}`), &s3); err != nil {
		t.Fatalf("unmarshal empty: %v", err)
	}
	if s3.BottomNavKeys == nil || len(s3.BottomNavKeys) != 0 {
		t.Fatalf("empty array must persist as non-nil empty slice, got %#v", s3.BottomNavKeys)
	}
}

// normalizeBottomNavKeys：去空白 / 去重 / 限 8 项。
func TestNormalizeBottomNavKeys(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil keeps empty", nil, []string{}},
		{"trim and drop empty", []string{" home ", "", "fans"}, []string{"home", "fans"}},
		{"dedupe", []string{"home", "home", "fans", "home"}, []string{"home", "fans"}},
		{"cap at 8", []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}, []string{"a", "b", "c", "d", "e", "f", "g", "h"}},
		{"order preserved", []string{"settings", "home", "fans"}, []string{"settings", "home", "fans"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := normalizeBottomNavKeys(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("normalize(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// sanitizeSettings 不剥离 bottomNavKeys（非敏感字段，应原样对外返回）。
func TestSanitizeSettingsKeepsBottomNavKeys(t *testing.T) {
	s := Settings{BottomNavKeys: []string{"home", "fans"}}
	out := sanitizeSettings(s)
	if !reflect.DeepEqual(out.BottomNavKeys, s.BottomNavKeys) {
		t.Fatalf("sanitize dropped bottomNavKeys: %v", out.BottomNavKeys)
	}
}

// normalizeScriptType：脚本类型规范化（"shell"/"python"，其余含空串=按 shebang 识别）。
func TestNormalizeScriptType(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"shell", "shell"},
		{"sh", "shell"},
		{" SH ", "shell"},
		{"python", "python"},
		{"Python", "python"},
		{"PYTHON", "python"},
		{"bogus", ""},
		{"python3", ""},
	}
	for _, c := range cases {
		if got := normalizeScriptType(c.in); got != c.want {
			t.Fatalf("normalizeScriptType(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// validateDiskGroupTaskRefs：上电后/下电前任务引用校验
// （任务不存在或直接执行器含同组下电 → 拒绝；其余允许）
func TestValidateDiskGroupTaskRefs(t *testing.T) {
	schedules := []Schedule{
		{ID: 1, Name: "下电组1", Executors: []Executor{{Type: ExecutorDiskGroupControl, DiskGroupID: 1, DiskAction: DiskActionOffline}}},
		{ID: 2, Name: "上电组2", Executors: []Executor{{Type: ExecutorDiskGroupControl, DiskGroupID: 2, DiskAction: DiskActionOnline}}},
		{ID: 3, Name: "脚本任务", Executors: []Executor{{Type: ExecutorExecScript, ScriptID: 1}}},
	}
	cases := []struct {
		name string
		s    Settings
		want string // 期望错误子串；空=应通过
	}{
		{"无任务引用", Settings{DiskGroups: []DiskGroupConfig{{ID: 1, Alias: "g"}}}, ""},
		{"下电前引用同组下电 → 拒绝", Settings{DiskGroups: []DiskGroupConfig{{ID: 1, OffTask: &DiskGroupTask{TaskID: 1}}}}, "硬盘组 1 的下电前任务「下电组1」包含对同一硬盘组的下电操作"},
		{"上电后引用同组下电 → 拒绝", Settings{DiskGroups: []DiskGroupConfig{{ID: 1, OnTask: &DiskGroupTask{TaskID: 1}}}}, "硬盘组 1 的上电后任务「下电组1」包含对同一硬盘组的下电操作"},
		{"任务不存在 → 拒绝", Settings{DiskGroups: []DiskGroupConfig{{ID: 1, OnTask: &DiskGroupTask{TaskID: 99}}}}, "硬盘组 1 的上电后任务引用的任务 #99 不存在"},
		{"不同组下电 → 通过", Settings{DiskGroups: []DiskGroupConfig{{ID: 2, OnTask: &DiskGroupTask{TaskID: 1}}}}, ""},
		{"上电动作 → 通过", Settings{DiskGroups: []DiskGroupConfig{{ID: 1, OnTask: &DiskGroupTask{TaskID: 2}}}}, ""},
		{"脚本任务 → 通过", Settings{DiskGroups: []DiskGroupConfig{{ID: 1, OffTask: &DiskGroupTask{TaskID: 3}}}}, ""},
		{"未配置任务 → 通过", Settings{DiskGroups: []DiskGroupConfig{{ID: 1, OnTask: &DiskGroupTask{}, OffTask: &DiskGroupTask{}}}}, ""},
	}
	for _, c := range cases {
		got := validateDiskGroupTaskRefs(&c.s, schedules)
		if c.want == "" && got != "" {
			t.Fatalf("%s: want pass, got error %q", c.name, got)
		}
		if c.want != "" && !strings.Contains(got, c.want) {
			t.Fatalf("%s: error %q should contain %q", c.name, got, c.want)
		}
	}
}
