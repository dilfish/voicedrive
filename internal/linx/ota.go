package linx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OTAActivation 是设备未绑定时平台返回的激活码信息。
type OTAActivation struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// OTAWebSocket 是设备已绑定时平台返回的 WS 接入信息。
type OTAWebSocket struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// OTAResult 是一次 OTA 上报的结果：未绑定时 Activation 非 nil，
// 已绑定时 WebSocket.Token 非空（URL 可为空，空则用默认端点）。
type OTAResult struct {
	Activation *OTAActivation `json:"activation"`
	WebSocket  *OTAWebSocket  `json:"websocket"`
}

// Bound 报告设备是否已完成绑定（拿到了 WS token）。
func (r *OTAResult) Bound() bool {
	return r != nil && r.WebSocket != nil && r.WebSocket.Token != ""
}

// otaRequest 是 OTA 上报的请求体，必填字段对齐 linx-emu（灵犀设备接口）。
type otaRequest struct {
	Version     int      `json:"version"`
	UUID        string   `json:"uuid"`
	Application otaApp   `json:"application"`
	Board       otaBoard `json:"board"`
	MACAddress  string   `json:"mac_address"`
	ChipModel   string   `json:"chip_model_name"`
}

type otaApp struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	CompileTime string `json:"compile_time"`
	ELFSHA256   string `json:"elf_sha256"`
}

type otaBoard struct {
	Type string `json:"type"`
	MAC  string `json:"mac"`
}

// RequestOTA 向 OTA 端点上报设备信息（对齐 linx-emu 连接流程）：
// 未绑定设备换取 6 位激活码（控制台「添加设备」输入），已绑定设备换取
// WS 握手所需的 Bearer token（及可选的端点覆盖）。
func RequestOTA(ctx context.Context, url, mac, clientID, ua string) (*OTAResult, error) {
	body := otaRequest{
		Version: 0,
		UUID:    clientID,
		Application: otaApp{
			Name:        "voicedrive-device",
			Version:     "0.1.0",
			CompileTime: time.Now().Format("2006-01-02 15:04:05"),
			ELFSHA256:   fmt.Sprintf("%064d", 0),
		},
		Board:      otaBoard{Type: "voicedrive-device", MAC: mac},
		MACAddress: mac,
		ChipModel:  "esp32s3",
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("编码 OTA 请求: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("构造 OTA 请求: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Activation-Version", "1")
	req.Header.Set("Device-Id", mac)
	req.Header.Set("Client-Id", clientID)
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept-Language", "zh-CN")

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("OTA 请求: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("读取 OTA 响应: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OTA HTTP %d: %s", resp.StatusCode, data)
	}

	var bizErr struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(data, &bizErr); err != nil {
		return nil, fmt.Errorf("解析 OTA 响应: %w", err)
	}
	if bizErr.Error != "" {
		return nil, fmt.Errorf("OTA 业务错误: %s", bizErr.Error)
	}
	var out OTAResult
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("解析 OTA 响应: %w", err)
	}
	return &out, nil
}
