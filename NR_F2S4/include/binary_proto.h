#pragma once
// =====================================================================
// NR_F2S4 BLE 二进制帧协议定义（FS.md）
// ---------------------------------------------------------------------
// 帧结构: [Magic(1)][SeqID(1)][CmdID(1)][PayloadLen(1)][Payload(N)][CRC16(2)]
//   - 总帧长 = 6 + N，小端编码
//   - CRC16-CCITT: poly=0x1021, init=0xFFFF, 无反射, xorout=0（CCITT-FALSE）
//   - CRC 覆盖范围: [0 .. 3+N]，CRC 字段本身小端存储
// 加密: 完整帧做 AES-128-GCM 封装。会话密钥由初始化密钥交换（RSA-2048-OAEP 分发）建立：
//   - UNINIT 设备：明文阶段（HELLO/PUBKEY 明文帧）→ KE_CONFIRM 后切换为加密阶段
//   - 已初始化设备：连接后直接用 NVS 保存的会话密钥加密（无密钥交换）
// 加密帧格式: [IV(12)][密文(帧)][TAG(16)]。
// =====================================================================
#include <stdint.h>
#include <stddef.h>
#include <string.h>

#define BP_MAGIC            0xAA
#define BP_HEAD_LEN         4U      // Magic + Seq + Cmd + Len
#define BP_CRC_LEN          2U
#define BP_MIN_FRAME        6U
#define BP_MAX_PAYLOAD      240U    // 单帧明文载荷上限（加密会话下受 BLE MTU 限制）

// ---- CmdID ----
enum BpCmdId : uint8_t {
  BP_CMD_INIT    = 0x01,   // 初始化（仅 UNINIT）
  BP_CMD_SPD     = 0x02,   // 风扇调速
  BP_CMD_SW      = 0x03,   // 开关控制
  BP_CMD_CFG     = 0x04,   // 配置读写（TargetType+TargetId+CfgKeyId+Value）
  BP_CMD_GETV    = 0x05,   // 版本
  BP_CMD_GETF    = 0x06,   // 风扇查询
  BP_CMD_GETS    = 0x07,   // 开关查询
  BP_CMD_GETSR   = 0x08,   // 传感器全量查询
  BP_CMD_PING    = 0x09,   // 心跳；应答负载 [ErrCode(1)][SignalQuality(1, 0~100)]
  BP_CMD_SAVE    = 0x0A,   // 配置落盘 NVS
  BP_CMD_RESET   = 0x0B,   // 恢复出厂
  BP_CMD_GETG    = 0x0C,   // 查询全局配置
  BP_CMD_HELLO   = 0x0D,   // 明文探测（上位机→固件，载荷空）；固件回明文 ACK[ErrCode][State(0=UNINIT/1=已初始化)]
  BP_CMD_KE_PUBKEY = 0x0E,// 明文密钥交换：上位机→固件，载荷 [ChunkIdx(1)][ChunkCount(1)][ChunkData(N)]；
                           //   分片拼满后固件解析 RSA-2048 公钥、生成 16B 对称密钥、OAEP-SHA256 加密，
                           //   密文(256B)分 2 片经 EVT_KE_CT 明文事件回传，末片回明文 ACK[ErrCode]
  BP_CMD_KE_CONFIRM = 0x0F,// 加密确认：上位机→固件，载荷 [Nonce(16)]；
                           //   固件解密成功后持久化对称密钥并置 WORK_WAIT，回加密 ACK[ErrCode][Nonce(16)]
  BP_CMD_GET_OFFLINE = 0x10,// 读取离线事件（加密）：固件回 [ErrCode][固件当前ms u32 LE][条数 u8][条目×N]，
                           //   条目 = [time_ms u32 LE][swId][state][cond] 共 7B；发送后固件清空缓冲
  BP_RESP_ACK    = 0x80,   // 应答（通用），载荷首个字节为 ErrorCode
  BP_EVT_BTN     = 0x81,   // 按键/SWn 状态事件（SeqID=0），载荷 [SwId][State][Cond]（Cond 见 SwEventCond）
  BP_EVT_SENSOR  = 0x82,   // 传感器周期上报（SeqID=0）
  BP_EVT_WARN    = 0x83,   // 心跳超时告警（SeqID=0）
  BP_EVT_KE_CT   = 0x85,   // 密钥交换密文事件（明文, SeqID=0, 载荷 [ChunkIdx][ChunkCount][密文片]；256B 密文分 2 片）
};

// ---- ErrorCode（对齐原文本协议错误语义）----
enum BpError : uint8_t {
  BP_ERR_OK                 = 0,
  BP_ERR_UNKNOWN_CMD        = 1,
  BP_ERR_SEC_REQUIRED       = 2,   // 需要加密会话 / 未授权连接
  BP_ERR_DECRYPT_FAIL       = 3,
  BP_ERR_STATE_NOT_UNINIT   = 4,   // 设备非 UNINIT 状态
  BP_ERR_KEY_DECRYPT_FAIL   = 5,   // 保留（固定密钥方案不再使用）
  BP_ERR_SAVE_AUTH_FAIL     = 6,   // 保存授权失败
  BP_ERR_VALUE_OUTOFRANGE   = 7,   // 值越界
  BP_ERR_FAN_DISABLED       = 8,
  BP_ERR_FAN_SPD_RANGE      = 9,
  BP_ERR_SW_STATE_INVALID   = 10,
  BP_ERR_SW_DISABLED        = 11,
  BP_ERR_SW_MUST_TURN_OFF   = 12,  // 保留
  BP_ERR_CFG_TARGET_UNKNOWN = 13,
  BP_ERR_CFG_KEY_UNKNOWN    = 14,
  BP_ERR_SENSOR_CH_RANGE    = 15,
  BP_ERR_SENSOR_TYPE_NONE   = 16,
  BP_ERR_SENSOR_NOT_READY   = 17,
  BP_ERR_SAVE_FAILED        = 18,
  BP_ERR_FAN_ID_RANGE       = 19,
  BP_ERR_SW_ID_RANGE        = 20,
  BP_ERR_SW_SYSTEM_LOCKED   = 21,  // 系统开关禁止被二进制指令关闭
  BP_ERR_BLE_NAME_INVALID   = 22,  // 广播名不合法（空/超长/含非打印字符）
  BP_ERR_KE_PUBKEY_INVALID  = 23,  // 密钥交换：RSA 公钥解析失败
};

// ---- CFG TargetType ----
enum BpCfgTarget : uint8_t {
  BP_CFG_TARGET_FN     = 0,
  BP_CFG_TARGET_SW     = 1,
  BP_CFG_TARGET_SENSOR = 2,
  BP_CFG_TARGET_GLOBAL = 3,
};

// ---- CfgKeyId（TargetType 内唯一）----
enum BpCfgKey : uint8_t {
  // FN（0）
  BP_KEY_FN_ENABLED        = 0x01,  // u8
  BP_KEY_FN_POWER_SPD      = 0x02,  // u8
  BP_KEY_FN_HB_FALLBACK    = 0x04,  // u8
  BP_KEY_FN_PWM_FREQ       = 0x05,  // u32
  // SW（1）
  BP_KEY_SW_ENABLED        = 0x11,  // u8
  BP_KEY_SW_POWER_ON_STATE = 0x12,  // u8
  BP_KEY_SW_POWER_ON_DELAY = 0x13,  // u32
  BP_KEY_SW_SYSTEM         = 0x14,  // u8，系统开关标志
  // SENSOR（2）
  BP_KEY_SENSOR_KIND       = 0x21,  // u8
  BP_KEY_SENSOR_ADDR       = 0x22,  // u8
  BP_KEY_SENSOR_ENABLED    = 0x23,  // u8
  BP_KEY_SENSOR_INTERVAL   = 0x24,  // u16
  // GLOBAL（3）
  BP_KEY_GLOBAL_HB_TIMEOUT = 0x31,  // u16
  BP_KEY_GLOBAL_BLE_NAME   = 0x32,  // string，1~20 字节，BLE 广播名
  BP_KEY_GLOBAL_DEBUG_MODE = 0x33,  // u8，0/1，调试模式
};

// ---- 载荷结构常量 ----
#define BP_GETSR_BLOCK_LEN   24U    // 单通道 24 字节（含 2 字节保留）
#define BP_GETSR_CH_MAX      4U
#define BP_GETSR_PAYLOAD     (2U + BP_GETSR_CH_MAX * BP_GETSR_BLOCK_LEN)  // 98
#define BP_EVT_SENSOR_LEN    18U    // ChId1+Kind1+Temp4+Hum4+Press4+Alt4

// ---- CRC16-CCITT（poly 0x1021, init 0xFFFF, 无反射, xorout 0）----
inline uint16_t bpCrc16(const uint8_t* data, size_t len)
{
  uint16_t crc = 0xFFFF;
  for(size_t i = 0; i < len; i++){
    crc ^= (uint16_t)data[i] << 8;
    for(int b = 0; b < 8; b++){
      if(crc & 0x8000) crc = (uint16_t)((crc << 1) ^ 0x1021);
      else             crc = (uint16_t)(crc << 1);
    }
  }
  return crc;
}

// ---- 构建完整帧（含 CRC，小端存储）----
// 返回帧长；payload 为空时传 nullptr。失败返回 0。
inline uint16_t bpBuildFrame(uint8_t seq, uint8_t cmdId,
                             const uint8_t* payload, uint8_t payloadLen,
                             uint8_t* out, size_t outMax)
{
  if(payloadLen > BP_MAX_PAYLOAD) return 0;
  if(outMax < BP_MIN_FRAME + payloadLen) return 0;
  out[0] = BP_MAGIC;
  out[1] = seq;
  out[2] = cmdId;
  out[3] = payloadLen;
  if(payloadLen > 0 && payload != nullptr){
    memcpy(out + BP_HEAD_LEN, payload, payloadLen);
  }
  uint16_t crc = bpCrc16(out, BP_HEAD_LEN + payloadLen);
  out[BP_HEAD_LEN + payloadLen]     = (uint8_t)(crc & 0xFF);        // 低字节在前
  out[BP_HEAD_LEN + payloadLen + 1] = (uint8_t)((crc >> 8) & 0xFF);
  return BP_MIN_FRAME + payloadLen;
}

// ---- 校验并解析接收帧 ----
// 成功返回 true 并填充 seq/cmdId/payload/payloadLen；失败返回 false。
inline bool bpParseFrame(const uint8_t* data, size_t len,
                         uint8_t* seq, uint8_t* cmdId,
                         const uint8_t** payload, uint8_t* payloadLen)
{
  if(data == nullptr || len < BP_MIN_FRAME) return false;
  if(data[0] != BP_MAGIC) return false;
  uint8_t pl = data[3];
  // L13: 协议层上限校验, 防止异常/被破解帧携带超长 payload 进入解析(防御纵深;
  //      明文 HELLO 探测帧载荷为 0 不受影响)
  if(pl > BP_MAX_PAYLOAD) return false;
  if(BP_MIN_FRAME + pl > len) return false;
  uint16_t crc = bpCrc16(data, BP_HEAD_LEN + pl);
  uint16_t got = (uint16_t)data[BP_HEAD_LEN + pl] |
                 ((uint16_t)data[BP_HEAD_LEN + pl + 1] << 8);
  if(crc != got) return false;
  if(seq)     *seq     = data[1];
  if(cmdId)   *cmdId   = data[2];
  if(payload) *payload = data + BP_HEAD_LEN;
  if(payloadLen) *payloadLen = pl;
  return true;
}
