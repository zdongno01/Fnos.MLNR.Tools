#include "fan_ctrl.h"
#include <Arduino.h>

// ===================== 风扇运行时状态 =====================
uint8_t  target_spd[FAN_COUNT]        = {0, 0};
uint8_t  current_spd[FAN_COUNT]       = {0, 0};
uint8_t  last_received_spd[FAN_COUNT] = {0, 0};
uint32_t last_spd_cmd_ms[FAN_COUNT]   = {0, 0};
static uint32_t last_smooth_tick = 0;

// 重设PWM频率; 返回 ledcSetup 实际配置的频率(可能因分频/分辨率舍入与请求值不同)
uint32_t updatePwmFreq(uint8_t fan_idx, uint32_t new_freq)
{
  // L15: 防御——调用方索引均受控, 但公开函数入口补齐越界检查
  if(fan_idx >= FAN_COUNT) return 0;
  ledcDetachPin(FAN_PINS[fan_idx]);
  // L24: 回填实际配置频率, 避免 GETF 上报与硬件实际不符的频率
  uint32_t actual = ledcSetup(FAN_PWM_CH[fan_idx], new_freq, PWM_RESOLUTION);
  ledcAttachPin(FAN_PINS[fan_idx], FAN_PWM_CH[fan_idx]);
  // L8: 恢复当前占空比，防止频率热更新瞬间占空比清零
  setFanOutput(fan_idx, current_spd[fan_idx]);
  return actual;
}

void setFanOutput(uint8_t fan_idx, uint8_t spd)
{
  // L15: 防御——调用方索引均受控, 但公开函数入口补齐越界检查
  if(fan_idx >= FAN_COUNT) return;
  // 占空比公式: SET=0 → 0%; SET=n → 30% + 0.7%*(n-1)
  // duty_x10 = (spd==0) ? 0 : 300 + 7*(spd-1)
  // duty = duty_x10 * 255 / 1000
  uint32_t duty;
  if(spd == 0){
    duty = 0;
  }else{
    uint32_t duty_x10 = 300UL + 7UL * (spd - 1);
    duty = duty_x10 * 255UL / 1000UL;
  }
  ledcWrite(FAN_PWM_CH[fan_idx], duty);
}

// 平滑调速：差值越大步长越大，自动变速
void smoothSpeedTask()
{
  uint32_t now = millis();
  if(now - last_smooth_tick < SMOOTH_STEP_MS) return;
  last_smooth_tick = now;

  for(uint8_t i = 0; i < FAN_COUNT; i++){
    int diff = (int)target_spd[i] - (int)current_spd[i];
    if(diff == 0) continue;

    // 根据差值动态步长：差值大则一次多跳，差值小精细调节
    uint8_t step;
    if(abs(diff) >= 30) step = 5;
    else if(abs(diff) >= 10) step = 2;
    else step = 1;

    if(diff > 0){
      current_spd[i] = constrain(current_spd[i] + step, 0, 100);
    }else{
      current_spd[i] = constrain(current_spd[i] - step, 0, 100);
    }
    setFanOutput(i, current_spd[i]);
  }
}
