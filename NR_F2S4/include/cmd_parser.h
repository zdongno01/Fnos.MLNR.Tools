#pragma once

#include <stdint.h>

// ===================== 指令解析核心（二进制帧协议 FS.md） =====================
// 帧格式: [Magic(1)=0xAA][SeqID(1)][CmdID(1)][PayloadLen(1)][Payload(N)][CRC16(2)]
// 命令 CmdID:
//   0x01 INIT       0x02 SPD:[FanId][Spd]     0x03 SW:[SwId][State]
//   0x04 CFG:[TargetType][TargetId][CfgKeyId][Value(N)]
//   0x05 GETV       0x06 GETF:[FanId]         0x07 GETS:[SwId]
//   0x08 GETSR      0x09 PING                 0x0A SAVE
//   0x0B RESET      0x0C GETG
//   0x0D HELLO(明文探测)  0x0E KE_PUBKEY(明文公钥分片)  0x0F KE_CONFIRM(加密确认)
// 应答: 0x80 RESP_ACK，载荷首字节为 ErrorCode（0=OK）
// 事件: 0x81 EVT_BTN / 0x82 EVT_SENSOR / 0x83 EVT_WARN（SeqID=0）
// 详细字段见 include/binary_proto.h 与 FS.md。
void parseBinaryCommand(uint8_t seq, uint8_t cmdId, const uint8_t* payload, uint8_t payloadLen);

// 明文 HELLO 应答（强制明文发送 ACK[ErrCode][State]，供解密兜底与明文阶段共用）
void handleHelloCmdPlain(uint8_t seq);

// 重置密钥交换分片状态（连接建立/断开时调用）
void keReset();
