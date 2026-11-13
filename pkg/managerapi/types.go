package managerapi

// API paths from the manager REST contract.
const (
	SignInPath            = "/api/v1/users/signin"
	SchedulerClustersPath = "/api/v1/scheduler-clusters"
	SeedPeerClustersPath  = "/api/v1/seed-peer-clusters"

	// ManagedByBio is attached to every record created by the operator.
	ManagedByBio = "managed by d7y-cattle-operator"
)

// SignInRequest is the Dragonfly v2.5.4 JWT signin body. The operator Secret
// key is username; the client maps it to the API field name.
type SignInRequest struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

// SignInResponse is the documented Dragonfly v2.5.4 signin response.
type SignInResponse struct {
	Token  string `json:"token"`
	Expire string `json:"expire"`
}

// SchedulerCluster is the subset of a manager record the operator uses.
type SchedulerCluster struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// SeedPeerCluster is the subset of a manager record the operator uses.
type SeedPeerCluster struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// SchedulerClusterConfig is the scheduler cluster config block.
type SchedulerClusterConfig struct {
	CandidateParentLimit int `json:"candidate_parent_limit"`
	FilterParentLimit    int `json:"filter_parent_limit"`
	JobRateLimit         int `json:"job_rate_limit"`
}

// BlockList is the block_list object. Dragonfly v2.5.4 expects an object
// ({} is accepted); the nested field shape is not confirmed, so callers start
// with an empty object.
type BlockList map[string]any

// ClientConfig is the scheduler cluster client_config block.
type ClientConfig struct {
	LoadLimit int       `json:"load_limit"`
	BlockList BlockList `json:"block_list"`
}

// SeedClientConfig is the scheduler cluster seed_client_config block.
type SeedClientConfig struct {
	BlockList BlockList `json:"block_list"`
}

// SchedulerClusterScopes is the optional scopes block.
type SchedulerClusterScopes struct {
	IDC       string   `json:"idc,omitempty"`
	Location  string   `json:"location,omitempty"`
	CIDRs     []string `json:"cidrs,omitempty"`
	Hostnames []string `json:"hostnames,omitempty"`
}

// SchedulerClusterRequest is the create request. The server assigns id; it is
// deliberately absent from this struct. seed_peer_cluster_id is also omitted;
// use LinkSeedPeerCluster with the explicit link endpoint instead.
type SchedulerClusterRequest struct {
	Name             string                  `json:"name"`
	Bio              string                  `json:"bio"`
	Config           SchedulerClusterConfig  `json:"config"`
	ClientConfig     ClientConfig            `json:"client_config"`
	SeedClientConfig SeedClientConfig        `json:"seed_client_config"`
	Scopes           *SchedulerClusterScopes `json:"scopes,omitempty"`
	IsDefault        bool                    `json:"is_default"`
}

// SeedPeerClusterConfig is the seed peer cluster config block.
type SeedPeerClusterConfig struct {
	LoadLimit int       `json:"load_limit"`
	BlockList BlockList `json:"block_list"`
}

// SeedPeerClusterRequest is the create request.
type SeedPeerClusterRequest struct {
	Name   string                `json:"name"`
	Bio    string                `json:"bio"`
	Config SeedPeerClusterConfig `json:"config"`
}

// SchedulerClusterName returns the contract name for a managed Cluster.
func SchedulerClusterName(namespace, name string) string {
	return namespace + "-" + name
}

// SeedPeerClusterName returns the contract name for a managed Cluster's seed
// peer record.
func SeedPeerClusterName(namespace, name string) string {
	return namespace + "-" + name + "-seed"
}

// NewSchedulerClusterRequest returns the contract create request with the
// managed-by defaults.
func NewSchedulerClusterRequest(namespace, name string) SchedulerClusterRequest {
	return SchedulerClusterRequest{
		Name: SchedulerClusterName(namespace, name),
		Bio:  ManagedByBio,
		Config: SchedulerClusterConfig{
			CandidateParentLimit: 100,
			FilterParentLimit:    100,
			JobRateLimit:         0,
		},
		ClientConfig: ClientConfig{
			LoadLimit: 500,
			BlockList: BlockList{},
		},
		SeedClientConfig: SeedClientConfig{
			BlockList: BlockList{},
		},
		IsDefault: false,
	}
}

// NewSeedPeerClusterRequest returns the contract create request with the
// managed-by defaults.
func NewSeedPeerClusterRequest(namespace, name string) SeedPeerClusterRequest {
	return SeedPeerClusterRequest{
		Name: SeedPeerClusterName(namespace, name),
		Bio:  ManagedByBio,
		Config: SeedPeerClusterConfig{
			LoadLimit: 500,
			BlockList: BlockList{},
		},
	}
}
