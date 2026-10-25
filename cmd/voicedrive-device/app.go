// voicedrive-device 是设备模拟器入口（02-linx §3/§8）：装配灵犀 WS 客户端、
// MCP 工具、确认语境与终端文本分流，覆盖无麦克风降级路径。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/Study-of-slag-weng/voicedrive1/internal/confirm"
	"github.com/Study-of-slag-weng/voicedrive1/internal/linx"
	"github.com/Study-of-slag-weng/voicedrive1/internal/logx"
	"github.com/Study-of-slag-weng/voicedrive1/internal/mcp"
	"github.com/Study-of-slag-weng/voicedrive1/internal/wakeword"
)

// ExpiredPhrase 是确认语境超时作废的固定播报（product-v2 §4.3）。
const ExpiredPhrase = "先不管了，需要再说"

// TalkPrompt 是无提醒词文本输入的降级提示（02 §3：本期无对讲客户端）。
const TalkPrompt = "对讲需音频，请用麦克风或注入文件"

// CommandSink 把提醒词命中后的剩余文本作为 ASR 等价结果送入指令会话。
// 文本上行报文格式待平台联调（02 §11-2），经此接口隔离；fake 环境可断言。
type CommandSink interface {
	SendCommand(text string)
}

// LinxCommandSink 是 CommandSink 的灵犀 WS 实现：
// 以占位信封 type:"asr_text" 文本帧上行（字段名 §11 联调后固化）。
type LinxCommandSink struct {
	Client *linx.Client
}

// SendCommand 实现 CommandSink。
func (s *LinxCommandSink) SendCommand(text string) {
	b, err := json.Marshal(struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{"asr_text", text})
	if err != nil {
		return // 静态两字段 marshal 不可能失败
	}
	s.Client.SendText(b)
}

// App 承载终端文本分流（02 §3）与确认重放；HandleLine 仅由输入循环单线程
// 调用，say 的锁仅为 onExpire 定时器 goroutine 的并发播报而设。
type App struct {
	matcher *wakeword.PrefixMatcher
	confirm *confirm.Machine
	reg     *mcp.Registry
	sink    CommandSink
	out     io.Writer
	logger  logx.Logger
	mu      sync.Mutex
}

// NewApp 构造 App。
func NewApp(wakeWord string, cf *confirm.Machine, reg *mcp.Registry, sink CommandSink, out io.Writer, logger logx.Logger) *App {
	return &App{
		matcher: wakeword.NewPrefix(wakeWord),
		confirm: cf,
		reg:     reg,
		sink:    sink,
		out:     out,
		logger:  logger,
	}
}

// HandleLine 处理一行终端输入，分流顺序（02 §3/§7）：
//  1. 确认语境 Pending → 免提醒词裸答；
//  2. 提醒词前缀命中 → 剩余文本经 CommandSink 进指令会话；
//  3. 未命中 → 对讲降级提示（检测不成立绝不上行灵犀）。
func (a *App) HandleLine(line string) {
	text := strings.TrimSpace(line)
	if text == "" {
		return
	}
	if a.confirm.Active() {
		switch res := a.confirm.Answer(text); res.Kind {
		case confirm.KindHit:
			a.say(a.replay(res))
			return
		case confirm.KindExpired:
			return // 作废播报已由 onExpire 回调输出
		case confirm.KindMiss, confirm.KindNotPending:
			// 未命中选项 → 当作新输入重新走分流
		}
	}
	if hit, rest := a.matcher.Match(text); hit {
		a.say("» 指令已上行：%s", rest)
		a.sink.SendCommand(rest)
		return
	}
	a.say(TalkPrompt)
}

// replay 携带消歧覆写后的参数重放工具调用（02 §7），返回新话术。
func (a *App) replay(res confirm.Result) string {
	tool, ok := a.reg.Get(res.Tool)
	if !ok {
		a.logger.Warnf("confirm: 重放未知工具 %q", res.Tool)
		return "操作没完成，再说一遍试试"
	}
	return tool.Handler(res.Args)
}

// say 行式输出话术（并发安全：输入循环与 onExpire 定时器两个写者）。
func (a *App) say(format string, args ...any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, _ = fmt.Fprintf(a.out, format+"\n", args...)
}

// mcpHandler 适配 Dispatcher.Handle 与 linx onMCP 回调签名。
func mcpHandler(disp *mcp.Dispatcher) func(payload []byte) []byte {
	return func(payload []byte) []byte { return disp.Handle(context.Background(), payload) }
}
