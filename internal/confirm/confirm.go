// Package confirm 实现确认语境状态机（product-v2 §4.3 / 02-linx §7）：
// 工具返回 clarify 或打扰性操作需对方确认时进入 Pending，10s 内裸答匹配
// 选项则携带消歧结果重放工具调用，未命中当作新输入重新走分流，超时作废播报。
package confirm

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// DefaultTTL 是 Pending 的默认存活窗口（product-v2 §4.3：10s）。
const DefaultTTL = 10 * time.Second

// maxOptions 是 clarify 选项数上限（product-v2 §4.3：≤3）。
const maxOptions = 3

// Pending 是一次待确认的追问。
type Pending struct {
	Question string         // 反问话术，≤40 字，直接送 TTS
	Options  []string       // 候选项，1~3 个
	Tool     string         // 命中后待重放的工具名
	Args     map[string]any // 触发 clarify 的原始工具参数
	ArgKey   string         // 命中后用选项覆写的参数键（如 group.switch 的 name）；空则不覆写
	TTL      time.Duration  // 存活窗口；≤0 取 DefaultTTL
}

// Expired 是超时作废事件的快照，供"先不管了，需要再说"播报使用。
type Expired struct {
	Question string
	Tool     string
}

// Kind 是 Answer 的结果类别。
type Kind int

const (
	KindNotPending Kind = iota // 无待确认语境（输入按正常分流处理）
	KindHit                    // 裸答命中选项，携带重放信息
	KindMiss                   // 未命中选项：语境作废，输入当作新指令重新走分流
	KindExpired                // 回答时已超 10s 窗口，语境已作废
)

// Result 是一次裸答的判定结果；仅 KindHit 时 Tool/Args/Answer 有意义。
type Result struct {
	Kind   Kind
	Tool   string         // 重放的工具名
	Args   map[string]any // 重放参数：原始 Args 的拷贝，ArgKey 非空时覆写为命中选项
	Answer string         // 命中的选项原文（canonical，供日志/播报）
}

// Machine 是确认语境状态机。并发安全：全部状态变更经同一把互斥锁串行，
// 超时定时器与输入处理先到先得；过期回调在锁外执行、恰好触发一次
// （只有赢得"清理"临界区的 goroutine 才回调）。
type Machine struct {
	mu       sync.Mutex
	now      func() time.Time
	onExpire func(Expired)
	pending  *pendingState
	timer    *time.Timer
}

type pendingState struct {
	question string
	options  []string
	tool     string
	args     map[string]any
	argKey   string
	deadline time.Time
}

// New 构造状态机；now 为时钟注入（测试可推进），onExpire 可为 nil。
func New(now func() time.Time, onExpire func(Expired)) *Machine {
	return &Machine{now: now, onExpire: onExpire}
}

// Ask 进入 Pending；非法追问（缺问题/工具、选项数不在 1~3）返回错误且状态不变。
// 已有 Pending 时新追问直接取代旧语境（旧定时器停止，不触发旧语境的 onExpire）。
func (m *Machine) Ask(p Pending) error {
	if strings.TrimSpace(p.Question) == "" {
		return fmt.Errorf("confirm: empty question")
	}
	if p.Tool == "" {
		return fmt.Errorf("confirm: empty tool")
	}
	if len(p.Options) < 1 || len(p.Options) > maxOptions {
		return fmt.Errorf("confirm: options must be 1~%d, got %d", maxOptions, len(p.Options))
	}
	ttl := p.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	deadline := m.now().Add(ttl)

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.timer != nil {
		m.timer.Stop()
	}
	m.pending = &pendingState{
		question: p.Question,
		options:  append([]string(nil), p.Options...),
		tool:     p.Tool,
		args:     p.Args,
		argKey:   p.ArgKey,
		deadline: deadline,
	}
	// 定时器只是超时播报的主动触发器；是否过期一律以 now() 对 deadline 判定，
	// 与 Answer/Active 的惰性检查同一条规则，定时器与输入先到先得。
	m.timer = time.AfterFunc(time.Until(deadline), m.maybeExpire)
	return nil
}

// Active 报告是否仍处于 Pending（顺带惰性清理已过期的语境）。
func (m *Machine) Active() bool {
	m.maybeExpire()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pending != nil
}

// Answer 判定一条裸答输入：
//   - 命中选项（原文忽略大小写与首尾空白，或"第一个/1/一"类序数）→ KindHit + 重放信息；
//   - 未命中 → KindMiss，语境作废，调用方须把该输入当作新指令重新走分流；
//   - 回答时已超窗 → KindExpired；无语境 → KindNotPending。
func (m *Machine) Answer(text string) Result {
	m.mu.Lock()
	p := m.pending
	if p == nil {
		m.mu.Unlock()
		return Result{Kind: KindNotPending}
	}
	if !m.now().Before(p.deadline) {
		m.clearLocked()
		m.mu.Unlock()
		m.notifyExpired(Expired{Question: p.question, Tool: p.tool})
		return Result{Kind: KindExpired}
	}
	idx, ok := matchOption(p.options, text)
	if !ok {
		// 未命中：当作新输入重新走分流，语境即时作废（02-linx §7）。
		m.clearLocked()
		m.mu.Unlock()
		return Result{Kind: KindMiss}
	}
	m.clearLocked()
	m.mu.Unlock()

	args := make(map[string]any, len(p.args)+1)
	for k, v := range p.args {
		args[k] = v
	}
	if p.argKey != "" {
		args[p.argKey] = p.options[idx]
	}
	return Result{Kind: KindHit, Tool: p.tool, Args: args, Answer: p.options[idx]}
}

// maybeExpire 供定时器与 Active 惰性触发：到期则作废语境并回调 onExpire。
func (m *Machine) maybeExpire() {
	m.mu.Lock()
	p := m.pending
	if p == nil || m.now().Before(p.deadline) {
		m.mu.Unlock()
		return
	}
	m.clearLocked()
	m.mu.Unlock()
	m.notifyExpired(Expired{Question: p.question, Tool: p.tool})
}

// notifyExpired 在锁外回调 onExpire（可能为 nil）。
func (m *Machine) notifyExpired(e Expired) {
	if m.onExpire != nil {
		m.onExpire(e)
	}
}

// clearLocked 置空语境并停表（须持锁调用）。
func (m *Machine) clearLocked() {
	m.pending = nil
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
}

// matchOption 先按选项原文匹配（TrimSpace + EqualFold），再按序数
// （"1/2/3"、"一/二/三"、"第一个/第二个/第三个"）映射选项下标。
func matchOption(options []string, text string) (int, bool) {
	t := strings.TrimSpace(text)
	if t == "" {
		return 0, false
	}
	for i, opt := range options {
		if strings.EqualFold(t, opt) {
			return i, true
		}
	}
	ordinals := map[string]int{
		"1": 0, "2": 1, "3": 2,
		"一": 0, "二": 1, "三": 2,
		"第一个": 0, "第二个": 1, "第三个": 2,
	}
	if idx, ok := ordinals[t]; ok && idx < len(options) {
		return idx, true
	}
	return 0, false
}
