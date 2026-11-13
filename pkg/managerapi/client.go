package managerapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultTimeout = 30 * time.Second
	pageSize       = 50
	maxPages       = 1000
	maxBodyBytes   = 1 << 20
	errorBodyBytes = 512
)

var (
	// ErrNotFound maps to HTTP 404: the manager record is missing.
	ErrNotFound = errors.New("manager api: not found")
	// ErrConflict maps to HTTP 409: the record already exists.
	ErrConflict = errors.New("manager api: conflict")
	// ErrUnauthorized maps to HTTP 401 or an unexpected 403.
	ErrUnauthorized = errors.New("manager api: unauthorized")
	// ErrUnprocessable maps to HTTP 422: invalid body/query; do not retry blindly.
	ErrUnprocessable = errors.New("manager api: unprocessable entity")
	// ErrMissingCredentials means username or password is empty.
	ErrMissingCredentials = errors.New("manager api: username and password are required")
	// ErrInvalidBaseURL means the base URL is not an absolute HTTPS URL.
	ErrInvalidBaseURL = errors.New("manager api: base URL must be an absolute https URL")
)

// HTTPError is a non-2xx response outside the documented sentinel cases.
type HTTPError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("manager api: %s", e.Status)
	}
	return fmt.Sprintf("manager api: %s: %s", e.Status, e.Body)
}

// Options configures the manager REST client.
type Options struct {
	BaseURL    string
	Username   string
	Password   string
	HTTPClient *http.Client
	Timeout    time.Duration
}

// Client is a small JWT-authenticated manager REST client.
type Client struct {
	baseURL  string
	baseHost string
	username string
	password string

	httpClient *http.Client
	timeout    time.Duration

	authMu sync.Mutex
	token  string
}

// New validates the options and returns a client. Production callers pass the
// HTTPS manager API URL and credentials read from the manager API Secret.
func New(opts Options) (*Client, error) {
	if strings.TrimSpace(opts.Username) == "" || opts.Password == "" {
		return nil, ErrMissingCredentials
	}
	base, err := url.Parse(strings.TrimSpace(opts.BaseURL))
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return nil, ErrInvalidBaseURL
	}
	if base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("%w: query and fragment are not allowed", ErrInvalidBaseURL)
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	httpClient := &http.Client{}
	if opts.HTTPClient != nil {
		copied := *opts.HTTPClient
		httpClient = &copied
	}
	if httpClient.Timeout == 0 {
		httpClient.Timeout = timeout
	}
	if httpClient.CheckRedirect == nil {
		httpClient.CheckRedirect = noRedirect
	}

	return &Client{
		baseURL:    strings.TrimRight(base.String(), "/"),
		baseHost:   canonicalHost(base.Host),
		username:   opts.Username,
		password:   opts.Password,
		httpClient: httpClient,
		timeout:    timeout,
	}, nil
}

// SignIn obtains a fresh JWT. Other methods sign in lazily when needed.
func (c *Client) SignIn(ctx context.Context) error {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	c.authMu.Lock()
	defer c.authMu.Unlock()
	_, err := c.signInLocked(ctx)
	return err
}

// ListSchedulerClusters returns all scheduler clusters, following the Link
// header rel=next pagination. A null response is treated as an empty list.
func (c *Client) ListSchedulerClusters(ctx context.Context) ([]SchedulerCluster, error) {
	return c.listSchedulerClusters(ctx, "")
}

// ListSchedulerClustersByName returns scheduler clusters matching the exact
// name filter. The manager applies the filter server-side.
func (c *Client) ListSchedulerClustersByName(ctx context.Context, name string) ([]SchedulerCluster, error) {
	return c.listSchedulerClusters(ctx, name)
}

// ListSeedPeerClusters returns all seed peer clusters, following the Link header
// rel=next pagination. A null response is treated as an empty list.
func (c *Client) ListSeedPeerClusters(ctx context.Context) ([]SeedPeerCluster, error) {
	return c.listSeedPeerClusters(ctx, "")
}

// ListSeedPeerClustersByName returns seed peer clusters matching the exact name
// filter.
func (c *Client) ListSeedPeerClustersByName(ctx context.Context, name string) ([]SeedPeerCluster, error) {
	return c.listSeedPeerClusters(ctx, name)
}

// GetSchedulerCluster fetches one record by server-assigned ID.
func (c *Client) GetSchedulerCluster(ctx context.Context, id int64) (*SchedulerCluster, error) {
	var out SchedulerCluster
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/%d", SchedulerClustersPath, id), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetSeedPeerCluster fetches one seed peer record by server-assigned ID.
func (c *Client) GetSeedPeerCluster(ctx context.Context, id int64) (*SeedPeerCluster, error) {
	var out SeedPeerCluster
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s/%d", SeedPeerClustersPath, id), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateSchedulerCluster creates a record. The server assigns the ID.
// seed_peer_cluster_id is deliberately not part of this request; use
// LinkSeedPeerCluster after both records exist.
func (c *Client) CreateSchedulerCluster(ctx context.Context, req SchedulerClusterRequest) (*SchedulerCluster, error) {
	if req.ClientConfig.BlockList == nil {
		req.ClientConfig.BlockList = BlockList{}
	}
	if req.SeedClientConfig.BlockList == nil {
		req.SeedClientConfig.BlockList = BlockList{}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("manager api: encode scheduler cluster: %w", err)
	}
	var out SchedulerCluster
	if err := c.do(ctx, http.MethodPost, SchedulerClustersPath, nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateSeedPeerCluster creates a seed peer record. The server assigns the ID;
// scheduler_cluster_id is not part of the create body.
func (c *Client) CreateSeedPeerCluster(ctx context.Context, req SeedPeerClusterRequest) (*SeedPeerCluster, error) {
	if req.Config.BlockList == nil {
		req.Config.BlockList = BlockList{}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("manager api: encode seed peer cluster: %w", err)
	}
	var out SeedPeerCluster
	if err := c.do(ctx, http.MethodPost, SeedPeerClustersPath, nil, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// LinkSeedPeerCluster links a seed peer cluster to a scheduler cluster using the
// explicit link endpoint. It returns nil on the documented 200 empty response.
// The endpoint clears all existing scheduler clusters of the target scheduler
// cluster and appends this seed peer cluster, so it is idempotent for the
// operator and re-applied on every reconcile/self-heal.
func (c *Client) LinkSeedPeerCluster(ctx context.Context, seedPeerID, schedulerID int64) error {
	if seedPeerID < 1 || schedulerID < 1 {
		return fmt.Errorf("manager api: link seed peer %d to scheduler %d: invalid ID", seedPeerID, schedulerID)
	}
	path := fmt.Sprintf("%s/%d/scheduler-clusters/%d", SeedPeerClustersPath, seedPeerID, schedulerID)
	return c.do(ctx, http.MethodPut, path, nil, nil, nil)
}

// EnsureSchedulerCluster returns the existing record by exact name or creates
// it. A 409 is treated as an existing record and resolved by re-reading.
func (c *Client) EnsureSchedulerCluster(ctx context.Context, req SchedulerClusterRequest) (*SchedulerCluster, error) {
	existing, err := c.findSchedulerCluster(ctx, req.Name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	created, err := c.CreateSchedulerCluster(ctx, req)
	if err == nil {
		return created, nil
	}
	if !errors.Is(err, ErrConflict) {
		return nil, err
	}

	existing, err = c.findSchedulerCluster(ctx, req.Name)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("manager api: scheduler cluster %q: %w", req.Name, ErrConflict)
	}
	return existing, nil
}

// EnsureSeedPeerCluster returns the existing record by exact name or creates
// it. A 409 is treated as an existing record and resolved by re-reading.
func (c *Client) EnsureSeedPeerCluster(ctx context.Context, req SeedPeerClusterRequest) (*SeedPeerCluster, error) {
	existing, err := c.findSeedPeerCluster(ctx, req.Name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	created, err := c.CreateSeedPeerCluster(ctx, req)
	if err == nil {
		return created, nil
	}
	if !errors.Is(err, ErrConflict) {
		return nil, err
	}

	existing, err = c.findSeedPeerCluster(ctx, req.Name)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("manager api: seed peer cluster %q: %w", req.Name, ErrConflict)
	}
	return existing, nil
}

func (c *Client) listSchedulerClusters(ctx context.Context, name string) ([]SchedulerCluster, error) {
	query := url.Values{
		"page":     {"1"},
		"per_page": {fmt.Sprintf("%d", pageSize)},
	}
	if name != "" {
		query.Set("name", name)
	}
	endpoint := c.endpoint(SchedulerClustersPath, query)

	var all []SchedulerCluster
	seen := map[string]bool{endpoint: true}
	for page := 1; page <= maxPages; page++ {
		var batch []SchedulerCluster
		headers, err := c.doURL(ctx, http.MethodGet, endpoint, nil, &batch)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)

		next, ok, err := c.nextLink(headers, endpoint)
		if err != nil {
			return nil, err
		}
		if !ok {
			return all, nil
		}
		if seen[next] {
			return nil, fmt.Errorf("manager api: scheduler cluster pagination loop at %s", next)
		}
		seen[next] = true
		endpoint = next
	}
	return nil, fmt.Errorf("manager api: scheduler cluster list exceeded %d pages", maxPages)
}

func (c *Client) listSeedPeerClusters(ctx context.Context, name string) ([]SeedPeerCluster, error) {
	query := url.Values{
		"page":     {"1"},
		"per_page": {fmt.Sprintf("%d", pageSize)},
	}
	if name != "" {
		query.Set("name", name)
	}
	endpoint := c.endpoint(SeedPeerClustersPath, query)

	var all []SeedPeerCluster
	seen := map[string]bool{endpoint: true}
	for page := 1; page <= maxPages; page++ {
		var batch []SeedPeerCluster
		headers, err := c.doURL(ctx, http.MethodGet, endpoint, nil, &batch)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)

		next, ok, err := c.nextLink(headers, endpoint)
		if err != nil {
			return nil, err
		}
		if !ok {
			return all, nil
		}
		if seen[next] {
			return nil, fmt.Errorf("manager api: seed peer cluster pagination loop at %s", next)
		}
		seen[next] = true
		endpoint = next
	}
	return nil, fmt.Errorf("manager api: seed peer cluster list exceeded %d pages", maxPages)
}

func (c *Client) findSchedulerCluster(ctx context.Context, name string) (*SchedulerCluster, error) {
	clusters, err := c.ListSchedulerClustersByName(ctx, name)
	if err != nil {
		return nil, err
	}
	for i := range clusters {
		if clusters[i].Name == name {
			return &clusters[i], nil
		}
	}
	return nil, nil
}

func (c *Client) findSeedPeerCluster(ctx context.Context, name string) (*SeedPeerCluster, error) {
	clusters, err := c.ListSeedPeerClustersByName(ctx, name)
	if err != nil {
		return nil, err
	}
	for i := range clusters {
		if clusters[i].Name == name {
			return &clusters[i], nil
		}
	}
	return nil, nil
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body []byte, out any) error {
	_, err := c.doURL(ctx, method, c.endpoint(path, query), body, out)
	return err
}

func (c *Client) doURL(ctx context.Context, method, endpoint string, body []byte, out any) (http.Header, error) {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	if !c.sameOrigin(endpoint) {
		return nil, fmt.Errorf("%w: refusing cross-origin request %s", ErrInvalidBaseURL, endpoint)
	}

	token, err := c.tokenOrSignIn(ctx)
	if err != nil {
		return nil, err
	}

	headers, err := c.doWithTokenURL(ctx, token, method, endpoint, body, out)
	if !errors.Is(err, ErrUnauthorized) {
		return headers, err
	}

	token, err = c.reauthenticate(ctx, token)
	if err != nil {
		return nil, err
	}
	return c.doWithTokenURL(ctx, token, method, endpoint, body, out)
}

func (c *Client) doWithTokenURL(ctx context.Context, token, method, endpoint string, body []byte, out any) (http.Header, error) {
	req, err := c.newRequest(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("manager api: %s %s: %w", method, endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyBytes))
		return nil, statusError(resp.StatusCode, resp.Status, snippet)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes))
		return resp.Header, nil
	}

	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes))
	if err := decoder.Decode(out); err != nil {
		return nil, fmt.Errorf("manager api: decode %s %s response: %w", method, endpoint, err)
	}
	return resp.Header, nil
}

func (c *Client) tokenOrSignIn(ctx context.Context) (string, error) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if c.token != "" {
		return c.token, nil
	}
	return c.signInLocked(ctx)
}

func (c *Client) reauthenticate(ctx context.Context, oldToken string) (string, error) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if c.token != "" && c.token != oldToken {
		return c.token, nil
	}
	c.token = ""
	return c.signInLocked(ctx)
}

func (c *Client) signInLocked(ctx context.Context) (string, error) {
	body, err := json.Marshal(SignInRequest{Name: c.username, Password: c.password})
	if err != nil {
		return "", fmt.Errorf("manager api: encode signin: %w", err)
	}
	req, err := c.newRequest(ctx, http.MethodPost, c.endpoint(SignInPath, nil), body)
	if err != nil {
		return "", err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("manager api: signin: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyBytes))
		return "", statusError(resp.StatusCode, resp.Status, snippet)
	}

	var out SignInResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&out); err != nil {
		return "", fmt.Errorf("manager api: decode signin response: %w", err)
	}
	if out.Token == "" {
		return "", fmt.Errorf("manager api: signin response has no token")
	}
	c.token = out.Token
	return out.Token, nil
}

func (c *Client) endpoint(path string, query url.Values) string {
	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	return endpoint
}

func (c *Client) newRequest(ctx context.Context, method, endpoint string, body []byte) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, fmt.Errorf("manager api: build %s %s request: %w", method, endpoint, err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (c *Client) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.timeout)
}

func (c *Client) sameOrigin(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	return err == nil && parsed.Scheme == "https" &&
		strings.EqualFold(canonicalHost(parsed.Host), c.baseHost)
}

// canonicalHost drops the default HTTPS port so a Link header that omits :443
// still matches a base URL that includes it.
func canonicalHost(host string) string {
	return strings.TrimSuffix(host, ":443")
}

func (c *Client) nextLink(headers http.Header, current string) (string, bool, error) {
	for _, header := range headers.Values("Link") {
		for _, part := range strings.Split(header, ",") {
			segments := strings.Split(part, ";")
			target := strings.TrimSpace(segments[0])
			if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
				continue
			}
			target = strings.TrimSuffix(strings.TrimPrefix(target, "<"), ">")
			for _, param := range segments[1:] {
				key, value, ok := strings.Cut(strings.TrimSpace(param), "=")
				if !ok || !strings.EqualFold(strings.TrimSpace(key), "rel") {
					continue
				}
				if strings.Trim(strings.TrimSpace(value), `"`) != "next" {
					continue
				}
				resolved, err := resolveReference(current, target)
				if err != nil {
					return "", false, fmt.Errorf("manager api: invalid next link %q: %w", target, err)
				}
				if !c.sameOrigin(resolved) {
					return "", false, fmt.Errorf("manager api: refusing cross-origin next link %s", resolved)
				}
				return resolved, true, nil
			}
		}
	}
	return "", false, nil
}

func resolveReference(current, target string) (string, error) {
	base, err := url.Parse(current)
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(target)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(ref).String(), nil
}

func statusError(code int, status string, body []byte) error {
	detail := strings.TrimSpace(string(body))
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: %s", ErrUnauthorized, detail)
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrNotFound, detail)
	case http.StatusConflict:
		return fmt.Errorf("%w: %s", ErrConflict, detail)
	case http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %s", ErrUnprocessable, detail)
	default:
		return &HTTPError{StatusCode: code, Status: status, Body: detail}
	}
}

func noRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}
