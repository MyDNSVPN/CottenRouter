package catalog

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestLatestReleaseRefreshesWhiteDNSOnEveryOperation(t *testing.T) {
	index := 0
	tags := []string{"v2026.10.05.103601-f4a2277", "v2026.10.08.163633-1f3cc52"}
	r := Resolver{APIBase: "https://unit.test", Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/repos/WhiteDNS/CottenDNS/releases/latest" || request.Header.Get("Cache-Control") != "no-cache" {
			t.Fatalf("stale or wrong release source: %s", request.URL)
		}
		body := fmt.Sprintf(`{"tag_name":%q,"draft":false,"prerelease":false}`, tags[index])
		index++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
	project := Projects()[0]
	for _, expected := range tags {
		got, err := r.LatestRelease(context.Background(), project)
		if err != nil || got != expected {
			t.Fatalf("got %q, %v; expected %q", got, err, expected)
		}
	}
}

func TestLatestReleaseFailsInsteadOfUsingAnOldVersion(t *testing.T) {
	for _, response := range []struct {
		status int
		body   string
	}{{403, `{}`}, {404, `{}`}, {200, `{"tag_name":"../unsafe"}`}, {200, `{"tag_name":"v2026.10.08","prerelease":true}`}, {200, `{"tag_name":"v2026.10.08","draft":true}`}, {200, `{}`}} {
		r := Resolver{APIBase: "https://unit.test", Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: response.status, Body: io.NopCloser(strings.NewReader(response.body))}, nil
		})}}
		if tag, err := r.LatestRelease(context.Background(), Projects()[0]); err == nil {
			t.Fatalf("unexpected fallback accepted %q", tag)
		}
	}
}
