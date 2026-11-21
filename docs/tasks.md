# pilot-go — the weekly tasks

The three ready tasks the pilot picks from each week. Each has the acceptance
criteria the agent must prove.

## T-1 — Add a health endpoint

Expose `GET /health` so a deploy can check liveness without touching the
database.

- Given the API is running, when `GET /health` is called, then it returns 200
  with `{"status":"ok"}`.

## T-2 — Log requests as structured JSON

Every HTTP request emits one slog line with method, path, status and duration.

- Given a request reaches the API, when the handler finishes, then the log line
  carries `method`, `path`, `status` and `duration_ms`.

## T-3 — Document the quickstart

The README shows the commands to run the stack locally.

- Given a new developer, when they open the README, then the run commands and
  the health check are visible.
