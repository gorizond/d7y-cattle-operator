package registryproxy

import (
	"context"
	"fmt"
)

// EnsureRobotSecret returns robot credentials for the data plane. It reads an
// existing Secret first, otherwise creates a system robot through the Harbor
// admin API and persists the credentials in the configured Secret.
func EnsureRobotSecret(ctx context.Context, harbor *HarborClient, name, mirrorProject string, sourceProjects []string, store SecretStore) (HarborRobot, error) {
	if store != nil {
		data, ok, err := store.Get(ctx, name)
		if err != nil {
			return HarborRobot{}, err
		}
		if ok && len(data["username"]) > 0 && len(data["password"]) > 0 {
			return HarborRobot{Name: string(data["username"]), Secret: string(data["password"])}, nil
		}
	}
	robot, err := harbor.EnsureSystemRobot(ctx, "d7y-registry-proxy", sourceProjects, mirrorProject)
	if err != nil {
		return HarborRobot{}, err
	}
	if robot.Secret == "" {
		return HarborRobot{}, fmt.Errorf("harbor robot %q returned no secret; delete the robot or provide the existing Secret", robot.Name)
	}
	if store != nil {
		if err := store.Put(ctx, name, map[string][]byte{
			"username": []byte(robot.Name),
			"password": []byte(robot.Secret),
		}); err != nil {
			return HarborRobot{}, err
		}
	}
	return robot, nil
}
