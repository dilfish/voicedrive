package logx

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoggerMethodsUseFormattedMessages(t *testing.T) {
	var buf bytes.Buffer
	logger := NewSlog(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logger.Debugf("debug %d", 1)
	logger.Infof("info %s", "two")
	logger.Warnf("warn %t", true)
	logger.Errorf("error %v", "four")

	output := buf.String()
	for _, want := range []string{"debug 1", "info two", "warn true", "error four"} {
		require.Contains(t, output, want)
	}
}

func TestNewNopDoesNotWrite(t *testing.T) {
	logger := NewNop()
	logger.Debugf("debug")
	logger.Infof("info")
	logger.Warnf("warn")
	logger.Errorf("error")
	require.NotNil(t, logger)
}
