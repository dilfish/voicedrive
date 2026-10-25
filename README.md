# voicedrive

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

完整的开发协作文档见 [docs/collaboration](docs/collaboration/README.md)。
