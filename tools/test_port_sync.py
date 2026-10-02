import importlib.util, json, pathlib, subprocess, tempfile, unittest
spec=importlib.util.spec_from_file_location('port_sync',pathlib.Path(__file__).with_name('port_sync.py'))
port_sync=importlib.util.module_from_spec(spec)
spec.loader.exec_module(port_sync)

class PortSyncTests(unittest.TestCase):
 def test_changed_files_mapped_without_source_mutation(self):
  with tempfile.TemporaryDirectory() as folder:
   root=pathlib.Path(folder)
   def git(*args):return subprocess.check_output(['git','-C',folder,*args],stderr=subprocess.DEVNULL,text=True).strip()
   git('init','-b','main');git('config','user.name','Fixture');git('config','user.email','fixture@example.invalid')
   (root/'quality.go').write_text('before');(root/'other.go').write_text('before');git('add','.');git('commit','-m','base');base=git('rev-parse','HEAD')
   (root/'quality.go').write_text('after');(root/'other.go').write_text('after');git('commit','-am','next');head=git('rev-parse','HEAD')
   manifest={'schema_version':1,'source_commit':base,'modules':{'quality':['quality'],'unaffected':['tokens']}}
   report=port_sync.compare(folder,manifest,'HEAD')
   self.assertEqual(report['source_commit'],head)
   self.assertEqual(report['modules']['quality'],['quality.go'])
   self.assertEqual(report['modules']['unaffected'],[])
   self.assertEqual(report['unmapped_files'],['other.go'])
   self.assertEqual(git('status','--porcelain'),'')
   self.assertEqual(git('rev-parse','HEAD'),head)
 def test_unsafe_ref_rejected(self):
  with self.assertRaises(ValueError):port_sync.compare('.',{'schema_version':1,'source_commit':'abc','modules':{}},'--output=bad')
