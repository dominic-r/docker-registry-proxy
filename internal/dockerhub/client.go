package dockerhub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sdko-org/registry-proxy/internal/config"
)

type Client struct {
	httpClient *http.Client
	config     *config.Config
	token      string
}

type tokenResponse struct {
	Token string `json:"token"`
}

func NewClient(cfg *config.Config) *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		config: cfg,
	}
}

func (c *Client) getToken(ctx context.Context, realm string, service string, scope string) error {
	params := url.Values{}
	params.Add("service", service)
	if scope != "" {
		params.Add("scope", scope)
	}

	tokenURL := fmt.Sprintf("%s?%s", realm, params.Encode())
	req, _ := http.NewRequest("GET", tokenURL, nil)

	if c.config.DockerHubUser != "" && c.config.DockerHubPassword != "" {
		req.SetBasicAuth(c.config.DockerHubUser, c.config.DockerHubPassword)
	}

	resp, err := c.httpClient.Do(req.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("token auth failed with status %d", resp.StatusCode)
	}

	var tokenResp tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return fmt.Errorf("failed to decode token response: %w", err)
	}

	c.token = tokenResp.Token
	return nil
}

func (c *Client) doRequestWithAuth(ctx context.Context, req *http.Request) (*http.Response, error) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized {
		authHeader := resp.Header.Get("WWW-Authenticate")
		if authHeader == "" {
			return resp, nil
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			return resp, nil
		}

		params := parseAuthParams(parts[1])
		if err := c.getToken(ctx, params["realm"], params["service"], params["scope"]); err != nil {
			return nil, fmt.Errorf("failed to get token: %w", err)
		}

		newReq := req.Clone(req.Context())
		newReq.Header.Set("Authorization", "Bearer "+c.token)
		return c.httpClient.Do(newReq)
	}

	return resp, nil
}

func parseAuthParams(header string) map[string]string {
	params := make(map[string]string)
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 {
			params[kv[0]] = strings.Trim(kv[1], `"`)
		}
	}
	return params
}

func (c *Client) GetManifest(ctx context.Context, image, reference string) (*http.Response, error) {
	url := fmt.Sprintf("https://registry-1.docker.io/v2/%s/manifests/%s", normalizeImageName(image), reference)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Add("Accept", "application/vnd.docker.distribution.manifest.v2+json")
	return c.doRequestWithAuth(ctx, req)
}

func (c *Client) GetBlob(ctx context.Context, image, digest string) (*http.Response, error) {
	url := fmt.Sprintf("https://registry-1.docker.io/v2/%s/blobs/%s", normalizeImageName(image), digest)
	req, _ := http.NewRequest("GET", url, nil)
	return c.doRequestWithAuth(ctx, req)
}

func normalizeImageName(image string) string {
	if !strings.Contains(image, "/") {
		return "library/" + image
	}
	return image
}
