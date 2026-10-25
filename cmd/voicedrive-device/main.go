package main

import (
	"bufio"
	"context"
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

// config 是命令行配置（02 §3/§8）。
type config struct {
	MAC      string
	Alias    string
	WakeWord string
	Server   string
	LinxURL  string
	Inject   string
}

// parseFlags 解析参数；仅 --mac 必填。
func parseFlags(args []string) (config, error) {
	fs := flag.NewFlagSet("voicedrive-device", flag.ContinueOnError)
	mac := fs.String("mac", "", "设备 MAC 地址（必填，唯一身份标识）")
	alias := fs.String("alias", "", "设备别名（展示用）")
	wakeWord := fs.String("wake-word", "小牛", "提醒词（端上变量，命中才进指令通道）")
	server := fs.String("server", "", "voicedrive server 地址")
	linxURL := fs.String("lingx-url", "ws://xrobo-io.qiniuapi.com/v1/ws/", "灵犀 WS 端点")
	inject := fs.String("inject", "", "注入音频文件（等同一次开口，V0 占位打点）")
	if err := fs.Parse(args); err != nil {
		return config{}, fmt.Errorf("解析参数: %w", err)
	}
	if *mac == "" {
		return config{}, fmt.Errorf("参数校验: --mac 必填")
	}
	return config{
		MAC:      *mac,
		Alias:    *alias,
		WakeWord: *wakeWord,
		Server:   *server,
		LinxURL:  *linxURL,
		Inject:   *inject,
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
	client := linx.NewClient(linx.Config{URL: cfg.LinxURL}, buf, mcpHandler(disp), logger)
	app = NewApp(cfg.WakeWord, cf, reg, &LinxCommandSink{Client: client}, out, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		if err := client.Run(ctx); err != nil {
			logger.Errorf("linx 退出: %v", err)
		}
	}()
	go statusLoop(ctx, client, out)

	fmt.Fprintf(out, "voicedrive-device mac=%s alias=%s wake-word=%s server=%s\n", cfg.MAC, cfg.Alias, cfg.WakeWord, cfg.Server)
	if cfg.Inject != "" {
		// 02 §3：注入音频等同一次开口；待 03 对讲/采集通道落地后接真实文件读取。
		logger.Infof("音频注入占位：%s（等同一次开口）", cfg.Inject)
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
