package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"tinygo.org/x/bluetooth"

	"mlnr/logger"
)

// ============================================================
// BLEManager 管理 NR_F2S4 蓝牙连接、加密会话、指令收发、心跳与重连。
//
// 依据 FS.md 二进制帧协议（CURRENT_STATE.md「二、BLE 通信协议」）：
//   - 服务 UUID:  4fafc201-1fb5-459e-8fcc-c5c9c331914b
//   - 写指令特征: beb5483e-36e1-4688-b7f5-ea07361b26a8 (Write, 上位机→固件)
//   - 通知特征:   3a1c6f4d-8b2e-4d79-a5f0-c9e7b1d4a286 (Notify, 固件→上位机)
//     2026-09-21 起固件收发分离：单特征 RW+Notify 时特征值收发共用，
//     远程写入与固件 notify 竞争导致偶发收到自己发出帧的回环
//     （日志特征 "unknown cmd 0x02/0x06"）。拆分后竞态根除；
//     连接旧固件（无通知特征）时自动回退单特征模式。
//   - 固定密钥方案: 连接即建立固定密钥加密会话（无 RSA 握手）
//   - 加密会话帧格式：[IV(12)][密文(N)][TAG(16)]，AES-128-GCM
//   - 二进制帧： [Magic(1)=0xAA][SeqID(1)][CmdID(1)][PayloadLen(1)][Payload(N)][CRC16(2)]
//   - CRC16-CCITT: poly=0x1021, init=0xFFFF, 无反射, xorout=0（CCITT-FALSE）
//   - 设备状态：UNINIT / WORK_WAIT / WORK_RUN
//   - 硬件：2 风扇(FAN1/FAN2)、4 开关(SW1~SW4)，硬盘组复用开关通道
//   - SPD 指令 350ms 节流
//   - 未初始化状态仅允许 INIT/PING/GET
//   - 异步事件：EVT_BTN 0x81 / EVT_SENSOR 0x82 / EVT_WARN 0x83（SeqID=0）
// ============================================================

var (
	// 4fafc201-1fb5-459e-8fcc-c5c9c331914b
	serviceUUID = bluetooth.NewUUID([16]byte{
		0x4f, 0xaf, 0xc2, 0x01, 0x1f, 0xb5, 0x45, 0x9e,
		0x8f, 0xcc, 0xc5, 0xc9, 0xc3, 0x31, 0x91, 0x4b,
	})
	// beb5483e-36e1-4688-b7f5-ea07361b26a8
	charUUID = bluetooth.NewUUID([16]byte{
		0xbe, 0xb5, 0x48, 0x3e, 0x36, 0xe1, 0x46, 0x88,
		0xb7, 0xf5, 0xea, 0x07, 0x36, 0x1b, 0x26, 0xa8,
	})
	// 3a1c6f4d-8b2e-4d79-a5f0-c9e7b1d4a286（收发分离后的专用通知特征）
	notifyCharUUID = bluetooth.NewUUID([16]byte{
		0x3a, 0x1c, 0x6f, 0x4d, 0x8b, 0x2e, 0x4d, 0x79,
		0xa5, 0xf0, 0xc9, 0xe7, 0xb1, 0xd4, 0xa2, 0x86,
	})

	// 广播名前缀（固件广播名 NR_F2S4_{MAC后6位}，FS.md 固件#1）
	deviceNamePrefix = "NR_F2S4"

	// SPD 指令节流间隔
	spdThrottle = 350 * time.Millisecond
	// 指令超时
	cmdTimeout = 3 * time.Second
	// 密钥交换密文事件等待超时（固件 RSA 加密约几十 ms，含分片发送余量）
	keCtTimeout = 8 * time.Second
	// 心跳周期（I4）：必须远小于固件 CFG_GLOBAL_HB_TIMEOUT（默认 30s），
	// 留出足够重试余量：5s 周期 + 3 次连续超时（≈15s）仍远低于 30s 固件超时
	heartbeatInterval = 5 * time.Second
	// 连续通信失败阈值：≥3 次写失败/超时视为断线
	commFailThreshold = 3
)

// ===================== 二进制帧协议常量（与固件 binary_proto.h 一致） =====================

const (
	bpMagic      = 0xAA
	bpHeadLen    = 4
	bpCrcLen     = 2
	bpMinFrame   = 6
	bpMaxPayload = 240
)

// CmdID
const (
	bpCmdInit       byte = 0x01
	bpCmdSPD        byte = 0x02
	bpCmdSW         byte = 0x03
	bpCmdCFG        byte = 0x04
	bpCmdGetV       byte = 0x05
	bpCmdGetF       byte = 0x06
	bpCmdGetS       byte = 0x07
	bpCmdGetSR      byte = 0x08
	bpCmdPing       byte = 0x09
	bpCmdSave       byte = 0x0A
	bpCmdReset      byte = 0x0B
	bpCmdGetG       byte = 0x0C // 查询全局配置
	bpCmdHello      byte = 0x0D // 明文探测：固件回明文 ACK[ErrCode][State(0=UNINIT/1=已初始化)]
	bpCmdKePub      byte = 0x0E // 明文密钥交换：载荷 [ChunkIdx][ChunkCount][ChunkData]，密文经 EVT_KE_CT 回传
	bpCmdKeCfm      byte = 0x0F // 加密确认：载荷 [Nonce(16)]
	bpCmdGetOffline byte = 0x10 // 读取离线事件（加密）：应答 [ErrCode][固件当前ms u32 LE][条数][条目×N]，读后固件清空
	bpRespAck       byte = 0x80
	bpEvtBtn        byte = 0x81
	bpEvtSensor     byte = 0x82
	bpEvtWarn       byte = 0x83
	bpEvtKeCt       byte = 0x85 // 密钥交换密文事件（明文）：载荷 [ChunkIdx][ChunkCount][密文片]
)

// SWn 事件条件（EVT_BTN 载荷第 3 字节 / 离线事件条目 cond 字段）
const (
	bpCondButton uint8 = 0 // 硬件按钮
	bpCondAuto   uint8 = 1 // 上电自动上线（POWER_ON_STATE）
	bpCondCmd    uint8 = 2 // 指令/其它
)

// ErrorCode
const (
	bpErrOK               uint8 = 0
	bpErrUnknownCmd       uint8 = 1
	bpErrSecRequired      uint8 = 2
	bpErrDecryptFail      uint8 = 3
	bpErrStateNotUninit   uint8 = 4
	bpErrSaveAuthFail     uint8 = 6
	bpErrValueOutOfRange  uint8 = 7
	bpErrFanDisabled      uint8 = 8
	bpErrFanSpdRange      uint8 = 9
	bpErrSwStateInvalid   uint8 = 10
	bpErrSwDisabled       uint8 = 11
	bpErrCfgTargetUnknown uint8 = 13
	bpErrCfgKeyUnknown    uint8 = 14
	bpErrSensorChRange    uint8 = 15
	bpErrSensorTypeNone   uint8 = 16
	bpErrSensorNotReady   uint8 = 17
	bpErrSaveFailed       uint8 = 18
	bpErrFanIdRange       uint8 = 19
	bpErrSwIdRange        uint8 = 20
	bpErrSwSystemLocked   uint8 = 21 // 系统开关锁定，禁止操作
	bpErrBleNameInvalid   uint8 = 22 // 广播名非法
	bpErrKePubkeyInvalid  uint8 = 23 // 密钥交换：RSA 公钥解析失败
)

// CFG TargetType
const (
	bpCfgTargetFN     byte = 0
	bpCfgTargetSW     byte = 1
	bpCfgTargetSensor byte = 2
	bpCfgTargetGlobal byte = 3
)

// CfgKeyId
const (
	bpKeyFnEnabled       byte = 0x01 // u8
	bpKeyFnPowerSpd      byte = 0x02 // u8
	bpKeyFnHbFallback    byte = 0x04 // u8
	bpKeyFnPwmFreq       byte = 0x05 // u32
	bpKeySwEnabled       byte = 0x11 // u8
	bpKeySwPowerOnState  byte = 0x12 // u8
	bpKeySwPowerOnDelay  byte = 0x13 // u32
	bpKeySwSystem        byte = 0x14 // u8，系统开关标志
	bpKeySensorKind      byte = 0x21 // u8
	bpKeySensorAddr      byte = 0x22 // u8
	bpKeySensorEnabled   byte = 0x23 // u8
	bpKeySensorInterval  byte = 0x24 // u16
	bpKeyGlobalHbTimeout byte = 0x31 // u16
	bpKeyGlobalBleName   byte = 0x32 // string 1~20 字节
	bpKeyGlobalDebugMode byte = 0x33 // u8
)

// GETSR 常量
const (
	bpGetSRBlockLen = 24
	bpGetSRChMax    = 4
	bpGetSRPayload  = 2 + bpGetSRChMax*bpGetSRBlockLen // 98
	bpEvtSensorLen  = 18                               // ChId1+Kind1+Temp4+Hum4+Press4+Alt4
)

// bpErrText 错误码转可读文本。
func bpErrText(code uint8) string {
	switch code {
	case bpErrOK:
		return "OK"
	case bpErrUnknownCmd:
		return "UNKNOWN COMMAND"
	case bpErrSecRequired:
		return "SEC REQUIRED, INIT FIRST"
	case bpErrDecryptFail:
		return "DECRYPT FAIL"
	case bpErrStateNotUninit:
		return "STATE NOT UNINIT"
	case bpErrSaveAuthFail:
		return "SAVE INIT FAIL"
	case bpErrValueOutOfRange:
		return "VALUE OUT OF LIMIT"
	case bpErrFanDisabled:
		return "FAN DISABLED"
	case bpErrFanSpdRange:
		return "SPD VALUE RANGE 0~100"
	case bpErrSwStateInvalid:
		return "SW STATE 0 OR 1"
	case bpErrSwDisabled:
		return "SW DISABLED"
	case bpErrCfgTargetUnknown:
		return "UNKNOWN CFG TARGET"
	case bpErrCfgKeyUnknown:
		return "UNKNOWN CFG KEY"
	case bpErrSensorChRange:
		return "SENSOR CH RANGE 0~3"
	case bpErrSensorTypeNone:
		return "SENSOR TYPE NONE"
	case bpErrSensorNotReady:
		return "SENSOR NOT READY"
	case bpErrSaveFailed:
		return "SAVE FAILED"
	case bpErrFanIdRange:
		return "FAN RANGE 0~1"
	case bpErrSwIdRange:
		return "SW RANGE 1~4"
	case bpErrSwSystemLocked:
		return "SW SYSTEM LOCKED"
	case bpErrBleNameInvalid:
		return "BLE NAME INVALID"
	case bpErrKePubkeyInvalid:
		return "KE PUBKEY INVALID"
	}
	return fmt.Sprintf("ERR CODE %d", code)
}

// crc16CCITT 计算 CRC16-CCITT（poly 0x1021, init 0xFFFF, 无反射, xorout 0）。
func crc16CCITT(data []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// buildBinaryFrame 构建完整二进制帧（含 CRC，小端存储）。
func buildBinaryFrame(seq uint8, cmdID byte, payload []byte) ([]byte, error) {
	if len(payload) > bpMaxPayload {
		return nil, fmt.Errorf("payload 超长")
	}
	frame := make([]byte, 0, bpMinFrame+len(payload))
	frame = append(frame, bpMagic, seq, cmdID, byte(len(payload)))
	frame = append(frame, payload...)
	crc := crc16CCITT(frame)
	frame = append(frame, byte(crc&0xFF), byte(crc>>8))
	return frame, nil
}

// parseBinaryFrame 校验并解析接收帧，成功返回 seq/cmdID/payload。
func parseBinaryFrame(data []byte) (uint8, byte, []byte, bool) {
	if len(data) < bpMinFrame || data[0] != bpMagic {
		return 0, 0, nil, false
	}
	plen := int(data[3])
	if bpMinFrame+plen > len(data) {
		return 0, 0, nil, false
	}
	crc := crc16CCITT(data[:bpHeadLen+plen])
	got := uint16(data[bpHeadLen+plen]) | uint16(data[bpHeadLen+plen+1])<<8
	if crc != got {
		return 0, 0, nil, false
	}
	return data[1], data[2], data[bpHeadLen : bpHeadLen+plen], true
}

// bpCmdName 将 CmdID 转为可读名称（上位机需求 #24：运行日志二进制解码）。
func bpCmdName(cmdID byte) string {
	switch cmdID {
	case bpCmdInit:
		return "INIT"
	case bpCmdSPD:
		return "SPD"
	case bpCmdSW:
		return "SW"
	case bpCmdCFG:
		return "CFG"
	case bpCmdGetV:
		return "GETV"
	case bpCmdGetF:
		return "GETF"
	case bpCmdGetS:
		return "GETS"
	case bpCmdGetSR:
		return "GETSR"
	case bpCmdPing:
		return "PING"
	case bpCmdSave:
		return "SAVE"
	case bpCmdReset:
		return "RESET"
	case bpCmdGetG:
		return "GETG"
	case bpCmdHello:
		return "HELLO"
	case bpCmdKePub:
		return "KE_PUBKEY"
	case bpCmdKeCfm:
		return "KE_CONFIRM"
	case bpRespAck:
		return "ACK"
	case bpEvtBtn:
		return "EVT_BTN"
	case bpEvtSensor:
		return "EVT_SENSOR"
	case bpEvtWarn:
		return "EVT_WARN"
	case bpEvtKeCt:
		return "EVT_KE_CT"
	}
	return fmt.Sprintf("UNKNOWN(0x%02X)", cmdID)
}

// bpCfgKeyName 将 CfgKeyId 转为可读名称。
func bpCfgKeyName(keyID byte) string {
	switch keyID {
	case bpKeyFnEnabled:
		return "ENABLED"
	case bpKeyFnPowerSpd:
		return "POWER_SPD"
	case bpKeyFnHbFallback:
		return "HB_FALLBACK"
	case bpKeyFnPwmFreq:
		return "PWM_FREQ"
	case bpKeySwEnabled:
		return "ENABLED"
	case bpKeySwPowerOnState:
		return "POWER_ON_STATE"
	case bpKeySwPowerOnDelay:
		return "POWER_ON_DELAY"
	case bpKeySwSystem:
		return "SYSTEM"
	case bpKeySensorKind:
		return "KIND"
	case bpKeySensorAddr:
		return "ADDR"
	case bpKeySensorEnabled:
		return "ENABLED"
	case bpKeySensorInterval:
		return "INTERVAL"
	case bpKeyGlobalHbTimeout:
		return "HB_TIMEOUT"
	case bpKeyGlobalBleName:
		return "BLE_NAME"
	case bpKeyGlobalDebugMode:
		return "DEBUG_MODE"
	}
	return fmt.Sprintf("0x%02X", keyID)
}

// bpCfgTargetName 将 CFG TargetType 转为可读名称。
func bpCfgTargetName(target byte) string {
	switch target {
	case bpCfgTargetFN:
		return "FN"
	case bpCfgTargetSW:
		return "SW"
	case bpCfgTargetSensor:
		return "SENSOR"
	case bpCfgTargetGlobal:
		return "GLOBAL"
	}
	return fmt.Sprintf("UNKNOWN(%d)", target)
}

// bpDecodeCFG 解码 CFG 载荷为可读文本。
// CFG 载荷：[TargetType(1)][TargetId(1)][CfgKeyId(1)][Value(N)]
func bpDecodeCFG(payload []byte) string {
	if len(payload) < 3 {
		return "CFG (解析失败: 载荷过短)"
	}
	target := payload[0]
	targetID := payload[1]
	keyID := payload[2]
	val := payload[3:]

	var valStr string
	switch keyID {
	case bpKeyGlobalBleName:
		// 字符串值
		valStr = fmt.Sprintf("%q", string(val))
	case bpKeyFnPwmFreq, bpKeySwPowerOnDelay:
		if len(val) >= 4 {
			valStr = fmt.Sprintf("%d", binary.LittleEndian.Uint32(val))
		} else {
			valStr = "(解析失败)"
		}
	case bpKeySensorInterval, bpKeyGlobalHbTimeout:
		if len(val) >= 2 {
			valStr = fmt.Sprintf("%d", binary.LittleEndian.Uint16(val))
		} else {
			valStr = "(解析失败)"
		}
	default:
		if len(val) == 1 {
			valStr = fmt.Sprintf("%d", val[0])
		} else if len(val) > 0 {
			valStr = hex.EncodeToString(val)
		} else {
			valStr = "(空)"
		}
	}
	return fmt.Sprintf("CFG %s id=%d key=%s val=%s", bpCfgTargetName(target), targetID, bpCfgKeyName(keyID), valStr)
}

// bpDecodeCmd 将指令/应答的 CmdID + Payload 解码为可读文本（上位机需求 #24）。
// 用于在日志中同时输出二进制 hex 与可解读文本。
func bpDecodeCmd(cmdID byte, payload []byte) string {
	switch cmdID {
	case bpCmdInit:
		return "INIT"
	case bpCmdSPD:
		if len(payload) >= 2 {
			return fmt.Sprintf("SPD fan=%d spd=%d", payload[0], payload[1])
		}
		return "SPD (解析失败)"
	case bpCmdSW:
		if len(payload) >= 2 {
			return fmt.Sprintf("SW sw=%d state=%d", payload[0], payload[1])
		}
		return "SW (解析失败)"
	case bpCmdCFG:
		return bpDecodeCFG(payload)
	case bpCmdGetV:
		return "GETV"
	case bpCmdGetF:
		if len(payload) >= 1 {
			return fmt.Sprintf("GETF fan=%d", payload[0])
		}
		return "GETF"
	case bpCmdGetS:
		if len(payload) >= 1 {
			return fmt.Sprintf("GETS sw=%d", payload[0])
		}
		return "GETS"
	case bpCmdGetSR:
		return "GETSR"
	case bpCmdPing:
		return "PING"
	case bpCmdSave:
		return "SAVE"
	case bpCmdReset:
		return "RESET"
	case bpCmdGetG:
		return "GETG"
	case bpRespAck:
		if len(payload) >= 1 {
			if payload[0] == bpErrOK {
				return "ACK OK"
			}
			return fmt.Sprintf("ACK ERR: %s", bpErrText(payload[0]))
		}
		return "ACK (解析失败)"
	case bpEvtBtn:
		if len(payload) >= 2 {
			return fmt.Sprintf("EVT_BTN sw=%d state=%d", payload[0], payload[1])
		}
		return "EVT_BTN (解析失败)"
	case bpEvtSensor:
		if len(payload) >= bpEvtSensorLen {
			ch := payload[0]
			kind := payload[1]
			temp := math.Float32frombits(binary.LittleEndian.Uint32(payload[2:6]))
			return fmt.Sprintf("EVT_SENSOR ch=%d kind=%d temp=%.1f", ch, kind, temp)
		}
		return "EVT_SENSOR (解析失败)"
	case bpEvtWarn:
		if len(payload) >= 1 {
			return fmt.Sprintf("EVT_WARN code=%d", payload[0])
		}
		return "EVT_WARN (解析失败)"
	}
	return fmt.Sprintf("%s (解析失败)", bpCmdName(cmdID))
}

// 应答帧（携带请求序列号，用于与请求配对）
type respFrame struct {
	seq     uint8
	cmdID   byte
	payload []byte
}

// BLEManager 管理 BLE 连接与指令收发。
type BLEManager struct {
	mu sync.RWMutex

	// BLE 硬件句柄
	adapter    *bluetooth.Adapter
	device     *bluetooth.Device
	char       *bluetooth.DeviceCharacteristic // 写指令特征（上位机→固件）
	notifyChar *bluetooth.DeviceCharacteristic // 通知特征（固件→上位机；旧固件单特征模式为 nil）

	// 运行状态
	state      ConnectionState
	deviceInfo DeviceInfo

	// 硬件实时状态（GET 同步后填充）
	fanStates    [MaxFanChannels]*FanHWState
	switchStates [MaxSwitchChannels]*SwitchHWState

	// I2C 传感器实时数据（多通道：EVT_SENSOR 带 ChId/Kind，按通道存储）
	sensorReadings [MaxSensorChannels]*SensorChannelReading
	lastSensorPush time.Time // 最近一次收到传感器推送的时间（心跳重置依据）

	// 加密会话
	sec *SecurityCtx

	// 设备解密失败计数（连续 3 次降级为明文会话）
	decryptFailCount int

	// 通信失败计数（断线检测）：写特征值失败时递增，连续 ≥3 次触发 markDisconnected
	commFailCount int

	// fnOS 本地设置（来自 store）
	settings Settings

	// 指令应答通道：携带帧序列号，异步事件（seq=0）不进入此通道
	responseCh chan respFrame
	cmdMu      sync.Mutex // 串行化指令发送
	connectMu  sync.Mutex // 串行化 Connect 入口

	// 密钥交换密文事件拼接状态（RSA-OAEP 密文 256B 分 2 片经 EVT_KE_CT 回传）
	keCtCh    chan []byte // 密文拼接完成后发送完整 256B
	keCtBuf   []byte      // 拼接缓冲
	keCtGot   int
	keCtTotal int

	// 请求序列号（每条指令递增，1~255 回绕）
	seq uint8

	// SPD 节流：记录上次下发时间与目标值，避免 350ms 内重复同值
	spdLastTime  [MaxFanChannels + 1]time.Time // index 0 = 所有风扇
	spdLastValue [MaxFanChannels + 1]int

	// 下发后回读实际转速节流（1s 内最多回读一次，避免拖动滑条时频繁 GETF）
	fanReadLast [MaxFanChannels + 1]time.Time

	// 心跳
	heartbeatStop    chan struct{}
	heartbeatRunning bool

	// 全局配置缓存（GETG 同步后填充）
	globalConfig *GlobalConfig

	// 自动重连
	reconnectStop chan struct{}

	// 后台扫描（实时推送）
	scanStop    chan struct{}
	scanMu      sync.Mutex
	scanRunning bool

	// 依赖
	store *Store
	hub   *Hub
	disk  *DiskManager
}

// NewBLEManager 创建 BLE 管理器。
func NewBLEManager(store *Store, hub *Hub) *BLEManager {
	m := &BLEManager{
		state:      StateDisconnected,
		store:      store,
		hub:        hub,
		settings:   store.GetSettings(),
		responseCh: make(chan respFrame, 16),
		keCtCh:     make(chan []byte, 2),
		seq:        1,
		sec:        NewSecurityCtx(),
	}
	m.deviceInfo.ConnectionState = m.state
	m.deviceInfo.SignalQuality = -1 // 未知，待 PING 应答填充
	// 会话密钥方案: 密钥按 MAC 存于本地设置(deviceKeys)，握手时装载；NewSecurityCtx 为空上下文
	return m
}

// SetDiskManager 注入硬盘组管理器（main.go 启动时调用）。
func (m *BLEManager) SetDiskManager(d *DiskManager) {
	m.disk = d
}

// ensureAdapter 确保 BLE 适配器已初始化并启用。
// 启动阶段 BlueZ/蓝牙服务可能未就绪（bluetoothd 对象树未构建、适配器未上电），
// Enable 失败时返回 false；调用方（Connect/ConnectTo，含自动重连循环）会稍后重试，
// 避免一次启动时序问题导致 adapter 永久为 nil、自动连接永远失败。
func (m *BLEManager) ensureAdapter() bool {
	if m.adapter != nil {
		return true
	}
	logger.Debug("ble", "ensureAdapter: adapter is nil, attempting on-demand Enable()...")
	m.adapter = bluetooth.DefaultAdapter
	if err := m.adapter.Enable(); err != nil {
		logger.Warn("ble", "enable adapter failed: %v (BLE hardware unavailable, will retry)", err)
		m.adapter = nil
		return false
	}
	logger.Info("ble", "adapter ready (on-demand enable)")
	return true
}

// Start 初始化 BLE 适配器。
// 启动阶段 Enable 失败不致命：Connect/ConnectTo 会自动按需重试，自动重连循环可自愈。
func (m *BLEManager) Start() error {
	_ = m.ensureAdapter()
	return nil
}

// Stop 停止所有后台任务并断开连接。
func (m *BLEManager) Stop() {
	m.stopHeartbeat()
	m.stopReconnect()
	m.StopScan()
	m.disconnect()
}

// ===== 状态查询 =====

func (m *BLEManager) GetState() ConnectionState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

func (m *BLEManager) GetDeviceInfo() DeviceInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	info := m.deviceInfo
	info.ConnectionState = m.state
	return info
}

// GetFanStates 返回所有风扇硬件状态的拷贝（nil 元素表示未同步）。
func (m *BLEManager) GetFanStates() []FanHWState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]FanHWState, 0, MaxFanChannels)
	for i := 0; i < MaxFanChannels; i++ {
		if m.fanStates[i] != nil {
			out = append(out, *m.fanStates[i])
		} else {
			out = append(out, FanHWState{Index: i + 1})
		}
	}
	return out
}

// GetSwitchStates 返回所有开关硬件状态的拷贝。
func (m *BLEManager) GetSwitchStates() []SwitchHWState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]SwitchHWState, 0, MaxSwitchChannels)
	for i := 0; i < MaxSwitchChannels; i++ {
		if m.switchStates[i] != nil {
			out = append(out, *m.switchStates[i])
		} else {
			out = append(out, SwitchHWState{Index: i + 1})
		}
	}
	return out
}

// GetSensorData 返回首个通道的传感器数据（兼容旧接口，nil 表示未收到推送）。
func (m *BLEManager) GetSensorData() *SensorData {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, rd := range m.sensorReadings {
		if rd != nil {
			return &SensorData{
				Temperature: rd.Temperature,
				Humidity:    rd.Humidity,
				Pressure:    rd.Pressure,
				Altitude:    rd.Altitude,
				UpdatedAt:   rd.UpdatedAt,
			}
		}
	}
	return nil
}

// GetSensorChannels 返回全部通道实时读数（nil 表示该通道无数据）。
func (m *BLEManager) GetSensorChannels() []*SensorChannelReading {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*SensorChannelReading, MaxSensorChannels)
	for i, rd := range m.sensorReadings {
		out[i] = rd
	}
	return out
}

func (m *BLEManager) GetSettings() Settings {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.settings
}

// IsEncryptedSession 判断当前是否处于加密会话（WORK_RUN 才允许业务指令）。
func (m *BLEManager) IsEncryptedSession() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.deviceInfo.SessionType == SessionEncrypted && m.deviceInfo.RunState == RunStateWorkRun
}

// SetSettings 更新内存设置并广播状态。
func (m *BLEManager) SetSettings(s Settings) {
	m.mu.Lock()
	m.settings = s
	m.mu.Unlock()
	m.broadcastState()
}

func (m *BLEManager) setState(s ConnectionState) {
	m.mu.Lock()
	m.state = s
	m.deviceInfo.ConnectionState = s
	if s == StateConnected {
		m.deviceInfo.ConnectedAt = time.Now().Format(time.RFC3339)
	} else if s == StateDisconnected {
		m.deviceInfo.ConnectedAt = ""
	}
	m.mu.Unlock()
	m.broadcastState()
}

func (m *BLEManager) setRunState(rs DeviceRunState) {
	m.mu.Lock()
	m.deviceInfo.RunState = rs
	m.mu.Unlock()
	m.broadcastState()
}

func (m *BLEManager) setSessionType(st SessionType) {
	m.mu.Lock()
	m.deviceInfo.SessionType = st
	m.mu.Unlock()
	m.broadcastState()
}

// ===== 扫描 =====

// Scan 扫描周围全部 BLE 设备，返回设备清单（名称以 NR_F2S4 开头的设备优先，其余按 RSSI 降序）。
func (m *BLEManager) Scan(timeout time.Duration) ([]DeviceInfo, error) {
	if m.adapter == nil {
		return nil, fmt.Errorf("BLE 硬件适配器不可用，请检查蓝牙是否启用")
	}
	// P4：后台扫描进行中时禁止再起一次性扫描，避免 adapter.Scan 冲突
	m.scanMu.Lock()
	if m.scanRunning {
		m.scanMu.Unlock()
		return nil, fmt.Errorf("扫描已在进行中")
	}
	m.scanMu.Unlock()
	m.setState(StateScanning)
	defer m.setState(StateDisconnected)

	var mu sync.Mutex
	results := make([]DeviceInfo, 0, 16)
	seen := make(map[string]bool)

	err := m.adapter.Scan(func(_ *bluetooth.Adapter, result bluetooth.ScanResult) {
		addr := result.Address.String()
		mu.Lock()
		if !seen[addr] {
			seen[addr] = true
			results = append(results, DeviceInfo{
				Name:            result.LocalName(),
				Address:         addr,
				RSSI:            int(result.RSSI),
				SignalPercent:   rssiToPercent(int(result.RSSI)),
				SignalQuality:   -1, // 扫描阶段尚无固件 PING 应答
				ConnectionState: StateDisconnected,
			})
		}
		mu.Unlock()
	})
	if err != nil {
		return nil, fmt.Errorf("扫描失败: %w", err)
	}

	// 扫描 timeout 时长后停止
	timer := time.AfterFunc(timeout, func() {
		_ = m.adapter.StopScan()
	})
	defer timer.Stop()
	time.Sleep(timeout)
	_ = m.adapter.StopScan()

	// 排序：目标广播名前缀优先，其次 RSSI 降序
	pref := m.devicePrefix()
	sort.Slice(results, func(i, j int) bool {
		pi := strings.HasPrefix(results[i].Name, pref)
		pj := strings.HasPrefix(results[j].Name, pref)
		if pi != pj {
			return pi
		}
		return results[i].RSSI > results[j].RSSI
	})
	return results, nil
}

// ===== 后台实时扫描（WebSocket 推送 scan_result 消息） =====

// StartScan 启动后台 BLE 扫描，每个新发现的设备通过 WS 广播 scan_result 消息。
// 返回已开始扫描（nil）或错误。扫描持续 12 秒或直到 StopScan 被调用。
func (m *BLEManager) StartScan() error {
	if m.adapter == nil {
		return fmt.Errorf("BLE 硬件适配器不可用，请检查蓝牙是否启用")
	}

	m.scanMu.Lock()
	if m.scanRunning {
		m.scanMu.Unlock()
		return fmt.Errorf("扫描已在进行中")
	}
	scanStop := make(chan struct{})
	scanDone := make(chan struct{})
	m.scanStop = scanStop
	m.scanRunning = true
	m.scanMu.Unlock()

	m.setState(StateScanning)

	seen := make(map[string]bool)
	var mu sync.Mutex

	// 广播扫描开始事件
	if m.hub != nil {
		m.hub.Broadcast(gin.H{"type": "scan_started", "time": time.Now().Format(time.RFC3339)})
	}

	// 启动扫描协程
	go func() {
		// 启动底层扫描
		err := m.adapter.Scan(func(_ *bluetooth.Adapter, result bluetooth.ScanResult) {
			addr := result.Address.String()
			mu.Lock()
			if seen[addr] {
				mu.Unlock()
				return
			}
			seen[addr] = true
			mu.Unlock()

			dev := DeviceInfo{
				Name:            result.LocalName(),
				Address:         addr,
				RSSI:            int(result.RSSI),
				SignalPercent:   rssiToPercent(int(result.RSSI)),
				SignalQuality:   -1, // 扫描阶段尚无固件 PING 应答
				ConnectionState: StateDisconnected,
			}
			// 实时推送新发现的设备
			if m.hub != nil {
				m.hub.Broadcast(gin.H{
					"type":   "scan_result",
					"device": dev,
					"isOurs": strings.HasPrefix(dev.Name, m.devicePrefix()),
					"time":   time.Now().Format(time.RFC3339),
				})
			}
		})
		if err != nil {
			logger.Warn("ble", "background scan error: %v", err)
		}
	}()

	// 定时停止 + 监听手动停止
	go func() {
		select {
		case <-scanStop:
			// 手动停止
		case <-time.After(12 * time.Second):
			// 超时自动停止
		}
		_ = m.adapter.StopScan()

		m.scanMu.Lock()
		m.scanRunning = false
		m.scanStop = nil
		m.scanMu.Unlock()

		m.setState(StateDisconnected)

		if m.hub != nil {
			m.hub.Broadcast(gin.H{"type": "scan_stopped", "time": time.Now().Format(time.RFC3339)})
		}
		logger.Info("ble", "scan stopped")
		close(scanDone)
	}()

	return nil
}

// StopScan 停止后台扫描并阻塞直到实际停止完成（避免与 ConnectTo 竞态）。
func (m *BLEManager) StopScan() {
	m.scanMu.Lock()
	if !m.scanRunning {
		m.scanMu.Unlock()
		return
	}
	stopCh := m.scanStop
	m.scanMu.Unlock()

	// 触发停止
	select {
	case <-stopCh:
		// 已关闭
	default:
		close(stopCh)
	}

	// 等待 adapter.StopScan 和状态转换完成（最多等 3 秒）
	deadline := time.After(3 * time.Second)
	for {
		m.scanMu.Lock()
		running := m.scanRunning
		m.scanMu.Unlock()
		if !running {
			return
		}
		select {
		case <-deadline:
			logger.Warn("ble", "StopScan timed out waiting for scan goroutine")
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// IsScanning 返回后台扫描是否正在运行。
func (m *BLEManager) IsScanning() bool {
	m.scanMu.Lock()
	defer m.scanMu.Unlock()
	return m.scanRunning
}

// devicePrefix 返回扫描过滤/排序用的设备广播名前缀（settings.DeviceName，空则回退默认 NR_F2S4）。
func (m *BLEManager) devicePrefix() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p := strings.TrimSpace(m.settings.DeviceName)
	if p == "" {
		p = deviceNamePrefix
	}
	return p
}

func (m *BLEManager) getLastAddress() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return strings.TrimSpace(m.settings.LastAddress)
}

// ===== 连接 =====

// ============================================================
// 连接状态机（I2 文档）
//
//   Disconnected ──Connect()──→ Connecting ──成功──→ Connected
//        ↑                          │                    │
//        │                          失败                   │ disconnect()
//        │                          ↓                    ↓
//        └──────────────────── Disconnected ←──────────────┘
//                                    │
//                               Scanning（Scan/StartScan 临时态）
//
//   connectMu 串行化 Connect 与 Disconnect（M3）：
//     - 手动 Disconnect 会等在飞的自动重连 Connect 完成后再断开，防止"复活"
//     - Connect 内部失败直接 setState(Disconnected) 后 return，不调 Disconnect，无死锁
//
//   断线检测（M4）：
//     - 写特征值失败 / 指令超时 → noteCommFailure() 累计
//     - 连续 ≥ commFailThreshold 次 → markDisconnected() → disconnect()
//     - 解密连续失败 3 次 → ClearKey + Plain（密钥失效）
// ============================================================

// Connect 自动连接：优先使用上次成功地址；否则扫描选择第一个 NR_F2S4 设备。
// 完成后同步全部硬件状态、初始化握手并启动心跳。
func (m *BLEManager) Connect() error {
	m.connectMu.Lock()
	defer m.connectMu.Unlock()
	logger.Debug("ble", "Connect() entered, state=%s", m.GetState())

	if m.GetState() == StateConnected || m.GetState() == StateConnecting {
		logger.Debug("ble", "Connect() aborted: already %s", m.GetState())
		return fmt.Errorf("已连接或正在连接中")
	}

	// 确保后台扫描已停止，避免 adapter 状态冲突
	m.StopScan()
	logger.Debug("ble", "Connect(): StopScan done, adapter=%v", m.adapter != nil)

	m.startReconnect()
	logger.Debug("ble", "Connect(): reconnect watcher started")

	if !m.ensureAdapter() {
		logger.Warn("ble", "Connect(): adapter unavailable, giving up this round")
		m.setState(StateDisconnected)
		return fmt.Errorf("BLE 硬件适配器不可用，请检查蓝牙是否启用")
	}
	logger.Debug("ble", "Connect(): adapter ready, proceeding to connect")

	m.setState(StateConnecting)

	if addr := m.getLastAddress(); addr != "" {
		logger.Debug("ble", "Connect(): target address from settings: %q", addr)
		// 关键：BlueZ 只有在扫描/连接过设备后才会把 Device1 对象注册到 DBus 对象树
		// (/org/bluez/hci0/dev_XX_XX_XX_XX_XX_XX)。tinygo gap_linux.go 的 Connect 会直接
		// 拼出该路径并 GetProperty("org.bluez.Device1.Connected")——对象不存在时必然报错
		// （"Method Get with signature ss doesn't exist"），等待多久都没用。
		// 因此必须先扫描到目标设备（BlueZ 注册对象）才能连接；手动连接成功正是因为
		// 用户先看到扫描清单里的设备再点击。自动连接扫描未发现目标时继续扫描重试，
		// 绝不能"直接连接"（对象未注册时必然失败，5 次重试纯属浪费）。
		const discoverTimeout = 30 * time.Second
		discoverDeadline := time.Now().Add(discoverTimeout)
		name, rssi := "", 0
		for {
			logger.Info("ble", "ensuring target device %s is discoverable (scan)...", addr)
			if n, r := m.ensureDeviceDiscovered(addr, 5*time.Second); n != "" {
				name, rssi = n, r
				logger.Info("ble", "device discovered: %s (%s) rssi=%d", name, addr, rssi)
				break
			}
			if time.Now().After(discoverDeadline) {
				logger.Warn("ble", "target device %s not discoverable within %v (out of range or not advertising), watcher will retry", addr, discoverTimeout)
				m.setState(StateDisconnected)
				return fmt.Errorf("设备 %s 未在广播（%v 内未扫描到）", addr, discoverTimeout)
			}
			logger.Debug("ble", "scan round done, target %s not found yet, rescanning...", addr)
		}
		logger.Debug("ble", "Connect(): target found, calling connectAddress(%s, %s, %d)", addr, name, rssi)
		if err := m.connectAddress(addr, name, rssi); err != nil {
			logger.Debug("ble", "Connect(): connectAddress failed: %v", err)
			m.setState(StateDisconnected)
			return err
		}
	} else {
		devs, err := m.Scan(12 * time.Second)
		if err != nil {
			m.setState(StateDisconnected)
			return err
		}
		var target *DeviceInfo
		pref := m.devicePrefix()
		for i := range devs {
			if strings.HasPrefix(devs[i].Name, pref) {
				target = &devs[i]
				break
			}
		}
		if target == nil {
			m.setState(StateDisconnected)
			return fmt.Errorf("未发现设备（前缀 %s）", pref)
		}
		if err := m.connectAddress(target.Address, target.Name, target.RSSI); err != nil {
			m.setState(StateDisconnected)
			return err
		}
	}

	// 连接成功，先置 Connected 并切换到当前设备 MAC 隔离配置
	m.setState(StateConnected)

	// Phase 4.1：切换到当前设备 MAC 隔离配置（持久化旧 MAC → 加载新 MAC）
	if m.store != nil {
		m.store.SwitchDeviceMAC(m.GetDeviceInfo().Address)
		m.mu.Lock()
		m.settings = m.store.GetSettings()
		m.mu.Unlock()
		// MAC 级配置加载后同步硬盘组配置到 DiskManager：
		// 否则 d.config 仍是启动时的默认配置（settings.json），
		// 监控界面/硬盘组管理界面显示错误的绑定关系（通道设置页读 store.settings 不受影响）。
		if m.disk != nil {
			m.disk.UpdateConfig(m.store.GetSettings().DiskGroups)
		}
		// 再次广播：让前端拿到切换后的 MAC 级设置（directSaveNVS / I2C 通道 / 硬盘组等）
		m.broadcastState()
	}

	// 会话密钥方案：连接后先握手（明文 HELLO 探测 → 密钥分发/装载 → 加密验证），
	// 握手成功前不发送任何加密业务指令（无会话密钥时 sendBinaryCommand 必然失败）
	if err := m.InitiateHandshake(); err != nil {
		logger.Warn("ble", "auto handshake failed: %v", err)
		// 握手失败视为连接不可用：断开 BLE 链路并回滚状态，
		// 避免 API 返回 200 但设备实际不可用、心跳持续刷 Warn
		m.disconnect()
		return fmt.Errorf("握手失败: %w", err)
	}

	// 握手成功后读取固件离线事件（离线期间 SWn 变化，反推真实时间写操作日志）
	go m.readOfflineEvents()

	// 握手成功后批量同步全部硬件状态
	if err := m.SyncAllStates(); err != nil {
		logger.Warn("ble", "initial sync failed: %v", err)
	}

	// 启动心跳
	m.startHeartbeat()

	// 上位机需求 #4：连接后异步执行完整硬件状态查询（含重试），不阻塞 Connect 返回
	go m.fullQueryAfterConnect()

	return nil
}

// ensureDeviceDiscovered 对指定 MAC 地址做短暂扫描，触发 BlueZ 在 DBus 对象树中注册
// 该设备的 Device1 对象。扫描完成后返回设备名和 RSSI（若找到），否则返回空字符串和 0。
// 此函数内部调用 adapter.Scan() 但不改变 BLEManager 的 ConnectionState，避免与 Connect()
// 的状态机冲突。
func (m *BLEManager) ensureDeviceDiscovered(target string, duration time.Duration) (string, int) {
	if m.adapter == nil {
		logger.Debug("ble", "ensureDeviceDiscovered(%s): adapter is nil, returning empty", target)
		return "", 0
	}

	logger.Debug("ble", "ensureDeviceDiscovered(%s): starting scan for %v...", target, duration)

	var foundName string
	var foundRSSI int
	var seenCount int
	var mu sync.Mutex

	scanDone := make(chan struct{}, 1)
	// 在独立 goroutine 中运行底层扫描。
	// 为什么：tinygo 的 Adapter.Scan 是阻塞调用，其"停止"依赖 DBus 信号循环
	// 处理 cancelChan；若回调（如日志 I/O）阻塞，信号循环卡死，StopScan 无法
	// 生效，Scan 永不返回 → Connect 永远卡在 scanning（已由线上日志证实：
	// 高频 TARGET HIT 日志 + fnOS 日志 I/O 变慢导致 7s+ 扫描不返回）。
	// 因此：回调内零日志（只更新状态），主流程用 select 超时兜底。
	go func() {
		err := m.adapter.Scan(func(_ *bluetooth.Adapter, result bluetooth.ScanResult) {
			addr := result.Address.String()
			mu.Lock()
			if addr == target {
				if foundName == "" {
					foundName = result.LocalName()
					foundRSSI = int(result.RSSI)
				}
			} else {
				seenCount++
			}
			mu.Unlock()
		})
		mu.Lock()
		errCopy := err
		mu.Unlock()
		if errCopy != nil {
			logger.Warn("ble", "ensureDeviceDiscovered(%s): scan error: %v", target, errCopy)
		}
		close(scanDone)
	}()

	// duration 后请求停止扫描（close cancelChan → Scan 返回）
	timer := time.AfterFunc(duration, func() {
		_ = m.adapter.StopScan()
	})
	defer timer.Stop()

	select {
	case <-scanDone:
		// 扫描正常返回
	case <-time.After(duration + 3*time.Second):
		// 扫描未在预期内返回（信号循环卡死等），强制超时：
		// 再次 StopScan 清理 adapter 层 cancelChan，泄漏的 Scan goroutine
		// 会被关闭并返回，后续扫描可重入（自愈），Connect 不再被卡死。
		logger.Warn("ble", "ensureDeviceDiscovered(%s): scan stuck, forcing timeout (found=%q)", target, foundName)
		_ = m.adapter.StopScan()
	}

	mu.Lock()
	defer mu.Unlock()
	if foundName != "" {
		logger.Debug("ble", "ensureDeviceDiscovered(%s): matched name=%q rssi=%d (seen %d other devices)", target, foundName, foundRSSI, seenCount)
		return foundName, foundRSSI
	}
	logger.Debug("ble", "ensureDeviceDiscovered(%s): no match in this round (%d other devices)", target, seenCount)
	return "", 0
}

// ConnectTo 按指定地址连接设备（用户从扫描清单中选择后调用）。
func (m *BLEManager) ConnectTo(dev DeviceInfo) error {
	m.connectMu.Lock()
	defer m.connectMu.Unlock()

	if m.GetState() == StateConnected || m.GetState() == StateConnecting {
		return fmt.Errorf("已连接或正在连接中")
	}
	if strings.TrimSpace(dev.Address) == "" {
		return fmt.Errorf("缺少设备地址")
	}

	// 确保后台扫描已停止，避免 adapter 状态冲突
	m.StopScan()

	m.startReconnect()

	if !m.ensureAdapter() {
		m.setState(StateDisconnected)
		return fmt.Errorf("BLE 硬件适配器不可用，请检查蓝牙是否启用")
	}

	m.setState(StateConnecting)
	if err := m.connectAddress(dev.Address, dev.Name, dev.RSSI); err != nil {
		m.setState(StateDisconnected)
		return err
	}

	m.setState(StateConnected)

	// Phase 4.1：切换到当前设备 MAC 隔离配置
	if m.store != nil {
		m.store.SwitchDeviceMAC(dev.Address)
		m.mu.Lock()
		m.settings = m.store.GetSettings()
		m.mu.Unlock()
		// MAC 级配置加载后同步硬盘组配置到 DiskManager（原因同上，见 Connect）
		if m.disk != nil {
			m.disk.UpdateConfig(m.store.GetSettings().DiskGroups)
		}
		// 再次广播：让前端拿到切换后的 MAC 级设置（directSaveNVS / I2C 通道 / 硬盘组等）
		m.broadcastState()
	}

	// 会话密钥方案：先握手（明文 HELLO 探测 → 密钥分发/装载），成功前不发送加密业务指令
	if err := m.InitiateHandshake(); err != nil {
		logger.Warn("ble", "auto handshake failed: %v", err)
		// 握手失败视为连接不可用：断开 BLE 链路并回滚状态，避免 API 返回 200 但设备不可用
		m.disconnect()
		return fmt.Errorf("握手失败: %w", err)
	}

	// 握手成功后读取固件离线事件（离线期间 SWn 变化，反推真实时间写操作日志）
	go m.readOfflineEvents()

	// 握手成功后批量同步全部硬件状态
	if err := m.SyncAllStates(); err != nil {
		logger.Warn("ble", "initial sync failed: %v", err)
	}
	m.startHeartbeat()

	// 上位机需求 #4：连接后异步执行完整硬件状态查询（含重试），不阻塞 ConnectTo 返回
	go m.fullQueryAfterConnect()

	return nil
}

// connectAddress 建立到指定地址的连接（含指数退避重试 + DBus transient 错误识别）。
// rssi 为连接前扫描到的信号强度（无扫描来源时传 0，前端显示 "—"）；连接后不再读 RSSI，
// 信号质量改由固件 PING 应答携带（见 applyPingQuality）。
//
// 错误分类：
//   - DBus transient 错误（Method doesn't exist / UnknownObject / 电源管理未就绪）：
//     BlueZ 刚启动时对象树未完全构建，指数退避后自动重试。
//   - 真实错误（硬件不存在、权限、认证）：立即返回不重试。
func (m *BLEManager) connectAddress(address, name string, rssi int) error {
	const totalAttempts = 5
	var lastErr error
	logger.Debug("ble", "connectAddress(%s, name=%q, rssi=%d): up to %d attempts", address, name, rssi, totalAttempts)

	// 防御性清除内存会话密钥：新连接必须从明文 HELLO 开始（或从本地装载密钥），
	// 不允许上一次连接残留的密钥污染本次握手（残留时明文帧会被误按加密解析）
	m.sec.ClearKey()

	for attempt := 1; attempt <= totalAttempts; attempt++ {
		addr := bluetooth.Address{}
		addr.Set(address)
		if addr.MAC == (bluetooth.MAC{}) {
			return fmt.Errorf("非法设备地址: %s", address)
		}

		// 设备可能刚启动广播, 稍候让BLE控制器稳定
		time.Sleep(300 * time.Millisecond)

		connectStart := time.Now()
		dev, err := m.adapter.Connect(addr, bluetooth.ConnectionParams{})
		if err != nil {
			lastErr = err
			isTransient := isDBusTransientError(err)
			if isTransient {
				// transient 错误是 BlueZ 对象树未就绪等时序问题，
				// 不是真实故障，降为 DEBUG 避免 WARN 日志刷屏
				logger.Debug("ble", "connect attempt %d/%d failed (%s) after %v: BlueZ transient — %v", attempt, totalAttempts, address, time.Since(connectStart).Round(time.Millisecond), err)
			} else {
				logger.Warn("ble", "connect attempt %d/%d failed (%s) after %v: %v", attempt, totalAttempts, address, time.Since(connectStart).Round(time.Millisecond), err)
			}
			if attempt < totalAttempts {
				// 指数退避：2s, 4s, 6s, 8s
				backoff := time.Duration(attempt) * 2 * time.Second
				if isTransient {
					// transient 错误给 BlueZ 更多时间初始化
					backoff = time.Duration(attempt+1) * 3 * time.Second
				}
				logger.Debug("ble", "waiting %v before retry", backoff)
				time.Sleep(backoff)
			}
			continue
		}
		logger.Debug("ble", "connect attempt %d/%d succeeded (%s) in %v", attempt, totalAttempts, address, time.Since(connectStart).Round(time.Millisecond))
		m.device = &dev

		displayName := name
		if displayName == "" {
			displayName = deviceNamePrefix
		}
		m.mu.Lock()
		m.deviceInfo = DeviceInfo{
			Name:            displayName,
			Address:         address,
			RSSI:            rssi,
			SignalPercent:   rssiToPercent(rssi),
			SignalQuality:   -1, // 连接后待固件 PING 应答填充
			ConnectionState: StateConnecting,
		}
		// 会话密钥方案: 连接初期为明文阶段（HELLO/密钥交换），握手完成后置 SessionEncrypted
		m.deviceInfo.SessionType = SessionPlain
		// RunState 置 Unknown: 实际工作状态需由 INIT 握手确认(UNINIT/WORK_RUN),
		// 过早置 WORK_RUN 会让 thermal 等自动任务在握手完成前就下发业务指令
		// (2026-09-05 联调日志: 连接瞬间 SPD 裸发 + 长会话设备崩溃)。握手成功后再置 WORK_RUN。
		m.deviceInfo.RunState = RunStateUnknown
		m.mu.Unlock()

		// 发现服务
		srvs, err := dev.DiscoverServices([]bluetooth.UUID{serviceUUID})
		if err != nil {
			_ = dev.Disconnect()
			return fmt.Errorf("发现服务失败: %w", err)
		}
		if len(srvs) == 0 {
			_ = dev.Disconnect()
			return fmt.Errorf("未找到目标服务 %s", serviceUUID)
		}

		// 发现业务特征（收发分离：写指令特征 + 通知特征）
		chars, err := srvs[0].DiscoverCharacteristics(nil)
		if err != nil {
			_ = dev.Disconnect()
			return fmt.Errorf("发现特征失败: %w", err)
		}
		var charWrite, charNotify *bluetooth.DeviceCharacteristic
		for i := range chars {
			switch chars[i].UUID() {
			case charUUID:
				charWrite = &chars[i]
			case notifyCharUUID:
				charNotify = &chars[i]
			}
		}
		if charWrite == nil {
			_ = dev.Disconnect()
			return fmt.Errorf("未找到写指令特征 %s", charUUID)
		}
		m.char = charWrite
		m.notifyChar = charNotify

		// 开启 Notify：优先收发分离的专用通知特征；
		// 旧固件只有单一 RW+Notify 特征（无通知特征），回退到写指令特征本身收发
		notifyTarget := charNotify
		if notifyTarget == nil {
			logger.Info("ble", "未发现通知特征（旧固件单特征模式），沿用写指令特征收发")
			notifyTarget = charWrite
		}
		if err := notifyTarget.EnableNotifications(func(data []byte) {
			m.handleNotify(data)
		}); err != nil {
			_ = dev.Disconnect()
			return fmt.Errorf("开启通知失败: %w", err)
		}

		// 保存上次成功 MAC
		m.mu.Lock()
		s := m.settings
		s.LastAddress = address
		m.settings = s
		m.mu.Unlock()
		m.store.SaveSettings(s)

		logger.Info("ble", "connected to %s (%s)", displayName, address)
		return nil
	}

	return fmt.Errorf("连接失败（已重试 %d 次）: %w", totalAttempts, lastErr)
}

// isDBusTransientError 判断 err 是否为 BlueZ 启动时序竞态导致的 DBus 瞬时错误。
// 这些错误通常发生在开机后首次自动连接，BlueZ daemon 已启动但对象树未完全构建时。
//
// 已知 transient 错误模式（tinygo.org/x/bluetooth / godbus / BlueZ）：
//   - Method \"Get\" with signature \"ss\" on interface \"org.freedesktop.DBus.Properties\" doesn't exist
//   - org.freedesktop.DBus.Error.UnknownObject
//   - org.freedesktop.DBus.Error.ServiceUnknown (org.bluez 还未注册)
//   - bluetooth: adapter hci0 does not exist (BlueZ adapter 注册延迟)
//   - errAdaptorNotPowered (适配器刚上电，Powered 属性还没变为 true)
func isDBusTransientError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	transientPatterns := []string{
		"doesn't exist",
		"UnknownObject",
		"ServiceUnknown",
		"does not exist",
		"adapter not powered",
		"adaptor not powered",
		"bluetooth: failed to connect", // BlueZ Device1.Connect 异步超时
	}
	for _, p := range transientPatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// Disconnect 主动断开连接。
// 持 connectMu 串行化 Connect/Disconnect：等待可能正在进行的自动重连 Connect 完成，
// 再执行断开，避免在飞的 Connect 在用户手动断开后"复活"设备。
// Disconnect 不会被 Connect 内部调用（Connect 失败时直接 setState 后 return），无死锁风险。
func (m *BLEManager) Disconnect() error {
	m.connectMu.Lock()
	defer m.connectMu.Unlock()
	m.stopHeartbeat()
	m.stopReconnect()
	m.disconnect()
	return nil
}

func (m *BLEManager) disconnect() {
	// 持指令锁清理连接句柄，与 sendBinaryCommand 的特征写入互斥，
	// 避免 char 在写入过程中被并发置空导致 panic
	m.cmdMu.Lock()
	if m.device != nil {
		if m.sec.HasKey() && m.IsEncryptedSession() {
			logger.Debug("ble", "encrypted session closed (disconnect)")
		}
		_ = m.device.Disconnect()
		m.device = nil
		m.char = nil
		m.notifyChar = nil
	}
	m.cmdMu.Unlock()

	// 会话密钥方案：断开必须清除内存会话密钥。
	// 不清则下次重连时 HasKey() 仍为 true，固件回的明文 HELLO ACK 会被误按加密帧
	// 解析（日志特征：decrypt notify failed: 加密帧长度不足 → HELLO 超时 → 握手失败）。
	m.sec.ClearKey()

	// Phase 4.1：断开前持久化当前 MAC 的配置 → 切回默认 settings.json
	// （SwitchDeviceMAC 内部持有自己的锁，不与 m.mu 冲突；settings 写入需持 m.mu 与 API 读并发安全）
	if m.store != nil {
		m.store.SwitchDeviceMAC("")
		// 切回默认配置后同步硬盘组配置，保持视图与设置一致
		if m.disk != nil {
			m.disk.UpdateConfig(m.store.GetSettings().DiskGroups)
		}
	}

	m.mu.Lock()
	if m.store != nil {
		m.settings = m.store.GetSettings()
	}
	m.clearStatesLocked()
	m.mu.Unlock()
	m.setState(StateDisconnected)
	logger.Info("ble", "disconnected")
}

// noteCommFailure 记录一次通信失败（写特征值失败/写超时）。
// 连续 ≥3 次视为连接已断开，触发 markDisconnected 让 UI 状态真实并自动重连。
func (m *BLEManager) noteCommFailure() {
	m.mu.Lock()
	m.commFailCount++
	if m.commFailCount >= commFailThreshold {
		m.commFailCount = 0
		m.mu.Unlock()
		go m.markDisconnected()
		return
	}
	m.mu.Unlock()
}

// noteCommOK 通信成功时清零失败计数。
func (m *BLEManager) noteCommOK() {
	m.mu.Lock()
	m.commFailCount = 0
	m.mu.Unlock()
}

// markDisconnected 设备断线时清理连接并置为 Disconnected（由自动重连 watcher 接管）。
func (m *BLEManager) markDisconnected() {
	m.mu.Lock()
	if m.state == StateDisconnected {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	logger.Warn("ble", "connection lost (repeated communication failure), disconnecting")
	m.disconnect()
}

// clearStatesLocked 清空硬件实时状态（调用方持有 m.mu）。
func (m *BLEManager) clearStatesLocked() {
	for i := 0; i < MaxFanChannels; i++ {
		m.fanStates[i] = nil
	}
	for i := 0; i < MaxSwitchChannels; i++ {
		m.switchStates[i] = nil
	}
	for i := 0; i < MaxSensorChannels; i++ {
		m.sensorReadings[i] = nil
	}
	m.lastSensorPush = time.Time{}
	m.decryptFailCount = 0
	m.deviceInfo.SessionType = SessionPlain
	m.deviceInfo.RunState = RunStateUnknown
	m.deviceInfo.Version = ""
}

// ============================================================
// 初始化握手（固定密钥方案）
// 连接即建立加密会话。设备工作状态由 INIT 决定：
//   - 未初始化(UNINIT) → 加密 INIT → WORK_RUN
//   - 已初始化 → 加密 INIT 返回 STATE NOT UNINIT → 加密 PING 验证 → WORK_RUN
// ============================================================

// InitiateHandshake 初始化设备（固定密钥方案，二进制帧协议）。
func (m *BLEManager) InitiateHandshake() error {
	if m.GetState() != StateConnected {
		return fmt.Errorf("设备未连接")
	}

	// ============ 第 1 步：明文 HELLO 探测设备状态 ============
	// 无论固件是否已初始化，明文 HELLO 都会被识别（固件解密失败时兜底按明文解析）
	helloPayload, errCode, err := m.sendPlainCommand(bpCmdHello, nil)
	if err != nil {
		return fmt.Errorf("HELLO 探测失败: %w", err)
	}
	if errCode != bpErrOK || len(helloPayload) < 2 {
		return fmt.Errorf("HELLO 应答异常: %s", bpErrText(errCode))
	}
	devState := helloPayload[1] // 0=UNINIT，1=已初始化
	m.mu.Lock()
	addr := m.deviceInfo.Address
	m.mu.Unlock()
	logger.Debug("ble", "handshake: HELLO ok, device state=%d addr=%s", devState, addr)

	if devState != 0 {
		// ============ 已初始化设备：装载本地会话密钥，加密 PING 验证 ============
		key := m.getDeviceKey(addr)
		if len(key) != AESKeyLen {
			return fmt.Errorf("本地无 %s 的会话密钥（固件已初始化），请重置固件后重新初始化", strings.ToUpper(addr))
		}
		if err := m.sec.SetKey(key); err != nil {
			return fmt.Errorf("装载会话密钥失败: %w", err)
		}
		pingPayload, pingErr, err := m.sendBinaryCommand(bpCmdPing, nil)
		if err != nil {
			return fmt.Errorf("加密会话验证失败: %w", err)
		}
		if pingErr != bpErrOK {
			return fmt.Errorf("会话密钥不匹配（%s），固件可能已被重置，请重置后重新初始化", bpErrText(pingErr))
		}
		// P7：验证通过后才置 Encrypted，失败时保持 Plain 不会误导状态展示
		m.setSessionType(SessionEncrypted)
		// 顺带解析固件 PING 应答的信号质量（连接早期即展示）
		m.applyPingQuality(pingPayload)
		m.setRunState(RunStateWorkRun)
		logger.Info("ble", "device already initialized, encrypted session verified (WORK_RUN)")
		return nil
	}

	// ============ UNINIT 设备：RSA-OAEP 密钥分发 ============
	// 1) 生成 RSA-2048 密钥对，保留私钥、只发公钥(DER)
	priv, pubDER, err := GenerateRSAKeyPair()
	if err != nil {
		return err
	}

	// 清空残留密文事件（上次握手失败可能遗留，避免误取旧密文）
	for {
		select {
		case <-m.keCtCh:
			continue
		default:
		}
		break
	}

	// 2) 公钥分片发送（每片 ≤ 240B，2 片 135B+135B）
	const chunkLen = 135
	chunks := (len(pubDER) + chunkLen - 1) / chunkLen
	for i := 0; i < chunks; i++ {
		end := (i + 1) * chunkLen
		if end > len(pubDER) {
			end = len(pubDER)
		}
		part := pubDER[i*chunkLen : end]
		payload := make([]byte, 2+len(part))
		payload[0] = byte(i)
		payload[1] = byte(chunks)
		copy(payload[2:], part)
		_, ackErr, err := m.sendPlainCommand(bpCmdKePub, payload)
		if err != nil {
			return fmt.Errorf("公钥分片 %d/%d 发送失败: %w", i+1, chunks, err)
		}
		if ackErr != bpErrOK {
			return fmt.Errorf("公钥分片 %d/%d 被拒: %s", i+1, chunks, bpErrText(ackErr))
		}
		logger.Debug("ble", "handshake: pubkey chunk %d/%d sent", i+1, chunks)
	}

	// 3) 等待固件 EVT_KE_CT 密文事件（256B 分 2 片，拼接后通知）
	var ciphertext []byte
	select {
	case ciphertext = <-m.keCtCh:
	case <-time.After(keCtTimeout):
		return fmt.Errorf("等待固件密文超时")
	}
	if len(ciphertext) != RSACipherLen {
		return fmt.Errorf("固件密文长度异常: %d", len(ciphertext))
	}

	// 4) 私钥解密得到对称密钥，随后立即销毁非对称私钥（用户拍板：密钥分发完成后删除）
	key, err := RSADecryptOAEP(priv, ciphertext)
	if err != nil {
		return fmt.Errorf("对称密钥解密失败: %w", err)
	}
	priv.Primes = nil // 显式清除素数，避免私钥残留内存
	priv = nil

	// 5) 装载对称密钥，发送加密 KE_CONFIRM（随机 nonce），固件解密后回显
	if err := m.sec.SetKey(key); err != nil {
		return fmt.Errorf("装载对称密钥失败: %w", err)
	}
	nonce := make([]byte, AESKeyLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("生成 nonce 失败: %w", err)
	}
	cfmPayload, cfmErr, err := m.sendBinaryCommand(bpCmdKeCfm, nonce)
	if err != nil {
		return fmt.Errorf("KE_CONFIRM 发送失败: %w", err)
	}
	if cfmErr != bpErrOK {
		return fmt.Errorf("KE_CONFIRM 被拒: %s", bpErrText(cfmErr))
	}
	// 回显校验：固件应原样返回 nonce
	if len(cfmPayload) < 1+AESKeyLen || !bytes.Equal(cfmPayload[1:1+AESKeyLen], nonce) {
		return fmt.Errorf("KE_CONFIRM 回显校验失败")
	}

	// P7：KE_CONFIRM 回显校验通过后才置 Encrypted，失败时保持 Plain
	m.setSessionType(SessionEncrypted)
	// 6) 双方持久化对称密钥（上位机按 MAC 存 settings；固件在 KE_CONFIRM 处理中落 NVS）
	m.saveDeviceKey(addr, key)
	m.setRunState(RunStateWorkRun)
	logger.Info("ble", "key exchange complete, device initialized (WORK_RUN)")
	return nil
}

// ============================================================
// 指令发送与应答处理
// ============================================================

// sendBinaryCommand 发送二进制指令并等待匹配 seq 的应答。
// 返回 (应答载荷, 错误码, 错误)。应答载荷首字节为 ErrorCode。
func (m *BLEManager) sendBinaryCommand(cmdID byte, payload []byte) ([]byte, uint8, error) {
	m.cmdMu.Lock()
	defer m.cmdMu.Unlock()

	if m.char == nil {
		return nil, 0, fmt.Errorf("BLE 特征未就绪")
	}
	if m.sec == nil || !m.sec.HasKey() {
		return nil, 0, fmt.Errorf("未建立加密会话")
	}

	// 分配请求序列号（1~255 回绕）
	m.mu.Lock()
	m.seq++
	if m.seq == 0 {
		m.seq = 1
	}
	seq := m.seq
	m.mu.Unlock()

	frame, err := buildBinaryFrame(seq, cmdID, payload)
	if err != nil {
		return nil, 0, err
	}
	enc, err := m.sec.Encrypt(frame)
	if err != nil {
		return nil, 0, fmt.Errorf("指令加密失败: %w", err)
	}

	// 清空旧的应答残留，防止读到上一条的迟到应答
	for {
		select {
		case <-m.responseCh:
			continue
		default:
		}
		break
	}

	if _, err := m.char.Write(enc); err != nil {
		m.noteCommFailure()
		return nil, 0, fmt.Errorf("写特征值失败: %w", err)
	}
	m.noteCommOK()
	logger.Debug("ble", "TX >> [0x%02X seq=%d] %d bytes: %s | %s", cmdID, seq, len(enc), hex.EncodeToString(enc), bpDecodeCmd(cmdID, payload))

	// 等待匹配 seq 的应答（指令串行化，异步事件不进入 responseCh）
	deadline := time.After(cmdTimeout)
	for {
		select {
		case rf := <-m.responseCh:
			if rf.seq != seq {
				continue
			}
			if len(rf.payload) == 0 {
				return nil, bpErrUnknownCmd, fmt.Errorf("应答载荷为空")
			}
			logger.Debug("ble", "RX << [0x%02X seq=%d] payload=%d | %s", rf.cmdID, rf.seq, len(rf.payload), bpDecodeCmd(rf.cmdID, rf.payload))
			return rf.payload, rf.payload[0], nil
		case <-deadline:
			// 指令超时也是通信失败的一种：累计 ≥3 次触发断线重连（M4）
			m.noteCommFailure()
			return nil, 0, fmt.Errorf("指令超时（%v）", cmdTimeout)
		}
	}
}

// sendPlainCommand 明文发送指令（密钥交换阶段，无会话密钥时使用）。
// 与 sendBinaryCommand 共用 seq 分配与应答通道，仅跳过加密步骤。
func (m *BLEManager) sendPlainCommand(cmdID byte, payload []byte) ([]byte, uint8, error) {
	m.cmdMu.Lock()
	defer m.cmdMu.Unlock()

	if m.char == nil {
		return nil, 0, fmt.Errorf("BLE 特征未就绪")
	}

	// 分配请求序列号（1~255 回绕）
	m.mu.Lock()
	m.seq++
	if m.seq == 0 {
		m.seq = 1
	}
	seq := m.seq
	m.mu.Unlock()

	frame, err := buildBinaryFrame(seq, cmdID, payload)
	if err != nil {
		return nil, 0, err
	}

	// 清空旧的应答残留，防止读到上一条的迟到应答
	for {
		select {
		case <-m.responseCh:
			continue
		default:
		}
		break
	}

	if _, err := m.char.Write(frame); err != nil {
		m.noteCommFailure()
		return nil, 0, fmt.Errorf("写特征值失败: %w", err)
	}
	m.noteCommOK()
	logger.Debug("ble", "TX >> plain [0x%02X seq=%d] %d bytes: %s", cmdID, seq, len(frame), hex.EncodeToString(frame))

	// 等待匹配 seq 的明文应答
	deadline := time.After(cmdTimeout)
	for {
		select {
		case rf := <-m.responseCh:
			if rf.seq != seq {
				continue
			}
			if len(rf.payload) == 0 {
				return nil, bpErrUnknownCmd, fmt.Errorf("应答载荷为空")
			}
			logger.Debug("ble", "RX << plain [0x%02X seq=%d] payload=%d | %s", rf.cmdID, rf.seq, len(rf.payload), bpDecodeCmd(rf.cmdID, rf.payload))
			return rf.payload, rf.payload[0], nil
		case <-deadline:
			return nil, 0, fmt.Errorf("指令超时（%v）", cmdTimeout)
		}
	}
}

// handleNotify BLE Notify 回调：解密 → 解析二进制帧 → 应答/事件分流。
func (m *BLEManager) handleNotify(data []byte) {
	if len(data) == 0 {
		return
	}

	// 会话密钥方案：有密钥 → 加密解析；无密钥 → 明文阶段（密钥交换：HELLO ACK / PUBKEY ACK / EVT_KE_CT）
	if m.sec.HasKey() {
		plain, err := m.sec.Decrypt(data)
		if err != nil {
			logger.Warn("ble", "decrypt notify failed: %v", err)
			m.mu.Lock()
			m.decryptFailCount++
			if m.decryptFailCount >= 3 {
				m.decryptFailCount = 0
				m.mu.Unlock()
				// 连续解密失败说明当前密钥已失效（如固件被重置回明文模式）：
				// 必须同时清除内存会话密钥，否则 handleNotify 仍按加密解析固件的明文帧
				m.sec.ClearKey()
				m.setSessionType(SessionPlain)
				m.setRunState(RunStateUninit)
				logger.Warn("ble", "连续 3 次解密失败，已降级为明文会话，请重新执行初始化握手")
			} else {
				m.mu.Unlock()
			}
			return
		}

		seq, cmdID, payload, ok := parseBinaryFrame(plain)
		if !ok {
			logger.Warn("ble", "invalid binary frame after decrypt (%d bytes)", len(plain))
			return
		}

		logger.Debug("ble", "RX << [0x%02X seq=%d] payload=%d | %s", cmdID, seq, len(payload), bpDecodeCmd(cmdID, payload))

		switch cmdID {
		case bpRespAck:
			// 指令应答：携带请求序列号，送入应答通道
			select {
			case m.responseCh <- respFrame{seq: seq, cmdID: cmdID, payload: payload}:
			default:
				logger.WarnRateLimited("ble", "resp-ch-full", 30*time.Second, "response channel full, dropping ack seq=%d", seq)
			}
		case bpEvtBtn:
			m.handleButtonEvent(payload)
		case bpEvtSensor:
			m.handleSensorFrame(payload)
		case bpEvtWarn:
			m.handleWarnEvent(payload)
		default:
			logger.Warn("ble", "unknown cmd 0x%02X (seq=%d)", cmdID, seq)
		}
		return
	}

	// 明文阶段：解析二进制帧（密钥交换期间，固件无会话密钥时按明文发送）
	seq, cmdID, payload, ok := parseBinaryFrame(data)
	if !ok {
		logger.Debug("ble", "plaintext frame parse failed (%d bytes), ignoring", len(data))
		return
	}
	logger.Debug("ble", "RX << plain [0x%02X seq=%d] payload=%d | %s", cmdID, seq, len(payload), bpDecodeCmd(cmdID, payload))
	switch cmdID {
	case bpRespAck:
		select {
		case m.responseCh <- respFrame{seq: seq, cmdID: cmdID, payload: payload}:
		default:
			logger.WarnRateLimited("ble", "resp-ch-full", 30*time.Second, "response channel full, dropping ack seq=%d", seq)
		}
	case bpEvtKeCt:
		m.handleKeCiphertextEvent(payload)
	case bpEvtBtn:
		m.handleButtonEvent(payload)
	case bpEvtSensor:
		m.handleSensorFrame(payload)
	case bpEvtWarn:
		m.handleWarnEvent(payload)
	default:
		logger.Debug("ble", "unknown plain cmd 0x%02X (seq=%d)", cmdID, seq)
	}
}

// handleKeCiphertextEvent 拼接 EVT_KE_CT 密文分片（RSA-OAEP 密文 256B 分 2 片明文事件）
func (m *BLEManager) handleKeCiphertextEvent(payload []byte) {
	if len(payload) < 3 { // [ChunkIdx][ChunkCount][Data]
		return
	}
	idx := int(payload[0])
	total := int(payload[1])
	chunk := payload[2:]
	if total <= 0 || idx >= total || len(chunk) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if idx == 0 {
		m.keCtBuf = nil
		m.keCtGot = 0
		m.keCtTotal = total
	}
	if m.keCtTotal != total || idx != m.keCtGot {
		// 分片序号不连续：丢弃重新等
		m.keCtBuf = nil
		m.keCtGot = 0
		return
	}
	m.keCtBuf = append(m.keCtBuf, chunk...)
	m.keCtGot++
	if m.keCtGot == m.keCtTotal {
		ct := m.keCtBuf
		m.keCtBuf = nil
		m.keCtGot = 0
		select {
		case m.keCtCh <- ct:
		default:
			logger.Warn("ble", "keCt channel full, dropping ciphertext event")
		}
	}
}

// ============================================================
// 会话密钥生命周期（I1 安全模型文档）
//
//   生成 → 验证 → 持久化 → 重连复用 → 固件重置时清除
//
//   1. 生成：UNINIT 设备走 RSA-OAEP 密钥分发（见 InitiateHandshake），
//      固件用上位机公钥加密随机对称密钥回传，上位机私钥解密。
//   2. 验证：发送加密 KE_CONFIRM（随机 nonce），固件解密后原样回显，
//      回显匹配才算密钥生效（见 InitiateHandshake 第 5~6 步）。
//   3. 持久化：saveDeviceKey 按 MAC（大写）存入 settings.DeviceKeys，
//      经 store.SaveSettings 落盘。对外 API 经 sanitizeSettings 剥离（S2）。
//   4. 重连复用：已初始化设备走 getDeviceKey 装载本地密钥 → 加密 PING 验证。
//   5. 清除：固件被重置回 UNINIT（解密连续失败 3 次）时 ClearKey + Plain。
// ============================================================

// getDeviceKey 按 MAC 取本地保存的会话密钥（大写 MAC 为键）
func (m *BLEManager) getDeviceKey(addr string) []byte {
	m.mu.RLock()
	defer m.mu.RUnlock()
	key, ok := m.settings.DeviceKeys[strings.ToUpper(addr)]
	if !ok {
		return nil
	}
	return key
}

// saveDeviceKey 持久化会话密钥（按 MAC），密钥分发完成后调用
func (m *BLEManager) saveDeviceKey(addr string, key []byte) {
	m.mu.Lock()
	if m.settings.DeviceKeys == nil {
		m.settings.DeviceKeys = map[string][]byte{}
	}
	m.settings.DeviceKeys[strings.ToUpper(addr)] = append([]byte(nil), key...)
	s := m.settings
	m.mu.Unlock()
	m.store.SaveSettings(s)
	logger.Debug("ble", "saved session key for %s", strings.ToUpper(addr))
}

// handleSensorData 处理传感器通道数据（EVT_SENSOR 帧解析结果）。
// 收到传感器推送视为一次 ping，记录时间供心跳循环跳过 PING。
func (m *BLEManager) handleSensorData(rd *SensorChannelReading) {
	m.mu.Lock()
	if rd.ChID >= 0 && rd.ChID < MaxSensorChannels {
		m.sensorReadings[rd.ChID] = rd
	}
	m.lastSensorPush = time.Now()
	m.mu.Unlock()
	m.broadcastState()
}

// handleSensorFrame 解析 EVT_SENSOR 0x82 帧载荷。
// 载荷 = [ChId(1)][Kind(1)][Temp f32][Hum f32][Press f32][Alt f32] = 18B，小端。
func (m *BLEManager) handleSensorFrame(payload []byte) {
	if len(payload) < bpEvtSensorLen {
		logger.Warn("ble", "EVT_SENSOR payload too short: %d", len(payload))
		return
	}
	rd := &SensorChannelReading{
		ChID:        int(payload[0]),
		Kind:        SensorKind(payload[1]),
		State:       "OK",
		Temperature: float64(math.Float32frombits(binary.LittleEndian.Uint32(payload[2:6]))),
		Humidity:    float64(math.Float32frombits(binary.LittleEndian.Uint32(payload[6:10]))),
		Pressure:    float64(math.Float32frombits(binary.LittleEndian.Uint32(payload[10:14]))),
		Altitude:    float64(math.Float32frombits(binary.LittleEndian.Uint32(payload[14:18]))),
		UpdatedAt:   time.Now().Format(time.RFC3339),
	}
	// 该传感器不提供的字段固件填 NaN，主机侧归零便于展示
	if math.IsNaN(rd.Temperature) {
		rd.Temperature = 0
	}
	if math.IsNaN(rd.Humidity) {
		rd.Humidity = 0
	}
	if math.IsNaN(rd.Pressure) {
		rd.Pressure = 0
	}
	if math.IsNaN(rd.Altitude) {
		rd.Altitude = 0
	}
	logger.Debug("ble", "sensor event ch%d kind=%d: %.2fC %.2f%% %.1fPa %.1fm",
		rd.ChID, rd.Kind, rd.Temperature, rd.Humidity, rd.Pressure, rd.Altitude)
	m.handleSensorData(rd)
}

// handleButtonEvent 处理 EVT_BTN 0x81 按键事件。
// 载荷 = [SwId(1)][SwState(1)]。
func (m *BLEManager) handleButtonEvent(payload []byte) {
	if len(payload) < 2 {
		return
	}
	n := int(payload[0])
	state := int(payload[1])
	// 载荷第 3 字节为事件条件（硬件按钮/上电自动上线/指令）；旧固件 2B 载荷默认为硬件按钮
	cond := uint8(bpCondButton)
	if len(payload) >= 3 {
		cond = payload[2]
	}
	if n < 1 || n > MaxSwitchChannels {
		return
	}
	logger.Info("ble", "button event: SW%d %s cond=%d", n, btnStateText(state), cond)

	if n >= 1 && n <= MaxSwitchChannels {
		m.updateSwitchState(n, state)
		if m.disk != nil {
			go m.disk.OnSwitchChanged(n, state == 1, cond)
		}
	}
}

// readOfflineEvents 读取固件离线事件（蓝牙离线期间 SWn 的状态变化）。
// 固件随包携带"固件当前毫秒时间"，上位机以接收时刻反推事件真实发生时间：
//
//	事件真实时间 = 接收时刻 - (固件当前ms - 事件ms)   （有符号差，兼容 millis 回绕）
//
// 反推结果写入硬盘组操作日志（仅记日志，不执行挂载/卸载——事件已发生，状态由全量查询同步）。
func (m *BLEManager) readOfflineEvents() {
	if m.GetState() != StateConnected {
		return
	}
	payload, errCode, err := m.sendBinaryCommand(bpCmdGetOffline, nil)
	if err != nil {
		logger.Warn("ble", "read offline events failed: %v", err)
		return
	}
	if errCode != bpErrOK || len(payload) < 6 {
		logger.Warn("ble", "read offline events bad ack: err=%s len=%d", bpErrText(errCode), len(payload))
		return
	}
	tRecv := time.Now()
	fwNow := binary.LittleEndian.Uint32(payload[1:5])
	n := int(payload[5])
	if n == 0 {
		return
	}
	if len(payload) < 6+n*7 {
		logger.Warn("ble", "offline events payload truncated: n=%d len=%d", n, len(payload))
		return
	}
	for i := 0; i < n; i++ {
		off := 6 + i*7
		evMs := binary.LittleEndian.Uint32(payload[off : off+4])
		swID := int(payload[off+4])
		state := payload[off+5]
		cond := payload[off+6]
		delta := int32(fwNow - evMs) // 有符号毫秒差，兼容 millis 回绕（约 49.7 天）
		real := tRecv.Add(-time.Duration(delta) * time.Millisecond)
		logger.Info("ble", "offline event: SW%d %s cond=%d fwNow=%dms evMs=%dms delta=%dms real=%s",
			swID, btnStateText(int(state)), cond, fwNow, evMs, delta, real.Format("2006-01-02 15:04:05"))
		if m.disk != nil {
			m.disk.LogOfflineSwitchEvent(swID, state == 1, cond, real)
		}
	}
}

// handleWarnEvent 处理 EVT_WARN 0x83 告警事件。
// 载荷 = [WarnCode(1)]；1=心跳超时。
func (m *BLEManager) handleWarnEvent(payload []byte) {
	if len(payload) < 1 {
		return
	}
	code := payload[0]
	if code != 0x01 {
		logger.Warn("ble", "unknown warn event code 0x%02X", code)
		return
	}
	logger.Warn("ble", "heartbeat timeout event (WARN 0x01)")
	m.mu.Lock()
	for i := 0; i < MaxFanChannels; i++ {
		if m.fanStates[i] != nil {
			m.fanStates[i].Heartbeat = "TIMEOUT"
			m.fanStates[i].UpdatedAt = time.Now().Format(time.RFC3339)
		}
	}
	m.mu.Unlock()
	m.broadcastState()
}

// ============================================================
// 业务指令封装（二进制帧）
// ============================================================

// SetFanSpeed 下发风扇转速（带 350ms 节流）。fanID: 1/2，speed: 0-100。
func (m *BLEManager) SetFanSpeed(fanID, speed int) (CommandResult, error) {
	if fanID < 1 || fanID > MaxFanChannels {
		return CommandResult{OK: false, Message: "风扇编号超出范围"}, fmt.Errorf("fan id out of range")
	}
	if speed < 0 || speed > 100 {
		return CommandResult{OK: false, Message: "转速超出范围 0-100"}, fmt.Errorf("speed out of range")
	}
	if !m.IsEncryptedSession() {
		return CommandResult{OK: false, Message: "未建立加密会话"}, fmt.Errorf("session not encrypted")
	}

	// 350ms 节流：同值在窗口内不下发
	now := time.Now()
	m.mu.Lock()
	if !m.spdLastTime[fanID].IsZero() && now.Sub(m.spdLastTime[fanID]) < spdThrottle && m.spdLastValue[fanID] == speed {
		m.mu.Unlock()
		return CommandResult{OK: true, Message: "节流跳过（同值 350ms 内）"}, nil
	}
	m.mu.Unlock()

	// SPD 载荷: [FanId(1)][Spd(1)]
	payload := []byte{byte(fanID), byte(speed)}
	_, errCode, err := m.sendBinaryCommand(bpCmdSPD, payload)
	if err != nil {
		return CommandResult{OK: false, Message: err.Error()}, err
	}
	if errCode != bpErrOK {
		return CommandResult{OK: false, Message: bpErrText(errCode), Raw: errCodeText(errCode)}, nil
	}

	// P5：节流时间戳仅在指令成功后才记录，避免失败后被节流跳过导致无法重试
	m.mu.Lock()
	m.spdLastTime[fanID] = now
	m.spdLastValue[fanID] = speed
	if m.fanStates[fanID-1] != nil {
		m.fanStates[fanID-1].TargetSpd = speed
		m.fanStates[fanID-1].UpdatedAt = time.Now().Format(time.RFC3339)
	}
	m.mu.Unlock()
	m.broadcastState()

	// 固件不主动推送实际转速（仅 GETF 查询可拿到 Cur），下发成功后异步回读一次，
	// 闭环更新 CurSpd 并广播，避免"目标转速在变、实际转速显示冻结"。
	go m.readBackFanSpeed(fanID)

	return CommandResult{OK: true, Message: "SPD OK"}, nil
}

// readBackFanSpeed 下发转速后回读一次该风扇实际转速（GETF，1s 节流）。
// 固件无转速推送事件，实际转速仅在 GETF 应答中返回；不回读则前端实际转速
// 只能停留在上次查询值。
func (m *BLEManager) readBackFanSpeed(fanID int) {
	if fanID < 1 || fanID > MaxFanChannels {
		return
	}
	now := time.Now()
	m.mu.Lock()
	if !m.fanReadLast[fanID].IsZero() && now.Sub(m.fanReadLast[fanID]) < time.Second {
		m.mu.Unlock()
		return
	}
	m.fanReadLast[fanID] = now
	m.mu.Unlock()
	if err := m.syncFan(fanID); err != nil {
		logger.Debug("ble", "readback fan%d speed failed: %v", fanID, err)
		return
	}
	m.broadcastState()
}

// SetSwitch 下发开关电平。swID: 1-4，state: 0/1。
func (m *BLEManager) SetSwitch(swID, state int) (CommandResult, error) {
	if swID < 1 || swID > MaxSwitchChannels {
		return CommandResult{OK: false, Message: "开关编号超出范围"}, fmt.Errorf("switch id out of range")
	}
	if state != 0 && state != 1 {
		return CommandResult{OK: false, Message: "电平必须为 0 或 1"}, fmt.Errorf("state must be 0 or 1")
	}
	if !m.IsEncryptedSession() {
		return CommandResult{OK: false, Message: "未建立加密会话"}, fmt.Errorf("session not encrypted")
	}

	// SW 载荷: [SwId(1)][State(1)]
	payload := []byte{byte(swID), byte(state)}
	_, errCode, err := m.sendBinaryCommand(bpCmdSW, payload)
	if err != nil {
		return CommandResult{OK: false, Message: err.Error()}, err
	}
	if errCode != bpErrOK {
		return CommandResult{OK: false, Message: bpErrText(errCode), Raw: errCodeText(errCode)}, nil
	}

	m.updateSwitchState(swID, state)
	return CommandResult{OK: true, Message: "SW OK"}, nil
}

// cfgKeyOf 将配置项名映射为 CfgKeyId；未知返回 0。
func cfgKeyOf(target byte, key string) (byte, bool) {
	upper := strings.ToUpper(key)
	switch target {
	case bpCfgTargetFN:
		switch upper {
		case "ENABLED":
			return bpKeyFnEnabled, true
		case "POWER_SPD":
			return bpKeyFnPowerSpd, true
		case "HB_FALLBACK_SPD":
			return bpKeyFnHbFallback, true
		case "PWM_FREQ":
			return bpKeyFnPwmFreq, true
		}
	case bpCfgTargetSW:
		switch upper {
		case "ENABLED":
			return bpKeySwEnabled, true
		case "POWER_ON_STATE":
			return bpKeySwPowerOnState, true
		case "POWER_ON_DELAY":
			return bpKeySwPowerOnDelay, true
		case "SYSTEM":
			return bpKeySwSystem, true
		}
	case bpCfgTargetSensor:
		switch upper {
		case "KIND":
			return bpKeySensorKind, true
		case "ADDR":
			return bpKeySensorAddr, true
		case "ENABLED":
			return bpKeySensorEnabled, true
		case "INTERVAL_SEC":
			return bpKeySensorInterval, true
		}
	case bpCfgTargetGlobal:
		switch upper {
		case "HB_TIMEOUT_SEC":
			return bpKeyGlobalHbTimeout, true
		case "DEBUG_MODE":
			return bpKeyGlobalDebugMode, true
		}
	}
	return 0, false
}

// cfgValueBytes 将配置值按 CfgKeyId 宽度编码为字节。
func cfgValueBytes(key byte, val int) ([]byte, bool) {
	switch key {
	case bpKeyFnPwmFreq, bpKeySwPowerOnDelay:
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, uint32(val))
		return b, true
	case bpKeySensorInterval, bpKeyGlobalHbTimeout:
		b := make([]byte, 2)
		binary.LittleEndian.PutUint16(b, uint16(val))
		return b, true
	default:
		return []byte{byte(val)}, true
	}
}

// sendCfg 下发 CFG 指令（TargetType + TargetId + CfgKeyId + Value）。
func (m *BLEManager) sendCfg(target byte, targetID byte, key string, val int) (CommandResult, error) {
	if !m.IsEncryptedSession() {
		return CommandResult{OK: false, Message: "未建立加密会话"}, fmt.Errorf("session not encrypted")
	}
	keyID, ok := cfgKeyOf(target, key)
	if !ok {
		return CommandResult{OK: false, Message: "未知配置项: " + key}, fmt.Errorf("unknown cfg key %s", key)
	}
	vb, _ := cfgValueBytes(keyID, val)
	return m.sendCfgRaw(target, targetID, keyID, vb)
}

// sendCfgRaw 下发 CFG 指令，值为原始字节（用于字符串值如 BLE_NAME，或通用数字值）。
func (m *BLEManager) sendCfgRaw(target byte, targetID byte, keyID byte, valBytes []byte) (CommandResult, error) {
	if !m.IsEncryptedSession() {
		return CommandResult{OK: false, Message: "未建立加密会话"}, fmt.Errorf("session not encrypted")
	}
	payload := append([]byte{target, targetID, keyID}, valBytes...)
	_, errCode, err := m.sendBinaryCommand(bpCmdCFG, payload)
	if err != nil {
		return CommandResult{OK: false, Message: err.Error()}, err
	}
	if errCode != bpErrOK {
		return CommandResult{OK: false, Message: bpErrText(errCode), Raw: errCodeText(errCode)}, nil
	}
	return CommandResult{OK: true, Message: "CFG OK"}, nil
}

// SetFanConfig 下发风扇配置项。
// 修复 #22：CFG 下发成功后同步更新 BLEManager 内部 fanStates 缓存，
// 避免 /api/status 或 WS state_update 返回旧值覆盖前端刚设置的配置。
func (m *BLEManager) SetFanConfig(fanID int, key string, val int) (CommandResult, error) {
	if fanID < 1 || fanID > MaxFanChannels {
		return CommandResult{OK: false, Message: "风扇编号超出范围"}, fmt.Errorf("fan id out of range")
	}
	res, err := m.sendCfg(bpCfgTargetFN, byte(fanID), key, val)
	if err != nil {
		return res, err
	}
	if !res.OK {
		return res, nil
	}

	m.mu.Lock()
	// 同步内部硬件状态缓存（/api/status 数据源）
	if m.fanStates[fanID-1] != nil {
		switch strings.ToUpper(key) {
		case "ENABLED":
			m.fanStates[fanID-1].Enabled = val == 1
		case "POWER_SPD":
			m.fanStates[fanID-1].PowerSpd = val
		case "HB_FALLBACK_SPD":
			m.fanStates[fanID-1].HBFallback = val
		case "PWM_FREQ":
			m.fanStates[fanID-1].PWMFreq = val
		}
		m.fanStates[fanID-1].UpdatedAt = time.Now().Format(time.RFC3339)
	}
	// 同步本地 settings.fans：ENABLED 影响 thermal 自动调速判定
	for i := range m.settings.Fans {
		if m.settings.Fans[i].ID == fanID {
			if strings.EqualFold(key, "ENABLED") {
				m.settings.Fans[i].Enabled = val == 1
			}
			break
		}
	}
	m.mu.Unlock()
	m.store.SaveSettings(m.settings)
	m.broadcastState()
	return res, nil
}

// SetSwitchConfig 下发开关配置项。
// 修复 #22：CFG 下发成功后同步更新 BLEManager 内部 switchStates 缓存，
// 避免 /api/status 或 WS state_update 返回旧值覆盖前端刚设置的配置。
func (m *BLEManager) SetSwitchConfig(swID int, key string, val int) (CommandResult, error) {
	if swID < 1 || swID > MaxSwitchChannels {
		return CommandResult{OK: false, Message: "开关编号超出范围"}, fmt.Errorf("switch id out of range")
	}
	res, err := m.sendCfg(bpCfgTargetSW, byte(swID), key, val)
	if err != nil {
		return res, err
	}
	if !res.OK {
		return res, nil
	}

	m.mu.Lock()
	if m.switchStates[swID-1] != nil {
		switch strings.ToUpper(key) {
		case "ENABLED":
			m.switchStates[swID-1].Enabled = val == 1
		case "POWER_ON_STATE":
			m.switchStates[swID-1].PowerOnState = val
		case "POWER_ON_DELAY":
			m.switchStates[swID-1].PowerOnDelay = val
		case "SYSTEM":
			m.switchStates[swID-1].System = val != 0
		}
		m.switchStates[swID-1].UpdatedAt = time.Now().Format(time.RFC3339)
	}
	m.mu.Unlock()
	m.broadcastState()
	return res, nil
}

// SetGlobalConfig 下发全局配置项（FS.md 上位机#7：心跳超时阈值全局化）。
// 修复 #22：CFG 下发成功后同步更新内部 globalConfig 缓存。
// G3: 入口做范围校验——此前无校验直接下发, HB_TIMEOUT_SEC 传负数会被 uint16 溢出,
//
//	固件端会拒绝但本地缓存已写入错误值; 非法输入直接返回, 不下发
func (m *BLEManager) SetGlobalConfig(key string, val int) (CommandResult, error) {
	switch strings.ToUpper(key) {
	case "HB_TIMEOUT_SEC":
		if val < 1 || val > 3600 {
			return CommandResult{OK: false}, fmt.Errorf("HB_TIMEOUT_SEC 取值范围 1~3600 秒，收到 %d", val)
		}
	case "DEBUG_MODE":
		if val != 0 && val != 1 {
			return CommandResult{OK: false}, fmt.Errorf("DEBUG_MODE 仅允许 0/1，收到 %d", val)
		}
	default:
		return CommandResult{OK: false}, fmt.Errorf("未知全局配置项: %s", key)
	}
	res, err := m.sendCfg(bpCfgTargetGlobal, 0, key, val)
	if err != nil {
		return res, err
	}
	if res.OK {
		m.mu.Lock()
		if m.globalConfig == nil {
			m.globalConfig = &GlobalConfig{}
		}
		switch strings.ToUpper(key) {
		case "HB_TIMEOUT_SEC":
			m.globalConfig.HbTimeout = uint16(val)
		case "DEBUG_MODE":
			m.globalConfig.DebugMode = val != 0
		}
		m.mu.Unlock()
	}
	return res, nil
}

// GetGlobalConfig 查询全局配置（GETG 指令）。
// 应答载荷：[ErrorCode(1)][HbTimeout u16(2)][DebugMode u8(1)][BleNameLen u8(1)][BleName(N)]
func (m *BLEManager) GetGlobalConfig() (*GlobalConfig, error) {
	if !m.IsEncryptedSession() {
		return nil, fmt.Errorf("未建立加密会话")
	}
	resp, errCode, err := m.sendBinaryCommand(bpCmdGetG, nil)
	if err != nil {
		return nil, err
	}
	if errCode != bpErrOK {
		return nil, fmt.Errorf("设备返回错误: %s", bpErrText(errCode))
	}
	// 最小长度：err(1)+hbTimeout(2)+debugMode(1)+nameLen(1) = 5
	if len(resp) < 5 {
		return nil, fmt.Errorf("GETG 应答长度异常: %d", len(resp))
	}
	cfg := &GlobalConfig{
		HbTimeout: binary.LittleEndian.Uint16(resp[1:3]),
		DebugMode: resp[3] != 0,
	}
	nameLen := int(resp[4])
	if nameLen > 0 && len(resp) >= 5+nameLen {
		cfg.BleName = string(resp[5 : 5+nameLen])
	}
	// 同步内部缓存
	m.mu.Lock()
	m.globalConfig = cfg
	m.mu.Unlock()
	return cfg, nil
}

// GetBleName 查询设备 BLE 广播名（通过 GETG 获取）。
func (m *BLEManager) GetBleName() (string, error) {
	cfg, err := m.GetGlobalConfig()
	if err != nil {
		return "", err
	}
	return cfg.BleName, nil
}

// SetBleName 修改设备 BLE 广播名。
// 校验：长度 1~20 字节，全部为可打印 ASCII（0x20~0x7E）。
// CFG 帧：TargetType=GLOBAL(3), TargetId=0, CfgKeyId=0x32, Value=name 字节。
func (m *BLEManager) SetBleName(name string) error {
	if len(name) < 1 || len(name) > 20 {
		return fmt.Errorf("广播名长度必须在 1~20 字节之间（当前 %d）", len(name))
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		if b < 0x20 || b > 0x7E {
			return fmt.Errorf("广播名包含非法字符（0x%02X），仅允许可打印 ASCII 0x20~0x7E", b)
		}
	}
	res, err := m.sendCfgRaw(bpCfgTargetGlobal, 0, bpKeyGlobalBleName, []byte(name))
	if err != nil {
		return err
	}
	if !res.OK {
		return fmt.Errorf("设备返回错误: %s", res.Message)
	}
	// 同步内部缓存
	m.mu.Lock()
	if m.globalConfig == nil {
		m.globalConfig = &GlobalConfig{}
	}
	m.globalConfig.BleName = name
	m.mu.Unlock()
	return nil
}

// GetDebugMode 查询调试模式开关（通过 GETG 获取）。
func (m *BLEManager) GetDebugMode() (bool, error) {
	cfg, err := m.GetGlobalConfig()
	if err != nil {
		return false, err
	}
	return cfg.DebugMode, nil
}

// SetDebugMode 设置调试模式开关。
// CFG 帧：TargetType=GLOBAL(3), TargetId=0, CfgKeyId=0x33, Value=u8 0/1。
func (m *BLEManager) SetDebugMode(enabled bool) error {
	v := byte(0)
	if enabled {
		v = 1
	}
	res, err := m.sendCfgRaw(bpCfgTargetGlobal, 0, bpKeyGlobalDebugMode, []byte{v})
	if err != nil {
		return err
	}
	if !res.OK {
		return fmt.Errorf("设备返回错误: %s", res.Message)
	}
	// 同步内部缓存
	m.mu.Lock()
	if m.globalConfig == nil {
		m.globalConfig = &GlobalConfig{}
	}
	m.globalConfig.DebugMode = enabled
	m.mu.Unlock()
	return nil
}

// SaveConfig 保存硬件 NVS 配置（SAVE 指令）。
func (m *BLEManager) SaveConfig() (CommandResult, error) {
	if !m.IsEncryptedSession() {
		return CommandResult{OK: false, Message: "未建立加密会话"}, fmt.Errorf("session not encrypted")
	}
	_, errCode, err := m.sendBinaryCommand(bpCmdSave, nil)
	if err != nil {
		return CommandResult{OK: false, Message: err.Error()}, err
	}
	if errCode != bpErrOK {
		return CommandResult{OK: false, Message: bpErrText(errCode), Raw: errCodeText(errCode)}, nil
	}
	return CommandResult{OK: true, Message: "SAVE CONFIG OK"}, nil
}

// FactoryReset 恢复出厂（RESET 指令）。
// 固件 RESET 仅清空业务配置（NVS 风扇/开关/传感器/全局命名空间），
// 不清除固定密钥，加密会话在下次重连自动恢复。
func (m *BLEManager) FactoryReset() (CommandResult, error) {
	if !m.IsEncryptedSession() {
		return CommandResult{OK: false, Message: "未建立加密会话"}, fmt.Errorf("session not encrypted")
	}
	_, errCode, err := m.sendBinaryCommand(bpCmdReset, nil)
	if err != nil {
		return CommandResult{OK: false, Message: err.Error()}, err
	}
	if errCode != bpErrOK {
		return CommandResult{OK: false, Message: bpErrText(errCode), Raw: errCodeText(errCode)}, nil
	}
	// 配置已恢复默认，重新同步硬件状态（保持加密会话与本地密钥）
	if err := m.SyncAllStates(); err != nil {
		logger.Warn("ble", "factory reset sync failed: %v", err)
	}
	return CommandResult{OK: true, Message: "FACTORY RESET OK"}, nil
}

// SetSensorEnabled 启用/停用 I2C 传感器采集（下发 CFG SENSOR CH0 ENABLED，兼容旧全局开关）。
func (m *BLEManager) SetSensorEnabled(enabled bool) (CommandResult, error) {
	v := 0
	if enabled {
		v = 1
	}
	res, err := m.sendCfg(bpCfgTargetSensor, 0, "ENABLED", v)
	if err != nil {
		return res, err
	}
	if res.OK {
		m.mu.Lock()
		s := m.settings
		s.SensorEnabled = enabled
		m.settings = s
		m.mu.Unlock()
		m.store.SaveSettings(s)
		m.broadcastState()
	}
	return res, nil
}

// SetSensorInterval 设置传感器采集间隔（秒，1~30，下发 CFG SENSOR CH0 INTERVAL_SEC）。
func (m *BLEManager) SetSensorInterval(sec int) (CommandResult, error) {
	if sec < 1 || sec > 30 {
		return CommandResult{OK: false, Message: "采集间隔必须在 1~30 秒之间"}, fmt.Errorf("interval out of range")
	}
	res, err := m.sendCfg(bpCfgTargetSensor, 0, "INTERVAL_SEC", sec)
	if err != nil {
		return res, err
	}
	if res.OK {
		m.mu.Lock()
		s := m.settings
		s.SensorIntervalSec = sec
		m.settings = s
		m.mu.Unlock()
		m.store.SaveSettings(s)
		m.broadcastState()
	}
	return res, nil
}

// UpdateSensorChannel 下发单通道 I2C 传感器配置（二进制 CFG SENSOR CHn）。
// 逐键下发，任一项失败即返回；全部成功后更新本地配置并落盘。
func (m *BLEManager) UpdateSensorChannel(chID int, patch SensorChannelConfig) (CommandResult, error) {
	if chID < 0 || chID >= MaxSensorChannels {
		return CommandResult{OK: false, Message: "通道编号必须在 0~3 之间"}, fmt.Errorf("ch out of range")
	}
	if !m.IsEncryptedSession() {
		return CommandResult{OK: false, Message: "未建立加密会话"}, fmt.Errorf("session not encrypted")
	}
	// 0=不修改该字段（删除/禁用通道只下发 ENABLED/INTERVAL）
	if patch.Kind < 0 || patch.Kind > 4 {
		return CommandResult{OK: false, Message: "传感器类型必须为 0~4（0=不修改，1~4=AHT20/BMP280/LM75/HTU21D）"}, fmt.Errorf("kind out of range")
	}
	if patch.Addr < 0 || patch.Addr > 0x77 || (patch.Addr > 0 && patch.Addr < 0x08) {
		return CommandResult{OK: false, Message: "I2C 地址必须在 0x08~0x77（8~119），0=不修改"}, fmt.Errorf("addr out of range")
	}
	if patch.IntervalSec < 1 || patch.IntervalSec > 3600 {
		return CommandResult{OK: false, Message: "采集间隔必须在 1~3600 秒之间"}, fmt.Errorf("interval out of range")
	}

	m.mu.RLock()
	cur := m.settings
	if len(cur.SensorChannels) != MaxSensorChannels {
		cur.SensorChannels = DefaultSensorChannels()
	}
	oldAlias := cur.SensorChannels[chID].Alias
	m.mu.RUnlock()

	// 逐键下发（幂等，重复下发无副作用）。
	// L18: 顺序改为 先禁用→改配置→再启用：启用时固件按 (type,addr) 与最近一次初始化参数
	//      差异决定是否重初始化，避免"旧类型已 OK 后热切换类型/地址"导致驱动状态与配置不一致
	cmds := []struct {
		key string
		val int
	}{
		{"ENABLED", 0},
	}
	if patch.Kind > 0 {
		cmds = append(cmds, struct {
			key string
			val int
		}{"KIND", int(patch.Kind)})
	}
	if patch.Addr > 0 {
		cmds = append(cmds, struct {
			key string
			val int
		}{"ADDR", patch.Addr})
	}
	cmds = append(cmds, struct {
		key string
		val int
	}{"INTERVAL_SEC", patch.IntervalSec})
	if patch.Enabled {
		cmds = append(cmds, struct {
			key string
			val int
		}{"ENABLED", 1})
	}
	for _, c := range cmds {
		res, err := m.sendCfg(bpCfgTargetSensor, byte(chID), c.key, c.val)
		if err != nil {
			return res, err
		}
		if !res.OK {
			return res, nil
		}
	}

	// 全部下发成功后更新本地配置并落盘（别名/主页显示开关为本地字段）
	alias := oldAlias
	if patch.Alias != "" {
		alias = patch.Alias
	}

	m.mu.Lock()
	ns := m.settings
	if len(ns.SensorChannels) != MaxSensorChannels {
		ns.SensorChannels = DefaultSensorChannels()
	}
	ns.SensorChannels[chID] = SensorChannelConfig{
		ID:          chID,
		Alias:       alias,
		Kind:        patch.Kind,
		Addr:        patch.Addr,
		Enabled:     patch.Enabled,
		IntervalSec: patch.IntervalSec,
		ShowTemp:    patch.ShowTemp,
		ShowHumi:    patch.ShowHumi,
		ShowPress:   patch.ShowPress,
		ShowAlt:     patch.ShowAlt,
	}
	sensorOn := false
	for _, c := range ns.SensorChannels {
		if c.Enabled {
			sensorOn = true
			break
		}
	}
	ns.SensorEnabled = sensorOn
	m.settings = ns
	m.mu.Unlock()
	m.store.SaveSettings(ns)

	m.broadcastState()
	return CommandResult{OK: true, Message: "传感器通道配置已下发并保存"}, nil
}

// ============================================================
// 状态同步（二进制 GET 指令解析）
// ============================================================

// SyncAllStates 批量同步全部风扇与开关状态。
func (m *BLEManager) SyncAllStates() error {
	if !m.IsEncryptedSession() {
		return fmt.Errorf("未建立加密会话")
	}

	// 同步版本号（GETV）
	if resp, errCode, err := m.sendBinaryCommand(bpCmdGetV, nil); err == nil && errCode == bpErrOK && len(resp) > 1 {
		m.mu.Lock()
		m.deviceInfo.Version = string(resp[1:])
		m.mu.Unlock()
	}

	// 同步全部风扇
	for i := 1; i <= MaxFanChannels; i++ {
		if err := m.syncFan(i); err != nil {
			logger.Warn("ble", "sync fan %d failed: %v", i, err)
		}
	}

	// 同步全部开关
	for i := 1; i <= MaxSwitchChannels; i++ {
		if err := m.syncSwitch(i); err != nil {
			logger.Warn("ble", "sync switch %d failed: %v", i, err)
		}
	}

	// 同步传感器配置与数据
	if err := m.syncSensor(); err != nil {
		logger.Warn("ble", "sync sensor failed: %v", err)
	}

	m.broadcastState()
	return nil
}

// syncFan 同步单路风扇状态（GETF，载荷 [FanId] 1-based，与固件 BP_CMD_GETF 一致）。
// 注意：固件 GETF 已统一为 1-based（i=n-1），若仍发 0-based 会导致 FAN1 查询报
// FAN_RANGE 错误、FAN2 查询返回 FAN1 数据，UI 上 FAN1 全 0、FAN2 显示 FAN1 配置。
func (m *BLEManager) syncFan(fanID int) error {
	resp, errCode, err := m.sendBinaryCommand(bpCmdGetF, []byte{byte(fanID)})
	if err != nil {
		return err
	}
	if errCode != bpErrOK {
		return fmt.Errorf("设备返回错误: %s", bpErrText(errCode))
	}
	// 载荷: [0]=err [1]=Enabled [2..5]=PWM_FREQ u32 [6]=PowerSpd [7]=HbFallback [8]=Target [9]=Cur
	if len(resp) < 10 {
		return fmt.Errorf("GETF 应答长度异常: %d", len(resp))
	}
	pl := resp[1:]
	state := FanHWState{Index: fanID, Pin: fanPin(fanID), UpdatedAt: time.Now().Format(time.RFC3339)}
	state.Enabled = pl[0] == 1
	state.PWMFreq = int(binary.LittleEndian.Uint32(pl[1:5]))
	state.PowerSpd = int(pl[5])
	state.HBFallback = int(pl[6])
	state.TargetSpd = int(pl[7])
	state.CurSpd = int(pl[8])
	state.Heartbeat = "OK"
	m.mu.Lock()
	m.fanStates[fanID-1] = &state
	m.mu.Unlock()
	return nil
}

// syncSwitch 同步单路开关状态（GETS，载荷 [SwId] 1-based）。
// GETS 应答 9 字节：[err][Enabled][PowerOnState][PowerOnDelay u32][CurState][System]
func (m *BLEManager) syncSwitch(swID int) error {
	resp, errCode, err := m.sendBinaryCommand(bpCmdGetS, []byte{byte(swID)})
	if err != nil {
		return err
	}
	if errCode != bpErrOK {
		return fmt.Errorf("设备返回错误: %s", bpErrText(errCode))
	}
	// 载荷: [0]=err [1]=Enabled [2]=PowerOnState [3..6]=PowerOnDelay u32 [7]=CurState [8]=System
	if len(resp) < 9 {
		return fmt.Errorf("GETS 应答长度异常: %d（期望 ≥9）", len(resp))
	}
	pl := resp[1:]
	state := SwitchHWState{Index: swID, Pin: swPin(swID), UpdatedAt: time.Now().Format(time.RFC3339)}
	state.Enabled = pl[0] == 1
	state.PowerOnState = int(pl[1])
	state.PowerOnDelay = int(binary.LittleEndian.Uint32(pl[2:6]))
	state.State = int(pl[6])
	state.System = pl[7] != 0
	m.mu.Lock()
	m.switchStates[swID-1] = &state
	m.mu.Unlock()
	return nil
}

// syncSensor 同步多通道传感器配置与最新数据（GETSR）。
func (m *BLEManager) syncSensor() error {
	resp, errCode, err := m.sendBinaryCommand(bpCmdGetSR, nil)
	if err != nil {
		return err
	}
	if errCode != bpErrOK {
		return fmt.Errorf("设备返回错误: %s", bpErrText(errCode))
	}
	if len(resp) < 2+bpGetSRBlockLen {
		return fmt.Errorf("GETSR 应答长度异常: %d", len(resp))
	}

	m.mu.Lock()
	s := m.settings
	if len(s.SensorChannels) != MaxSensorChannels {
		s.SensorChannels = DefaultSensorChannels()
	}
	changed := false
	for i := 0; i < bpGetSRChMax; i++ {
		off := 2 + i*bpGetSRBlockLen
		if off+bpGetSRBlockLen > len(resp) {
			break
		}
		b := resp[off : off+bpGetSRBlockLen]
		kind := SensorKind(b[0])
		addr := int(b[1])
		enabled := b[2] == 1
		interval := int(binary.LittleEndian.Uint16(b[3:5]))
		stateOK := b[5] == 1

		if i >= len(s.SensorChannels) {
			s.SensorChannels = append(s.SensorChannels, DefaultSensorChannels()[i])
		}
		cfg := &s.SensorChannels[i]
		if cfg.Kind != kind {
			cfg.Kind = kind
			changed = true
		}
		if cfg.Addr != addr {
			cfg.Addr = addr
			changed = true
		}
		if cfg.Enabled != enabled {
			cfg.Enabled = enabled
			changed = true
		}
		if cfg.IntervalSec != interval {
			cfg.IntervalSec = interval
			changed = true
		}
		// 无二进制推送时用 GETSR 快照填充展示
		if m.sensorReadings[i] == nil {
			rd := &SensorChannelReading{
				ChID:  i,
				Kind:  kind,
				State: "FAIL",
			}
			if stateOK {
				rd.State = "OK"
			}
			rd.Temperature = float64(math.Float32frombits(binary.LittleEndian.Uint32(b[6:10])))
			rd.Humidity = float64(math.Float32frombits(binary.LittleEndian.Uint32(b[10:14])))
			rd.Pressure = float64(math.Float32frombits(binary.LittleEndian.Uint32(b[14:18])))
			rd.Altitude = float64(math.Float32frombits(binary.LittleEndian.Uint32(b[18:22])))
			if math.IsNaN(rd.Temperature) {
				rd.Temperature = 0
			}
			if math.IsNaN(rd.Humidity) {
				rd.Humidity = 0
			}
			if math.IsNaN(rd.Pressure) {
				rd.Pressure = 0
			}
			if math.IsNaN(rd.Altitude) {
				rd.Altitude = 0
			}
			rd.UpdatedAt = time.Now().Format(time.RFC3339)
			m.sensorReadings[i] = rd
		}
	}
	// 任一通道启用即视为总开关
	sensorOn := false
	for _, c := range s.SensorChannels {
		if c.Enabled {
			sensorOn = true
			break
		}
	}
	if s.SensorEnabled != sensorOn {
		s.SensorEnabled = sensorOn
		changed = true
	}
	if changed {
		m.settings = s
	}
	m.mu.Unlock()
	if changed {
		m.store.SaveSettings(s)
	}
	return nil
}

// updateSwitchState 更新开关状态缓存（来自 GET 或事件）。
func (m *BLEManager) updateSwitchState(swID, state int) {
	m.mu.Lock()
	if m.switchStates[swID-1] == nil {
		m.switchStates[swID-1] = &SwitchHWState{Index: swID, Pin: swPin(swID)}
	}
	m.switchStates[swID-1].State = state
	m.switchStates[swID-1].UpdatedAt = time.Now().Format(time.RFC3339)
	m.mu.Unlock()
	m.broadcastState()
}

// fullQueryAfterConnect 连接握手完成后执行完整硬件状态查询（上位机需求 #4）。
// 异步执行，不阻塞 Connect 返回；每个查询独立重试，最多 5 次，间隔 2 秒。
// 查询序列：GETV → 每个风扇 GETF → 每个开关 GETS → GETSR → GETG。
// 全部完成后通过 WebSocket 推送更新状态。
func (m *BLEManager) fullQueryAfterConnect() {
	// 稍候让握手后的设备状态稳定
	time.Sleep(200 * time.Millisecond)

	// retryQuery 带重试的查询封装
	retryQuery := func(name string, fn func() error) {
		for attempt := 1; attempt <= 5; attempt++ {
			if m.GetState() != StateConnected {
				logger.Warn("ble", "%s 查询中止：连接已断开", name)
				return
			}
			if err := fn(); err != nil {
				logger.Warn("ble", "%s 查询失败（第 %d/5 次）: %v", name, attempt, err)
				if attempt < 5 {
					time.Sleep(2 * time.Second)
				} else {
					logger.Warn("ble", "%s 查询在 5 次重试后仍失败，放弃", name)
				}
			} else {
				logger.Info("ble", "%s 查询成功", name)
				return
			}
		}
	}

	// GETV：版本号
	retryQuery("GETV", func() error {
		resp, errCode, err := m.sendBinaryCommand(bpCmdGetV, nil)
		if err != nil {
			return err
		}
		if errCode != bpErrOK {
			return fmt.Errorf("GETV: %s", bpErrText(errCode))
		}
		if len(resp) > 1 {
			m.mu.Lock()
			m.deviceInfo.Version = string(resp[1:])
			m.mu.Unlock()
		}
		return nil
	})

	// 每个风扇 GETF
	for i := 1; i <= MaxFanChannels; i++ {
		fanID := i
		retryQuery(fmt.Sprintf("GETF(fan%d)", fanID), func() error { return m.syncFan(fanID) })
	}

	// 每个开关 GETS
	for i := 1; i <= MaxSwitchChannels; i++ {
		swID := i
		retryQuery(fmt.Sprintf("GETS(sw%d)", swID), func() error { return m.syncSwitch(swID) })
	}

	// GETSR：传感器配置与数据
	retryQuery("GETSR", func() error { return m.syncSensor() })

	// GETG：全局配置
	retryQuery("GETG", func() error {
		cfg, err := m.GetGlobalConfig()
		if err != nil {
			return err
		}
		logger.Info("ble", "全局配置: hb_timeout=%d debug_mode=%v ble_name=%q", cfg.HbTimeout, cfg.DebugMode, cfg.BleName)
		return nil
	})

	// 全部完成后 WS 推送
	m.broadcastState()
	logger.Info("ble", "连接后完整硬件状态查询完成")
}

// ============================================================
// 心跳
// ============================================================

func (m *BLEManager) startHeartbeat() {
	m.mu.Lock()
	if m.heartbeatRunning {
		m.mu.Unlock()
		return
	}
	m.heartbeatRunning = true
	m.heartbeatStop = make(chan struct{})
	stop := m.heartbeatStop
	m.mu.Unlock()

	go m.heartbeatLoop(stop)
}

func (m *BLEManager) stopHeartbeat() {
	m.mu.Lock()
	if m.heartbeatStop != nil {
		close(m.heartbeatStop)
		m.heartbeatStop = nil
	}
	m.heartbeatRunning = false
	m.mu.Unlock()
}

func (m *BLEManager) heartbeatLoop(stop chan struct{}) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if m.GetState() != StateConnected {
				continue
			}
			// 收到传感器推送数据视为一次 ping，
			// 心跳周期内已有推送则跳过本轮 PING（重置心跳计时器）
			m.mu.RLock()
			last := m.lastSensorPush
			m.mu.RUnlock()
			if !last.IsZero() && time.Since(last) < heartbeatInterval {
				continue
			}
			// PING 超时会在 sendBinaryCommand 内部累计 noteCommFailure，
			// 连续 ≥3 次失败自动触发 markDisconnected 断线重连（M4）。
			if payload, _, err := m.sendBinaryCommand(bpCmdPing, nil); err != nil {
				logger.Warn("ble", "heartbeat ping failed: %v", err)
			} else {
				// PING 应答携带固件推算的信号质量（替代 RSSI）
				m.applyPingQuality(payload)
			}
		}
	}
}

// ============================================================
// 信号质量（固件 PING 应答携带，替代 RSSI 轮询）
// ============================================================
// 背景：BLE 连接建立后主机侧无法实时读取 RSSI（BlueZ/tinygo 限制）。
// 固件侧统计接收帧异常率（解密失败/CRC失败/重复Seq/Seq跳变）推算信号质量（0~100），
// 上位机通过 PING 应答负载 [ErrCode][SignalQuality] 获取。
// PING 应答负载向后兼容：旧固件只有 [ErrCode]，len<2 时忽略。

func (m *BLEManager) applyPingQuality(payload []byte) {
	if len(payload) < 2 {
		return // 旧固件 PING 应答只有 ErrorCode
	}
	q := int(payload[1])
	m.mu.Lock()
	changed := m.deviceInfo.SignalQuality != q
	m.deviceInfo.SignalQuality = q
	m.mu.Unlock()
	if changed {
		m.broadcastState()
		logger.Debug("ble", "signal quality from firmware ping: %d%%", q)
	}
}

// ============================================================
// 自动重连
// ============================================================

func (m *BLEManager) startReconnect() {
	m.mu.Lock()
	if m.reconnectStop != nil {
		m.mu.Unlock()
		return
	}
	m.reconnectStop = make(chan struct{})
	stop := m.reconnectStop
	m.mu.Unlock()

	// 首次尝试：启动后立即检查一次，不等 ticker
	// 这样 Connect() 内部失败后，reconnect goroutine 能快速接管重试
	go func() {
		// 给首次尝试留一点缓冲（避免 Connect() 自身的 5 次重试还在进行中）
		startedAt := time.Now()
		time.Sleep(1 * time.Second)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if m.GetState() == StateDisconnected {
				s := m.GetSettings()
				if s.AutoConnect && strings.TrimSpace(s.LastAddress) != "" {
					target := s.LastAddress
					logger.Info("ble", "auto reconnecting to %s", target)
					if err := m.Connect(); err != nil {
						logger.Debug("ble", "reconnect attempt failed: %v", err)
					}
				} else {
					logger.Debug("ble", "reconnect watcher: disconnected but no auto-connect config (AutoConnect=%v LastAddress=%q), sleeping", s.AutoConnect, s.LastAddress)
				}
			} else {
				logger.Debug("ble", "reconnect watcher: state=%s, skipping", m.GetState())
			}

			// 重试间隔：开机阶段（前 3 分钟）快速重试 5s，稳定后 15s
			var interval time.Duration
			if time.Since(startedAt) < 3*time.Minute {
				interval = 5 * time.Second
			} else {
				interval = 15 * time.Second
			}
			select {
			case <-stop:
				return
			case <-time.After(interval):
			}
		}
	}()
}

func (m *BLEManager) stopReconnect() {
	m.mu.Lock()
	if m.reconnectStop != nil {
		close(m.reconnectStop)
		m.reconnectStop = nil
	}
	m.mu.Unlock()
}

// ============================================================
// WebSocket 广播
// ============================================================

func (m *BLEManager) broadcastState() {
	if m.hub == nil {
		return
	}
	msg := StateUpdateMsg{
		Type:           "state_update",
		Device:         m.GetDeviceInfo(),
		Fans:           m.GetFanStates(),
		Switches:       m.GetSwitchStates(),
		Sensor:         m.GetSensorData(),
		SensorChannels: m.GetSensorChannels(),
		Time:           time.Now().Format(time.RFC3339),
	}
	if m.disk != nil {
		msg.DiskGroups = m.disk.GetViews()
	}
	// 携带当前设置（MAC 隔离配置）：连接/切换设备后前端据此刷新
	// directSaveNVS、I2C 通道配置、硬盘组等（前端 applyStateUpdate 处理 msg.setting）
	s := sanitizeSettings(m.GetSettings())
	msg.Setting = &s
	m.hub.Broadcast(msg)
}

// ============================================================
// 工具函数
// ============================================================

// errCodeText 生成原始错误码文本（供 Raw 字段展示）。
func errCodeText(code uint8) string {
	return fmt.Sprintf("ERR:%s", bpErrText(code))
}

func rssiToPercent(rssi int) int {
	if rssi >= -40 {
		return 100
	}
	if rssi <= -100 {
		return 0
	}
	pct := 2 * (rssi + 100)
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct
}

func btnStateText(state int) string {
	if state == 1 {
		return "按下"
	}
	return "松开"
}

// fanPin 返回风扇对应的 GPIO 引脚名（仅展示用）。
func fanPin(id int) string {
	switch id {
	case 1:
		return "GPIO21"
	case 2:
		return "GPIO20"
	}
	return ""
}

// swPin 返回开关对应的 GPIO 引脚名。
func swPin(id int) string {
	pins := []string{"GPIO4", "GPIO5", "GPIO6", "GPIO7"}
	if id >= 1 && id <= len(pins) {
		return pins[id-1]
	}
	return ""
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// 静态断言：确保 strconv 包被使用（保留给未来数值解析）。
var _ = strconv.Atoi
