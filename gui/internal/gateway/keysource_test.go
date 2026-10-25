package gateway

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigKeySourceReloadsAndKeepsLastGoodValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"api_key":"first"}`)
	key := ConfigKeySource(path)
	if key() != "first" {
		t.Fatal("initial key not loaded")
	}
	write(`{"api_key":"second"}`)
	if key() != "second" {
		t.Fatal("rotated key not loaded")
	}
	write(`{`)
	if key() != "second" {
		t.Fatal("partial writes must not clear the key")
	}
	write(`{"api_key":""}`)
	if key() != "" {
		t.Fatal("explicit empty key must be respected")
	}
}
