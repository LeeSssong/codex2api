"""Codex-only credential worker compose boundary. Contains no secret values."""
import copy
import pathlib
import json
import urllib.parse

APP_SECRET_KEYS={'CODEX2API_CREDENTIAL_OPS_KEY','CODEX2API_CREDENTIAL_OPS_WORKER_TOKEN'}
APP_OPTIONAL_KEYS={'CODEX2API_CREDENTIAL_OPS_SESSION_STUDIO_ENDPOINT','CODEX2API_CREDENTIAL_OPS_SESSION_STUDIO_HEADERS'}

def environment_values(text,required,optional=frozenset()):
 result={}
 for line in text.splitlines():
  if not line.strip() or line.lstrip().startswith('#'):continue
  key,sep,value=line.partition('=')
  if not sep or key not in required|optional or key in result:raise ValueError('invalid dedicated credential environment')
  if key in required and len(value)<32:raise ValueError('credential secret is too short')
  result[key]=value
 if not required.issubset(result):raise ValueError('missing dedicated credential environment')
 endpoint=result.get('CODEX2API_CREDENTIAL_OPS_SESSION_STUDIO_ENDPOINT','')
 if endpoint:
  parsed=urllib.parse.urlsplit(endpoint)
  if parsed.scheme!='https' or not parsed.hostname or parsed.username or parsed.password:raise ValueError('Session Studio requires an explicit HTTPS endpoint')
 headers=result.get('CODEX2API_CREDENTIAL_OPS_SESSION_STUDIO_HEADERS','')
 if headers:
  value=json.loads(headers)
  if not endpoint or not isinstance(value,dict) or any(not isinstance(k,str) or not isinstance(v,str) or '\r' in v or '\n' in v for k,v in value.items()):raise ValueError('invalid Session Studio headers')
 return result

def runtime_compose(config,image,app_env,worker_env):
 for raw in (app_env,worker_env):
  path=pathlib.PurePosixPath(raw)
  if not path.is_absolute() or '..' in path.parts or path.parent!=pathlib.PurePosixPath('/opt/codex2api/secrets'):
   raise ValueError('credential env must be in the private Codex secrets directory')
 if not image.startswith('sha256:'):raise ValueError('credential runtime image must be immutable')
 result=copy.deepcopy(config)
 app=result['services']['codex2api']
 environment=app.get('environment',{})
 if (APP_SECRET_KEYS|APP_OPTIONAL_KEYS).intersection(environment):raise ValueError('compose overrides credential secret files')
 port=int(environment.get('CODEX_PORT',18080))
 if not 1<=port<=65535:raise ValueError('invalid Codex application port')
 networks=app.get('networks',{})
 if 'codex2api-net' not in networks:raise ValueError('dedicated Codex network required')
 files=app.get('env_file',[])
 if isinstance(files,(str,dict)):files=[files]
 if not any((f.get('path') if isinstance(f,dict) else f)==app_env for f in files):
  files=[*files,{'path':app_env,'required':True}]
 app['env_file']=files
 result['services']['credential-runtime']={
  'image':image,'container_name':'codex2api-credential-runtime','restart':'unless-stopped',
  'env_file':[{'path':worker_env,'required':True}],
  'environment':{'CODEX2API_CREDENTIAL_OPS_URL':f'http://codex2api:{port}/api/internal/credential-ops'},
  'networks':{'codex2api-net':None},'read_only':True,'tmpfs':['/tmp:rw,noexec,nosuid,size=67108864'],
  'cpus':0.5,'mem_limit':'512m','pids_limit':128,'stop_grace_period':'300s',
  'cap_drop':['ALL'],'security_opt':['no-new-privileges:true'],
  'logging':{'driver':'json-file','options':{'max-size':'5m','max-file':'2'}},
  'healthcheck':{'test':['CMD','python','/app/worker.py','--healthcheck'],'interval':'15s','timeout':'5s','start_period':'30s','retries':3},
 }
 return result
