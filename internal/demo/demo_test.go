package demo

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestConfigRequiresExactlyThreeDistinctDevices(t *testing.T) {
	c := DefaultConfig()
	c.Devices = c.Devices[:2]
	if err := c.Validate(); err == nil {
		t.Fatal("accepted fewer than three devices")
	}
	c = DefaultConfig()
	c.Devices[1].MAC = c.Devices[0].MAC
	if err := c.Validate(); err == nil {
		t.Fatal("accepted duplicate MAC")
	}
}

func TestConfigRejectsUnknownFieldsAndBadPorts(t *testing.T) {
	c := DefaultConfig()
	c.Service.Port = 0
	if err := c.Validate(); err == nil {
		t.Fatal("accepted bad port")
	}
	if _, err := LoadConfig([]byte(`{"bogus":true}`)); err == nil {
		t.Fatal("accepted unknown field")
	}
}

func TestPreflightBlocksMissingV2MediaWithoutStartingAnything(t *testing.T) {
	c := DefaultConfig()
	for i := range c.Devices {
		c.Devices[i].Port = freePort(t)
	}
	c.Service.Port = freePort(t)
	c.Mock.Port = freePort(t)
	c.Service.Path = os.Args[0]
	c.Mock.Path = os.Args[0]
	for i := range c.Devices {
		c.Devices[i].Path = os.Args[0]
	}
	r := Preflight(context.Background(), c)
	if r.Overall != StatusBlocked {
		t.Fatalf("overall=%s", r.Overall)
	}
	if r.Started {
		t.Fatal("preflight started a process")
	}
	if !strings.Contains(r.Item("v2_media").Detail, "missing") {
		t.Fatal(r.Item("v2_media"))
	}
}

func reservePort(port int) (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:"+fmt.Sprint(port))
}

func TestPreflightDetectsMissingCommandAndOccupiedPort(t *testing.T) {
	c := DefaultConfig()
	c.Mock.Port = freePort(t)
	c.Service.Path = filepath.Join(t.TempDir(), "does-not-exist")
	c.Mock.Path = os.Args[0]
	for i := range c.Devices {
		c.Devices[i].Path = os.Args[0]
		c.Devices[i].Port = freePort(t)
	}
	c.Media.V2Audio = filepath.Join(t.TempDir(), "voice.wav")
	l, err := reservePort(c.Mock.Port)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	r := Preflight(context.Background(), c)
	if r.Item("service_command").Status != StatusBlocked {
		t.Fatal(r.Item("service_command"))
	}
	if r.Item("mock_port").Status != StatusFailed {
		t.Fatal(r.Item("mock_port"))
	}
}

func TestSupervisorSuccessFailureTimeoutAndCancelCleanUp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell syntax differs")
	}
	for _, tc := range []struct {
		name, script string
		want         Status
	}{
		{"success", "exit 0", StatusPassed}, {"failure", "exit 3", StatusFailed},
		{"timeout", "sleep 5", StatusBlocked}, {"cancel", "sleep 5", StatusBlocked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cancel := func() {}
			if tc.name == "timeout" {
				ctx, cancel = context.WithTimeout(ctx, 30*time.Millisecond)
			}
			if tc.name == "cancel" {
				var c context.CancelFunc
				ctx, c = context.WithCancel(ctx)
				cancel = c
				go func() { time.Sleep(20 * time.Millisecond); c() }()
			}
			result := Run(ctx, os.Args[0], []string{"-test.run=TestHelperProcess", "--", tc.script}, nil)
			cancel()
			if result.Status != tc.want {
				t.Fatalf("status=%s detail=%s", result.Status, result.Detail)
			}
			if result.PID != 0 {
				t.Fatal("process was not cleaned up")
			}
		})
	}
}

func TestHelperProcess(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "--") {
		return
	}
	for i, a := range os.Args {
		if a == "--" && i+1 < len(os.Args) {
			if strings.HasPrefix(os.Args[i+1], "sleep") {
				time.Sleep(5 * time.Second)
			}
			if strings.HasPrefix(os.Args[i+1], "exit") {
				if strings.TrimSpace(os.Args[i+1]) == "exit 0" {
					os.Exit(0)
				}
				os.Exit(3)
			}
			return
		}
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return p
}
