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
        self.assertEqual(calls[-1][1]['status'], 'succeeded')
        self.assertEqual(calls[-1][1]['credential']['refresh_token'], 'test-rt')
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
        self.assertEqual(len(calls), 2)
        self.assertTrue(all(path.endswith('/renew') for path, _ in calls))

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
