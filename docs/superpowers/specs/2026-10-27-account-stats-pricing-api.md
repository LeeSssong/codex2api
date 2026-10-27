# Account statistical pricing, local candidate

This implementation follows Sub2API commit `a6d32edec667271d38c03ef55ac4a2edf77dfc5d` for OpenAI/Codex account statistics. It does not migrate customer billing or deploy a service.

## Configuration API

Authenticated admin routes `GET /accounts/:id/stats-pricing` and `PUT /accounts/:id/stats-pricing` read/replace one account's statistical configuration. The configuration is persisted in the existing encrypted credentials store, loaded at startup, and applied by the common proxy usage sink. Empty `account_rate_multiplier` means 1; explicit 0 is valid. Rules do not alter native customer charges, API key quotas, or scope budgets.

Example (token prices are USD per million tokens; image/request prices are USD per unit):

```json
{
  "account_rate_multiplier": 2,
  "source_group_id": 12,
  "apply_sale_pricing": false,
  "long_context_enabled": true,
  "rules": [
    {
      "account_ids": [42],
      "group_ids": [12],
      "pricing": [
        {
          "models": ["gpt-5.4"],
          "input": 2.5,
          "output": 15,
          "cache_read": 0.25,
          "cache_write": 2.5,
          "reasoning_multipliers": {"high": 1.5},
          "intervals": [
            {"min_tokens": 272000, "input": 5, "output": 22.5, "cache_read": 0.5, "cache_write": 5}
          ]
        },
        {"models": ["gpt-image-*"], "image_tiers": {"1K": 0.01, "2K": 0.03, "4K": 0}}
      ]
    }
  ],
  "upstream_prices": [
    {
      "models": ["gpt-5.4"],
      "input": 2.5, "output": 15, "cache_read": 0.25, "cache_write": 2.5,
      "long_context_threshold": 272000, "long_input_multiplier": 2, "long_output_multiplier": 1.5
    }
  ]
}
```

Rules are evaluated in order. Account IDs OR the single configured `source_group_id` select an outer rule. Empty selectors mean this account; no source group is inferred from native memberships. Within an outer rule, exact model matches precede trailing-wildcard matches. A flat rule is shorthand for an outer rule containing one price. `source_group_id` is an explicitly configured statistical context, not automatic migration of request-specific source routing.

Custom token rules use original exclusive ordinary input/cache read/cache write buckets and separate image output. Intervals are `(min_tokens,max_tokens]` and include input/cache/output totals. Unspecified interval prices are zero, matching the source. Token/request sums <=0 fall through; explicit image tier 0 matches and stays final. Image tiers use maximum edge <=1024 / <=2048 / larger for 1K / 2K / 4K; empty/auto has no matching custom image tier. `image_input`, `image_output`, and `image_cache_read` are available in catalog prices. Catalog calculation splits image output from total output; source custom rules have their own additive image-output term.

Priority: custom statistical rule, configured sale price when `apply_sale_pricing`, independent upstream model catalog, observed sale fallback. Image tier totals are final and skip the account multiplier. Known OpenAI defaults are pinned from the source rather than native sale overrides. Source GPT-6 defaults include long-context ladders; GPT-5.4 fallback has none without a configured catalog ladder. Catalog threshold is strict `>`; GPT-6.1 Sol bypasses that ladder. Custom intervals remain independent. Statistical fees round to ten decimal places, matching source storage scale. Unknown-token rows clear standard/statistical costs after resolution but preserve a final custom request/image price, matching the source quirk.

## Persistence and compatibility

`usage_logs` adds nullable `account_cost`, `account_stats_cost`, `standard_cost`, `account_rate_multiplier`, `quota_usage_admitted`, `quota_tokens`; source text and pricing JSON are additive. Historical rows remain untouched. `AccountCostSQL(alias)` resolves `COALESCE(account_cost, COALESCE(account_stats_cost,standard_cost,account_billed) * COALESCE(account_rate_multiplier,1))`; the last argument is an explicitly approximate legacy fallback. `StandardCostSQL(alias)` resolves `COALESCE(standard_cost,account_billed)`.

Admitted events also enter `account_usage_cost_events`, independently of diagnostic log mode. Each event stores only account/model/time, original numeric pricing snapshot, token count, fee snapshots, and native user charge. It excludes client/request/prompt/response identities. UUIDs are generated once per log submission and survive internal buffer retries; caller submissions are not globally deduplicated. This ledger and existing counters commit atomically. New quota queries use the ledger plus historical logs with `quota_usage_admitted IS NULL`, so retained full logs are not double counted. No destructive backfill or historical repricing is performed.

`standard_cost` records configured native sale cost on observed original inputs; `user_billed` remains actual native customer charge. Those are distinct from source account fee. This change does not reproduce every source customer/channel/group tariff or its eight-decimal customer debit policy. Account fee equivalence requires matching original quantities, statistical configuration, and model catalog. Native non-Codex channels keep native fees. Exotic provider policies, automatic source catalog import, request-specific source group routing, timezone/peak tariffs for other providers, and arbitrary source channel configuration are outside this OpenAI scope and are not claimed equivalent.
