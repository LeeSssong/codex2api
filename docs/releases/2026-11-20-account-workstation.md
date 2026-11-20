# 2026-11-20 account workstation release

Owner: this chat. Branch: `grok/account-workstation`.
Application source: `9b8488f49ef2cc4d516fe35c861b81c2e4f25ca7`.
Source tree: `62d846c174a3e9783465a923d95dff08537e82ef`.
Upstream ancestor: `4514623dda4ae025fde5af439345c1c0ba641a7c`.
Release-tool follow-up on main: `fc9c2207f7c65de6b3b5d36b82009d0f2962aed1`.

The user authorized all four workstation steps and a fast Codex2API deploy on netcup `159.195.12.230` while the service was not serving external traffic.

## What shipped

Existing accounts keep the installation identity already shown upstream. The startup record gathers fingerprint mode, timezone, and dedicated egress into `credentials.workstation`. New Codex accounts use single-machine multi-window when the configured default is empty or off; an explicit stored off stays passthrough. Workstation accounts are not scheduled or dialed through host-direct, group, global, or pool egress unless they have their own proxy, enabled native IPv6, or fixed egress. A stored workstation that disagrees with the outbound request is held and is not sent.

## Release facts

- Live baseline before switch: container `8ebad546aa1ea7f3e2849058f38e600b5cbf8a9fee06c0b6b15cd40e735234aa`, image `sha256:51296310eb06152268bddfd1e19813981c9f1a474e52e730f9a95be2b4762afb`, binary `71094e059d2432ffe1c76219042d449a3919d81990b87379e4aadc05180fc163`, source `7280c31ca6698ef95518c27e541789ca07312ced`.
- New binary SHA256: `96d9244e8cb2fa3e2cf52a526fdd84bf4513adbb63029232a148abd9081f05a2`.
- New image: `sha256:c6c46673cfae4e20c2931e7001dee8917a8de5d17dd6002cebc92c51a68e5633`.
- Running container after recovery: `b72cadafbdb3ebe295cdc10b99998c6057ee2baab65af0c9be18445c4614d0e0`.
- Rollback image captured from the previous executable: `sha256:66c6c44bebed74ad4d9423f1f438387d91d042e800bf1922dec49e5f196591e9`.
- Maintenance began 2026-11-20 23:58:11.386 +08:00. The candidate started at 23:58:21.332 +08:00. Public health returned at 2026-11-21 00:00:41 +08:00. Window: 149.613 seconds.
- Disposable database rehearsal passed. Stopped backup is `/opt/codex2api/backups/workstation-20261120-9b8488f4/stopped.dump`. Retained configuration fingerprints and intelligence history `[293, 293]` matched after startup. Intelligence stayed enabled, interval 1800 seconds, revision 17. Credential worker heartbeat was online.
- Every non-deleted account was already absent. The database has 85 deleted account rows. No account was enabled.
- Sub2API, PostgreSQL, Redis, Caddy, and the other recorded containers kept the same ID and start time.

The first smoke failed because the quota check required a non-deleted account. Automatic rollback refused to restore the stopped dump after the candidate writer had started, which is the intended protection. The same verified image was started again, the original Codex route was restored, and the maintenance reject rule was removed. The empty-account smoke allowance is the follow-up commit and was not in the running binary.

Do not restore `stopped.dump` over later writes. Do not redeploy `7280c31c`. The authoritative source record is `/opt/codex2api/source-release.json`.
