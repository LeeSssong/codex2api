# Codex2API credential runtime

The dedicated worker invokes password/TOTP using toSub2, email OTP using Turb,
or an explicitly configured Session Studio HTTPS endpoint. Exact source commits
are in `dependencies.json`. Build the Dockerfile with verified named contexts
`tosub2` and `turb`; retain their MIT licenses and `SOURCE-LICENSE` for the adapted
Sub2API protocol adapter. The worker has no Sub2API network or credential dependency.

Application-only protected environment:

- `CODEX2API_CREDENTIAL_OPS_KEY`: independently generated, at least 32 characters.
  Passwords, TOTP secrets and mailbox URLs fail closed if the key is unavailable.
- `CODEX2API_CREDENTIAL_OPS_WORKER_TOKEN`: independently generated, at least 32
  characters; shared only with this application's dedicated worker.
- Optional `CODEX2API_CREDENTIAL_OPS_SESSION_STUDIO_ENDPOINT`: explicit HTTPS
  endpoint. No destination is inferred. Optional
  `CODEX2API_CREDENTIAL_OPS_SESSION_STUDIO_HEADERS`: protected JSON string map.

Worker-only protected environment:

- `CODEX2API_CREDENTIAL_OPS_WORKER_TOKEN`.
- `CODEX2API_CREDENTIAL_OPS_URL`, default
  `http://codex2api:18080/api/internal/credential-ops`.
- Optional `CODEX2API_CREDENTIAL_OPS_WORKER_ID`, unique per running replica.
- Optional `CODEX2API_CREDENTIAL_OPS_TRUSTED_OTP_HOSTS`, explicit private mailbox
  host allowlist. Public HTTPS mailboxes require no allowlist.

Keep application secrets in `/opt/codex2api/secrets/credential-app.env` and worker
secrets in `/opt/codex2api/secrets/credential-worker.env`, readable by their owner
only. The worker never receives the application encryption key. Its protocol
subprocesses receive neither the worker token nor the application key. Run as
`codex2api-credential-runtime` on the private application network, expose no
ports, use read-only root storage with writable `/tmp`, and apply resource caps.

`python /app/worker.py --check` verifies both pinned engine imports and Node
availability. `--healthcheck` checks a heartbeat younger than 65 seconds. The
supervisor renews a two-minute lease every 25 seconds and stops the whole protocol
process group on cancellation, lost lease, plugin disable, shutdown or a 25-minute
deadline. Claim attempt and native credential generation fence every callback.
Task completion, identity deduplication and native account writes share a DB
transaction; native runtime pool publication follows the committed write.

Admin bootstrap hooks: call `db.EnsureCredentialOpsSchema(ctx)` after native DB
migrations, `handler.RegisterCredentialOpsRoutes(adminGroup)` for admin routes,
`handler.RegisterCredentialOpsWorkerRoutes(router)` outside admin authentication,
and `handler.StartCredentialOpsScheduler(ctx)` using the application lifecycle
context. On shutdown, cancel that context and call
`handler.WaitCredentialOpsScheduler(shutdownCtx)` before closing the DB.
The scheduler uses native usage probes and queues login only after
confirmed authorization failures reach the configured threshold. Transient
network failures do not increment the authorization failure streak.

Local verification:

```sh
go test ./admin ./database ./credentialops -run CredentialOps -count=1
CODEX2API_TEST_POSTGRES_DSN=postgres://postgres@127.0.0.1:15439/credentials_test?sslmode=disable go test ./database -run CredentialOpsPostgres -count=1
python -m unittest discover -s tools/credential-runtime -p 'test_*.py' -v
```

These tests use synthetic credentials and isolated databases. They do not claim
a live OpenAI login, a configured Session Studio service, or mailbox delivery.
