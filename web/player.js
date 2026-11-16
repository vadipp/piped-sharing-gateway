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
function playbackError() {
    status.textContent = "Playback failed. Retry to refresh the streams.";
    retry.hidden = false;
}

// Combined MP4 files have no adaptive variants for Shaka's built-in quality menu.
// Register a settings submenu that switches only the formats returned by this share.
function registerCombinedQuality(formats, change) {
    shaka.ui.OverflowMenu.registerElement("combined_quality", {
        create(parent, controls) {
            return new class extends shaka.ui.SettingsMenu {
                constructor() {
                    super(parent, controls, "M80-720v480h800v-480H80Zm80 80h640v320H160v-320Z");
                    this.button.classList.add("shared-quality-button");
                    this.menu.classList.add("shared-quality-menu");
                    this.button.setAttribute("aria-label", "Quality");
                    this.nameSpan.textContent = "Quality";
                    this.backSpan.textContent = "Quality";
                    this.options = formats.map((format, index) => {
                        const option = document.createElement("button");
                        option.type = "button";
                        option.className = "shared-quality-option";
                        option.textContent = format.quality || format.mimeType;
                        option.addEventListener("click", async () => {
                            this.options.forEach(item => { item.disabled = true; });
                            try { await change(index); this.select(index); }
                            catch { playbackError(); }
                            finally { this.options.forEach(item => { item.disabled = false; }); }
                        });
                        this.menu.append(option);
                        return option;
                    });
                    this.select(0);
                }
                select(index) {
                    this.currentSelection.textContent = this.options[index].textContent;
                    this.button.setAttribute("shaka-status", this.currentSelection.textContent);
                    this.options.forEach((option, i) => {
                        option.setAttribute("aria-selected", String(i === index));
                        option.classList.toggle("shaka-chosen-item", i === index);
                    });
                }
            }();
        },
    });
}

async function load() {
    retry.hidden = true;
    status.textContent = "Loading video…";
    try {
        const res = await fetch(location.pathname + "/video?refresh=1", {credentials:"omit", cache:"no-store"});
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
        const supported = shaka.Player.isBrowserSupported();
        const adaptive = supported && (mpd || data.hls);
        document.getElementById("native-quality").hidden = supported;
        if (supported) {
            if (!adaptive && !combined.length) throw new Error("No playable formats available.");
            video.controls = false;
            player = new shaka.Player();
            await player.attach(video);
            let selected = 0;
            const addSubtitles = async () => {
                for (const sub of data.subtitles || []) {
                    try { await player.addTextTrackAsync(sub.url, sub.code || "und", "subtitle", sub.mimeType, undefined, sub.name); } catch { /* Unsupported subtitles do not block playback. */ }
                }
            };
            if (!adaptive) registerCombinedQuality(combined, async index => {
                if (index === selected) return;
                const time = video.currentTime;
                const playing = !video.paused;
                const rate = video.playbackRate;
                await player.load(combined[index].url, time, combined[index].mimeType);
                video.playbackRate = rate;
                await addSubtitles();
                selected = index;
                if (playing) await video.play();
            });
            overlay = new shaka.ui.Overlay(player, document.getElementById("container"), video);
            overlay.configure({overflowMenuButtons:[adaptive ? "quality" : "combined_quality", "language", "captions", "playback_rate"], playbackRates:[0.5, 0.75, 1, 1.25, 1.5, 2], castReceiverAppId:""});
            player.configure({streaming:{bufferingGoal:30, rebufferingGoal:2}});
            player.addEventListener("error", playbackError);
            if (mpd) manifestURL = URL.createObjectURL(new Blob([mpd], {type:"application/dash+xml"}));
            await player.load(adaptive ? (manifestURL || data.hls) : combined[0].url, start, adaptive ? (mpd ? "application/dash+xml" : "application/x-mpegurl") : combined[0].mimeType);
            await addSubtitles();
        } else {
            if (!combined.length) throw new Error("This browser cannot play the available formats.");
            video.controls = true;
            const select = document.getElementById("quality");
            select.replaceChildren(...combined.map((f,i) => new Option(f.quality || f.mimeType, String(i))));
            let restore;
            const change = time => {
                const playing = !video.paused;
                const rate = video.playbackRate;
                if (restore) video.removeEventListener("loadedmetadata", restore);
                restore = () => { video.currentTime=time;video.playbackRate=rate;if(playing)video.play().catch(()=>{}); };
                video.addEventListener("loadedmetadata", restore, {once:true});
                video.src = combined[Number(select.value)].url;
            };
            select.onchange=()=>change(video.currentTime);
            video.querySelectorAll("track").forEach(track => track.remove());
            for (const sub of data.subtitles || []) {
                if (sub.mimeType !== "text/vtt") continue;
                const track = document.createElement("track");
                track.kind = "subtitles"; track.src = sub.url; track.srclang = sub.code || "und"; track.label = sub.name || sub.code;
                video.append(track);
            }
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
