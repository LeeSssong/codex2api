import copy,unittest
from release_blue_green import change_upstream

class RoutingTests(unittest.TestCase):
 def test_changes_only_codex_upstream(self):
  source={'apps':{'http':{'servers':{'main':{'routes':[
   {'match':[{'host':['codex.xingqiaolab.top']}],'handle':[{'handler':'reverse_proxy','upstreams':[{'dial':'172.18.0.1:18080'}]}]},
   {'match':[{'host':['api.xingqiaolab.top']}],'handle':[{'handler':'reverse_proxy','upstreams':[{'dial':'sub:8080'}]}]},
  ]}}}}}
  before=copy.deepcopy(source);result=change_upstream(source,18080,18081)
  self.assertEqual(source,before)
  routes=result['apps']['http']['servers']['main']['routes']
  self.assertEqual(routes[0]['handle'][0]['upstreams'][0]['dial'],'172.18.0.1:18081')
  self.assertEqual(routes[1],before['apps']['http']['servers']['main']['routes'][1])
  with self.assertRaises(ValueError):change_upstream(source,9999,18081)

if __name__=='__main__':unittest.main()
