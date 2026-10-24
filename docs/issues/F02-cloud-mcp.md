# F02 云端：MCP Server 五个工具

| 项 | 值 |
| --- | --- |
| 负责人 | A |
| 优先级 | P0 |
| 预估 | 3 人日 |
| 依赖 | F01 |
| 阻塞 | F05 F10 |
| 标签 | area/cloud, p0, size/L |

## 背景

灵矽通过 MCP 把"用户想干什么"交给我们。这是产品唯一的功能入口。

## 做什么

实现 MCP 协议端点：`GET /mcp`（SSE）+ `POST /mcp/message`，JSON-RPC 2.0。

五个工具：

| 工具 | 参数 | 返回（≤40 字） |
| --- | --- | --- |
| `group.send_voice` | `group`, `text` | "好，已发给夜班组的老王和小李" |
| `group.create` | `name` | "群建好了，口令 482913" |
| `group.join` | `code` | "已加入夜班组，共 5 人" |
| `group.list` | — | "你在 3 个群：夜班组、家庭群、沪宁线" |
| `group.switch` | `group` | "现在只听夜班组的消息" |

## 不做什么

- 不做端侧 MCP（设备侧由 `linx-emu` 负责，与本服务无关）
- 不依赖 `structuredContent.funcs` 驱动设备

## 验收标准

- [ ] `initialize` / `tools/list` / `tools/call` 三条链路都能用 curl 打通
- [ ] 身份只从 `X-Linx-Device-Id` 取；伪造 body 里的 device_id 无效
- [ ] `group.send_voice` 在音频未就绪时就返回，P95 < 300ms
- [ ] 相同 `device + text` 在 30 秒内重复调用只产生一条消息
- [ ] 缺参数返回 `isError: true` + 反问，不编默认值
- [ ] 返回文本长度断言 ≤ 40 字（单测覆盖）

## 技术要点

- 身份三件套：`X-Linx-Device-Id` / `X-Linx-Agent-Id` / `X-Linx-Session-Id`，只信前者
- 幂等键：`device_id + text + 30s 窗口`
- 群名解析交给 F05，这里只调用
- 工具描述要写成"什么时候用"，不是"这个工具干什么"——模型靠它决定调用
