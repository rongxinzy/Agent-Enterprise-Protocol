// Local integration adapter for main runtime + PR358; never changes business state via SQL.
import assert from 'node:assert/strict';
import http from 'node:http';
import {createHash} from 'node:crypto';

export async function higressAccess(ctx, alice, bob, model) {
  const {kubectl, apply, forward, until, report, record} = ctx;
  const metadata = name => ({name, namespace: 'aep-system'});
  const code = `import http from 'node:http';
let calls=0;
http.createServer(async(req,res)=>{
  if(req.url==='/stats')return res.end(JSON.stringify({calls}));
  let text='';for await(const chunk of req)text+=chunk;
  let body;try{body=JSON.parse(text);}catch{res.writeHead(400).end('{}');return;}
  calls++;res.setHeader('content-type','application/json');
  res.end(JSON.stringify({id:'disposable',object:'chat.completion',created:1,model:body.model,
    choices:[{index:0,message:{role:'assistant',content:'probe'},finish_reason:'stop'}],
    usage:{prompt_tokens:1,completion_tokens:1,total_tokens:2},path:req.url,host:req.headers.host,
    providerAuthenticated:req.headers.authorization==='Bearer '+process.env.PROVIDER_KEY,
    leaked:Object.keys(req.headers).filter(k=>['x-aep-access-binding','x-aep-access-observation','x-aep-forged','x-consumer-id','x-ext-auth-host'].includes(k)),
    identity:Object.fromEntries(Object.entries(req.headers).filter(([name])=>name.startsWith('x-aep-')||name==='x-mse-consumer')),
    validatedModel:req.headers['x-aep-model-id']}));
}).listen(8080,'0.0.0.0');`;
  await apply([
    {apiVersion:'v1',kind:'ConfigMap',metadata:metadata('access-upstream'),data:{'server.mjs':code}},
    {apiVersion:'apps/v1',kind:'Deployment',metadata:metadata('access-upstream'),spec:{replicas:1,
      selector:{matchLabels:{app:'access-upstream'}},template:{metadata:{labels:{app:'access-upstream'}},spec:{
        automountServiceAccountToken:false,securityContext:{runAsNonRoot:true,runAsUser:1000,seccompProfile:{type:'RuntimeDefault'}},
        containers:[{name:'mock',image:'node:24-alpine',command:['node','/code/server.mjs'],
          env:[{name:'PROVIDER_KEY',valueFrom:{secretKeyRef:{name:'access-upstream-credential',key:'api-key'}}}],
          securityContext:{allowPrivilegeEscalation:false,readOnlyRootFilesystem:true,capabilities:{drop:['ALL']}},
          volumeMounts:[{name:'code',mountPath:'/code',readOnly:true}],readinessProbe:{tcpSocket:{port:8080}}}],
        volumes:[{name:'code',configMap:{name:'access-upstream'}}]}}}},
    {apiVersion:'v1',kind:'Service',metadata:metadata('access-upstream'),spec:{selector:{app:'access-upstream'},ports:[{port:8080}]}},
  ]);
  await kubectl(['apply','-k','deploy/kubernetes/access-ingress']);
  await kubectl(['-n','aep-system','rollout','status','deployment/access-upstream','--timeout=120s']);
  console.log('Waiting for managed Higress inference handoff');
  let port=await forward('deployment/higress-gateway',80,'higress-system');
  const statsPort=await forward('deployment/access-upstream',8080);
  const stats=async()=> (await(await fetch('http://127.0.0.1:'+statsPort+'/stats')).json()).calls;
  const call=(person, id=model.id, options={})=>new Promise((resolve,reject)=>{
    // Each assertion targets the currently loaded listener. An existing HTTP/1
    // keep-alive connection can retain a draining listener's old filter chain
    // after adding a WasmPlugin. This does NOT prove adoption on old connections.
    const req=http.request({agent:false,host:'127.0.0.1',port,path:'/v1/chat/completions',method:'POST',timeout:options.timeout??6000,headers:{
      host:options.host??'shared.access.test',authorization:'Bearer '+(options.token??person.tokens.modelAccessToken),
      'content-type':'application/json','x-aep-access-binding':options.binding??person.binding,...options.headers}},res=>{
      let body='';res.on('data',chunk=>body+=chunk);res.on('end',()=>resolve({status:res.statusCode,
        observation:res.headers['x-aep-access-observation'],body:res.statusCode===200?JSON.parse(body):body}));
    });req.on('error',reject);req.on('timeout',()=>req.destroy(new Error('Higress timeout')));
    req.end(options.body??JSON.stringify({model:id,messages:[{role:'user',content:'probe'}]}));
  }).catch(async error=>{port=await forward('deployment/higress-gateway',80,'higress-system');throw error;});
  const route=async person=>JSON.parse(await kubectl(['-n','higress-system','get','envoyfilter','access-'+person.binding,'-o','json']));
  async function check(person,status,id=model.id){
    const result=await call(person,id);
    if(status!==200){
      assert(result.status>=400&&result.status<500,'expected deny, got '+result.status);
      if(id===model.id)assert.equal(await kubectl(['-n','higress-system','get','envoyfilter','access-'+person.binding,'--ignore-not-found','-o','json']),'','revoked or expired selection route was not reclaimed');
      return;
    }
    assert.equal(result.status,200,JSON.stringify(result));
    assert.deepEqual(result.body.leaked,[]);
    assert.equal(result.body.providerAuthenticated,true,'Caller bearer cleanup must preserve server-side provider credential injection');
    assert.equal(result.body.model,'disposable-model','Existing ai-proxy must map the catalog model');
    assert.equal(result.body.path,'/v1/chat/completions');
    assert.equal(result.body.host,'access-upstream.aep-system.svc:8080');
    assert.equal(result.observation,undefined);
  }
  await until(async()=>check(alice,200));await until(async()=>check(bob,200));
  const first=await route(alice);
  assert.equal(first.metadata.labels['gateway.aep.zhiyuan.io/access-route'],'true');
  await kubectl(['-n','higress-system','delete','envoyfilter',first.metadata.name]);
  await until(async()=>assert.notEqual((await route(alice)).metadata.uid,first.metadata.uid));
  await until(async()=>check(alice,200));
  record('Operator-created binding route hands off to actual ModelRoute/ai-proxy and repairs deletion; no test-side session route creation');
  const stable=await route(alice);
  const source=()=>kubectl(['-n','aep-system','get','modelaccessgrant','access-'+alice.binding,'-o','json']).then(JSON.parse);
  const sourceSequence=(await source()).spec.publication.sequence;
  await until(async()=>assert((await source()).spec.publication.sequence>sourceSequence));
  assert.equal((await route(alice)).metadata.resourceVersion,stable.metadata.resourceVersion);
  await check(alice,200);
  record('Real business relay renewal advances publication without rewriting the stable session selection route');
  const before=await stats();
  const attacks=[{binding:bob.binding},{binding:''},{binding:'unknown'},{binding:'authorized'},
    {token:'unknown'},{token:''},{host:'other.access.test'},
    {body:JSON.stringify({model:'unassigned-model'}),headers:{'x-aep-model-id':model.id}},
    ...['{','null','[]','{}','{"model":42}'].map(body=>({body})),
    {body:JSON.stringify({model:model.id,padding:'x'.repeat(9000)})}];
  for(const [index,options] of attacks.entries()){
    const result=await call(alice,model.id,options);
    assert(result.status>=400&&result.status<500,'escalation case '+index+' not denied '+result.status);
    assert.equal(result.observation,undefined);
  }
  assert.equal(await stats(),before,'denied requests reached model upstream');
  await check(alice,200);
  const rewritten=await call(alice,model.id,{headers:{'x-aep-model-id':'forged-model'}});
  assert.equal(rewritten.status,200);assert.equal(rewritten.body.model,'disposable-model');
  record('Missing/cross-user/authorized binding, invalid JWT, wrong Host, malformed/oversized body and actual-model escalation deny; forged model header cannot select a different route');
  const spec=JSON.parse(await kubectl(['-n','aep-system','get','modelaccessgrant','access-'+alice.binding,'-o','json'])).spec;
  const expected={
    'x-mse-consumer':'aep.'+Buffer.from(spec.deploymentId).toString('base64url')+'.'+Buffer.from(spec.subject.id).toString('base64url'),
    'x-aep-deployment-id':spec.deploymentId,'x-aep-user-id':spec.subject.id,'x-aep-session-id':spec.binding.sessionId,
    'x-aep-model-id':model.id,
    ...Object.fromEntries([['team',spec.teamIds??[]],['role',spec.roleIds??[]]].map(([kind,ids])=>[
      'x-aep-'+kind+'-ids',ids.length?'|'+[...new Set(ids)].sort().map(id=>Buffer.from(id).toString('base64url')).join('|')+'|':''])),
  };
  for(const [kind,ids] of [['global',['']],['user',[spec.subject.id]],['team',spec.teamIds??[]],['role',spec.roleIds??[]]])
    for(const id of ids)for(const scope of ['all','m.'+Buffer.from(model.id).toString('base64url')]){
      const key=kind+'.'+Buffer.from(id).toString('base64url')+'.'+scope;
      expected['x-aep-limit-'+createHash('sha256').update(key).digest('hex')]='1';
    }
  const forged=await call(alice,model.id,{headers:{'x-aep-forged':'attacker','x-aep-limit-foreign':'1',
    'x-aep-user-id':bob.user.id,'x-aep-team-ids':'forged','x-mse-consumer':'quota-admin','x-consumer-id':'quota-admin','x-ext-auth-host':'other'}});
  assert.equal(forged.status,200);assert.deepEqual(forged.body.leaked,[]);
  assert.deepEqual(forged.body.identity,expected,'Native OPA identity/key encoding differs from existing gatewaypolicy.Headers contract');
  report.productionBindingRouteProjectionImplemented=true;report.reservedHeaderPrefixSanitizationImplemented=true;
  report.gatewayAcceptanceFreshConnectionsOnly=true;report.existingConnectionAdoptionVerified=false;
  report.quotaIdentityVerified=false;report.bindingHintIsPublicContract=false;
  report.scope='business API -> DB/outbox/relay -> ModelAccessGrant -> Operator-owned binding routes -> real Higress -> existing ModelRoute/ai-proxy -> mock model';
  return {check,call,stats,async outage(person){
    const before=await stats();
    for(const name of ['authorino-a','authorino-b'])await kubectl(['-n','aep-system','scale','deployment/'+name,'--replicas=0']);
    await until(async()=>assert.equal(JSON.parse(await kubectl(['-n','aep-system','get','pods','-l','aep-acceptance-authorino=true','-o','json'])).items.length,0));
    const result=await call(person);assert(result.status>=400&&result.status<600);assert.equal(await stats(),before);
    for(const name of ['authorino-a','authorino-b']){
      await kubectl(['-n','aep-system','scale','deployment/'+name,'--replicas=1']);
      await kubectl(['-n','aep-system','rollout','status','deployment/'+name,'--timeout=150s']);
    }
    await until(async()=>check(person,200));
    record('Authorization outage fails closed before upstream; restart restores model route handoff');
  }};
}
