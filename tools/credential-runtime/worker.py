#!/usr/bin/env python3
import json, os, sys, uuid

if not os.environ.get("CODEX2API_CREDENTIAL_OPS_KEY"):
    print(json.dumps({"status":"failed","error":"CODEX2API_CREDENTIAL_OPS_KEY is required"}), flush=True)
    raise SystemExit(78)

for line in sys.stdin:
    try:
        req=json.loads(line); op=req.get("op", "status"); task=req.get("task_id") or str(uuid.uuid4())
        if op == "start": out={"task_id":task,"status":"running","stage":"starting"}
        elif op == "cancel": out={"task_id":task,"status":"cancelled","stage":"failed"}
        else: out={"task_id":task,"status":"succeeded","stage":"status"}
        print(json.dumps(out), flush=True)
    except Exception:
        print(json.dumps({"status":"failed","error":"invalid request"}), flush=True)
