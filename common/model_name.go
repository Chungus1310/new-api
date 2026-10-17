package common

import "strings"

// ModelNameFoldKey folds the punctuation that model names differ in without
// changing identity: "claude-opus-4.6", "claude-opus-4-6" and "claude_opus_4_6"
// all key the same. Letters and digits are kept as is, so two genuinely
// different models can never collide.
func ModelNameFoldKey(modelName string) string {
	var b strings.Builder
	b.Grow(len(modelName))
	for _, r := range strings.ToLower(modelName) {
		switch r {
		case '.', '-', '_', ' ':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
