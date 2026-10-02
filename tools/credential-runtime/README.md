# Codex2API credential runtime

This runtime is an independently versioned worker boundary. It accepts one JSON request per line on stdin and emits one redacted JSON status per line on stdout. It does not import Sub2API modules, endpoints, cookies, or credentials.

Required deployment secret: `CODEX2API_CREDENTIAL_OPS_KEY` (32-byte equivalent entropy; never log it). The application and runtime must use the same owner-only key. A missing key is a hard configuration error for password, TOTP, and OTP URL persistence.

Deployment contract: run this container as `codex2api-credential-runtime` on the private `codex2api-net` network, with no public ports and `CODEX2API_CREDENTIAL_OPS_KEY` sourced from `/opt/codex2api/.env`. The API container may invoke it over an internal stdin/stdout supervisor or replace the placeholder worker protocol with the production browser-login adapter; it must remain independently restartable and versioned. The application secret is `CODEX2API_CREDENTIAL_OPS_KEY`, and the runtime service must not receive Sub2API keys, cookies, or endpoint URLs.
