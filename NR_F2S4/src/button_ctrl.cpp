#include "button_ctrl.h"
#include "switch_ctrl.h"
#include "hw_config.h"
#include "app_state.h"
#include "ble_service.h"
#include <Arduino.h>
#include <cstdio>

// ===================== 按钮运行时状态 =====================
static bool     btn_stable[BTN_COUNT]      = {false, false, false, false};
static bool     btn_last_read[BTN_COUNT]   = {false, false, false, false};
static uint32_t btn_last_change[BTN_COUNT] = {0, 0, 0, 0};

// 上电重置状态(上电瞬间按钮1+按钮3同时按住 → 持续5s触发强制重置回UNINIT)
static bool     boot_reset_armed  = false;  // 上电时两键是否同按(仅上电采样, 运行期不再武装)
static uint32_t boot_reset_start  = 0;      // 上电重置起始时间

// WORK_RUN下按键触发的LED亮灯截止时间, 0表示已熄灭
static uint32_t led_off_at = 0;

void buttonInit()
{
  // LED初始化: 默认高电平(灭灯), 运行期由app_state按状态节奏驱动
  pinMode(LED_PIN, OUTPUT);
  digitalWrite(LED_PIN, HIGH);

  for(uint8_t i = 0; i < BTN_COUNT; i++){
    // 板卡无外部上拉, 使用内部上拉: 空闲=高电平, 按下到地=低电平
    pinMode(BTN_PINS[i], INPUT_PULLUP);
    btn_last_read[i]   = (digitalRead(BTN_PINS[i]) == LOW);
    btn_stable[i]      = btn_last_read[i];
    btn_last_change[i] = 0;
  }

  // 上电重置采样: 上电瞬间按钮1+按钮3同时按住 → 武装上电重置(计时5s后回UNINIT)
  // 仅在此刻采样; 正常运行期间再同时按下两键不进入重置流程
  boot_reset_armed = (btn_stable[RESET_BTN_A_IDX] && btn_stable[RESET_BTN_B_IDX]);
  boot_reset_start = millis();
  if(boot_reset_armed){
    Serial.println("[BTN] boot reset armed: hold BTN1+BTN3 5s to reset to UNINIT");
  }
}

// 按钮扫描(消抖 + 按下沿切换对应开关 + 上电重置检测 + WORK_RUN下LED闪烁)
void buttonTask()
{
  uint32_t now = millis();

  // ---- 上电重置: 上电时按钮1+按钮3按住持续5s → 强制重置回UNINIT ----
  // 计时期间LED常亮作为5s倒计时提示(置g_led_held_on让状态任务跳过节奏驱动)
  if(boot_reset_armed){
    bool a = (digitalRead(BTN_PINS[RESET_BTN_A_IDX]) == LOW);
    bool b = (digitalRead(BTN_PINS[RESET_BTN_B_IDX]) == LOW);
    if(a && b && (now - boot_reset_start) >= RESET_HOLD_MS){
      // 计时满5s: 执行强制重置, 解除武装; LED交回重置后的状态任务接管
      boot_reset_armed = false;
      g_led_held_on = false;
      Serial.println("[BTN] boot reset: BTN1+BTN3 held 5s -> force reset to UNINIT");
      appStateForceReset();
      return; // 重置期间跳过本轮其余处理
    }else if(!(a && b)){
      // 5s内松开任一键: 取消本次上电重置, LED立即熄灭交回状态任务
      boot_reset_armed = false;
      g_led_held_on = false;
      digitalWrite(LED_PIN, HIGH);
      Serial.println("[BTN] boot reset cancelled (released early)");
    }else{
      // 仍按住: 上电重置计时中, LED常亮提示, 并抑制普通按键切换, 避免上电瞬间误操作开关
      g_led_held_on = true;
      digitalWrite(LED_PIN, LOW); // LED常亮(低电平亮)
      return;
    }
  }

  for(uint8_t i = 0; i < BTN_COUNT; i++){
    bool pressed = (digitalRead(BTN_PINS[i]) == LOW);
    if(pressed != btn_last_read[i]){
      btn_last_read[i]  = pressed;
      btn_last_change[i]= now;
    }else if(pressed != btn_stable[i] && (now - btn_last_change[i]) >= BTN_DEBOUNCE_MS){
      btn_stable[i] = pressed;
      if(pressed){
        // 按下沿: 切换对应开关电平(超越启用状态与设备状态)
        bool newState = !sw_state[i];
        // 统一入口：在线推送 EVT_BTN（带条件=硬件按钮），离线记录到离线事件缓冲
        setSwitchOutput(i, newState, SW_COND_BUTTON);
        // WORK_RUN下任意按键触发LED亮500ms(状态机不接管此时LED)
        if(g_dev_state == STATE_WORK_RUN){
          digitalWrite(LED_PIN, LOW);
          led_off_at = now + LED_FLASH_MS;
        }
      }
    }
  }

  // WORK_RUN下LED超时熄灭
  // FS5-12: 用有符号差值比较替代 now>=led_off_at, 天然兼容millis回绕(约49.7天)
  if(g_dev_state == STATE_WORK_RUN && led_off_at && (int32_t)(now - led_off_at) >= 0){
    digitalWrite(LED_PIN, HIGH);
    led_off_at = 0;
  }
}
