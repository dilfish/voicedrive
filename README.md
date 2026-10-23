# voicedrive

面向司机的语音对讲机（基于灵矽 AI 的端侧/云端 MCP）。

- 架构设计：[docs/architecture.md](docs/architecture.md)
- 设备模拟器：[cmd/linx-emu](cmd/linx-emu) — 冒充 ESP32-S3，联调灵矽设备协议
- 平台 mock：[cmd/linx-mock](cmd/linx-mock) — 本地冒充灵矽，测试期间不连外网

## 本地联调（推荐，不碰真实接口）

```bash
# 终端 1：起 mock 平台
go run ./cmd/linx-mock                  # 127.0.0.1:18090

# 终端 2：模拟设备指向 mock
go run ./cmd/linx-emu -mac AA:BB:CC:DD:EE:01 \
  -ota http://127.0.0.1:18090/v1/ota/ \
  -ws  ws://127.0.0.1:18090/v1/ws/ \
  -say "把音量调到30" -abort 800ms
```

mock 默认 `-bind-after 2`：同一 MAC 第一次 OTA 返回激活码，第二次自动绑定并下发 token。
也可以 `curl -X POST localhost:18090/_bind -d '{"mac":"AA:BB:CC:DD:EE:01"}'` 手动绑定，
`GET /_devices` 查看已注册设备。

mock 的对话是规则驱动的（含"音量"→ `self.audio.volume`，含"状态/电量"→ `self.device.status`），
TTS 用 2 秒 PCM 静音代替真实音频。

## 对接真实灵矽

```bash
# 1. 首次：拿到 6 位激活码，去控制台「添加设备」绑定到自己的智能体
go run ./cmd/linx-emu -mac E4:5F:01:23:45:67

# 2. 绑定后重跑：OTA 返回 ws 地址与 token，自动建连并发 hello
go run ./cmd/linx-emu -mac E4:5F:01:23:45:67

# 3. 免麦克风验证 tools/call：用 listen.detect 注入一句话
go run ./cmd/linx-emu -mac E4:5F:01:23:45:67 -say "把音量调到30"

# 4. 验证打断，并在打断后于同一 session 再问一次
go run ./cmd/linx-emu -mac E4:5F:01:23:45:67 -say "现在几点" -abort 1500ms
```

`-mac` 必须固定，绑定以它为准。
