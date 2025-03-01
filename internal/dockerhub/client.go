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
	"github.com/sirupsen/logrus"
)

type Client struct {
	httpClient *http.Client
	config     *config.Config
	log        *logrus.Entry
	token      string
	tokenExp   time.Time
}

type tokenResponse struct {
	Token     string    `json:"token"`
	ExpiresIn int       `json:"expires_in"`
	IssuedAt  time.Time `json:"issued_at"`
}

type loggingTransport struct {
	log *logrus.Entry
}

func NewClient(logger *logrus.Logger, cfg *config.Config) *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &loggingTransport{log: logger.WithField("component", "dockerhub_transport")},
		},
		config: cfg,
		log:    logger.WithField("component", "dockerhub_client"),
	}
}

func (c *Client) getToken(ctx context.Context, realm string, service string, scope string) error {
	start := time.Now()
	log := c.log.WithFields(logrus.Fields{
		"operation": "token_auth",
		"realm":     realm,
		"service":   service,
		"scope":     scope,
	})

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
		log.WithError(err).Error("Token request failed")
		return fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.WithField("status_code", resp.StatusCode).Error("Token auth failed")
		return fmt.Errorf("token auth failed with status %d", resp.StatusCode)
	}

	var tokenResp tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		log.WithError(err).Error("Failed to decode token response")
		return fmt.Errorf("failed to decode token response: %w", err)
	}

	c.token = tokenResp.Token
	c.tokenExp = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
	log.WithFields(logrus.Fields{
		"duration":   time.Since(start),
		"expires_in": tokenResp.ExpiresIn,
	}).Debug("Acquired Docker Hub token")
	return nil
}

func (c *Client) DoRequestWithAuth(ctx context.Context, req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", "RegistryProxy/1.0")

	if c.token != "" && time.Now().Before(c.tokenExp) {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.log.WithError(err).Error("Request failed")
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

func (t *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	log := t.log.WithFields(logrus.Fields{
		"method": req.Method,
		"url":    req.URL.String(),
	})

	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		log.WithError(err).Error("HTTP request failed")
		return nil, err
	}

	log.WithFields(logrus.Fields{
		"status_code": resp.StatusCode,
		"duration":    time.Since(start),
	}).Debug("HTTP request completed")
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

func (c *Client) GetManifest(ctx context.Context, image, reference, acceptHeader string) (*http.Response, error) {
	url := fmt.Sprintf("https://registry-1.docker.io/v2/%s/manifests/%s", normalizeImageName(image), reference)
	req, _ := http.NewRequest("GET", url, nil)
	if acceptHeader != "" {
		req.Header.Set("Accept", acceptHeader)
	} else {
		req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json")
	}
	return c.DoRequestWithAuth(ctx, req)
}

func (c *Client) GetBlob(ctx context.Context, image, digest string) (*http.Response, error) {
	url := fmt.Sprintf("https://registry-1.docker.io/v2/%s/blobs/%s", normalizeImageName(image), digest)
	req, _ := http.NewRequest("GET", url, nil)
	return c.DoRequestWithAuth(ctx, req)
}

func normalizeImageName(image string) string {
	if !strings.Contains(image, "/") {
		return "library/" + image
	}
	return image
}

func (c *Client) GetTags(ctx context.Context, image string) (*http.Response, error) {
	url := fmt.Sprintf("https://registry-1.docker.io/v2/%s/tags/list", normalizeImageName(image))
	req, _ := http.NewRequest("GET", url, nil)
	return c.DoRequestWithAuth(ctx, req)
}
