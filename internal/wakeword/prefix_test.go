package wakeword

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrefixMatcher_Match(t *testing.T) {
	tests := []struct {
		name  string
		word  string
		input string
		hit   bool
		rest  string
	}{
		{"空格分隔", "小牛", "小牛 建个群", true, "建个群"},
		{"全角逗号分隔", "小牛", "小牛，建个群", true, "建个群"},
		{"无分隔紧贴", "小牛", "小牛建个群", true, "建个群"},
		{"输入前导空格", "小牛", "   小牛 建个群", true, "建个群"},
		{"仅提醒词", "小牛", "小牛", true, ""},
		{"仅提醒词带尾分隔", "小牛", "小牛，", true, ""},
		{"半句话边界（已知误命中，V0 接受）", "小牛", "小牛顿是科学家", true, "顿是科学家"},
		{"提醒词不在开头", "小牛", "建个群小牛", false, ""},
		{"空输入", "小牛", "", false, ""},
		{"纯空白输入", "小牛", "   ", false, ""},
		{"英文小写", "moox", "moox hello", true, "hello"},
		{"英文大写输入", "moox", "MOOX hello", true, "hello"},
		{"英文紧贴命令", "moox", "mooxhello", true, "hello"},
		{"英文唤醒词大小写混合定义", "MooX", "moox建个群", true, "建个群"},
		{"全角空格分隔", "小牛", "小牛　建个群", true, "建个群"},
		{"唤醒词为空串", "", "小牛 建个群", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewPrefix(tt.word)
			hit, rest := m.Match(tt.input)
			require.Equal(t, tt.hit, hit)
			require.Equal(t, tt.rest, rest)
		})
	}
}
