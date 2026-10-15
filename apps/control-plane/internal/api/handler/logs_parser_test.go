package handler

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
)

type fixedErrorReader struct {
	err error
}

func (r fixedErrorReader) Read([]byte) (int, error) { return 0, r.err }

func dockerFrame(stream byte, payload string) []byte {
	frame := make([]byte, 8+len(payload))
	frame[0] = stream
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(payload)))
	copy(frame[8:], payload)
	return frame
}

func TestLogDecoderMultiplexesStreamsAndLines(t *testing.T) {
	data := append(dockerFrame(1, "out-1\nout"), dockerFrame(2, "err-1\n")...)
	data = append(data, dockerFrame(1, "-2\r\nlast")...)
	entries, err := decodeTail(bytes.NewReader(data), swarm.LogFramingMultiplexed, 1024, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []LogEntryResponse{{Stream: "stdout", Message: "out-1"}, {Stream: "stderr", Message: "err-1"}, {Stream: "stdout", Message: "out-2"}, {Stream: "stdout", Message: "last"}}
	if len(entries) != len(want) {
		t.Fatalf("entries = %#v", entries)
	}
	for index := range want {
		if entries[index] != want[index] {
			t.Fatalf("entry %d = %#v, want %#v", index, entries[index], want[index])
		}
	}
}

func TestLogDecoderRejectsMalformedAndOverLimitInput(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		framing swarm.LogFraming
		limit   int64
		want    error
	}{
		{name: "unknown framing", data: []byte("x"), framing: "unknown", limit: 10, want: errInvalidLogFraming},
		{name: "system error", data: dockerFrame(3, "runtime failure"), framing: swarm.LogFramingMultiplexed, limit: 20, want: errRuntimeLogUpstream},
		{name: "empty system error", data: dockerFrame(3, ""), framing: swarm.LogFramingMultiplexed, limit: 20, want: errRuntimeLogUpstream},
		{name: "empty data frame", data: dockerFrame(1, ""), framing: swarm.LogFramingMultiplexed, limit: 20, want: errMalformedLogData},
		{name: "invalid stream", data: dockerFrame(4, "x"), framing: swarm.LogFramingMultiplexed, limit: 10, want: errMalformedLogData},
		{name: "reserved header", data: func() []byte { b := dockerFrame(1, "x"); b[1] = 1; return b }(), framing: swarm.LogFramingMultiplexed, limit: 10, want: errMalformedLogData},
		{name: "truncated header", data: []byte{1, 0}, framing: swarm.LogFramingMultiplexed, limit: 10, want: errMalformedLogData},
		{name: "truncated payload", data: dockerFrame(1, "long")[:10], framing: swarm.LogFramingMultiplexed, limit: 10, want: errMalformedLogData},
		{name: "frame too large", data: dockerFrame(1, "too-long"), framing: swarm.LogFramingMultiplexed, limit: 3, want: errLogPayloadLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeTail(bytes.NewReader(test.data), test.framing, test.limit, 10)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestLogDecoderPreservesMultiplexedTransportErrors(t *testing.T) {
	transportErr := errors.New("runtime transport failed")
	if _, err := decodeTail(fixedErrorReader{err: transportErr}, swarm.LogFramingMultiplexed, 1024, 10); !errors.Is(err, transportErr) {
		t.Fatalf("header transport error = %v", err)
	}

	header := dockerFrame(1, "payload")[:8]
	reader := io.MultiReader(bytes.NewReader(header), fixedErrorReader{err: transportErr})
	if _, err := decodeTail(reader, swarm.LogFramingMultiplexed, 1024, 10); !errors.Is(err, transportErr) {
		t.Fatalf("payload transport error = %v", err)
	}
}

func TestLogDecoderEnforcesCumulativeSnapshotLimitAndLineCount(t *testing.T) {
	data := append(dockerFrame(1, "a\n"), dockerFrame(1, "b\n")...)
	decoder, err := newLogDecoder(bytes.NewReader(data), swarm.LogFramingMultiplexed, 10, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.Next(); err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.Next(); !errors.Is(err, errLogPayloadLimit) {
		t.Fatalf("error = %v", err)
	}

	entries, err := decodeTail(bytes.NewReader(dockerFrame(1, "a\nb\nc\n")), swarm.LogFramingMultiplexed, 100, 2)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries/error = %#v/%v", entries, err)
	}
}

func TestDecodeTailValidatesDataAfterTheRequestedLineLimit(t *testing.T) {
	validPrefix := dockerFrame(1, "first\nsecond\n")
	for _, test := range []struct {
		name   string
		suffix []byte
		limit  int64
		want   error
	}{
		{name: "system error", suffix: dockerFrame(3, "private daemon error"), limit: 1024, want: errRuntimeLogUpstream},
		{name: "malformed frame", suffix: []byte{1, 0}, limit: 1024, want: errMalformedLogData},
		{name: "payload budget", suffix: dockerFrame(1, "overflow"), limit: int64(len("first\nsecond\n")), want: errLogPayloadLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := append(append([]byte(nil), validPrefix...), test.suffix...)
			_, err := decodeTail(bytes.NewReader(data), swarm.LogFramingMultiplexed, test.limit, 1)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}

	entries, err := decodeTail(bytes.NewReader(dockerFrame(1, "first\nsecond\nthird\n")), swarm.LogFramingMultiplexed, 1024, 2)
	if err != nil || len(entries) != 2 || entries[0].Message != "first" || entries[1].Message != "second" {
		t.Fatalf("entries/error = %#v/%v", entries, err)
	}
}

func TestLogDecoderQueuesAtMostOneLineFromLargeMultiplexedFrame(t *testing.T) {
	payload := bytes.Repeat([]byte{'\n'}, 10000)
	decoder, err := newLogDecoder(bytes.NewReader(dockerFrame(1, string(payload))), swarm.LogFramingMultiplexed, int64(len(payload)), 0)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := decoder.Next()
	if err != nil || entry.Stream != "stdout" || entry.Message != "" {
		t.Fatalf("entry/error = %#v/%v", entry, err)
	}
	if len(decoder.queue) > 1 || len(decoder.bufferedChunk) != len(payload)-1 {
		t.Fatalf("queue/buffered = %d/%d", len(decoder.queue), len(decoder.bufferedChunk))
	}
}

func TestLogDecoderEmptyInputReturnsEOF(t *testing.T) {
	decoder, err := newLogDecoder(bytes.NewReader(nil), swarm.LogFramingMultiplexed, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("error = %v", err)
	}
}

func FuzzLogDecoderNeverPanics(f *testing.F) {
	f.Add([]byte{})
	f.Add(dockerFrame(1, "hello\n"))
	f.Add([]byte("hello\nworld"))
	f.Add([]byte{2, 0, 0, 0, 0, 0, 0, 1, 0xff})
	f.Fuzz(func(t *testing.T, data []byte) {
		decoder, err := newLogDecoder(bytes.NewReader(data), swarm.LogFramingMultiplexed, 256, 512)
		if err != nil {
			return
		}
		for index := 0; index <= len(data)+1; index++ {
			_, err := decoder.Next()
			if err != nil {
				return
			}
		}
		t.Fatal("decoder produced more records than bounded input permits")
	})
}
