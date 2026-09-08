// No account or remote model is used: exercise the installed Cursor through acp-go.
import {spawn} from 'node:child_process';
import {createInterface} from 'node:readline';
import {mkdtempSync,mkdirSync,writeFileSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join,resolve} from 'node:path';
import assert from 'node:assert/strict';
const [binary,cursor]=process.argv.slice(2).map(p=>resolve(p));
const root=mkdtempSync(join(tmpdir(),'acp-cursor-handshake-'));console.log('Artifacts: '+root);
for(const permission of ['default','auto','full-access']){
 const cwd=join(root,permission);mkdirSync(cwd);
 const child=spawn(binary,[],{cwd,env:{PATH:process.env.PATH,HOME:cwd,USERPROFILE:cwd,CURSOR_CONFIG_DIR:cwd,CURSOR_PATH:cursor,ACP_TEST_CLI:'cursor',ACP_TEST_PERMISSION:permission,CURSOR_API_KEY:'offline-fixture',CURSOR_API_ENDPOINT:'http://127.0.0.1:9',NO_OPEN_BROWSER:'1',DO_NOT_TRACK:'1'},stdio:['pipe','pipe','pipe']});
 let stderr='';child.stderr.on('data',x=>stderr+=x);let initialized;
 try{
 initialized=await new Promise((resolve,reject)=>{
 const timer=setTimeout(()=>reject(Error('Cursor initialize timed out')),20000);
 child.once('exit',code=>{clearTimeout(timer);reject(Error('Cursor exited '+code+' '+stderr))});
 createInterface({input:child.stdout}).on('line',line=>{const message=JSON.parse(line);if(message.id===1){clearTimeout(timer);message.error?reject(Error(JSON.stringify(message.error))):resolve(message.result)}});
 child.stdin.write(JSON.stringify({jsonrpc:'2.0',id:1,method:'initialize',params:{protocolVersion:1,clientCapabilities:{elicitation:{form:{}},fs:{readTextFile:false,writeTextFile:false},terminal:false}}})+'\n');
 });
 assert.equal(initialized.protocolVersion,1);assert.equal(initialized.agentCapabilities.mcpCapabilities.http,true);
 writeFileSync(join(root,permission+'.json'),JSON.stringify(initialized,null,2));console.log(permission+' initialize PASS: '+initialized.agentInfo?.name);
 }finally{child.stdin.end();child.kill();await new Promise(r=>child.once('exit',r))}
}
