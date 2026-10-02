#!/usr/bin/env python3
"""Build the independent credential runtime from the same verified source tree."""
import argparse
import json
import pathlib
import re
import subprocess
import tarfile
import tempfile
from release_build import command, docker_command, verify_source

def checked_dependencies(value):
 expected={'tosub2':'https://github.com/poxiao33/toSub2.git','turb':'https://github.com/myfanhua/turb-gpt-free-register.git'}
 if set(value)!=set(expected):raise ValueError('both locked login engines are required')
 for name,url in expected.items():
  item=value[name]
  if item.get('repository')!=url or not re.fullmatch('[0-9a-f]{40}',item.get('commit','')):raise ValueError('invalid locked engine source')
 return value

def build(root,upstream,output,host,builder):
 root=pathlib.Path(root).resolve();revision,tree=verify_source(root,upstream)
 docker_command(host)
 output=pathlib.Path(output).resolve();output.mkdir(mode=0o700,parents=True,exist_ok=False)
 image='codex2api-credential-runtime:release-'+revision[:12]
 with tempfile.TemporaryDirectory(prefix='codex2api-runtime-build-') as folder:
  temp=pathlib.Path(folder);source=temp/'source';source.mkdir();archive=temp/'source.tar'
  with archive.open('wb') as out:subprocess.run(['git','archive',revision],cwd=root,stdout=out,check=True)
  with tarfile.open(archive) as package:package.extractall(source)
  runtime=source/'tools'/'credential-runtime'
  deps=checked_dependencies(json.loads((runtime/'dependencies.json').read_text()))
  contexts=[]
  for name,item in deps.items():
   repo=temp/(name+'-git');subprocess.run(['git','init','-q',str(repo)],check=True)
   subprocess.run(['git','-C',str(repo),'fetch','-q','--depth','1',item['repository'],item['commit']],check=True)
   if command(['git','rev-parse','FETCH_HEAD'],repo)!=item['commit']:raise ValueError('engine commit mismatch')
   exported=temp/name;exported.mkdir();engine_archive=temp/(name+'.tar')
   with engine_archive.open('wb') as out:subprocess.run(['git','archive','FETCH_HEAD'],cwd=repo,stdout=out,check=True)
   with tarfile.open(engine_archive) as package:package.extractall(exported)
   contexts+=['--build-context',name+'='+str(exported)]
  args=docker_command(host,'buildx','build','--builder',builder,'--platform','linux/amd64','--load','--label','org.opencontainers.image.revision='+revision,'--label','io.xingqiao.source-tree='+tree,'--label','io.xingqiao.upstream-revision='+upstream,'--label','io.xingqiao.plugin-sdk=plugins/v1','-t',image)
  subprocess.run(args+contexts+[str(runtime)],check=True)
 metadata=json.loads(command(docker_command(host,'image','inspect',image),root))[0]
 labels=metadata['Config'].get('Labels',{})
 if metadata['Architecture']!='amd64' or labels.get('org.opencontainers.image.revision')!=revision or labels.get('io.xingqiao.source-tree')!=tree:raise ValueError('runtime source labels mismatch')
 subprocess.run(docker_command(host,'run','--rm','--network','none','--memory','256m','--cpus','0.5',metadata['Id'],'--check'),check=True)
 verify_source(root,upstream)
 manifest={'revision':revision,'tree':tree,'upstream_revision':upstream,'image':image,'digest':metadata['Id'],'dependencies':deps}
 (output/'manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')
 with (output/'image.tar').open('wb') as out:subprocess.run(docker_command(host,'save',image),stdout=out,check=True)
 print(json.dumps(manifest))

if __name__=='__main__':
 p=argparse.ArgumentParser(description=__doc__)
 for name in ['root','upstream','output','builder']:p.add_argument('--'+name,required=True)
 p.add_argument('--docker-host',choices=['ssh://sub2api-prod']);a=p.parse_args()
 build(a.root,a.upstream,a.output,a.docker_host,a.builder)
