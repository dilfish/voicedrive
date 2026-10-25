// Package demo contains the deliberately conservative V2 demo preflight.
// It does not start Docker, connect to a database, or access the network.
package demo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Status string

const (
	StatusPassed  Status = "passed"
	StatusFailed  Status = "failed"
	StatusBlocked Status = "blocked"
	StatusNotRun  Status = "not_run"
)

type Command struct {
	Path string `json:"path"`
	Port int    `json:"port"`
}
type Device struct {
	MAC  string `json:"mac"`
	Path string `json:"path"`
	Port int    `json:"port"`
}
type Config struct {
	Service Command  `json:"service"`
	Mock    Command  `json:"mock"`
	Devices []Device `json:"devices"`
	Media   struct {
		V2Audio string `json:"v2_audio"`
	} `json:"media"`
}

func DefaultConfig() Config {
	return Config{Service: Command{Path: "./bin/server", Port: 8080}, Mock: Command{Path: "./bin/linx-mock", Port: 18080}, Devices: []Device{{MAC: "AA:BB:CC:DD:EE:01", Path: "./bin/voicedrive-command", Port: 18101}, {MAC: "AA:BB:CC:DD:EE:02", Path: "./bin/voicedrive-command", Port: 18102}, {MAC: "AA:BB:CC:DD:EE:03", Path: "./bin/voicedrive-command", Port: 18103}}}
}
func (c Config) Validate() error {
	if len(c.Devices) != 3 {
		return fmt.Errorf("devices must contain exactly three entries")
	}
	seen := map[string]bool{}
	for i, d := range c.Devices {
		if !validMAC(d.MAC) || seen[strings.ToUpper(d.MAC)] {
			return fmt.Errorf("devices[%d].mac must be distinct valid MAC", i)
		}
		seen[strings.ToUpper(d.MAC)] = true
		if d.Port < 1 || d.Port > 65535 {
			return fmt.Errorf("devices[%d].port out of range", i)
		}
		if strings.TrimSpace(d.Path) == "" {
			return fmt.Errorf("devices[%d].path is required", i)
		}
	}
	for name, x := range map[string]Command{"service": c.Service, "mock": c.Mock} {
		if strings.TrimSpace(x.Path) == "" {
			return fmt.Errorf("%s.path is required", name)
		}
		if x.Port < 1 || x.Port > 65535 {
			return fmt.Errorf("%s.port out of range", name)
		}
	}
	return nil
}
func validMAC(s string) bool {
	var b [6]byte
	_, err := fmt.Sscanf(strings.ToUpper(s), "%02X:%02X:%02X:%02X:%02X:%02X", &b[0], &b[1], &b[2], &b[3], &b[4], &b[5])
	return err == nil && len(s) == 17
}

type ItemResult struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
}
type Report struct {
	Overall     Status       `json:"overall"`
	Started     bool         `json:"started"`
	Items       []ItemResult `json:"items"`
	GeneratedAt time.Time    `json:"generated_at"`
}

func (r Report) Item(name string) ItemResult {
	for _, x := range r.Items {
		if x.Name == name {
			return x
		}
	}
	return ItemResult{Name: name, Status: StatusNotRun}
}
func Preflight(ctx context.Context, c Config) Report {
	r := Report{Overall: StatusPassed, GeneratedAt: time.Now().UTC()}
	add := func(n string, s Status, d string) {
		r.Items = append(r.Items, ItemResult{n, s, d})
		if s == StatusFailed {
			r.Overall = StatusFailed
		}
		if s == StatusBlocked && r.Overall == StatusPassed {
			r.Overall = StatusBlocked
		}
	}
	if err := c.Validate(); err != nil {
		add("config", StatusFailed, err.Error())
		add("v2_media", StatusNotRun, "not checked because config is invalid")
		r.Overall = StatusFailed
		return r
	}
	add("config", StatusPassed, "strict configuration accepted")
	if info, err := os.Stat(c.Media.V2Audio); err != nil || !info.Mode().IsRegular() {
		add("v2_media", StatusBlocked, fmt.Sprintf("missing V2 original media: %v", err))
	} else {
		add("v2_media", StatusPassed, "audio fixture exists; transport and mixing not verified")
	}
	commands := map[string]string{"service_command": c.Service.Path, "mock_command": c.Mock.Path}
	for i, d := range c.Devices {
		commands[fmt.Sprintf("device_%d_command", i+1)] = d.Path
	}
	for n, p := range commands {
		if _, err := exec.LookPath(p); err != nil {
			add(n, StatusBlocked, "environment dependency missing: executable not built or not found")
			continue
		}
		add(n, StatusPassed, "executable found")
	}
	for n, p := range map[string]int{"service_port": c.Service.Port, "mock_port": c.Mock.Port, "device_1_port": c.Devices[0].Port, "device_2_port": c.Devices[1].Port, "device_3_port": c.Devices[2].Port} {
		if err := portFree(p); err != nil {
			add(n, StatusFailed, err.Error())
		} else {
			add(n, StatusPassed, "available")
		}
		if err := ctx.Err(); err != nil {
			add("context", StatusFailed, "preflight cancelled")
			return r
		}
	}
	add("v2_runtime", StatusBlocked, "original media transport, mixing and full runtime assembly are not implemented")
	add("v2_start", StatusNotRun, "not run: V2 demo is intentionally not executable until original media and runtime wiring exist")
	return r
}
func portFree(port int) error {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("port %d unavailable: %w", port, err)
	}
	if err := l.Close(); err != nil {
		return fmt.Errorf("close preflight listener: %w", err)
	}
	return nil
}

type RunResult struct {
	Status Status
	Detail string
	PID    int
}

func Run(ctx context.Context, path string, args []string, env []string) RunResult {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	if env == nil {
		cmd.Env = os.Environ()
	}
	if err := cmd.Start(); err != nil {
		return RunResult{Status: StatusFailed, Detail: err.Error()}
	}
	err := cmd.Wait()
	if ctx.Err() != nil {
		return RunResult{Status: StatusBlocked, Detail: ctx.Err().Error()}
	}
	if err != nil {
		var e *exec.ExitError
		if errors.As(err, &e) {
			return RunResult{Status: StatusFailed, Detail: err.Error()}
		}
		return RunResult{Status: StatusFailed, Detail: err.Error()}
	}
	return RunResult{Status: StatusPassed, Detail: "completed"}
}

func LoadConfig(data []byte) (Config, error) {
	var c Config
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("decode demo config: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return c, fmt.Errorf("expected one configuration document")
	}
	if err := c.Validate(); err != nil {
		return c, fmt.Errorf("validate demo config: %w", err)
	}
	return c, nil
}
