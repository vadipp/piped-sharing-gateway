/* All URLs below come from the capability gateway, never from location parameters. */
"use strict";
const video = document.getElementById("video");
const status = document.getElementById("status");
const retry = document.getElementById("retry");
let player;
let overlay;
let manifestURL;
const initialTime = Math.max(0, Number(new URL(location.href).searchParams.get("t")) || 0);
const esc = value => String(value).replace(/[<>&"']/g, c => ({"<":"&lt;", ">":"&gt;", "&":"&amp;", '"':"&quot;", "'":"&apos;"})[c]);

function dashManifest(data) {
    const formats = [...data.videoStreams, ...data.audioStreams].filter(f =>
        (f.videoOnly || f.mimeType?.startsWith("audio/")) && f.indexEnd > f.indexStart && f.initEnd > 0);
    if (!formats.some(f => f.videoOnly) || !formats.some(f => f.mimeType.startsWith("audio/"))) return null;
    const groups = new Map();
    for (const f of formats) {
        const key = f.mimeType;
        if (!groups.has(key)) groups.set(key, []);
        groups.get(key).push(f);
    }
    let id = 0;
    return `<?xml version="1.0"?><MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" profiles="urn:mpeg:dash:profile:isoff-on-demand:2011" minBufferTime="PT1.5S" mediaPresentationDuration="PT${Number(data.duration)}S"><Period>` +
        [...groups].map(([mime, items]) => `<AdaptationSet mimeType="${esc(mime)}" segmentAlignment="true">` + items.map(f =>
            `<Representation id="${id++}" bandwidth="${Number(f.bitrate) || 1}" codecs="${esc(f.codec)}"` +
            (f.videoOnly ? ` width="${Number(f.width)}" height="${Number(f.height)}" frameRate="${Number(f.fps) || 30}"` : "") +
            `><BaseURL>${esc(new URL(f.url, location.origin).href)}</BaseURL><SegmentBase indexRange="${Number(f.indexStart)}-${Number(f.indexEnd)}"><Initialization range="${Number(f.initStart)}-${Number(f.initEnd)}"/></SegmentBase></Representation>`
        ).join("") + "</AdaptationSet>").join("") + "</Period></MPD>";
}
async function load() {
    retry.hidden = true;
    status.textContent = "Loading video…";
    try {
        const res = await fetch(location.pathname + "/video", {credentials:"omit", cache:"no-store"});
        if (!res.ok) throw new Error("This shared video is unavailable or its link has been revoked.");
        const data = await res.json();
        document.title = data.title || "Shared video";
        document.getElementById("title").textContent = data.title;
        document.getElementById("author").textContent = data.uploader;
        if (data.thumbnailUrl) video.poster = data.thumbnailUrl;
        const start = video.currentTime || initialTime;
        if (overlay) { await overlay.destroy(); overlay = null; }
        if (player) { await player.destroy(); player = null; }
        if (manifestURL) { URL.revokeObjectURL(manifestURL); manifestURL = null; }
        const mpd = dashManifest(data);
        const combined = data.videoStreams.filter(f => !f.videoOnly);
        shaka.polyfill.installAll();
        const adaptive = shaka.Player.isBrowserSupported() && (mpd || data.hls);
        document.getElementById("fallback").hidden = Boolean(adaptive);
        if (adaptive) {
            video.controls = false;
            player = new shaka.Player();
            await player.attach(video);
            overlay = new shaka.ui.Overlay(player, document.getElementById("container"), video);
            overlay.configure({overflowMenuButtons:["quality", "language", "captions", "playback_rate"], playbackRates:[0.5, 0.75, 1, 1.25, 1.5, 2], castReceiverAppId:""});
            player.configure({streaming:{bufferingGoal:30, rebufferingGoal:2}});
            player.addEventListener("error", () => {status.textContent="Playback failed. Retry to refresh the streams.";retry.hidden=false;});
            if (mpd) manifestURL = URL.createObjectURL(new Blob([mpd], {type:"application/dash+xml"}));
            await player.load(manifestURL || data.hls, start, mpd ? "application/dash+xml" : "application/x-mpegurl");
            for (const sub of data.subtitles || []) {
                try { await player.addTextTrackAsync(sub.url, sub.code || "und", "subtitle", sub.mimeType, undefined, sub.name); } catch { /* Unsupported subtitle formats do not block playback. */ }
            }
        } else {
            if (!combined.length) throw new Error("This browser cannot play the available formats.");
            video.controls = true;
            const select = document.getElementById("quality");
            select.replaceChildren(...combined.map((f,i) => new Option(f.quality || f.mimeType, String(i))));
            const change = time => { video.src=combined[Number(select.value)].url;video.addEventListener("loadedmetadata",()=>{video.currentTime=time;},{once:true}); };
            select.onchange=()=>{const time=video.currentTime;const playing=!video.paused;change(time);if(playing)video.play().catch(()=>{});};
            document.getElementById("speed").onchange=e=>{video.playbackRate=Number(e.target.value);};
            change(start);
        }
        status.textContent="";
    } catch (err) {
        status.textContent=err.message || "Playback failed. Please retry.";
        retry.hidden=false;
    }
}
video.addEventListener("error",()=>{status.textContent="Playback failed. Retry to refresh the streams.";retry.hidden=false;});
retry.addEventListener("click", load);
load();
