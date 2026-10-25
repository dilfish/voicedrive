package groupclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Study-of-slag-weng/voicedrive1/internal/logx"
)

// recorded 捕获 fake voicedrive 收到的单次请求。
type recorded struct {
	Method string
	Path   string
	Header http.Header
	Body   map[string]any
}

// fakeServer 记录全部请求并按脚本回放统一响应体。
type fakeServer struct {
	mu       sync.Mutex
	reqs     []recorded
	respBody string
	status   int
	delay    time.Duration
}

func newFakeServer(t *testing.T, respBody string) (*fakeServer, *httptest.Server) {
	t.Helper()
	f := &fakeServer{respBody: respBody, status: http.StatusOK}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		if f.delay > 0 {
			time.Sleep(f.delay)
		}
		rec := recorded{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone()}
		if r.Body != nil {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
				rec.Body = body
			}
		}
		f.reqs = append(f.reqs, rec)
		respBody, status := f.respBody, f.status
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeServer) last(t *testing.T) recorded {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.NotEmpty(t, f.reqs)
	return f.reqs[len(f.reqs)-1]
}

func newClient(base string) *Client {
	return New(base, "AA:BB:CC:DD:EE:FF", 3*time.Second, logx.NewNop())
}

func okBody() string { return `{"status":"ok","message":"群建好了，口令 12345"}` }

func TestClient_HeadersAndEndpoints(t *testing.T) {
	tests := []struct {
		name   string
		call   func(c *Client) (Result, error)
		method string
		path   string
		body   map[string]any
	}{
		{"create", func(c *Client) (Result, error) { return c.Create("夜班组", "12345") }, http.MethodPost, "/v1/group/create",
			map[string]any{"name": "夜班组", "passcode": "12345"}},
		{"create 不带口令", func(c *Client) (Result, error) { return c.Create("夜班组", "") }, http.MethodPost, "/v1/group/create",
			map[string]any{"name": "夜班组"}},
		{"join", func(c *Client) (Result, error) { return c.Join("12345") }, http.MethodPost, "/v1/group/join",
			map[string]any{"passcode": "12345"}},
		{"switch", func(c *Client) (Result, error) { return c.Switch("夜班组") }, http.MethodPost, "/v1/group/switch",
			map[string]any{"name": "夜班组"}},
		{"mute on", func(c *Client) (Result, error) { return c.Mute("夜班组", true) }, http.MethodPost, "/v1/group/mute",
			map[string]any{"name": "夜班组", "on": true}},
		{"mute off", func(c *Client) (Result, error) { return c.Mute("夜班组", false) }, http.MethodPost, "/v1/group/mute",
			map[string]any{"name": "夜班组", "on": false}},
		{"list", func(c *Client) (Result, error) { return c.List() }, http.MethodGet, "/v1/group/list", nil},
		{"leave", func(c *Client) (Result, error) { return c.Leave("夜班组") }, http.MethodPost, "/v1/group/leave",
			map[string]any{"name": "夜班组"}},
		{"rename", func(c *Client) (Result, error) { return c.Rename("夜班组", "打灰组") }, http.MethodPost, "/v1/group/rename",
			map[string]any{"name": "夜班组", "new_name": "打灰组"}},
		{"pat", func(c *Client) (Result, error) { return c.Pat("徐工") }, http.MethodPost, "/v1/group/pat",
			map[string]any{"alias": "徐工"}},
		{"invite", func(c *Client) (Result, error) { return c.Invite("徐工", "夜班组") }, http.MethodPost, "/v1/group/invite",
			map[string]any{"alias": "徐工", "group_name": "夜班组"}},
		{"query_member", func(c *Client) (Result, error) { return c.QueryMember("夜班组", "徐工") }, http.MethodPost, "/v1/group/query_member",
			map[string]any{"group_name": "夜班组", "alias": "徐工"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, srv := newFakeServer(t, okBody())
			c := newClient(srv.URL)
			res, err := tt.call(c)
			require.NoError(t, err)
			require.Equal(t, Result{Status: "ok", Message: "群建好了，口令 12345"}, res)

			rec := f.last(t)
			require.Equal(t, tt.method, rec.Method)
			require.Equal(t, tt.path, rec.Path)
			if tt.body != nil {
				require.Equal(t, tt.body, rec.Body)
				// 身份只认 Header：请求体绝不含 mac 类身份键
				require.NotContains(t, rec.Body, "mac")
			}
			require.Equal(t, "AA:BB:CC:DD:EE:FF", rec.Header.Get("X-VD-MAC"))
			require.NotEmpty(t, rec.Header.Get("X-VD-Fingerprint"))
		})
	}
}

func TestClient_FingerprintDeterministic(t *testing.T) {
	f, srv := newFakeServer(t, okBody())
	c := newClient(srv.URL)
	_, err := c.Switch("夜班组")
	require.NoError(t, err)
	_, err = c.Switch("夜班组")
	require.NoError(t, err)
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Len(t, f.reqs, 2)
	require.Equal(t, f.reqs[0].Header.Get("X-VD-Fingerprint"), f.reqs[1].Header.Get("X-VD-Fingerprint"))
}

func TestClient_ResponseStates(t *testing.T) {
	tests := []struct {
		name string
		body string
		want Result
	}{
		{"ok", `{"status":"ok","message":"已加入夜班组，共 3 人"}`, Result{Status: "ok", Message: "已加入夜班组，共 3 人"}},
		{"clarify 带选项", `{"status":"clarify","message":"你说的是夜班组还是打灰组？","options":["夜班组","打灰组"]}`,
			Result{Status: "clarify", Message: "你说的是夜班组还是打灰组？", Options: []string{"夜班组", "打灰组"}}},
		{"error", `{"status":"error","message":"口令不对，再说说看"}`, Result{Status: "error", Message: "口令不对，再说说看"}},
		{"无 options 字段", `{"status":"clarify","message":"哪个？"}`, Result{Status: "clarify", Message: "哪个？"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, srv := newFakeServer(t, tt.body)
			c := newClient(srv.URL)
			res, err := c.Join("12345")
			require.NoError(t, err)
			require.Equal(t, tt.want, res)
		})
	}
}

func TestClient_Timeout(t *testing.T) {
	f, srv := newFakeServer(t, okBody())
	f.mu.Lock()
	f.delay = 200 * time.Millisecond
	f.mu.Unlock()
	c := New(srv.URL, "AA:BB:CC:DD:EE:FF", 50*time.Millisecond, logx.NewNop())
	_, err := c.Switch("夜班组")
	require.Error(t, err)
	require.Equal(t, "网络不太好，再说一遍试试", NetworkErrorPhrase)
}

func TestClient_HTTPError(t *testing.T) {
	f, srv := newFakeServer(t, `{"status":"error","message":"x"}`)
	f.mu.Lock()
	f.status = http.StatusInternalServerError
	f.mu.Unlock()
	c := newClient(srv.URL)
	_, err := c.Switch("夜班组")
	require.Error(t, err)
}

func TestClient_MalformedBody(t *testing.T) {
	_, srv := newFakeServer(t, `not-json`)
	c := newClient(srv.URL)
	_, err := c.List()
	require.Error(t, err)
}
