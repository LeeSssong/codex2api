#!/usr/bin/env python3
"""Private durable OpenAI login worker, adapted from Sub2API's MIT worker."""
import json, os, subprocess, tempfile, time, urllib.request, urllib.error

BASE=os.environ.get("CODEX2API_CREDENTIAL_OPS_URL", "http://codex2api:8080/api/admin/internal/credential-ops").rstrip("/")
TOKEN=os.environ.get("CODEX2API_CREDENTIAL_OPS_WORKER_TOKEN", "")
WORKER=os.environ.get("CODEX2API_CREDENTIAL_OPS_WORKER_ID", "credential-runtime")
ROOT=os.environ.get("TOSUB2_ROOT", "")
if len(TOKEN)<32: raise SystemExit("CODEX2API_CREDENTIAL_OPS_WORKER_TOKEN must be at least 32 chars")
if not ROOT or not os.path.isfile(os.path.join(ROOT,"src","protocol-login.mjs")): raise SystemExit("TOSUB2_ROOT/src/protocol-login.mjs is required")

def request(path, body=None):
    raw=None if body is None else json.dumps(body).encode()
    req=urllib.request.Request(BASE+path,raw,headers={"Content-Type":"application/json","X-Codex2API-Credential-Worker":TOKEN},method="POST")
    try:
        with urllib.request.urlopen(req,timeout=35) as rsp: return rsp.status,json.loads(rsp.read() or b"{}")
    except urllib.error.HTTPError as err: return err.code,{}

def protocol(login):
    # This is the source-derived toSub2 password/TOTP flow used by Sub2API.
    # Password and TOTP are environment-only and output is parsed from a 0700 temp dir.
    with tempfile.TemporaryDirectory(prefix="codex2api-reauth-") as tmp:
        out=os.path.join(tmp,"oauth.json")
        env=os.environ.copy(); env["CHATGPT_LOGIN_PASSWORD"]=login["password"]; env["CHATGPT_TOTP_SECRET"]=login.get("totp_secret","")
        cmd=["node",os.path.join(ROOT,"src","protocol-login.mjs"),"--email",login["email"],"--output-mode","sub2api","--sub2api-out",out,"--sub2api-name","codex2api-credential-runtime"]
        if login.get("proxy_url"): cmd.extend(["--proxy",login["proxy_url"]])
        p=subprocess.run(cmd,cwd=tmp,env=env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=1500,check=False)
        if p.returncode: raise RuntimeError("OpenAI protocol login failed")
        result=json.load(open(out,encoding="utf-8")); accounts=result.get("accounts",[]); credential=accounts[0].get("credentials",{}) if len(accounts)==1 else {}
        if not all(credential.get(k) for k in ("access_token","refresh_token","id_token")): raise RuntimeError("OpenAI protocol returned incomplete credentials")
        return {key:credential[key] for key in ("access_token","refresh_token","id_token","expires_at","account_id","chatgpt_account_id") if key in credential}

while True:
    status,payload=request("/claim",{"worker_id":WORKER})
    if status==204: time.sleep(2); continue
    if status!=200: time.sleep(5); continue
    task=payload["task"]
    try:
        credential=protocol(payload["login"])
        request("/login/%s/complete" % task["ID"],{"worker_id":WORKER,"status":"succeeded","stage":"succeeded","credential":credential})
    except Exception:
        request("/login/%s/complete" % task["ID"],{"worker_id":WORKER,"status":"failed","stage":"failed"})
