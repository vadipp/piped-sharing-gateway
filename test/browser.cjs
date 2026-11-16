// End-to-end adaptive playback through the real gateway and a local Piped fixture.
const { chromium } = require('playwright');
const { createServer } = require('node:http');
const fs = require('node:fs');
const { spawn, execFileSync } = require('node:child_process');
const assert = require('node:assert/strict');
const dir = process.env.FIXTURE_DIR || '/tmp/gateway-browser';
fs.mkdirSync(dir, {recursive:true});
for (const [name,size] of [['low','320x180'],['high','640x360']]) {
    execFileSync('ffmpeg',['-hide_banner','-loglevel','error','-y','-f','lavfi','-i',`testsrc=size=${size}:rate=24`,'-t','8','-an','-c:v','libx264','-profile:v','baseline','-level:v','3.0','-pix_fmt','yuv420p','-movflags','+dash+global_sidx+frag_keyframe',`${dir}/${name}.mp4`]);
}
execFileSync('ffmpeg',['-hide_banner','-loglevel','error','-y','-f','lavfi','-i','sine=frequency=440:sample_rate=44100','-t','8','-vn','-c:a','aac','-movflags','+dash+global_sidx+frag_keyframe',`${dir}/audio.mp4`]);
execFileSync('ffmpeg',['-hide_banner','-loglevel','error','-y','-i',`${dir}/low.mp4`,'-i',`${dir}/audio.mp4`,'-map','0:v','-map','1:a','-c','copy',`${dir}/combined.mp4`]);
execFileSync('ffmpeg',['-hide_banner','-loglevel','error','-y','-i',`${dir}/high.mp4`,'-i',`${dir}/audio.mp4`,'-map','0:v','-map','1:a','-c','copy',`${dir}/combined-high.mp4`]);
execFileSync('ffmpeg',['-hide_banner','-loglevel','error','-y','-i',`${dir}/combined.mp4`,'-c','copy','-hls_time','2','-hls_list_size','0','-hls_segment_filename',`${dir}/segment%03d.ts`,`${dir}/playlist.m3u8`]);
const origin = 'http://127.0.0.1:18780';
function format(name, width, height) {
    const data=fs.readFileSync(`${dir}/${name}.mp4`);
    let offset=0, indexStart, indexEnd;
    while(offset<data.length) {
        const size=data.readUInt32BE(offset), type=data.toString('ascii',offset+4,offset+8);
        if(type==='sidx') {indexStart=offset;indexEnd=offset+size-1;break;}
        if(size<8) break;
        offset+=size;
    }
    assert.ok(indexEnd>indexStart);
    const avc=data.indexOf(Buffer.from('avcC'));
    return {url:`https://media.example.com/${name}.mp4`,mimeType:width?'video/mp4':'audio/mp4',codec:width?`avc1.${data.subarray(avc+5,avc+8).toString('hex')}`:'mp4a.40.2',quality:width?`${height}p`:'AAC',videoOnly:Boolean(width),width,height,fps:24,bitrate:width?500000:128000,initStart:0,initEnd:indexStart-1,indexStart,indexEnd};
}
(async()=>{
    const upstream=createServer((req,res)=>{
        assert.equal(req.headers.cookie,undefined);
        assert.equal(req.headers.authorization,undefined);
        if(req.url==='/streams/lmnopqrstuv' || req.url==='/streams/hlsfixture1') {
            const hls=req.url.endsWith('hlsfixture1');
            res.setHeader('Content-Type','application/json');
            res.end(JSON.stringify({title:hls?'HLS fixture':'Combined fixture',duration:8,hls:hls?'https://media.example.com/playlist.m3u8':null,videoStreams:hls?[]:[{url:'https://media.example.com/combined.mp4',mimeType:'video/mp4',quality:'180p',videoOnly:false},{url:'https://media.example.com/combined-high.mp4',mimeType:'video/mp4',quality:'360p',videoOnly:false}],audioStreams:[],subtitles:[]}));return;
        }
        if(req.url==='/streams/abcdefghijk') {
            res.setHeader('Content-Type','application/json');
            res.end(JSON.stringify({title:'Shared fixture',uploader:'Fixture author',duration:8,videoStreams:[format('low',320,180),format('high',640,360)],audioStreams:[format('audio')],subtitles:[],relatedStreams:[{url:'/watch?v=other-video'}]}));return;
        }
        const name=req.url.slice(1);
        if(!['low.mp4','high.mp4','audio.mp4','combined.mp4','combined-high.mp4','playlist.m3u8'].includes(name)&&!/^segment\d{3}\.ts$/.test(name)){res.writeHead(404).end();return;}
        const data=fs.readFileSync(`${dir}/${name}`),range=req.headers.range;
        res.setHeader('Content-Type',name.endsWith('.m3u8')?'application/vnd.apple.mpegurl':name.endsWith('.ts')?'video/mp2t':name==='audio.mp4'?'audio/mp4':'video/mp4');
        res.setHeader('Accept-Ranges','bytes');
        if(range){const [,a,b]=/^bytes=(\d+)-(\d*)$/.exec(range);const start=Number(a),end=b?Math.min(Number(b),data.length-1):data.length-1;res.setHeader('Content-Range',`bytes ${start}-${end}/${data.length}`);res.writeHead(206);res.end(data.subarray(start,end+1));}
        else res.end(data);
    });
    await new Promise(resolve=>upstream.listen(18782,'127.0.0.1',resolve));
    const gateway=spawn('./gateway',[],{env:{...process.env,PUBLIC_ORIGIN:origin,ADMIN_ORIGIN:'https://piped.example.com',BACKEND_ORIGIN:'http://127.0.0.1:18782',MEDIA_ORIGIN:'https://media.example.com',MEDIA_UPSTREAM:'http://127.0.0.1:18782',ADMIN_SECRET:'fixture-management-secret-32-characters',DATABASE_PATH:`${dir}/shares.db`,PUBLIC_LISTEN:':18780',ADMIN_LISTEN:':18781'},stdio:'inherit'});
    let browser;
    try {
        for(let i=0;i<100;i++){try{if((await fetch(origin+'/healthz')).ok)break;}catch{}await new Promise(r=>setTimeout(r,100));}
        const created=await fetch('http://127.0.0.1:18781/shares',{method:'POST',headers:{Authorization:'Bearer fixture-management-secret-32-characters',Origin:'https://piped.example.com','Content-Type':'application/json'},body:JSON.stringify({videoId:'abcdefghijk'})});
        assert.equal(created.status,201);
        const share=await created.json();
        browser=await chromium.launch({headless:true,channel:'chrome'});
        const context=await browser.newContext();
        const page=await context.newPage();
        page.on('console',async msg=>{if(msg.type()==='error'||msg.type()==='warning'){const args=await Promise.all(msg.args().map(a=>a.jsonValue().catch(()=>null)));console.log('::warning::'+JSON.stringify(args));}});
        page.on('requestfailed',req=>console.log('::warning::Request failed: '+req.failure()?.errorText));
        const errors=[];page.on('pageerror',e=>errors.push(e.message));
        await page.goto(share.url+'?t=1');
        const codecs=[format('low',320,180),format('audio')].map(f=>`${f.mimeType}; codecs="${f.codec}"`);
        const supported=await page.evaluate(types=>types.map(type=>({type,supported:MediaSource.isTypeSupported(type)})),codecs);
        assert.ok(supported.every(s=>s.supported),'Fixture codecs unsupported: '+JSON.stringify(supported));
        try {await page.waitForFunction(()=>document.querySelector('video').readyState>=2,{},{timeout:30000});}
        catch(e) {console.log('::error::Player state: '+JSON.stringify(await page.evaluate(()=>({status:document.getElementById('status').textContent,mediaError:document.querySelector('video').error?.message,ready:document.querySelector('video').readyState}))));throw e;}
        assert.equal(await page.locator('h1').textContent(),'Shared fixture');
        assert.equal(await page.locator('a').count(),0);
        assert.equal(await page.locator('input[type=search]').count(),0);
        await page.locator('video').evaluate(async v=>{v.muted=true;await v.play();});
        await page.waitForFunction(()=>document.querySelector('video').currentTime>2);
        await page.locator('video').evaluate(v=>{v.currentTime=5;v.playbackRate=1.5;});
        await page.waitForFunction(()=>document.querySelector('video').currentTime>=5&&document.querySelector('video').readyState>=2);
        const qualities=await page.locator('.shaka-resolution-button').count();
        assert.ok(qualities>0,'quality selection is available');
        assert.equal(await page.locator('#status').textContent(),'');
        assert.deepEqual(errors,[]);
        assert.deepEqual(await context.cookies(),[]);
        const metadata=await (await fetch(share.url+'/video')).json();
        assert.equal(metadata.relatedStreams,undefined);
        assert.ok(metadata.videoStreams.every(f=>f.url.startsWith('/s/')));
        assert.equal((await fetch(origin+metadata.videoStreams[0].url+'?url=https://evil.example.com')).status,404);
        assert.equal((await fetch(share.url+'/video?videoId=other-video')).status,404);
        for(const videoId of ['lmnopqrstuv','hlsfixture1']) {
            const next=await (await fetch('http://127.0.0.1:18781/shares',{method:'POST',headers:{Authorization:'Bearer fixture-management-secret-32-characters',Origin:'https://piped.example.com','Content-Type':'application/json'},body:JSON.stringify({videoId})})).json();
            await page.goto(next.url);
            await page.waitForFunction(()=>document.querySelector('video').readyState>=2,{},{timeout:30000});
            await page.locator('video').evaluate(async v=>{v.muted=true;await v.play();});
            await page.waitForFunction(()=>document.querySelector('video').currentTime>1);
            assert.equal(await page.locator('a').count(),0);
            assert.equal(await page.locator('#status').textContent(),'');
            assert.equal(await page.locator('.details select').count(),0);
            assert.equal(await page.locator('#speed').count(),0);
            assert.equal(await page.locator('video').evaluate(v=>v.controls),false);
            if (videoId === 'lmnopqrstuv') {
                const openSettings = async () => {
                    await page.locator('video').hover();
                    if (!await page.locator('.shaka-overflow-menu').isVisible()) await page.locator('.shaka-overflow-menu-button').click();
                };
                assert.equal(await page.locator('video').evaluate(v=>v.videoWidth),320);
                await page.locator('video').evaluate(v=>{v.currentTime=3;v.playbackRate=1.5;});
                await openSettings();
                await page.locator('.shared-quality-button').click();
                await page.locator('.shared-quality-option').filter({hasText:'360p'}).click();
                await page.waitForFunction(()=>{const v=document.querySelector('video');return v.videoWidth===640&&v.currentTime>=3&&!v.paused;},{},{timeout:30000});
                assert.equal(await page.locator('video').evaluate(v=>v.playbackRate),1.5);
                await page.locator('video').evaluate(v=>v.pause());
                await openSettings();
                await page.locator('.shared-quality-button').click();
                await page.locator('.shared-quality-option').filter({hasText:'180p'}).click();
                await page.waitForFunction(()=>document.querySelector('video').videoWidth===320);
                assert.equal(await page.locator('video').evaluate(v=>v.paused),true);
                assert.equal(await page.locator('video').evaluate(v=>v.playbackRate),1.5);
                await openSettings();
                await page.locator('.shaka-playbackrate-button').click();
                await page.locator('.shaka-playback-rates button').filter({hasText:/^2x$/}).click();
                assert.equal(await page.locator('video').evaluate(v=>v.playbackRate),2);
            }
        }
        const revoked=await fetch('http://127.0.0.1:18781/shares/'+share.id,{method:'DELETE',headers:{Authorization:'Bearer fixture-management-secret-32-characters',Origin:'https://piped.example.com'}});
        assert.equal(revoked.status,204);
        assert.equal((await fetch(origin+metadata.videoStreams[0].url)).status,404);
        assert.equal((await fetch(share.url)).status,404);
        console.log('Browser DASH, HLS and combined playback, controls, seek, isolation and revocation passed.');
    } finally {if(browser)await browser.close();gateway.kill('SIGTERM');upstream.close();}
})().catch(e=>{console.error("::error::"+String(e.stack || e).replaceAll("\n","%0A"));process.exitCode=1;});
