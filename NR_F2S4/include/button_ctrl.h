#pragma once

// 按钮引脚初始化(INPUT_PULLUP模式, 板卡无外部上拉, 内部上拉按下为低)
// 同时采样上电瞬间按钮1+按钮3是否同按, 用于武装"上电重置回UNINIT"
void buttonInit();

// 按钮扫描任务(消抖 + 按下沿切换对应开关电平 + 上电重置检测)
void buttonTask();
