package agentdocs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func archive(t *testing.T, dir string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, b := range map[string][]byte{dir + "/open-agent-docs": body, dir + "/README.md": []byte("x"), "../evil": []byte("no")} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(b)), Typeflag: tar.TypeReg})
		_, _ = tw.Write(b)
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func TestInstallChecksLatestRelease(t *testing.T) {
	if !LocalSupported() {
		t.Skip("no upstream build for this platform")
	}
	asset := "open-agent-docs-v9.0.0-" + releaseTriples[platform()]
	good := archive(t, asset, []byte("#!/bin/sh\necho hi\n"))
	sum := sha256.Sum256(good)
	served := good
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			fmt.Fprintf(w, `{"tag_name":"v9.0.0","body":"notes","assets":[{"name":"SHA256SUMS","browser_download_url":"%[1]s/sums"},{"name":"%[2]s.tar.gz","browser_download_url":"%[1]s/a"}]}`, "http://"+r.Host, asset)
		case "/sums":
			fmt.Fprintf(w, "%s  %s.tar.gz\n", hex.EncodeToString(sum[:]), asset)
		case "/a":
			_, _ = w.Write(served)
		}
	}))
	defer srv.Close()
	old := releaseAPI
	releaseAPI = srv.URL + "/latest"
	defer func() { releaseAPI = old }()

	s := &Service{dir: t.TempDir()}
	ctx := context.Background()
	rel, err := s.latest(ctx, true)
	if err != nil || rel.Tag != "v9.0.0" || rel.Notes != "notes" {
		t.Fatalf("%+v %v", rel, err)
	}
	// a tampered download is refused, nothing installed
	served = archive(t, asset, []byte("tampered"))
	if _, err := s.install(ctx, rel); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("tampered: %v", err)
	}
	if _, err := os.Stat(s.binPath("v9.0.0")); err == nil {
		t.Fatal("installed anyway")
	}
	served = good
	bin, err := s.install(ctx, rel)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(bin); !bytes.Contains(b, []byte("echo hi")) {
		t.Fatalf("binary %q", b)
	}
	if _, err := os.Stat(s.appDir() + "/../evil"); err == nil {
		t.Fatal("unpacked outside")
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"v0.3.0", "v0.2.0", true}, {"v0.2.0", "v0.2.0", false}, {"v0.10.0", "v0.9.1", true}, {"v1.0.0", "v1.0.0-rc1", false}, {"v0.2.1", "v0.3.0", false}} {
		if got := newer(c.a, c.b); got != c.want {
			t.Errorf("%s > %s: %v", c.a, c.b, got)
		}
	}
}
