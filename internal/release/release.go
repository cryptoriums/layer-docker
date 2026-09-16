// Package release resolves and downloads official Tellor Layer binaries from the
// upstream GitHub releases, verifying them against the checksums published with
// each release. Nothing here builds from source: the binary we run is the same
// artifact every other validator runs, which is the point of this image.
package release

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultRepo is the upstream repository publishing the official binaries.
const DefaultRepo = "tellor-io/layer"

// BinaryName is the executable inside the release tarball, and the name
// cosmovisor expects under each upgrade's bin/ directory.
const BinaryName = "layerd"

// Client downloads release artifacts. The zero value is not usable; use New.
type Client struct {
	repo string
	http *http.Client
	// baseURL is the release download host. Overridden in tests; empty means
	// github.com.
	baseURL string
}

// New returns a Client for repo (owner/name). An empty repo uses DefaultRepo.
func New(repo string, timeout time.Duration) *Client {
	if repo == "" {
		repo = DefaultRepo
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	return &Client{repo: repo, http: &http.Client{Timeout: timeout}}
}

// Version is an upgrade/release identifier such as "v6.1.8". Cosmovisor names its
// upgrade directories after the on-chain plan name, and Tellor's plan names match
// the release tags exactly, so the same string serves both purposes.
type Version string

// Tag returns the git tag form ("v6.1.8").
func (v Version) Tag() string {
	s := string(v)
	if strings.HasPrefix(s, "v") {
		return s
	}
	return "v" + s
}

// number returns the tag without its leading "v" ("6.1.8"). The checksums file is
// named with this form while the tag keeps the "v", so both are needed.
func (v Version) number() string {
	return strings.TrimPrefix(v.Tag(), "v")
}

// Valid reports whether v looks like a release version. It is deliberately strict:
// this string is interpolated into download URLs and used as a directory name, so
// anything unexpected is rejected rather than sanitised.
func (v Version) Valid() bool {
	s := v.number()
	if s == "" {
		return false
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 || len(parts) > 4 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// AssetName is the release tarball for a platform, e.g. "layer_Linux_x86_64.tar.gz".
// Upstream publishes Linux and Darwin builds for amd64 and arm64.
func AssetName(goos, goarch string) (string, error) {
	var osPart string
	switch goos {
	case "linux":
		osPart = "Linux"
	case "darwin":
		osPart = "Darwin"
	default:
		return "", fmt.Errorf("unsupported OS %q", goos)
	}

	var archPart string
	switch goarch {
	case "amd64":
		archPart = "x86_64"
	case "arm64":
		archPart = "arm64"
	default:
		return "", fmt.Errorf("unsupported architecture %q", goarch)
	}

	return fmt.Sprintf("layer_%s_%s.tar.gz", osPart, archPart), nil
}

// checksumsName is the checksums file published alongside the tarballs.
func checksumsName(v Version) string {
	return fmt.Sprintf("layer_%s_checksums.txt", v.number())
}

// downloadURL builds the browser download URL for a release asset.
func (c *Client) downloadURL(v Version, asset string) string {
	base := c.baseURL
	if base == "" {
		base = "https://github.com"
	}
	return fmt.Sprintf("%s/%s/releases/download/%s/%s", base, c.repo, v.Tag(), asset)
}

func (c *Client) get(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp.Body, nil
}

// ExpectedSHA256 returns the published checksum for asset in release v.
func (c *Client) ExpectedSHA256(ctx context.Context, v Version, asset string) (string, error) {
	body, err := c.get(ctx, c.downloadURL(v, checksumsName(v)))
	if err != nil {
		return "", fmt.Errorf("fetch checksums: %w", err)
	}
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read checksums: %w", err)
	}

	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if fields[1] == asset {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("no checksum published for %s in %s", asset, v.Tag())
}

// FetchBinary downloads the release tarball for v, verifies it against the
// published checksum, and returns the extracted layerd executable.
//
// The checksum is verified over the whole tarball before anything is extracted, so
// a corrupted or substituted download can never reach the filesystem as a binary
// we would then run as the validator.
func (c *Client) FetchBinary(ctx context.Context, v Version, goos, goarch string) ([]byte, error) {
	if !v.Valid() {
		return nil, fmt.Errorf("invalid version %q", string(v))
	}
	asset, err := AssetName(goos, goarch)
	if err != nil {
		return nil, err
	}

	want, err := c.ExpectedSHA256(ctx, v, asset)
	if err != nil {
		return nil, err
	}

	body, err := c.get(ctx, c.downloadURL(v, asset))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", asset, err)
	}
	defer func() { _ = body.Close() }()

	tarball, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", asset, err)
	}

	sum := sha256.Sum256(tarball)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return nil, fmt.Errorf("checksum mismatch for %s: got %s, want %s", asset, got, want)
	}

	bin, err := extractBinary(tarball)
	if err != nil {
		return nil, fmt.Errorf("extract %s from %s: %w", BinaryName, asset, err)
	}
	return bin, nil
}

// extractBinary pulls the layerd executable out of a release tarball.
func extractBinary(tarball []byte) ([]byte, error) {
	gz, err := gzip.NewReader(strings.NewReader(string(tarball)))
	if err != nil {
		return nil, err
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s not found in archive", BinaryName)
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		// Match the basename so a tarball that nests the binary still works.
		name := hdr.Name
		if i := strings.LastIndex(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		if name != BinaryName {
			continue
		}
		return io.ReadAll(tr)
	}
}
