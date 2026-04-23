# Contributing

Bug reports, feature requests, and PRs welcome.

## First time here?

- **Forking to run this for your own gym?** See
  [`docs/FORK-SETUP.md`](docs/FORK-SETUP.md) — no Go background assumed.
- **Contributing changes back?** Read on.

## Development setup

Requirements:

- Go 1.24+ (`go version` to confirm)
- An Arbox member account (for live-testing against the API)

```bash
git clone https://github.com/amanz81/arbox-scheduler.git
cd arbox-scheduler
cp .env.example .env        # fill in ARBOX_EMAIL + ARBOX_PASSWORD
go build -o bin/arbox ./cmd/arbox
go build -o bin/arbox-mcp ./cmd/arbox-mcp
go test ./...               # must pass before any commit
```

## Branching + commits

- Work on a branch off `main`: `git checkout -b feat/<short-name>`.
- One logical change per PR. Keep the diff small enough to read in 5 min.
- **Tests must pass** locally before you push. `go test ./...`.
- Commit messages follow
  [Conventional Commits](https://www.conventionalcommits.org/):
  `feat(x): …`, `fix(x): …`, `docs: …`, `refactor(x): …`, `test(x): …`.
  Body explains the *why* — tradeoffs, history — not a file list.
- PR title mirrors the first commit header. Body includes a short
  "Why" + "What changes" + "Risk" section.

## Tests that matter

- `go test ./...` is the baseline gate.
- For HTTP API changes, exercise them in `cmd/arbox/http_api_test.go` —
  it wires a fake Arbox upstream with `httptest.NewServer` so the tests
  are fully offline and deterministic.
- For MCP shim changes, `cmd/arbox-mcp/main_test.go` covers the MCP
  protocol (initialize / tools.list / tools.call / error paths).
- Renderer changes (how `/status`, `/morning`, etc. look in Telegram)
  are easiest to verify locally with `./bin/arbox tg-preview /status`
  — same build and code path, just prints to stdout.

## Safety rules for code review

A few things that must stay wired:

1. The HTTP API server **refuses to start** unless at least one of
   `ARBOX_API_READ_TOKEN` / `ARBOX_API_ADMIN_TOKEN` is set. Keep this
   default-off posture for every new binary build.
2. Every mutation endpoint requires the admin token **AND** `?confirm=1`
   on the URL. Without `?confirm=1` the handler returns
   `{"actual_send": false, "would_send": ...}` and writes a dry-run
   audit line. Do not remove this gate.
3. `/api/v1/arbox/query` passthrough has a specific defence-in-depth
   posture: admin-only, method allowlist, path regex, `..` rejection,
   auth-path blocklist, audit-always. All five must remain.
4. Bearer token comparisons use `subtle.ConstantTimeCompare` in
   `cmd/arbox/http_auth.go`. Keep it constant-time — don't refactor to
   `==`.
5. `.env` must stay in `.gitignore`. If you add a new secrets file,
   gitignore it **and** add a `*.example` template with placeholder
   values.

## Reporting bugs

Open a GitHub issue with:

- The `arbox --version` output
- Which host you're running on (Mac / Linux / systemd / Docker / VPS)
- A short log excerpt (redact any Bearer tokens first)
- The reproducer steps

If you've found a security issue — please email rather than opening a
public issue.

## Code style

- Idiomatic Go. Small packages. Explicit errors, no panics in the
  request/tick path.
- No time-of-day dependencies in tests. Pass `now time.Time` into the
  function under test; never call `time.Now()` in an assertion.
- Comments explain *why*, not *what*. File/function headers for
  behavioural context (e.g. "Cloudflare in front of Arbox blocks
  plausible-Go UAs with HTTP 403; impersonate a desktop Safari").

## License

This project is released under the MIT license; see `LICENSE`. By
contributing you agree your changes are licensed the same way.
