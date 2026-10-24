# F11 平台：mock 剧本化

| 项 | 值 |
| --- | --- |
| 负责人 | C |
| 优先级 | P1 |
| 预估 | 2 人日 |
| 依赖 | — |
| 阻塞 | F12 |
| 标签 | area/platform, p1, size/M |

## 背景

真实灵矽要花钱、依赖网络、结果不稳定。`cmd/linx-mock` 已经能顶替平台，但对话还是写死的规则，没法编排多轮场景。

## 做什么

- 剧本文件（YAML/JSON）：一串步骤，每步声明"用户说什么 → 期望设备回什么"
- mock 按剧本驱动：发 `stt`、发 `tools/call`、校验设备回执
- 支持断言：工具名、参数、返回文本
- 支持失败注入：超时、错误码、断开连接
- 输出：`report.json`（通过/失败/耗时）+ 退出码

## 不做什么

- 不做真实 ASR/LLM（那是真机联调的事）
- 不模拟音频内容（TTS 用静音帧代替）

## 验收标准

- [ ] 一条剧本能串起：说话 → tools/call → bind_message → 上传 → 投递
- [ ] 断言失败时打印"期望 vs 实际"，退出码非 0
- [ ] 失败注入（工具返回 isError）能被剧本断言到
- [ ] CI 里 `make test-mock` 5 分钟内跑完
- [ ] 新增剧本不改 Go 代码

## 技术要点

```yaml
- say: "给夜班组说一句我快到了"
  expect_tool: group.send_voice
  expect_args: { group: "夜班组", text: "我快到了" }
  then: bind_message
```

- 剧本与 `linx-emu` 的 `-say` 解耦：mock 自己驱动设备，不需要人工
- 端口、MAC、剧本路径都走参数，便于并行跑多份
