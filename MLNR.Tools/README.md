
fnos需要写入:
echo "drivetemp" >> /etc/modules-load.d/drivetemp.conf


cd D:\Documents\trae_projects\麻辣牛肉\MLNR.Tools\server
$env:mlnr_BLE_MOCK="1"; $env:mlnr_DEV="1"
..\mlnr-dev.exe
# 浏览器访问 http://127.0.0.1:8088/app/mlnr/
