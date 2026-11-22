package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a concurrency-safe sink for the server's log output: the log
// line is written from the request goroutine while the test goroutine reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestNewHandlerLogsEveryRequest is the end-to-end acceptance test for T-2: a
// request that reaches the API over real HTTP produces exactly one JSON log
// line carrying method, path, status and duration_ms.
func TestNewHandlerLogsEveryRequest(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{name: "sample route", method: http.MethodGet, path: "/", wantStatus: http.StatusOK},
		{name: "health route", method: http.MethodGet, path: "/health", wantStatus: http.StatusOK},
		{name: "unknown route", method: http.MethodGet, path: "/nope", wantStatus: http.StatusNotFound},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logs := &syncBuffer{}
			logger := slog.New(slog.NewJSONHandler(logs, nil))

			srv := httptest.NewServer(newHandler(logger))
			defer srv.Close()

			req, err := http.NewRequest(tc.method, srv.URL+tc.path, nil)
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("do request: %v", err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("response status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}

			line := waitForLogLine(t, logs)

			var entry map[string]any
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				t.Fatalf("log line is not valid JSON: %v\nline: %s", err, line)
			}

			if got, want := entry["method"], tc.method; got != want {
				t.Errorf("log method = %v, want %v", got, want)
			}
			if got, want := entry["path"], tc.path; got != want {
				t.Errorf("log path = %v, want %v", got, want)
			}
			if got, want := entry["status"], float64(tc.wantStatus); got != want {
				t.Errorf("log status = %v, want %v", got, want)
			}
			if _, ok := entry["duration_ms"].(float64); !ok {
				t.Errorf("log duration_ms = %v (%T), want a JSON number", entry["duration_ms"], entry["duration_ms"])
			}
		})
	}
}

// TestNewHandlerServesSampleRoute is a small sanity check that the bootstrapped
// server actually serves a request; it keeps the T-2 wiring honest.
func TestNewHandlerServesSampleRoute(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv := httptest.NewServer(newHandler(logger))
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got, want := string(body), `{"service":"pilot-go"}`; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// TestNewHandlerServesHealth is the acceptance test for T-1: GET /health over
// real HTTP returns 200 with exactly {"status":"ok"}, so a deploy can check
// liveness. The body is compared byte-for-byte on purpose; a trailing newline
// would fail here.
func TestNewHandlerServesHealth(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	srv := httptest.NewServer(newHandler(logger))
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got, want := string(body), `{"status":"ok"}`; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
	if got := resp.Header.Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Errorf("Content-Type = %q, want it to contain application/json", got)
	}
}

// TestHealthDoesNotTouchAnyStore documents and enforces the "without touching
// the database" part of T-1: the health handler is served even when no store or
// database is wired into newHandler at all. A liveness check that depended on a
// datastore would make a DB outage look like a dead process.
func TestHealthDoesNotTouchAnyStore(t *testing.T) {
	// newHandler takes only a logger, so there is no datastore in scope for the
	// handler to reach. Assert the route answers 200 without any setup beyond
	// the routes themselves.
	h := newHandler(slog.New(slog.NewJSONHandler(io.Discard, nil)))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got, want := rec.Body.String(), `{"status":"ok"}`; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// waitForLogLine blocks until the request log line has been written and returns
// it, failing if more than one line appears.
func waitForLogLine(t *testing.T, logs *syncBuffer) string {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		out := strings.TrimSpace(logs.String())
		if out != "" {
			// Give a duplicate line, if any, a chance to show up.
			time.Sleep(20 * time.Millisecond)
			lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
			if len(lines) != 1 {
				t.Fatalf("log output has %d lines, want exactly 1:\n%s", len(lines), logs.String())
			}
			return lines[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("no log line was written within the deadline")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
