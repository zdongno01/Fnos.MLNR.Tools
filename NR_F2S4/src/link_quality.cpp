#include "link_quality.h"

// =====================================================================
// 链路质量统计实现
// 滑动窗口记录最近 LQ_WINDOW 帧是否异常（1=异常），
// 信号质量 = 100 - 窗口内异常率×100。
// 说明：本模块不做分配、不依赖 Arduino 库，可在 BLE 回调/任务线程安全调用
//（BLE 回调与 secTask 串行消费，实际无并发；仍保持简单无锁实现）。
// =====================================================================

#define LQ_WINDOW 64

static uint8_t s_window[LQ_WINDOW]; // 1 = 异常帧
static uint8_t s_head   = 0;
static uint8_t s_count  = 0;        // 已收帧数（< LQ_WINDOW 时窗口未满）
static uint8_t s_bad    = 0;        // 窗口内异常帧数
static uint8_t s_lastSeq = 0;
static bool    s_hasLast = false;
static uint8_t s_quality = 100;

void lqInit()
{
  for(int i = 0; i < LQ_WINDOW; i++) s_window[i] = 0;
  s_head    = 0;
  s_count   = 0;
  s_bad     = 0;
  s_lastSeq = 0;
  s_hasLast = false;
  s_quality = 100;
}

void lqNoteFrame(uint8_t seq, bool ok)
{
  bool bad = !ok;

  // 帧解析成功时才做 SeqID 连续性检查（损坏帧没有可信 seq）
  if(ok && s_hasLast){
    uint8_t expected = (uint8_t)(s_lastSeq + 1); // 1~255 回绕
    if(seq == s_lastSeq)      bad = true;        // 重复 seq：上位机重发（链路 ACK 丢失迹象）
    else if(seq != expected)  bad = true;        // seq 跳变：丢帧/乱序
  }
  if(ok){
    s_lastSeq = seq;
    s_hasLast = true;
  }

  // 滑动窗口更新
  uint8_t idx = s_head;
  if(s_count < LQ_WINDOW){
    s_count++;
  }else{
    if(s_window[idx]) s_bad--;
  }
  s_window[idx] = bad ? 1 : 0;
  if(bad) s_bad++;
  s_head = (uint8_t)((s_head + 1) % LQ_WINDOW);

  // 信号质量 = 100 - 异常率×100（0~100）
  uint32_t total = s_count ? (uint32_t)s_count : 1;
  uint32_t rate100 = ((uint32_t)s_bad * 100) / total;
  s_quality = (uint8_t)(rate100 > 100 ? 0 : 100 - rate100);
}

uint8_t lqGetQuality()
{
  return s_quality;
}
