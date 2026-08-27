package validator

import (
	"bufio"
	"bytes"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"caption-release-workbench/internal/domain"
)

func (e *Engine) Parse(source string, revisionID string) ([]domain.Cue, error) {
	if len(source) == 0 {
		return nil, &SyntaxError{Line: 1, Column: 1, Message: "内容为空"}
	}
	if len(source) > maxSourceBytes {
		return nil, &SyntaxError{Line: 1, Column: 1, Message: "内容超过 2 MiB 上限"}
	}
	source = strings.ReplaceAll(source, "\r\n", "\n")
	source = strings.ReplaceAll(source, "\r", "\n")
	lines := strings.Split(source, "\n")
	if strings.TrimSpace(strings.TrimPrefix(lines[0], "\ufeff")) != "WEBVTT" {
		return nil, &SyntaxError{Line: 1, Column: 1, Message: "必须以 WEBVTT 开头"}
	}
	var cues []domain.Cue
	for index := 1; index < len(lines); {
		if strings.TrimSpace(lines[index]) == "" {
			index++
			continue
		}
		lineNumber := index + 1
		timing := strings.TrimSpace(lines[index])
		if !strings.Contains(timing, "-->") {
			index++
			if index >= len(lines) {
				return nil, &SyntaxError{Line: lineNumber, Column: 1, Message: "字幕标识后缺少时间码"}
			}
			timing = strings.TrimSpace(lines[index])
			lineNumber = index + 1
		}
		start, end, err := parseTiming(timing)
		if err != nil {
			return nil, &SyntaxError{Line: lineNumber, Column: 1, Message: err.Error()}
		}
		index++
		var textLines []string
		for index < len(lines) && strings.TrimSpace(lines[index]) != "" {
			if strings.Contains(lines[index], "-->") {
				return nil, &SyntaxError{Line: index + 1, Column: 1, Message: "字幕段之间必须有空行"}
			}
			textLines = append(textLines, strings.TrimSpace(lines[index]))
			index++
		}
		if len(textLines) == 0 {
			return nil, &SyntaxError{Line: lineNumber + 1, Column: 1, Message: "字幕文本不能为空"}
		}
		if len(cues) >= maxCueCount {
			return nil, &SyntaxError{Line: lineNumber, Column: 1, Message: "字幕段超过 10000 条上限"}
		}
		cue, err := normalizeCue(revisionID, len(cues)+1, start, end, textLines)
		if err != nil {
			return nil, &SyntaxError{Line: lineNumber + 1, Column: 1, Message: err.Error()}
		}
		cues = append(cues, cue)
	}
	if len(cues) == 0 {
		return nil, &SyntaxError{Line: 2, Column: 1, Message: "未找到字幕段"}
	}
	return cues, nil
}

func parseTiming(value string) (int64, int64, error) {
	matches := timingPattern.FindStringSubmatch(value)
	if matches == nil {
		return 0, 0, fmt.Errorf("时间码格式应为 HH:MM:SS.mmm --> HH:MM:SS.mmm")
	}
	parts := make([]int64, 8)
	for i := 1; i < len(matches); i++ {
		parsed, _ := strconv.ParseInt(matches[i], 10, 64)
		parts[i-1] = parsed
	}
	if parts[1] > 59 || parts[2] > 59 || parts[5] > 59 || parts[6] > 59 {
		return 0, 0, fmt.Errorf("分钟和秒必须小于 60")
	}
	start := ((parts[0]*60+parts[1])*60+parts[2])*1000 + parts[3]
	end := ((parts[4]*60+parts[5])*60+parts[6])*1000 + parts[7]
	return start, end, nil
}

func normalizeCue(revisionID string, ordinal int, start, end int64, lines []string) (domain.Cue, error) {
	raw := strings.Join(lines, "\n")
	if !utf8.ValidString(raw) {
		return domain.Cue{}, fmt.Errorf("字幕不是有效 UTF-8")
	}
	if strings.ContainsRune(raw, '\x00') {
		return domain.Cue{}, fmt.Errorf("字幕包含非法空字符")
	}
	speaker := ""
	if match := voicePattern.FindStringSubmatch(raw); match != nil {
		speaker = strings.TrimSpace(html.UnescapeString(match[1]))
		raw = match[2]
	}
	styles := collectStyles(raw)
	plain := strings.TrimSpace(anyTagPattern.ReplaceAllString(raw, ""))
	plain = html.UnescapeString(plain)
	var spoken, sound []string
	for _, line := range strings.Split(plain, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if soundPattern.MatchString(line) {
			sound = append(sound, line)
		} else if line != "" {
			spoken = append(spoken, line)
		}
	}
	text := strings.Join(spoken, "\n")
	soundText := strings.Join(sound, " ")
	id := stableID("cue", fmt.Sprintf("%s:%d:%d:%d:%s", revisionID, ordinal, start, end, plain))
	return domain.Cue{ID: id, RevisionID: revisionID, Ordinal: ordinal, StartMS: start, EndMS: end, Speaker: speaker, Text: text, SoundDescription: soundText, StyleTags: styles}, nil
}

func collectStyles(value string) []string {
	seen := map[string]bool{}
	for _, match := range tagPattern.FindAllStringSubmatch(value, -1) {
		style := strings.TrimPrefix(match[1], "c.")
		if style == "c" {
			continue
		}
		seen[style] = true
	}
	styles := make([]string, 0, len(seen))
	for style := range seen {
		styles = append(styles, style)
	}
	sort.Strings(styles)
	return styles
}

func ReadVTT(reader *bufio.Reader, limit int64) (string, error) {
	var buffer bytes.Buffer
	if _, err := buffer.ReadFrom(reader); err != nil {
		return "", err
	}
	if int64(buffer.Len()) > limit {
		return "", fmt.Errorf("输入超过 %d 字节", limit)
	}
	return buffer.String(), nil
}
