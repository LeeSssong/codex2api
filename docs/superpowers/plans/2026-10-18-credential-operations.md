# Credential Operations Implementation Plan

> **For agentic workers:** Use subagent-driven-development for the backend task and inline execution for the frontend/runtime integration; maintain one implementation subagent at a time. The user approved the design and explicitly requires 2FA login on first import.

**Goal:** Port the complete OpenAI account credential operations flow into Codex2API, including first login/import, re-login, inspection, 2FA recovery and dual OAuth grants.
**Architecture:** Use native admin/auth/database adapters, independent encrypted credential storage and durable jobs. Reuse the old native port only as a tested adapter, and current Sub2API source for newer protocol behavior. The React UI and runtime consume an explicit API contract below.
**Tech Stack:** Go 1.26.9, SQLite/PostgreSQL, React 19, TypeScript, Python/Node protocol Worker.
**Spec:** `docs/superpowers/specs/2026-10-18-credential-operations-migration-design.md`

## Global Constraints

- Source checkout is read only: `/Users/gongtengxinwen/Documents/sub2api搭建/upstream/sub2api`, source SHA `e46b0b00b00b37ab74d95f9d800cc6396d83f672`.
- Native adapter reference: `git show 9d187930:<path>`; do not restore unrelated smart operations/plugins/token guard dispatchers.
- New tables use `credential_operations_*` after retirement migrations. Support both SQLite and PostgreSQL.
- First import supports email/password/optional long-lived TOTP and durable task execution; successful import enrolls encrypted login config plus monitoring atomically with native identity handling.
- Dedicated random AES-GCM key, atomic private key file initialization, refuse replacement when encrypted data exists. No secrets in ordinary responses/logs.
- Default probe/retry/threshold/cooldown: 1800 seconds / 300 seconds / 2 / 1800 seconds. Worker concurrency 1–16, default 3. Local Worker by default, no implicit remote fallback.
- Preserve fixed egress bindings, explicit direct choice, route generations, account credential generations, job lease/attempt and config revision fences.
- No production deployment or real account mutations in this implementation run.

## API Contract

Admin base `/api/admin`. Ordinary responses are direct JSON, errors include `error`. Keep original native adapter task fields `ID`, `AccountID`, `Status`, `Stage`, `Attempt`, `Error`, `CreatedAt`; newer task fields `OAuthProfile` and `Kind` may extend them. Never serialize raw encrypted task inputs.

- `GET /credential-ops`: `{enabled,accounts,tasks,rules,login_configured,session_studio_configured,runtime_settings,worker,encryption}`. Account row: `account_id,name,email,enabled,auto_relogin,probe_state,probe_detail,fail_streak,last_probe_at,last_reauth_at,next_probe_at,cooldown_until,blocked_reason,config`. `config` is redacted `{account_id,email,mode,engine,proxy_source,proxy_id,password_configured,totp_configured,otp_url_configured,revision}`.
- `GET /credential-ops/encryption`, `POST /credential-ops/encryption/initialize` return `{configured,source}`.
- `POST /credential-ops/import`: one entry `{name,email,mode,engine,proxy_source,proxy_id,password,totp_secret,otp_url,group_ids}` returns task. Accept repeated first-import entries without duplicate identity; persist input before returning.
- `GET|PUT /accounts/:id/credential-ops/config`: request `{email,mode,engine,proxy_source,proxy_id,password,totp_secret,otp_url,clear_password,clear_totp,clear_otp_url,revision}`; blank secret preserves saved value.
- `POST /accounts/:id/credential-ops/login`: same config fields optional when saved config exists; optional `oauth_profile: codex|excel`; returns active/new task.
- `GET|DELETE /credential-ops/login/:job_id`: task status / cancellation.
- `PUT /credential-ops/rules`: `{interval_seconds,retry_seconds,failure_threshold,cooldown_seconds}`.
- `PUT /credential-ops/runtime`: `{engine,worker_concurrency,session_studio_endpoint,session_studio_headers}`. Empty secret headers preserve existing values; overview returns only configuration presence.
- `PUT /accounts/:id/credential-ops/monitor`: `{enabled,auto_relogin}`. `DELETE` removes monitoring. `POST /accounts/:id/credential-ops/probe` performs immediate inspection.
- `GET|POST /accounts/:id/credential-ops/totp`: status / create rotation. `POST .../totp/verify`: `{action:verify_new|verify_old}`. `POST .../totp/export`: `{source:current|candidate|previous,include_password:false}` returns explicit secret data only to authenticated admin, no-store.
- Internal Worker base `/api/internal/credential-ops`, authenticated with `X-Codex2API-Credential-Worker`. Core adapter may retain `{task,login}` claims plus `/login/:job_id/renew|complete` with attempt fences. New TOTP/Excel capability endpoints must match the shipped Worker; document contract in runtime README and tests. Never accept an old Worker for unsupported tasks.

## Task 1: Native credential service, persistence, admin and Worker

**Ownership:** implementation subagent owns `credentialops/`, `database/credential_operations*.go`, `admin/credential_operations*.go`, `tools/credential-runtime/`, bootstrap changes in `main.go`/`admin/handler.go`/database migration callers. It may add focused tests and source licensing records. Do not edit frontend or proxy BPS consumers.

**Reference:** old `credentialops/{config,crypto,types}.go`, `database/credential_ops*.go`, `admin/credential_ops*.go`, `tools/credential-runtime/*`; current source `openai_oauth_reauth*`, `account_token_guard_v2*`, `openai_totp_rotation*`, `openai_excel_oauth*`, `internal/reauthruntime` and `tools/openai_*`.

- [ ] Write the first-import persistence/security test before implementation. Create fake OAuth identity tokens through existing test helpers, queue account_id=0, claim, complete and assert one native account, encrypted login config and enabled monitor; duplicate import resolves same identity. Confirm ordinary task/overview serialization omits password and TOTP.

```go
func TestCredentialOperationsInitialImportPersistsAndEnrolls(t *testing.T) {
    db := setupTestDB(t)
    ctx := context.Background()
    if err := db.EnsureCredentialOperationsSchema(ctx); err != nil { t.Fatal(err) }
    // Use the real queue/claim/complete methods and generated test JWT identity;
    // assert the account, encrypted config and monitor after the commit.
}
```

- [ ] Run focused `go test ./database ./admin ./credentialops -run CredentialOperations -count=1`; record expected missing-feature failure, then implement using native test helpers and real SQLite transactions.
- [ ] Port/adapt native queue, leases, config encryption, account identity transaction and scheduler. Replace plugin dependencies with module runtime controls. Add a database unique constraint covering per-account active login/rotation, config revisions and account generations. New initialization runs after retirement on both backends.
- [ ] Implement first import and saved-config relogin endpoints per API contract, group selection, monitor lifecycle, rules, runtime and encryption setup. Reconcile stale probe results after pause/edit and prevent transient failures triggering login. Add global engine, concurrency, worker heartbeat/capabilities and all overview fields from approved design.
- [ ] Port current TOTP recovery state machine, private journal, admin export, recovery verification and capability gating; all activation phases retain local and DB persistence order. Persist uncertain state across worker/app failure.
- [ ] Add separate Codex/Excel profile grants and queue/cooldown support. Expose database methods to retrieve/refresh/invalidate Excel grant with generation CAS; inform root of the exact Go interface needed by BPS consumers. Do not overwrite Codex tokens or claim grant acquisition without protocol tests.
- [ ] Ship independent current Worker engines, pinned dependency manifest/licenses, owned runtime supervisor and Docker build, with configuration/concurrency sync, egress snapshots and cancellation/lease renewal. Adapt current source scripts with fault-injection tests for TOTP and Excel.
- [ ] Run direct Go and Worker tests; inspect diff for source consistency, sensitive serialization, uniqueness races and re-start migration behavior. Commit only owned files. Write report with RED/GREEN commands, endpoint differences, unverified live external operations, commits and exact dependency/build requirements.

## Task 2: React operations center and first-login import

**Ownership:** root owns `frontend/src/lib/credentialOps.ts`, `frontend/src/lib/credentialImport.ts`, their relevant tests, `frontend/src/components/TwoFAImport.tsx`, `frontend/src/components/CredentialConfigEditor.tsx`, `frontend/src/components/CredentialTOTPDialog.tsx`, `frontend/src/pages/CredentialOps.tsx`, `App.tsx`, `Layout.tsx`, `Accounts.tsx`, locale and navigation helpers.

- [ ] Write actual parser tests first; run `node --experimental-strip-types --test src/lib/credentialImport.test.mjs` and observe missing parser failure.

```js
test('preserves an optional empty TOTP field and rejects over 100 entries', () => {
  assert.deepEqual(parseCredentialImport('demo@example.invalid----a password----'),
    [{ email: 'demo@example.invalid', password: 'a password', totp_secret: '' }]);
  assert.throws(() => parseCredentialImport(Array(101).fill('demo@example.invalid----pw----').join('\n')));
});
```

- [ ] Implement native typed API wrapper and robust text/CSV/JSON parsing, duplicate entry validation, limits and redaction. Do not store secrets in localStorage or widget state.
- [ ] Build shared first-login component used by `/credential-ops` and the ordinary “添加账号” dialog as a distinct password/2FA method. Provide single/batch input, persistent job progress, cancel and retry without repeat login after successful commit; auto-clear secrets after queueing and late-response protection after close.
- [ ] Implement selected operations-center design: accounts/tasks/rules/runtime, existing-account enrollment, config editor, filtering, manual/batch relogin, inspect/pause/resume/remove, encryption init and Worker state. Use native UI components, themes, loading/error states and translations.
- [ ] Build TOTP dialog with rotation confirmation, current/candidate/previous export, optional password, QR, JSON/combination downloads, verify-only recovery and secret clearing. Status does not fetch export automatically.
- [ ] Wire App/sidebar/account row shortcuts without changing unrelated account behavior. Run `npm run typecheck`, relevant unit tests and `npm run build`. Record desktop/mobile browser screenshots and interaction checks against mock API, then real local server if available.

## Task 3: Native BPS/egress integration and whole-feature acceptance

**Ownership:** root owns `proxy/`, `auth/` integration and additional narrowly scoped native fixes; coordinate any database interface amendments with task 1 owner.

- [ ] Write failing dual-grant routing tests before changing `proxy/basispoints.go`: normal Codex keeps original token, BPS uses independent Excel grant, Excel 401 invalidates only matching grant and does not poison Codex account; Excel-only accounts cannot enter native Codex path.
- [ ] Integrate Excel grant load/refresh/CAS invalidation and auto-acquisition using the task-1 API. Cover missing grant, cancelled task, cooldown and login-profile identity rejection.
- [ ] Verify fixed egress resolution and route generation checks in import/login/probe/Excel/TOTP paths. Source Mihomo pool adapter uses an independent manager with explicit settings; no sharing of source leases/services. Ensure source-supported proxy choices have real consumers and cancellation release.
- [ ] Run independent scoped code review of auth/persistence/Worker/interface changes; fix actionable findings and re-run affected tests. Update spec-parity matrix with code links and evidence for every approved behavior, explicitly separating mock checks from live upstream tests.
- [ ] Run appropriate final Go tests, frontend typecheck/tests/build and Worker tests. Persist completion/limitations in one feature handoff document and README use instructions. Keep the implementation available on `codex/credential-operations`; do not deploy without an explicit release instruction.

## Plan review

| Shared surface | Producer → consumer | Ruling |
|---|---|---|
| API contract | Task 1 → Task 2 | Use contract above; extensions stay additive, coordinate through messages. |
| Excel grants | Task 1 → Task 3 | Root waits for exact DB method signatures before integrating BPS. |
| Main bootstrap | Task 1 → all | Backend owner wires service after native migration, owns shutdown ordering. |
| First import | Task 1 commit → Task 2 UI | Durable server task and atomic enrollment are required; UI success alone cannot satisfy. |
| Task 1 internal consistency | Tests → implementation | Fake identities and local protocol mocks only; no real account changes. |
| Task 2 internal consistency | Parser/API tests → UI | Use the same component in first add and operations; no parallel login storage. |
| Task 3 internal consistency | Consumer tests → source parity | Do not mark source parity from unconsumed fields or unavailable controls. |

Ruling: Root implements frontend while a single backend implementation agent works in owned files; no two implementation subagents run concurrently. API contract freezes cross-task interfaces, root integrates sequentially after backend report.
