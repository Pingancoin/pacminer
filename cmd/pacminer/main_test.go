package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestNormalizePoolAddress(t *testing.T) {
	tests := map[string]string{
		"stratum.pingancoin.org":          "stratum.pingancoin.org:3333",
		"stratum.pingancoin.org:4444":     "stratum.pingancoin.org:4444",
		"stratum+tcp://example.com:3333":  "example.com:3333",
		"tcp://example.com:3333":          "example.com:3333",
		"stratum://example.com:3333":      "example.com:3333",
		"  stratum.pingancoin.org:3333  ": "stratum.pingancoin.org:3333",
	}
	for input, want := range tests {
		if got := normalizePoolAddress(input); got != want {
			t.Fatalf("normalizePoolAddress(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestJobFromNotifyUsesHeaderExtension(t *testing.T) {
	header := make([]byte, headerLength)
	binary.LittleEndian.PutUint32(header[headerHeightOffset:headerLength], 123)
	headerHex := hex.EncodeToString(header)
	params := []json.RawMessage{
		raw("job-1"),
		raw("prev"),
		raw("coinbase"),
		raw("207fffff"),
		raw("0000002a"),
		raw(true),
		raw(headerHex),
	}

	job, err := jobFromNotify(params, 2, 9)
	if err != nil {
		t.Fatal(err)
	}
	if job.id != "job-1" || job.seq != 9 || job.difficulty != 2 {
		t.Fatalf("unexpected job metadata: %+v", job)
	}
	if got := binary.LittleEndian.Uint64(job.header[headerTimestampOffset:headerBitsOffset]); got != 42 {
		t.Fatalf("timestamp = %d, want 42", got)
	}
	if got := binary.LittleEndian.Uint32(job.header[headerBitsOffset:headerNonceOffset]); got != 0x207fffff {
		t.Fatalf("bits = %08x, want 207fffff", got)
	}
	if got := binary.LittleEndian.Uint32(job.header[headerHeightOffset:headerLength]); got != 123 {
		t.Fatalf("height = %d, want 123", got)
	}
}

func TestJobFromNotifyRequiresHeaderExtension(t *testing.T) {
	_, err := jobFromNotify([]json.RawMessage{raw("job")}, 1, 1)
	if err == nil {
		t.Fatal("expected missing headerhex error")
	}
}

func raw(v any) json.RawMessage {
	encoded, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return encoded
}
