#!/bin/sh
# 检测硬盘是否闲置（io 时间 < 1% 视为闲置）
cat /sys/block/sd*/stat | awk '{print $10}' | head -1
