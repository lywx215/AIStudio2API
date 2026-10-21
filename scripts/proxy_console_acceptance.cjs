// Production-built UI + local synthetic HTTP. No real sources, accounts or models.
// Set PROXY_PLAYWRIGHT_PATH to an installed Playwright package if not on NODE_PATH.
const { chromium } = require(process.env.PROXY_PLAYWRIGHT_PATH || 'playwright')
const http = require('node:http')
const fs = require('node:fs')
const path = require('node:path')
const assert = require('node:assert/strict')
const root = path.resolve(__dirname, '..', 'internal/webui/dist')
const evidence = path.resolve(__dirname, '..', 'runtime/proxy-console-acceptance')
fs.mkdirSync(evidence, { recursive: true })
const source = { id:'source-fixture', name:'Synthetic Webshare', kind:'webshare', protocol:'socks5', enabled:true, revision:1, configured:true, refreshed_at:new Date().toISOString(), error:'' }
const proxies = Array.from({length:55}, (_,i)=>({id:`proxy-${i}`,protocol:'socks5',endpoint:`proxy-${i}.invalid:${1080+i}`,enabled:true,revision:1,state:i===1?'untested':'passed',exit_ip:i===1?'':'8.8.8.8',latency_ms:18+i,studio_latency_ms:30+i,checked_at:i===1?'':new Date().toISOString(),error:'',present:true,eligible:i!==1,sources:source.name,accounts:i===0?1:0}))
const accounts = ['one@example.test','two@example.test'].map((id,i)=>({id,label:id,enabled:i!==0,state:i===0?'disabled':'ready',proxy:'',egress:'',locale:'en-US',timezone:'UTC',models:[],benefit_tier:'Free',message:''}))
const jobs = new Map(), requests = [], pageErrors = []
let unknownNext = false, sourceDraft, timer
function json(res,body,status=200){res.writeHead(status,{'Content-Type':'application/json','Cache-Control':'no-store'});res.end(JSON.stringify(body))}
function parsed(req){return new Promise(resolve=>{let text='';req.on('data',chunk=>text+=chunk);req.on('end',()=>resolve(text?JSON.parse(text):{}))})}
const server=http.createServer(async(req,res)=>{
 const url=new URL(req.url,'http://localhost'), p=url.pathname
 if(p.startsWith('/api/')){
  const body=await parsed(req);requests.push({method:req.method,path:p,body})
  if(p==='/api/auth/session')return json(res,{enabled:true,authenticated:true,username:'fixture'})
  if(p==='/api/status')return json(res,{state:'STOPPED',running:false,ready:true,version:'synthetic',active_requests:0,accounts:{total:2,ready:1}})
  if(p==='/api/accounts')return json(res,{accounts})
  if(p==='/api/models')return json(res,{models:[]})
  if(p==='/api/cooldowns')return json(res,{cooldowns:[]})
  if(p==='/api/requests')return json(res,{requests:[]})
  if(p==='/api/events'){res.writeHead(200,{'Content-Type':'text/event-stream'});res.write(': synthetic fixture\n\n');return}
  if(p==='/api/config')return json(res,{upstream_channels:['playground','build'],proxy_pool:[]})
  if(p==='/api/update')return json(res,{available:false,current:'synthetic'})
  if(p==='/api/proxies/sources'&&req.method==='GET')return json(res,{items:[source]})
  if(p.startsWith('/api/proxies/sources/')&&req.method==='PUT'){sourceDraft=body;Object.assign(source,{name:body.name,revision:source.revision+1});return json(res,source)}
  if(p.endsWith('/refresh'))return json(res,{count:55,invalid:0})
  if(p==='/api/proxies/stats')return json(res,{total:55,passed:54,failed:0,restricted:0,untested:1,eligible:54,average_ms:32})
  if(p==='/api/proxies'){
   let items=proxies.filter(v=>(!url.searchParams.get('eligible')||v.eligible)&&(!url.searchParams.get('search')||v.endpoint.includes(url.searchParams.get('search'))))
   const total=items.length, offset=Number(url.searchParams.get('offset')||0),limit=Number(url.searchParams.get('limit')||50)
   return json(res,{items:items.slice(offset,offset+limit),total,offset,limit})
  }
  if(p==='/api/proxies/account-contexts')return json(res,{items:body.ids.map((id,i)=>({id,account_id:`account-${i}`,endpoint:'',credential_version:3,state_version:4,enabled:i!==0,archived:false,deleted:false}))})
  if(p==='/api/proxies/jobs'&&req.method==='GET')return json(res,{items:[...jobs.values()].map(job=>({...job,items:[]}))})
  if(p==='/api/proxies/jobs'&&req.method==='POST'){
   const original=[...jobs.values()].find(job=>job.key===req.headers['idempotency-key']);if(original)return json(res,original,202)
   const id=`job-${jobs.size}`, targets=body.kind==='assign'?body.targets:body.all?proxies:body.ids.map(id=>proxies.find(p=>p.id===id))
   const job={id,kind:body.kind,status:'running',total:targets.length,done:0,cancel_requested:false,offset:0,limit:50,items:targets.map((target,i)=>({index:i,proxy_id:'proxy-0',endpoint:proxies[0].endpoint,account_id:body.kind==='assign'?target.id:'',status:'queued',code:''}))}
   job.key=req.headers['idempotency-key'];jobs.set(id,job)
   timer=setTimeout(()=>{job.status='completed';job.done=job.total;job.items.forEach(item=>{item.status='succeeded';item.code=body.kind==='assign'?'PROXY_APPLIED':'';item.probe={state:'passed',exit_ip:'8.8.8.8',latency_ms:20}})},2200)
   if(unknownNext){unknownNext=false;return json(res,{error:{code:'COMMIT_RESULT_UNKNOWN',message:'COMMIT_RESULT_UNKNOWN'}},503)}
   return json(res,job,202)
  }
  if(p.startsWith('/api/proxies/jobs/by-key/')){const key=decodeURIComponent(p.split('/').at(-1)),job=[...jobs.values()].find(j=>j.key===key);return json(res,job||{error:{code:'JOB_NOT_FOUND',message:'JOB_NOT_FOUND'}},job?200:404)}
  if(p.startsWith('/api/proxies/jobs/')){const id=p.split('/')[4],job=jobs.get(id);if(p.endsWith('/cancel')){job.status='cancelled';job.cancel_requested=true;res.writeHead(204);return res.end()};const offset=Number(url.searchParams.get('offset')||0);return json(res,{...job,items:job.items.slice(offset,offset+50),offset})}
  return json(res,{})
 }
 let file=path.join(root,p==='/'?'index.html':p);if(!file.startsWith(root))return res.end();if(!fs.existsSync(file))file=path.join(root,'index.html')
 const type={'.html':'text/html','.js':'text/javascript','.css':'text/css','.woff2':'font/woff2'}[path.extname(file)]||'application/octet-stream';res.writeHead(200,{'Content-Type':type});fs.createReadStream(file).pipe(res)
})
;(async()=>{
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));const origin=`http://127.0.0.1:${server.address().port}`
 const browser=await chromium.launch({headless:true,...(process.env.PROXY_CHROME_PATH?{executablePath:process.env.PROXY_CHROME_PATH}:{})})
 try{
  const context=await browser.newContext({viewport:{width:1440,height:1000}});const page=await context.newPage();page.on('pageerror',e=>pageErrors.push(e.message))
  await context.route('**/*',route=>route.request().url().startsWith(origin)?route.continue():route.abort())
  await page.goto(origin+'/?tab=proxies');await page.getByRole('heading',{name:'代理管理',exact:true}).waitFor()
  await page.getByRole('cell',{name:'proxy-0.invalid:1080 SOCKS5',exact:true}).waitFor()
  await page.getByRole('button',{name:'编辑',exact:true}).click();const dialog=page.getByRole('dialog');await dialog.getByLabel('来源名称').fill('Edited synthetic source');await dialog.getByRole('button',{name:'保存',exact:true}).click();await dialog.waitFor({state:'hidden'});assert.equal(sourceDraft.url,'')
  await page.getByRole('button',{name:'更新列表',exact:true}).click();await page.getByText('列表已更新：55；失败 0').waitFor()
  await page.getByRole('button',{name:'下一页',exact:true}).click();await page.getByRole('cell',{name:'proxy-50.invalid:1130 SOCKS5',exact:true}).waitFor()
  await page.getByRole('button',{name:'上一页',exact:true}).click();await page.getByRole('checkbox',{name:'proxy-0.invalid:1080',exact:true}).check()
  await page.getByRole('button',{name:'测试所选 (1)',exact:true}).click();await page.getByText('处理中',{exact:false}).first().waitFor();await page.getByText('1 / 1 · 已结束').waitFor({timeout:8000})
  await page.screenshot({path:path.join(evidence,'proxies-desktop.png'),fullPage:true})
  await page.getByRole('button',{name:'账户',exact:true}).click();await page.getByRole('button',{name:'切换代理',exact:true}).first().click();await page.getByRole('dialog').getByText('one@example.test',{exact:true}).waitFor();await page.getByRole('dialog').getByRole('combobox').nth(1).selectOption('specified');await page.getByRole('dialog').locator('select[size]').selectOption('proxy-0');await page.getByRole('dialog').getByRole('button',{name:'设置代理',exact:true}).click();await page.getByText('1 / 1 · 已结束').waitFor({timeout:8000});await page.getByRole('dialog').getByText('代理已设置',{exact:false}).waitFor();await page.screenshot({path:path.join(evidence,'account-proxy.png'),fullPage:true});await page.getByRole('dialog').getByRole('button',{name:'关闭',exact:true}).click()
  await page.getByRole('checkbox',{name:'勾选账户 one@example.test',exact:true}).check();await page.getByRole('checkbox',{name:'勾选账户 two@example.test',exact:true}).check();await page.getByRole('button',{name:'批量随机代理 (2)',exact:true}).click();await page.getByRole('dialog').getByText('two@example.test',{exact:true}).waitFor();unknownNext=true;await page.getByRole('dialog').getByRole('button',{name:'设置代理',exact:true}).click();await page.getByText('提交结果未知，请只读核对原任务；不会重新抽取或重发。').waitFor();const count=requests.filter(r=>r.method==='POST'&&r.path==='/api/proxies/jobs').length;await page.getByRole('dialog').getByRole('button',{name:'核对提交',exact:true}).click();await page.getByText('2 / 2 · 已结束').waitFor({timeout:8000});assert.equal(requests.filter(r=>r.method==='POST'&&r.path==='/api/proxies/jobs').length,count)
  const assignments=requests.filter(r=>r.method==='POST'&&r.path==='/api/proxies/jobs'&&r.body.kind==='assign');assert.equal(assignments.length,2);assert.equal(assignments[0].body.mode,'specified');assert.equal(assignments[1].body.targets.length,2);assert(!JSON.stringify(assignments).includes('synthetic-password'))
  await page.getByRole('dialog').getByRole('button',{name:'关闭',exact:true}).click();await page.setViewportSize({width:390,height:844});await page.getByRole('button',{name:'代理管理',exact:true}).click();await page.getByRole('heading',{name:'代理管理',exact:true}).waitFor();await page.waitForTimeout(400);assert(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth+1));await page.screenshot({path:path.join(evidence,'proxies-mobile.png'),fullPage:true});assert.equal(pageErrors.length,0,pageErrors.join('\n'))
  fs.writeFileSync(path.join(evidence,'result.json'),JSON.stringify({passed:true,checks:['source edit retains private URL','refresh','bounded pagination','selected background test','single specified proxy','two-account random selection','unknown submission GET-only reconciliation','responsive rendering','no page errors'],productionCalls:0},null,2));console.log('Proxy console browser acceptance passed (9 checks, synthetic HTTP only).')
 }finally{await browser.close()}
})().catch(error=>{console.error(error.message);process.exitCode=1}).finally(()=>{clearTimeout(timer);server.closeAllConnections();server.close()})
