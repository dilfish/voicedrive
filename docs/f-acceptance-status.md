# 已开发 F 系列能力与待验收项

本清单依据 issue 验收文字和当前代码核对。单元测试通过不等于全部验收完成。灵犀客户端由其他开发者负责；本文列出的输入接口供其装配，不另行实现客户端。

| Issue | 本次补齐 | 仍需完成或验证 |
| --- | --- | --- |
| F01 | — | PostgreSQL 真实集成、并发 100 条 seq 与幂等验收；现有 store 单测未覆盖数据库 |
| F03 | — | 1000 连接压测、真实数据库下断线补发验收 |
| F04 | 上传前校验消息归属，拒绝返回 403；临时文件原子替换；WAV chunk 校验；60 秒上限 | 3 秒 TTS 兜底、失败状态通知、签名 URL、存储接口；真实数据库下越权及并发上传测试 |
| F06 | PCM 640 字节分片、尾帧补零；ffmpeg 采集与取消回收；WAV metadata/扩展 fmt chunk 解析；取消时不继续等待控制面连接 | 回车启停录音、静音 1.5 秒停止、CLI 麦克风装配、实时上行节奏、多设备音频验收；灵犀发送由客户端负责人接入 |
| F07 | 精确匹配优先于归一化；FallbackCount；回调移出锁；超大 utterance 拒绝；移除已绑定音频引用；SetLatestTranscript；可取消重试；原始 PCM HTTP 上传测试 | 真实音频听辨、30 分钟 RSS 观察；STT 回调装配由客户端负责人完成 |
| F08 | — | 设备 token/device_id 协议与现有 MAC 模式统一；双 WS 在线验收需客户端完成 |
| F09 | 下载请求使用播放 context，Stop 可取消下载 | 真正边下边播、原始 PCM 的播放器格式支持、溢出语音提示、TTS/abort 事件装配；打断保留未读与关闭竞态需继续验证 |
| F11 | JSON HTTP/WS 剧本、错误回执断言、超时/断连、静音 PCM 上传、报告与退出码 | 真实服务端的 bind/upload/delivery 全链路；目前 WS 测试适配器只验证平台协议，不代替端到端验收；静音 TTS 二进制帧未实现 |

## 给灵犀客户端负责人的输入接口

- `device.StreamPCM(ctx, reader, send)`：每次回调 640 字节，16kHz mono s16le；最后不足 20ms 的帧补零。该函数不限速，网络发送方决定发送节奏；阻塞 reader 的拥有者负责取消时关闭 reader。
- `device.CaptureMicrophone(ctx, input, send)`：调用 ffmpeg 默认音频后端，输出同样的 PCM 帧。取消 context 时终止并回收采集进程。macOS input 为音频设备编号（例如 `0`），首次运行需允许终端访问麦克风，并预装 ffmpeg。
- `buffer.SetLatestTranscript(text)`：将 STT 文本挂到最近已完成的 utterance。
- `buffer.FallbackCount()`：读取文本未匹配而回退最近音频的累计次数；`OnFallback` 可在回调内读取该值。
- `queue.SetTTSSpeaking(bool)` 和 `queue.Interrupt()`：已有仲裁入口，客户端负责连接 TTS 生命周期及 abort 信令。

## 验证

`go test ./...`、`go test -race ./internal/device ./internal/server ./internal/mock`、`go vet ./...`、`git diff --check` 均通过。采集清理测试用独立测试子进程，不依赖麦克风或 ffmpeg；实际声卡采集尚未人工验收。
