package monitoring

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/glueckkanja/terraform-provider-gkvm/internal/source"
	"gopkg.in/yaml.v3"
)

// ValidatePath checks that a profile directory path is safe before making requests.
func ValidatePath(path string) error {
	if err := source.ValidatePath(path); err != nil {
		return fmt.Errorf("invalid profile_path %q: must not contain path traversal (..) or start with /", path)
	}
	return nil
}

// FetchProfiles lists YAML files in the given directory and returns parsed profiles as JSON strings.
// If path is empty, it defaults to "defaults".
func FetchProfiles(client source.Client, path string) (map[string]string, error) {
	if err := ValidatePath(path); err != nil {
		return nil, err
	}

	if path == "" {
		path = "defaults"
	}

	entries, err := client.ListDirectory(path)
	if err != nil {
		return nil, fmt.Errorf("listing profiles directory: %w", err)
	}

	result := make(map[string]string)

	for _, entry := range entries {
		if entry.IsDir || !strings.HasSuffix(entry.Name, ".yaml") {
			continue
		}

		name := strings.TrimSuffix(entry.Name, ".yaml")

		filePath := entry.Path
		if filePath == "" {
			filePath = path + "/" + entry.Name
		}

		content, err := client.FetchFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("fetching profile %s: %w", name, err)
		}

		var profile Profile
		if err := yaml.Unmarshal(content, &profile); err != nil {
			return nil, fmt.Errorf("parsing profile %s: %w", name, err)
		}

		if profile.MetricAlerts == nil {
			profile.MetricAlerts = make(map[string]interface{})
		}
		if profile.LogAlerts == nil {
			profile.LogAlerts = make(map[string]interface{})
		}

		jsonBytes, err := json.Marshal(profile)
		if err != nil {
			return nil, fmt.Errorf("serializing profile %s: %w", name, err)
		}

		result[name] = string(jsonBytes)
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("no YAML profiles found in %s/%s (ref: %s) at %s", client.Subject(), path, client.Reference(), client.Endpoint())
	}

	return result, nil
}
