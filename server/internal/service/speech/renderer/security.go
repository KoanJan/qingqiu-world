package renderer

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ValidateRequestSnapshot ensures adapter provenance is useful and secret-free.
// Rejecting an invalid snapshot is safer than silently changing it because the
// persisted value must match the request that actually produced the audio.
func ValidateRequestSnapshot(raw string) (string, error) {
	var value map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return "", fmt.Errorf("snapshot must be a JSON object: %w", err)
	}
	if len(value) == 0 {
		return "", fmt.Errorf("snapshot must not be empty")
	}
	if secretPath := findSecretConfigPath(value, ""); secretPath != "" {
		return "", fmt.Errorf("snapshot contains forbidden secret field %q", secretPath)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("normalize snapshot: %w", err)
	}
	return string(encoded), nil
}

func isSecretConfigKey(key string) bool {
	key = strings.ToLower(key)
	return strings.Contains(key, "key") || strings.Contains(key, "secret") ||
		strings.Contains(key, "token") || strings.Contains(key, "password") ||
		strings.Contains(key, "credential")
}

func findSecretConfigPath(value interface{}, parent string) string {
	switch item := value.(type) {
	case map[string]interface{}:
		for childKey, childValue := range item {
			path := childKey
			if parent != "" {
				path = parent + "." + childKey
			}
			if isSecretConfigKey(childKey) {
				return path
			}
			if found := findSecretConfigPath(childValue, path); found != "" {
				return found
			}
		}
	case []interface{}:
		for index, childValue := range item {
			path := fmt.Sprintf("%s[%d]", parent, index)
			if found := findSecretConfigPath(childValue, path); found != "" {
				return found
			}
		}
	}
	return ""
}
