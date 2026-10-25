package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Study-of-slag-weng/voicedrive1/internal/confirm"
	"github.com/Study-of-slag-weng/voicedrive1/internal/device"
	"github.com/Study-of-slag-weng/voicedrive1/internal/groupclient"
	"github.com/Study-of-slag-weng/voicedrive1/internal/linx"
	"github.com/Study-of-slag-weng/voicedrive1/internal/logx"
	"github.com/Study-of-slag-weng/voicedrive1/internal/mcp"
	"github.com/Study-of-slag-weng/voicedrive1/internal/model"
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
	micDur := fs.Duration("mic-duration", 0, "--mic 采集时长（如 5s、30s）；0=持续采集，说完由平台 listen stop 收口，Ctrl-C 退出")
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
	utterances := device.NewUtteranceBuffer()
	messagePlayer := &device.AFPlayer{}
	var messageQueue *device.PlaybackQueue
	var control *device.Client
	if strings.TrimSpace(cfg.Server) != "" {
		controlURL, err := controlEndpoint(cfg.Server)
		if err != nil {
			return err
		}
		control, err = device.NewClient(controlURL, cfg.MAC)
		if err != nil {
			return fmt.Errorf("创建控制面客户端: %w", err)
		}
		messageQueue, err = device.NewPlaybackQueue(messagePlayer, control.Ack)
		if err != nil {
			return fmt.Errorf("创建群消息播放队列: %w", err)
		}
		control.OnMessage = func(_ context.Context, message model.DeviceMessage) error {
			messageQueue.Enqueue(message)
			return nil
		}
		control.OnGroupChanged = func(_ context.Context, group model.Group) error {
			messageQueue.SetActiveGroup(group.ID)
			return nil
		}
		control.OnBindMessage = func(ctx context.Context, messageID, transcript string) error {
			uploader := &device.AudioUploader{Endpoint: cfg.Server, MAC: cfg.MAC}
			return utterances.BindAndUpload(ctx, uploader, messageID, transcript)
		}
		defer messageQueue.Close()
	}
	// player 消费 TTS 下行帧经 ffplay 播放；缺 ffplay 时降级为纯文本输出。
	// 装配顺序：player 先于 client 创建，OnStateChange 回调闭包引用它时已定型
	// （回调运行于 client 读循环 goroutine，赋值发生在 goroutine 启动之前）。
	var player *linx.Player
	if ffplay, err := exec.LookPath("ffplay"); err == nil {
		player, err = linx.NewPlayer(buf, func(ctx context.Context) (linx.Sink, error) {
			return newFFPlaySink(ctx, ffplay, os.Stderr)
		}, 600*time.Millisecond)
		if err != nil {
			return fmt.Errorf("创建播放泵: %w", err)
		}
		logger.Infof("TTS 播放后端: %s", ffplay)
	} else {
		logger.Warnf("未找到 ffplay（ffmpeg 套件），TTS 下行不播放，仅输出文本")
	}
	// uplinkCtx 承载注入/麦克风上行：平台 listen stop（ASR 判定说完）即取消，
	// 采集与帧化随之停住；父 ctx（Ctrl-C）取消同样传播。
	uplinkCtx, uplinkCancel := context.WithCancel(ctx)
	client := linx.NewClient(linx.Config{
		URL:          linxURL,
		Token:        token,
		DeviceID:     cfg.MAC,
		ClientID:     clientID,
		UserAgent:    userAgent,
		ListenMode:   listenMode(cfg.Inject, cfg.Mic),
		OnListenStop: uplinkCancel,
		OnStateChange: func(s linx.SessionState) {
			if messageQueue != nil {
				messageQueue.SetTTSSpeaking(s == linx.StateSpeaking)
				if s == linx.StateSpeaking {
					_ = messageQueue.Interrupt()
				}
			}
			// 抢话打断：用户开口（listening）立即停掉在播的上一句回答。
			if s == linx.StateListening && player != nil {
				player.Stop()
				if messageQueue != nil {
					_ = messageQueue.Interrupt()
				}
			}
		},
	}, buf, mcpHandler(disp), logger)
	app = NewApp(cfg.WakeWord, cf, reg, &LinxCommandSink{Client: client}, out, logger)

	go func() {
		if err := client.Run(ctx); err != nil {
			logger.Errorf("linx 退出: %v", err)
		}
	}()
	if control != nil {
		go func() {
			if err := control.Run(ctx); err != nil {
				logger.Errorf("控制面退出: %v", err)
			}
		}()
	}
	if player != nil {
		go func() {
			// 播放泵 ctx 取消时返回 nil；其余为后端故障（如 ffplay 崩溃）。
			if err := player.Run(ctx); err != nil {
				logger.Errorf("播放泵退出: %v", err)
			}
		}()
	}
	go statusLoop(ctx, client, out)

	fmt.Fprintf(out, "voicedrive-device mac=%s alias=%s wake-word=%s server=%s linx=%s\n", cfg.MAC, cfg.Alias, cfg.WakeWord, cfg.Server, linxURL)
	if cfg.Inject != "" || cfg.Mic != "" {
		// 注入/麦克风等同一次开口（02 §3）：会话就绪后上行，失败只报错不影响指令循环。
		go func() {
			if err := waitSession(uplinkCtx, client, 5*time.Second, logger); err != nil {
				logger.Warnf("音频上行: %v", err)
				return
			}
			var err error
			if cfg.Inject != "" {
				err = uplinkWAVWithBuffer(uplinkCtx, client, cfg.Inject, utterances)
			} else {
				err = uplinkMic(uplinkCtx, client, cfg.Mic, cfg.MicDuration, os.Stderr, logger, utterances)
			}
			if err != nil {
				logger.Errorf("音频上行: %v", err)
				return
			}
			logger.Infof("音频上行结束")
		}()
	}

	if err := commandLoop(ctx, os.Stdin, app.HandleLine, out); err != nil {
		return fmt.Errorf("命令循环: %w", err)
	}
	stop()
	return nil
}

func controlEndpoint(serverURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(serverURL), "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("服务端地址无效: %q", serverURL)
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("服务端地址协议不支持: %q", parsed.Scheme)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v1/device"
	parsed.RawQuery = ""
	return parsed.String(), nil
}

// commandLoop 逐行读取输入交给 handle 处理；stdin EOF 后不退出进程，
// 阻塞等待 ctx 结束——后台/nohup 启动时输入流关闭，设备长驻进程不得随之死亡。
func commandLoop(ctx context.Context, r io.Reader, handle func(string), out io.Writer) error {
	sc := bufio.NewScanner(r)
	fmt.Fprint(out, "> ")
	for sc.Scan() {
		handle(sc.Text())
		fmt.Fprint(out, "> ")
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("读 stdin: %w", err)
	}
	<-ctx.Done()
	return nil
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
