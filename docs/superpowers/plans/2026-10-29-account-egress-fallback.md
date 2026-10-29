# Account bound egress with direct fallback

Owner: current Codex chat. Exclusive workspace: `/Users/gongtengxinwen/.codex/worktrees/account-egress-fallback/codex2api`.
Branch: `codex/account-egress-fallback`. Base: `7b28052976f9a1dc5f00fc1da3f77416d093bf55`.

User-approved design: all account-bearing outbound traffic prefers the account's
bound HTTP/SOCKS proxy, fixed resource or native IPv6. The user's 2026-10-29
correction explicitly permits direct fallback when the binding is unavailable.
No global/pool/Resin substitute on fallback. Never replay a possibly submitted
OAuth refresh, generation request, or stream. The original implementation
handoff was local-only; the user subsequently authorized deployment on
2026-10-29. That later status supersedes the original restriction. Deployment
evidence and current production status are recorded in
`docs/releases/2026-10-29-account-egress-fallback.md`.

- [x] Shared request audit and safe fallback transport, with failing-first tests.
- [x] HTTP/business/maintenance/telemetry route precedence and fallback.
- [x] Websocket route precedence, connection isolation and pre-send fallback.
- [x] OAuth and associated credential worker route selection/fallback.
- [x] Intelligence runs record actual route/source/fallback, preserve old history.
- [x] Regression and race suites, build, self review and release handoff.

Acceptance: a working bound route wins over inherited or explicit auxiliary
routes. Unavailable bound routes try host direct with environment proxies off.
Upstream HTTP errors, cancellation, post-write failures and started streams do
not cause transport replay. Pool keys include account/route identity; a direct
fallback connection is not inserted into a bound websocket pool. IPv6 observes
actual source. New detection output uses the observed route; old rows are not
rewritten. Scheduled detector settings and account state remain untouched.

Verification (2026-10-29): `go test ./... -p 1 -count=1 -timeout=8m` passed all
packages; `go test -race ./internal/egressroute ./proxy/wsrelay -count=2`
passed; `go build ./...` and `git diff --check` passed. Parallel `go test ./...`
hit an unrelated flaky credentialops child-process environment test once; that
package passed in the serial full suite and the test passed five isolated runs.
No front-end files changed. The `frontend/dist/placeholder` used to satisfy Go
embed during local build is ignored build scaffolding, not source.

Review caught and fixed: OAuth fallback-client pool identity, unavailable OAuth
bindings still preferring the disabled URL, websocket direct-fallback reuse,
and worker Mihomo allocation before account binding selection. Websocket
fallback remains pre-handshake only. HTTP fallback does not retry after request
headers are written or after streaming starts. Historical intelligence rows are
unchanged. Work remains local and uncommitted on this task branch; no push,
merge, deploy, production access, or unrelated service operation occurred.
