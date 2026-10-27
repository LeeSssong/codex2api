#!/usr/bin/env python3
"""Codex2API-only worker. Supervise isolated source-derived protocol engines."""
import argparse
import base64
import hashlib
import json
import logging
import os
from pathlib import Path
import secrets
import signal
import subprocess
import sys
import tempfile
import time
import queue
import threading
from concurrent.futures import ThreadPoolExecutor
from types import SimpleNamespace
import urllib.error
import urllib.parse
import urllib.request

HEARTBEAT = Path(os.environ.get("CODEX2API_CREDENTIAL_OPS_HEARTBEAT", "/tmp/credential-worker-heartbeat"))
STOP = False
VERSION="1.0.0"
CAPABILITIES=["login_codex_v1","email_otp_v1","excel_oauth_v1","totp_rotation_v1"]
CLIENT_ID = "app_EMoamEEZ73f0CkXaXp7hrann"
REDIRECT = "http://localhost:1455/auth/callback"

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *_args, **_kwargs):
        return None

API_OPENER = urllib.request.build_opener(NoRedirect())

def heartbeat():
    HEARTBEAT.write_text(str(time.time()), encoding="ascii")

def request(path, body):
    base = os.environ.get("CODEX2API_CREDENTIAL_OPS_URL", "http://codex2api:18080/api/internal/credential-ops").rstrip("/")
    token = os.environ.get("CODEX2API_CREDENTIAL_OPS_WORKER_TOKEN", "")
    req = urllib.request.Request(base + path, json.dumps(body).encode(), headers={
        "Content-Type": "application/json", "X-Codex2API-Credential-Worker": token}, method="POST")
    try:
        with API_OPENER.open(req, timeout=20) as response:
            return response.status, json.loads(response.read(1048576) or b"{}")
    except urllib.error.HTTPError as error:
        return error.code, {}
    except (OSError, ValueError):
        return 503, {}

def check():
    import curl_cffi  # noqa: F401
    root = Path(os.environ.get("TOSUB2_ROOT", "/opt/tosub2"))
    if not (root / "src/protocol-login.mjs").is_file():
        raise RuntimeError("pinned toSub2 runner is unavailable")
    subprocess.run([os.environ.get("NODE_EXECUTABLE","node"), "--check", str(root / "src/protocol-login.mjs")], check=True,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    turb = Path(os.environ.get("TURB_ROOT", "/opt/turb"))
    if not (turb / "core/codex_oauth.py").is_file():
        raise RuntimeError("pinned Turb email OTP engine is unavailable")
    import engines
    engines.load_protocol(turb)
    from openai_totp_rotation import Journal
    Journal()

class EngineOutput:
    def __init__(self, directory, login):
        self.directory = Path(directory)
        self.login = login
        self.config = SimpleNamespace(tosub2_root=Path(os.environ.get("TOSUB2_ROOT", "/opt/tosub2")))
        self.state = secrets.token_urlsafe(32)
        self.verifier = secrets.token_urlsafe(48)

    def _rpc(self, name, body):
        sys.stdout.write(json.dumps({"rpc":name,"body":body})+"\n")
        sys.stdout.flush()
        response=json.loads(sys.stdin.readline())
        if response.get("status") != 204:
            raise RuntimeError("durable protocol acknowledgement unavailable")
        return response

    def totp_phase(self, _task, phase, secret=""):
        return self._rpc("totp-phase",{"phase":phase,"secret":secret})

    def totp_finish(self, _task, success, error_code=""):
        return self._rpc("totp-finish",{"success":success,"error_code":error_code})

    def totp_recover(self, task, secret):
        return self._rpc("totp-recover",{"recovery_task_id":task,"secret":secret})

    def progress(self, _task, stage):
        self.directory.joinpath("stage").write_text(stage, encoding="ascii")

    def credentials(self, _task, credentials, _extra):
        self.directory.joinpath("result.json").write_text(json.dumps(credentials), encoding="utf-8")
        return {"status": "succeeded"}

    def callback(self, task, callback_url):
        parsed = urllib.parse.urlparse(callback_url)
        params = urllib.parse.parse_qs(parsed.query)
        if params.get("state") != [self.state] or not params.get("code"):
            raise RuntimeError("OAuth callback state mismatch")
        from curl_cffi import requests
        with requests.Session(impersonate="chrome", proxy=self.login.get("proxy_url") or None) as session:
            response = session.post("https://auth.openai.com/oauth/token", data={
                "grant_type": "authorization_code", "client_id": CLIENT_ID,
                "redirect_uri": REDIRECT, "code": params["code"][0], "code_verifier": self.verifier,
            }, timeout=30, allow_redirects=False)
            if response.status_code != 200:
                raise RuntimeError("OAuth code exchange failed")
            return self.credentials(task, response.json(), {})

    def authorization_url(self):
        challenge = base64.urlsafe_b64encode(hashlib.sha256(self.verifier.encode()).digest()).decode().rstrip("=")
        return "https://auth.openai.com/oauth/authorize?" + urllib.parse.urlencode({
            "client_id": CLIENT_ID, "redirect_uri": REDIRECT, "response_type": "code",
            "scope": "openid profile email offline_access", "state": self.state,
            "code_challenge": challenge, "code_challenge_method": "S256",
            "id_token_add_organizations": "true", "codex_cli_simplified_flow": "true"})

def run_engine(directory):
    import engines
    logging.disable(logging.CRITICAL)
    login = json.loads(sys.stdin.readline())
    expected_parent=login.pop("_supervisor_pid",os.getppid())
    if os.getppid()!=expected_parent:
        raise RuntimeError("protocol supervisor exited")
    def watch_supervisor():
        while True:
            if os.getppid()!=expected_parent:
                os.killpg(os.getpgrp(),signal.SIGKILL)
            time.sleep(0.5)
    threading.Thread(target=watch_supervisor,daemon=True).start()
    api = EngineOutput(directory, login)
    claim = dict(login, task_id=login.get("task_id",1), account_id=login.get("account_id",0), login_email=login["email"],
                 credential_mode=login["mode"], auth_url=api.authorization_url())
    if login.get("kind","").startswith("totp_"):
        from openai_totp_rotation import rotate
        rotate(api,claim)
        api.directory.joinpath("totp-finished").write_text("finished")
        return
    protocol = engines.load_protocol(Path(os.environ.get("TURB_ROOT", "/opt/turb"))) if login["mode"] == "email_otp_url" else None
    engines.process_claim(api, protocol, claim)

def terminate_process(process):
    if process.poll() is not None:
        return
    os.killpg(process.pid, signal.SIGTERM)
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        process.wait()

def process_claim(payload, worker):
    task = payload["task"]
    identity = {"worker_id": worker, "attempt": task["Attempt"]}
    task_path = "/login/%s" % task["ID"]
    status, _ = request(task_path + "/renew", dict(identity, stage="starting"))
    if status != 204:
        return
    with tempfile.TemporaryDirectory(prefix="codex2api-login-") as directory:
        # Protocol engines cannot inherit the application key or worker token.
        allowed = {"PATH", "HOME", "TMPDIR", "TOSUB2_ROOT", "TURB_ROOT", "TOSUB2_PYTHON",
                   "NODE_EXECUTABLE", "CODEX2API_CREDENTIAL_OPS_TRUSTED_OTP_HOSTS", "OPENAI_TOTP_JOURNAL_DIR"}
        env = {key: value for key, value in os.environ.items() if key in allowed}
        process = subprocess.Popen([os.environ.get("TOSUB2_PYTHON") or sys.executable, str(Path(__file__).resolve()), "--run-engine", directory],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
            env=env, start_new_session=True)
        login=dict(payload["login"],task_id=task["ID"],account_id=task.get("AccountID",0),kind=task.get("Kind","login"),_supervisor_pid=os.getpid())
        process.stdin.write((json.dumps(login)+"\n").encode())
        process.stdin.flush()
        messages=queue.Queue()
        def read_messages():
            for line in process.stdout:
                if len(line)>1048576:
                    break
                messages.put(line)
        reader=threading.Thread(target=read_messages,daemon=True)
        reader.start()
        def drain_messages():
            while True:
                try:line=messages.get_nowait()
                except queue.Empty:return
                rpc=json.loads(line)
                name=rpc.get("rpc")
                if name not in {"totp-phase","totp-finish","totp-recover"}:
                    raise RuntimeError("unexpected protocol RPC")
                body=dict(rpc.get("body",{}),**identity)
                route=task_path
                if name=="totp-recover":
                    route="/login/%s" % body.pop("recovery_task_id")
                status,_=request(route+"/"+name,body)
                process.stdin.write((json.dumps({"status":status})+"\n").encode())
                process.stdin.flush()
        started, renewed = time.monotonic(), time.monotonic()
        try:
            while process.poll() is None:
                heartbeat()
                drain_messages()
                if STOP or time.monotonic() - started > 1500:
                    terminate_process(process)
                    request(task_path + "/complete", dict(identity, status="failed", stage="failed"))
                    return
                if time.monotonic() - renewed >= 25:
                    stage_file = Path(directory) / "stage"
                    stage = stage_file.read_text()[:64] if stage_file.is_file() else "protocol_login"
                    status, _ = request(task_path + "/renew", dict(identity, stage=stage))
                    if status != 204:
                        terminate_process(process)
                        return
                    renewed = time.monotonic()
                time.sleep(1)
            drain_messages()
            if task.get("Kind","").startswith("totp_") and Path(directory,"totp-finished").exists():return
            result_file = Path(directory) / "result.json"
            if process.returncode or not result_file.is_file():
                request(task_path + "/complete", dict(identity, status="failed", stage="failed"))
                return
            credential = json.loads(result_file.read_text())
            if not all(isinstance(credential.get(key), str) and credential[key] for key in ("access_token", "refresh_token", "id_token")):
                raise RuntimeError("engine returned incomplete credentials")
            for _ in range(3):
                status, _ = request(task_path + "/complete", dict(identity, status="succeeded", stage="succeeded", credential=credential))
                if status < 500:
                    break
                heartbeat()
                time.sleep(2)
        finally:
            terminate_process(process)
            process.stdin.close()
            process.stdout.close()
            reader.join(timeout=2)
            request(task_path+"/release",identity)

class WorkerPool:
    def __init__(self):self.active=set()
    def reap(self):self.active={future for future in self.active if not future.done()}
    def available(self,concurrency):return max(0,concurrency-len(self.active))

def safe_process_claim(payload,worker):
    try:process_claim(payload,worker)
    except Exception:
        task=payload.get("task",{})
        kind=task.get("Kind","")
        path="/login/%s/" % task.get("ID",0)
        body={"worker_id":worker,"attempt":task.get("Attempt",0)}
        if kind.startswith("totp_"):request(path+"totp-finish",dict(body,success=False))
        else:request(path+"complete",dict(body,status="failed",stage="failed"))

def stop(_signal, _frame):
    global STOP
    STOP = True

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--healthcheck", action="store_true")
    parser.add_argument("--run-engine")
    args = parser.parse_args()
    if args.healthcheck:
        try:
            return 0 if time.time() - float(HEARTBEAT.read_text()) < 65 else 1
        except (OSError, ValueError):
            return 1
    if args.run_engine:
        run_engine(args.run_engine)
        return 0
    check()
    if args.check:
        return 0
    if len(os.environ.get("CODEX2API_CREDENTIAL_OPS_WORKER_TOKEN", "")) < 32:
        raise RuntimeError("dedicated worker token must contain at least 32 characters")
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    worker = os.environ.get("CODEX2API_CREDENTIAL_OPS_WORKER_ID", "credential-runtime")
    pool=WorkerPool()
    next_recovery=0
    with ThreadPoolExecutor(max_workers=16) as executor:
        while not STOP:
            heartbeat()
            pool.reap()
            if time.monotonic()>=next_recovery:
                try:
                    from openai_totp_rotation import Journal
                    class RecoveryAPI:
                        def totp_recover(self,task,secret):
                            status,_=request("/login/%s/totp-recover"%task,{"secret":secret})
                            if status!=204:raise RuntimeError("journal recovery not acknowledged")
                    Journal().recover(RecoveryAPI())
                except Exception:pass
                next_recovery=time.monotonic()+60
            status,settings=request("/runtime",{"worker_id":worker,"version":VERSION,"capabilities":CAPABILITIES})
            concurrency=max(1,min(16,int(settings.get("worker_concurrency",3)))) if status==200 else 1
            if status==200 and settings.get("enabled",True):
                for _ in range(pool.available(concurrency)):
                    status,payload=request("/claim",{"worker_id":worker,"version":VERSION,"capabilities":CAPABILITIES})
                    if status!=200:break
                    pool.active.add(executor.submit(safe_process_claim,payload,worker))
            time.sleep(2)
    return 0

if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception:
        # External response text can contain passwords and tokens.
        print("credential runtime failed; inspect configuration and engine availability", file=sys.stderr)
        sys.exit(1)
