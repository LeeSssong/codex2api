import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

import engines
import worker


class WorkerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.addCleanup(self.tmp.cleanup)
        worker.STOP = False
        self.environment = patch.dict(os.environ, {
            "TOSUB2_ROOT": str(self.root), "NODE_EXECUTABLE": str(self.root / "node"),
            "CODEX2API_CREDENTIAL_OPS_WORKER_TOKEN": "w" * 32,
            "CODEX2API_CREDENTIAL_OPS_KEY": "must-not-reach-engine",
        })
        self.environment.start()
        self.addCleanup(self.environment.stop)
        self.root.joinpath("src").mkdir()
        self.root.joinpath("src/protocol-login.mjs").write_text("// test protocol")
        node = self.root / "node"
        node.write_text("#!" + sys.executable + "\n" + """
import json, os, pathlib, sys
assert 'CODEX2API_CREDENTIAL_OPS_WORKER_TOKEN' not in os.environ
assert 'CODEX2API_CREDENTIAL_OPS_KEY' not in os.environ
assert os.environ['CHATGPT_LOGIN_PASSWORD'] == 'test-password'
assert os.environ['CHATGPT_TOTP_SECRET'] == 'JBSWY3DPEHPK3PXP'
assert '--output-mode' in sys.argv and sys.argv[sys.argv.index('--output-mode')+1] == 'sub2api'
pathlib.Path(sys.argv[sys.argv.index('--sub2api-out')+1]).write_text(json.dumps({'accounts':[{'credentials': {'access_token':'test-at','refresh_token':'test-rt','id_token':'test-id'}}]}))
""")
        node.chmod(0o700)

    def payload(self):
        return {"task": {"ID": 7, "Attempt": 2}, "login": {"email": "test@example.com", "password": "test-password", "totp_secret": "JBSWY3DPEHPK3PXP", "mode": "password_totp", "engine": "local_worker"}}

    def test_real_isolated_runner_contract_and_secret_separation(self):
        calls = []
        def api(path, body):
            calls.append((path, body))
            return (200 if path.endswith('/complete') else 204), {}
        with patch.object(worker, 'request', side_effect=api), patch.object(worker, 'HEARTBEAT', self.root / 'heartbeat'):
            worker.process_claim(self.payload(), 'worker-test')
        self.assertEqual(calls[0], ('/login/7/renew', {'worker_id': 'worker-test', 'attempt': 2, 'stage': 'starting'}))
        completion=[body for path,body in calls if path.endswith('/complete')][-1]
        self.assertEqual(completion['status'], 'succeeded')
        self.assertEqual(completion['credential']['refresh_token'], 'test-rt')
        self.assertTrue(self.root.joinpath('heartbeat').is_file())

    def test_stale_initial_claim_never_runs_engine(self):
        with patch.object(worker, 'request', return_value=(409, {})), patch.object(subprocess, 'Popen') as process:
            worker.process_claim(self.payload(), 'worker-test')
        process.assert_not_called()

    def test_preflight_check_requires_no_token_or_network(self):
        with patch.dict(os.environ, {"CODEX2API_CREDENTIAL_OPS_WORKER_TOKEN": ""}), patch.object(worker, 'check') as check, patch.object(worker, 'request') as request, patch.object(sys, 'argv', ['worker.py', '--check']):
            self.assertEqual(worker.main(), 0)
            check.assert_called_once()
            request.assert_not_called()

    def test_cancelled_renewal_kills_process_group_and_has_no_callback(self):
        self.root.joinpath('node').write_text('#!' + sys.executable + '\nimport time\ntime.sleep(300)\n')
        self.root.joinpath('node').chmod(0o700)
        calls = []
        def api(path, body):
            calls.append((path, body))
            return (204 if len(calls) == 1 else 409), {}
        ticks = iter([0, 0, 1, 30])
        real_time = time.monotonic
        def clock():
            return next(ticks, real_time())
        with patch.object(worker, 'request', side_effect=api), patch.object(worker, 'HEARTBEAT', self.root / 'heartbeat'), patch.object(worker.time, 'monotonic', side_effect=clock):
            worker.process_claim(self.payload(), 'worker-test')
        self.assertEqual(len(calls), 3)
        self.assertTrue(all(path.endswith('/renew') for path, _ in calls[:-1]))
        self.assertTrue(calls[-1][0].endswith('/release'))

    def test_healthcheck_is_bounded_by_heartbeat_age(self):
        with patch.object(worker, 'HEARTBEAT', self.root / 'heartbeat'), patch.object(sys, 'argv', ['worker.py', '--healthcheck']):
            self.assertEqual(worker.main(), 1)
            worker.heartbeat()
            self.assertEqual(worker.main(), 0)
            worker.HEARTBEAT.write_text(str(time.time() - 120))
            self.assertEqual(worker.main(), 1)

    def test_email_otp_requires_explicit_protocol_and_secret(self):
        with self.assertRaises(engines.WorkerError):
            engines.process_claim(None, None, {'credential_mode': 'email_otp_url'})
        with self.assertRaises(engines.WorkerError):
            engines.validate_otp_url('https://127.0.0.1/inbox')

    def test_pkce_callback_checks_state_before_exchange(self):
        output = worker.EngineOutput(self.root, {'email': 'test@example.com'})
        auth = output.authorization_url()
        self.assertIn('code_challenge_method=S256', auth)
        with self.assertRaises(RuntimeError):
            output.callback(1, 'http://localhost:1455/auth/callback?state=wrong&code=fake')

    def test_session_studio_parser_and_redirect_boundary(self):
        response = io.BytesIO(b'{"type":"progress"}\n{"type":"result","payload":{"status":"active","credential":{"access_token":"test"}}}')
        self.assertEqual(engines._session_studio_result(response)['status'], 'active')
        with self.assertRaises(engines.WorkerError):
            engines.process_session_studio_claim(None, {'task_id': 1, 'account_id': 0, 'login_email': 'test@example.com', 'password': 'fake', 'relogin_endpoint': 'http://unsafe.example/login'})


if __name__ == '__main__':
    unittest.main()

class CurrentCapabilitiesTests(unittest.TestCase):
    def test_engine_rpc_must_require_database_ack_before_activation(self):
        with tempfile.TemporaryDirectory() as folder:
            output=worker.EngineOutput(folder,{'email':'test@example.invalid'})
            incoming=io.StringIO('{"status":409}\n')
            outgoing=io.StringIO()
            with patch.object(sys,'stdin',incoming),patch.object(sys,'stdout',outgoing):
                with self.assertRaises(RuntimeError):output.totp_phase(7,'prepared','SYNTHETIC-CANDIDATE')
            data=json.loads(outgoing.getvalue())
            self.assertEqual(data,{'rpc':'totp-phase','body':{'phase':'prepared','secret':'SYNTHETIC-CANDIDATE'}})

    def test_concurrency_lowering_does_not_cancel_existing_tasks(self):
        pool=worker.WorkerPool()
        pool.active={object(),object(),object()}
        self.assertEqual(pool.available(1),0)
        self.assertEqual(len(pool.active),3)
        self.assertEqual(pool.available(4),1)

class RelocatableRuntimeTests(unittest.TestCase):
    setUp=WorkerTests.setUp
    payload=WorkerTests.payload
    def test_configured_python_wrapper_launches_isolated_engine(self):
        wrapper=self.root/'python-wrapper'
        marker=self.root/'wrapper-used'
        wrapper.write_text('#!/bin/sh\nprintf used > "'+str(marker)+'"\nexec "'+sys.executable+'" "$@"\n')
        wrapper.chmod(0o700)
        def api(path,body):return (200 if path.endswith('/complete') else 204),{}
        with patch.dict(os.environ,{'TOSUB2_PYTHON':str(wrapper)}),patch.object(worker,'request',side_effect=api),patch.object(worker,'HEARTBEAT',self.root/'heartbeat'):
            worker.process_claim(self.payload(),'wrapper-test')
        self.assertTrue(marker.exists(),'isolated engine bypassed the configured relocatable Python wrapper')

    def test_docker_journal_install_creates_private_directory(self):
        import shlex
        dockerfile=Path(__file__).with_name('Dockerfile').read_text()
        command=None
        for line in dockerfile.splitlines():
            if not line.startswith('RUN '):continue
            tokens=shlex.split(line[4:])
            for index,value in enumerate(tokens):
                if value!='install':continue
                end=tokens.index('&&',index) if '&&' in tokens[index:] else len(tokens)
                candidate=tokens[index:end]
                if '/data/credential-operations-recovery' in candidate:command=candidate
        self.assertIsNotNone(command,'Docker journal bootstrap must create a private directory')
        target=self.root/'docker-journal'
        command=[str(target) if value=='/data/credential-operations-recovery' else value for value in command]
        for flag,value in [('-o',str(os.getuid())),('-g',str(os.getgid()))]:
            if flag in command:command[command.index(flag)+1]=value
        subprocess.run(command,check=True,capture_output=True)
        self.assertEqual(target.stat().st_mode&0o777,0o700)
