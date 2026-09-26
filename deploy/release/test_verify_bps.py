import json, struct, types, unittest, zlib
from verify_bps import Failure, Verifier, png, sse
class Checks(unittest.TestCase):
    def test_png(self):
        data=png('0123ABEF'); self.assertTrue(data.startswith(b'\x89PNG\r\n\x1a\n')); pos=8; kinds=[]; raw=b''
        while pos<len(data):
            size=struct.unpack('!I',data[pos:pos+4])[0]; kind=data[pos+4:pos+8]; payload=data[pos+8:pos+8+size]
            self.assertEqual(zlib.crc32(kind+payload)&0xffffffff,struct.unpack('!I',data[pos+8+size:pos+12+size])[0]); kinds.append(kind)
            if kind==b'IHDR':self.assertEqual(struct.unpack('!IIBBBBB',payload),(612,132,8,0,0,0,0))
            if kind==b'IDAT':raw+=payload
            pos+=12+size
        self.assertEqual(kinds,[b'IHDR',b'IDAT',b'IEND']); self.assertEqual(len(zlib.decompress(raw)),613*132)
        self.assertNotIn(b'0123ABEF',data); self.assertNotEqual(data,png('0123ABEE'))
    def event(self,typ):return ('data: '+json.dumps({'type':typ,'response':{'status':'completed','usage':{'input_tokens':1}}})+'\n\n').encode()
    def test_sse_completion(self):self.assertEqual(sse(self.event('response.completed'))['usage']['input_tokens'],1)
    def test_sse_missing_error_duplicate(self):
        for data in (b'data: [DONE]\n\n',self.event('response.failed'),self.event('response.completed')+self.event('error'),self.event('response.completed')*2):
            with self.assertRaises(Failure):sse(data)
    def fake(self,conflict=False,delete_error=False):
        class Fake(Verifier):
            def run(self):
                self.original={'enabled':False,'model_scope':'all','models':[],'image_relay_enabled':False,'image_relay_public_origin':'','image_relay_epoch':2}
                self.owned=dict(self.original,enabled=True,image_relay_enabled=True,image_relay_public_origin='https://codex.xingqiaolab.top',image_relay_epoch=3)
                self.current=dict(self.owned); self.key_id=7; self.operations=[]
                if conflict:self.current['image_relay_epoch']=99
                raise Failure('upstream_http_403')
            def admin(self,path,method='GET',data=None):
                self.operations.append((path,method))
                if delete_error:raise Failure('delete_failed')
                return {}
            def settings(self):return self.current
            def save(self,data):self.operations.append(('settings','PUT')); self.current=dict(data,image_relay_epoch=self.current['image_relay_epoch']+1)
        return Fake(types.SimpleNamespace())
    def test_failure_cleanup(self):
        v=self.fake(); r=v.execute(); self.assertEqual(r['status'],'failed'); self.assertTrue(r['cleanup']['key_revoked']); self.assertTrue(r['cleanup']['global_restored']); self.assertFalse(v.current['enabled']); self.assertTrue(v.current['image_relay_enabled'])
    def test_disabled_asset_cleanup_accepts_403_or_404(self):
        for status in (403,404):
            with self.subTest(status=status):
                v=self.fake()
                v.urls=['https://example.invalid/signed-asset']
                v.http=types.SimpleNamespace(request=lambda url, code=status: (code,{},b''))
                r=v.execute()
                self.assertTrue(r['cleanup']['key_revoked'])
                self.assertTrue(r['cleanup']['global_restored'])
                self.assertEqual(r['cleanup']['old_signed_urls_rejected'],1)
                self.assertEqual(r['cleanup']['errors'],[])
    def test_conflict_no_overwrite(self):
        v=self.fake(conflict=True); r=v.execute(); self.assertIn('concurrent_settings_change_no_overwrite',r['cleanup']['errors']); self.assertNotIn(('settings','PUT'),v.operations)
    def test_revoke_failure_still_restores(self):
        v=self.fake(delete_error=True); r=v.execute(); self.assertTrue(r['cleanup']['global_restored']); self.assertIn('key_revoke_failed',r['cleanup']['errors'])
if __name__=='__main__':unittest.main()
