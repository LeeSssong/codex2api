import copy
import unittest

class RuntimeComposeTests(unittest.TestCase):
 def fixture(self):
  return {'services':{'codex2api':{'image':'old','environment':{'CODEX_PORT':'18080'},'env_file':[{'path':'existing.env','required':True}],'networks':{'codex2api-net':None}},'postgres':{'image':'postgres:18'},'redis':{'image':'redis:7'}},'networks':{'codex2api-net':{'name':'codex2api-net'}}}
 def test_only_codex_application_and_new_private_worker_change(self):
  from credential_runtime_release import runtime_compose
  source=self.fixture();before=copy.deepcopy(source)
  result=runtime_compose(source,'sha256:worker','/opt/codex2api/secrets/credential-app.env','/opt/codex2api/secrets/credential-worker.env')
  self.assertEqual(source,before)
  self.assertEqual(result['services']['postgres'],before['services']['postgres'])
  self.assertEqual(result['services']['redis'],before['services']['redis'])
  self.assertEqual(result['networks'],before['networks'])
  self.assertEqual(result['services']['codex2api']['env_file'][-1]['path'],'/opt/codex2api/secrets/credential-app.env')
  worker=result['services']['credential-runtime']
  self.assertNotIn('ports',worker)
  self.assertEqual(worker['image'],'sha256:worker')
  self.assertEqual(worker['environment']['CODEX2API_CREDENTIAL_OPS_URL'],'http://codex2api:18080/api/internal/credential-ops')
  self.assertEqual(worker['env_file'],[{'path':'/opt/codex2api/secrets/credential-worker.env','required':True}])
  self.assertEqual(worker['mem_limit'],'512m')
 def test_reject_secret_override(self):
  from credential_runtime_release import runtime_compose
  value=self.fixture();value['services']['codex2api']['environment']['CODEX2API_CREDENTIAL_OPS_KEY']='existing'
  with self.assertRaises(ValueError):runtime_compose(value,'sha256:worker','/opt/codex2api/secrets/credential-app.env','/opt/codex2api/secrets/credential-worker.env')
 def test_reject_external_secret_paths(self):
  from credential_runtime_release import runtime_compose
  with self.assertRaises(ValueError):runtime_compose(self.fixture(),'sha256:worker','/opt/sub2api/secret.env','/opt/codex2api/secrets/credential-worker.env')
