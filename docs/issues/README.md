# V0 Issues 索引

V0 = 语音群消息演示系统。成品是电脑上的模拟设备，不是真机。

## 文件

| 文件 | 内容 |
| --- | --- |
| [EPIC.md](EPIC.md) | 产品设计总流程：主链路、四条核心流程、约束、里程碑 |
| [FEATURES.md](FEATURES.md) | 功能列表：功能 → 负责人 → issue → 依赖 |
| `F01`~`F05` | 云端（A） |
| `F06`~`F09` | 模拟设备（B） |
| `F10`~`F12` | 平台与验收（C） |

## 依赖顺序

```
F01 骨架
 ├─ F02 MCP 工具 ──┬─ F05 群名归一化
 │                 └─ F04 消息音频
 └─ F03 投递 ────────── F08 控制面客户端
                         │
F06 说话 ── F07 缓冲配对 ─┘
                         └─ F09 播放仲裁
F10 智能体 ─┐
F11 mock 剧本 ─┼─ F12 端到端验收
F01~F09 ─────┘
```

## 标签约定

| 标签 | 含义 |
| --- | --- |
| `area/cloud` `area/device` `area/platform` | 归属 |
| `p0` `p1` | 优先级 |
| `size/S` `size/M` `size/L` | 预估 |

## 建 issue

```bash
gh label create area/cloud    --color 5319e7
gh label create area/device   --color 0e8a16
gh label create area/platform --color fbca04

for f in docs/issues/F*.md; do
  title=$(head -1 "$f" | sed 's/^# //')
  gh issue create --title "$title" --body-file "$f" \
    --label "$(grep -m1 '| 标签 |' "$f" | sed 's/.*| \(.*\) |/\1/')"
done
```
