package main

import "testing"

func TestResolveHddSerial(t *testing.T) {
	snap := ThermalSnapshot{Temps: []TemperatureReading{
		{ID: "hdd_sdb_temp1", Category: "hdd", Serial: "TSC3AN128E2-F1T50S"},
		{ID: "hdd_TSC3AN128E2-F1T50S_temp1", Category: "hdd", Serial: "TSC3AN128E2-F1T50S"},
	}}
	disks := []lsblkDisk{{Name: "sda", Serial: "WDC123"}}

	cases := []struct {
		refID string
		want  string
	}{
		{"hdd_sdb_temp1", "TSC3AN128E2-F1T50S"}, // 旧设备名 ID → 快照匹配真实 SN
		{"hdd_TSC3AN128E2-F1T50S_temp1", "TSC3AN128E2-F1T50S"}, // 新 SN ID → 原样
		{"hdd_sda_temp1", "WDC123"},                            // 设备名 → 磁盘缓存映射 SN
		{"hdd_unknown_temp1", "unknown"},                       // 无法解析 → 回退 key
		{"cpu_package_0", ""},                                  // 非磁盘参考
	}
	for _, c := range cases {
		if got := resolveHddSerial(c.refID, snap, disks); got != c.want {
			t.Errorf("resolveHddSerial(%q) = %q, want %q", c.refID, got, c.want)
		}
	}
}

func TestLogFanFaultOnce(t *testing.T) {
	tm := &ThermalManager{faultLogged: make(map[int]string)}

	if !tm.logFanFaultOnce(1, "skip-disk-ABC") {
		t.Fatal("first record should log")
	}
	if tm.logFanFaultOnce(1, "skip-disk-ABC") {
		t.Fatal("same state must not log again")
	}
	if !tm.logFanFaultOnce(1, "fault") {
		t.Fatal("state change should log")
	}
	tm.logFanFaultOnce(1, "") // 恢复正常
	if !tm.logFanFaultOnce(1, "skip-disk-ABC") {
		t.Fatal("after recovery the same state should log once again")
	}
	if tm.logFanFaultOnce(1, "skip-disk-ABC") {
		t.Fatal("repeated state after re-log must stay suppressed")
	}
}
