#include "offline_log.h"
#include "hw_config.h"
#include <Arduino.h>
#include <string.h>
#include <freertos/FreeRTOS.h>   // portMUX_TYPE / portENTER_CRITICAL

// ===================== 离线事件环形缓冲 =====================
struct OfflineEvent {
  uint32_t time_ms;   // 事件发生时的 millis()
  uint8_t  sw_id;     // 1~SW_COUNT
  uint8_t  state;     // 0=关 1=开
  uint8_t  cond;      // SwEventCond
};

static OfflineEvent s_ev[OFFLINE_EV_MAX];
static uint8_t s_head  = 0;   // 最旧条目下标
static uint8_t s_count = 0;   // 当前条数

// L23: 临界区——offlineLogPush 运行于 loop 线程(开关动作), offlineLogDrain/Commit
//      运行于 secTask 线程(断线补发), 无锁时 s_head/s_count/条目三者可能撕裂。
//      单核下关中断整段保护即可(操作均为数微秒级)
static portMUX_TYPE s_log_mux = portMUX_INITIALIZER_UNLOCKED;

void offlineLogInit()
{
  portENTER_CRITICAL(&s_log_mux);
  s_head  = 0;
  s_count = 0;
  portEXIT_CRITICAL(&s_log_mux);
}

void offlineLogPush(uint8_t swId, uint8_t state, uint8_t cond)
{
  if(swId == 0 || swId > SW_COUNT) return;
  uint32_t now = millis();
  portENTER_CRITICAL(&s_log_mux);
  OfflineEvent* slot;
  if(s_count < OFFLINE_EV_MAX){
    slot = &s_ev[(uint8_t)((s_head + s_count) % OFFLINE_EV_MAX)];
    s_count++;
  }else{
    slot = &s_ev[s_head];   // 满则覆盖最旧
    s_head = (uint8_t)((s_head + 1) % OFFLINE_EV_MAX);
  }
  slot->time_ms = now;
  slot->sw_id   = swId;
  slot->state   = state;
  slot->cond    = cond;
  portEXIT_CRITICAL(&s_log_mux);
}

uint8_t offlineLogCount()
{
  uint8_t n;
  portENTER_CRITICAL(&s_log_mux);
  n = s_count;
  portEXIT_CRITICAL(&s_log_mux);
  return n;
}

uint8_t offlineLogDrain(uint8_t* out, uint16_t outMax, uint8_t maxCount)
{
  // L3: 只拷贝不清空，由调用方在发送成功后调用 offlineLogCommit 确认
  portENTER_CRITICAL(&s_log_mux);
  uint8_t n = s_count;
  if(n > maxCount) n = maxCount;
  if(outMax < (uint16_t)n * 7) n = (uint8_t)(outMax / 7);
  for(uint8_t i = 0; i < n; i++){
    const OfflineEvent& e = s_ev[(uint8_t)((s_head + i) % OFFLINE_EV_MAX)];
    uint8_t* p = out + (size_t)i * 7;
    p[0] = e.time_ms & 0xFF;
    p[1] = (e.time_ms >> 8) & 0xFF;
    p[2] = (e.time_ms >> 16) & 0xFF;
    p[3] = (e.time_ms >> 24) & 0xFF;
    p[4] = e.sw_id;
    p[5] = e.state;
    p[6] = e.cond;
  }
  portEXIT_CRITICAL(&s_log_mux);
  return n;
}

void offlineLogCommit(uint8_t count)
{
  // L3: 发送成功后移除已确认的条目
  portENTER_CRITICAL(&s_log_mux);
  if(count > s_count) count = s_count;
  s_head = (uint8_t)((s_head + count) % OFFLINE_EV_MAX);
  s_count = (uint8_t)(s_count - count);
  portEXIT_CRITICAL(&s_log_mux);
}
