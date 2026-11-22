// Package middleware holds the HTTP middleware used by the pilot-go API.
package middleware

import (
	"log/slog"
	"math"
	"net/http"
	"time"
)

// statusRecorder wraps an http.ResponseWriter so the middleware can observe the
// status code that was actually sent to the client. Handlers that never call
// WriteHeader are recorded as 200 OK, which is the status net/http writes
// implicitly on the first Write.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

// WriteHeader remembers the first status code and forwards it to the wrapped
// writer. Redundant calls are ignored, so the recorded status always matches
// what was written to the client.
func (r *statusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.wroteHeader = true
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Write records the implicit 200 OK before forwarding the payload.
func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(b)
}

// Unwrap exposes the wrapped writer to http.ResponseController, so optional
// interfaces such as http.Flusher and http.Hijacker stay reachable by handlers
// and by net/http itself.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// durationMillis converts a measured handler duration into milliseconds. The
// value is fractional and rounded to microsecond resolution, so a handler that
// finishes faster than a millisecond reports a small non-zero number instead of
// a flat 0. The unit stays milliseconds and the JSON key stays duration_ms.
// Negative measurements (which time.Since should never produce) clamp to 0.
func durationMillis(d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return math.Round(float64(d)/float64(time.Microsecond)) / 1000
}

// Logging returns middleware that emits exactly one structured log line per
// HTTP request once the wrapped handler finishes. The line carries the request
// method, the request path, the response status and the handler duration in
// milliseconds.
//
// duration_ms is a fractional number with microsecond resolution (for example
// 0.412 for a 412µs handler), so fast handlers stay measurable instead of being
// truncated to 0. The key and the unit are unchanged.
//
// The path is taken from r.URL.Path on purpose: query strings, headers and
// bodies are never logged, so request parameters do not leak into the logs.
//
// The line is emitted from a deferred call, so it is written for every request
// that reaches the wrapper, including requests whose handler panics. For a
// panic that happens before any header was written the logged status is 500,
// because that is what net/http sends to the client in that case; the panic is
// then re-raised unchanged so net/http keeps its usual behaviour.
func Logging(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			defer func() {
				recovered := recover()

				// A panic aborts the response; net/http answers with 500 when
				// nothing has been written yet. Record that so the log line does
				// not claim a 200 for a failed request.
				if recovered != nil && !rec.wroteHeader {
					rec.status = http.StatusInternalServerError
				}

				logger.Info("http request",
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Int("status", rec.status),
					slog.Float64("duration_ms", durationMillis(time.Since(start))),
				)

				if recovered != nil {
					panic(recovered)
				}
			}()

			next.ServeHTTP(rec, r)
		})
	}
}
