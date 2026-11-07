package server

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

var benchmarkAgentOutput string

// Each iteration models a 256KiB stream. Both implementations expose immutable
// snapshots every 256 deltas plus completion. Keep the former append-and-trim
// path as a reproducible comparison, not as a production alternative.
func BenchmarkAgentJobOutput(b *testing.B) {
	for _, chunkSize := range []int{32, 1024, 32768} {
		b.Run(fmt.Sprintf("chunk_%d", chunkSize), func(b *testing.B) {
			for _, legacy := range []bool{true, false} {
				name := "buffer"
				if legacy {
					name = "legacy"
				}
				b.Run(name, func(b *testing.B) {
					chunk := strings.Repeat("x", chunkSize)
					b.SetBytes(256 * 1024)
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						var output agentJobOutput
						var text string
						for n := 0; n < 256*1024/chunkSize; n++ {
							if legacy {
								text = legacyAgentJobTail(text + chunk)
							} else {
								output.append(chunk)
							}
							if n%256 == 0 {
								if legacy {
									benchmarkAgentOutput = text
								} else {
									benchmarkAgentOutput = output.String()
								}
							}
						}
						if !legacy {
							text = output.String()
						}
						benchmarkAgentOutput = text
						if len(text) != agentJobOutputLimit {
							b.Fatal("incorrect output length")
						}
					}
				})
			}
		})
	}
}

func legacyAgentJobTail(output string) string {
	if len(output) <= agentJobOutputLimit {
		return output
	}
	start := len(output) - agentJobOutputLimit
	for start < len(output) && !utf8.RuneStart(output[start]) {
		start++
	}
	return output[start:]
}
