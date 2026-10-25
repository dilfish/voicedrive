package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Study-of-slag-weng/voicedrive1/internal/confirm"
	"github.com/Study-of-slag-weng/voicedrive1/internal/groupclient"
	"github.com/Study-of-slag-weng/voicedrive1/internal/linx"
	"github.com/Study-of-slag-weng/voicedrive1/internal/logx"
	"github.com/Study-of-slag-weng/voicedrive1/internal/mcp"
)

// userAgent 是 OTA 与 WS 握手共用的 User-Agent（对齐 linx-emu 惯例）。
const userAgent = "voicedrive-device/0.1.0"

// config 是命令行配置（02 §3/§8）。
type config struct {
	MAC         string
	Alias       string
	WakeWord    string
	Server      string
	LinxURL     string
	OTAURL      string
	Token       string
	Inject      string
	Mic         string
	MicDuration time.Duration
}

// parseFlags 解析参数；仅 --mac 必填，--inject 与 --mic 互斥。
func parseFlags(args []string) (config, error) {
	fs := flag.NewFlagSet("voicedrive-device", flag.ContinueOnError)
	mac := fs.String("mac", "", "设备 MAC 地址（必填，唯一身份标识）")
	alias := fs.String("alias", "", "设备别名（展示用）")
	wakeWord := fs.String("wake-word", "小牛", "提醒词（端上变量，命中才进指令通道）")
	server := fs.String("server", "", "voicedrive server 地址")
	linxURL := fs.String("lingx-url", "ws://xrobo-io.qiniuapi.com/v1/ws/", "灵犀 WS 端点")
	otaURL := fs.String("ota", "https://xrobo.qiniuapi.com/v1/ota/", "灵犀 OTA 上报端点（换激活码/token）")
	token := fs.String("token", os.Getenv("LINX_TOKEN"), "WS token，留空自动走 OTA 引导")
	inject := fs.String("inject", "", "注入 WAV 音频文件（16k/mono/s16，等同一次开口）")
	mic := fs.String("mic", "", "用电脑麦克风采集上行（ffmpeg avfoundation 设备序号，如 0），需装 ffmpeg")
	micDur := fs.Duration("mic-duration", 0, "--mic 采集时长；0=持续采集，说完由平台 listen stop 收口，Ctrl-C 退出")
	if err := fs.Parse(args); err != nil {
		return config{}, fmt.Errorf("解析参数: %w", err)
	}
	if *mac == "" {
		return config{}, fmt.Errorf("参数校验: --mac 必填")
	}
	if *inject != "" && *mic != "" {
		return config{}, fmt.Errorf("参数校验: --inject 与 --mic 互斥，二选一")
	}
	return config{
		MAC:         *mac,
		Alias:       *alias,
		WakeWord:    *wakeWord,
		Server:      *server,
		LinxURL:     *linxURL,
		OTAURL:      *otaURL,
		Token:       *token,
		Inject:      *inject,
		Mic:         *mic,
		MicDuration: *micDur,
	}, nil
}

func main() {
	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run 装配全部组件（构造函数注入，宪法 3.2）并阻塞至退出。
func run(cfg config) error {
	logger := logx.NewStderr(slog.LevelInfo)
	out := os.Stdout

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	clientID := newUUID()
	token, linxURL := cfg.Token, cfg.LinxURL
	if token == "" {
		// 未显式给 token：走 OTA 引导（对齐 linx-emu），绑定完成前不建 WS。
		var wsOverride string
		var err error
		token, wsOverride, err = bootstrapToken(ctx, cfg.OTAURL, cfg.MAC, clientID, userAgent, 5*time.Second, out, logger)
		if err != nil {
			return fmt.Errorf("OTA 引导: %w", err)
		}
		if wsOverride != "" {
			linxURL = wsOverride
		}
	}

	gc := groupclient.New(cfg.Server, cfg.MAC, 3*time.Second, logger)

	var app *App
	cf := confirm.New(time.Now, func(confirm.Expired) {
		app.say(ExpiredPhrase)
	})

	reg := mcp.NewRegistry()
	if err := registerGroupTools(reg, gc, cf, nil, logger); err != nil {
		return fmt.Errorf("注册工具: %w", err)
	}
	disp := mcp.NewDispatcherWithRegistry(reg)

	buf := linx.NewAudioBuffer()
	// uplinkCtx 承载注入/麦克风上行：平台 listen stop（ASR 判定说完）即取消，
	// 采集与帧化随之停住；父 ctx（Ctrl-C）取消同样传播。
	uplinkCtx, uplinkCancel := context.WithCancel(ctx)
	client := linx.NewClient(linx.Config{
		URL:          linxURL,
		Token:        token,
		DeviceID:     cfg.MAC,
		ClientID:     clientID,
		UserAgent:    userAgent,
		OnListenStop: uplinkCancel,
	}, buf, mcpHandler(disp), logger)
	app = NewApp(cfg.WakeWord, cf, reg, &LinxCommandSink{Client: client}, out, logger)

	go func() {
		if err := client.Run(ctx); err != nil {
			logger.Errorf("linx 退出: %v", err)
		}
	}()
	go statusLoop(ctx, client, out)

	fmt.Fprintf(out, "voicedrive-device mac=%s alias=%s wake-word=%s server=%s linx=%s\n", cfg.MAC, cfg.Alias, cfg.WakeWord, cfg.Server, linxURL)
	if cfg.Inject != "" || cfg.Mic != "" {
		// 注入/麦克风等同一次开口（02 §3）：会话就绪后上行，失败只报错不影响指令循环。
		go func() {
			if err := waitSession(uplinkCtx, client); err != nil {
				logger.Warnf("音频上行: %v", err)
				return
			}
			var err error
			if cfg.Inject != "" {
				err = uplinkWAV(uplinkCtx, client, cfg.Inject)
			} else {
				err = uplinkMic(uplinkCtx, client, cfg.Mic, cfg.MicDuration, os.Stderr, logger)
			}
			if err != nil {
				logger.Errorf("音频上行: %v", err)
				return
			}
			logger.Infof("音频上行结束")
		}()
	}

	sc := bufio.NewScanner(os.Stdin)
	fmt.Fprint(out, "> ")
	for sc.Scan() {
		app.HandleLine(sc.Text())
		fmt.Fprint(out, "> ")
	}
	stop()
	return sc.Err()
}

// bootstrapToken 走 OTA 引导（对齐 linx-emu 连接流程）：未绑定设备把激活码
// 打印到 out（相同码只打一次），按 poll 周期重试直至控制台完成绑定，返回
// WS token 与可选的端点覆盖（空串表示沿用默认 WS 地址）。OTA 瞬时错误仅告警
// 重试；ctx 取消时返回错误。
func bootstrapToken(ctx context.Context, otaURL, mac, clientID, ua string, poll time.Duration, out io.Writer, logger logx.Logger) (token, wsURL string, err error) {
	printed := ""
	for {
		res, otaErr := linx.RequestOTA(ctx, otaURL, mac, clientID, ua)
		switch {
		case otaErr != nil:
			logger.Warnf("OTA 上报失败，%v 后重试: %v", poll, otaErr)
		case res.Bound():
			return res.WebSocket.Token, res.WebSocket.URL, nil
		case res.Activation != nil:
			if res.Activation.Code != printed {
				printed = res.Activation.Code
				fmt.Fprintf(out, "未绑定，激活码: %s\n%s\n去灵犀控制台「添加设备」输入，每 %v 轮询绑定状态…\n",
					res.Activation.Code, res.Activation.Message, poll)
			}
		default:
			logger.Warnf("OTA 响应既无激活码也无 token，%v 后重试", poll)
		}
		select {
		case <-ctx.Done():
			return "", "", fmt.Errorf("OTA 引导被取消: %w", ctx.Err())
		case <-time.After(poll):
		}
	}
}

// newUUID 生成 v4 UUID（crypto/rand），作为握手 Client-Id 与 OTA uuid。
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("熵源不可用: " + err.Error()) // crypto/rand 失败即进程级异常，无法继续
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// statusLoop 轮询会话状态并在变化时行式输出（终端 UI V0 简版，02 §8）。
func statusLoop(ctx context.Context, c *linx.Client, out io.Writer) {
	last := linx.SessionState("")
	tk := time.NewTicker(200 * time.Millisecond)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			if s := c.State(); s != last {
				last = s
				fmt.Fprintf(out, "[会话] %s\n", s)
			}
		}
	}
}
