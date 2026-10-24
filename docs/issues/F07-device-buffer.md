# F07 设备：音频缓冲与配对上传

| 项 | 值 |
| --- | --- |
| 负责人 | B |
| 优先级 | P0 |
| 预估 | 3 人日 |
| 依赖 | F04 F06 |
| 阻塞 | — |
| 标签 | area/device, p0, size/L |

## 背景

录音开始时设备不知道这句话要发给谁。等 `bind_message` 回来，得能准确取回"刚才那段"音频。

## 做什么

- 滚动缓冲：保留最近 60 秒 PCM，按 `utterance_id` 分段
- 每个 utterance 记录：`id`、起始时间、PCM、对应 `stt.text`
- 收到 `stt` 时把文本挂到"最近一个已结束的 utterance"上
- 收到 `bind_message{id, text}` 时：
  1. 按 `text` 精确匹配 utterance
  2. 匹配不到 → 归一化（去标点）后再匹配
  3. 仍失败 → 取最近一个 utterance，并打点降级
- 上传：`POST /v1/messages/{id}/audio`，失败重试 2 次（间隔 300ms / 1s）

## 不做什么

- 不做本地 VAD 切句（V0 接受整段上传，混句也认）
- 不做本地压缩（PCM 直接传，2MB 上限够 60 秒）
- 不做断点续传

## 验收标准

- [ ] 说一句 → 2 秒后 `bind_message` 到达 → 上传的音频正好是那一句（人工听辨）
- [ ] 说两句间隔 5 秒，`bind_message` 指向第一句时上传第一句，不串
- [ ] 缓冲超过 60 秒自动淘汰最旧的，内存不增长（跑 30 分钟观察 RSS）
- [ ] 上传失败重试 2 次后放弃，日志有明确原因，不影响会话继续
- [ ] 配对失败走降级路径时，打点计数 +1

## 技术要点

```go
type utterance struct {
    ID     string
    At     time.Time
    PCM    []byte
    STT    string   // stt 消息回来时装上
}
```

- 环形缓冲用固定容量切片，避免频繁 GC
- `stt` 与 utterance 的关联靠"最近结束"启发式，因为协议没有 utterance 标识
- 上传在独立 goroutine，不阻塞 WS 读循环
