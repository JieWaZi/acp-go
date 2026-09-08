// Exercises actual CLI processes against a loopback OpenAI-compatible model fixture.
import { spawn } from 'node:child_process';
import { createServer } from 'node:http';
import { createInterface } from 'node:readline';
import { mkdtempSync,mkdirSync,writeFileSync,existsSync,readFileSync } from 'node:fs';
import {tmpdir} from 'node:os';
import {join,resolve} from 'node:path';
import {pathToFileURL} from 'node:url';
import assert from 'node:assert/strict';
const [binary,piPath,kimiPath]=process.argv.slice(2).map(x=>resolve(x));
if(!binary||!piPath||!kimiPath)throw Error('binary, Pi path, Kimi path required');
const root=mkdtempSync(join(tmpdir(),'acp-unified-'));console.log('Artifacts: '+root);
const reviews=[],modelCalls=[],questions=[],approvals=[],audit=[];
let task;
const textOf=(content)=>typeof content==='string'?content:(content??[]).filter(x=>x.type==='text').map(x=>x.text).join('\n');
const server=createServer(async(req,res)=>{
 try{
 let raw='';for await(const part of req)raw+=part;const body=raw?JSON.parse(raw):{};
 if(req.url==='/mcp') {
  if(req.method!=='POST'){res.writeHead(405).end();return}
  assert.equal(req.headers.authorization,'Bearer fixture-mcp');
  if(body.id===undefined){res.writeHead(202).end();return}
  let result={};
  if(body.method==='initialize')result={protocolVersion:body.params.protocolVersion,serverInfo:{name:'fixture',version:'1'},capabilities:{tools:{}}};
  if(body.method==='tools/list')result={tools:[{name:'record',description:'Writes a fixture marker file',inputSchema:{type:'object',properties:{path:{type:'string'}},required:['path']}}]};
  if(body.method==='tools/call'){assert.ok(body.params.arguments.path.startsWith(root));writeFileSync(body.params.arguments.path,'MCP executed');result={content:[{type:'text',text:'recorded'}]}}
  res.writeHead(200,{'content-type':'application/json'}).end(JSON.stringify({jsonrpc:'2.0',id:body.id,result}));return;
 }
 if(!req.url.includes('/chat/completions'))throw Error('unexpected endpoint '+req.url);
 const reviewer=(body.messages??[]).some(m=>m.role==='system'&&textOf(m.content).includes('automatic approval reviewer'));
 const lastUser=body.messages.findLastIndex(m=>m.role==='user');
 let delta,stop;
 if(reviewer){
  assert.equal((body.tools??[]).length,0,'reviewer can execute tools');
  assert.equal(body.model,'selected-model','review ignored selected model');
  reviews.push({cli:task.cli,marker:task.marker,model:body.model});
  const outcome=task.review??'allow';
  delta={role:'assistant',content:outcome==='malformed'?'not a decision':JSON.stringify({outcome,risk_level:outcome==='allow'?'low':'high',rationale:'offline review fixture'})};stop='stop';
 }else{
  modelCalls.push({cli:task.cli,marker:task.marker,tools:(body.tools??[]).map(t=>t.function.name),messages:body.messages});
  writeFileSync(join(root,"model-calls.json"),JSON.stringify(modelCalls,null,2));
  const called=body.messages.slice(lastUser+1).some(m=>m.role==='tool');
  if(called){delta={role:'assistant',content:'fixture complete'};stop='stop'}else{
   let name,args;
   if(task.kind==='question'){
    name=(body.tools??[]).map(t=>t.function.name).find(n=>n.endsWith('AskUserQuestion'));
    assert.ok(name,'model does not have AskUserQuestion');
    args={questions:[{question:'Which option?',header:'Choice',options:[{label:'A',description:'First'},{label:'B',description:'Second'}]}]};
   }else if(task.kind==='mcp'){name=task.cli==='pi'?'mcp':'record';args=task.cli==='pi'?{server:'fixture',tool:'record',args:{path:join(task.cwd,task.marker+'.txt')}}:{path:join(task.cwd,task.marker+'.txt')}}
   else if(task.cli==='pi'){name='write';args={path:join(task.cwd,task.marker+'.txt'),content:'executed'}}
   else {name='WriteFile';args={path:join(task.cwd,task.marker+'.txt'),content:'executed'}}
   delta={role:'assistant',tool_calls:[{index:0,id:'call_'+task.marker,type:'function',function:{name,arguments:JSON.stringify(args)}}]};stop='tool_calls';
  }
 }
 if(body.stream===false){res.writeHead(200,{'content-type':'application/json'}).end(JSON.stringify({id:'fixture',object:'chat.completion',created:0,model:body.model,choices:[{index:0,message:delta,finish_reason:stop}],usage:{prompt_tokens:10,completion_tokens:5,total_tokens:15}}));return}
 res.writeHead(200,{'content-type':'text/event-stream'});
 for(const [d,f] of [[delta,null],[{},stop]])res.write('data: '+JSON.stringify({id:'fixture',object:'chat.completion.chunk',created:0,model:body.model,choices:[{index:0,delta:d,finish_reason:f}]})+'\n\n');res.end('data: [DONE]\n\n');
 }catch(error){console.error(error);res.writeHead(500).end(JSON.stringify({error:{message:String(error)}}))}
});
await new Promise(r=>server.listen(0,'127.0.0.1',r));const baseUrl='http://127.0.0.1:'+server.address().port+'/v1';
try{
for(const cli of (process.env.ACP_TEST_ONLY_CLI?[process.env.ACP_TEST_ONLY_CLI]:['pi','kimi']))for(const permission of (process.env.ACP_TEST_ONLY_PERMISSION?[process.env.ACP_TEST_ONLY_PERMISSION]:['default','auto','full-access'])){
 const work=join(root,cli+'-'+permission);mkdirSync(work);const home=join(work,'home'),config=join(work,'config'),cwd=join(work,'workspace');for(const dir of[home,config,cwd])mkdirSync(dir);
 if(cli==='pi'){
 writeFileSync(join(config,'models.json'),JSON.stringify({providers:{'local-fixture':{baseUrl,api:'openai-completions',apiKey:'dummy',models:[{id:'local-model',name:'Offline'},{id:'selected-model',name:'Selected'}]}}}));
 writeFileSync(join(config,'settings.json'),JSON.stringify({defaultProvider:'local-fixture',defaultModel:'local-model',quietStartup:true}));
 }else{
 mkdirSync(join(config,'credentials'));
 // Same dummy credential fixture as official tests/acp/conftest.py; no remote auth request.
 writeFileSync(join(config,'credentials','kimi-code.json'),JSON.stringify({access_token:'offline-fixture-token',expires_at:Date.now()/1000+86400*365,scope:'openid',token_type:'Bearer'}));
 writeFileSync(join(config,'config.toml'),`default_model = "local"\n[models.local]\nprovider="fixture"\nmodel="local-model"\nmax_context_size=100000\n[models.selected]\nprovider="fixture"\nmodel="selected-model"\nmax_context_size=100000\n[providers.fixture]\ntype="openai_legacy"\nbase_url=${JSON.stringify(baseUrl)}\napi_key="dummy"\n[loop_control]\nmax_retries_per_step=1\n`);
 }
 const env={PATH:process.env.PATH,HOME:home,USERPROFILE:home,TMPDIR:config,ACP_TEST_CLI:cli,ACP_TEST_PERMISSION:permission,ACP_TEST_STATE:join(config,'state'),KIMI_SHARE_DIR:config,KIMI_PATH:kimiPath,PI_PATH:piPath,PI_CODING_AGENT_DIR:config,PI_SKIP_VERSION_CHECK:'1',NO_COLOR:'1',ACP_GO_TEST_HOME:home,NODE_OPTIONS:'--import='+pathToFileURL(resolve('scripts/pi-integration/home-isolation.mjs')).href};
 const child=spawn(binary,[],{cwd,env,stdio:['pipe','pipe','pipe']});let stderr='';child.stderr.on('data',x=>{stderr+=x;writeFileSync(join(work,'stderr.log'),stderr)});
 let sequence=0;const pending=new Map();let firstQuestion;
 const send=x=>child.stdin.write(JSON.stringify(x)+'\n');
 createInterface({input:child.stdout}).on('line',line=>{
  const message=JSON.parse(line);writeFileSync(join(work,'last-message.json'),line);
  if(!message.method){const entry=pending.get(message.id);if(entry){pending.delete(message.id);clearTimeout(entry.timer);message.error?entry.reject(Error(JSON.stringify(message.error))):entry.resolve(message.result)};return}
  if(message.method==='session/update' && message.params.update._meta?.['acp-go/permission-review']){
   audit.push({cli,marker:task.marker,...message.params.update._meta['acp-go/permission-review']});
  }else if(message.method==='session/request_permission'){
   approvals.push({cli,permission,marker:task.marker,title:message.params.toolCall.title});
   const option=message.params.options.find(x=>x.kind===(task.deny?'reject_once':'allow_once'));
   assert.ok(option);send({jsonrpc:'2.0',id:message.id,result:{outcome:{outcome:'selected',optionId:option.optionId}}});
  }else if(message.method==='elicitation/create'){
   questions.push({cli,permission,sessionId:message.params._meta?.sessionId});
   assert.equal(message.params._meta?.sessionId,firstQuestion);
   const answer=()=>send({jsonrpc:'2.0',id:message.id,result:{action:'accept',content:{question_0:'B'}}});
   if(process.env.ACP_TEST_LONG_QUESTIONS==='1')setTimeout(answer,61000);else answer();
  }
 });
 const request=(method,params)=>new Promise((resolve,reject)=>{const id=++sequence;const timer=setTimeout(()=>{pending.delete(id);reject(Error(cli+' '+permission+' timeout '+method+'; stderr='+stderr.slice(-1000)))},90000);pending.set(id,{resolve,reject,timer});send({jsonrpc:'2.0',id,method,params})});
 try{
 await request('initialize',{protocolVersion:1,clientCapabilities:{elicitation:{form:{}},fs:{readTextFile:false,writeTextFile:false},terminal:false}});
 const session=await request('session/new',{cwd,mcpServers:[{type:'http',name:'fixture',url:baseUrl.replace('/v1','/mcp'),headers:[{name:'Authorization',value:'Bearer fixture-mcp'}]}]});writeFileSync(join(work,'session.json'),JSON.stringify(session,null,2));firstQuestion=session.sessionId;
 await request('session/set_config_option',{sessionId:session.sessionId,configId:'model',value:cli==='pi'?'local-fixture/selected-model':'selected'});
 for(const scenario of [{marker:'question',kind:'question'},{marker:'allow',kind:'write'},{marker:'deny',kind:'write',deny:true,review:'deny'},{marker:'mcp-allow',kind:'mcp'},{marker:'mcp-deny',kind:'mcp',deny:true,review:'deny'},...(permission==='auto'?[{marker:'review-failure',kind:'write',deny:true,review:'malformed'}]:[])]){
  task={...scenario,cli,permission,cwd,marker:cli+'-'+permission+'-'+scenario.marker};
  const beforeApprovals=approvals.length,beforeReviews=reviews.length,beforeQuestions=questions.length;
  await request('session/prompt',{sessionId:session.sessionId,prompt:[{type:'text',text:'LOCAL_FIXTURE '+task.marker}]});
  if(scenario.kind==='question'){
   assert.equal(questions.length,beforeQuestions+1,'question was silently dropped');
   const last=modelCalls.findLast(x=>x.marker===task.marker);assert.ok(last.messages.filter(m=>m.role==='tool').map(m=>textOf(m.content)).join('\n').includes('"B"'),'answer not returned to actual model');
  }else{
   const shouldExecute=permission==='full-access'||!scenario.deny;
   assert.equal(existsSync(join(cwd,task.marker+'.txt')),shouldExecute,'permission did not control actual write');
   if(permission==='auto'){const record=audit.findLast(x=>x.marker===task.marker);assert.ok(record,'missing auto-review audit');assert.equal(record.outcome,scenario.deny?'ask':'allow');assert.ok(!JSON.stringify(record).includes('offline review fixture'),'audit exposes model rationale');assert.ok(reviews.length>beforeReviews,'auto mode did not call reviewer');if(!scenario.deny)assert.equal(approvals.length,beforeApprovals,'low-risk action still required manual approval');else assert.ok(approvals.length>beforeApprovals,'unsafe or broken review did not ask user')}
   if(permission==='full-access')assert.equal(approvals.length,beforeApprovals);
  }
 }
 console.log(cli+' '+permission+' PASS');
 }finally{child.stdin.end();child.kill();await new Promise(r=>child.once('exit',r));for(const p of pending.values())clearTimeout(p.timer)}
}
writeFileSync(join(root,'results.json'),JSON.stringify({reviews,questions,approvals,audit,modelCalls},null,2));console.log('PASS: unified permissions and questions through real CLI processes');
}finally{server.closeAllConnections();await new Promise(r=>server.close(r))}
