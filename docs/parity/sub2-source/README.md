# Sub2API account quota semantics

Reference repository https://github.com/Wei-Shaw/sub2api (local production fork source), pinned production commit a6d32edec667271d38c03ef55ac4a2edf77dfc5d from the user's local sub2api搭建 repository under upstream/sub2api/.

Account-cost resolver, account window projection and silent-refresh behavior are adapted to the native Go/React application. Preserve upstream LGPL-3.0 license in this directory and provenance when distributing these adaptations. Native prices/customer charges are not assumed identical to another deployment's configured prices; conformance compares the same normalized usage and explicit pricing policy.

Source evidence and numerical fixtures are in source-accounting-audit.md. New account fee events preserve immutable costs; pre-parity rows use their original recorded amounts and are labeled legacy/mixed. Historical revaluation using current prices is intentionally not performed. This task is local-only; no production deployment or remote main push is authorized.
