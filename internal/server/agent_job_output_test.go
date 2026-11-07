package server

import (
	"math/rand"
	"strings"
	"testing"
)

func TestAgentJobOutputMatchesTailAcrossChunkBoundaries(t *testing.T) {
	random := rand.New(rand.NewSource(17))
	source := strings.Repeat("a中文🌈0123456789", 10000)
	for _, size := range []int{1, 7, 1024, agentJobOutputLimit, agentJobOutputLimit * 2} {
		var output agentJobOutput
		expected := ""
		var saved, immutable string
		for i := 0; i < 400; i++ {
			start := random.Intn(len(source) - size)
			chunk := source[start : start+size]
			output.append(chunk)
			expected = agentJobTail(expected + chunk)
			if actual := output.String(); actual != expected {
				t.Fatalf("size=%d iteration=%d actual=%d expected=%d", size, i, len(actual), len(expected))
			}
			if len(output.data) > agentJobOutputLimit {
				t.Fatal("unbounded output")
			}
			if i == 10 {
				saved = output.String()
				immutable = strings.Clone(saved)
			}
			if saved != immutable {
				t.Fatal("later append mutated an earlier snapshot")
			}
		}
	}
}
