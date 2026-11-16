package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"
)

//go:embed web
var assets embed.FS
var videoIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func headers(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; media-src 'self' blob:; connect-src 'self'; worker-src blob:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func (g *Gateway) public(w http.ResponseWriter, r *http.Request) {
	headers(w)
	if r.Method != "GET" && r.Method != "HEAD" {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", 405)
		return
	}
	if r.URL.Path == "/healthz" {
		w.Write([]byte("ok\n"))
		return
	}
	if r.URL.Path == "/robots.txt" {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("User-agent: *\nDisallow: /\n"))
		return
	}
	if strings.HasPrefix(r.URL.Path, "/assets/") {
		name := strings.TrimPrefix(r.URL.Path, "/assets/")
		switch name {
		case "player.js", "player.css", "vendor/shaka-player.ui.js", "vendor/controls.css":
		default:
			http.NotFound(w, r)
			return
		}
		sub, _ := fs.Sub(assets, "web")
		http.StripPrefix("/assets/", http.FileServer(http.FS(sub))).ServeHTTP(w, r)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "s" {
		http.NotFound(w, r)
		return
	}
	token := parts[1]
	share, err := g.store.lookup(token)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 2 && r.URL.Path == "/s/"+token {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		body, _ := assets.ReadFile("web/index.html")
		w.Write(body)
		return
	}
	if len(parts) == 3 && parts[2] == "video" && (r.URL.RawQuery == "" || r.URL.RawQuery == "refresh=1") {
		if r.URL.RawQuery == "refresh=1" {
			g.mu.Lock()
			if cv := g.cache[share.ID]; cv != nil && time.Now().After(cv.until.Add(-4*time.Minute-30*time.Second)) {
				cv.until = time.Time{}
			}
			g.mu.Unlock()
		}
		ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
		defer cancel()
		v, e := g.metadata(ctx, share, token)
		if e != nil {
			http.Error(w, "Video temporarily unavailable. Please retry.", 502)
			return
		}
		writeJSON(w, v)
		return
	}
	if len(parts) == 4 && parts[2] == "r" && hashPattern.MatchString(parts[3]) {
		g.media(w, r, share, token, parts[3])
		return
	}
	http.NotFound(w, r)
}
func (g *Gateway) admin(w http.ResponseWriter, r *http.Request) {
	headers(w)
	supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(supplied), []byte(g.config.AdminSecret)) != 1 {
		http.Error(w, "Unauthorized", 401)
		return
	}
	// The management port is private. A reverse proxy must authenticate users,
	// strip inbound Authorization, and inject its own gateway credential.
	if r.Method != "GET" && r.Header.Get("Origin") != g.config.AdminOrigin {
		http.Error(w, "Forbidden origin", 403)
		return
	}
	if r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == "/shares" {
		switch r.Method {
		case "POST":
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				http.Error(w, "Expected JSON", 415)
				return
			}
			var input struct {
				VideoID string `json:"videoId"`
			}
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
			dec.DisallowUnknownFields()
			if dec.Decode(&input) != nil || !videoIDPattern.MatchString(input.VideoID) {
				http.Error(w, "Invalid video ID", 400)
				return
			}
			token, err := g.store.create(input.VideoID)
			if err != nil {
				http.Error(w, "Unable to create share", 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(201)
			writeJSON(w, map[string]string{"url": g.config.PublicOrigin + "/s/" + token, "id": tokenHash(token)})
		case "GET":
			shares, err := g.store.list()
			if err != nil {
				http.Error(w, "Unable to list shares", 500)
				return
			}
			writeJSON(w, shares)
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "Method not allowed", 405)
		}
		return
	}
	if strings.HasPrefix(r.URL.Path, "/shares/") && r.Method == "DELETE" {
		id := strings.TrimPrefix(r.URL.Path, "/shares/")
		if !hashPattern.MatchString(id) || g.store.revoke(id) != nil {
			http.NotFound(w, r)
			return
		}
		g.mu.Lock()
		delete(g.cache, id)
		g.mu.Unlock()
		w.WriteHeader(204)
		return
	}
	http.NotFound(w, r)
}
func envOr(name, fallback string) string {
	if s := os.Getenv(name); s != "" {
		return s
	}
	return fallback
}
func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		c := http.Client{Timeout: 3 * time.Second}
		res, e := c.Get("http://127.0.0.1" + envOr("PUBLIC_LISTEN", ":8080") + "/healthz")
		if e != nil {
			os.Exit(1)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	c, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	store, err := openStore(envOr("DATABASE_PATH", "/data/shares.db"))
	if err != nil {
		log.Fatal("Cannot open share database")
	}
	defer store.db.Close()
	g := newGateway(c, store)
	public := &http.Server{Addr: envOr("PUBLIC_LISTEN", ":8080"), Handler: http.HandlerFunc(g.public), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	admin := &http.Server{Addr: envOr("ADMIN_LISTEN", ":8081"), Handler: http.HandlerFunc(g.admin), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	for _, server := range []*http.Server{public, admin} {
		go func(s *http.Server) {
			if e := s.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
				log.Print("HTTP listener failed")
				stop()
			}
		}(server)
	}
	log.Print("Sharing gateway started")
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	public.Shutdown(shutdown)
	admin.Shutdown(shutdown)
}
