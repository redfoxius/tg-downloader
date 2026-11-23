package config

import (
	"path/filepath"
	"testing"
)

func TestLoadDefaultsAndEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	t.Setenv("TG_API_ID", "42")
	t.Setenv("TG_API_HASH", "h")
	c, err := Load(p) // file does not exist yet
	if err != nil {
		t.Fatal(err)
	}
	if c.APIID != 42 || c.APIHash != "h" || c.Addr != "127.0.0.1:8181" || c.DataDir != "data" {
		t.Fatalf("unexpected: %+v", c)
	}
	if filepath.Dir(c.SessionPath) != filepath.Dir(p) {
		t.Fatalf("session should sit next to config: %s", c.SessionPath)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "c.yaml")
	if err := (&Config{APIID: 7, APIHash: "x"}).Save(p); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil || c.APIID != 7 || c.APIHash != "x" {
		t.Fatalf("%+v %v", c, err)
	}
}
