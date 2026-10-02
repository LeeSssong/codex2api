import unittest
from build_credential_runtime import checked_dependencies

class SourceTests(unittest.TestCase):
 def fixture(self):return {'tosub2':{'repository':'https://github.com/poxiao33/toSub2.git','commit':'8'*40},'turb':{'repository':'https://github.com/myfanhua/turb-gpt-free-register.git','commit':'d'*40}}
 def test_pinned_sources(self):self.assertEqual(checked_dependencies(self.fixture()),self.fixture())
 def test_branch_ref_rejected(self):
  value=self.fixture();value['tosub2']['commit']='main'
  with self.assertRaises(ValueError):checked_dependencies(value)
 def test_unrelated_source_rejected(self):
  value=self.fixture();value['turb']['repository']='https://example.invalid/repository.git'
  with self.assertRaises(ValueError):checked_dependencies(value)
