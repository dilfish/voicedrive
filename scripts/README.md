# Mock 剧本

`cmd/linx-mock` 提供两种执行方式，均输出每步通过/失败、耗时和错误到 `report.json`；断言失败立即停止并以退出码 1 结束。默认总超时 1 分钟，支持 `-timeout` 调整。

## 平台 WebSocket

```sh
go run ./cmd/linx-mock -listen 127.0.0.1:9090 \
  -script scripts/platform.json -report report.json
```

设备连接 `ws://127.0.0.1:9090/v1/ws/`。可用 `-mac` 要求连接带指定 `X-VD-MAC`；每次运行只接受一个设备会话。端口可设 `127.0.0.1:0`，启动时打印实际地址。

步骤字段：

| action | 行为 |
| --- | --- |
| `send` | 将 `send` 对象作为 JSON 文本帧发送；支持 stt、MCP initialize/tools/list/tools/call、TTS 生命周期及错误码等任意信封 |
| `expect` | 读取设备 JSON，按 `expect` 逐字段断言 |
| `timeout` | 在 `timeout_ms` 内不回复，断言设备未发送数据；该步骤应放在末尾，因为读取超时后连接无法继续读 |
| `disconnect` | 发送 1012 关闭帧，随后结束会话时关闭连接；应放在末尾 |

`expect` 使用点分路径，支持数组下标，例如 `payload.result.content.0.text`。`timeout_ms` 默认 3000，每步同时受总超时限制。`scripts/tool-error.json` 演示调用未知工具并断言设备返回 `isError:true`。mock 不伪造设备回执，工具返回错误需由设备或测试适配器产生。

协议采用 docs/02-linx.md 中的 `type:mcp` + `payload` 信封。本阶段不做 ASR、LLM、Opus 编解码或真实 TTS；JSON 剧本直接指定识别文本和工具参数。

## HTTP 场景

先启动 PostgreSQL 和 voicedrive server，再运行：

```sh
go run ./cmd/linx-mock -server http://localhost:8080 \
  -script scripts/demo.json -report report.json
```

示例覆盖两设备注册、建群、入群、切群、创建消息、缺少上传身份的错误、静音 PCM 上传及音频下载。它不监听设备控制面 WebSocket，不能证明 `bind_message` 或投递已被设备处理。

HTTP 步骤的 `action` 可省略或设为 `http`；支持 `method`（默认 POST）、`path`、`mac`、`body`、`expect_code`（默认 200）、`expect`、`capture`。`capture` 将响应字段保存为变量，例如 `{"group_id":"id"}`，后续用 `${group_id}` 引用。变量作为字符串替换，适合 ID 和路径。音频可用 `audio_file`（相对当前工作目录）或 `silence_ms`（16kHz 单声道 s16le，最多 60 秒）。

## 自动验证与当前边界

```sh
make test-mock
```

测试不依赖数据库、外网或声卡，用测试 WebSocket 设备验证 hello、stt、工具名/参数、返回文本、isError、断言失败和超时；HTTP 测试验证动态 ID 引用及失败即停。

当前设备入口尚无灵犀 WebSocket/MCP 客户端（Issue #14），因此 `platform.json` 需要兼容客户端或测试适配器。Issue #11 的“说话 → tools/call → bind_message → 上传 → 投递”完整设备验收尚未闭环；CI 协议测试不能替代该端到端验收。
