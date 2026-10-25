package gateway

import (
	"encoding/json"
	"os"
	"sync"
)

// ConfigKeySource follows key rotations without restarting the GUI.
// Retain the last valid key during a transient read or partial bind-mount write.
func ConfigKeySource(path string) func() string {
	var mu sync.Mutex
	var last string
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		raw, err := os.ReadFile(path)
		if err != nil {
			return last
		}
		var doc map[string]json.RawMessage
		if json.Unmarshal(raw, &doc) != nil || doc == nil {
			return last
		}
		var next string
		if value, exists := doc["api_key"]; exists {
			if json.Unmarshal(value, &next) != nil {
				return last
			}
		}
		last = next
		return last
	}
}
