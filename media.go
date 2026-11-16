package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Format struct {
	URL        string `json:"url"`
	MIME       string `json:"mimeType"`
	Codec      string `json:"codec"`
	Quality    string `json:"quality"`
	VideoOnly  bool   `json:"videoOnly"`
	Bitrate    int64  `json:"bitrate"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	FPS        int    `json:"fps"`
	InitStart  int64  `json:"initStart"`
	InitEnd    int64  `json:"initEnd"`
	IndexStart int64  `json:"indexStart"`
	IndexEnd   int64  `json:"indexEnd"`
}
type Subtitle struct {
	URL  string `json:"url"`
	Code string `json:"code"`
	Name string `json:"name"`
	MIME string `json:"mimeType"`
}
type Video struct {
	Title     string     `json:"title"`
	Author    string     `json:"uploader"`
	Duration  int64      `json:"duration"`
	Thumbnail string     `json:"thumbnailUrl,omitempty"`
	HLS       string     `json:"hls,omitempty"`
	Videos    []Format   `json:"videoStreams"`
	Audios    []Format   `json:"audioStreams"`
	Subtitles []Subtitle `json:"subtitles"`
}
type resource struct {
	URL      string
	Manifest bool
}
type cachedVideo struct {
	video         Video
	resources     map[string]resource
	until         time.Time
	touched       time.Time
	resourceBytes int
}
type Gateway struct {
	config Config
	store  *Store
	client *http.Client
	mu     sync.Mutex
	cache  map[string]*cachedVideo
	// Serializes cache misses to bound backend extraction and memory use.
	extraction chan struct{}
	streams    chan struct{}
	manifests  chan struct{}
}

func newGateway(c Config, s *Store) *Gateway {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 8
	transport.ResponseHeaderTimeout = 30 * time.Second
	return &Gateway{config: c, store: s, client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, cache: map[string]*cachedVideo{}, extraction: make(chan struct{}, 2), streams: make(chan struct{}, 32), manifests: make(chan struct{}, 4)}
}
func (g *Gateway) allowed(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.Scheme == g.config.MediaOrigin.Scheme && u.Host == g.config.MediaOrigin.Host && u.User == nil && u.Fragment == "" && u.Opaque == "" && strings.HasPrefix(u.Path, "/")
}
func (g *Gateway) register(cv *cachedVideo, token, raw string, manifest bool) (string, error) {
	if !g.allowed(raw) {
		return "", errors.New("backend returned an unsupported resource origin")
	}
	if len(raw) > 16384 || len(cv.resources) >= 1024 || cv.resourceBytes+len(raw) > 256<<10 {
		return "", errors.New("resource limit reached")
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%t:%s", manifest, raw)))
	id := hex.EncodeToString(sum[:])
	if _, exists := cv.resources[id]; !exists {
		cv.resourceBytes += len(raw)
	}
	cv.resources[id] = resource{raw, manifest}
	return "/s/" + token + "/r/" + id, nil
}
func (g *Gateway) metadata(ctx context.Context, share Share, token string) (Video, error) {
	g.mu.Lock()
	cv := g.cache[share.ID]
	if cv != nil && time.Now().Before(cv.until) {
		cv.touched = time.Now()
		v := cv.video
		g.mu.Unlock()
		return v, nil
	}
	g.mu.Unlock()
	select {
	case g.extraction <- struct{}{}:
		defer func() { <-g.extraction }()
	case <-ctx.Done():
		return Video{}, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", g.config.Backend.String()+"/streams/"+share.VideoID, nil)
	res, err := g.client.Do(req)
	if err != nil {
		return Video{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return Video{}, errors.New("backend extraction failed")
	}
	var v Video
	if err = json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&v); err != nil {
		return v, err
	}
	if len(v.Title) > 4096 || len(v.Author) > 4096 || len(v.Videos)+len(v.Audios) > 512 || len(v.Subtitles) > 100 {
		return Video{}, errors.New("metadata exceeds limits")
	}
	now := time.Now()
	next := &cachedVideo{resources: map[string]resource{}, until: now.Add(5 * time.Minute), touched: now}
	// Only explicitly selected fields are decoded. Recommendations, account data,
	// descriptions and navigation links never enter the public response.
	rewrite := func(raw string, manifest bool) string {
		if raw == "" {
			return ""
		}
		s, e := g.register(next, token, raw, manifest)
		if e != nil {
			return ""
		}
		return s
	}
	v.Thumbnail = rewrite(v.Thumbnail, false)
	v.HLS = rewrite(v.HLS, true)
	filter := func(formats []Format) []Format {
		out := []Format{}
		for _, f := range formats {
			if len(f.Codec) > 256 || len(f.Quality) > 256 {
				continue
			}
			f.URL = rewrite(f.URL, false)
			if f.URL != "" {
				out = append(out, f)
			}
		}
		return out
	}
	v.Videos = filter(v.Videos)
	v.Audios = filter(v.Audios)
	subs := []Subtitle{}
	for _, s := range v.Subtitles {
		if len(s.Code) > 64 || len(s.Name) > 256 || len(s.MIME) > 128 {
			continue
		}
		s.URL = rewrite(s.URL, false)
		if s.URL != "" {
			subs = append(subs, s)
		}
	}
	v.Subtitles = subs
	if len(v.Videos) == 0 && v.HLS == "" {
		return v, errors.New("no supported media returned by backend")
	}
	next.video = v
	g.mu.Lock()
	defer g.mu.Unlock()
	// Retain already-issued URLs during a refresh so in-flight playback can finish.
	if old := g.cache[share.ID]; old != nil {
		for id, r := range old.resources {
			if _, exists := next.resources[id]; !exists && len(next.resources) < 1024 && next.resourceBytes+len(r.URL) <= 256<<10 {
				next.resources[id] = r
				next.resourceBytes += len(r.URL)
			}
		}
	}
	if len(g.cache) >= 64 {
		oldest := ""
		var t time.Time
		for id, c := range g.cache {
			if oldest == "" || c.touched.Before(t) {
				oldest = id
				t = c.touched
			}
		}
		delete(g.cache, oldest)
	}
	g.cache[share.ID] = next
	return v, nil
}

var uriAttribute = regexp.MustCompile(`URI="([^"\r\n]+)"`)

func (g *Gateway) rewriteHLS(body []byte, base *url.URL, shareID, token string) ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	cv := g.cache[shareID]
	if cv == nil {
		return nil, errors.New("reload player")
	}
	replace := func(s string) (string, error) {
		u, e := url.Parse(s)
		if e != nil {
			return "", e
		}
		u = base.ResolveReference(u)
		isManifest := strings.Contains(u.Path, "/manifest/") || strings.HasSuffix(u.Path, ".m3u8")
		return g.register(cv, token, u.String(), isManifest)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var out strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "#") {
			var failure error
			line = uriAttribute.ReplaceAllStringFunc(line, func(s string) string {
				parts := uriAttribute.FindStringSubmatch(s)
				r, e := replace(parts[1])
				if e != nil {
					failure = e
				}
				return `URI="` + r + `"`
			})
			if failure != nil {
				return nil, failure
			}
		} else if trim != "" {
			r, e := replace(trim)
			if e != nil {
				return nil, e
			}
			line = r
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return []byte(out.String()), scanner.Err()
}
func (g *Gateway) media(w http.ResponseWriter, r *http.Request, share Share, token, id string) {
	if r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	g.mu.Lock()
	cv := g.cache[share.ID]
	var target resource
	var ok bool
	if cv != nil {
		target, ok = cv.resources[id]
		cv.touched = time.Now()
	}
	g.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	select {
	case g.streams <- struct{}{}:
		defer func() { <-g.streams }()
	default:
		http.Error(w, "Too many concurrent streams", 429)
		return
	}
	raw, _ := url.Parse(target.URL)
	up := *g.config.MediaUpstream
	up.Path = raw.Path
	up.RawPath = raw.RawPath
	up.RawQuery = raw.RawQuery
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, r.Method, up.String(), nil)
	req.Host = g.config.MediaOrigin.Host
	// Never forward cookies, Authorization, identity, or arbitrary request headers.
	for _, key := range []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since"} {
		if s := r.Header.Get(key); s != "" {
			req.Header.Set(key, s)
		}
	}
	res, err := g.client.Do(req)
	if err != nil {
		http.Error(w, "Media unavailable", 502)
		return
	}
	defer res.Body.Close()
	// Redirects never escape the gateway or expand its allowlist.
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		http.Error(w, "Unexpected media redirect", 502)
		return
	}
	if res.StatusCode != 200 && res.StatusCode != 206 && res.StatusCode != 304 && res.StatusCode != 416 {
		http.Error(w, "Media unavailable; reload the player to refresh streams", 502)
		return
	}
	if target.Manifest && r.Method != "HEAD" && res.StatusCode == 200 {
		select {
		case g.manifests <- struct{}{}:
			defer func() { <-g.manifests }()
		default:
			http.Error(w, "Too many concurrent manifests", 429)
			return
		}
		body, e := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
		if e != nil || len(body) > 2<<20 || !strings.HasPrefix(strings.TrimSpace(string(body)), "#EXTM3U") {
			http.Error(w, "Unsupported manifest", 502)
			return
		}
		rewritten, e := g.rewriteHLS(body, raw, share.ID, token)
		if e != nil {
			http.Error(w, "Unsupported manifest resource", 502)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Write(rewritten)
		return
	}
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
		if s := res.Header.Get(key); s != "" {
			w.Header().Set(key, s)
		}
	}
	// A media response can never be interpreted as an active document.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.WriteHeader(res.StatusCode)
	if r.Method != "HEAD" {
		io.Copy(w, res.Body)
	}
}
