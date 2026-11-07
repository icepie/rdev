package server

import (
	"strings"
	"unicode/utf8"
)

// agentJobOutput retains the final output bytes without rebuilding a 32KiB
// string for every streamed token. The owning jobs mutex guards all access.
// UTF-8 trimming happens on snapshots so split input runes can finish arriving.
type agentJobOutput struct {
	data      []byte
	start     int
	truncated bool
}

func (output *agentJobOutput) append(text string) {
	if len(text) >= agentJobOutputLimit {
		output.truncated = output.truncated || len(output.data) > 0 || len(text) > agentJobOutputLimit
		output.data = append(output.data[:0], text[len(text)-agentJobOutputLimit:]...)
		output.start = 0
		return
	}
	if room := agentJobOutputLimit - len(output.data); room > 0 {
		take := min(room, len(text))
		output.data = append(output.data, text[:take]...)
		text = text[take:]
	}
	if len(text) == 0 {
		return
	}
	output.truncated = true
	n := copy(output.data[output.start:], text)
	copy(output.data, text[n:])
	output.start = (output.start + len(text)) % agentJobOutputLimit
}

func (output *agentJobOutput) String() string {
	var result string
	if output.start == 0 {
		result = string(output.data)
	} else {
		var builder strings.Builder
		builder.Grow(len(output.data))
		builder.Write(output.data[output.start:])
		builder.Write(output.data[:output.start])
		result = builder.String()
	}
	if output.truncated {
		for len(result) > 0 && !utf8.RuneStart(result[0]) {
			result = result[1:]
		}
	}
	return result
}
