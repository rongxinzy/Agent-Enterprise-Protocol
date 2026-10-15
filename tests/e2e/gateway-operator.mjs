// Real, isolated Kind + upstream Higress acceptance. This exercises the
// operator/data-plane boundary; direct fixture headers do not test AEP auth.
import assert from 'node:assert/strict';
import {spawn} from 'node:child_process';
import {createHash} from 'node:crypto';
import {mkdtemp, readFile, writeFile, rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const kind = process.env.KIND ?? 'kind';
const helm = process.env.HELM ?? 'helm';
const cluster = process.env.AEP_OPERATOR_E2E_CLUSTER ?? 'aep-operator-e2e';
assert(/^aep-operator-[a-z0-9-]+$/.test(cluster), 'Use a dedicated operator test cluster');
const work = await mkdtemp(path.join(tmpdir(), 'aep-operator-'));
const kubeconfig = path.join(work, 'kubeconfig');
let owned = false;
const env = {...process.env, KUBECONFIG: kubeconfig};
const ns = 'aep-system', hns = 'higress-system';
const key = 'operator-e2e-disposable-provider';
const images = [
  'higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/gateway:2.2.4',
  'higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/higress:2.2.4',
  'higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/pilot:2.2.4',
  'node:24-alpine', 'redis:7-alpine',
];
async function command(name, args, {input, allowFailure = false, commandEnv = env, timeout = 240_000} = {}) {
  return await new Promise((resolve, reject) => {
    const child = spawn(name, args, {cwd: root, env: commandEnv, stdio: ['pipe', 'pipe', 'pipe'], shell: false});
    let out = '', errors = '';
    const timer = setTimeout(() => { child.kill(); reject(new Error(name + ' timed out')); }, timeout);
    child.stdout.on('data', value => out += value);
    child.stderr.on('data', value => errors += value);
    child.on('error', error => {clearTimeout(timer); reject(error);});
    child.on('exit', code => {clearTimeout(timer); code === 0 || allowFailure ? resolve(out.trim()) : reject(new Error(name + ' failed: ' + errors));});
    child.stdin.end(input);
  });
}
const kubectl = (args, options) => command('kubectl', ['--kubeconfig', kubeconfig, '--context', 'kind-' + cluster, ...args], options);
const apply = objects => kubectl(['apply', '-f', '-'], {input:JSON.stringify({apiVersion:'v1',kind:'List',items:objects})});
const get = async (resource, name, namespace = ns) => JSON.parse(await kubectl(['-n', namespace, 'get', resource, name, '-o', 'json']));
async function until(check, timeout = 180_000) {
  const end = Date.now() + timeout;
  let last;
  while (Date.now() < end) {
    try { return await check(); } catch (error) { last = error; await new Promise(resolve => setTimeout(resolve, 1500)); }
  }
  throw last ?? new Error('Convergence timeout');
}
async function applied(kindOf, name) {
  return until(async () => {
    const object = await get(kindOf, name);
    const condition = object.status?.conditions?.find(item => item.type === 'Applied');
    assert.equal(condition?.status, 'True', kindOf + ' Applied: ' + JSON.stringify(object.status));
    assert.equal(condition.observedGeneration, object.metadata.generation);
    assert.notEqual(object.status.conditions.find(item => item.type === 'Ready')?.status, 'True', 'SSA must not claim Ready');
    return object;
  });
}
function deployment(name, image, port, extra = {}, namespace = ns) {
  return {apiVersion:'apps/v1',kind:'Deployment',metadata:{name,namespace},spec:{replicas:1,
    selector:{matchLabels:{app:name}},template:{metadata:{labels:{app:name}},spec:{
      securityContext:{runAsNonRoot:true,runAsUser:65532,runAsGroup:65532,seccompProfile:{type:'RuntimeDefault'}},
      containers:[{name,image,ports:[{containerPort:port}],securityContext:{allowPrivilegeEscalation:false,capabilities:{drop:['ALL']}},
        resources:{requests:{cpu:'20m',memory:'32Mi'},limits:{memory:'128Mi'}},...extra.container}],
      ...extra.pod}}}};
}
const service = (name, port, namespace = ns) => ({apiVersion:'v1',kind:'Service',metadata:{name,namespace},spec:{selector:{app:name},ports:[{name:'http',port,targetPort:port}]}});
const condition = (object,type) => object.status?.conditions?.find(item => item.type === type);
async function call(model = 'enterprise-chat', user = 'alice', headers = {}, stream = false) {
  const uri = model === 'bench-anthropic' ? '/bench-anthropic/v1/messages' : '/v1/chat/completions';
  const body = {model,messages:[{role:'user',content:'operator acceptance'}],stream,max_tokens:16};
  const code = 'fetch(' + JSON.stringify('http://higress-gateway.higress-system.svc.cluster.local' + uri) +
    ',{method:"POST",headers:' + JSON.stringify({'content-type':'application/json','x-aep-deployment-id':'demo',
      'x-aep-user-id':user,'x-aep-session-id':'operator-e2e','x-aep-model-id':model,...headers}) +
    ',body:' + JSON.stringify(JSON.stringify(body)) + '}).then(async r=>console.log(JSON.stringify({status:r.status,body:await r.text()}))).catch(e=>{console.error(e.message);process.exit(1)})';
  return JSON.parse(await kubectl(['-n',ns,'exec','deployment/mock-openai','--','node','-e',code]));
}
const report = {operator:true,higress:'2.2.4',checks:[]};
async function record(name, check) {
  await check();
  report.checks.push(name);
  console.log('PASS ' + name);
}
try {
  const clusters = (await command(kind,['get','clusters'])).split(/\s+/);
  if (clusters.includes(cluster)) {
    assert.equal(process.env.AEP_OPERATOR_E2E_REUSE,'true','Existing cluster is protected; explicitly opt in to reuse');
    await writeFile(kubeconfig,await command(kind,['get','kubeconfig','--name',cluster]),{mode:0o600});
    const existing = await kubectl(['get','deployment','aep-gateway-reconciler','-n',ns,'--ignore-not-found','-o','name']);
    assert.equal(existing,'','A legacy reconciler must not run in the operator test cluster');
  } else {
    await command(kind,['create','cluster','--name',cluster,'--image',process.env.AEP_OPERATOR_NODE_IMAGE ??
      'kindest/node@sha256:3489c7674813ba5d8b1a9977baea8a6e553784dab7b84759d1014dbd78f7ebd5','--kubeconfig',kubeconfig,'--wait','120s']);
    owned = true;
  }
  await kubectl(['create','namespace',hns],{allowFailure:true});
  await kubectl(['taint','nodes','--all','node-role.kubernetes.io/control-plane-'],{allowFailure:true});
  let operatorImage = process.env.AEP_OPERATOR_IMAGE;
  if (!operatorImage) {
    const ko = process.env.KO ? [process.env.KO,[]] : ['go',['run','github.com/google/ko@v0.18.0']];
    const refs = path.join(work,'operator-image.txt');
    await command(ko[0],[...ko[1],'build','--platform','linux/amd64','--image-refs',refs,'./services/gateway-operator/cmd'],
      {commandEnv:{...env,KO_DOCKER_REPO:'ko.local'},timeout:600_000});
    operatorImage = (await readFile(refs,'utf8')).trim();
  }
  const imageConfig = JSON.parse(await command('docker',['image','inspect',operatorImage,'--format','{{json .Config}}']));
  assert.match(imageConfig.User,/^65532(?::65532)?$/,'Operator image must use the deployment non-root identity');
  assert.deepEqual(imageConfig.Entrypoint,['/ko-app/cmd'],'Operator image must run the ko-built manager');
  report.operatorImage = operatorImage;
  report.kubernetes = JSON.parse(await kubectl(['version','-o','json'])).serverVersion.gitVersion;
  for (const image of images) {
    try { await command('docker',['image','inspect',image]); }
    catch { await command('docker',['pull',image]); }
  }
  // Docker's image store can retain a multi-platform index with only the
  // host platform's layers. Export the selected platform instead of asking
  // containerd to import missing architectures.
  for (const [index,image] of [...images,operatorImage].entries()) {
    const archive=path.join(work,'image-'+index+'.tar');
    await command('docker',['image','save','--platform','linux/amd64','-o',archive,image],{timeout:600_000});
    await command(kind,['load','image-archive','--name',cluster,archive],{timeout:600_000});
    await rm(archive);
  }
  let chart = process.env.AEP_HIGRESS_CORE_CHART;
  if (!chart) {
    await command(helm,['pull','higress-core','--repo','https://higress.io/helm-charts','--version','2.2.4','--untar','--untardir',work]);
    chart = path.join(work,'higress-core');
  }
  const values = path.join(work,'values.json');
  await writeFile(values,JSON.stringify({global:{local:true,enableIstioAPI:true,enableGatewayAPI:false,o11y:{enabled:false}},
    gateway:{replicas:1,service:{type:'ClusterIP'},resources:{requests:{cpu:'100m',memory:'256Mi'},limits:{memory:'1Gi'}}},
    controller:{replicas:1,automaticHttps:{enabled:false},resources:{requests:{cpu:'100m',memory:'128Mi'}}}}));
  const chartYaml = await command(helm,['template','higress',chart,'--namespace',hns,'--include-crds','-f',values]);
  const crds = await command(helm,['show','crds',chart]);
  if (crds.trim()) await kubectl(['apply','-f','-'],{input:crds});
  await kubectl(['wait','--for=condition=Established','crds','--all','--timeout=60s']);
  await kubectl(['apply','-f','-'],{input:chartYaml});
  for (const name of ['higress-controller','higress-gateway']) await kubectl(['-n',hns,'rollout','status','deployment/'+name,'--timeout=180s']);
  let operatorYaml = await command('kubectl',['kustomize','services/gateway-operator/config/default']);
  operatorYaml = operatorYaml.replace('ko://github.com/rongxinzy/Agent-Enterprise-Protocol/services/gateway-operator/cmd',operatorImage);
  await kubectl(['apply','-f','-'],{input:operatorYaml});
  await apply([
    {apiVersion:'v1',kind:'ConfigMap',metadata:{name:'mock-openai',namespace:ns},data:{'mock-openai.mjs':await readFile(path.join(root,'tests/e2e/mock-openai.mjs'),'utf8')}},
    deployment('mock-openai','node:24-alpine',8080,{container:{command:['node','/app/mock-openai.mjs'],env:[{name:'MOCK_OPENAI_API_KEY',value:key},{name:'MOCK_OPENAI_PORT',value:'8080'}],
      volumeMounts:[{name:'code',mountPath:'/app',readOnly:true}]},pod:{volumes:[{name:'code',configMap:{name:'mock-openai'}}]}}),
    service('mock-openai',8080),
    deployment('redis','redis:7-alpine',6379,{container:{args:['--save','','--appendonly','no']}},hns),service('redis',6379,hns),
    {apiVersion:'v1',kind:'Service',metadata:{name:'aep-model-gateway',namespace:hns},spec:{ports:[{name:'http',port:80,targetPort:8080}]}},
    {apiVersion:'v1',kind:'Secret',metadata:{name:'provider',namespace:'aep-credentials'},stringData:{key}},
    {apiVersion:'networking.k8s.io/v1',kind:'Ingress',metadata:{name:'redis-registration',namespace:hns},spec:{ingressClassName:'higress',
      rules:[{http:{paths:[{path:'/redis-unused',pathType:'Prefix',backend:{service:{name:'redis',port:{number:6379}}}}]}}]}},
  ]);
  for (const name of ['aep-gateway-operator','mock-openai']) await kubectl(['-n',ns,'rollout','status','deployment/'+name,'--timeout=180s']);
  await kubectl(['-n',hns,'rollout','status','deployment/redis','--timeout=120s']);
  const mock = JSON.parse(await kubectl(['-n',ns,'get','pods','-l','app=mock-openai','-o','json'])).items[0];
  await apply([{apiVersion:'v1',kind:'Endpoints',metadata:{name:'aep-model-gateway',namespace:hns},subsets:[{addresses:[{ip:mock.status.podIP}],ports:[{name:'http',port:8080}]}]}]);
  const sa = 'system:serviceaccount:aep-system:aep-gateway-operator';
  await record('namespace RBAC and sole AEP writer',async()=>{
    for (const [verb,resource,namespace,answer,identity = sa] of [
      ['patch','wasmplugins.extensions.higress.io',hns,'yes'],
      ['patch','wasmplugins.extensions.higress.io','default','no'],
      ['get','secrets/provider','aep-credentials','yes'],
      ['get','secrets/aep-native-gateway',hns,'yes'],
      ['get','secrets/unrelated',hns,'no'],
      ['list','secrets',hns,'no'],
      ['update','leases/gateway-operator.gateway.aep.zhiyuan.io',ns,'yes'],
      ['update','leases/unrelated',ns,'no'],
      ['patch','wasmplugins.extensions.higress.io',hns,'no','system:serviceaccount:aep-system:aep-gateway-reconciler'],
    ]) {
      const result = await kubectl(['auth','can-i',verb,resource,'-n',namespace,'--as',identity],{allowFailure:true});
      assert.equal(result,answer,verb+' '+resource+' '+namespace);
    }
  });
  await apply([{apiVersion:'gateway.aep.zhiyuan.io/v1alpha1',kind:'DeploymentConfig',metadata:{name:'deployment',namespace:ns},
    spec:{deploymentId:'demo',nativeGateway:{enabled:true,redisService:'redis.higress-system.svc.cluster.local',redisPort:6379,redisDatabase:0}}}]);
  await applied('deploymentconfig','deployment');
  await apply([
    {apiVersion:'gateway.aep.zhiyuan.io/v1alpha1',kind:'ModelRoute',metadata:{name:'openai',namespace:ns},spec:{modelId:'enterprise-chat',protocol:'openai',
      upstream:{endpoint:'http://mock-openai.aep-system.svc.cluster.local:8080/v1',model:'mock-upstream-chat'},credentialRef:{name:'provider',key:'key'},enabled:true}},
    {apiVersion:'gateway.aep.zhiyuan.io/v1alpha1',kind:'ModelRoute',metadata:{name:'anthropic',namespace:ns},spec:{modelId:'bench-anthropic',protocol:'anthropic',
      upstream:{endpoint:'http://mock-openai.aep-system.svc.cluster.local:8080/api/anthropic',model:'mock-upstream-chat'},credentialRef:{name:'provider',key:'key'},enabled:true}},
  ]);
  await record('OpenAI provider credential and model rewrite',async()=>{await applied('modelroute','openai');await until(async()=>{const result=await call();assert.equal(result.status,200,JSON.stringify(result));assert.match(result.body,/choices/);});});
  await record('Anthropic credential injection and streaming',async()=>{await applied('modelroute','anthropic');await until(async()=>{const result=await call('bench-anthropic','alice',{},true);assert.equal(result.status,200,JSON.stringify(result));assert.match(result.body,/event: message_start/);assert.match(result.body,/event: message_stop/);});});
  await apply([{apiVersion:'gateway.aep.zhiyuan.io/v1alpha1',kind:'ModelPolicy',metadata:{name:'user-model',namespace:ns},spec:{
    targetRef:{kind:'ModelRoute',name:'openai'},subject:{type:'user',id:'alice'},rateLimits:[{maximum:1,intervalSeconds:86400}]}}]);
  await record('native Redis limit for user × model',async()=>{
    await applied('modelpolicy','user-model');
    const subject = 'user.'+Buffer.from('alice').toString('base64url')+'.m.'+Buffer.from('enterprise-chat').toString('base64url');
    const header = 'x-aep-limit-'+createHash('sha256').update(subject).digest('hex');
    await until(async()=>{assert.equal((await call('enterprise-chat','alice',{[header]:'1'})).status,429);});
    assert.equal((await call('enterprise-chat','bob')).status,200);
  });
  await kubectl(['-n',ns,'delete','modelpolicy','user-model']);
  await record('policy removal updates shared plugins',async()=>{await until(async()=>assert.equal((await call()).status,200));});
  await record('two replicas and Lease handover',async()=>{
    const leaseName='gateway-operator.gateway.aep.zhiyuan.io';
    const before=await get('lease',leaseName);
    const leader=before.spec.holderIdentity.split('_')[0];
    await kubectl(['-n',ns,'delete','pod',leader,'--wait=true']);
    await until(async()=>{const current=await get('lease',leaseName);assert.ok(current.spec.holderIdentity);assert.notEqual(current.spec.holderIdentity,before.spec.holderIdentity);});
    await kubectl(['-n',ns,'patch','modelroute','openai','--type=merge','-p','{"spec":{"enabled":false}}']);
    await applied('modelroute','openai');
    await until(async()=>assert.notEqual((await call()).status,200));
  });
  await kubectl(['-n',ns,'delete','modelroutes','--all','--timeout=120s']);
  await record('final route deletion empties aggregate and preserves foreign objects',async()=>{
    await until(async()=>{
      const all=JSON.parse(await kubectl(['-n',hns,'get','ingresses','-o','json']));
      assert.deepEqual(all.items.map(item=>item.metadata.name),['redis-registration']);
      const plugins=JSON.parse(await kubectl(['-n',hns,'get','wasmplugins','-o','json']));
      assert.equal(plugins.items.length,2);
      assert.ok(plugins.items.every(item=>(item.spec.matchRules??[]).length===0));
    });
  });
  await kubectl(['-n',ns,'delete','deploymentconfig','deployment','--timeout=120s']);
  await record('deployment finalizer removes only owned resources',async()=>{
    assert.equal(JSON.parse(await kubectl(['-n',hns,'get','wasmplugins','-o','json'])).items.length,0);
    await get('ingress','redis-registration',hns);
  });
  report.completedAt = new Date().toISOString();
  if (process.env.AEP_OPERATOR_E2E_REPORT) await writeFile(process.env.AEP_OPERATOR_E2E_REPORT,JSON.stringify(report,null,2)+'\n');
  console.log('Operator acceptance passed: '+report.checks.length+' checks. AEP API/outbox/auth cutover is a separate stage.');
} finally {
  if (owned && process.env.AEP_OPERATOR_E2E_KEEP !== 'true') await command(kind,['delete','cluster','--name',cluster],{allowFailure:true});
  await rm(work,{recursive:true,force:true});
}
