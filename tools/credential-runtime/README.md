# Codex2API credential runtime

This runtime is an independently versioned worker boundary. It polls Codex2API's authenticated private worker API, invokes the pinned toSub2 password/TOTP protocol, and submits only resulting OAuth credentials through the generation-fenced callback. It does not import Sub2API modules, endpoints, cookies, or credentials.

Required deployment secret: `CODEX2API_CREDENTIAL_OPS_KEY` (32-byte equivalent entropy; never log it). The application and runtime must use the same owner-only key. A missing key is a hard configuration error for password, TOTP, and OTP URL persistence.

Deployment contract: run this container as `codex2api-credential-runtime` on the private `codex2api-net` network, with no public ports and `CODEX2API_CREDENTIAL_OPS_KEY` plus `CODEX2API_CREDENTIAL_OPS_WORKER_TOKEN` sourced from `/opt/codex2api/.env`. The worker token must be independently generated and at least 32 characters. Build with the `tosub2` context pinned to `8548397e89bf80e508eda64a87e0d556d43abc84`; retain its license. The application secret is `CODEX2API_CREDENTIAL_OPS_KEY`, and the runtime service must not receive Sub2API keys, cookies, or endpoint URLs.
