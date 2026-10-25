# 指令 Agent v2.1

本目录是 #10 的配置说明与 JSON 语料，不是实现代码或真实验收报告。产品基线为 [product-v2.md §4、§5](../../product-v2.md)，工具签名以 [docs/02-linx.md §6.2](../02-linx.md) 为准；确认状态参见该文档 §7、§9。存在冲突或未落实细节时应记录缺口，不以提示词替代设备实现。

## 产物与范围

- `prompt.md`：控制台系统提示词，版本 v2.1。
- `tools.json`：10 个工具的说明清单（数组，字段为 `name`、`description`、`phase`），不是可以直接导入的 Function Call JSON Schema。
- `commands.json`：命令评测输入，含 30 条独立 V0 正向工具指令，以及路由、缺参、歧义、拒绝、确认和超时边界。
- `resolution.json`：独立的 30 条群名解析契约样例，**不是命令评测器输入**；`expected.action` 为 `matched`、`ambiguous`、`not_found`，`arguments` 承载 `group_id` 或 `options`。

V0 覆盖 `group.create/join/switch/mute/list`。V0.5 包括 `group.leave/rename/pat/invite/query_member`，需要在 V0 后单独联调；当前语料不代表它们已验收。F12 改别名是产品需求，但 §6.2 的 10 工具中没有改别名入口：不得杜撰 `alias.rename` 或用改群名代替，应记录接口缺口。`group.send_voice` 已退役；普通说话走原声对讲，不是工具调用。补播推迟 V1。

## 控制台 Function Call 配置

1. 在灵犀控制台的指令 Agent 配置中粘贴 `prompt.md`，记录配置版本 `v2.1` 和实际模型标识；界面名称以当前控制台为准。
2. 按 `tools.json` 逐项配置工具说明，按 §6.2 填写参数和必填项：`create(name, passcode?)`、`join(passcode)`、`switch(name)`、`mute(name, on)`、`list()`、`leave(name)`、`rename(name, new_name)`、`pat(alias)`、`invite(alias, group_name)`、`query_member(group_name, alias)`。除 `on` 为 boolean 外均为 string；`list` 参数对象为空。未说建群口令时不要填 `passcode`。
3. Function Call 若要求 schema，使用 object，逐项声明 properties、required；不要把本清单的 `phase` 当工具参数，也不要把语料的 `action` 当工具名称。设备通过 MCP 的 `tools/list` 提供正式 inputSchema；控制台配置与设备注册必须核对一致，不假定清单能自动导入或直连执行。
4. 不注册 `group.send_voice` 或未定义的改别名工具。身份由可信设备适配层/Header 注入，不允许模型生成 MAC、身份或鉴权参数。
5. 本目录无凭证，不要求把真实 Key、token 或设备标识写进文档/语料。真实接入由操作者在本地安全配置；日志导出前脱敏。

## 上下文与边界口径

每条用例的 `context` 都是明确上下文字符串；各条独立初始化，不继承上一条结果。群号、口令、名字均为合成测试数据。`commands.json` 的 `input` 保留完整开口内容，便于测试本地分流；实际设备命中提醒词后仅把后续内容送给灵犀。

设备本地分流和 `internal/confirm` 的 pending、时钟、事件顺序是**外部依赖**。提示词不能代替它们：无提醒词且无有效确认命中时绝不上行灵犀；自定义提醒词由设备提供。文本降级中的 `intercom` 只表示选择对讲通道，真实播放仍需麦克风或音频注入，不能用一段文字证明原声广播成功。

确认窗口为 10 秒；有效裸答命中才可重放原工具，拒绝取消，未命中重新走分流。恰好 deadline 的语料显式规定“到期事件先处理”，因此期望取消；这只是确定性测试调度，不自行增加全局 `now >= deadline` 规则。若实际采用其他事件顺序，先按 §9 的串行事件规则复核，不宣称产品已经定义同一时间戳的全序。超时用例观察的是到期作废结果，迟到的普通裸答不得恢复原命令。

`clarify` 表示当前轮还不能确定合法调用参数；`reject` 表示明确不可执行；`cancel` 表示 pending 作废；`intercom` 表示本地对讲路由。`tool` 的参数必须使用 §6.2 的签名，解析出的群 ID 不应冒充工具的 `name` 参数。群不存在的负例上下文明确给出权威查询结果，不要求 LLM 猜不存在。

## 群名解析语料

基线为 product-v2 §4.3、F4、§11-9 及 docs/01-voice-group §5.3：精确匹配、别名/群名归一化后处理模糊候选，多命中反问，群可按创建时间/人数描述，选项至多 3 个。具体编辑距离阈值、大小写/标点规则和候选排序并未在这里重新定义。

`resolution.json` 的上下文逐条列出候选群及显式别名；不依赖隐含共享字典。歧义样例只使用明确别名同时指向两群的情形，`options` 是候选群 ID 数组（按上下文列举顺序列出，仅供稳定表达，比较时视为集合，不作为产品排序结论）。未找到样例明确给出权威解析无其他候选；不从“名字看起来不像”推断模糊算法结论。该文件用来独立核对解析层契约，不送入 `agent-eval` 的命令 action 枚举。

## 真实结果采集手册

1. 固定模型、控制台版本、10 工具配置、设备提醒词与服务端测试数据；逐条按 `context` 重置群列表、别名、焦点、静音、pending 和时钟。若环境不能构造某条件，记录阻塞，不填造结果。
2. 通过真实设备指令链路/灵犀会话输入用例，采集实际 Function Call 的工具名与参数；非工具用例同时采集本地路由、反问/拒绝及 confirm 事件证据。不能只把所有输入直接发给模型后声称已覆盖本地分流。
3. 按用例 ID 记录观测结果，保留脱敏原始事件及配置标识以便追溯。无工具时记录实际发生的非工具 action；不得将 expected 复制成 actual。失败、缺失、环境阻塞如实另记，不以预期替补。
4. 保存结果为 `actual.json`，格式如下。示例仅演示格式，不是实际采集；离线构造结果必须用 `source: "fixture"`，只有真实灵犀链路观测才能标为 `real-lingxi`。混合来源应拆文件。

```json
{"source":"real-lingxi|fixture","model":"...","config_version":"v2.1","results":[{"id":"v0-create-01","actual":{"action":"tool","tool":"group.create","arguments":{"name":"夜班组"}}}]}
```

`source` 实际取值是 `real-lingxi` 或 `fixture`，不是带竖线的字面值。非工具 actual 如 `{"action":"clarify"}`，不填 `tool`，可省略 `arguments`。工具调用结果只评 action、工具与参数，**不包含 TTS 评价**；话术长度、安全提示、原声延迟、音质与是否实际播出须另做端到端检查。

评测命令（在仓库根目录执行）：

```sh
go run ./cmd/agent-eval -corpus docs/agent-v2/commands.json -results actual.json -report report.json
```

## 评分与退出码

- 工具选择：以所有期望为 `tool` 的用例为分母，action 和工具名均正确才命中；阈值 ≥90%。
- 参数抽取：同样以所有期望为 `tool` 的用例为分母，工具及全部参数完整匹配才命中；阈值 ≥85%。多余参数也算错误；不对一条用例的多个字段部分计分。
- `clarify/reject/intercom/cancel` 按类别单独统计，专项必须全部通过。不涉及的话术和反问选项内容不在此评分中，须结合原始记录人工复核。
- 缺失、重复、未知 ID、未知字段、重复 JSON key、未知工具、缺必填参数及非法参数类型视为输入错误，不能获得通过报告。合法但错误的工具/参数保留在报告的逐条 expected/actual 差异中。
- 退出码：0 表示该离线数据达到评分门槛，1 表示指标或专项未通过，2 表示参数、输入或文件错误。`go run` 会包装非零程序退出码；自动化如需直接读取 1/2，应先 `go build -o ./agent-eval ./cmd/agent-eval` 再运行二进制。
- 报告文件必须尚不存在，避免覆盖历史证据或输入；每轮使用独立路径。`source` 由操作者声明，评测器不验证来源真实性。fixture 的报告带 `fixture-only`；真实来源也标为用户提供的记录，不是独立验证。
- `make test-agent` 校验语料结构、工具清单和评测逻辑。语料自检将 expected 用作 fixture，只验证数据一致性，绝非真实模型准确率。

## 尚待验收

- 操作者完成远端控制台配置并记录实际模型/版本；本次不操作控制台。
- 真实灵犀准确率及原始证据、反馈 ≤40 字、TTS ≤8 秒。
- 设备提醒词分流、确认状态机及真实服务端联调；当前仓库模拟器仍有旧消息链路，不能直接用旧 `-say` 作为本指令链路证据。
- V0.5 工具逐项联调、改别名接口对齐；原声媒体与端到端演示交由对应模块及 #12 验收。

当前文档交付没有凭证，也没有进行真实灵犀采集，不宣称真实验收通过。fixture 只能验证评测管线；真实命令指标也不能代替完整产品验收。
