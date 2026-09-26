#!/usr/bin/env python3
"""Two upstream requests only: tool call then fresh screenshot OCR.
Place beside verify_bps.py. Inherits exclusive-writer setup and finally cleanup.
"""
from verify_bps import *
import re

def phase(name): print(name,flush=True)
def classify(reply, expected=None):
    text=''.join(c.get('text','') for o in reply.get('output',[]) if o.get('type')=='message' for c in o.get('content',[]) if c.get('type')=='output_text').strip()
    candidates=re.findall(r'(?<![A-Za-z0-9])[0-9A-Fa-f]{8}(?![A-Za-z0-9])',text)
    unique={x.upper() for x in candidates}
    refusal=any(c.get('type')=='refusal' for o in reply.get('output',[]) for c in o.get('content',[]) if isinstance(c,dict))
    low=text.lower()
    unreadable=any(p in low for p in ("cannot read", "can't read", "unable to read", "cannot access", "can't access", "unable to access", "cannot view", "can't view", "unable to view", "cannot see", "can't see", "unable to see", "无法读取", "无法查看", "无法访问", "看不到", "无法辨认"))
    known={'message','reasoning','function_call','custom_tool_call'}
    kinds=sorted({x.get('type') if x.get('type') in known else 'other' for x in reply.get('output',[])})
    return {'reply_length':len(text),'output_types':kinds,'hex_candidate_count':len(candidates),'unique_hex_candidate_count':len(unique),'correct':expected is not None and unique=={expected.upper()} and not refusal and not unreadable,'refusal':refusal,'unreadable_statement':unreadable}

class AcceptanceHTTP(HTTP):
    def request(self,url,method='GET',data=None,headers=None):
        headers=dict(headers or {})
        headers.setdefault('User-Agent','Mozilla/5.0 (compatible; BPS-Acceptance/1.0)')
        return super().request(url,method,data,headers)

class ToolImageVerifier(Verifier):
    def __init__(self,args):
        super().__init__(args,AcceptanceHTTP())
        self.report['response_diagnostics']=[]
    def infer(self,history,tools=None):
        require(self.calls<2,'two_request_limit')
        phase('upstream_request_'+str(self.calls+1))
        response=super().infer(history,tools)
        # Store numeric usage and structural diagnostics immediately, before OCR assertions.
        self.report['response_diagnostics'].append({'request':self.calls,'usage':response['usage'],**classify(response)})
        return response
    def cleanup(self):
        phase('cleanup')
        return super().cleanup()
    def run(self):
        phase('setup'); self.setup(); start=utc(); self.report['started_at']=start
        tool={'type':'function','name':'capture_acceptance_screenshot','description':'Capture the current acceptance screenshot.','parameters':{'type':'object','properties':{},'additionalProperties':False,'required':[]},'strict':True}
        history=[{'role':'user','content':[{'type':'input_text','text':'Call capture_acceptance_screenshot once now. When its screenshot is returned, read the eight hexadecimal characters shown and reply with only those characters.'}]}]
        first=self.infer(history,[tool]); calls=[x for x in first.get('output',[]) if x.get('type')=='function_call']
        require(len(calls)==1 and calls[0].get('name')==tool['name'] and calls[0].get('call_id'),'tool_call_mismatch')
        history.extend(first['output']); nonce,img=self.image_part()
        history.append({'type':'function_call_output','call_id':calls[0]['call_id'],'output':[img]})
        second=self.infer(history,[tool]); diagnostics=classify(second,nonce)
        self.report['response_diagnostics'][-1].update(diagnostics)
        phase('verify_image_asset'); self.check_assets()
        phase('reconcile_usage'); self.reconcile(start,[first,second])
        phase('verify_ocr')
        require(not any(x.get('type')=='function_call' for x in second.get('output',[])),'unexpected_extra_tool_call')
        require(diagnostics['correct'],'tool_image_ocr_mismatch')
        self.report['finished_at']=utc()
    def reconcile(self,start,responses):
        deadline=time.monotonic()+30; rows=[]
        while True:
            rows=[]; page=1
            while True:
                query={'start':start,'end':utc(),'page':page,'page_size':20,'api_key_id':self.key_id,'account_id':self.a.account_id}
                result=self.admin('/usage/logs?'+urllib.parse.urlencode(query)); batch=result.get('logs',[]); rows.extend(batch)
                require(len(rows)<=len(responses),'unexpected_usage_count')
                if len(batch)<20: break
                page+=1
            if len(rows)==len(responses):break
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
        assets=json.loads(output.strip()); require(len(assets)==len(self.images),'asset_count_mismatch')
        by_digest={hashlib.sha256(x).hexdigest():x for x in self.images}
        for asset in assets:
            expiry=asset['relay_expires_at']; expiry=int(expiry) if isinstance(expiry,(int,float)) else int(dt.datetime.fromisoformat(expiry.replace('Z','+00:00')).timestamp())
            require(expiry>time.time() and asset['relay_epoch']==self.owned['image_relay_epoch'],'asset_epoch_or_expiry_mismatch')
            identifier=asset['id']; sig=hmac.new(hashlib.sha256(self.signing.encode()).digest(),(str(identifier)+'|'+str(expiry)+'|0').encode(),hashlib.sha256).digest()[:16].hex()
            url=ORIGIN+'/p/img/'+str(identifier)+'?'+urllib.parse.urlencode({'exp':expiry,'sig':sig})
            self.urls.append(url); status,headers,raw=self.http.request(url); h={k.lower():v for k,v in headers.items()}
            require(status==200 and 'no-store' in h.get('cache-control','').lower() and h.get('content-type','').startswith('image/png') and h.get('x-content-type-options','').lower()=='nosniff','asset_http_or_cache_mismatch')
            require(raw==by_digest.get(asset['relay_digest']),'asset_bytes_mismatch')
        self.report['image_assets_verified']=len(self.images)

def main():
    p=argparse.ArgumentParser(description=__doc__)
    for name in ('account-id','group-id'):p.add_argument('--'+name,type=int,required=True)
    for name in ('model','output','container','db-container'):p.add_argument('--'+name,required=True)
    p.add_argument('--admin-base',default='http://127.0.0.1:18080'); a=p.parse_args()
    require(urllib.parse.urlparse(a.admin_base).hostname in ('127.0.0.1','localhost','::1'),'admin_not_loopback')
    os.umask(0o077); fd=os.open(a.output,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
    signal.signal(signal.SIGTERM,lambda *_:(_ for _ in ()).throw(KeyboardInterrupt()))
    result=ToolImageVerifier(a).execute()
    with os.fdopen(fd,'w') as f:json.dump(result,f,indent=2); f.write(chr(10))
    phase('report_written_'+result['status']); return 0 if result['status']=='passed' else 1
if __name__=='__main__':raise SystemExit(main())
