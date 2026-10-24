# F01 云端：服务骨架与数据模型

| 项 | 值 |
| --- | --- |
| 负责人 | A |
| 优先级 | P0 |
| 预估 | 2 人日 |
| 依赖 | — |
| 阻塞 | F02 F03 F04 F05 |
| 标签 | area/cloud, p0, size/M |

## 背景

所有业务状态都在这一个服务里：谁在哪个群、谁发了什么、谁听到了。先把它立起来，其余 issue 才有落点。

## 做什么

- 单个 Go 服务，进程内分模块：`group` / `message` / `mcp` / `deliver` / `audio`
- Postgres 建表：`device` / `group` / `membership` / `message`
- 设备注册：`mac → device_id`，首次出现自动建档
- `message.seq` 按群单调递增（`SELECT ... FOR UPDATE` 或 advisory lock）
- 本地开发用 docker-compose 起 Postgres，一条 `make db` 就绪

## 不做什么

- 不做 Redis、不做消息队列、不做分库分表
- 不做鉴权体系（V0 用设备 MAC 当身份，够演示）

## 验收标准

- [ ] `make db && go run ./cmd/server` 能起来，日志打印监听地址
- [ ] 建群 → 加两个成员 → 发一条消息，数据库里能查到完整链路
- [ ] 同一群并发插入 100 条消息，`seq` 无重复、无空洞
- [ ] `go test ./internal/...` 覆盖 seq 分配与幂等去重

## 技术要点

```sql
device(id, mac, name, agent_id, active_group_id, last_acked_seq, last_seen_at)
"group"(id, name, owner_device_id, invite_code, created_at)
membership(group_id, device_id, alias, joined_at)          -- alias: 老王
message(id, group_id, from_device_id, text, audio_url,
        duration_ms, seq, created_at)
```

`alias` 是群里显示的名字，不要用 MAC 当称呼。
