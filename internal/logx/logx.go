// Package logx 提供项目统一的日志薄接口。
//
// 规范强制使用 qiniu.com/cdnbase/xlog.v1，但该模块为私有仓库、公共代理不可拉取，
// 故以本接口隔离：业务代码只依赖 logx.Logger，当前实现为 log/slog，
// 待内网凭证可用后新增 xlog 适配器即可整体切换，业务代码零改动（宪法第四条：接口由消费者定义）。
package logx

import (
	"context"
	"fmt"
	"log/slog"
	"os"
)

// Logger 是设备进程各组件依赖的最小日志接口。
type Logger interface {
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
}

// NewSlog 返回基于 log/slog 的 Logger 实现。
func NewSlog(h slog.Handler) Logger {
	l := slog.New(h)
	return &slogLogger{l: l}
}

// NewStderr 返回输出到 stderr、level 及以上的默认实现。
func NewStderr(level slog.Level) Logger {
	return NewSlog(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

// NewNop 返回丢弃全部输出的实现，供测试使用。
func NewNop() Logger { return nopLogger{} }

type slogLogger struct{ l *slog.Logger }

func (s *slogLogger) Debugf(format string, args ...any) {
	s.l.Log(context.Background(), slog.LevelDebug, fmt.Sprintf(format, args...))
}
func (s *slogLogger) Infof(format string, args ...any) {
	s.l.Log(context.Background(), slog.LevelInfo, fmt.Sprintf(format, args...))
}
func (s *slogLogger) Warnf(format string, args ...any) {
	s.l.Log(context.Background(), slog.LevelWarn, fmt.Sprintf(format, args...))
}
func (s *slogLogger) Errorf(format string, args ...any) {
	s.l.Log(context.Background(), slog.LevelError, fmt.Sprintf(format, args...))
}

type nopLogger struct{}

func (nopLogger) Debugf(string, ...any) {}
func (nopLogger) Infof(string, ...any)  {}
func (nopLogger) Warnf(string, ...any)  {}
func (nopLogger) Errorf(string, ...any) {}
