package linx

import "fmt"

// SessionState 是指令会话状态（02-linx §4.3）。
type SessionState string

const (
	StateIdle      SessionState = "idle"      // 无会话
	StateListening SessionState = "listening" // 持续上行音频（唤醒命中后）
	StateThinking  SessionState = "thinking"  // ASR 结束，LLM/工具执行中
	StateSpeaking  SessionState = "speaking"  // TTS 播放中
)

// Event 是驱动状态迁移的会话事件（ASR/TTS 生命周期 + 本地唤醒）。
type Event int

const (
	EventWake     Event = iota // 本地唤醒命中（listening 期重复命中保持）
	EventASRStop               // 平台 listen stop / VAD 静默
	EventTTSStart              // 平台 tts start
	EventTTSStop               // 平台 tts stop
)

// transitions 是合法迁移表（表驱动，02-linx §4.3 状态图）：
// idle→listening→thinking→speaking→idle 主环；
// speaking 期间可抢话回 listening（采集不中断，是否打断 TTS 由平台决定）。
var transitions = map[SessionState]map[Event]SessionState{
	StateIdle: {EventWake: StateListening},
	StateListening: {
		EventASRStop: StateThinking,
		EventWake:    StateListening,
	},
	StateThinking: {EventTTSStart: StateSpeaking},
	StateSpeaking: {
		EventTTSStop: StateIdle,
		EventWake:    StateListening,
	},
}

// SessionMachine 是会话状态机；事件仅来自 WS 读循环与本地唤醒，
// 单 goroutine 串行驱动即无竞态；若跨 goroutine 使用须外部同步。
type SessionMachine struct {
	state SessionState
}

// NewSessionMachine 构造 idle 初始状态。
func NewSessionMachine() *SessionMachine { return &SessionMachine{state: StateIdle} }

// State 返回当前状态。
func (m *SessionMachine) State() SessionState { return m.state }

// Transition 按事件迁移；非法迁移返回错误且状态不变（不 panic，记日志归调用方）。
func (m *SessionMachine) Transition(ev Event) error {
	nexts, ok := transitions[m.state]
	if !ok {
		return fmt.Errorf("linx: unknown state %q", m.state)
	}
	next, ok := nexts[ev]
	if !ok {
		return fmt.Errorf("linx: illegal transition %d in state %q", ev, m.state)
	}
	m.state = next
	return nil
}
