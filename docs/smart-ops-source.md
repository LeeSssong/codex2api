# Smart Operations Port

Source: Sub2API commit `2a0948c4bb7f74e4a960cca60cec67db857fc843`.
Reference modules are `backend/internal/service/oauth_auto_config*.go`,
`priority_scheduling*.go`, `model_billing_config.go`, `pelican_group_tests.go`,
`pelican_scheduled.go`, and `pelican_showcase.go`. Source licensing is LGPL-3.0;
retain the project's existing source attribution and notices.

Codex2API owns the settings, native accounts, credential transactions, outbox,
quality records, proxy pool, scheduling leases, usage logs, and billing. No
Sub2API endpoint, database, worker, proxy selector, or credential store is used.

## Runtime Contracts

- New native OAuth identities and verified first 2FA imports apply defaults in
  their insertion transaction. Existing identities retain explicit overrides.
- Successful and failed native requests feed a bounded observer queue. Every
  progression step persists the counter and native concurrency change together.
  The native scheduler outbox publishes changes to other instances. Queue
  overflow pauses upgrades until a new configuration revision is saved.
- Priority scheduling reads cached native quality evidence, P90 first-token
  latency, native billed cost, and current load. Selection preserves native
  eligibility, exclusions, API-key scopes, egress, cooldown, and admission.
  Stateful bindings stay on their owner; fresh selections use the scorer.
- Pelican plans support cron in UTC or minute intervals, editing, pause, manual
  runs, cancellation, and retained history. Atomic claims and renewable leases
  fence replicas. Saved samples are reused after lease recovery. Sample requests
  use the native quality executor and native usage/billing pipeline. Output is
  delivered only through the existing sandbox preview.
- Model multipliers freeze once before native API-key scope counters and the
  usage writer. They change customer token fees, preserve upstream account cost,
  and do not compound or alter per-image/video billing.
- BPS defaults remain a reusable template and never automatically enable a new
  account. Active quality BPS policies consume model scope, unsupported-tool and
  encrypted-content omission, native session proxy pool selection, cache-creation
  classification, failure/group actions, and scheduled recovery.

## Native Transport Boundaries

Native Basispoints uses SSE. WS/SSE acceleration and the external Mihomo session
selector are unavailable in this native transport. Their enabled combinations
are rejected by the backend with a concrete reason; the UI marks them unavailable.
The supported session-proxy source is Codex2API's own `ip_pool`. These settings
do not modify the unrelated Codex WebSocket transport.
The template uses the native Basispoints catalog (`gpt-6-astra` and
`gpt-5.6-sol` by default). Explicit scopes outside the configured native catalog
are rejected; "all models" means every model supported by that catalog.

## Startup And Shutdown

Call `db.EnsureSmartOpsSchema(ctx)` before migrate-only exits. Start with
`handler.InitSmartOps(rootContext)`, register with
`handler.RegisterSmartOps(authenticatedAdminGroup)`, and cancel the root context
before `handler.WaitSmartOps()` and database shutdown.

The native BPS route-admission check uses
`smartOpsBPSBody(account, body)` before `basispoints.NativeCodexReason`, sharing
the executor's active policy transformation. Quality-plan creation copies the
BPS template only when a plan omitted its BPS policy, preserving explicit values.
