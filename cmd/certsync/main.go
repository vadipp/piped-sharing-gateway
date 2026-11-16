// certsync installs a validated certificate pair fetched through a restricted SSH
// export command. It never logs or exposes certificate private-key material.
package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type pair struct{ cert, key []byte }
type cappedWriter struct {
	bytes.Buffer
	remaining int
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		return 0, errors.New("export exceeds size limit")
	}
	n, e := w.Buffer.Write(p)
	w.remaining -= n
	return n, e
}
func fetch(ctx context.Context, config, alias string) (pair, error) {
	var out cappedWriter
	out.remaining = 64 << 10
	cmd := exec.CommandContext(ctx, "ssh", "-F", config, "--", alias)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return pair{}, errors.New("SSH certificate export failed")
	}
	return unpack(out.Bytes())
}
func unpack(data []byte) (pair, error) {
	if len(data) > 64<<10 {
		return pair{}, errors.New("export exceeds size limit")
	}
	var p pair
	r := tar.NewReader(bytes.NewReader(data))
	seen := map[string]bool{}
	for {
		h, e := r.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return p, errors.New("invalid export archive")
		}
		if (h.Name != "fullchain.pem" && h.Name != "privkey.pem") || seen[h.Name] || h.Typeflag != tar.TypeReg || h.Size <= 0 || h.Size > 32<<10 {
			return p, errors.New("unexpected export entry")
		}
		seen[h.Name] = true
		b, e := io.ReadAll(r)
		if e != nil {
			return p, e
		}
		if h.Name == "fullchain.pem" {
			p.cert = b
		} else {
			p.key = b
		}
	}
	if len(seen) != 2 {
		return p, errors.New("incomplete certificate export")
	}
	return p, nil
}
func validate(p pair, hostname string, roots *x509.CertPool, now time.Time) error {
	parsed, e := tls.X509KeyPair(p.cert, p.key)
	if e != nil {
		return errors.New("invalid or mismatched certificate pair")
	}
	leaf, e := x509.ParseCertificate(parsed.Certificate[0])
	if e != nil {
		return errors.New("invalid leaf certificate")
	}
	if leaf.NotAfter.Before(now.Add(24 * time.Hour)) {
		return errors.New("certificate expires in less than 24 hours")
	}
	intermediates := x509.NewCertPool()
	for _, der := range parsed.Certificate[1:] {
		c, e := x509.ParseCertificate(der)
		if e != nil {
			return errors.New("invalid intermediate certificate")
		}
		intermediates.AddCert(c)
	}
	if _, e = leaf.Verify(x509.VerifyOptions{DNSName: hostname, Roots: roots, Intermediates: intermediates, CurrentTime: now}); e != nil {
		return fmt.Errorf("certificate validation failed: %w", e)
	}
	return nil
}
func writeFile(path string, data []byte, mode os.FileMode) error {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if e != nil {
		return e
	}
	if _, e = f.Write(data); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	return f.Close()
}
func syncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func safeRelative(path string) bool {
	return path != "" && !filepath.IsAbs(path) && filepath.Clean(path) == path && path != ".." && !strings.HasPrefix(path, ".."+string(os.PathSeparator)) && path != "."
}
func digest(p []byte) string { h := sha256.Sum256(p); return hex.EncodeToString(h[:]) }
func install(p pair, dest, current, marker string) (bool, error) {
	if !safeRelative(current) {
		return false, errors.New("current must be a relative path inside destination")
	}
	if e := os.MkdirAll(dest, 0700); e != nil {
		return false, e
	}
	lock, e := os.OpenFile(filepath.Join(dest, ".sync.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return false, e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return false, errors.New("another sync is running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	link := filepath.Join(dest, current)
	oldCert, _ := os.ReadFile(filepath.Join(link, "fullchain.pem"))
	oldKey, _ := os.ReadFile(filepath.Join(link, "privkey.pem"))
	if bytes.Equal(oldCert, p.cert) {
		if _, e = tls.X509KeyPair(oldCert, oldKey); e == nil {
			return false, nil
		}
	}
	revision := digest(p.cert)
	versions := filepath.Join(dest, "versions")
	version := filepath.Join(versions, revision)
	if e = os.MkdirAll(versions, 0700); e != nil {
		return false, e
	}
	if existing, e := os.ReadFile(filepath.Join(version, "fullchain.pem")); e == nil {
		key, e := os.ReadFile(filepath.Join(version, "privkey.pem"))
		if e != nil {
			return false, e
		}
		if !bytes.Equal(existing, p.cert) {
			return false, errors.New("existing version does not match its digest")
		}
		if _, e = tls.X509KeyPair(existing, key); e != nil {
			return false, errors.New("existing version has an invalid key")
		}
	} else {
		stage, e := os.MkdirTemp(versions, ".incoming-")
		if e != nil {
			return false, e
		}
		defer os.RemoveAll(stage)
		if e = writeFile(filepath.Join(stage, "fullchain.pem"), p.cert, 0644); e != nil {
			return false, e
		}
		if e = writeFile(filepath.Join(stage, "privkey.pem"), p.key, 0600); e != nil {
			return false, e
		}
		if e = syncDir(stage); e != nil {
			return false, e
		}
		if e = os.Rename(stage, version); e != nil {
			return false, e
		}
		if e = syncDir(versions); e != nil {
			return false, e
		}
	}
	parent := filepath.Dir(link)
	if e = os.MkdirAll(parent, 0700); e != nil {
		return false, e
	}
	oldTarget, _ := os.Readlink(link)
	target, e := filepath.Rel(parent, version)
	if e != nil {
		return false, e
	}
	temp, e := os.CreateTemp(parent, ".current-")
	if e != nil {
		return false, e
	}
	next := temp.Name()
	temp.Close()
	os.Remove(next)
	defer os.Remove(next)
	if e = os.Symlink(target, next); e != nil {
		return false, e
	}
	// Persist notification before the switch, so a process interruption cannot
	// silently leave nginx using its previously loaded certificate indefinitely.
	if marker != "" {
		f, err := os.OpenFile(marker, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return false, err
		}
		_, e = f.Write([]byte("certificate changed\n"))
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e == nil {
			e = closeErr
		}
		if e != nil {
			return false, e
		}
		if e = syncDir(filepath.Dir(marker)); e != nil {
			return false, e
		}
	}
	if e = os.Rename(next, link); e != nil {
		return false, e
	}
	if e = syncDir(parent); e != nil {
		return false, e
	}
	entries, _ := os.ReadDir(versions)
	previous := filepath.Base(oldTarget)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() && len(name) == 64 && name != revision && name != previous {
			if _, e := hex.DecodeString(name); e == nil {
				os.RemoveAll(filepath.Join(versions, name))
			}
		}
	}
	return true, nil
}
func run() error {
	config := flag.String("ssh-config", "", "SSH config containing strict host-key verification and restricted identity")
	alias := flag.String("ssh-alias", "certificate-source", "Host alias in that config")
	hostname := flag.String("hostname", "", "Hostname the certificate must cover")
	dest := flag.String("destination", "", "Writable certificate directory")
	current := flag.String("current", "current", "Relative symlink nginx reads below destination")
	marker := flag.String("changed-file", "", "Optional notification file for a host-side nginx reload")
	flag.Parse()
	if *config == "" || *hostname == "" || *dest == "" || strings.HasPrefix(*alias, "-") {
		return errors.New("ssh-config, hostname and destination are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, e := fetch(ctx, *config, *alias)
	if e != nil {
		return e
	}
	if e = validate(p, *hostname, nil, time.Now()); e != nil {
		return e
	}
	changed, e := install(p, *dest, *current, *marker)
	if e != nil {
		return e
	}
	if changed {
		fmt.Println("Certificate validated and updated")
	} else {
		fmt.Println("Certificate unchanged")
	}
	return nil
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
