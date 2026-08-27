package validator

import (
	"bytes"
	"fmt"
	"strings"

	"caption-release-workbench/internal/domain"
)

func RenderVTT(cues []domain.Cue) string {
	var buffer bytes.Buffer
	buffer.WriteString("WEBVTT\n\n")
	for _, cue := range cues {
		fmt.Fprintf(&buffer, "%d\n%s --> %s\n", cue.Ordinal, formatTime(cue.StartMS), formatTime(cue.EndMS))
		prefix := ""
		suffix := ""
		if cue.Speaker != "" {
			prefix += "<v " + escapeMarkup(cue.Speaker) + ">"
		}
		for _, style := range cue.StyleTags {
			switch style {
			case "b", "i", "u":
				prefix += "<" + style + ">"
				suffix = "</" + style + ">" + suffix
			default:
				prefix += "<c." + escapeClass(style) + ">"
				suffix = "</c>" + suffix
			}
		}
		content := cue.Text
		if cue.SoundDescription != "" {
			if content != "" {
				content += "\n"
			}
			content += cue.SoundDescription
		}
		buffer.WriteString(prefix + escapeText(content) + suffix + "\n\n")
	}
	return buffer.String()
}

func NormalizedDigest(cues []domain.Cue) string { return SourceDigest(RenderVTT(cues)) }

func formatTime(milliseconds int64) string {
	if milliseconds < 0 {
		milliseconds = 0
	}
	hours := milliseconds / 3600000
	milliseconds %= 3600000
	minutes := milliseconds / 60000
	milliseconds %= 60000
	seconds := milliseconds / 1000
	millis := milliseconds % 1000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", hours, minutes, seconds, millis)
}

func escapeText(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	return value
}

func escapeMarkup(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "", ">", "").Replace(value)
}

func escapeClass(value string) string {
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "default"
	}
	return b.String()
}
