#!/usr/bin/env python3
"""Bounded BPS acceptance; run only under exclusive settings-writer ownership.
Never prints secrets, SSE, image nonces or signed URLs. No upstream retry.
Settings API has no CAS: fingerprint checks detect observed concurrent writes,
but cannot exclude an interleaving between GET and PUT; exclusive writer required.
"""
import argparse, base64, datetime as dt, hashlib, hmac, json, os, secrets, signal
import struct, subprocess, time, urllib.error, urllib.parse, urllib.request, zlib

ORIGIN = 'https://codex.xingqiaolab.top'
FIELDS = ('enabled','model_scope','models','image_relay_enabled','image_relay_public_origin')
FONT = ('01110100011001110101110011000101110','00100011000010000100001000010001110','01110100010000100010001000100011111','11110000010000101110000010000111110','00010001100101010010111110001000010','11111100001000011110000010000111110','01110100001000011110100011000101110','11111000010001000100010000100001000','01110100011000101110100011000101110','01110100011000101111000010000101110','01110100011000111111100011000110001','11110100011000111110100011000111110','01111100001000010000100001000001111','11110100011000110001100011000111110','11111100001000011110100001000011111','11111100001000011110100001000010000')
class Failure(Exception): pass

def require(ok, code):
    if not ok: raise Failure(code)
def utc(): return dt.datetime.now(dt.timezone.utc).isoformat().replace('+00:00','Z')
def fingerprint(s): return json.dumps({k:s.get(k) for k in FIELDS + ('image_relay_epoch',)},sort_keys=True)
def png(nonce):
    require(len(nonce)==8 and all(c in '0123456789ABCDEF' for c in nonce),'invalid_nonce')
    scale,pad=12,24; w,h=47*scale+2*pad,7*scale+2*pad
    pixels=bytearray([255])*(w*h)
    for i,c in enumerate(nonce):
        for j,v in enumerate(FONT[int(c,16)]):
            if v=='1':
                for y in range(pad+(j//5)*scale,pad+(j//5+1)*scale):
                    x=pad+(i*6+j%5)*scale; pixels[y*w+x:y*w+x+scale]=bytes(scale)
    def chunk(kind,data): return struct.pack('!I',len(data))+kind+data+struct.pack('!I',zlib.crc32(kind+data)&0xffffffff)
    raw=b''.join(b'\0'+pixels[y*w:(y+1)*w] for y in range(h))
    return b'\x89PNG\r\n\x1a\n'+chunk(b'IHDR',struct.pack('!IIBBBBB',w,h,8,0,0,0,0))+chunk(b'IDAT',zlib.compress(raw))+chunk(b'IEND',b'')
def sse(raw):
    require(len(raw)<=8*1024*1024,'sse_too_large'); completed=None
    for block in raw.decode('utf-8').replace('\r\n','\n').split('\n\n'):
        lines=[x[5:].lstrip() for x in block.splitlines() if x.startswith('data:')]
        if not lines: continue
        data='\n'.join(lines)
        if data=='[DONE]': continue
        try: e=json.loads(data)
        except Exception: raise Failure('invalid_sse_json') from None
        typ=e.get('type','')
        require(typ not in ('error','response.failed','response.incomplete') and not e.get('error'),'sse_error')
        if typ=='response.completed':
            require(completed is None,'duplicate_completion'); completed=e.get('response',{})
            require(completed.get('status')=='completed' and isinstance(completed.get('usage'),dict),'invalid_completion')
    require(completed is not None,'missing_completion'); return completed
class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self,*args,**kwargs): return None
class HTTP:
    def __init__(self): self.opener=urllib.request.build_opener(NoRedirect())
    def request(self,url,method='GET',data=None,headers=None):
        payload=None if data is None else json.dumps(data).encode()
        hdr=dict(headers or {})
        if payload is not None: hdr['Content-Type']='application/json'
        try:
            with self.opener.open(urllib.request.Request(url,data=payload,headers=hdr,method=method),timeout=90) as r:
                body=r.read(8*1024*1024+1); require(len(body)<=8*1024*1024,'body_too_large')
                return r.status,dict(r.headers.items()),body
        except urllib.error.HTTPError as e: return e.code,dict(e.headers.items()),b''
        except Exception: raise Failure('transport_error') from None

def command(args,stdin=None):
    r=subprocess.run(args,input=stdin,text=True,capture_output=True,timeout=30)
    require(r.returncode==0,'local_command_failed'); return r.stdout
class Verifier:
    def __init__(self,args,http=None):
        self.a=args; self.http=http or HTTP(); self.admin_secret=''; self.signing=''; self.key=None; self.key_id=None
        self.name='bps-acceptance-'+secrets.token_hex(8); self.original=None; self.owned=None; self.calls=0; self.urls=[]; self.images=[]
        self.report={'status':'failed','requests':0,'cleanup':{},'limitations':['Settings GET/PUT is not atomic CAS; exclusive settings writer required.']}
    def admin(self,path,method='GET',data=None):
        status,_,body=self.http.request(self.a.admin_base+'/api/admin'+path,method,data,{'X-Admin-Key':self.admin_secret})
        require(200<=status<300,'admin_http_'+str(status))
        return json.loads(body) if body else {}
    def settings(self): return self.admin('/settings/basispoints')
    def save(self,data): return self.admin('/settings/basispoints','PUT',data)
    def load_secrets(self):
        env=json.loads(command(['docker','inspect','--format','{{json .Config.Env}}',self.a.container]))
        env=dict(x.split('=',1) for x in env if '=' in x)
        self.admin_secret=env.get('ADMIN_SECRET','').strip(); self.signing=env.get('IMAGE_ASSET_SIGNING_SECRET','').strip()
        require(self.admin_secret and self.signing,'missing_runtime_secret')
    def setup(self):
        self.load_secrets(); self.original=self.settings(); s=self.original
        require(s.get('enabled') is False,'global_not_initially_disabled')
        runtime=s.get('image_relay_runtime',{}); require(runtime.get('signing_key_configured') is True and runtime.get('backend')=='local','image_relay_runtime_unavailable')
        require(s.get('model_scope')=='all' or self.a.model in s.get('models',[]),'global_model_policy_disallows')
        groups=self.admin('/account-groups').get('groups',[])
        selected=[x for x in groups if x.get('id')==self.a.group_id]
        require(len(selected)==1 and selected[0].get('member_count')==1,'group_not_single_selected_account')
        account=self.admin('/accounts/'+str(self.a.account_id))
        require(self.a.group_id in account.get('group_ids',[]),'account_not_in_selected_group')
        require(account.get('enabled') is True and account.get('status')=='active' and not account.get('agent_identity') and not account.get('openai_responses_api') and not account.get('grok_api') and not account.get('claude_api') and not account.get('allowed_api_key_ids'),'account_unavailable_or_key_bound')
        route=self.admin('/accounts/'+str(self.a.account_id)+'/codex-routes?'+urllib.parse.urlencode({'model':self.a.model}))
        policy=route.get('basispoints_policy',{})
        require(policy.get('model_scope') in ('inherit','all') or self.a.model in policy.get('models',[]),'account_model_policy_disallows')
        # Global-off route denial is expected; inspect static policy before enabling.
        require(any(x.get('upstream')=='basispoints' for x in route.get('paths',[])),'missing_bps_route')
        require(fingerprint(self.settings())==fingerprint(s),'concurrent_settings_change')
        desired={k:s.get(k) for k in FIELDS}; desired.update(enabled=True,image_relay_enabled=True,image_relay_public_origin=ORIGIN)
        self.owned=dict(desired,image_relay_epoch=int(s['image_relay_epoch'])+1)
        self.save(desired); current=self.settings(); require(fingerprint(current)==fingerprint(self.owned),'settings_write_mismatch')
        route=self.admin('/accounts/'+str(self.a.account_id)+'/codex-routes?'+urllib.parse.urlencode({'model':self.a.model}))
        require(any(x.get('upstream')=='basispoints' and x.get('allowed') is True and x.get('health')=='ready' and x.get('capability')!='unsupported' for x in route.get('paths',[])),'bps_route_disallowed')
        self.key='sk-'+secrets.token_hex(24)
        data={'name':self.name,'key':self.key,'quota_limit':0.25,'expires_at':(dt.datetime.now(dt.timezone.utc)+dt.timedelta(hours=1)).isoformat(),'allowed_group_ids':[self.a.group_id],'limits':{'codex_route_policy':'basispoints_only','codex_capability_filter':'any','upstream_channel':'codex','model_allow':[self.a.model],'rpm':6,'rpd':6,'max_concurrency':1,'image_generation_policy':'block','allow_live':False,'auto_compact_overflow':False}}
        try:
            created=self.admin('/keys','POST',data); self.key_id=created.get('id'); require(self.key_id is not None,'missing_key_id')
        except Exception:
            try:
                keys=self.admin('/keys'); keys=keys.get('keys',[]) if isinstance(keys,dict) else keys
                match=[k for k in keys if k.get('name')==self.name]
                if len(match)==1:self.key_id=match[0]['id']
                else:self.report['cleanup']['key_creation_unresolved']=True
            except Exception:self.report['cleanup']['key_creation_unresolved']=True
            raise
        self.report.update(account_id=self.a.account_id,group_id=self.a.group_id,key_id=self.key_id,model=self.a.model)
    def infer(self,history,tools=None):
        require(self.calls<4,'request_limit'); self.calls+=1; self.report['requests']=self.calls
        body={'model':self.a.model,'input':history,'stream':True,'store':False,'reasoning':{'effort':'low'},'max_output_tokens':512}
        if tools: body.update(tools=tools,tool_choice='auto')
        status,headers,raw=self.http.request(ORIGIN+'/v1/responses','POST',body,{'Authorization':'Bearer '+self.key,'Session-Id':self.name})
        require(status==200,'upstream_http_'+str(status)); headers={k.lower():v for k,v in headers.items()}
        require(headers.get('x-codex2api-upstream','').lower()=='basispoints','actual_upstream_not_bps')
        return sse(raw)
    def output_text(self,r): return ''.join(c.get('text','') for o in r.get('output',[]) if o.get('type')=='message' for c in o.get('content',[]) if c.get('type')=='output_text').strip()
    def image_part(self):
        nonce=secrets.token_hex(4).upper(); data=png(nonce); self.images.append(data)
        return nonce,{'type':'input_image','image_url':'data:image/png;base64,'+base64.b64encode(data).decode()}
    def run(self):
        self.setup(); start=utc(); history=[]; responses=[]
        nonce=secrets.token_hex(4).upper(); history.append({'role':'user','content':[{'type':'input_text','text':'Reply with exactly '+nonce}]})
        r=self.infer(history); require(self.output_text(r)==nonce,'text_mismatch'); responses.append(r); history.extend(r['output'])
        nonce,img=self.image_part(); history.append({'role':'user','content':[{'type':'input_text','text':'Read the eight hexadecimal characters in this image. Reply with only those characters.'},img]})
        r=self.infer(history); require(self.output_text(r)==nonce,'image_ocr_mismatch'); responses.append(r); history.extend(r['output'])
        tool={'type':'function','name':'capture_acceptance_screenshot','description':'Capture the current acceptance screenshot.','parameters':{'type':'object','properties':{},'additionalProperties':False,'required':[]},'strict':True}
        history.append({'role':'user','content':[{'type':'input_text','text':'Call capture_acceptance_screenshot once now to get a fresh screenshot. Then read its eight hexadecimal characters and reply with only those characters.'}]})
        r=self.infer(history,[tool]); calls=[x for x in r.get('output',[]) if x.get('type')=='function_call']
        require(len(calls)==1 and calls[0].get('name')==tool['name'] and calls[0].get('call_id'),'tool_call_mismatch'); responses.append(r); history.extend(r['output'])
        nonce,img=self.image_part(); history.append({'type':'function_call_output','call_id':calls[0]['call_id'],'output':[img]})
        r=self.infer(history,[tool]); require(self.output_text(r)==nonce and not any(x.get('type')=='function_call' for x in r.get('output',[])),'tool_image_ocr_mismatch'); responses.append(r)
        self.reconcile(start,responses); self.check_assets()
    def reconcile(self,start,responses):
        deadline=time.monotonic()+30; rows=[]
        while True:
            rows=[]; page=1
            while True:
                query={'start':start,'end':utc(),'page':page,'page_size':20,'api_key_id':self.key_id,'account_id':self.a.account_id}
                result=self.admin('/usage/logs?'+urllib.parse.urlencode(query)); batch=result.get('logs',[]); rows.extend(batch)
                require(len(rows)<=4,'unexpected_usage_count')
                if len(batch)<20: break
                page+=1
            if len(rows)==4:break
            require(time.monotonic()<deadline,'usage_not_ready'); time.sleep(min(1,max(0,deadline-time.monotonic())))
        rows.sort(key=lambda x:x['id']); approved=[]
        for r,row in zip(responses,rows):
            u=r['usage']; details=u.get('input_tokens_details',{})
            require(row.get('api_key_id')==self.key_id and row.get('account_id')==self.a.account_id and row.get('model')==self.a.model,'usage_identity_mismatch')
            for field,value in [('input_tokens',u['input_tokens']),('output_tokens',u['output_tokens']),('cached_tokens',details.get('cached_tokens',0))]: require(row.get(field,0)==value,'usage_'+field+'_mismatch')
            require(row.get('status_code',200)==200 and row.get('attempt_index',1) in (0,1) and not row.get('is_retry_attempt',False),'usage_not_single_success')
            require(row.get('total_tokens')==u['input_tokens']+u['output_tokens'],'usage_total_tokens_mismatch')
            cache_write=details.get('cache_write_tokens',details.get('cache_creation_tokens',u.get('cache_write_tokens',u.get('cache_creation_tokens',u.get('cache_creation_input_tokens',0)))))
            require(row.get('cache_write_5m_tokens',0)+row.get('cache_write_1h_tokens',0)==cache_write,'usage_cache_write_mismatch')
            components=sum(float(row.get(k,0)) for k in ('input_cost','output_cost','cache_read_cost','cache_write_5m_cost','cache_write_1h_cost'))
            require(abs(components-float(row.get('total_cost',0)))<=1e-9,'cost_components_mismatch')
            keep=('id','input_tokens','output_tokens','cached_tokens','cache_write_5m_tokens','cache_write_1h_tokens','input_cost','output_cost','cache_read_cost','cache_write_5m_cost','cache_write_1h_cost','total_cost','account_billed','user_billed','image_input_cost','image_cache_read_cost')
            approved.append({k:row[k] for k in keep if k in row})
        self.report['response_usage']=[r['usage'] for r in responses]; self.report['usage']=approved; self.report['total_cost']=sum(float(x.get('total_cost',0)) for x in rows)
        require(self.report['total_cost']<=0.25,'budget_exceeded')
    def check_assets(self):
        scope=hashlib.sha256((str(self.a.account_id)+'|'+hashlib.sha256(self.key.encode()).hexdigest()).encode()).hexdigest()
        sql="BEGIN READ ONLY; SELECT COALESCE(json_agg(x),'[]') FROM (SELECT id,relay_expires_at,relay_digest,relay_epoch FROM image_assets WHERE model='bps-inbound' AND relay_state='ready' AND relay_scope_hash='"+scope+"') x; COMMIT;"
        output=command(['docker','exec','-i',self.a.db_container,'sh','-c','exec psql -X -qAt -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"'],sql)
        assets=json.loads(output.strip()); require(len(assets)==2,'asset_count_mismatch')
        by_digest={hashlib.sha256(x).hexdigest():x for x in self.images}
        for asset in assets:
            expiry=asset['relay_expires_at']; expiry=int(expiry) if isinstance(expiry,(int,float)) else int(dt.datetime.fromisoformat(expiry.replace('Z','+00:00')).timestamp())
            require(expiry>time.time() and asset['relay_epoch']==self.owned['image_relay_epoch'],'asset_epoch_or_expiry_mismatch')
            identifier=asset['id']; sig=hmac.new(hashlib.sha256(self.signing.encode()).digest(),(str(identifier)+'|'+str(expiry)+'|0').encode(),hashlib.sha256).digest()[:16].hex()
            url=ORIGIN+'/p/img/'+str(identifier)+'?'+urllib.parse.urlencode({'exp':expiry,'sig':sig})
            self.urls.append(url); status,headers,raw=self.http.request(url); h={k.lower():v for k,v in headers.items()}
            require(status==200 and 'no-store' in h.get('cache-control','').lower() and h.get('content-type','').startswith('image/png') and h.get('x-content-type-options','').lower()=='nosniff','asset_http_or_cache_mismatch')
            require(raw==by_digest.get(asset['relay_digest']),'asset_bytes_mismatch')
        self.report['image_assets_verified']=2
    def cleanup(self):
        errors=[]
        if self.key_id is not None:
            try:self.admin('/keys/'+str(self.key_id),'DELETE'); self.report['cleanup']['key_revoked']=True
            except Exception:errors.append('key_revoke_failed')
        if self.owned is not None:
            try:
                current=self.settings()
                if fingerprint(current)==fingerprint(self.original): self.report['cleanup']['global_restored']=True
                else:
                    require(fingerprint(current)==fingerprint(self.owned),'concurrent_settings_change_no_overwrite')
                    restored={k:current.get(k) for k in FIELDS}; restored['enabled']=self.original['enabled']; self.save(restored)
                    final=self.settings(); require(final.get('enabled') is False and int(final['image_relay_epoch'])>int(current['image_relay_epoch']),'global_restore_verification_failed')
                    self.report['cleanup']['global_restored']=True
                    for url in self.urls: require(self.http.request(url)[0] in (403,404),'old_asset_url_still_available')
                    self.report['cleanup']['old_signed_urls_rejected']=len(self.urls)
            except Failure as e:errors.append(str(e))
            except Exception:errors.append('settings_restore_failed')
        self.report['cleanup']['errors']=errors
        return not errors and not self.report['cleanup'].get('key_creation_unresolved')
    def execute(self):
        ok=False
        try:self.run(); ok=True
        except Failure as e:self.report['failure']=str(e)
        except (KeyboardInterrupt,SystemExit):self.report['failure']='interrupted'
        except Exception:self.report['failure']='unexpected_error'
        finally:
            clean=self.cleanup(); self.report['status']='passed' if ok and clean else 'failed'
        return self.report

def main():
    p=argparse.ArgumentParser(description=__doc__)
    for name in ('account-id','group-id'):p.add_argument('--'+name,type=int,required=True)
    for name in ('model','output','container','db-container'):p.add_argument('--'+name,required=True)
    p.add_argument('--admin-base',default='http://127.0.0.1:18080'); a=p.parse_args()
    require(urllib.parse.urlparse(a.admin_base).hostname in ('127.0.0.1','localhost','::1'),'admin_not_loopback')
    os.umask(0o077); fd=os.open(a.output,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
    signal.signal(signal.SIGTERM,lambda *_:(_ for _ in ()).throw(KeyboardInterrupt()))
    result=Verifier(a).execute()
    with os.fdopen(fd,'w') as f:json.dump(result,f,indent=2); f.write('\n')
    print(json.dumps({'status':result['status'],'report_written':True})); return 0 if result['status']=='passed' else 1
if __name__=='__main__':raise SystemExit(main())
