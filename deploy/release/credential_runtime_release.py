"""Codex-only credential worker compose boundary. Contains no secret values."""
import copy
import pathlib

APP_SECRET_KEYS={'CODEX2API_CREDENTIAL_OPS_KEY','CODEX2API_CREDENTIAL_OPS_WORKER_TOKEN'}

def runtime_compose(config,image,app_env,worker_env):
 for raw in (app_env,worker_env):
  path=pathlib.PurePosixPath(raw)
  if not path.is_absolute() or '..' in path.parts or path.parent!=pathlib.PurePosixPath('/opt/codex2api/secrets'):
   raise ValueError('credential env must be in the private Codex secrets directory')
 if not image.startswith('sha256:'):raise ValueError('credential runtime image must be immutable')
 result=copy.deepcopy(config)
 app=result['services']['codex2api']
 environment=app.get('environment',{})
 if APP_SECRET_KEYS.intersection(environment):raise ValueError('compose overrides credential secret files')
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
