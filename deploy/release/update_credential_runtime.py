#!/usr/bin/env python3
"""Upgrade only the installed credential executor. Never migrates the app DB."""
import argparse, copy, fcntl, json, os, pathlib, subprocess, time, urllib.request
from release_host import checked_file, atomic_restore, check_source, assert_unchanged_containers

def replacement(config,image):
 if not image.startswith('sha256:'):raise ValueError('immutable runtime image required')
 old=config.get('services',{}).get('credential-runtime')
 if not old or old.get('container_name')!='codex2api-credential-runtime':raise ValueError('credential runtime must already be installed')
 result=copy.deepcopy(config);result['services']['credential-runtime']['image']=image
 return result

def update(image,revision,tree,release_id):
 root=pathlib.Path('/opt/codex2api');compose,info=checked_file(root/'docker-compose.yml',private=True)
 folder=root/'backups'/release_id;folder.mkdir(mode=0o700,exist_ok=False)
 def run(args,timeout=60):
  with (folder/'commands.log').open('ab') as log:
   result=subprocess.run(args,stdout=subprocess.PIPE,stderr=log,timeout=timeout)
  if result.returncode:raise RuntimeError('runtime operation failed; see protected command log')
  return result.stdout
 def inspect(name):return json.loads(run(['docker','inspect',name]))[0]
 def protected():
  result={}
  for name in run(['docker','ps','--format','{{.Names}}']).decode().splitlines():
   if name=='codex2api-credential-runtime':continue
   c=inspect(name);result[name]={'id':c['Id'],'started_at':c['State']['StartedAt']}
  return result
 def dc(*args,timeout=60):return run(['docker','compose','--project-directory',str(root),'-f',str(compose),*args],timeout)
 candidate=json.loads(run(['docker','image','inspect',image]))[0]
 check_source(candidate['Config'].get('Labels',{}),revision,tree)
 if candidate['Architecture']!='amd64' or candidate['Config'].get('Labels',{}).get('io.xingqiao.plugin-sdk')!='plugins/v1':raise ValueError('runtime architecture or SDK incompatible')
 current=inspect('codex2api-credential-runtime');before=compose.read_bytes();config=json.loads(before)
 after=replacement(config,candidate['Id'])
 rollback=replacement(config,current['Image'])
 (folder/'compose.before.json').write_bytes(before)
 (folder/'compose.rollback.json').write_text(json.dumps(rollback,indent=2)+'\n')
 app=inspect('codex2api');env=dict(item.split('=',1) for item in app['Config']['Env']);secret=env.get('ADMIN_SECRET')
 if not secret:raise ValueError('admin credential required for compatibility preflight')
 port=int(env.get('CODEX_PORT',18080))
 req=urllib.request.Request(f'http://127.0.0.1:{port}/api/admin/plugins',headers={'X-Admin-Key':secret})
 with urllib.request.urlopen(req,timeout=15) as rsp:plugins=json.load(rsp)['plugins']
 if not any(p['id']=='credential-ops' and p['sdk_compatibility']=='plugins/v1' for p in plugins):raise ValueError('installed host does not support this runtime SDK')
 protected_before=protected();started=time.monotonic();switched=False
 report={'revision':revision,'tree':tree,'runtime_digest':candidate['Id'],'old_runtime_digest':current['Image'],'result':'pending','rolled_back':False}
 def save():
  report['elapsed_seconds']=round(time.monotonic()-started,2)
  (folder/'release.json').write_text(json.dumps(report,indent=2)+'\n')
 def healthy(expected):
  deadline=time.monotonic()+90
  while time.monotonic()<deadline:
   c=inspect('codex2api-credential-runtime')
   if c['Image']==expected and c['State'].get('Health',{}).get('Status')=='healthy':return
   time.sleep(1)
  raise RuntimeError('credential runtime readiness timed out')
 save()
 try:
  run(['docker','run','--rm','--network','none','--memory','256m','--cpus','0.5',candidate['Id'],'--check'])
  switched=True
  dc('stop','-t','300','credential-runtime',timeout=330)
  atomic_restore(compose,(json.dumps(after,indent=2)+'\n').encode(),0o600,info.st_uid,info.st_gid)
  dc('config','--quiet');dc('up','-d','--no-deps','credential-runtime',timeout=120)
  healthy(candidate['Id']);assert_unchanged_containers(protected_before,protected())
  with urllib.request.urlopen(f'http://127.0.0.1:{port}/health',timeout=15) as rsp:
   if rsp.status!=200:raise RuntimeError('application health failed')
  report['result']='success';save()
 except BaseException:
  report['result']='failed';save()
  if switched:
   dc('stop','-t','300','credential-runtime',timeout=330)
   atomic_restore(compose,(json.dumps(rollback,indent=2)+'\n').encode(),0o600,info.st_uid,info.st_gid)
   dc('up','-d','--no-deps','credential-runtime',timeout=120);healthy(current['Image'])
   assert_unchanged_containers(protected_before,protected());report['rolled_back']=True;save()
  raise
 print(json.dumps(report))

if __name__=='__main__':
 import re
 parser=argparse.ArgumentParser(description=__doc__)
 for name in ['image','revision','tree','release-id']:parser.add_argument('--'+name,required=True)
 args=parser.parse_args()
 if not re.fullmatch('[a-zA-Z0-9._-]+',args.release_id):parser.error('invalid release ID')
 os.umask(0o077)
 with open('/var/lock/codex2api-release.lock','w') as lock:
  fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
  update(args.image,args.revision,args.tree,args.release_id)
