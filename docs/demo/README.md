# Demo 预检与演示边界

当前 `#12` 只提供预检、结构化报告和后续进程编排的最小框架。**不启动 Docker、不访问数据库、不联网**，也不会用旧上传媒体冒充 V2 原声媒体。缺少真实 V2 原声媒体时，默认运行必须报告 `blocked` 并以非零状态退出。

## 90 秒三设备 V0 步骤

以下是组件就绪后的目标脚本，**当前未自动执行**；准备依赖不计入 90 秒。

| 时间 | 操作与人工验收 |
| --- | --- |
| 0～20 秒 | A：“小牛，建个群叫夜班组，口令12345”；B/C 口令入群，确认焦点及安全提示“现在直接说话，全群都能听到”在首次发言前播出 |
| 20～35 秒 | A 无提醒词说一段可辨识原声，B/C 同时听到；核对灵汐无对应上行音频/工具调用，记录说完到对端出声 ≤1 秒 |
| 35～45 秒 | B/C 同时说不同内容，A 同时听到两路，不排队、不丢失、不卡顿 |
| 45～60 秒 | 切到第二群，旧群不出声、新群可对讲；切群后再次播安全提示 |
| 60～75 秒 | 检查非焦点群静音和切入自动解除静音；完整的“连续30秒零播放/提醒”专项另行计时，不挤进90秒脚本 |
| 75～90 秒 | 制造群名歧义，≤3选项，10秒内裸答命中；另测拒绝、未命中重新分流、恰好到期和超时不执行旧操作 |

准备三台设备、不同 MAC、可区分预录音频和独立数据库。当前预检不创建/访问数据库，后续装配必须用独立临时数据且不得清理用户数据。保存自动报告、原始事件、音频测量及人工听辨记录；任何 failed、blocked 或 not_run 不能计为通过。手动麦克风模式须预检权限和输出设备；自动化不要求声卡。

V0.5 是独立阶段：单独准备其运行物料、接口验证和报告，不把 V0 的旧上传结果当作 V2 证据。当前仓库只实现框架，尚未实现完整的 #12 进程编排或 V2 演示启动。

真实接口需遵循 MVP 约定：PCM、20ms 音频帧、`listen.detect`，以及 MCP payload schema。凭证只从环境变量读取，尤其是 `LINX_TOKEN`，绝不写入日志或报告。V2 真实原声媒体缺失时保持 BLOCKED。

## 指令 CLI

已提供独立 `cmd/voicedrive-command`，保留原设备控制面入口。先构建，再使用类似参数：

```text
voicedrive-command -mac AA:BB:CC:DD:EE:01 -server http://127.0.0.1:8080 -lingx-url ws://127.0.0.1:9000 -wake-word 小牛
```

令牌从本地安全环境变量 `LINX_TOKEN` 注入，不要把实际值写进命令历史或文档。本阶段不提供 OTA，需使用已绑定 MAC。默认真实 WS 为 `wss://xrobo-io.qiniuapi.com/v1/ws/`。与模拟服务联调时也使用显式本地测试凭据；不将其当生产 token。

按 `mvp:cmd/linx-emu/main.go` 接口，hello 采用 PCM 16kHz mono s16le、320 samples/640 bytes/20ms，而非旧文档的 Opus/60ms。等待 hello.session_id 后发送 listen.start/manual 和 listen.detect/text，MCP 走 type:mcp + payload，初始化 capabilities.tools 为对象。客户端只运行一次连接生命周期，断线明确失败，不自动重连。下行音频由 `linx.Player` 播放泵消费，经 ffplay 从 stdin 流式播放（句间 600ms 空闲优雅收口、抢话打断）；未安装 ffplay 时降级为仅输出文本，不代表音频验收通过。

V2 HTTP 桥接目标为 `/v1/group/*`（list 为 GET），身份仅由 X-VD-MAC 注入。当前 main 服务仍主要是 `/v1/groups/*` 旧接口，真实服务尚不能直接完成10工具联调，不能把默认地址可达视为能力就绪。可用本地 fake 完成协议/执行链路测试。

## 离线验证与预检

```sh
make test-demo
```

该入口测试真实本地分流 → fake 灵汐 WS → MCP → fake voicedrive HTTP → 反馈，以及确认边界、进程清理和预检；不是实际模型准确率或原声端到端报告。`make demo`/`make demo-check` 默认报告到新的 `report.json`；通过 `DEMO_ARGS='-report another-report.json -config local-demo.json'` 指定配置，已有报告不覆盖。即使提供音频文件，原声转发/混音/运行装配仍明确阻塞。

配置示例（路径需自行构建准备；设备 port 是预留测试入口，不表示当前 CLI 已监听该端口）：

```json
{"service":{"path":"./bin/server","port":8080},"mock":{"path":"./bin/linx-mock","port":18080},"devices":[{"mac":"AA:BB:CC:DD:EE:01","path":"./bin/voicedrive-command","port":18101},{"mac":"AA:BB:CC:DD:EE:02","path":"./bin/voicedrive-command","port":18102},{"mac":"AA:BB:CC:DD:EE:03","path":"./bin/voicedrive-command","port":18103}],"media":{"v2_audio":"./local-audio.wav"}}
```

## 报告和状态

`-check` 默认开启；`-config` 接受严格 JSON 配置，未知字段和非三设备/重复 MAC 都会失败。`-report` 只创建权限 `0600` 的新文件，若文件已存在则拒绝覆盖。状态只有 `passed`、`failed`、`blocked`、`not_run`；所有未验证项目必须在报告中出现。
