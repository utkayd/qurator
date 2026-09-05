package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestLoggerInjectsRequestID(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger("info", "json", &buf)

	ctx := WithRequestID(context.Background(), "abc")
	logger.InfoContext(ctx, "x")

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("unmarshal log line: %v (line: %s)", err, buf.String())
	}
	if got, ok := rec["request_id"]; !ok || got != "abc" {
		t.Fatalf("expected request_id=abc, got %v (line: %s)", got, buf.String())
	}
}

func TestLoggerOmitsRequestIDWhenAbsent(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger("info", "json", &buf)

	logger.InfoContext(context.Background(), "x")

	if strings.Contains(buf.String(), "request_id") {
		t.Fatalf("expected no request_id key, got line: %s", buf.String())
	}
}

func TestNewRequestIDIsHex32(t *testing.T) {
	id := NewRequestID()
	if len(id) != 32 {
		t.Fatalf("expected 32 hex chars (16 bytes), got %d: %q", len(id), id)
	}
}

func TestRequestIDFromAbsent(t *testing.T) {
	if _, ok := RequestIDFrom(context.Background()); ok {
		t.Fatalf("expected no request id in bare context")
	}
}
