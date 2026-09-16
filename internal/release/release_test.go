package release

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
	"strings"
	"testing"
)

func TestVersionForms(t *testing.T) {
	cases := []struct {
		in          string
		tag, number string
		valid       bool
	}{
		{"v6.1.8", "v6.1.8", "6.1.8", true},
		{"6.1.8", "v6.1.8", "6.1.8", true},
		{"v6.1", "v6.1", "6.1", true},
		{"", "v", "", false},
		{"v6", "v6", "6", false}, // needs at least major.minor
		{"v6.1.8-rc1", "v6.1.8-rc1", "6.1.8-rc1", false},
		{"../../etc", "v../../etc", "../../etc", false},
		{"v6.1.x", "v6.1.x", "6.1.x", false},
	}
	for _, c := range cases {
		v := Version(c.in)
		if got := v.Tag(); got != c.tag {
			t.Errorf("Version(%q).Tag() = %q, want %q", c.in, got, c.tag)
		}
		if got := v.number(); got != c.number {
			t.Errorf("Version(%q).number() = %q, want %q", c.in, got, c.number)
		}
		if got := v.Valid(); got != c.valid {
			t.Errorf("Version(%q).Valid() = %v, want %v", c.in, got, c.valid)
		}
	}
}

func TestAssetName(t *testing.T) {
	cases := map[[2]string]string{
		{"linux", "amd64"}:  "layer_Linux_x86_64.tar.gz",
		{"linux", "arm64"}:  "layer_Linux_arm64.tar.gz",
		{"darwin", "arm64"}: "layer_Darwin_arm64.tar.gz",
	}
	for in, want := range cases {
		got, err := AssetName(in[0], in[1])
		if err != nil {
			t.Fatalf("AssetName(%q,%q): %v", in[0], in[1], err)
		}
		if got != want {
			t.Errorf("AssetName(%q,%q) = %q, want %q", in[0], in[1], got, want)
		}
	}
	if _, err := AssetName("windows", "amd64"); err == nil {
		t.Error("expected an error for an unsupported OS")
	}
}

// tarballWith builds a gzipped tar containing one file.
func tarballWith(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// releaseServer serves a fake GitHub release. checksumOverride, when non-empty,
// is published instead of the real digest so a mismatch can be exercised.
func releaseServer(t *testing.T, asset string, tarball []byte, checksumOverride string) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(tarball)
	digest := hex.EncodeToString(sum[:])
	if checksumOverride != "" {
		digest = checksumOverride
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "_checksums.txt"):
			fmt.Fprintf(w, "%s  %s\n", digest, asset)
		case strings.HasSuffix(r.URL.Path, asset):
			_, _ = w.Write(tarball)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// clientFor points a Client at the test server by overriding the download host.
func clientFor(srv *httptest.Server) *Client {
	c := New("owner/repo", 0)
	c.baseURL = srv.URL
	return c
}

func TestFetchBinaryVerifiesChecksum(t *testing.T) {
	asset := "layer_Linux_x86_64.tar.gz"
	tarball := tarballWith(t, "layerd", []byte("fake-binary-content"))

	srv := releaseServer(t, asset, tarball, "")
	defer srv.Close()

	bin, err := clientFor(srv).FetchBinary(context.Background(), "v6.1.8", "linux", "amd64")
	if err != nil {
		t.Fatalf("FetchBinary: %v", err)
	}
	if string(bin) != "fake-binary-content" {
		t.Fatalf("extracted %q, want the binary content", string(bin))
	}
}

// A tarball that does not match its published checksum must never be installed -
// this is the only thing standing between us and running an unexpected binary as
// the validator.
func TestFetchBinaryRejectsChecksumMismatch(t *testing.T) {
	asset := "layer_Linux_x86_64.tar.gz"
	tarball := tarballWith(t, "layerd", []byte("fake-binary-content"))

	srv := releaseServer(t, asset, tarball, strings.Repeat("00", 32))
	defer srv.Close()

	_, err := clientFor(srv).FetchBinary(context.Background(), "v6.1.8", "linux", "amd64")
	if err == nil {
		t.Fatal("expected a checksum mismatch error")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v, want a checksum mismatch", err)
	}
}

func TestFetchBinaryRejectsInvalidVersion(t *testing.T) {
	if _, err := New("owner/repo", 0).FetchBinary(context.Background(), "../../evil", "linux", "amd64"); err == nil {
		t.Fatal("expected an invalid-version error")
	}
}

func TestFetchBinaryErrorsWhenArchiveLacksBinary(t *testing.T) {
	asset := "layer_Linux_x86_64.tar.gz"
	tarball := tarballWith(t, "README.md", []byte("not a binary"))

	srv := releaseServer(t, asset, tarball, "")
	defer srv.Close()

	_, err := clientFor(srv).FetchBinary(context.Background(), "v6.1.8", "linux", "amd64")
	if err == nil {
		t.Fatal("expected an error when layerd is absent from the archive")
	}
}
