package server

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const loopUsage = "Usage: /loop [count|duration] [--while|--until '<command>'] <prompt>"

var loopDurationPart = regexp.MustCompile(`^(\d+)([a-z]+)`)

func loopWord(input string) (string, string, error) {
	input = strings.TrimSpace(input)
	var b strings.Builder
	var quote rune
	escaped := false
	for i, r := range input {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if unicode.IsSpace(r) {
			return b.String(), strings.TrimSpace(input[i:]), nil
		}
		b.WriteRune(r)
	}
	if quote != 0 || escaped {
		return "", "", fmt.Errorf("unterminated shell word")
	}
	return b.String(), "", nil
}

func loopTimeUnit(unit string) int64 {
	switch strings.ToLower(unit) {
	case "s", "sec", "secs", "second", "seconds":
		return 1000
	case "m", "min", "mins", "minute", "minutes":
		return 60000
	case "h", "hr", "hrs", "hour", "hours":
		return 3600000
	}
	return 0
}

func parseAgentLoop(text string) (*agentLoop, error) {
	l := &agentLoop{Status: "active"}
	rest := strings.TrimSpace(text)
	first, after, err := loopWord(rest)
	if err != nil {
		return nil, err
	}
	if first != "" && (first[0] >= '0' && first[0] <= '9' || first[0] == '+' || first[0] == '-') {
		if first[0] == '+' || first[0] == '-' {
			return nil, fmt.Errorf("%s", loopUsage)
		}
		n, countErr := strconv.ParseInt(first, 10, 64)
		if countErr == nil {
			if n <= 0 {
				return nil, fmt.Errorf("loop count must be positive")
			}
			unit, remainder, _ := loopWord(after)
			if ms := loopTimeUnit(unit); ms != 0 {
				if n > int64((1<<63-1)/time.Millisecond)/ms {
					return nil, fmt.Errorf("loop duration is too large")
				}
				l.DurationMs = n * ms
				rest = remainder
			} else {
				l.MaxIterations = n
				rest = after
			}
		} else {
			token := strings.ToLower(first)
			for token != "" {
				part := loopDurationPart.FindStringSubmatch(token)
				if part == nil {
					return nil, fmt.Errorf("%s", loopUsage)
				}
				amount, e := strconv.ParseInt(part[1], 10, 64)
				unit := loopTimeUnit(part[2])
				if e != nil || amount <= 0 || unit == 0 || amount > (int64((1<<63-1)/time.Millisecond)-l.DurationMs)/unit {
					return nil, fmt.Errorf("invalid loop duration")
				}
				l.DurationMs += amount * unit
				token = token[len(part[0]):]
			}
			rest = after
		}
	}
	for strings.HasPrefix(rest, "--") {
		end := strings.IndexFunc(rest, func(r rune) bool { return unicode.IsSpace(r) || r == '=' })
		flag, value := rest, ""
		if end >= 0 {
			flag, value = rest[:end], strings.TrimSpace(rest[end:])
			value = strings.TrimPrefix(value, "=")
		}
		if flag != "--while" && flag != "--until" {
			return nil, fmt.Errorf("unknown loop flag %s", flag)
		}
		if l.Condition != "" {
			return nil, fmt.Errorf("use only one of --while or --until")
		}
		command, remainder, e := loopWord(value)
		if e != nil {
			return nil, e
		}
		if strings.TrimSpace(command) == "" || strings.HasPrefix(command, "-") {
			return nil, fmt.Errorf("%s requires a shell command", flag)
		}
		l.Condition, l.Until, rest = command, flag == "--until", remainder
	}
	l.Prompt = strings.TrimSpace(rest)
	if l.DurationMs > 0 {
		l.deadline = time.Now().Add(time.Duration(l.DurationMs) * time.Millisecond)
	}
	return l, nil
}

func loopConditionContinues(code int, until bool) (bool, error) {
	if code != 0 && code != 1 {
		return false, fmt.Errorf("loop condition exited with code %d (expected 0 or 1)", code)
	}
	return (code == 0) != until, nil
}
