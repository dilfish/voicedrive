# V2 剧本化灵矽 mock（#11）

这是独立于 #10 的协议框架，不是 ASR/LLM，也不是可替代真实设备的模拟实现。当前 main 尚缺 #24/#27/#28 的 MCP、灵矽客户端和 V2 设备装配，故 #11 **尚未全部验收**。

## 运行

```sh
go run ./cmd/linx-scriptmock -script docs/mock/handshake.json -listen 127.0.0.1:18080 -mac TEST-01 -report report.json
```

让设备连接 `ws://127.0.0.1:18080/?mac=TEST-01`。每次新建报告路径，避免覆盖证据。默认总超时 5 秒，可用 `-timeout 30s` 覆盖，上限 4 分钟。`make test-mock` 离线执行 race 测试，无麦克风、凭证或真实灵矽需求。默认 `make` 仍为构建。

本入口原名 `cmd/linx-mock`；合入 main 的 #30 后，为避免覆盖既有功能改名为 `cmd/linx-scriptmock`。原 `cmd/linx-mock`、`internal/mock` 和 `scripts/` 保持 main 的实现，使用方式见 [scripts/README.md](../../scripts/README.md)。两套 DSL、MCP 信封、MAC 传递方式及报告格式不同，不可混用；后续设备适配需要显式选定协议。本次只做共存兼容，不宣称完成协议统一。

单实例只接受一台设备的一条连接；多设备测试启动不同端口/MAC/报告路径的实例，不共享状态。默认仅绑定回环地址。退出时关闭连接与 HTTP 服务，报告权限 0600。不要输入凭证，记录器会完整记录所有应用层上行帧；录制文件不要直接公开。

## JSON DSL v1

顶层：`version:1`、可选 `mac`、`timeout_ms`（0 为默认 5000，最多 240000）及非空 `steps`（最多 1000）。未知字段和多余 JSON 文档报错。

| op | 字段 | 含义 |
| --- | --- | --- |
| send | frame 对象 | 平台向设备发送精确 JSON 文本帧；可发送 MCP 请求、控制事件或错误响应 |
| expect | frame 对象、timeout_ms | 等待下一条上行文本帧，与整个 JSON 结构精确比较；工具名、参数、回执均可比较，多余字段也失败 |
| quiet | timeout_ms | 观察窗口内零上行数据帧，包含二进制音频、文本和 MCP 帧；连接关闭也失败 |
| input | text、timeout_ms | 经显式 Adapter 注入设备真实输入入口，不直接制造 tools/call |
| advance | advance_ms、timeout_ms | 经 Adapter 推进设备测试时钟，毫秒数为正 |
| disconnect | 无 | 平台主动断连，供失败注入；当前会话不支持重连，重连联调待后续扩展 |

单步默认超时 1 秒，上限 60 秒，仍受全局超时约束。`quiet` 比“零音频/零 tools/call”更严格：任何应用上行帧都失败，心跳场景请另设专用剧本。工具调用正常方向是 **平台 → 设备**；因此上行零 tools/call 仅检查设备误发，是否未触发下行调用还需结合剧本步骤和真实输入适配证据。WebSocket ping/pong/close 控制帧不作为数据帧录制。

外层 `{"type":"mcp","payload":{...}}` 是本地 mock **约定**；现有文档只确认 type:mcp 包装，payload 字段及控制事件字段须等真实平台联调固化。本工具不假装该约定已被真实设备支持。不会自动猜测 hello、MCP 响应或工具参数，所有期望显式写在剧本里。错误注入区分业务状态 error、JSON-RPC error、isError；不强制将业务失败改成协议错误。

## 真实输入适配接口

可通过 `-adapter http://127.0.0.1:19090/test-input` 启用本地测试入口，仅允许回环 HTTP，无凭证、query 或跳转。POST 请求为 `{"op":"input","text":"小牛建群"}` 或 `{"op":"advance","advance_ms":10000}`，设备在其真实输入/定时器串行循环处理后返回 204。适配器必须遵守 context 取消，绝不能直接执行工具绕过提醒词分流和确认状态。

未配置 adapter 的 input/advance 明确失败并报告 BLOCKED。HTTP 204 只是入口接收凭据，成功路由仍需 expect/quiet 和真实设备状态证据验证。当前仓库未实现此设备测试入口，不应把 input 剧本标为通过。接口由消费者定义，可在 Go 集成测试注入其他 Adapter 实现；实现必须遵守取消约束，不得启动不受控 goroutine。

## 报告与退出码

报告含 passed、总耗时、失败原因、逐步期望/实际/状态/耗时及全部收到的数据帧。实际帧使用 `data_base64` 无损保存，错误信息另含期望与实际。任一步失败立即停止；未执行步骤不列为成功。

直接编译运行时退出码：0 全部步骤通过、1 运行/断言失败、2 参数/文件/剧本错误。`go run` 会包装非零退出码，CI 如需区分 1/2 请先构建二进制。报告路径必须尚不存在；启动失败可能留下空报告，不能将其解析为成功。

## 已验与待验

- 已验：真实 WS 上的协议测试客户端、JSON 精确断言、限时等待、主动断连、零帧观察、适配接口及错误状态、独立实例、报告和退出码。协议测试客户端仅测框架，不是真实设备，也不是 LLM。
- 待 #24/#27/#28：实际 hello/initialize/tools/list/tools/call 完整执行、handler → fake voicedrive → 设备响应链路、真实输入分流（默认/自定义提醒词）和录音零上行专项。
- 待真实 confirm：10 秒裸答、未命中重新分流、deadline 事件顺序、拒绝和超时无副作用；框架仅提供 input/advance，不实现确认状态机。
- 待 V0.5：邀请/拍一拍对方确认、拒绝、超时及静音群不打扰；不自造未对齐接口。
- 原声对讲及混音不是本 mock 职责；静音音频/协议回执不能证明原声链路成功。

新增场景只改 JSON。`handshake.json` 展示 MCP 序列的结构模板，tools/list 的空数组是协议测试占位，真实设备需按实际注册的 10 工具替换该期望，不能以空数组证明设备注册完成。`error.json` 展示错误回执期望，`blocked-input.json` 是需真实 adapter 的提醒词/quiet 场景。
