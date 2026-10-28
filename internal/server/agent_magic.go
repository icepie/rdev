package server

// Magic keywords (omp magic-keywords parity): standalone lowercase trigger
// words in the user's prose opt the turn into specialized behavior. They
// fire only in prose — not inside code spans, fenced code blocks, or paths.

import "regexp"

var ultrathinkRe = regexp.MustCompile(`(?i)\bultrathink\b`)

// magicTurnEffort returns the per-turn reasoning-effort override implied by
// magic keywords in text ("" = keep the session's configured level).
// "ultrathink" requests the highest supported thinking effort.
func magicTurnEffort(text string) string {
	if ultrathinkRe.MatchString(stripCodeSpans(text)) {
		return "high"
	}
	return ""
}

// stripCodeSpans removes fenced code blocks (``` … ```) and inline code
// spans (`…`) so trigger words inside code don't fire.
func stripCodeSpans(s string) string {
	var out []byte
	i := 0
	for i < len(s) {
		// Fenced block: ``` up to the closing fence (or EOF).
		if s[i] == '`' {
			run := 1
			for i+run < len(s) && s[i+run] == '`' {
				run++
			}
			fence := s[i : i+run]
			// Find the matching closing run.
			end := -1
			for j := i + run; j+run <= len(s); j++ {
				if s[j] == '`' && s[j:j+run] == fence {
					end = j + run
					break
				}
			}
			if end < 0 {
				// Unclosed fence: drop the rest for fenced blocks (```),
				// keep the backticks for a single `.
				if run >= 3 {
					return string(out)
				}
				out = append(out, s[i:i+run]...)
				i += run
				continue
			}
			i = end
			continue
		}
		out = append(out, s[i])
		i++
	}
	return string(out)
}
