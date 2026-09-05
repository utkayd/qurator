package httpapi

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteErrorNeverLeaksCause(t *testing.T) {
	driverErr := errors.New("pq: password authentication failed for user \"qurator\" dsn=postgres://u:hunter2@db/x")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/codes", nil)
	Internal(rec, req, driverErr)
	body := rec.Body.String()
	for _, leak := range []string{"hunter2", "postgres://", "pq:", "password"} {
		if strings.Contains(body, leak) {
			t.Fatalf("body leaked %q: %s", leak, body)
		}
	}
	var eb ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &eb); err != nil {
		t.Fatal(err)
	}
	if eb.Error.Code != CodeInternal || rec.Code != 500 {
		t.Fatalf("got %s/%d", eb.Error.Code, rec.Code)
	}
}

func TestStatusMapping(t *testing.T) {
	cases := map[ErrorCode]int{
		CodeInvalidRequest: 400, CodeContentTooLarge: 413, CodeAliasTaken: 409, CodeAliasReserved: 409,
		CodeNotFound: 404, CodeCodeDisabled: 410, CodeUnauthorized: 401, CodeTokenRevoked: 401,
		CodeForbidden: 403, CodeConflict: 409, CodeRateLimited: 429, CodeInternal: 500,
	}
	for c, want := range cases {
		if got := c.Status(); got != want {
			t.Errorf("%s: got %d want %d", c, got, want)
		}
	}
}
