package validator

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
)

const (
	maxSourceBytes = 2 << 20
	maxCueCount    = 10000
	minGapMS       = 80
	maxCharsLine   = 28
	maxCharsSecond = 18.0
)

var (
	timingPattern = regexp.MustCompile(`^([0-9]{2,}):([0-9]{2}):([0-9]{2})[.,]([0-9]{3})\s+-->\s+([0-9]{2,}):([0-9]{2}):([0-9]{2})[.,]([0-9]{3})(?:\s+.*)?$`)
	voicePattern  = regexp.MustCompile(`^<v(?:\.[^ >]+)*\s+([^>]+)>(.*)$`)
	tagPattern    = regexp.MustCompile(`</?(b|i|u|c(?:\.[a-zA-Z0-9_-]+)?)>`)
	anyTagPattern = regexp.MustCompile(`<[^>]+>`)
	soundPattern  = regexp.MustCompile(`^[\[【（(].+[\]】）)]$`)
)

type SyntaxError struct {
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Message string `json:"message"`
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("WebVTT 第 %d 行第 %d 列：%s", e.Line, e.Column, e.Message)
}

type Engine struct{}

func New() *Engine { return &Engine{} }

func SourceDigest(source string) string {
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}

func stableID(prefix, value string) string {
	sum := sha256.Sum256([]byte(value))
	return prefix + "_" + hex.EncodeToString(sum[:10])
}
