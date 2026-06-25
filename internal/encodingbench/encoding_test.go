// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package encodingbench

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

// avgSize encodes the whole corpus with codec c, verifies that every record
// round-trips (Decode(Encode(a)) proto.Equal a), and returns the average encoded
// value size in bytes. A failing round-trip fails the test, so any reported size
// is for a genuinely lossless encoding.
func avgSize(t *testing.T, c Codec) float64 {
	t.Helper()
	corpus := Corpus()
	var total int
	for i, a := range corpus {
		b, err := c.Encode(a)
		if err != nil {
			t.Fatalf("%s: encode[%d]: %v", c.Name, i, err)
		}
		total += len(b)
		got, err := c.Decode(b)
		if err != nil {
			t.Fatalf("%s: decode[%d]: %v", c.Name, i, err)
		}
		if !proto.Equal(a, got) {
			t.Fatalf("%s: round-trip mismatch at record %d", c.Name, i)
		}
	}
	avg := float64(total) / float64(len(corpus))
	t.Logf("%s: %.1f B/value (avg over %d records)", c.Name, avg, len(corpus))
	return avg
}

func codecByName(name string) Codec {
	for _, c := range Codecs() {
		if c.Name == name {
			return c
		}
	}
	panic("unknown codec: " + name)
}

// One test per encoding, so each can be run and shared independently:
//   go test ./internal/encodingbench/ -run TestBinaryProto -v

func TestProtojson(t *testing.T)   { avgSize(t, codecByName("protojson (current)")) }
func TestBinaryProto(t *testing.T) { avgSize(t, codecByName("binary protobuf")) }
func TestFieldTrims(t *testing.T)  { avgSize(t, codecByName("binary + field trims")) }
func TestZstdNoDict(t *testing.T)  { avgSize(t, codecByName("zstd(binary), no dict")) }
func TestZstdRawDict(t *testing.T) { avgSize(t, codecByName("zstd(binary), raw dict")) }

// TestSummary prints the full comparison table (value size only; add the fixed
// per-record Valkey overhead, ~120 B for a 42 B key plus engine bookkeeping, to
// estimate in-RAM cost, or run TestValkeyPerRecord for the measured figure).
func TestSummary(t *testing.T) {
	base := avgSizeQuiet(codecByName("protojson (current)"))
	t.Logf("%-26s %9s  %14s", "encoding", "B/value", "vs protojson")
	for _, c := range Codecs() {
		v := avgSizeQuiet(c)
		t.Logf("%-26s %9.1f  %13.0f%%", c.Name, v, 100*(1-v/base))
	}
	t.Logf("%-26s %9.1f  %13.0f%%   (ceiling, not a per-record encoding)",
		"shared-context stream*", SharedContextCeiling(), 100*(1-SharedContextCeiling()/base))
	t.Logf("raw dictionary shipped once: %d B", len(RawDictBytes()))
}

// avgSizeQuiet is avgSize without the round-trip check or logging, for the
// summary table (round-trips are already asserted by the per-encoding tests).
func avgSizeQuiet(c Codec) float64 {
	corpus := Corpus()
	var total int
	for _, a := range corpus {
		b, _ := c.Encode(a)
		total += len(b)
	}
	return float64(total) / float64(len(corpus))
}

// Encode/decode throughput, so the CPU cost of each encoding is visible
// alongside its size (compression trades CPU for bytes):
//   go test ./internal/encodingbench/ -run x -bench . -benchmem

func BenchmarkEncode(b *testing.B) {
	corpus := Corpus()
	for _, c := range Codecs() {
		c := c
		b.Run(c.Name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if _, err := c.Encode(corpus[i%len(corpus)]); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkDecode(b *testing.B) {
	corpus := Corpus()
	for _, c := range Codecs() {
		c := c
		enc := make([][]byte, len(corpus))
		for i, a := range corpus {
			enc[i], _ = c.Encode(a)
		}
		b.Run(c.Name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if _, err := c.Decode(enc[i%len(enc)]); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
