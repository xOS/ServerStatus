package model

import (
	"os"
	"path/filepath"
	"testing"
)

const baseConfigYAML = `
oauth2:
  type: github
  admin: admin
  clientid: client-id
  clientsecret: client-secret
`

func writeConfigFile(t *testing.T, body string) string {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(filename, []byte(body), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return filename
}

func withoutEnv(t *testing.T, key string) {
	t.Helper()
	old, ok := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if ok {
			_ = os.Setenv(key, old)
			return
		}
		_ = os.Unsetenv(key)
	})
}

func TestConfigFrontendDistDefault(t *testing.T) {
	withoutEnv(t, "NG_FRONTEND_DIST")

	var c Config
	if err := c.Read(writeConfigFile(t, baseConfigYAML)); err != nil {
		t.Fatalf("read config: %v", err)
	}
	if c.Frontend.Dist != DefaultFrontendDist {
		t.Fatalf("frontend dist = %q, want %q", c.Frontend.Dist, DefaultFrontendDist)
	}
}

func TestConfigFrontendDistExplicitEmpty(t *testing.T) {
	withoutEnv(t, "NG_FRONTEND_DIST")

	var c Config
	if err := c.Read(writeConfigFile(t, baseConfigYAML+`
frontend:
  dist: ""
`)); err != nil {
		t.Fatalf("read config: %v", err)
	}
	if c.Frontend.Dist != "" {
		t.Fatalf("frontend dist = %q, want empty", c.Frontend.Dist)
	}
}

func TestConfigFrontendDistEnvOverridesFile(t *testing.T) {
	t.Setenv("NG_FRONTEND_DIST", "/opt/serverstatus/frontend-dist")

	var c Config
	if err := c.Read(writeConfigFile(t, baseConfigYAML+`
frontend:
  dist: frontend/dist
`)); err != nil {
		t.Fatalf("read config: %v", err)
	}
	if c.Frontend.Dist != "/opt/serverstatus/frontend-dist" {
		t.Fatalf("frontend dist = %q, want env override", c.Frontend.Dist)
	}
}
