#include "sensor.h"
#include "hw_config.h"
#include "ble_service.h"
#include "security.h"
#include "nvs_store.h"
#include <Arduino.h>
#include <Wire.h>
#include <math.h>
#include <esp_task_wdt.h>

// ===================== 传感器运行时状态 =====================
SensorData g_sensor_ch[SENSOR_CH_MAX];
bool g_sensor_ok[SENSOR_CH_MAX] = {false, false, false, false};
uint8_t g_sensor_init_type[SENSOR_CH_MAX] = {0xFF, 0xFF, 0xFF, 0xFF};
uint8_t g_sensor_init_addr[SENSOR_CH_MAX] = {0};

// L8: g_sensor_ch 跨线程(loop 写 / secTask GETSR 读)快照临界区。
//     SensorData 为 16B 结构体(4×float), 非原子——写端 4 次 32-bit 写期间若被
//     preemption 打断, 读端可能拿到"温度已更新、湿度未更新"的撕裂快照。
//     单核下用关中断临界区做整体拷贝即可, 临界区极短(<1us)
static portMUX_TYPE g_sensor_data_mux = portMUX_INITIALIZER_UNLOCKED;

// 原子写入单通道快照(loop 采集线程)
static void sensorSetChannelSnapshot(uint8_t ch, const SensorData& d)
{
  portENTER_CRITICAL(&g_sensor_data_mux);
  g_sensor_ch[ch] = d;
  portEXIT_CRITICAL(&g_sensor_data_mux);
}

// 原子读取单通道快照(secTask GETSR 线程)
SensorData sensorGetChannelSnapshot(uint8_t ch)
{
  SensorData d;
  portENTER_CRITICAL(&g_sensor_data_mux);
  d = g_sensor_ch[ch];
  portEXIT_CRITICAL(&g_sensor_data_mux);
  return d;
}

// S3: I2C 总线互斥锁
SemaphoreHandle_t g_i2c_mutex = nullptr;
// L6: 传感器连续失败计数，达到阈值后置 g_sensor_ok=false
static int g_sensor_fail_count[SENSOR_CH_MAX] = {0, 0, 0, 0};
#define SENSOR_FAIL_THRESHOLD 3

// M9: BMP280校准参数按通道独立存储，防止多通道共享一份导致校准错误
static uint16_t bmp_dig_T1[SENSOR_CH_MAX];
static int16_t  bmp_dig_T2[SENSOR_CH_MAX], bmp_dig_T3[SENSOR_CH_MAX];
static uint16_t bmp_dig_P1[SENSOR_CH_MAX];
static int16_t  bmp_dig_P2[SENSOR_CH_MAX], bmp_dig_P3[SENSOR_CH_MAX], bmp_dig_P4[SENSOR_CH_MAX], bmp_dig_P5[SENSOR_CH_MAX];
static int16_t  bmp_dig_P6[SENSOR_CH_MAX], bmp_dig_P7[SENSOR_CH_MAX], bmp_dig_P8[SENSOR_CH_MAX], bmp_dig_P9[SENSOR_CH_MAX];
static int32_t  bmp_t_fine[SENSOR_CH_MAX] = {0, 0, 0, 0};

// 海拔基准压力(Pa), 默认标准大气压
static const float SEA_LEVEL_PA = 101325.0f;

// ===================== I2C Debug 输出(USB CDC Serial) =====================
// 构建时带 -D SENSOR_DEBUG(platformio.ini build_flags) 则输出全量分步日志
// (总线扫描/每次读写/10s状态行); 不带则仅输出传感器初始化与采集失败等错误日志。
// USB 串口: 板子 USB 口, 115200。
#ifdef SENSOR_DEBUG
#define DBG(fmt, ...) Serial.printf("[I2C] " fmt "\n", ##__VA_ARGS__)
#else
#define DBG(fmt, ...) do {} while(0)
#endif

// Wire.endTransmission() 返回码文本化: 0=OK 1=数据过长 2=地址NACK 3=数据NACK 4=其他 5=超时
static const char* wireErrText(uint8_t e)
{
  switch(e){
    case 0:  return "OK";
    case 1:  return "DATA_TOO_LONG";
    case 2:  return "NACK_ADDR";
    case 3:  return "NACK_DATA";
    case 4:  return "OTHER";
    case 5:  return "TIMEOUT";
    default: return "?";
  }
}

// ===================== I2C 基础读写 =====================
static bool i2cWrite(uint8_t addr, const uint8_t* data, size_t len)
{
  Wire.beginTransmission(addr);
  Wire.write(data, len);
  uint8_t e = Wire.endTransmission();
  if(e != 0) DBG("write addr=0x%02X len=%u -> endTransmission=%u(%s)", addr, len, e, wireErrText(e));
  return (e == 0);
}

// 地址探测: 只发地址字节, 返回是否收到 ACK(用于诊断"设备不在总线上" vs "初始化协议失败")
static bool i2cProbe(uint8_t addr)
{
  Wire.beginTransmission(addr);
  uint8_t e = Wire.endTransmission();
  DBG("probe addr=0x%02X -> %s", addr, e == 0 ? "ACK" : "NAK");
  return (e == 0);
}

static bool i2cRead(uint8_t addr, uint8_t* buf, size_t len)
{
  size_t got = Wire.requestFrom((int)addr, (int)len);
  if(got != len){
    DBG("read addr=0x%02X want=%u got=%u", addr, len, got);
    return false;
  }
  for(size_t i = 0; i < len; i++) buf[i] = Wire.read();
  return true;
}

// 寄存器读取: 先写寄存器地址(不停止总线)再读, 适用于BMP280/LM75等寄存器型设备
static bool i2cReadReg(uint8_t addr, uint8_t reg, uint8_t* buf, size_t len)
{
  Wire.beginTransmission(addr);
  Wire.write(reg);
  uint8_t e = Wire.endTransmission(false); // 保持总线, 不发送STOP
  if(e != 0){
    DBG("readReg addr=0x%02X reg=0x%02X -> write=%u(%s)", addr, reg, e, wireErrText(e));
    return false;
  }
  size_t got = Wire.requestFrom((int)addr, (int)len);
  if(got != len){
    DBG("readReg addr=0x%02X reg=0x%02X -> read got=%u/%u", addr, reg, got, len);
    return false;
  }
  for(size_t i = 0; i < len; i++) buf[i] = Wire.read();
  return true;
}

static bool i2cWriteReg(uint8_t addr, uint8_t reg, uint8_t val)
{
  // 单事务写入 [寄存器地址][数据], 避免拆成两次独立事务导致寄存器指针漂移
  Wire.beginTransmission(addr);
  Wire.write(reg);
  Wire.write(val);
  uint8_t e = Wire.endTransmission();
  if(e != 0) DBG("writeReg addr=0x%02X reg=0x%02X val=0x%02X -> %u(%s)", addr, reg, val, e, wireErrText(e));
  return (e == 0);
}

#ifdef SENSOR_DEBUG
// 启动时全地址扫描(0x08~0x77): 直接确认总线上有哪些设备在线, 诊断接线/地址/上拉问题
static void i2cScanBus()
{
  Serial.printf("[I2C] bus scan SDA=%d SCL=%d %luHz:", (int)I2C_SDA_PIN, (int)I2C_SCL_PIN,
                (unsigned long)I2C_FREQ_HZ);
  bool any = false;
  for(uint16_t a = 0x08; a <= 0x77; a++){
    Wire.beginTransmission((uint8_t)a);
    if(Wire.endTransmission() == 0){
      Serial.printf(" 0x%02X", a);
      any = true;
    }
    // L25: 全地址扫描 112 个地址约耗时 2~3s, 分段喂狗防止逼近 TWDT
    if((a & 0x0F) == 0x0F) esp_task_wdt_reset();
  }
  Serial.printf(any ? "\n" : " (no device ACK)\n");
}
#endif // SENSOR_DEBUG

// ===================== AHT20 =====================
static bool aht20Init(uint8_t addr)
{
  // 软复位后等待初始化完成
  uint8_t cmd = AHT20_CMD_SOFTRESET;
  i2cWrite(addr, &cmd, 1);
  delay(20);

  // 检查校准使能位, 未使能则发送初始化命令
  uint8_t status = 0;
  if(!i2cRead(addr, &status, 1)) return false;
  if(!(status & AHT20_CAL_ENABLE)){
    uint8_t init_cmd[3] = {AHT20_CMD_INIT, 0x08, 0x00};
    if(!i2cWrite(addr, init_cmd, 3)) return false;
    delay(10);
    if(!i2cRead(addr, &status, 1) || !(status & AHT20_CAL_ENABLE)){
      return false;
    }
  }
  return true;
}

static bool aht20Read(uint8_t addr, float* temp, float* humi)
{
  // 触发测量
  uint8_t trig[3] = {AHT20_CMD_TRIGGER, 0x33, 0x00};
  if(!i2cWrite(addr, trig, 3)) return false;
  delay(80); // 测量时间约75ms

  uint8_t raw[7];
  uint8_t status = 0;
  for(int retry = 0; retry < 10; retry++){
    if(!i2cRead(addr, raw, 7)) return false;
    status = raw[0];
    if(!(status & AHT20_BUSY_BIT)) break;
    delay(10);
  }
  if(status & AHT20_BUSY_BIT) return false;

  // 20bit湿度 + 20bit温度
  uint32_t h = ((uint32_t)raw[1] << 12) | ((uint32_t)raw[2] << 4) | (raw[3] >> 4);
  uint32_t t = (((uint32_t)raw[3] & 0x0F) << 16) | ((uint32_t)raw[4] << 8) | raw[5];

  *humi = (float)h * 100.0f / 1048576.0f;          // /2^20 *100%
  *temp = (float)t * 200.0f / 1048576.0f - 50.0f;  // /2^20 *200 -50
  // L18: 与 HTU21D 一致做范围钳位, 避免受扰数据上报越界值(如湿度150%)
  if(*humi < 0.0f) *humi = 0.0f;
  if(*humi > 100.0f) *humi = 100.0f;
  if(*temp < -40.0f) *temp = -40.0f;
  if(*temp > 125.0f) *temp = 125.0f;
  return true;
}

// ===================== BMP280 (Bosch官方补偿算法) =====================
static bool bmp280ReadCalib(uint8_t ch, uint8_t addr)
{
  uint8_t cal[24];
  if(!i2cReadReg(addr, BMP280_REG_CALIB, cal, 24)) return false;

  bmp_dig_T1[ch] = (uint16_t)((cal[1] << 8) | cal[0]);
  bmp_dig_T2[ch] = (int16_t)((cal[3] << 8) | cal[2]);
  bmp_dig_T3[ch] = (int16_t)((cal[5] << 8) | cal[4]);
  bmp_dig_P1[ch] = (uint16_t)((cal[7] << 8) | cal[6]);
  bmp_dig_P2[ch] = (int16_t)((cal[9] << 8) | cal[8]);
  bmp_dig_P3[ch] = (int16_t)((cal[11] << 8) | cal[10]);
  bmp_dig_P4[ch] = (int16_t)((cal[13] << 8) | cal[12]);
  bmp_dig_P5[ch] = (int16_t)((cal[15] << 8) | cal[14]);
  bmp_dig_P6[ch] = (int16_t)((cal[17] << 8) | cal[16]);
  bmp_dig_P7[ch] = (int16_t)((cal[19] << 8) | cal[18]);
  bmp_dig_P8[ch] = (int16_t)((cal[21] << 8) | cal[20]);
  bmp_dig_P9[ch] = (int16_t)((cal[23] << 8) | cal[22]);
  return (bmp_dig_T1[ch] != 0 && bmp_dig_P1[ch] != 0);
}

static bool bmp280Init(uint8_t ch, uint8_t addr)
{
  // 验证芯片ID
  uint8_t id = 0;
  if(!i2cReadReg(addr, BMP280_REG_ID, &id, 1) || id != BMP280_CHIP_ID) return false;

  // 软复位
  if(!i2cWriteReg(addr, BMP280_REG_RESET, BMP280_RESET_VAL)) return false;
  delay(5);

  if(!bmp280ReadCalib(ch, addr)) return false;

  // 配置: 待机1000ms(t_sb=101), IIR滤波系数16(filter=100) → 0b101_100_00 = 0xB0
  i2cWriteReg(addr, BMP280_REG_CONFIG, 0xB0);
  // 控制: 温度x2(osrs_t=010), 压力x16(osrs_p=101, 超超高分辨率), 正常模式(mode=11)
  //       → 0b010_101_11 = 0x57。
  // 修复: 此前误写 0xB4(0b1011_0100, mode=00=睡眠), 芯片从不自动转换, 数据寄存器
  //       恒为上电垃圾值, 经(正确的)补偿公式后表现为温度/气压/海拔整体偏离
  //       (如温度偏低约10℃、气压约756hPa、海拔虚高约2400m)。
  i2cWriteReg(addr, BMP280_REG_CTRL, 0x57);
  return true;
}

static float bmp280CompTemp(uint8_t ch, int32_t adc_t)
{
  float var1 = ((float)adc_t / 16384.0f - (float)bmp_dig_T1[ch] / 1024.0f) * (float)bmp_dig_T2[ch];
  float var2 = (((float)adc_t / 131072.0f - (float)bmp_dig_T1[ch] / 8192.0f) *
                ((float)adc_t / 131072.0f - (float)bmp_dig_T1[ch] / 8192.0f)) * (float)bmp_dig_T3[ch];
  bmp_t_fine[ch] = (int32_t)(var1 + var2);
  return (var1 + var2) / 5120.0f;
}

static float bmp280CompPress(uint8_t ch, int32_t adc_p)
{
  float var1 = (float)bmp_t_fine[ch] / 2.0f - 64000.0f;
  float var2 = var1 * var1 * (float)bmp_dig_P6[ch] / 32768.0f;
  var2 = var2 + var1 * (float)bmp_dig_P5[ch] * 2.0f;
  var2 = (var2 / 4.0f) + ((float)bmp_dig_P4[ch] * 65536.0f);
  float var3 = (float)bmp_dig_P3[ch] * var1 * var1 / 524288.0f;
  var1 = (var3 + (float)bmp_dig_P2[ch] * var1) / 524288.0f;
  var1 = (1.0f + var1 / 32768.0f) * (float)bmp_dig_P1[ch];
  if(var1 < 0.01f) return 0; // 防除零

  float p = 1048576.0f - (float)adc_p;
  p = (p - (var2 / 4096.0f)) * 6250.0f / var1;
  var1 = (float)bmp_dig_P9[ch] * p * p / 2147483648.0f;
  var2 = p * (float)bmp_dig_P8[ch] / 32768.0f;
  p = p + (var1 + var2 + (float)bmp_dig_P7[ch]) / 16.0f;
  return p;
}

static bool bmp280Read(uint8_t ch, uint8_t addr, float* temp, float* press)
{
  uint8_t raw[6];
  if(!i2cReadReg(addr, BMP280_REG_PRESS, raw, 6)) return false;

  int32_t adc_p = ((int32_t)raw[0] << 12) | ((int32_t)raw[1] << 4) | (raw[2] >> 4);
  int32_t adc_t = ((int32_t)raw[3] << 12) | ((int32_t)raw[4] << 4) | (raw[5] >> 4);

  float t = bmp280CompTemp(ch, adc_t); // 更新t_fine并返回芯片温度
  float p = bmp280CompPress(ch, adc_p);
  *temp  = t;
  *press = p;
  return (p > 30000.0f && p < 120000.0f); // 合理性检查
}

// ===================== LM75 =====================
static bool lm75Init(uint8_t addr)
{
  // LM75 无ID寄存器, 读温度寄存器2字节验证在线
  uint8_t raw[2];
  return i2cReadReg(addr, LM75_REG_TEMP, raw, 2);
}

static bool lm75Read(uint8_t addr, float* temp)
{
  uint8_t raw[2];
  if(!i2cReadReg(addr, LM75_REG_TEMP, raw, 2)) return false;
  // 2字节大端, 高9位有效(11bit精度), 0.5℃分辨率
  int16_t t = (int16_t)(((uint16_t)raw[0] << 8) | raw[1]);
  *temp = (float)(t >> 7) * 0.5f;
  return (*temp > -60.0f && *temp < 150.0f); // 合理性检查
}

// ===================== HTU21D =====================
static bool htu21dInit(uint8_t addr)
{
  // 软复位: 数据手册规定复位耗时最长15ms(含校准数据重载), 实际芯片经常更久。
  // 固定等50ms预留余量, 避免复位未完成时后续读用户寄存器被 NACK/超时。
  uint8_t cmd = HTU21D_CMD_RESET;
  bool wok = i2cWrite(addr, &cmd, 1);
  DBG("HTU21D@0x%02X reset(0xFE) write=%d", addr, wok);
  if(!wok) return false; // 复位命令写失败(NAK)= 设备不在总线上
  delay(50);

  // 读用户寄存器(0xE7, 写命令+重复起始读1字节)验证在线;
  // 若首次读取仍在复位窗口内失败, 再等50ms重试一次
  for(int attempt = 0; attempt < 2; attempt++){
    uint8_t v = 0;
    bool ok = i2cReadReg(addr, HTU21D_REG_USER, &v, 1);
    DBG("HTU21D@0x%02X userreg(0xE7) read attempt%d=%d val=0x%02X", addr, attempt + 1, ok, v);
    if(ok) return true;
    delay(50);
  }
  return false;
}

static bool htu21dRead(uint8_t addr, float* temp, float* humi)
{
  // 使用 no-hold 命令(0xF3/0xF5): 发命令+STOP后固定延时再读, 不依赖从机时钟拉伸。
  // hold-master(0xE3/0xE5)在 ESP32 Wire 驱动下, 从机拉低 SCL 会消耗驱动的
  // 50ms 默认超时, 与最长约50ms的温度转换时间临界相撞, 易间歇失败。
  uint8_t cmd = HTU21D_CMD_TEMP_NOHOLD;
  if(!i2cWrite(addr, &cmd, 1)){
    DBG("HTU21D@0x%02X temp trigger(0xF3) write FAIL", addr);
    return false;
  }
  delay(70); // 温度转换最长约50ms, 留余量
  uint8_t raw[3];
  if(!i2cRead(addr, raw, 3)){
    DBG("HTU21D@0x%02X temp data read FAIL", addr);
    return false; // 2字节数据 + 1字节CRC(忽略)
  }
  uint32_t t = ((uint32_t)raw[0] << 8) | raw[1];
  *temp = -46.85f + 175.72f * (float)t / 65536.0f;
  DBG("HTU21D@0x%02X temp raw=%02X%02X%02X -> %.2fC", addr, raw[0], raw[1], raw[2], *temp);

  // 触发湿度测量
  cmd = HTU21D_CMD_HUMI_NOHOLD;
  if(!i2cWrite(addr, &cmd, 1)){
    DBG("HTU21D@0x%02X humi trigger(0xF5) write FAIL", addr);
    return false;
  }
  delay(50); // 湿度转换最长约25ms, 留余量
  if(!i2cRead(addr, raw, 3)){
    DBG("HTU21D@0x%02X humi data read FAIL", addr);
    return false;
  }
  uint32_t h = ((uint32_t)raw[0] << 8) | raw[1];
  *humi = -6.0f + 125.0f * (float)h / 65536.0f;
  if(*humi < 0.0f) *humi = 0.0f;
  if(*humi > 100.0f) *humi = 100.0f;
  DBG("HTU21D@0x%02X humi raw=%02X%02X%02X -> %.2f%%", addr, raw[0], raw[1], raw[2], *humi);
  return (*temp > -60.0f && *temp < 150.0f);
}

// ===================== 初始化与采集任务 =====================
// 按通道类型调用对应驱动初始化(地址取该通道配置)
static bool sensorChannelInit(uint8_t ch, uint8_t addr)
{
  bool ok = false;
  switch(g_sensor_cfg[ch].type){
    case SENSOR_AHT20:  ok = aht20Init(addr); break;
    case SENSOR_BMP280: ok = bmp280Init(ch, addr); break;
    case SENSOR_LM75:   ok = lm75Init(addr); break;
    case SENSOR_HTU21D: ok = htu21dInit(addr); break;
    default:            break;
  }
  // L18: 记录最近一次成功初始化的 (type, addr)，供运行期配置变更判定是否需要重初始化
  if(ok){
    g_sensor_init_type[ch] = g_sensor_cfg[ch].type;
    g_sensor_init_addr[ch] = addr;
  }
  return ok;
}

bool sensorInit()
{
  // S3: 创建 I2C 互斥锁
  if(g_i2c_mutex == nullptr){
    g_i2c_mutex = xSemaphoreCreateMutex();
  }
  Wire.begin((int)I2C_SDA_PIN, (int)I2C_SCL_PIN, (uint32_t)I2C_FREQ_HZ);
  Wire.setTimeOut(200); // 默认50ms偏紧, 抬升I2C事务超时余量(hold-master/慢速从机)
  delay(10);

#ifdef SENSOR_DEBUG
  // 启动总线扫描: 打印总线上所有响应地址(0x08~0x77), 直接确认传感器是否在线上
  i2cScanBus();
#endif

  bool any = false;
  for(uint8_t ch = 0; ch < SENSOR_CH_MAX; ch++){
    // L17: setup 期间通道循环间喂看门狗，防止初始化耗时过长触发 TWDT 复位
    esp_task_wdt_reset();
    SensorConfig& cfg = g_sensor_cfg[ch];
    g_sensor_ok[ch] = false;
    if(cfg.type == SENSOR_NONE) continue;

    g_sensor_ok[ch] = sensorChannelInit(ch, cfg.addr);
    if(!g_sensor_ok[ch]){
      delay(50); // 上电/复位时序: 给设备额外恢复时间后再重试, 避免两次尝试都落在复位窗口内
      g_sensor_ok[ch] = sensorChannelInit(ch, cfg.addr);
    }
    if(g_sensor_ok[ch]){
      any = true;
      Serial.printf("[SENSOR] CH%u type=%u addr=0x%02X OK\n", ch, cfg.type, cfg.addr);
    }else{
      // 诊断: 探测地址ACK/NAK, 区分"设备不在总线上(接线/地址/上拉)"与"在总线上但初始化协议失败(时序)"
      Serial.printf("[SENSOR] CH%u type=%u addr=0x%02X FAIL (probe %s)\n", ch, cfg.type, cfg.addr,
                    i2cProbe(cfg.addr) ? "ACK" : "NAK");
    }
  }
  return any;
}

// 单通道重初始化(不重开 I2C 总线, 不影响其他通道)
bool sensorReinitChannel(uint8_t ch)
{
  if(ch >= SENSOR_CH_MAX) return false;
  SensorConfig& cfg = g_sensor_cfg[ch];
  if(cfg.type == SENSOR_NONE) return false;

  // S3: 获取 I2C 互斥锁，防止与 loop 线程的 sensorTask 并发访问 Wire 总线
  if(g_i2c_mutex != nullptr) xSemaphoreTake(g_i2c_mutex, portMAX_DELAY);

  g_sensor_ok[ch] = sensorChannelInit(ch, cfg.addr);
  if(!g_sensor_ok[ch]){
    delay(50); // 与 sensorInit 一致: 给设备恢复时间后再重试, 而非紧挨着立即重试
    g_sensor_ok[ch] = sensorChannelInit(ch, cfg.addr);
  }
  if(g_sensor_ok[ch]){
    // L6: 初始化成功，重置失败计数
    g_sensor_fail_count[ch] = 0;
    Serial.printf("[SENSOR] CH%u reinit type=%u addr=0x%02X OK\n", ch, cfg.type, cfg.addr);
  }else{
    Serial.printf("[SENSOR] CH%u reinit type=%u addr=0x%02X FAIL (probe %s)\n", ch, cfg.type, cfg.addr,
                  i2cProbe(cfg.addr) ? "ACK" : "NAK");
  }

  if(g_i2c_mutex != nullptr) xSemaphoreGive(g_i2c_mutex);
  return g_sensor_ok[ch];
}

// 按通道类型采集并填入 SensorData; 不提供的字段保持NaN
static bool sensorChannelRead(uint8_t ch, SensorData& d)
{
  SensorConfig& cfg = g_sensor_cfg[ch];
  // L7: loop 侧取锁加超时(500ms)——secTask 重初始化(sensorReinitChannel)持锁跨多个
  //     delay(20~50ms), 此期间若 loop 无限期阻塞在取锁处, 会连带拖后喂狗/按键响应。
  //     取不到锁则放弃本轮采集(返回 false, 由失败计数与重试机制兜底)
  if(g_i2c_mutex != nullptr &&
     xSemaphoreTake(g_i2c_mutex, pdMS_TO_TICKS(500)) != pdTRUE){
    return false;
  }

  bool ok = false;
  switch(cfg.type){
    case SENSOR_AHT20:
      ok = aht20Read(cfg.addr, &d.temperature, &d.humidity);
      break;
    case SENSOR_BMP280: {
      float t = 0, p = 0;
      if(bmp280Read(ch, cfg.addr, &t, &p)){
        d.temperature = t;
        d.pressure    = p;
        d.altitude    = 44330.0f * (1.0f - powf(p / SEA_LEVEL_PA, 0.1902949572f));
        ok = true;
      }
      break;
    }
    case SENSOR_LM75:
      ok = lm75Read(cfg.addr, &d.temperature);
      break;
    case SENSOR_HTU21D:
      ok = htu21dRead(cfg.addr, &d.temperature, &d.humidity);
      break;
    default:
      ok = false;
      break;
  }

  if(g_i2c_mutex != nullptr) xSemaphoreGive(g_i2c_mutex);
  return ok;
}

void sensorTask()
{
  static uint32_t last_ms[SENSOR_CH_MAX] = {0, 0, 0, 0};
  static uint32_t applied_interval[SENSOR_CH_MAX] = {0, 0, 0, 0};
  uint32_t now = millis();

#ifdef SENSOR_DEBUG
  // 联调: 每10s打印一次I2C在线状态(全地址扫描0x08~0x77 + 各通道配置地址探测),
  // 无需抓启动日志, 运行时随时打开串口即可观察总线上有哪些设备
  static uint32_t i2c_status_ms = 0;
  if(now - i2c_status_ms >= 10000UL){
    i2c_status_ms = now;
    // S3: I2C 扫描也须持锁
    if(g_i2c_mutex != nullptr) xSemaphoreTake(g_i2c_mutex, portMAX_DELAY);
    Serial.printf("[I2C] status: bus{");
    bool any = false;
    for(uint16_t a = 0x08; a <= 0x77; a++){
      Wire.beginTransmission((uint8_t)a);
      if(Wire.endTransmission() == 0){
        Serial.printf("%s0x%02X", any ? " " : "", a);
        any = true;
      }
    }
    Serial.printf(any ? "}" : "none}");
    for(uint8_t ch = 0; ch < SENSOR_CH_MAX; ch++){
      SensorConfig& cfg = g_sensor_cfg[ch];
      // OK/FAIL=初始化状态; en/dis=该通道是否启用采集(未启用则sensorTask不会读取, 上位机显示0.0)
      Serial.printf(" CH%u:%s(%s)", ch, g_sensor_ok[ch] ? "OK" : "FAIL", cfg.enabled ? "en" : "dis");
      if(cfg.type != SENSOR_NONE && !g_sensor_ok[ch]){
        Wire.beginTransmission(cfg.addr);
        Serial.printf("(probe%s)", (Wire.endTransmission() == 0) ? "ACK" : "NAK");
      }
    }
    // 总线空闲电平: H=约3.3V正常; SDA=L=数据线被拉死(短路/器件卡住/接错脚); SCL=L=时钟线被拉死
    Serial.printf(" SDA=%s SCL=%s\n",
                  digitalRead(I2C_SDA_PIN) ? "H" : "L",
                  digitalRead(I2C_SCL_PIN) ? "H" : "L");
    if(g_i2c_mutex != nullptr) xSemaphoreGive(g_i2c_mutex);
  }
#endif // SENSOR_DEBUG

  for(uint8_t ch = 0; ch < SENSOR_CH_MAX; ch++){
    SensorConfig& cfg = g_sensor_cfg[ch];
    if(!cfg.enabled || cfg.type == SENSOR_NONE || !g_sensor_ok[ch]) continue;

    // 实际上报间隔以用户配置 cfg.interval_sec 为准(1~3600s, 默认2s)。
    // L10: 此前固定取"心跳超时/2"导致该配置被静默忽略, 现已改为配置真实生效
    uint32_t interval_ms = (uint32_t)cfg.interval_sec * 1000UL;
    if(interval_ms == 0) interval_ms = 1000UL; // 防零保护
    // 间隔被修改后立即开始新一轮采集
    if(applied_interval[ch] != interval_ms){
      applied_interval[ch] = interval_ms;
      last_ms[ch] = 0;
    }
    if(last_ms[ch] != 0 && (now - last_ms[ch]) < interval_ms) continue;
    last_ms[ch] = now;

    // 采集; 不提供的字段保持NaN, 主机解析后归零展示
    SensorData d = {NAN, NAN, NAN, NAN};
    if(!sensorChannelRead(ch, d)){
      // L6: 连续失败计数，达到阈值后置 g_sensor_ok=false（实时健康而非仅初始化成功）
      if(++g_sensor_fail_count[ch] >= SENSOR_FAIL_THRESHOLD){
        g_sensor_ok[ch] = false;
      }
      // 采集失败: 显式重置为全 NaN——驱动层(lm75/htu21d/aht20)在范围检查失败前
      // 已写出参, d 可能含被判定非法的坏值(如 200℃), 直接存 d 会向 GETSR 上报坏值
      Serial.printf("[SENSOR] CH%u read FAIL type=%u addr=0x%02X\n", ch, cfg.type, cfg.addr);
      sensorSetChannelSnapshot(ch, SensorData{NAN, NAN, NAN, NAN});
      continue;
    }

    // L6: 采集成功，重置失败计数
    g_sensor_fail_count[ch] = 0;
    sensorSetChannelSnapshot(ch, d);
    DBG("CH%u read OK T=%.2f H=%.2f P=%.1f A=%.1f", ch, d.temperature, d.humidity,
        d.pressure, d.altitude);
    bleSendSensorChannelData(ch);
  }
}
