import pathlib, subprocess, tempfile, unittest
from release_build import verify_source

class SourceTests(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.root=pathlib.Path(self.tmp.name)/'repo';self.root.mkdir();remote=pathlib.Path(self.tmp.name)/'remote.git'
  subprocess.run(['git','init','--bare',str(remote)],check=True,capture_output=True)
  self.git('init','-b','main');self.git('config','user.name','Build Test');self.git('config','user.email','test@example.invalid')
  (self.root/'file').write_text('one');self.git('add','.');self.git('commit','-m','fixture');self.git('remote','add','origin',str(remote));self.git('push','-u','origin','main');self.sha=self.git('rev-parse','HEAD')
 def git(self,*args):return subprocess.check_output(['git',*args],cwd=self.root,stderr=subprocess.DEVNULL,text=True).strip()
 def test_verified_main_matches_remote(self):self.assertEqual(verify_source(self.root,self.sha)[0],self.sha)
 def test_dirty_tree_rejected(self):
  (self.root/'file').write_text('two')
  with self.assertRaises(ValueError):verify_source(self.root,self.sha)
 def test_unpushed_commit_rejected(self):
  (self.root/'file').write_text('two');self.git('commit','-am','unpublished')
  with self.assertRaises(ValueError):verify_source(self.root,self.sha)
 def test_feature_branch_rejected(self):
  self.git('checkout','-b','codex/candidate')
  with self.assertRaises(ValueError):verify_source(self.root,self.sha)
