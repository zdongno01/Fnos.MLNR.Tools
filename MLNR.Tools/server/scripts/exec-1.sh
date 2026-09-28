#!/bin/sh
# 发送 ntfy 推送
curl -s -d "$1" ntfy.sh/mlnr-alerts || exit 1
