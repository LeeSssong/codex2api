# Fixed egress management API

All endpoints below are under `/api/admin/fixed-egress`, require existing admin auth, JSON bodies, and return sanitized snapshots after successful mutations. Errors `{error:string}`. Feature off by default. No proxy credentials or provider key in any response; empty `api_key` preserves saved key.

GET `/`: snapshot `{config:{enabled,revision},pools:Pool[],providers:Provider[],resources:Resource[],bindings:Binding[],enrollments:{account_id,pool_id}[],events:Event[],tasks:Task[],accounts:{id,name}[],resin_enabled:boolean,ipv6_enabled:boolean}`. Arrays always present. `resource.endpoint` is sanitized scheme://host:port, no username/password.

Pool `{id,name,enabled,strategy:"strict"|"stable",cooldown_seconds,max_attempts,rotate_429,retry_5xx}`.
Provider `{id,name,base_url,protocol:"http"|"https"|"socks5",auth_format:"1"|"2"|"3",country,city,whitelist_ips:string[],port_start,port_end,capacity,refill_interval_seconds,enabled,has_key}`. API base defaults `https://ipapi.cliproxy.com`, no credential/query. State/city provider extensions only where supported.
Resource `{id,pool_id,provider_id,provider_port,endpoint,observed_exit_ip,state,owned,provider_task_id,changed_at,cooldown_until?}`.
Binding `{account_id,pool_id,resource_id,generation,state,changed_reason,change_count,changed_at}`.
Task `{id,provider_id,port,state,result,resource_id,created_at,updated_at}`; unknown task must reconcile, never repeat start blindly.
Event `{id,account_id,kind,detail,created_at}` (detail JSON string safe IDs/reason only).

PUT `/config`: `{enabled,revision}` CAS; reject conflicting Resin/local IPv6; disable keeps enrollment blocking (unenroll explicitly to restore previous route).
PUT `/pools/:id`: full Pool (path id wins).
PUT `/providers/:id`: Provider plus optional `api_key` string (blank retains); no key echo.
POST `/import`: `{pool_id,protocol,format:1|2|3,lines:string}`; imports pending resources, no observed exit trust.
POST `/resources/:id/test`: real proxy egress probe (bounded timeout); marks healthy on verified exit, quarantine on bound exit drift. Inconclusive target failure keeps old state.
POST `/resources/:id/retire`: `{}` local retire when unbound; imported resources never remote stopped.
POST `/providers/:id/sync`: `{pool_id}` calls online only, imports pending entries, reconciles tasks, never rebinds/deletes missing resources.
POST `/providers/:id/start`: `{pool_id}` allocates one port/task and calls start once; capacity/rate caps. Ambiguous result returns 409 plus task retained, resolve via sync.
POST `/resources/:id/stop`: `{}` owned/unbound/drained only, specific remote stop port. No stop-all endpoint.
POST `/accounts/:id/enroll`: `{pool_id}` native Codex account only, claim healthy verified resource; disabled pool/feature fails closed.
POST `/accounts/:id/unenroll`: `{}` restores previous saved legacy routing for future requests.
POST `/accounts/:id/rotate`: `{generation,resource_id}` expected current generation plus desired healthy spare same pool.

Limits: import <=500 rows/128KiB; admin writes <=256KiB; IDs [A-Za-z0-9_-] 1..64; pool cooldown 30..86400; attempts 1..5; port 1..65535; capacity 1..1000; provider refill interval 30..86400. No automatic remote stop. Provider API key is required on initial provider save. Protocol explicit, 443 is a provider port not inferred TLS.

Remote stop requires explicit release of every resource lease across instances. Heartbeat expiry alone never certifies that a stream drained: if a process crashes, its orphaned lease intentionally blocks remote stop until an operator verifies it is stopped and reconciles the lease. Local retirement does not issue any supplier stop request. Stop-unknown tasks reconcile to stopped only when a successful recognized online inventory no longer contains that port.
