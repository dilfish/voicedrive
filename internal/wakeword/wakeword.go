// Package wakeword 提醒词检测：通道分流的本地判定（02-linx §1/§5）。
// 提醒词是端上变量（--wake-word，默认"小牛"），检测不成立音频绝不上行灵犀。
package wakeword

import "strings"

// Detector 是麦克风场景的流式唤醒检测接口，喂入 20ms PCM 块；
// KWS 实现（如 sherpa-onnx）后置接入（02-linx §5），V0 用文本前缀匹配与注入文件代替。
type Detector interface {
	Feed(pcm []int16) (hit bool)
	Reset()
}

// PrefixMatcher 终端文本输入的提醒词前缀匹配，V0 默认实现：
// 以提醒词开头才命中（半句话边界）；命中后返回去掉提醒词与前导分隔符的剩余文本。
type PrefixMatcher struct {
	word string
}

// NewPrefix 按提醒词构造匹配器；word 为空串时永不命中。
func NewPrefix(word string) *PrefixMatcher {
	return &PrefixMatcher{word: word}
}

// Match 判定 text 是否以提醒词开头（大小写不敏感、忽略前导空白）；
// rest 为去掉提醒词及其后空白与通用标点后的命令文本。
func (p *PrefixMatcher) Match(text string) (hit bool, rest string) {
	if p.word == "" {
		return false, ""
	}
	runes := []rune(strings.TrimSpace(text))
	if len(runes) < len([]rune(p.word)) {
		return false, ""
	}
	head := string(runes[:len([]rune(p.word))])
	if !strings.EqualFold(head, p.word) {
		return false, ""
	}
	rest = strings.TrimFunc(string(runes[len([]rune(p.word)):]), isSeparator)
	return true, rest
}

// isSeparator 吃掉提醒词后的空白与中英文通用标点（如"小牛，建个群"）。
func isSeparator(r rune) bool {
	switch r {
	case ' ', '\t', '　', '，', '。', '、', '！', '？', '；', '：', ',', '.', '!', '?', ';', ':':
		return true
	}
	return false
}
