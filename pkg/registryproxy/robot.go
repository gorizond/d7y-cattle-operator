package registryproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// systemRobotName is the Harbor system robot used by the proxy data plane.
const systemRobotName = "d7y-registry-proxy"

// SecretStore is the small Secret surface used by the operator to persist
// Harbor robot credentials. The proxy itself only reads the mounted Secret and
// never writes Kubernetes resources.
type SecretStore interface {
	Get(ctx context.Context, name string) (map[string][]byte, bool, error)
	Put(ctx context.Context, name string, data map[string][]byte) error
}

// robotFingerprint identifies the desired robot permission set. The Secret
// stores it so a restart can detect stale grants (for example a source project
// added to the allowlist) and recreate the robot once.
func robotFingerprint(name, mirrorProject string, sourceProjects []string) string {
	projects := append([]string(nil), sourceProjects...)
	sort.Strings(projects)
	sum := sha256.Sum256([]byte(name + "\x00" + mirrorProject + "\x00" + strings.Join(projects, "\x00")))
	// The version prefix forces one reconciliation when the fingerprint
	// layout changes in a future release.
	return "v1:sha256:" + hex.EncodeToString(sum[:])
}

// findRobot returns the Harbor robot with the given name, if any.
func (h *HarborClient) findRobot(ctx context.Context, name string) (HarborRobot, bool, error) {
	var list []HarborRobot
	if _, err := h.do(ctx, http.MethodGet, "/api/v2.0/robots?q="+url.QueryEscape("name="+name), nil, &list); err != nil {
		return HarborRobot{}, false, err
	}
	for _, item := range list {
		if robotName(item.Name) == name {
			return item, true, nil
		}
	}
	return HarborRobot{}, false, nil
}

// RobotExists reports whether a robot with the given name exists.
func (h *HarborClient) RobotExists(ctx context.Context, name string) (bool, error) {
	_, ok, err := h.findRobot(ctx, name)
	return ok, err
}

// DeleteRobot removes a system robot by name. A missing robot is not an error.
func (h *HarborClient) DeleteRobot(ctx context.Context, name string) error {
	item, ok, err := h.findRobot(ctx, name)
	if err != nil || !ok {
		return err
	}
	status, err := h.do(ctx, http.MethodDelete, "/api/v2.0/robots/"+strconv.Itoa(item.ID), nil, nil)
	if err != nil && status != http.StatusNotFound {
		return err
	}
	return nil
}

// EnsureRobotSecret provisions the Harbor system robot from the operator and
// persists its credentials in the upstream Secret that Fleet copies downstream.
// It reads an existing Secret first when its permission fingerprint matches,
// otherwise it deletes the stale robot, creates a system robot through the
// Harbor admin API, and writes the new credentials and fingerprint. Secret
// values are never logged.
func EnsureRobotSecret(ctx context.Context, harbor *HarborClient, name, mirrorProject string, sourceProjects []string, store SecretStore) (HarborRobot, error) {
	fingerprint := robotFingerprint(systemRobotName, mirrorProject, sourceProjects)
	if store != nil {
		data, ok, err := store.Get(ctx, name)
		if err != nil {
			return HarborRobot{}, err
		}
		hasCredentials := ok && len(data["username"]) > 0 && len(data["password"]) > 0
		if hasCredentials && string(data["permissions-fingerprint"]) == fingerprint {
			// Harbor may have been reinstalled while the Secret survived;
			// reuse the stored credentials only when the robot still exists.
			exists, err := harbor.RobotExists(ctx, systemRobotName)
			if err != nil {
				return HarborRobot{}, err
			}
			if exists {
				return HarborRobot{Name: string(data["username"]), Secret: string(data["password"])}, nil
			}
		}
		if hasCredentials {
			// Old Secret without a fingerprint or with a different grant set:
			// Harbor only returns a robot secret on creation, so drop the
			// stale robot and recreate it with the current permissions.
			if err := harbor.DeleteRobot(ctx, systemRobotName); err != nil {
				return HarborRobot{}, fmt.Errorf("delete stale Harbor robot %q: %w", systemRobotName, err)
			}
		}
	}
	robot, err := harbor.EnsureSystemRobot(ctx, systemRobotName, sourceProjects, mirrorProject)
	if err != nil {
		return HarborRobot{}, err
	}
	if robot.Secret == "" {
		return HarborRobot{}, fmt.Errorf("harbor robot %q returned no secret; delete the robot or provide the existing Secret", robot.Name)
	}
	if store != nil {
		if err := store.Put(ctx, name, map[string][]byte{
			"username":                []byte(robot.Name),
			"password":                []byte(robot.Secret),
			"permissions-fingerprint": []byte(fingerprint),
		}); err != nil {
			return HarborRobot{}, err
		}
	}
	return robot, nil
}
