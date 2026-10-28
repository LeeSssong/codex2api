# Dedicated host IPv6 egress

The **Proxies → Dedicated host IPv6 egress** panel assigns an existing public
IPv6 source address to each native Codex account. It is disabled by default.
Only accounts explicitly selecting Local IPv6 participate; other accounts retain
their selected proxy/direct route. Enrolled accounts fail closed when the pool
is disabled or exhausted. Resin remains a conflicting global egress and must be
disabled before enabling this feature.

The container must already be able to bind these addresses. On Linux, host
networking shares the host network namespace. The application does not create
addresses, change routes, require `NET_ADMIN`, or fall back to IPv4. An explicit
allowlist is recommended to reserve the host's primary address and addresses
used by other applications. An empty allowlist selects all visible public IPv6
addresses. Every replica sharing the database must see the same address pool.

Bindings, cooldowns and configuration live in the database. Atomic claims and
a unique address constraint prevent concurrent workers or blue/green instances
from assigning the same current address to different accounts. A delayed failure
from an old address cannot rotate a newer assignment again. Deleting an account
removes its current binding; disabling an account does not discard its binding.

The default cooldown is 600 seconds and the direct exchange attempts up to three
addresses for the selected account. Existing gateway account-selection/retry
policies remain separate. Temporary 429 responses and failures establishing a
connection rotate the source. Optional 500/502/503/504 handling also covers
explicit upstream error frames before output. Authentication failures, hard quota
exhaustion, invalid requests and policy rejections do not rotate addresses.
Neither a 5xx nor a rotation is a model-quality failure or proof of IP throttling.

HTTP, WebSocket, maintenance requests and OAuth use the account's source binding.
OAuth refresh is never replayed transparently. Connection pools remain isolated
by purpose and TLS profile: business, maintenance, subscription, OAuth and
Basispoints share an account source address but never a pooled connection. HTTP and WebSocket stream preflight
buffers only creation metadata, up to 64 KiB; it stops at the first output event.
Visible text, reasoning and tool events are never replayed by this feature.
Failures reporting token usage are also not replayed. Errors after output and
premature stream closure rotate the source for subsequent requests only. Existing active streams
keep their connection when another request rotates the account; new exchanges
use the new address and WebSocket continuation reuse respects the source route.

The panel reports pool availability, account/IP bindings, rotation counts and the
last rotation reason. Usage audit labels record the actual IPv6 source, and
rotation logs contain only account ID, old/new address and a fixed reason label.
An exhausted or unavailable pool fails closed until an address becomes available.

Admin API: `GET /api/admin/ipv6-egress`, `PUT /api/admin/ipv6-egress`.
Configuration writes require the current `revision`; stale writes return 409.
The enable switch saves only enablement, while address/cooldown/retry drafts use
the explicit save button. Automatic status refresh never clears a failed write.

For rollout, keep the feature disabled during migration and overlap, validate the
new instance, switch ingress, drain and stop the old instance, then enable a
verified address pool. Switch individual accounts to a real proxy or explicit direct IPv4 to leave the
pool. Disabling the global feature blocks enrolled accounts rather than silently
changing their source address. A version rollback must also disable it before
traffic is handed back to a version that does not implement dedicated IPv6.

## Account membership and persistent container attachment

Account selection is independent of `proxy_url`: `ipv6_egress_enabled` opts a
native Codex account into this manager. The read-only `ipv6_egress_ip` shows the
current binding; empty means not yet assigned. A real proxy or explicit blank
opts out. New native imports default to membership and `America/New_York` when
not explicitly specified; the import `default_timezone` control can disable that
fallback with an empty value. Existing accounts are not bulk-enrolled.

`deploy/release/ipv6_pool.py` and `codex2api-ipv6-pool.service` restore a dedicated
ipvlan L2 interface inside only the running Compose `codex2api` namespace. Install
the root-owned helper at `/opt/codex2api/ipv6_pool.py` and unit under systemd, with
root-owned `/opt/codex2api/ipv6-pool.json`:

```json
{"prefix":"2a0a:4cc0:101:799:c2a:28::/112","count":128,"parent":"eth0","interface":"c2a-v6","gateway":"fe80::1"}
```

The config reserves offsets 1..128 from the dedicated /112 (addresses retain the
uplink's /64); it excludes the host primary. The watcher restores attachment on
app recreation/restart or host reboot and serializes against explicit `ensure`
with its own namespace lock. It does not restart Docker, modify IPv4 routes,
change host routes, or attach interfaces to other containers. The existing
maintenance controller calls `ensure` and checks application discovery before
accepting a recreated app as ready, including on rollback. Unexpected interface,
parent, addresses or route state fails closed rather than overwriting it.

Before activating the global pool switch, verify real account inference and
membership routing. On rollback to a version without account membership,
**disable the pool first**, since older versions apply its global switch more
broadly. Preserve existing account credentials and current writes. To remove the
network helper, first disable IPv6 egress, stop/disable only its systemd unit,
then remove `c2a-v6` from the verified app namespace; do not remove the host uplink
or unrelated Docker networks.

## Manual account address selection

Both account editors can choose an available pool address after Local IPv6 has
been saved for that account. An unassigned account may also claim an address
manually. This action does not enable a disabled account. Editing route intent
must be saved first. The picker refreshes pool status; occupied or cooling
addresses cannot be selected. The server repeats these checks atomically, so a
stale picker cannot steal another account's address.

`POST /api/admin/accounts/:id/ipv6-egress` accepts
`{"ip":"<selected pool IPv6>","expected_ip":"<current IP or empty>"}` and
returns `{"ip":"<actual IPv6>"}`. The target must be locally visible, permitted
by the pool allowlist, free of another binding/lease and out of cooldown. An
expected binding mismatch, occupied target or cooling target returns 409. The
old address remains unavailable while old request leases exist; after the last
connection lease closes it receives the configured cooldown. Existing rotation
continues to handle eligible upstream failures.

## Abnormal shutdown lease recovery

HTTP response close and WebSocket close release durable connection leases.
Normal application shutdown closes WebSocket connections. A forced kill,
incomplete shutdown or database failure during release can leave a lease
behind. Leases do not automatically expire: doing so could assign an address to
a second account while the first still has an active stream. Orphans therefore
reduce available pool capacity until recovered; exhaustion fails closed.

Recovery requires the common release lock, a verified stopped state for every
application instance sharing this database, and a current backup. Keep active
account bindings, finish retired bindings and add cooldowns for orphan addresses
without active bindings before removing stale leases. Never clear leases while
an application instance is running, and never delete leases without completing
the retirement/cooldown work. The initial release creates an empty lease table
and requires no recovery.
