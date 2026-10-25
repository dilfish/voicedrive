# voicedrive

## V2 智能体配置与评测（#10）

版本化 prompt、工具描述、指令及群名语料和离线评测说明见 [docs/agent-v2](docs/agent-v2/README.md)。离线评测不调用灵矽，不代表真实模型或端到端验收已通过。

## V2 剧本化 mock（#11）

JSON 剧本、独立 WebSocket mock、输入适配接口和验收边界见 [docs/mock](docs/mock/README.md)。运行 `make test-mock` 可离线验证框架；真实设备指令及确认链路仍需依赖组件就绪后联调。

## F01 服务骨架

按 issue #2 提供 Postgres 服务骨架、设备注册、群/成员/消息模型及群内消息序号。

```bash
make db
make run
```

默认监听 `:8080`，数据库使用 `DATABASE_URL` 覆盖。接口示例：

```bash
curl -X POST localhost:8080/v1/devices \
  -H 'Content-Type: application/json' \
  -d '{"mac":"AA:BB:CC:DD:EE:01","name":"老王"}'
```

设备控制面 WebSocket 地址为 `ws://localhost:8080/v1/device`，使用 `X-VD-MAC` Header 标识设备。设备可以加入多个群，但服务端只向设备当前 `active_group_id` 对应的群转发和补发消息；切换焦点群后，收听目标随之切换。服务端下发 `message`，设备上行带 `group_id` 的 `played` 和 `heartbeat`；确认序号按设备和群分别保存，重连后当前焦点群最多补发 50 条消息。

切换焦点群：

```bash
curl -X POST localhost:8080/v1/groups/<group-id>/switch \
  -H 'X-VD-MAC: AA:BB:CC:DD:EE:01'
```

设备在线时，切换成功还会收到 `group_changed` 帧，设备应清空旧群播放队列并切换到返回的群。

设备端控制客户端可直接运行：

```bash
go run ./cmd/voicedrive-device -mac AA:BB:CC:DD:EE:01 \
  -server ws://localhost:8080/v1/device
```

`bind_message` 会通过客户端回调交给音频缓冲/上传模块；连续 3 次 heartbeat 写入失败后客户端主动断开并进入重连退避。

消息音频上传接口：

```bash
curl -X POST http://localhost:8080/v1/messages/<message-id>/audio \
  -H 'X-VD-MAC: AA:BB:CC:DD:EE:01' \
  -H 'Content-Type: audio/wav' \
  --data-binary @voice.wav
```

V0 接受 16 kHz、单声道、16-bit PCM 或 WAV，大小上限 2 MB。没有 `audio_url` 的消息先通过 `bind_message` 请求发送设备上传，上传成功后才向当前焦点群投递 `message`。

完整的开发协作文档见 [docs/collaboration](docs/collaboration/README.md)。
