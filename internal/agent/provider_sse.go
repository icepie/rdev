package agent

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Bound the assembled event as well as individual lines: a provider can split
// one event across arbitrarily many small data lines without terminating it.
const maxSSEEventBytes = 16 << 20

// readSSEEvents handles framing only. process reports protocol completion;
// ordinary EOF returns false so each adapter can enforce its terminal event.
// A last event without a blank terminator is accepted for gateway compatibility.
func readSSEEvents(body io.Reader, poke func(), process func(string) (bool, error)) (bool, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), maxSSEEventBytes+1)
	var data strings.Builder
	hasData := false
	flush := func() (bool, error) {
		if !hasData {
			return false, nil
		}
		payload := data.String()
		data.Reset()
		hasData = false
		return process(payload)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if done, err := flush(); done || err != nil {
				return done, err
			}
			continue
		}
		// Comments (: ping) and metadata also prove the connection is alive.
		// They do not constitute model progress or suppress pre-output retries.
		if poke != nil {
			poke()
		}
		field, payload, _ := strings.Cut(line, ":")
		if field != "data" {
			continue
		}
		// SSE removes exactly one optional space after the colon, not all
		// leading/trailing whitespace from the field value.
		payload = strings.TrimPrefix(payload, " ")
		size := data.Len() + len(payload)
		if hasData {
			size++ // newline joining consecutive data fields
		}
		if size > maxSSEEventBytes {
			return false, fmt.Errorf("agent: SSE event exceeds %d bytes", maxSSEEventBytes)
		}
		if hasData {
			data.WriteByte('\n')
		}
		data.WriteString(payload)
		hasData = true
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("agent: read SSE stream: %w", err)
	}
	return flush()
}
