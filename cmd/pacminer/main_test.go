package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
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
	binary.LittleEndian.PutUint32(header[headerHeightOffset:headerHeightOffset+4], 123)
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
	if got := binary.LittleEndian.Uint32(job.header[headerTimestampOffset : headerTimestampOffset+4]); got != 42 {
		t.Fatalf("timestamp = %d, want 42", got)
	}
	if got := binary.LittleEndian.Uint32(job.header[headerBitsOffset : headerBitsOffset+4]); got != 0x207fffff {
		t.Fatalf("bits = %08x, want 207fffff", got)
	}
	if got := binary.LittleEndian.Uint32(job.header[headerHeightOffset : headerHeightOffset+4]); got != 123 {
		t.Fatalf("height = %d, want 123", got)
	}
}

func TestJobFromNotifyRequiresHeaderExtension(t *testing.T) {
	_, err := jobFromNotify([]json.RawMessage{raw("job")}, 1, 1)
	if err == nil {
		t.Fatal("expected missing headerhex error")
	}
}

func TestExtraNonceHexUsesFullPoolPayload(t *testing.T) {
	got := extraNonceHex(0x1122334455667788)
	if len(got) != 24 {
		t.Fatalf("extraNonceHex length = %d, want 24 hex chars", len(got))
	}
	if got == "8877665544332211" {
		t.Fatal("extraNonceHex must submit the full 12-byte payload, not the old 8-byte form")
	}
}

func TestOpenCLKernelPadsPACHeaderLength(t *testing.T) {
	source, err := os.ReadFile("opencl_kernel_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	kernel := string(source)
	for _, want := range []string{
		"b2[52] = (uchar)0x80;",
		"b2[55] = (uchar)0x01;",
		"b2[62] = (uchar)0x05;",
		"b2[63] = (uchar)0xa0;",
		"compress(h, b2, (ulong)1440);",
	} {
		if !strings.Contains(kernel, want) {
			t.Fatalf("OpenCL kernel is missing PAC header padding %q", want)
		}
	}
	if strings.Contains(kernel, "b1[24] = (uchar)0x80;") {
		t.Fatal("OpenCL kernel still uses the old 88-byte header padding")
	}
}

func raw(v any) json.RawMessage {
	encoded, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return encoded
}
