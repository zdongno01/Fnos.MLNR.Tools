#pragma once
#include <stdint.h>

// ===================== 设备状态定义 =====================
enum DeviceState : uint8_t {
  STATE_UNINIT    = 0,  // 未初始化(等待配网/授权), LED每秒闪2次, 允许任意设备连接
  STATE_WORK_WAIT = 1,  // 已初始化, 等待授权设备连接, LED每秒闪1次, 仅授权设备可连接
  STATE_WORK_RUN  = 2   // 工作运行状态, LED熄灭, 仅授权设备维持连接
};

// 当前设备状态(定义于app_state.cpp)
extern volatile uint8_t g_dev_state;

// LED被外部占用为常亮(上电重置计时中由button_ctrl置位), 状态任务跳过节奏驱动
extern volatile bool g_led_held_on;

// 上电初始化: 依据NVS授权记录决定初始状态, 并加载授权数据
void appStateInit();

// 状态切换(含LED即时反应)
void appStateSet(uint8_t s);

// 状态任务: LED节奏闪烁 + WORK_WAIT下检测授权设备已连接则进入WORK_RUN
void appStateTask();

// 组合键/强制重置: 断开连接, 清除授权与配置, 回到STATE_UNINIT
void appStateForceReset();
