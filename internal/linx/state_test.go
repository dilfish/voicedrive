package linx

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionState_String(t *testing.T) {
	require.Equal(t, "idle", string(StateIdle))
	require.Equal(t, "listening", string(StateListening))
	require.Equal(t, "thinking", string(StateThinking))
	require.Equal(t, "speaking", string(StateSpeaking))
}

func TestSessionStateMachine_LegalTransitions(t *testing.T) {
	tests := []struct {
		name string
		path []Event
		want SessionState
	}{
		{"完整一轮", []Event{EventWake, EventASRStop, EventTTSStart, EventTTSStop}, StateIdle},
		{"抢话：speaking 期间唤醒再次 listening", []Event{EventWake, EventASRStop, EventTTSStart, EventWake, EventASRStop, EventTTSStart, EventTTSStop}, StateIdle},
		{"连续唤醒保持 listening", []Event{EventWake, EventWake, EventASRStop, EventTTSStart, EventTTSStop}, StateIdle},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewSessionMachine()
			require.Equal(t, StateIdle, m.State())
			for _, ev := range tt.path {
				require.NoError(t, m.Transition(ev))
			}
			require.Equal(t, tt.want, m.State())
		})
	}
}

func TestSessionStateMachine_IllegalTransitions(t *testing.T) {
	tests := []struct {
		name string
		path []Event
	}{
		{"idle 直达 speaking", []Event{EventTTSStart}},
		{"idle 直达 thinking", []Event{EventASRStop}},
		{"idle 重复 idle 事件", []Event{EventTTSStop}},
		{"listening 直达 speaking", []Event{EventWake, EventTTSStart}},
		{"thinking 再来 ASRStop", []Event{EventWake, EventASRStop, EventASRStop}},
		{"unknown 事件", []Event{Event(99)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewSessionMachine()
			var err error
			for _, ev := range tt.path {
				err = m.Transition(ev)
				if err != nil {
					break
				}
			}
			require.Error(t, err)
		})
	}
}

func TestSessionStateMachine_IllegalKeepsState(t *testing.T) {
	m := NewSessionMachine()
	require.NoError(t, m.Transition(EventWake))
	require.Error(t, m.Transition(EventTTSStart))
	require.Equal(t, StateListening, m.State())
}
