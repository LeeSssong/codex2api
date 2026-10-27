# Source audit checkpoint

Exact source: a6d32edec667271d38c03ef55ac4a2edf77dfc5d, repository /Users/gongtengxinwen/Documents/sub2api搭建; all source paths below relative to upstream/sub2api. Read with git show (working checkout HEAD differs).

## Confirmed admission
- backend/internal/handler/openai_gateway_handler.go:1165-1166: HTTP Responses error with partial result is submitted as usage; failover errors always result=nil. submitResponsesUsage:1167-1210 ignores nil, otherwise invokes RecordUsage.
- backend/internal/service/openai_gateway_usage.go:148-156: RegionalMetered goes to its separate ledger; UsageUnavailable is rejected (no token row/no billing). :205-223 distinguishes complete/partial/unknown. :614-625 writes unknown as zero financial cost, not invented price.
- Normal usage AccountID remains selected account.ID (:484), including shadow account; resolve parent credential at235-242 is only billingAccount for policy. Therefore Spark belongs to shadow ID and cannot be added to parent merely by removing Spark predicate.
- backend/internal/repository/usage_log_repo_stats.go:585-596: admitted ledger rows summed by account and start only, no HTTP status/internal/retry/model/generation predicate. Native Codex2API diagnostic/retry log rows are a broader ledger, so native summary needs an explicit source-equivalent admission mapping.
- Separate account_probe_cost_logs aggregation exists in usage_log_repo_stats.go:34-46. Must trace diagnostic callers before deciding native internal_reason eligibility.

## Confirmed costs
- backend/internal/service/account_stats_pricing.go:21-25 priority: matching custom account/group stats rule; channel ApplyPricingToAccountStats uses upstream total before user multiplier; upstream model-file price; fallback total_cost * account multiplier.
- applyAccountStatsCost:371-409 freezes upstream model fallback, service tier/reasoning and price at request time. final AccountCost overrides all fallback math; explicit zero remains valid. Custom image tier matching per-request price >=0 (:199-211) produces final cost WITHOUT account multiplier. Custom zero token price yields nil (:350-351), so only an explicit snapshot/image zero is guaranteed valid zero.
- Source SQL formula: COALESCE(account_cost, COALESCE(account_stats_cost,total_cost)*COALESCE(account_rate_multiplier,1)), usage_log_repo_stats.go:591.
- Source original input buckets are exclusive: upstream input=max(total input-cache read-cache creation,0), cache creation original retained (:245-271). User cache-as-input conversion cannot contaminate account stats (:593-612).
- Custom token rule interval total includes input+output+cache write+cache read (:320), not input only. Exact/wildcard matching platform scoped (:249-289); account or group rule IDs match OR (:229-246). Account stats long context only account upstream flag, not user group flag (:412-417).

More findings and fixtures pending below.

## Completed admission trace and native adapter contract

Source HTTP Responses main caller backend/internal/handler/openai_gateway_handler.go:1165-1230,1330-1360:
1. A selected upstream attempt producing no forward result creates no usage row. Pre-admission/transport/failover errors return or retry before submission. A native terminal HTTP error log is not automatically a source usage event.
2. A nonnil result is submitted after success and after partial/error completion. Explicit ClientDisconnect and canceled-client branches submit result at1216-1230. A 499 with observed upstream usage must be eligible; status499 is not an admission exclusion.
3. UsageUnavailable=true returns an error before persistence, service/openai_gateway_usage.go154-156. UsageIncomplete with known/positive observed usage is partial at216-223. Unknown result is audit-row-only, standard/user costs zero and billing dedup is not claimed (gateway_usage_billing.go451-458). This unknown result can still count one request in the source summary; there is no completeness filter in source stats SQL.
4. Partial observed usage is chargeable for observed quantity, marked reconciliation_required, usage_billing.go19-22,43-54. Arbitrary estimates from output character counts are not upstream observed usage.
5. Cyber rejection takes the explicit RecordCyberPolicyUsageLog->RecordUsage adapter with upstream failed-response token quantities, openai_gateway_usage.go79-113; caller guards normal and error paths from double submission. Cyber is not inherently excluded.
6. WS differs from HTTP: handler AfterTurn at3782-3799 drops turnErr unless ImageCount>0, with cyber separately recorded; otherwise normal result gets RecordUsage. Do not claim universal errored-WS text inclusion from HTTP evidence.
7. UsageLog AccountID is selected account.ID, even when shadow credentials resolve parent for billing policy (openai_gateway_usage.go235-242,481-485). Spark shadow rows therefore stay attached to the shadow account ID. No extra parent total is implied by source SQL.
8. Request-log uniqueness is (request_id,api_key_id) ON CONFLICT DO NOTHING, repository/usage_log_repo_insert.go403,1074. Attempt-specific log request ID uses AttemptID (openai_gateway_usage.go452-455; usage_billing.go166-172). Customer billing identity uses stable logical ID + immutable observed-usage fingerprint, NOT AttemptID (usage_billing.go111-163); unknown does not claim it. Distinct genuine upstream attempts may retain individual audit rows, while repeated delivery of one attempt must not create additional rows.

Native Codex2API baseline04b563c2:
- database/postgres.go4615-4703 UsageLogInput has status, is_retry_attempt, internal_reason, parent_request_id, upstream_request_id, tokens but no known/partial/estimated provenance. All diagnostics and attempts share usage_logs; this is broader than source ledger.
- proxy/handler.go1618-1724 common logging sink freezes only native image billing, then logs; error logs e.g.6535-6554 include terminal/pre-response failures not source rows.
- proxy/handler.go8064-8072 overwrites failed-stream usage with deltaCharCount/3 estimate. This MUST NOT be treated as source-observed chargeable usage. Need explicit caller provenance/completeness flag or preserve original upstream usage separately; token>0 is insufficient.
- database/account_quota_windows_usage.go currently filters499, retry, credential-generation, all internal reasons and Spark. Source-equivalent adaptation should admit upstream-result events with actual usage/result evidence (or an explicit known unknown-result audit flag), preserve partial and canceled known usage, exclude pure transport/admission diagnostic attempt rows, keep independent upstream child requests, and deduplicate identical attempt delivery. Generation filtering has no source quota equivalent after admitted ledger selection.
- A safe practical fallback for legacy native rows is explicitly labeled legacy: inference with observed-token evidence and no diagnostic origin; cannot reconstruct zero-token-success versus absent usage, or estimated tokens, perfectly from current columns. Never silently backfill exact source semantics.

### Internal reasons

Source AccountProbeCostRecorder explicitly has no user/API identity and never writes usage_logs (service/account_probe_cost_service.go12-14); manual, monitor, scheduled classified boundary service/account_test_service.go595-625 writes separate account_probe_cost_logs. Manual admin caller account_handler.go1616; scheduled runner169-180. Pelican intelligence/quality tests enter TestAccountConnection directly, pelican_test_service.go46-55, also not RecordUsage. Native connection_test, quality_test, grok_capability_probe are diagnostics and should stay excluded from the source usage window. This exclusion is supported by the caller boundary, not guessed from names.

Native overflow_compact_summary is a real child Responses inference with inherited API key and independent upstream call; proxy/internal_response.go40-96,118-132 preserves ParentRequestID for attribution but creates a new request context. ParentRequestID is not a reason to deduplicate child cost with parent. Native prompt_intelligence_cy_analysis explicitly selected API key execution (admin/prompt_intelligence_ai.go780; proxy/internal_response_api_key.go13-26) also performs a real gateway model inference. It should be admitted if matching actual gateway usage, not excluded just because internal_reason is nonempty. Legacy ExecuteInternalResponse without attributed API key remains an administrative ambiguous mapping; mark its native category explicitly, don't blanket infer from empty internal_reason/API key alone.

## Source window and zero/missing behavior

service/account_usage_service.go787-798 queries both five-hour and seven-day stats even if percent progress missing; successful empty stats creates progress utilization0 and explicit zero stats. buildCodexUsageProgressFromExtra1546-1607 returns nil if used-percent key absent; present zero is valid. It reads absolute reset; fallback reset_after_seconds uses codex_usage_updated_at if parseable, otherwise now (1585-1599). Expired reset (<=now) sets utilization0 at1602-1605. It retains reset timestamp. codexWindowStatsStart1610-1614: future reset minus fixed duration; missing/expired reset => now-duration. Five-hour duration5*time.Hour; seven-day7*24*time.Hour787/794.

Source prediction frontend AccountUsageCell.vue886-902 requires finite numeric utilization and current account cost, both>0, then cost*100/utilization; no10minute age or minimum-percent gate. Source prediction is seven-day only; additional five-hour estimate is intentional native feature.

## Exact numeric conformance fixtures (USD)

Unless stated otherwise source fallback model gpt-5.4, default tier, no custom channel stats rules, account multiplier1. Source base prices billing_service.go551-560 input2.5e-6, output15e-6, write2.5e-6, cached-read0.25e-6. Input below is upstream inclusive input, then source splits exclusive buckets at openai_gateway_usage.go245-271.

| Fixture | Input data | Expected account pre-multiplier / final |
|---|---|---|
| Ordinary | total input1000, output100 | .0040000000 / .0040000000 |
| Cached | total input1000 incl cache200, output100 | 800*2.5e-6+200*.25e-6+100*15e-6=.0035500000 |
| Cached multiplier2 | preceding row, accountRate2 | .0035500000 / .0071000000 |
| Priority | same cached token row, tierpriority (all component prices2x) | .0071000000 |
| Flex | same cached token row, tierflex default.5x | .0017750000 |
| Cachewrite original vs user conversion | total input1000 incl cache-read200/cache-write100, output100 | ordinary700*.0000025+read200*.00000025+write100*.0000025+out100*.000015=.0035500000; user cache-as-input cannot alter this original account calculation |
| Custom stats token rule | same exclusive700/100/100write/200read; rule USD/token input1e-6/out2e-6/write3e-6/read.5e-6; accountRate2 | (.0007+.0002+.0003+.0001)*2=.0026000000 |
| Custom image tier | billingModeimage, size2048x1152 (2K), count2, matching tier unit.03, accountRate9 | .0600000000 final (NOT.54) |
| Explicit zero final | account_cost=0, account_stats_cost5,total10,rate2 | 0 |
| Missing final | account_costNULL, account_stats_cost5,total10,rate2 | 10 |
| Missing stats too | account_costNULL,account_stats_costNULL,total10,rateNULL | 10 |
| Partial known cancel499 | partial observed inclusive input1000/cache200,out100 | same account.0035500000 eligible, reconciliation marker; source handler1216-1230 |
| Retry then success | failed transport/failovernil result then above success | one admitted row; requests1,tokens1100,cost.0035500000 |
| Independent child inference | parent ordinary row.004 and child cached row.00355 sameaccount | requests2,tokens2200,cost.0075500000; ParentRequestID isn't shared-attempt dedup |
| Diagnostic probe | connection/quality same observed quantities | source quota usage rows0/cost0; separate probe ledger may have its own cost |

Long-context fixture MUST specify the source model catalog snapshot: static gpt5.4 fallback itself has no ladder (billing_service.go551-560;1950-1951 says ladders catalog-driven). With official catalog threshold272000, input-side multiplier2/output1.5, account upstream long-context enabled:
- ordinary1000+cache-read300000 (inclusive301000), output1000 => .005+.15+.0225=.1775000000; accountRate2=>.3550000000.
- same +cache-write10000 => .005+.15+.05+.0225=.2275000000.
- strict threshold check total input272000 uses base;272001 uses whole-request long multiplier. Output does not count toward threshold. This source check differs from native database/billing.go>= threshold.
- Source tests billing_service_test.go281-297,365-389,415-432 assert these formulas; shouldApplySessionLongContextPricing2072-2083 counts ordinary+cachewrite+cacheread.
- GPT6.1Sol original upstream model exempts official ladder for OpenAI OAuth/setup/apikey, openai_gateway_usage.go721-750; custom group intervals unaffected.

## Price compatibility and precision

USD/MTok source fallback cards (billing_service.go551-702), order input/output/cachewrite/cacheread:
- gpt5.4:2.5/15/2.5/.25; gpt5.5:5/30/5/.5, priority2.5x.
- gpt5.6sol:5/30/6.25/.5; terra:2/12/2.5/.2; historical5.6luna:.2/1.2/.25/.02.
- gpt6astra:10/50/12.5/1;6.1sol:2/10/2.5/.1;6sol:2/10/2.5/.2; historical6luna:.1/.5/.125/.01.
- gpt5.3codex AND source Spark alias fallback1.5/12/1.5/.15 (native original different);gpt5.2:1.75/14/1.75/.175.
- listed cards priority2x except5.5; flex default.5x. Explicit model FastMultiplier/FlexMultiplier can override generic tier; serviceTierCostMultiplier127-173. Do not multiply explicit priority card twice (enforceOpenAIFastPricingRatio2045-2061).
- Account stats custom rules use plain rule rates and reasoning multiplier; they do not reuse user sale override/model tier/long pipeline. Rule interval basis includes output, while official long threshold excludes output. Model-file fallback uses CalculateCostUnified and request's service tier/pricing time/account long flag (account_stats_pricing120-144).
- Image size tier classifier image_billing_size.go28-61: invalid empty/auto;1K/2K/4K direct; max dimension<=1024=>1K,<=2048=>2K,otherwise4K. Output dimensions win and maximum output tier selected before input; missing all default2K (64-100).
- Custom rule matching exact models before trailing-* prefix wildcard with platform scope; rules first applicable match by accountID OR groupID. Native per-account configuration can faithfully implement supported subset but cannot claim imported channel/group matching without equivalent configured data.
- Custom token summedcost<=0 returnsnil (no zero override); flat per-request<=0 returnsnil; image interval >=0 accepts valid zero. A present AccountCost=0 ALWAYS wins SQL COALESCE.
- Source float64 calculations persist account_cost/account_stats_cost/total_cost numeric(20,10), schema usage_log.go119-149; migrations101/201. AccountRate snapshot decimal10,4. Stored scale10 rounding occurs per row before SUM; native REAL unrestricted totals can drift unless freezing row account cost to10 decimals before sum. PostgreSQL numeric rounds halves away from zero.
- User ActualCost is separately quantized8decimal with decimal.NewFromFloat.Round(8) (usage_billing175-218; gateway_usage_billing421-431). TotalCost and independent account amount deliberately untouched by this8decimal quantization. Example raw user.000078125=>.00007813; account amount.0000781250 remains at scale10. Do not apply8decimal rounding to account prediction cost.
- Historical rows without original model, tier, upstream cache categories, request-time prices cannot be recomputed reliably from today's price table. Freeze new rows and explicitly mark legacy provenance.

## Source caveat

Unknown zeroing openai_gateway_usage.go614-625 clears component/TotalCost/ActualCost/AccountStatsCost but not an already set AccountCost from applyAccountStatsCost. With positive custom per-request stats pricing, an unknown result may retain AccountCost. This is actual code behavior; don't silently state unknown is guaranteed final account0 for every custom pricing configuration. Normal zero-quantity model-file fallback produces0. We should preserve conservative no-invented-cost behavior in native adapter and document this edge rather than invent data.

No source checkout changes, server access, deployment, or push performed.
