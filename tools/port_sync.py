#!/usr/bin/env python3
"""Compare a separately supplied source checkout with the pinned port baseline.

Read-only: never fetches, checks out, copies files, updates manifests or deploys.
Unmapped paths are reported because shared source changes may affect adapters.
"""
import argparse
import json
import pathlib
import subprocess

def compare(source,manifest,target):
 if manifest.get('schema_version')!=1:raise ValueError('unsupported port manifest')
 for ref in (manifest['source_commit'],target):
  if not isinstance(ref,str) or not ref or ref.startswith('-') or '\x00' in ref:raise ValueError('invalid source revision')
 root=pathlib.Path(source).resolve()
 def git(*args):return subprocess.check_output(['git','-C',str(root),*args],text=True).strip()
 base=git('rev-parse','--verify',manifest['source_commit']+'^{commit}')
 head=git('rev-parse','--verify',target+'^{commit}')
 changed=[name for name in git('diff','--name-only',base,head,'--').splitlines() if name]
 modules={name:[path for path in changed if any(path.startswith(prefix) for prefix in prefixes)] for name,prefixes in manifest['modules'].items()}
 mapped={path for paths in modules.values() for path in paths}
 return {'baseline_commit':base,'source_commit':head,'modules':modules,'unmapped_files':[path for path in changed if path not in mapped],'action':'review-and-port-in-codex2api','source_modified':False}

if __name__=='__main__':
 parser=argparse.ArgumentParser(description=__doc__)
 parser.add_argument('--source',required=True,help='Independent source Git checkout; read-only')
 parser.add_argument('--manifest',default=str(pathlib.Path(__file__).resolve().parents[1]/'plugins'/'ports.json'))
 parser.add_argument('--to',default='HEAD')
 args=parser.parse_args()
 print(json.dumps(compare(args.source,json.loads(pathlib.Path(args.manifest).read_text()),args.to),ensure_ascii=False,indent=2))
