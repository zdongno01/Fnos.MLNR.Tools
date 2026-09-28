#!/bin/sh
# 启动 rsync 增量备份
rsync -a --delete "$1" 2>/dev/null || true
