package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestSSEFramingAndLiveness(t *testing.T) {
	stream := ": ping\r\nevent: message\r\nid: 42\r\nretry: 1000\r\n" +
		"data:  first \r\ndata: second\r\n\r\n: ping\n\ndata\n\ndata: last"
	var events []string
	pokes := 0
	done, err := readSSEEvents(strings.NewReader(stream), func() { pokes++ }, func(data string) (bool, error) {
		events = append(events, data)
		return data == "last", nil
	})
	if err != nil || !done || pokes != 9 || !reflect.DeepEqual(events, []string{" first \nsecond", "", "last"}) {
		t.Fatalf("done=%v err=%v pokes=%d events=%q", done, err, pokes, events)
	}
}

func TestSSEEventLimitAcrossLines(t *testing.T) {
	// Each line fits; only the aggregate exceeds the limit. No event should
	// reach the model adapter before its complete frame has been validated.
	line := "data: " + strings.Repeat("x", 1<<20) + "\n"
	readers := make([]io.Reader, 16)
	for i := range readers {
		readers[i] = strings.NewReader(line)
	}
	called := false
	done, err := readSSEEvents(io.MultiReader(readers...), nil, func(string) (bool, error) {
		called = true
		return true, nil
	})
	if err == nil || !strings.Contains(err.Error(), "SSE event exceeds") || done || called {
		t.Fatalf("done=%v called=%v err=%v", done, called, err)
	}
}

func TestSSETerminalAndReadErrors(t *testing.T) {
	wireErr := errors.New("fixture connection reset")
	for _, tc := range []struct {
		name    string
		input   string
		done    bool
		wantErr error
	}{
		{"terminal stops reading", "data: done\n\n", true, nil},
		{"failed read never flushes incomplete event", "data: partial\n", false, wireErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			done, err := readSSEEvents(io.MultiReader(strings.NewReader(tc.input), sseErrorReader{wireErr}), nil, func(string) (bool, error) {
				calls++
				return true, nil
			})
			if done != tc.done || !errors.Is(err, tc.wantErr) || (calls == 1) != tc.done {
				t.Fatalf("done=%v calls=%d err=%v", done, calls, err)
			}
		})
	}
	processErr := errors.New("fixture invalid provider event")
	_, err := readSSEEvents(strings.NewReader("data: invalid\n\ndata: ignored\n\n"), nil, func(string) (bool, error) {
		return false, processErr
	})
	if !errors.Is(err, processErr) {
		t.Fatalf("lost adapter error: %v", err)
	}
}

type sseErrorReader struct{ err error }

func (r sseErrorReader) Read([]byte) (int, error) { return 0, r.err }

func TestResponsesCommentHeartbeatsAndPrematureEOF(t *testing.T) {
	pokes := 0
	assistant, _, err := readResponsesStream(strings.NewReader(": ping\n\n: ping\r\n\r\n"), "scope", nil, nil, func() { pokes++ })
	if pokes != 2 || err == nil || assistant.Responses != nil || assistant.Content != "" || len(assistant.ToolCalls) != 0 {
		t.Fatalf("heartbeats must keep transport alive without claiming model progress: pokes=%d message=%+v err=%v", pokes, assistant, err)
	}
}

func TestResponsesHeartbeatsKeepRequestAlive(t *testing.T) {
	// Virtual time exercises the actual transport watchdog over more than its
	// timeout, without a slow/flaky wall-clock HTTP test or global timer edits.
	synctest.Test(t, func(t *testing.T) {
		requests := 0
		cfg := testConfig("https://fixture.invalid", nil)
		cfg.API = "openai-responses"
		cfg.HTTPClient = &http.Client{Transport: sseFixtureTransport(func(req *http.Request) (*http.Response, error) {
			requests++
			reader, writer := io.Pipe()
			go func() {
				<-req.Context().Done()
				reader.CloseWithError(req.Context().Err())
			}()
			go func() {
				defer writer.Close()
				for i := 0; i < 5; i++ {
					time.Sleep(streamIdleTimeout / 4)
					if _, err := io.WriteString(writer, ": ping\n\n"); err != nil {
						return
					}
				}
				io.WriteString(writer, responseComplete([]any{responseText("alive")}))
			}()
			return &http.Response{StatusCode: http.StatusOK, Body: reader, Header: make(http.Header)}, nil
		})}
		answer, err := New(cfg).Run(context.Background(), "hello", nil)
		if err != nil || answer != "alive" || requests != 1 {
			t.Fatalf("answer=%q err=%v requests=%d", answer, err, requests)
		}
	})
}

type sseFixtureTransport func(*http.Request) (*http.Response, error)

func (f sseFixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
