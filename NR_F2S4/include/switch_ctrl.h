#pragma once
#include <stdint.h>
#include "hw_config.h"
#include "offline_log.h"

// 开关当前电平状态, true=高电平(定义于switch_ctrl.cpp)
extern bool sw_state[SW_COUNT];

// 开关引脚初始化(输出模式, 置低电平)
void switchInit();

// 设置开关电平, true=高电平(开), false=低电平(关)。
// cond 为事件来源(SwEventCond)：统一入口，内部完成
//   在线(ble_connected) → 推送 EVT_BTN（带条件）
//   离线(未连接)       → 记录到离线事件环形缓冲
//   UNINIT 状态        → 不记录不推送
void setSwitchOutput(uint8_t sw_idx, bool on, uint8_t cond);

// 安排开机延迟上电: 延迟delay_ms后应用到配置的默认状态
void switchBootSchedule(uint8_t sw_idx, uint32_t delay_ms);

// 取消开机延迟上电(停用的开关不自动上电)
void switchBootCancel(uint8_t sw_idx);

// 开机延迟上电任务: 到时后应用到默认状态
void switchBootTask();
