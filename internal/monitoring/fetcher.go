package monitoring

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/glueckkanja/terraform-provider-gkvm/internal/github"
	"gopkg.in/yaml.v3"
)

// ValidatePath checks that a profile directory path is safe before making requests.
func ValidatePath(path string) error {
	if path != "" && (strings.Contains(path, "..") || strings.HasPrefix(path, "/")) {
		return fmt.Errorf("invalid profile_path %q: must not contain path traversal (..) or start with /", path)
	}
	return nil
}

// FetchProfiles lists YAML files in the given directory and returns parsed profiles as JSON strings.
// If path is empty, it defaults to "defaults".
func FetchProfiles(client *github.Client, path string) (map[string]string, error) {
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
		if entry.Type != "file" || !strings.HasSuffix(entry.Name, ".yaml") {
			continue
		}

		name := strings.TrimSuffix(entry.Name, ".yaml")

		if entry.DownloadURL == "" {
			return nil, fmt.Errorf("profile %s has no download_url — file may be too large or binary", name)
		}

		content, err := client.FetchFile(entry.DownloadURL)
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
		return nil, fmt.Errorf("no YAML profiles found in %s/%s (ref: %s)", client.Repo, path, client.Ref)
	}

	return result, nil
}
