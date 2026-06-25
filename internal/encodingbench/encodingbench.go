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

// Package encodingbench measures the on-the-wire and in-Valkey size of several
// candidate encodings for the control-plane Actor record. It is a standalone,
// shareable benchmark: anyone can run `go test ./internal/encodingbench/...` to
// reproduce the numbers, and point ENCODINGBENCH_VALKEY_ADDR at a Valkey/Redis
// instance to measure real per-record memory (value + key + engine overhead).
//
// Each encoding is exercised by its own test (TestProtojson, TestBinaryProto,
// TestFieldTrims, TestZstdNoDict, TestZstdRawDict); TestSummary prints the full
// comparison table. All codecs are round-trip verified, so the reported sizes
// are for genuinely decodable encodings, not lossy approximations.
//
// See docs/record-encoding-benchmarks.md for the writeup and guidance.
package encodingbench

import (
	"fmt"
	"math/rand"
	"strings"
	"sync"

	trimpb "github.com/agent-substrate/substrate/internal/encodingbench/trimmedpb"
	pb "github.com/agent-substrate/substrate/pkg/proto/ateapipb"
	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// CorpusSize is the number of synthetic Actor records each measurement runs over.
const CorpusSize = 50000

// snapBucketPrefix is the fixed portion of an external snapshot URI. The field
// trims codec relies on the full URI being
// "<snapBucketPrefix><template>/<actorID>/<suffix>" so it can store only the
// suffix and reconstruct the rest from fields already present in the record.
const snapBucketPrefix = "gs://ate-snapshots-prod/default/"

var (
	templates = []string{"claude-code-multiplex", "counter", "multi-template", "agent-secret"}
	zones     = []string{"us-central1-a", "us-central1-b", "us-east1-c", "europe-west1-b"}
	statuses  = []pb.Actor_Status{pb.Actor_STATUS_RUNNING, pb.Actor_STATUS_SUSPENDED, pb.Actor_STATUS_PAUSED}
)

var (
	corpusOnce sync.Once
	corpus     []*pb.Actor
)

// Corpus returns a deterministic set of representative Actor records. The same
// records are produced on every run (fixed RNG seed) so results are reproducible.
func Corpus() []*pb.Actor {
	corpusOnce.Do(func() {
		r := rand.New(rand.NewSource(42))
		detUUID := func() string {
			var b [16]byte
			r.Read(b[:])
			u, _ := uuid.FromBytes(b[:])
			return u.String()
		}
		corpus = make([]*pb.Actor, CorpusSize)
		for i := range corpus {
			tmpl := templates[i%len(templates)]
			id := detUUID()
			corpus[i] = &pb.Actor{
				ActorId:                id,
				Version:                int64(i%40) + 1,
				ActorTemplateNamespace: "default",
				ActorTemplateName:      tmpl,
				Status:                 statuses[i%len(statuses)],
				AteomPodNamespace:      "ate-system",
				AteomPodName:           fmt.Sprintf("worker-pool-prod-%c-%05x", "abcdef"[i%6], r.Intn(0xfffff)),
				AteomPodIp:             fmt.Sprintf("10.%d.%d.%d", r.Intn(256), r.Intn(256), r.Intn(254)+1),
				AteomPodUid:            detUUID(),
				WorkerPoolName:         "worker-pool-prod",
				WorkerSelector:         &pb.Selector{MatchLabels: map[string]string{"zone": zones[i%len(zones)]}},
				LatestSnapshotInfo: &pb.SnapshotInfo{
					Type: pb.SnapshotType_SNAPSHOT_TYPE_EXTERNAL,
					Data: &pb.SnapshotInfo_External{External: &pb.ExternalSnapshotInfo{
						SnapshotUriPrefix: snapBucketPrefix + tmpl + "/" + id + "/" + fmt.Sprintf("snap-%05d", i%40),
					}},
				},
			}
		}
	})
	return corpus
}

// Codec is one encoding under test. Encode and Decode must round-trip: for any
// Actor a, Decode(Encode(a)) must proto.Equal a.
type Codec struct {
	Name   string
	Encode func(*pb.Actor) ([]byte, error)
	Decode func([]byte) (*pb.Actor, error)
}

// Codecs returns every encoding under test, in reporting order.
func Codecs() []Codec {
	return []Codec{
		{"protojson (current)", encProtojson, decProtojson},
		{"binary protobuf", encBinary, decBinary},
		{"binary + field trims", encTrim, decTrim},
		{"zstd(binary), no dict", encZstdNoDict, decZstdNoDict},
		{"zstd(binary), raw dict", encZstdDict, decZstdDict},
	}
}

// --- protojson (current production encoding) ---

func encProtojson(a *pb.Actor) ([]byte, error) { return protojson.Marshal(a) }
func decProtojson(b []byte) (*pb.Actor, error) {
	a := &pb.Actor{}
	return a, protojson.Unmarshal(b, a)
}

// --- binary protobuf ---

func encBinary(a *pb.Actor) ([]byte, error) { return proto.Marshal(a) }
func decBinary(b []byte) (*pb.Actor, error) {
	a := &pb.Actor{}
	return a, proto.Unmarshal(b, a)
}

// --- binary + field trims ---
//
// A real protobuf encoding (trimmedpb.TrimmedActor, generated from trimmed.proto)
// that realizes the field-level trims as an actual schema change, so the measured
// size includes protobuf's field tags rather than estimating them away. Versus
// ateapipb.Actor it stores the two UUIDs as 16-byte bytes fields instead of
// 36-char strings, and the snapshot as a flat template-relative suffix string
// instead of the nested SnapshotInfo/ExternalSnapshotInfo message + full URI.

func toTrimmed(a *pb.Actor) (*trimpb.TrimmedActor, error) {
	aid, err := uuid.Parse(a.GetActorId())
	if err != nil {
		return nil, err
	}
	t := &trimpb.TrimmedActor{
		ActorId:                append([]byte(nil), aid[:]...),
		Version:                a.GetVersion(),
		ActorTemplateNamespace: a.GetActorTemplateNamespace(),
		ActorTemplateName:      a.GetActorTemplateName(),
		Status:                 trimpb.TrimmedActor_Status(a.GetStatus()),
		AteomPodNamespace:      a.GetAteomPodNamespace(),
		AteomPodName:           a.GetAteomPodName(),
		AteomPodIp:             a.GetAteomPodIp(),
		WorkerPoolName:         a.GetWorkerPoolName(),
		WorkerSelector:         a.GetWorkerSelector().GetMatchLabels(),
	}
	if s := a.GetAteomPodUid(); s != "" {
		u, err := uuid.Parse(s)
		if err != nil {
			return nil, err
		}
		t.AteomPodUid = append([]byte(nil), u[:]...)
	}
	if ext := a.GetLatestSnapshotInfo().GetExternal(); ext != nil {
		uri := ext.GetSnapshotUriPrefix()
		prefix := snapBucketPrefix + a.GetActorTemplateName() + "/" + a.GetActorId() + "/"
		if strings.HasPrefix(uri, prefix) {
			t.SnapshotSuffix = uri[len(prefix):]
		} else {
			t.SnapshotUriFull = uri
		}
	}
	return t, nil
}

func fromTrimmed(t *trimpb.TrimmedActor) (*pb.Actor, error) {
	aid, err := uuid.FromBytes(t.GetActorId())
	if err != nil {
		return nil, err
	}
	a := &pb.Actor{
		ActorId:                aid.String(),
		Version:                t.GetVersion(),
		ActorTemplateNamespace: t.GetActorTemplateNamespace(),
		ActorTemplateName:      t.GetActorTemplateName(),
		Status:                 pb.Actor_Status(t.GetStatus()),
		AteomPodNamespace:      t.GetAteomPodNamespace(),
		AteomPodName:           t.GetAteomPodName(),
		AteomPodIp:             t.GetAteomPodIp(),
		WorkerPoolName:         t.GetWorkerPoolName(),
	}
	if len(t.GetAteomPodUid()) == 16 {
		u, err := uuid.FromBytes(t.GetAteomPodUid())
		if err != nil {
			return nil, err
		}
		a.AteomPodUid = u.String()
	}
	if len(t.GetWorkerSelector()) > 0 {
		a.WorkerSelector = &pb.Selector{MatchLabels: t.GetWorkerSelector()}
	}
	var uri string
	switch {
	case t.GetSnapshotSuffix() != "":
		uri = snapBucketPrefix + a.ActorTemplateName + "/" + a.ActorId + "/" + t.GetSnapshotSuffix()
	case t.GetSnapshotUriFull() != "":
		uri = t.GetSnapshotUriFull()
	}
	if uri != "" {
		a.LatestSnapshotInfo = &pb.SnapshotInfo{
			Type: pb.SnapshotType_SNAPSHOT_TYPE_EXTERNAL,
			Data: &pb.SnapshotInfo_External{External: &pb.ExternalSnapshotInfo{SnapshotUriPrefix: uri}},
		}
	}
	return a, nil
}

func encTrim(a *pb.Actor) ([]byte, error) {
	t, err := toTrimmed(a)
	if err != nil {
		return nil, err
	}
	return proto.Marshal(t)
}

func decTrim(b []byte) (*pb.Actor, error) {
	t := &trimpb.TrimmedActor{}
	if err := proto.Unmarshal(b, t); err != nil {
		return nil, err
	}
	return fromTrimmed(t)
}

// --- zstd over binary protobuf, with and without a shared dictionary ---

func mustEncoder(opts ...zstd.EOption) *zstd.Encoder {
	e, err := zstd.NewWriter(nil, append([]zstd.EOption{zstd.WithEncoderLevel(zstd.SpeedBestCompression)}, opts...)...)
	if err != nil {
		panic(err)
	}
	return e
}

var (
	zEnc = mustEncoder()
	zDec = func() *zstd.Decoder {
		d, err := zstd.NewReader(nil)
		if err != nil {
			panic(err)
		}
		return d
	}()
)

func encZstdNoDict(a *pb.Actor) ([]byte, error) {
	b, err := proto.Marshal(a)
	if err != nil {
		return nil, err
	}
	return zEnc.EncodeAll(b, nil), nil
}

func decZstdNoDict(b []byte) (*pb.Actor, error) {
	raw, err := zDec.DecodeAll(b, nil)
	if err != nil {
		return nil, err
	}
	a := &pb.Actor{}
	return a, proto.Unmarshal(raw, a)
}

// The dictionary is the concatenation of the first dictSamples corpus records'
// binary encodings, used as raw prior content. klauspost's entropy-trained
// BuildDict is currently unreliable (returns empty), so this uses a raw-content
// dictionary: simpler, deterministic, and a conservative stand-in for a properly
// trained dictionary, which would do at least as well.
const dictID = 7
const dictSamples = 64

var (
	dictOnce     sync.Once
	zEncDict     *zstd.Encoder
	zDecDict     *zstd.Decoder
	rawDictBytes []byte
)

func dictCodecs() (*zstd.Encoder, *zstd.Decoder) {
	dictOnce.Do(func() {
		c := Corpus()
		var d []byte
		for i := 0; i < dictSamples && i < len(c); i++ {
			b, _ := proto.Marshal(c[i])
			d = append(d, b...)
		}
		rawDictBytes = d
		zEncDict = mustEncoder(zstd.WithEncoderDictRaw(dictID, d))
		dec, err := zstd.NewReader(nil, zstd.WithDecoderDictRaw(dictID, d))
		if err != nil {
			panic(err)
		}
		zDecDict = dec
	})
	return zEncDict, zDecDict
}

// RawDictBytes returns the dictionary content (for reporting its shipped size).
func RawDictBytes() []byte { dictCodecs(); return rawDictBytes }

func encZstdDict(a *pb.Actor) ([]byte, error) {
	e, _ := dictCodecs()
	b, err := proto.Marshal(a)
	if err != nil {
		return nil, err
	}
	return e.EncodeAll(b, nil), nil
}

func decZstdDict(b []byte) (*pb.Actor, error) {
	_, d := dictCodecs()
	raw, err := d.DecodeAll(b, nil)
	if err != nil {
		return nil, err
	}
	a := &pb.Actor{}
	return a, proto.Unmarshal(raw, a)
}

// SharedContextCeiling compresses the whole corpus as one stream and divides by
// the record count. It is the floor a perfectly trained dictionary approaches
// (not itself a viable per-record encoding), reported for context only.
func SharedContextCeiling() float64 {
	c := Corpus()
	var all []byte
	for _, a := range c {
		b, _ := proto.Marshal(a)
		all = append(all, b...)
	}
	return float64(len(zEnc.EncodeAll(all, nil))) / float64(len(c))
}
