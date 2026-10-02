# Codex2API credential runtime

This runtime is an independently versioned worker boundary. It accepts one JSON request per line on stdin and emits one redacted JSON status per line on stdout. It does not import Sub2API modules, endpoints, cookies, or credentials.

Required deployment secret: `CODEX2API_CREDENTIAL_OPS_KEY` (32-byte equivalent entropy; never log it). The application and runtime must use the same owner-only key. A missing key is a hard configuration error for password, TOTP, and OTP URL persistence.
