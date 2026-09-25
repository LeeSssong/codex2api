#!/usr/bin/env python3
"""Build once from verified, clean and pushed main; retain an immutable image."""
import argparse, datetime, json, os, pathlib, subprocess, tarfile, tempfile

def command(args, cwd, **kwargs):
 return subprocess.check_output(args, cwd=cwd, text=True, **kwargs).strip()

def verify_source(root, upstream):
 root=pathlib.Path(root).resolve()
 if pathlib.Path(command(['git','rev-parse','--show-toplevel'],root)).resolve()!=root:raise ValueError('must use the repository root')
 if command(['git','symbolic-ref','--short','HEAD'],root)!='main':raise ValueError('release requires main, not a feature branch or detached HEAD')
 if command(['git','status','--porcelain'],root):raise ValueError('release requires a clean working tree')
 revision=command(['git','rev-parse','HEAD'],root); tree=command(['git','rev-parse','HEAD^{tree}'],root)
 remote=command(['git','ls-remote','--exit-code','origin','refs/heads/main'],root).split()[0]
 if revision!=remote or command(['git','rev-parse','origin/main'],root)!=remote:raise ValueError('main must match freshly verified origin/main')
 command(['git','merge-base','--is-ancestor',upstream,revision],root)
 return revision,tree

def build(root, upstream, output):
 root=pathlib.Path(root).resolve();revision,tree=verify_source(root,upstream)
 output=pathlib.Path(output).resolve();output.mkdir(mode=0o700,parents=True,exist_ok=False)
 image='codex2api:smartops-'+revision[:12]
 build_version='smartops-'+datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%d')+'-'+revision[:12]
 with tempfile.TemporaryDirectory(prefix='codex2api-verified-build-') as tmp:
  source=pathlib.Path(tmp)/'source';source.mkdir()
  archive=pathlib.Path(tmp)/'source.tar'
  with archive.open('wb') as out:subprocess.run(['git','archive','--format=tar',revision],cwd=root,stdout=out,check=True)
  with tarfile.open(archive) as tar:tar.extractall(source)
  env=os.environ.copy();env['VITE_APP_VERSION']=build_version
  subprocess.run(['npm','ci','--no-audit','--no-fund'],cwd=source/'frontend',env=env,check=True)
  subprocess.run(['npm','run','build'],cwd=source/'frontend',env=env,check=True)
  env.update({'CGO_ENABLED':'0','GOOS':'linux','GOARCH':'amd64'})
  runtime=pathlib.Path(tmp)/'runtime';runtime.mkdir();binary=runtime/'codex2api'
  flags='-s -w -X github.com/codex2api/internal/version.Version='+build_version+' -X github.com/codex2api/internal/version.Revision='+revision+' -X github.com/codex2api/internal/version.SourceTree='+tree+' -X github.com/codex2api/internal/version.UpstreamRevision='+upstream
  subprocess.run(['go','build','-trimpath','-ldflags='+flags,'-o',str(binary),'.'],cwd=source,env=env,check=True)
  subprocess.run(['docker','buildx','build','--load','--platform','linux/amd64','-f',str(source/'deploy/release/Dockerfile.runtime'),'--build-arg','SOURCE_REVISION='+revision,'--build-arg','SOURCE_TREE='+tree,'--build-arg','UPSTREAM_REVISION='+upstream,'-t',image,str(runtime)],check=True)
 metadata=json.loads(command(['docker','image','inspect',image],root))[0]
 labels=metadata['Config'].get('Labels',{})
 if metadata['Architecture']!='amd64' or labels.get('org.opencontainers.image.revision')!=revision or labels.get('io.xingqiao.source-tree')!=tree:raise ValueError('built image provenance mismatch')
 verify_source(root,upstream)
 manifest={'revision':revision,'tree':tree,'upstream_revision':upstream,'image':image,'digest':metadata['Id'],'build_version':build_version}
 (output/'manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')
 subprocess.run(['docker','save','-o',str(output/'image.tar'),image],check=True)
 print(json.dumps(manifest))

if __name__=='__main__':
 p=argparse.ArgumentParser();p.add_argument('--root',required=True);p.add_argument('--upstream',required=True);p.add_argument('--output',required=True);a=p.parse_args();build(a.root,a.upstream,a.output)
