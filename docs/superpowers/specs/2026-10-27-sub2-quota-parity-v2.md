# Sub2API quota semantics parity — authorized local implementation

User approved implementation, explicitly NO DEPLOYMENT. Do not push main, run deployment tools, change server configuration or run production probes. Base is freshly fetched origin/main 9a24ddf4. Original shared checkout remains protected. Each writer uses a separate worktree; coordinator integrates explicit commits.

Reference: locally available Sub2API production commit a6d32edec667271d38c03ef55ac4a2edf77dfc5d under /Users/gongtengxinwen/Documents/sub2api搭建. Use git show for exact source. Relevant files: backend/internal/service/account_stats_pricing.go, openai_gateway_usage.go, gateway_usage_billing.go, account_usage_service.go; backend/internal/repository/usage_log_repo_stats.go; frontend/src/components/account/{AccountUsageCell,UsageProgressBar}.vue, views/admin/AccountsView.vue, utils/accountUsageRefresh.ts (all prefixed upstream/sub2api/).

## Accounting contract

Per-row account cost: COALESCE(account_cost, COALESCE(account_stats_cost,total_cost) * COALESCE(account_rate_multiplier,1)). An explicit zero is valid; nil is missing. Final image-tier account_cost already includes its applicable price and must not receive account multiplier again. Standard cost and user cost remain independently recorded. Use original upstream token categories/model/service tier for account stats; retain native user quota/charge semantics when changing account statistics. Freeze cost inputs at request logging time; do not rewrite historical rows from today's price table.

Match source admission of billable usage, failure/cancellation/retry/internal behavior, and summary inclusion; don't remove filters blindly or count diagnostic-only rows as source usage events. Need traced source evidence and shared conformance fixtures for ordinary/cached/tier/long context/image/zero/multiplier/partial/retry/cancel/internal/Spark. Legacy values remain explicitly marked when original inputs unavailable; no automatic unverifiable backfill.

## Window and projection contract

Keep quota_5h/quota_7d shape. Align with source: active future reset minus fixed5h/7d; missing/expired reset uses rolling5h/7d start. Expired percent becomes0. Use account cost in prediction cost*100/percent. Only finite positive cost and percent produce estimate; no observation timestamp/10minute freshness/low-percent gate. No account-generation/Spark/status filter beyond the source-equivalent admitted usage ledger. 5h prediction is the sole intentional addition to source7d prediction. Existing non-Codex/montly behavior must be explicitly scoped rather than mislabeled as source equality.

## Frontend contract

Use one coherent successful usage response for percent, fees, prediction and reset. Keep last data while refreshing, on transport failure, and while live metadata indicates a new window pending successful fetch. Replace all fields atomically on successful response, including valid empty/new-cycle data; never pair prior cost with a new live percent. Remove 10minute expiry and low-sample hiding. Source5minute cache TTL triggers refetch, not display invalidation. Source account-refresh-key changes force background usage fetch; manual refresh bypasses cache; batch/dedupe and version tokens prevent stale overwrites. Desktop batch and mobile visibility behavior use source cadence. Remove independent30second barrier for required quota refresh; retain unrelated page polling when not part of quota. No persisted sensitive account data in browser storage. Only true first missing data gets placeholder; completed null/error-entry behavior follows source exactly.

## Ownership and validation

Frontend worker owns frontend/. Accounting worker owns billing/usage persistence/auth/proxy mapping, but not database/account_quota_windows_usage.go or admin/account_quota_windows_projection.go/page_stats. Coordinator owns those window/projection/query adapters and docs. Read-only source auditor supplies actual event-admission rules and pricing fixture evidence. Independent review on integrated diff, targeted Go/DB plus frontend tests/build/browser and saved parity matrix. End state committed local candidate only; report migrations/legacy limitations and no deployment plainly.
