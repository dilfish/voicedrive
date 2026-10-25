package device

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// HTTPMessageSender submits text messages to one selected group.
type HTTPMessageSender struct {
	Endpoint string
	GroupID  string
	MAC      string
	Client   *http.Client
}

func (s *HTTPMessageSender) Send(ctx context.Context, text string) error {
	if s.Endpoint == "" || s.GroupID == "" || s.MAC == "" || strings.TrimSpace(text) == "" {
		return errors.New("message endpoint, group ID, MAC, and text are required")
	}
	body, err := json.Marshal(struct {
		MAC  string `json:"mac"`
		Text string `json:"text"`
	}{MAC: s.MAC, Text: text})
	if err != nil {
		return fmt.Errorf("encode message: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.Endpoint, "/")+"/v1/groups/"+s.GroupID+"/messages", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create message request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("send message returned %s", response.Status)
	}
	return nil
}
