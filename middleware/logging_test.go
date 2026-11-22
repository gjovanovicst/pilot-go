// Package middleware_test holds the tests for the T-2 request logging
// middleware: every request must produce exactly one structured JSON log line
// carrying method, path, status and duration_ms.
package middleware_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"pilot-go/middleware"
)

// logEntry is a decoded slog JSON line.
type logEntry map[string]any

// requestCase describes one request/response pair exercised through the
// logging middleware.
type requestCase struct {
	name       string
	method     string
	target     string
	handler    http.HandlerFunc
	wantPath   string
	wantStatus int
}

func requestCases() []requestCase {
	return []requestCase{
		{
			name:   "explicit 200",
			method: http.MethodGet,
			target: "/items",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("ok"))
			},
			wantPath:   "/items",
			wantStatus: http.StatusOK,
		},
		{
			name:   "implicit 200 when WriteHeader is never called",
			method: http.MethodGet,
			target: "/implicit",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("body without status"))
				_, _ = w.Write([]byte(" and a second write"))
			},
			wantPath:   "/implicit",
			wantStatus: http.StatusOK,
		},
		{
			name:   "404",
			method: http.MethodGet,
			target: "/missing",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.NotFound(w, r)
			},
			wantPath:   "/missing",
			wantStatus: http.StatusNotFound,
		},
		{
			name:   "500",
			method: http.MethodPost,
			target: "/boom",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantPath:   "/boom",
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:   "query string is not logged as part of the path",
			method: http.MethodGet,
			target: "/search?q=secret&token=abc",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			},
			wantPath:   "/search",
			wantStatus: http.StatusNoContent,
		},
	}
}

// TestLoggingEmitsOneLinePerRequest is the acceptance test for T-2: after a
// request reaches the API and the handler finishes, exactly one JSON log line
// is emitted and it carries method, path, status and duration_ms.
func TestLoggingEmitsOneLinePerRequest(t *testing.T) {
	for _, tc := range requestCases() {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, nil))
			handler := middleware.Logging(logger)(tc.handler)

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.target, nil))

			if got := rec.Code; got != tc.wantStatus {
				t.Fatalf("response status = %d, want %d", got, tc.wantStatus)
			}

			entry := singleEntry(t, buf.String())

			if got, want := entry["method"], tc.method; got != want {
				t.Errorf("log method = %v, want %v", got, want)
			}
			if got, want := entry["path"], tc.wantPath; got != want {
				t.Errorf("log path = %v, want %v", got, want)
			}
			if got, want := entry["status"], float64(tc.wantStatus); got != want {
				t.Errorf("log status = %v, want %v", got, want)
			}

			duration, ok := entry["duration_ms"].(float64)
			if !ok {
				t.Fatalf("log duration_ms = %v (%T), want a JSON number", entry["duration_ms"], entry["duration_ms"])
			}
			if duration < 0 {
				t.Errorf("log duration_ms = %v, want a non-negative value", duration)
			}
			if math.Trunc(duration) != duration {
				t.Errorf("log duration_ms = %v, want whole milliseconds", duration)
			}
		})
	}
}

// TestLoggingEmitsExactlyTheContractKeys pins the emitted key set: the four
// contract fields must be present and no request metadata beyond them (such as
// a query string, headers or a body) may be logged.
func TestLoggingEmitsExactlyTheContractKeys(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	handler := middleware.Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/items?token=super-secret", nil))

	entry := singleEntry(t, buf.String())

	wantKeys := []string{"time", "level", "msg", "method", "path", "status", "duration_ms"}
	if len(entry) != len(wantKeys) {
		t.Fatalf("log line has keys %v, want exactly %v", sortedKeys(entry), wantKeys)
	}
	for _, key := range wantKeys {
		if _, ok := entry[key]; !ok {
			t.Errorf("log line is missing key %q (got %v)", key, sortedKeys(entry))
		}
	}
	if got, want := entry["msg"], "http request"; got != want {
		t.Errorf("log msg = %v, want %v", got, want)
	}
	if strings.Contains(buf.String(), "super-secret") {
		t.Errorf("log line leaked the query string: %s", buf.String())
	}
}

// TestLoggingMakesDurationObservable verifies that duration_ms reflects the
// handler runtime instead of being a constant zero.
func TestLoggingMakesDurationObservable(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	handler := middleware.Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/slow", nil))

	entry := singleEntry(t, buf.String())
	duration, ok := entry["duration_ms"].(float64)
	if !ok {
		t.Fatalf("log duration_ms = %v (%T), want a JSON number", entry["duration_ms"], entry["duration_ms"])
	}
	if duration < 20 {
		t.Errorf("log duration_ms = %v, want at least 20 for a 20ms handler", duration)
	}
}

// TestLoggingEmitsLineWhenHandlerPanics makes sure a panicking handler is still
// accounted for: exactly one log line is written before the panic continues,
// carrying the contract fields, and the status is the one the client actually
// sees (500 when nothing was written yet, otherwise the status already sent).
func TestLoggingEmitsLineWhenHandlerPanics(t *testing.T) {
	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantStatus int
	}{
		{
			name: "panic before any header is 500",
			handler: func(w http.ResponseWriter, r *http.Request) {
				panic("handler exploded")
			},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "panic after a header keeps the status already sent",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusTeapot)
				panic("handler exploded after the header")
			},
			wantStatus: http.StatusTeapot,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, nil))
			handler := middleware.Logging(logger)(tc.handler)

			func() {
				defer func() {
					if recover() == nil {
						t.Fatal("expected the handler panic to keep propagating")
					}
				}()
				handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/panic", nil))
			}()

			entry := singleEntry(t, buf.String())
			if got, want := entry["method"], http.MethodGet; got != want {
				t.Errorf("log method = %v, want %v", got, want)
			}
			if got, want := entry["path"], "/panic"; got != want {
				t.Errorf("log path = %v, want %v", got, want)
			}
			if got, want := entry["status"], float64(tc.wantStatus); got != want {
				t.Errorf("log status = %v, want %v", got, want)
			}
			if _, ok := entry["duration_ms"]; !ok {
				t.Errorf("log line is missing duration_ms: %v", entry)
			}
		})
	}
}

// TestLoggingKeepsOptionalInterfacesReachable guards against the response
// writer wrapper hiding http.Flusher behind the recorder.
func TestLoggingKeepsOptionalInterfacesReachable(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	flushed := false
	handler := middleware.Logging(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("Flush through the logging wrapper: %v", err)
		}
		flushed = true
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stream", nil))

	if !flushed {
		t.Fatal("handler never reached the flush assertion")
	}
	if !rec.Flushed {
		t.Error("underlying writer was not flushed")
	}
	singleEntry(t, buf.String())
}

// singleEntry decodes the log output and fails unless it holds exactly one JSON
// object. It is what pins the "one slog line per request" requirement.
func singleEntry(t *testing.T, out string) logEntry {
	t.Helper()

	lines := make([]string, 0, 1)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("log output has %d lines, want exactly 1:\n%s", len(lines), out)
	}

	var entry logEntry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("log line is not valid JSON: %v\nline: %s", err, lines[0])
	}
	return entry
}

func sortedKeys(entry logEntry) []string {
	keys := make([]string, 0, len(entry))
	for key := range entry {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
