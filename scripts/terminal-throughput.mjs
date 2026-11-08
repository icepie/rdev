// Linux loopback transport baseline. Usage:
// bun scripts/terminal-throughput.mjs /tmp/rdev-server /tmp/rdev-client report.json
// Build the Web assets before either Go binary; never overlap Vite and go build.
import { spawn } from 'node:child_process';
import { mkdtemp, readFile, writeFile, rm, access } from 'node:fs/promises';
import { constants, openSync, closeSync, createReadStream } from 'node:fs';
import { tmpdir, cpus } from 'node:os';
import { join, resolve } from 'node:path';
import { createServer } from 'node:net';
import { randomUUID, createHash } from 'node:crypto';

const [serverArg, clientArg, outputArg] = process.argv.slice(2);
if (!serverArg || !clientArg || process.platform !== 'linux') throw new Error('Usage (Linux): bun scripts/terminal-throughput.mjs <server binary> <client binary> [report.json]');
const serverBin=resolve(serverArg), clientBin=resolve(clientArg);
await Promise.all([access(serverBin,constants.X_OK),access(clientBin,constants.X_OK)]);
const directory=await mkdtemp(join(tmpdir(),'rdev-throughput-'));
const children=[], sockets=new Set();
let sampler, stopped=false;
const rss={server:{baseline:0,peak:0},client:{baseline:0,peak:0}};
const delay=ms=>new Promise(resolve=>setTimeout(resolve,ms));
async function hashFile(path) {
 const hash=createHash('sha256');
 for await (const chunk of createReadStream(path)) hash.update(chunk);
 return hash.digest('hex');
}
async function freePorts(){
 const reservations=[];
 try {
  for(let i=0;i<4;i++){
   const server=createServer();await new Promise((resolve,reject)=>{server.once('error',reject);server.listen(0,'127.0.0.1',resolve)});reservations.push(server);
  }
  return reservations.map(server=>server.address().port);
 } finally {await Promise.all(reservations.map(server=>new Promise(resolve=>server.close(resolve))))}
}
function launch(name,binary,args){
 const log=openSync(join(directory,`${name}.log`),'w');
 const child=spawn(binary,args,{stdio:['ignore',log,log],env:{...process.env,DISPLAY:':98',RDEV_SHELL:'/bin/sh'}});
 closeSync(log);child.on('error',()=>{});children.push(child);return child;
}
async function waitFor(probe){
 const deadline=Date.now()+15000;
 while(Date.now()<deadline){
  if(children.some(child=>child.exitCode!==null||child.signalCode!==null))throw new Error('fixture process exited');
  try{if(await probe())return}catch{}
  await delay(50);
 }
 throw new Error('fixture startup timeout');
}
async function readRSS(child){
 try{return Number((await readFile(`/proc/${child.pid}/status`,'utf8')).match(/^VmRSS:\s+(\d+)/m)?.[1]||0)*1024}catch{return 0}
}
async function cleanup(){
 if(stopped)return;stopped=true;clearInterval(sampler);
 for(const socket of sockets)socket.close();
 for(const child of children.toReversed())if(child.exitCode===null)child.kill('SIGTERM');
 for(const child of children.toReversed()){
  for(let n=0;n<40&&child.exitCode===null&&child.signalCode===null;n++)await delay(25);
  if(child.exitCode===null&&child.signalCode===null)child.kill('SIGKILL');
 }
 await rm(directory,{recursive:true,force:true});
}
for(const signal of ['SIGINT','SIGTERM'])process.once(signal,()=>void cleanup().then(()=>process.exit(1)));
const begin=Buffer.from('\x1eSTART\x1f'), end=Buffer.from('\x1eEND\x1f');
function terminal(url){
 return new Promise((resolve,reject)=>{
  const start=performance.now(), socket=new WebSocket(url);socket.binaryType='arraybuffer';sockets.add(socket);
  let running, settled=false, timer=setTimeout(()=>fail(new Error('terminal readiness timeout')),10000);
  const fail=error=>{clearTimeout(timer);if(settled)return;settled=true;socket.close();sockets.delete(socket);if(running)running.reject(error);else reject(error)};
  socket.onerror=()=>fail(new Error('terminal socket error'));
  socket.onclose=()=>{if(!settled)fail(new Error('terminal closed before completion'))};
  socket.onmessage=event=>{
   if(typeof event.data==='string'){
    const frame=JSON.parse(event.data);
    if(frame.op==='error')return fail(new Error('terminal server error'));
    if(frame.op==='ready'){
     clearTimeout(timer);
     const readyMs=performance.now()-start;
     resolve({readyMs,run(bytes){
      if(running)throw new Error('terminal already used');
      return new Promise((resolve,reject)=>{
       running={resolve,reject,start:performance.now(),chunks:[],tail:Buffer.alloc(0),length:0,bytes};
       timer=setTimeout(()=>fail(new Error('terminal output timeout')),20000);
       socket.send(new TextEncoder().encode(`stty -echo; printf '\\036START\\037'; head -c ${bytes} /dev/zero | tr '\\000' x; printf '\\036END\\037'\n`));
      });
     }});
    }
    return;
   }
   if(!running)return;
   const data=Buffer.from(event.data);running.chunks.push(data);running.length+=data.length;
   if(running.length>running.bytes+65536)return fail(new Error('unexpected terminal output volume'));
   // Markers may straddle frames; inspect only when the end marker could exist.
   const tail=Buffer.concat([running.tail,data]);
   running.tail=tail.subarray(Math.max(0,tail.length-end.length+1));
   if(tail.indexOf(end)<0)return;
   const all=Buffer.concat(running.chunks), first=all.indexOf(begin), last=all.indexOf(end,first+begin.length);
   const payload=all.subarray(first+begin.length,last);
   if(first<0||payload.length!==running.bytes||payload.some(byte=>byte!==120))return fail(new Error('terminal payload mismatch'));
   const completionMs=performance.now()-running.start;
   clearTimeout(timer);settled=true;socket.close();sockets.delete(socket);
   running.resolve({completionMs,verifiedBytes:payload.length});
  };
 });
}
try {
 const [http,tcp,kcp,ssh]=await freePorts();
 const token=randomUUID(),device=`throughput-${randomUUID()}`;
 const server=launch('server',serverBin,['--http',`127.0.0.1:${http}`,'--tcp',`127.0.0.1:${tcp}`,'--kcp',`127.0.0.1:${kcp}`,'--ssh',`127.0.0.1:${ssh}`,'--data',directory,'--admin-token',token,'--no-auto-update']);
 const clientsURL=`http://127.0.0.1:${http}/api/clients?token=${token}`;
 await waitFor(async()=>{const response=await fetch(clientsURL,{signal:AbortSignal.timeout(1000)});return response.ok});
 const client=launch('client',clientBin,['--server',`tcp://127.0.0.1:${tcp}`,'--id',device,'--shell','/bin/sh','--no-auto-update']);
 await waitFor(async()=>{const response=await fetch(clientsURL,{signal:AbortSignal.timeout(1000)});return (await response.json()).some(item=>item.id===device)});
 const sampleRSS=async()=>{
  for(const [name,child] of [['server',server],['client',client]])rss[name].peak=Math.max(rss[name].peak,await readRSS(child));
 };
 for(const [name,child] of [['server',server],['client',client]])rss[name].baseline=await readRSS(child);
 await sampleRSS();sampler=setInterval(()=>void sampleRSS(),25);
 const url=`ws://127.0.0.1:${http}/terminal?device=${device}&token=${token}`;
 const warmup=await terminal(url);await warmup.run(32768);
 const samples=[];
 for(const concurrency of [1,4,8])for(let repeat=0;repeat<5;repeat++){
  const terminals=await Promise.all(Array.from({length:concurrency},()=>terminal(url)));
  const start=performance.now(),results=await Promise.all(terminals.map(term=>term.run(256*1024)));
  const wallMs=performance.now()-start,verifiedBytes=results.reduce((sum,result)=>sum+result.verifiedBytes,0);
  samples.push({concurrency,repeat:repeat+1,wallMs,verifiedBytes,MiBPerSecond:verifiedBytes/1048576/(wallMs/1000),readyMs:terminals.map(term=>term.readyMs),completionMs:results.map(result=>result.completionMs)});
  await delay(100);
 }
 await sampleRSS();
 const report={scope:'Linux loopback shell -> client TCP -> server WebSocket -> Bun receiver; no browser rendering, GPU or remote-network claims',date:new Date().toISOString(),runtime:{bun:process.versions.bun,nodeCompatibility:process.version,platform:process.platform,arch:process.arch},binaries:{serverSHA256:await hashFile(serverBin),clientSHA256:await hashFile(clientBin)},cpu:cpus()[0]?.model,bytesPerTerminal:256*1024,repeats:5,rssSamplingMs:25,rss,samples};
 const json=JSON.stringify(report,null,2)+'\n';
 if(outputArg)await writeFile(resolve(outputArg),json);else process.stdout.write(json);
} finally {await cleanup()}
