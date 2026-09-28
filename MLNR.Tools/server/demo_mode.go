package main

// demo_mode.go：演示模式（环境变量 mlnr_BLE_MOCK=1 启用）。
//
// 用途：无真实硬件（蓝牙控制板 / 硬盘 / 温度传感器）时的界面演示与截图。
// 伪造"已连接"设备、开关/风扇/传感器实时状态、温度快照与历史趋势，
// 让监控总览、风扇、硬盘组等页面呈现完整运行数据。
//
// 生产环境不设置 mlnr_BLE_MOCK 即完全不受影响（不进入本文件任何分支）。

import (
	"fmt"
	"math"
	"os"
	"time"

	"mlnr/logger"
)

// demoModeEnabled 是否启用演示模式。
func demoModeEnabled() bool { return os.Getenv("mlnr_BLE_MOCK") == "1" }

func f64p(v float64) *float64 { return &v }

// installBLEMock 伪造"已连接 + 开关/风扇/传感器实时状态"。
func installBLEMock(m *BLEManager) {
	now := time.Now().Format(time.RFC3339)
	m.mu.Lock()
	m.deviceInfo = DeviceInfo{
		Name:            "NR_F2S4",
		Address:         "AA:BB:CC:DD:EE:01",
		RSSI:            -52,
		SignalPercent:   72,
		SignalQuality:   85,
		ConnectionState: StateConnected,
		SessionType:     SessionEncrypted,
		RunState:        RunStateWorkRun,
		Version:         "v1.2.0",
		ConnectedAt:     now,
	}
	for i := 0; i < MaxFanChannels; i++ {
		m.fanStates[i] = nil
	}
	for i := 0; i < MaxSwitchChannels; i++ {
		m.switchStates[i] = nil
	}
	for i := 0; i < MaxSensorChannels; i++ {
		m.sensorReadings[i] = nil
	}

	// 风扇：FAN1 42% / FAN2 55%，心跳正常
	m.fanStates[0] = &FanHWState{Index: 1, Pin: "GPIO21", Enabled: true, PWMFreq: 25000,
		PowerSpd: 60, HBFallback: 80, TargetSpd: 42, CurSpd: 42, Heartbeat: "OK", UpdatedAt: now}
	m.fanStates[1] = &FanHWState{Index: 2, Pin: "GPIO20", Enabled: true, PWMFreq: 25000,
		PowerSpd: 30, HBFallback: 80, TargetSpd: 55, CurSpd: 55, Heartbeat: "OK", UpdatedAt: now}

	// 开关：SW1/SW2 开（影音库、冷备归档在线），SW3/SW4 关
	for _, s := range []struct{ idx, state int }{{1, 1}, {2, 1}, {3, 0}, {4, 0}} {
		m.switchStates[s.idx-1] = &SwitchHWState{Index: s.idx, Pin: fmt.Sprintf("GPIO%d", 10+s.idx),
			Enabled: true, PowerOnState: 0, PowerOnDelay: 0, State: s.state,
			System: s.idx == 1, UpdatedAt: now}
	}

	// 传感器：CH0 AHT20 温湿度 / CH1 BMP280 气压 / CH2 LM75 温度；CH3 HTU21D 未启用
	chans := []struct {
		ch int
		kind SensorKind
		t, h, p float64
	}{
		{0, SensorAHT20, 26.4, 58.2, 0},
		{1, SensorBMP280, 26.1, 0, 101325},
		{2, SensorLM75, 34.8, 0, 0},
		{3, SensorHTU21D, 0, 0, 0},
	}
	for _, c := range chans {
		st := "OK"
		if c.ch == 3 {
			st = "FAIL" // 未启用通道
		}
		m.sensorReadings[c.ch] = &SensorChannelReading{
			ChID: c.ch, Kind: c.kind, State: st,
			Temperature: c.t, Humidity: c.h, Pressure: c.p, Altitude: 0, UpdatedAt: now,
		}
	}
	m.mu.Unlock()

	m.setState(StateConnected)
	m.setSessionType(SessionEncrypted)
	m.setRunState(RunStateWorkRun)
	logger.Info("demo", "BLE mock armed: NR_F2S4 connected (mlnr_BLE_MOCK=1, 演示数据)")
}

// installThermalMock 伪造温度快照 + 60 分钟历史趋势。
// 磁盘 SN 与 settings.json 中测试配置的硬盘序列号一致，供硬盘组视图温度关联。
func installThermalMock(t *ThermalManager) {
	now := time.Now()
	readings := []TemperatureReading{
		{ID: "cpu", Name: "CPU Package", Category: "cpu", Value: 52.3,
			Updated: now.Format(time.RFC3339), Device: "cpu0", Model: "AMD Ryzen 5 5600G",
			Zone: "temp1", Crit: f64p(95)},
		{ID: "mb", Name: "主板", Category: "mb", Value: 38.1,
			Updated: now.Format(time.RFC3339), Device: "mb", Zone: "temp1", Crit: nil},
		{ID: "hdd:sda", Name: "影音盘 A", Category: "hdd", Value: 41.2,
			Updated: now.Format(time.RFC3339), Device: "WD-WX1A9Z4R8A2K", Model: "WD80EAAZ",
			Serial: "WD-WX1A9Z4R8A2K", Zone: "temp1", Crit: f64p(60)},
		{ID: "hdd:sdb", Name: "影音盘 B", Category: "hdd", Value: 39.7,
			Updated: now.Format(time.RFC3339), Device: "ST2000DM008-2ER102-Z9B2X1N", Model: "ST2000DM008",
			Serial: "ST2000DM008-2ER102-Z9B2X1N", Zone: "temp1", Crit: f64p(60)},
		{ID: "hdd:sdc", Name: "归档盘 A", Category: "hdd", Value: 33.4,
			Updated: now.Format(time.RFC3339), Device: "TOSHIBA-MG08ACA16TE-66P0A1B2", Model: "MG08ACA16TE",
			Serial: "TOSHIBA-MG08ACA16TE-66P0A1B2", Zone: "temp1", Crit: f64p(60)},
		{ID: "hdd:sdd", Name: "归档盘 B", Category: "hdd", Value: 32.8,
			Updated: now.Format(time.RFC3339), Device: "ST8000VN004-2M1101-WW21A4R9", Model: "ST8000VN004",
			Serial: "ST8000VN004-2M1101-WW21A4R9", Zone: "temp1", Crit: f64p(60)},
		{ID: "hdd:nvme", Name: "下载 SSD", Category: "hdd", Value: 45.6,
			Updated: now.Format(time.RFC3339), Device: "S5GYNX0R123456", Model: "Samsung 980 PRO",
			Serial: "S5GYNX0R123456", Zone: "nvme0", Crit: f64p(70)},
		{ID: "sen:0", Name: "AHT20 环境", Category: "other", Value: 26.4,
			Updated: now.Format(time.RFC3339), Device: "i2c-0", Zone: "ch0", Crit: nil},
	}
	t.mu.Lock()
	t.snapshot = ThermalSnapshot{Temps: readings, Time: now.Format(time.RFC3339)}
	// 历史趋势：最近 60 分钟，每分钟 1 点（CPU / 影音盘 A / 两路风扇转速）
	base := now.Add(-60 * time.Minute).UnixMilli()
	for i := 0; i <= 60; i++ {
		ms := base + int64(i)*60000
		wave := math.Sin(float64(i)/6.0) * 3.0
		t.history.Append("temp:cpu", HistoryPoint{Time: ms, Value: 52.3 + wave})
		t.history.Append("temp:hdd:sda", HistoryPoint{Time: ms, Value: 41.2 + wave*0.8})
		t.history.Append("fan:1:spd", HistoryPoint{Time: ms, Value: 42 + float64((i*7)%6)})
		t.history.Append("fan:2:spd", HistoryPoint{Time: ms, Value: 55 + float64((i*5)%5)})
	}
	t.mu.Unlock()
	logger.Info("demo", "thermal mock armed: %d 温度点 + 60 分钟历史趋势", len(readings))
}
