#pragma once
#include <stdint.h>
#include <stddef.h>

// ===================== 离线事件记录（蓝牙未连接期间的 SWn 开关事件） =====================
// 蓝牙离线时（未连接/未授权），SWn 状态变化（上电自动上线/硬件按钮/其它）无法实时推送，
// 记录到内存环形缓冲；连接后上位机经 BP_CMD_GET_OFFLINE 读取，固件随包携带当前毫秒时间，
// 上位机据此反推事件真实发生时间，写入硬盘组操作日志。
//
// 存储容量设计：单条 7 字节（time_ms u32 LE + swId u8 + state u8 + cond u8），
// 环形缓冲 32 条 = 224 字节 RAM（ESP32-C3 可用 RAM 约 320KB，占比可忽略；
// 32 条可覆盖：开机延迟上电 4 开关 + 长时间离线期间的按钮操作，超出自动覆盖最旧记录）。
#define OFFLINE_EV_MAX 32

// SWn 事件条件（推送/记录来源）
enum SwEventCond : uint8_t {
  SW_COND_BUTTON = 0,  // 硬件按钮
  SW_COND_AUTO   = 1,  // 上电自动上线（POWER_ON_STATE）
  SW_COND_CMD    = 2,  // 指令/其它
};

void offlineLogInit();
// 记录一条离线事件（满则覆盖最旧）；sw_id 1~SW_COUNT
void offlineLogPush(uint8_t swId, uint8_t state, uint8_t cond);
// 当前条数
uint8_t offlineLogCount();
// 拷贝最多 maxCount 条（受 outMax 限制）到 out，不清空缓冲；返回实际拷贝条数
// L3: 不清空缓冲，由调用方在发送成功后调用 offlineLogCommit 确认
uint8_t offlineLogDrain(uint8_t* out, uint16_t outMax, uint8_t maxCount);
// 确认提交：从环形缓冲中移除最旧的 count 条（发送成功后调用）
void offlineLogCommit(uint8_t count);
