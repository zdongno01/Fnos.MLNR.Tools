#include "switch_ctrl.h"
#include "nvs_store.h"
#include "app_state.h"
#include "ble_service.h"
#include <Arduino.h>

// ===================== 开关运行时状态 =====================
bool sw_state[SW_COUNT] = {false, false, false, false};
static bool     sw_boot_applied[SW_COUNT]  = {false, false, false, false}; // 开机延迟上电是否已执行
static uint32_t sw_boot_apply_at[SW_COUNT] = {0, 0, 0, 0};

void switchInit()
{
  offlineLogInit();
  for(uint8_t i = 0; i < SW_COUNT; i++){
    pinMode(SW_PINS[i], OUTPUT);
    digitalWrite(SW_PINS[i], LOW);
  }
}

void setSwitchOutput(uint8_t sw_idx, bool on, uint8_t cond)
{
  // L15: 防御——调用方索引均受控, 但公开函数入口补齐越界检查
  if(sw_idx >= SW_COUNT) return;
  sw_state[sw_idx] = on;
  digitalWrite(SW_PINS[sw_idx], on ? HIGH : LOW);

  // 事件统一出口：UNINIT 不记录不推送；在线推送 EVT_BTN（带条件）；离线记录到环形缓冲
  if(g_dev_state == STATE_UNINIT) return;
  if(ble_connected){
    bleSendButtonEvent((uint8_t)(sw_idx + 1), on ? 1 : 0, cond);
  }else{
    offlineLogPush((uint8_t)(sw_idx + 1), on ? 1 : 0, cond);
  }
}

void switchBootSchedule(uint8_t sw_idx, uint32_t delay_ms)
{
  sw_boot_applied[sw_idx]  = false;
  sw_boot_apply_at[sw_idx] = millis() + delay_ms;
}

void switchBootCancel(uint8_t sw_idx)
{
  sw_boot_applied[sw_idx] = true;
}

// 开机延迟上电: 到时后应用到默认状态
void switchBootTask()
{
  uint32_t now = millis();
  for(uint8_t i = 0; i < SW_COUNT; i++){
    if(sw_boot_applied[i]) continue;
    // 防御: UNINIT 状态下开关不得上电, 一律取消 boot schedule
    if(g_dev_state == STATE_UNINIT){
      sw_boot_applied[i] = true;
      continue;
    }
    // FS5-3: 运行期被停用的开关不再执行开机延迟上电
    if(!g_sw_cfg[i].enabled){
      sw_boot_applied[i] = true;
      continue;
    }
    if((int32_t)(now - sw_boot_apply_at[i]) >= 0){
      sw_boot_applied[i] = true;
      setSwitchOutput(i, g_sw_cfg[i].power_on_state != 0, SW_COND_AUTO);
    }
  }
}
