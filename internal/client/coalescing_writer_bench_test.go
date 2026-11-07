package client

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"rdev/internal/protocol"
)

type benchmarkOutputTransport struct {
	recordingOutputTransport
	framesSent int
}

func (transport *benchmarkOutputTransport) WriteBinary([]byte) error {
	transport.framesSent++
	return nil
}

// A memory transport isolates framing/coalescing cost from network latency.
// The long timer interval measures byte-threshold batching deterministically;
// timing and final-flush ordering are exercised by the behavioral tests.
func BenchmarkCoalescingOutput(b *testing.B) {
	for _, size := range []int{32, 1024, 8192} {
		b.Run(fmt.Sprintf("chunk_%d", size), func(b *testing.B) {
			transport := &benchmarkOutputTransport{}
			client := NewClient("", "benchmark", "", "")
			client.transport = transport
			chunk := bytes.Repeat([]byte("x"), size)
			b.ReportAllocs()
			b.SetBytes(256 * 1024)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				writer := newCoalescingWriterWithInterval(client, "benchmark", protocol.BinData, time.Hour)
				for n := 0; n < 256*1024/size; n++ {
					writer.Write(chunk)
				}
				writer.flush()
			}
			b.ReportMetric(float64(transport.framesSent)/float64(b.N), "frames/op")
		})
	}
}
