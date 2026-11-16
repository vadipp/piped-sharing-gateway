package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func testGateway(t *testing.T) (*Gateway, *httptest.Server) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("visitor credentials forwarded")
		}
		switch r.URL.Path {
		case "/streams/abcdefghijk":
			io.WriteString(w, `{"title":"Only this video","uploader":"An author","duration":60,"videoStreams":[{"url":"https://media.example.com/videoplayback?signature=private","mimeType":"video/mp4","quality":"360p"}],"relatedStreams":[{"url":"/watch?v=other-video"}],"description":"<a href='/search'>Search</a>","thumbnailUrl":"http://127.0.0.1/secret"}`)
		case "/videoplayback":
			if r.Host != "media.example.com" {
				t.Error("incorrect proxy virtual host")
			}
			if r.Header.Get("Range") == "bytes=4-7" {
				w.Header().Set("Content-Range", "bytes 4-7/8")
				w.WriteHeader(206)
				io.WriteString(w, "4567")
			} else {
				w.Header().Set("Content-Type", "video/mp4")
				io.WriteString(w, "01234567")
			}
		case "/redirect":
			w.Header().Set("Location", "http://127.0.0.1/secret")
			w.WriteHeader(302)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	store, err := openStore(filepath.Join(t.TempDir(), "shares.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.db.Close() })
	backend, _ := url.Parse(upstream.URL)
	media, _ := url.Parse("https://media.example.com")
	g := newGateway(Config{PublicOrigin: "https://share.example.com", AdminOrigin: "https://piped.example.com", AdminSecret: strings.Repeat("s", 32), Backend: backend, MediaOrigin: media, MediaUpstream: backend}, store)
	return g, upstream
}
func request(handler http.HandlerFunc, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}
func TestShareIsolationAndRevocation(t *testing.T) {
	g, _ := testGateway(t)
	token, e := g.store.create("abcdefghijk")
	if e != nil {
		t.Fatal(e)
	}
	prefix := "/s/" + token
	w := request(g.public, "GET", prefix+"/video", "", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "relatedStreams") || strings.Contains(w.Body.String(), "127.0.0.1") || strings.Contains(w.Body.String(), "signature=") || strings.Contains(w.Body.String(), "description") {
		t.Fatal("private or navigation data escaped")
	}
	var v Video
	json.Unmarshal(w.Body.Bytes(), &v)
	resource := v.Videos[0].URL
	w = request(g.public, "GET", resource, "", map[string]string{"Range": "bytes=4-7", "Cookie": "session=private", "Authorization": "Bearer private"})
	if w.Code != 206 || w.Body.String() != "4567" || w.Header().Get("Content-Range") == "" {
		t.Fatal("range playback failed", w.Code)
	}
	other, _ := g.store.create("other-video")
	for _, path := range []string{resource + "?url=http://127.0.0.1/", prefix + "/streams/other-video", prefix + "/video?videoId=other-video", strings.Replace(resource, token, other, 1), "/search", "/shares"} {
		if w := request(g.public, "GET", path, "", nil); w.Code != 404 {
			t.Errorf("unexpected access %s: %d", path, w.Code)
		}
	}
	if e = g.store.revoke(tokenHash(token)); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{prefix, prefix + "/video", resource} {
		if w := request(g.public, "GET", path, "", nil); w.Code != 404 {
			t.Fatal("revoked share accepted", w.Code)
		}
	}
}
func TestManagementAuthenticationAndCSRF(t *testing.T) {
	g, _ := testGateway(t)
	auth := map[string]string{"Authorization": "Bearer " + g.config.AdminSecret, "Origin": g.config.AdminOrigin, "Content-Type": "application/json"}
	if w := request(g.admin, "POST", "/shares", `{"videoId":"abcdefghijk"}`, nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	wrong := map[string]string{"Authorization": auth["Authorization"], "Origin": "https://evil.example.com", "Content-Type": "application/json"}
	if w := request(g.admin, "POST", "/shares", `{"videoId":"abcdefghijk"}`, wrong); w.Code != 403 {
		t.Fatal(w.Code)
	}
	for _, body := range []string{`{"videoId":"../../secret"}`, `{"videoId":"abcdefghijk","url":"http://localhost"}`} {
		if w := request(g.admin, "POST", "/shares", body, auth); w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	w := request(g.admin, "POST", "/shares", `{"videoId":"abcdefghijk"}`, auth)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var data map[string]string
	json.Unmarshal(w.Body.Bytes(), &data)
	if !strings.HasPrefix(data["url"], g.config.PublicOrigin+"/s/") {
		t.Fatal("wrong public origin")
	}
	w = request(g.admin, "GET", "/shares", "", auth)
	if strings.Contains(w.Body.String(), strings.TrimPrefix(data["url"], g.config.PublicOrigin+"/s/")) {
		t.Fatal("raw token stored in management listing")
	}
	w = request(g.admin, "DELETE", "/shares/"+data["id"], "", auth)
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
}
func TestManifestAllowlist(t *testing.T) {
	g, _ := testGateway(t)
	token, _ := g.store.create("abcdefghijk")
	share, _ := g.store.lookup(token)
	if _, err := g.metadata(context.Background(), share, token); err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("https://media.example.com/api/manifest/master.m3u8")
	body := []byte("#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"/key?id=1\"\nvariant.m3u8\n/segment/1.ts\n")
	got, err := g.rewriteHLS(body, base, share.ID, token)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "URI=\"/key") || strings.Contains(string(got), "variant.m3u8") || strings.Contains(string(got), "/segment/") {
		t.Fatal("unscoped manifest URL")
	}
	for _, bad := range []string{"http://127.0.0.1/private", "https://media.example.com.evil.test/segment", "https://user@media.example.com/secret", "//evil.example.com/segment"} {
		if _, err := g.rewriteHLS([]byte("#EXTM3U\n"+bad+"\n"), base, share.ID, token); err == nil {
			t.Error("unsafe manifest accepted", bad)
		}
	}
	g.mu.Lock()
	path, _ := g.register(g.cache[share.ID], token, "https://media.example.com/redirect", false)
	g.mu.Unlock()
	if w := request(g.public, "GET", path, "", nil); w.Code != 502 || w.Header().Get("Location") != "" {
		t.Fatal("redirect escaped gateway")
	}
}
func TestPersistentNonExpiringToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shares.db")
	s, e := openStore(path)
	if e != nil {
		t.Fatal(e)
	}
	token, _ := s.create("abcdefghijk")
	s.db.Exec("UPDATE shares SET created=1")
	s.db.Close()
	s, e = openStore(path)
	if e != nil {
		t.Fatal(e)
	}
	defer s.db.Close()
	if share, e := s.lookup(token); e != nil || share.VideoID != "abcdefghijk" {
		t.Fatal("old token lost across restart")
	}
}
