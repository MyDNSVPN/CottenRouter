package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// LatestRelease reads upstream at operation time, independently of the router
// release or the commit used to fetch the upstream installation script.
func (r Resolver) LatestRelease(ctx context.Context, project Project) (string, error) {
	if r.Client == nil {
		r.Client = http.DefaultClient
	}
	if r.APIBase == "" {
		r.APIBase = "https://api.github.com"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(r.APIBase, "/")+"/repos/"+project.RepoFullName+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "CottenRouter")
	request.Header.Set("Cache-Control", "no-cache")
	response, err := r.Client.Do(request)
	if err != nil {
		return "", fmt.Errorf("resolve latest %s release: %w", project.Name, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("resolve latest %s release: HTTP %d", project.Name, response.StatusCode)
	}
	var release struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(response.Body).Decode(&release); err != nil {
		return "", err
	}
	if release.Draft || release.Prerelease || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,159}$`).MatchString(release.Tag) {
		return "", fmt.Errorf("latest %s release has an unsafe or unpublished tag", project.Name)
	}
	return release.Tag, nil
}
