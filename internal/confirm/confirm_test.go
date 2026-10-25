package confirm

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeClock 可推进的时钟注入实现。
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Now()} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestAsk_Validation(t *testing.T) {
	tests := []struct {
		name    string
		pending Pending
		wantErr bool
	}{
		{"合法", Pending{Question: "哪个群？", Options: []string{"夜班组", "打灰组"}, Tool: "group.switch"}, false},
		{"TTL 缺省合法", Pending{Question: "q", Options: []string{"要", "不要"}, Tool: "group.join", TTL: time.Hour}, false},
		{"问题为空", Pending{Options: []string{"要"}, Tool: "group.join"}, true},
		{"工具为空", Pending{Question: "q", Options: []string{"要"}}, true},
		{"选项为空", Pending{Question: "q", Tool: "group.join"}, true},
		{"选项超 3 个", Pending{Question: "q", Options: []string{"a", "b", "c", "d"}, Tool: "group.join"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New(newFakeClock().Now, nil)
			err := m.Ask(tt.pending)
			if tt.wantErr {
				require.Error(t, err)
				require.False(t, m.Active())
			} else {
				require.NoError(t, err)
				require.True(t, m.Active())
			}
		})
	}
}

func TestAnswer(t *testing.T) {
	askSwitch := func() *Machine {
		m := New(newFakeClock().Now, nil)
		err := m.Ask(Pending{
			Question: "你说的是夜班组还是打灰组？",
			Options:  []string{"夜班组", "打灰组"},
			Tool:     "group.switch",
			Args:     map[string]any{"name": "夜"},
			ArgKey:   "name",
		})
		require.NoError(t, err)
		return m
	}
	tests := []struct {
		name    string
		machine func() *Machine
		input   string
		want    Result
	}{
		{"无 Pending", func() *Machine { return New(newFakeClock().Now, nil) }, "要", Result{Kind: KindNotPending}},
		{"精确命中", askSwitch, "打灰组", Result{Kind: KindHit, Tool: "group.switch", Args: map[string]any{"name": "打灰组"}, Answer: "打灰组"}},
		{"命中去空白", askSwitch, "  打灰组 ", Result{Kind: KindHit, Tool: "group.switch", Args: map[string]any{"name": "打灰组"}, Answer: "打灰组"}},
		{"序数命中第二个", askSwitch, "第二个", Result{Kind: KindHit, Tool: "group.switch", Args: map[string]any{"name": "打灰组"}, Answer: "打灰组"}},
		{"序数命中一", askSwitch, "一", Result{Kind: KindHit, Tool: "group.switch", Args: map[string]any{"name": "夜班组"}, Answer: "夜班组"}},
		{"未命中当新输入", askSwitch, "建个群", Result{Kind: KindMiss}},
		{"序数越界未命中", askSwitch, "第三个", Result{Kind: KindMiss}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.machine().Answer(tt.input)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestAnswer_MissClearsPending(t *testing.T) {
	m := New(newFakeClock().Now, nil)
	require.NoError(t, m.Ask(Pending{Question: "q", Options: []string{"要", "不要"}, Tool: "group.join"}))
	require.Equal(t, KindMiss, m.Answer("建个群").Kind)
	require.False(t, m.Active())
	require.Equal(t, KindNotPending, m.Answer("要").Kind)
}

func TestAnswer_HitWithoutArgKey(t *testing.T) {
	m := New(newFakeClock().Now, nil)
	require.NoError(t, m.Ask(Pending{
		Question: "q", Options: []string{"a", "b"}, Tool: "t",
		Args: map[string]any{"k": "v"},
	}))
	got := m.Answer("b")
	require.Equal(t, KindHit, got.Kind)
	require.Equal(t, map[string]any{"k": "v"}, got.Args)
	require.Equal(t, "b", got.Answer)
}

func TestAnswer_DoesNotMutateOriginalArgs(t *testing.T) {
	m := New(newFakeClock().Now, nil)
	orig := map[string]any{"name": "夜"}
	require.NoError(t, m.Ask(Pending{Question: "q", Options: []string{"夜班组", "打灰组"}, Tool: "group.switch", Args: orig, ArgKey: "name"}))
	got := m.Answer("打灰组")
	require.Equal(t, map[string]any{"name": "打灰组"}, got.Args)
	require.Equal(t, map[string]any{"name": "夜"}, orig)
}

func TestAnswer_ExpiredByClock(t *testing.T) {
	clock := newFakeClock()
	var expired []Expired
	var mu sync.Mutex
	m := New(clock.Now, func(e Expired) { mu.Lock(); expired = append(expired, e); mu.Unlock() })
	require.NoError(t, m.Ask(Pending{Question: "q", Options: []string{"要"}, Tool: "group.join"}))
	clock.Advance(DefaultTTL + time.Second)
	got := m.Answer("要")
	require.Equal(t, KindExpired, got.Kind)
	require.False(t, m.Active())
	mu.Lock()
	require.Len(t, expired, 1)
	require.Equal(t, Expired{Question: "q", Tool: "group.join"}, expired[0])
	mu.Unlock()
	// 已作废后再答不重复触发
	require.Equal(t, KindNotPending, m.Answer("要").Kind)
	mu.Lock()
	require.Len(t, expired, 1)
	mu.Unlock()
}

func TestActive_LazyExpire(t *testing.T) {
	clock := newFakeClock()
	var expired []Expired
	var mu sync.Mutex
	m := New(clock.Now, func(e Expired) { mu.Lock(); expired = append(expired, e); mu.Unlock() })
	require.NoError(t, m.Ask(Pending{Question: "q", Options: []string{"要"}, Tool: "group.join"}))
	require.True(t, m.Active())
	clock.Advance(DefaultTTL)
	require.False(t, m.Active())
	mu.Lock()
	require.Len(t, expired, 1)
	mu.Unlock()
}

func TestTimer_FiresOnExpire(t *testing.T) {
	var mu sync.Mutex
	var expired []Expired
	m := New(time.Now, func(e Expired) { mu.Lock(); expired = append(expired, e); mu.Unlock() })
	require.NoError(t, m.Ask(Pending{Question: "q", Options: []string{"要"}, Tool: "group.join", TTL: 10 * time.Millisecond}))
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(expired) == 1
	}, time.Second, time.Millisecond)
	require.False(t, m.Active())
}

func TestAsk_ReplacesPending(t *testing.T) {
	m := New(newFakeClock().Now, nil)
	require.NoError(t, m.Ask(Pending{Question: "q1", Options: []string{"a"}, Tool: "t1"}))
	require.NoError(t, m.Ask(Pending{Question: "q2", Options: []string{"b"}, Tool: "t2"}))
	got := m.Answer("b")
	require.Equal(t, KindHit, got.Kind)
	require.Equal(t, "t2", got.Tool)
}

func TestMachine_Concurrent(t *testing.T) {
	m := New(time.Now, nil)
	require.NoError(t, m.Ask(Pending{Question: "q", Options: []string{"要", "不要"}, Tool: "group.join"}))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				m.Active()
				m.Answer("要")
			}
		}()
	}
	wg.Wait()
	require.False(t, m.Active())
}
