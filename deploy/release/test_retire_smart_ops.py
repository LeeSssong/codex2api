import copy
import unittest
from retire_smart_ops import retire_compose

class RetirementTests(unittest.TestCase):
    def test_removes_only_dedicated_runtime_and_secret_environment(self):
        source = {'services': {'codex2api': {'image': 'old', 'environment': {'ADMIN_SECRET': 'keep', 'CODEX2API_CREDENTIAL_OPS_KEY': 'remove', 'CODEX2API_CREDENTIAL_OPS_WORKER_TOKEN': 'remove'}, 'volumes': ['/data:/data']}, 'credential-runtime': {'container_name': 'codex2api-credential-runtime'}, 'postgres': {'image': 'postgres:18'}}, 'networks': {'codex2api-net': {'external': True}}}
        before = copy.deepcopy(source)
        got = retire_compose(source, 'sha256:new')
        self.assertEqual(source, before)
        self.assertEqual(got['services']['codex2api'], {'image':'sha256:new','environment':{'ADMIN_SECRET':'keep'}, 'volumes':['/data:/data']})
        self.assertEqual(got['services']['postgres'], source['services']['postgres'])
        self.assertNotIn('credential-runtime', got['services'])
        self.assertEqual(got['networks'], source['networks'])
        self.assertEqual(retire_compose(got,'sha256:new'), got)

    def test_refuses_unresolved_environment_files(self):
        with self.assertRaises(ValueError):
            retire_compose({'services': {'codex2api': {'env_file':['private.env']}}}, 'new')

    def test_refuses_unrelated_service_using_reserved_name(self):
        with self.assertRaises(ValueError):
            retire_compose({'services': {'codex2api': {}, 'credential-runtime': {'container_name':'unrelated'}}}, 'new')

    def test_invalid_environment_is_rejected_before_mutating_source(self):
        source={'services': {'codex2api': {'environment':['ADMIN_SECRET=keep']}}}
        before=copy.deepcopy(source)
        with self.assertRaises(ValueError):
            retire_compose(source,'sha256:new')
        self.assertEqual(source,before)
