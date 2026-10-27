# Codex2API credential runtime

This application-owned runtime adapts Sub2API commit e46b0b00b00b37ab74d95f9d800cc6396d83f672 current password/TOTP, email-OTP, TOTP recovery and Excel PKCE engines. See SOURCE-LICENSE (LGPL-3.0) and SOURCE-MANIFEST.json. It never reads Sub2API account data or a Sub2API runtime release. Pinned toSub2 and Turb dependency commits are in dependencies.json; npm and Python dependencies are locked. Upstream protocol code is staged locally at image build time.

## Build and deployment

From this directory, build a standalone worker with:

    docker build --target worker -t codex2api-credential-runtime .

Or export a verified managed Linux bundle built by Codex2API:

    docker build --target artifact --output type=local,dest=./bundle .
    sha256sum ./bundle/manifest.json

Place the bundle on a private read-only runtime volume. Set CODEX2API_CREDENTIAL_OPS_RUNTIME_DIR to that directory and CODEX2API_CREDENTIAL_OPS_MANIFEST_SHA256 to the deployment-pinned manifest SHA256. The Go supervisor validates every file before launch and owns process-group shutdown/restart. It creates a private random worker token at $DATA_DIR/secrets/credential-operations-worker.token, without passing it to protocol children. The runtime builds for the Docker builder CPU architecture; build separately for each Linux target. The source manifest contains file hashes, dependency versions and licenses.

Standalone workers set CODEX2API_CREDENTIAL_OPS_URL=http://application:18080/api/internal/credential-ops and a dedicated random CODEX2API_CREDENTIAL_OPS_WORKER_TOKEN of at least 32 characters on both application and worker. Mount /data persistently. Do not expose this token to browser code. The worker --check validates Node, toSub2, Turb, curl_cffi and journal availability without logging into accounts. The admin runtime setting controls concurrency 1–16 (default 3). Lowering it drains active operations. The local worker never falls back to Session Studio; its HTTPS endpoint and headers require an explicit admin selection.

Back up the database, $DATA_DIR/secrets/credential-operations.key, the worker token, and the entire persistent credential-operations-recovery journal including its independent key.bin together. Key initialization refuses replacement when ciphertext exists. Rotation journals are AES-GCM encrypted, mode 0600, and fsync'ed; activation requires both local journal persistence and database ACK. Interrupted rotations become uncertain and can only verify saved old/new credentials, never enroll or activate again automatically.

## Internal protocol v1

All requests are POST JSON authenticated by X-Codex2API-Credential-Worker. runtime returns current engine/concurrency and records worker_id,version,capabilities heartbeat. claim uses the same fields and returns task,login or 204. Capabilities are login_codex_v1, email_otp_v1, excel_oauth_v1, totp_rotation_v1; an old worker cannot claim unsupported tasks. task includes ID,AccountID,Attempt,Kind,OAuthProfile; plaintext login is only on this authenticated internal response with no-store.

login/:id/renew accepts worker_id,attempt,stage; lease/config/account/route generations are fenced. complete adds status,credential for login. totp-phase adds phase,secret and requires ordered phases enrolling -> prepared -> activating -> verifying; prepared acknowledges a committed encrypted candidate. Recovery tasks only use verify_recovery. totp-finish adds success; only verified new/old pairs can adopt configuration. totp-recover accepts secret to reconcile an encrypted local journal candidate without upstream mutation. release proves the isolated child exited before releasing route/Mihomo leases. A stale renewal kills its process group and still releases only the matching attempt; expired fixed-egress leases are intentionally not silently drained.

## Offline checks

    python3 -m unittest discover -s tools/credential-runtime -p 'test*.py'
    node --test tools/credential-runtime/test_openai_totp_journal.mjs tools/credential-runtime/test_openai_excel_password_flow.mjs

These tests use synthetic accounts and fault injection. They do not prove live upstream first login, re-login, rotation or Excel authorization; those require isolated test accounts and configured egress. No production or real source account mutations are part of this build.

Mihomo node listeners bind to 127.0.0.1 on the application host. A standalone Worker using that pool must share the application network namespace (Docker network_mode: service:codex2api) and use the localhost internal API. A Worker on a separate host/network cannot consume these loopback listeners. Managed runtime children already share the application host. A managed or standalone worker never reads another application's account data.
