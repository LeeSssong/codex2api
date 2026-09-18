# State management

Automatic State capture is configured in **State management > Capture settings**.
The account badges, account filters, dashboard and State management use the same
backend snapshot. State matching checks identity, credential generation, exact
model, configured length, token structure and issue time plus one hour. A length
match is not a model capability check.

## Counts and switches

- Total accounts do not change when automatic reuse is toggled.
- Reuse accounts are distinct in-scope accounts with a valid State for at least
  one selected model. Valid combinations count account/model pairs separately.
- Available State accounts additionally satisfy account and model scheduling
  gates. Cooldown and disabled accounts retain saved coverage. Busy request slots
  do not change these counts; actual admission still enforces every slot limit.
- Model coverage is reported separately. One available model never implies full
  model coverage.
- Automatic reuse controls collection and injection. Without strict eligibility,
  a missing State follows the existing fallback. Strict eligibility requires a
  matching State at selection and again at dispatch. Strict eligibility with
  reuse disabled blocks native Codex OAuth requests and shows a configuration
  conflict. Other provider channels and relay accounts are unaffected.

Account and State links support `state=valid|available|missing` and `state_model=<exact-model>`.
Unknown exact model names are rejected by the backend; deselected known models
fall back to any selected model before pagination and bulk selection. Deselecting
a model does not delete its saved value. Configuration changes invalidate the
live revision, and successful browser mutations notify other open tabs.
State management shows distinct accounts with valid State and each selected
model's saved coverage separately from dispatch availability. The model choices
in capture settings show counts for the currently saved configuration.

## Renewal and budgets

The renewal window is configurable from 1 to 59 minutes. Existing installations
default to the previous 10-minute behavior, with staged concurrency off. No saved
switch or global/account concurrency is migrated to a new value.

Optional staged concurrency has an early capture budget, an urgent threshold and
capture budget, and a budget for first capture or expired State. For example, a
30-minute renewal window, 10-minute urgent threshold, and 1/3/10 capture budgets
can be combined with a 1-request urgent business budget. All capture budgets are
per account across selected models and subordinate to the global capture limit
(maximum 20) and the normal system account limit. The urgent business budget is
transient, also covers buffered session admission, never interrupts active
requests, and resets after recovery, disable, scope changes or shutdown. If any
selected model is urgent or missing, that account uses the corresponding stage.

IPv6, proxy-pool and mixed capture routes are available. Mixed mode rotates both
configured sources, falling back to the available source if the other has no
route. A 401/403/429 still pauses the account across all capture routes and models.
Only response headers are inspected; the request is cancelled before closing the
body. Retry diagnostics distinguish Retry-After, usage windows and local backoff.
Cooldown, exhausted usage windows, quota pause thresholds, disabled accounts,
authorization failures and model cooldowns also block capture. Availability is
rechecked immediately before the upstream request; active workers are cancelled
by the manager when their account becomes unavailable. Restricted models do not
increase capture budgets or reduce another model's business concurrency. Saved
valid values remain intact and collection resumes after restrictions clear.

Renewal keeps the old valid value. A replacement must expire later and have more
remaining lifetime than the renewal window. Successful persistence precedes
publication and peer cancellation. Configuration/identity changes and successful
imports fence late capture responses. Imports keep whichever valid matching value
expires later; duplicate or older packages do not overwrite or renew it.

## Verification and preview

`go test ./...`, `npm test`, `npm run typecheck`, `npm run build`, then
`go build -o .dev/codex2api-state-verified.exe .` validate the backend and embedded
frontend. Go State tests use synthetic tokens, temporary SQLite databases,
controllable time and synchronized mock upstreams.

The optional preview is entirely synthetic:

```powershell
# Terminal 1, from frontend/
node tests/state-management-fixture.mjs --serve
# Terminal 2, from frontend/
$env:VITE_API_TARGET='http://127.0.0.1:18129'
node node_modules/vite/bin/vite.js --host 127.0.0.1 --port 5179 --strictPort
# Terminal 3, from frontend/
node tests/state-management.browser.test.mjs
```

The browser test intercepts all API requests and blocks external requests. It
checks desktop/mobile, light/dark, reduced motion, filtered navigation, reload and
model deselection. Screenshots are written to `.dev/state-ui/`. Set
`STATE_BROWSER_PATH` to an installed Chromium executable when Playwright's bundled
browser is unavailable. The preview never reads production credentials or starts
real capture. No Docker image is published by these steps.
