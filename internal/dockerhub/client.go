package dockerhub

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sdko-org/registry-proxy/internal/config"
)

type Client struct {
	httpClient *http.Client
	config     *config.Config
}

func NewClient(cfg *config.Config) *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		config: cfg,
	}
}

func (c *Client) GetManifest(ctx context.Context, image, reference string) (*http.Response, error) {
	url := fmt.Sprintf("https://registry-1.docker.io/v2/%s/manifests/%s", normalizeImageName(image), reference)
	req, _ := http.NewRequest("GET", url, nil)

	if c.config.DockerHubUser != "" && c.config.DockerHubPassword != "" {
		req.SetBasicAuth(c.config.DockerHubUser, c.config.DockerHubPassword)
	}

	return c.httpClient.Do(req.WithContext(ctx))
}

func (c *Client) GetBlob(ctx context.Context, image, digest string) (*http.Response, error) {
	url := fmt.Sprintf("https://registry-1.docker.io/v2/%s/blobs/%s", normalizeImageName(image), digest)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	if c.config.DockerHubUser != "" && c.config.DockerHubPassword != "" {
		req.SetBasicAuth(c.config.DockerHubUser, c.config.DockerHubPassword)
	}

	return c.httpClient.Do(req.WithContext(ctx))
}

func normalizeImageName(image string) string {
	if strings.HasPrefix(image, "library/") {
		return strings.TrimPrefix(image, "library/")
	}
	return image
}
