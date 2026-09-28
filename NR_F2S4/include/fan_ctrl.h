#pragma once
#include <stdint.h>
#include "hw_config.h"

// 风扇运行时状态(定义于fan_ctrl.cpp)
extern uint8_t  target_spd[FAN_COUNT];        // 目标转速(外部指令期望)
extern uint8_t  current_spd[FAN_COUNT];       // 当前实际输出转速(平滑渐变)
extern uint8_t  last_received_spd[FAN_COUNT]; // 最近一次收到的转速指令
extern uint32_t last_spd_cmd_ms[FAN_COUNT];   // 最近一次转速指令时间(节流用)

// 重设PWM频率; 返回 ledcSetup 实际配置的频率(可能因分频/分辨率舍入与请求值不同),
// 返回 0 表示参数非法未执行
uint32_t updatePwmFreq(uint8_t fan_idx, uint32_t new_freq);

// 直接设置风扇输出(含最小启动阈值判断)
void setFanOutput(uint8_t fan_idx, uint8_t spd);

// 平滑调速: 差值越大步长越大, 自动变速
void smoothSpeedTask();
