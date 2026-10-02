import copy, unittest

class UpdateTests(unittest.TestCase):
 def test_only_runtime_image_changes(self):
  from update_credential_runtime import replacement
  before={'services':{'codex2api':{'image':'app','environment':{'sensitive':'synthetic'}},'credential-runtime':{'image':'old','container_name':'codex2api-credential-runtime'},'postgres':{'image':'pg'}},'networks':{'codex2api-net':{}}}
  expected=copy.deepcopy(before);expected['services']['credential-runtime']['image']='sha256:new'
  self.assertEqual(replacement(before,'sha256:new'),expected)
  self.assertEqual(before['services']['credential-runtime']['image'],'old')
 def test_runtime_must_already_be_installed(self):
  from update_credential_runtime import replacement
  with self.assertRaises(ValueError):replacement({'services':{'codex2api':{}}},'sha256:new')
 def test_mutable_image_rejected(self):
  from update_credential_runtime import replacement
  with self.assertRaises(ValueError):replacement({'services':{}},'latest')
