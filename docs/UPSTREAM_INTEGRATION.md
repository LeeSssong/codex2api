# Upstream integration

This checkout combines the following pinned upstream revisions:

| Source | Branch | Revision |
| --- | --- | --- |
| james-6-23/codex2api | main | 5601508801d0b4d1efc9881e5ad04eb8077c654f |
| hloolx/codex2api | main | b402c611 |

The integration starts from the main project and merges hloolx with conflict
preference for hloolx. State capture, portable State reuse, Basispoints routing,
per-key route policies, verified capability probes, and quota recovery follow
hloolx. The credential-level State API remains available alongside managed State.

Integration repairs preserve the main project's Daybreak account snapshot,
channel monitoring, image job queue, scheduler selection timeout, live concurrency
caps, and account links. The system settings insert includes both the main
project's exhausted-credit reset setting and hloolx's Basispoints switch.
Basispoints is an active setting in this checkout, even though the main project
retired its old implementation. Connection tests follow hloolx's automatic
start and abort-on-cleanup behavior.

Queued image route retries reuse the pipeline's disk spooling to release large
input buffers while waiting upstream. Detector probes bypass payload rules, and
fresh State probes keep their managed-State bypass flag through native routing.

## Run the integrated source

The default image deployment files follow hloolx and pull its published image.
Those images do not contain this local integration. To run the combined code,
build this checkout with the existing local deployment file:

```sh
docker compose -f docker-compose.local.yml up -d --build
```

For an installation already configured for SQLite:

```sh
docker compose -f docker-compose.sqlite.local.yml up -d --build
```

Use the compose project and data volumes belonging to the existing installation.
Switching between the image and local compose project names creates separate data
volumes. Keep existing credentials and database configuration in `.env`.

For future upgrades, fetch both upstreams, update the main project, and merge
hloolx with `-X theirs` when it is the incoming branch. Run Go tests, frontend
tests, type checking, the frontend build, and State/route browser regressions
before deploying. Text conflict resolution alone cannot detect interface or
database placeholder conflicts.
