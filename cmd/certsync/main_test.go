package main

import (
	"archive/tar"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixture(t *testing.T, serial int64) (pair, *x509.CertPool) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now()
	c := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "*.example.com"}, DNSNames: []string{"*.example.com"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(90 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, e := x509.CreateCertificate(rand.Reader, c, c, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	p := pair{pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw})}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(p.cert)
	return p, roots
}
func TestValidation(t *testing.T) {
	p, roots := fixture(t, 1)
	if e := validate(p, "video.example.com", roots, time.Now()); e != nil {
		t.Fatal(e)
	}
	if e := validate(p, "video.other.example", roots, time.Now()); e == nil {
		t.Fatal("wrong domain accepted")
	}
	if e := validate(p, "video.example.com", roots, time.Now().Add(90*24*time.Hour)); e == nil {
		t.Fatal("expired certificate accepted")
	}
	other, _ := fixture(t, 2)
	p.key = other.key
	if e := validate(p, "video.example.com", roots, time.Now()); e == nil {
		t.Fatal("mismatched key accepted")
	}
}
func archive(t *testing.T, p pair, extra string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, f := range []struct {
		name string
		data []byte
	}{{"fullchain.pem", p.cert}, {"privkey.pem", p.key}} {
		w.WriteHeader(&tar.Header{Name: f.name, Mode: 0600, Size: int64(len(f.data)), Typeflag: tar.TypeReg})
		w.Write(f.data)
	}
	if extra != "" {
		w.WriteHeader(&tar.Header{Name: extra, Mode: 0600, Size: 1, Typeflag: tar.TypeReg})
		w.Write([]byte("x"))
	}
	w.Close()
	return b.Bytes()
}
func TestArchiveScope(t *testing.T) {
	p, _ := fixture(t, 1)
	got, e := unpack(archive(t, p, ""))
	if e != nil || !bytes.Equal(got.key, p.key) {
		t.Fatal("valid export rejected", e)
	}
	for _, name := range []string{"../../outside", "fullchain.pem", "unexpected"} {
		if _, e := unpack(archive(t, p, name)); e == nil {
			t.Fatal("unsafe archive accepted", name)
		}
	}
	if _, e := unpack(make([]byte, 65<<10)); e == nil {
		t.Fatal("oversized archive accepted")
	}
}
func TestAtomicInstallAndRetention(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "needs-reload")
	p, _ := fixture(t, 1)
	changed, e := install(p, dir, "live/current", marker)
	if e != nil || !changed {
		t.Fatal(e)
	}
	info, e := os.Stat(filepath.Join(dir, "live/current/privkey.pem"))
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("key permissions", e)
	}
	if _, e = os.Stat(marker); e != nil {
		t.Fatal("no reload notification")
	}
	os.Remove(marker)
	changed, e = install(p, dir, "live/current", marker)
	if e != nil || changed {
		t.Fatal("unchanged certificate replaced", e)
	}
	if _, e = os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("unchanged certificate notified reload")
	}
	for i := int64(2); i <= 3; i++ {
		next, _ := fixture(t, i)
		if _, e = install(next, dir, "live/current", marker); e != nil {
			t.Fatal(e)
		}
		p = next
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "versions"))
	if len(entries) != 2 {
		t.Fatal("retention", len(entries))
	}
	current, _ := os.ReadFile(filepath.Join(dir, "live/current/fullchain.pem"))
	if !bytes.Equal(current, p.cert) {
		t.Fatal("current certificate not switched")
	}
	if _, e = install(p, dir, "../outside", marker); e == nil {
		t.Fatal("destination traversal accepted")
	}
}
