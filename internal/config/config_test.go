package config

import (
	"strings"
	"testing"
)

func TestParseAppliesDefaults(t *testing.T) {
	cfg, err := Parse([]byte(`{"sources":{"local":{"engine":"mysql"}}}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.HTTP.Addr != "127.0.0.1:7000" {
		t.Errorf("default http addr = %q", cfg.HTTP.Addr)
	}
	if cfg.HTTP.Path != "/mcp" {
		t.Errorf("default http path = %q", cfg.HTTP.Path)
	}
	src := cfg.Sources["local"]
	if src.Port != 3306 {
		t.Errorf("default port = %d", src.Port)
	}
	if src.Host != "127.0.0.1" {
		t.Errorf("default host = %q", src.Host)
	}
	if src.Name() != "local" {
		t.Errorf("name = %q", src.Name())
	}
}

func TestParseExpandsEnv(t *testing.T) {
	t.Setenv("DB_PW", "s3cret")
	cfg, err := Parse([]byte(`{"sources":{"local":{"engine":"mysql","password":"${DB_PW}"}}}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := cfg.Sources["local"].Password; got != "s3cret" {
		t.Errorf("expanded password = %q", got)
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	_, err := Parse([]byte(`{"sources":{"local":{"engine":"mysql","bogus":true}}}`))
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestValidateRejectsBadEngine(t *testing.T) {
	_, err := Parse([]byte(`{"sources":{"local":{"engine":"oracle"}}}`))
	if err == nil {
		t.Fatal("expected error for unsupported engine")
	}
}

func TestValidateRejectsNoSources(t *testing.T) {
	_, err := Parse([]byte(`{"sources":{}}`))
	if err == nil {
		t.Fatal("expected error for empty sources")
	}
}

func TestValidateRejectsDSNWithSSH(t *testing.T) {
	in := `{"sources":{"r":{"engine":"mysql","dsn":"u:p@tcp(h:3306)/d","ssh":{"host":"b","user":"x"}}}}`
	_, err := Parse([]byte(in))
	if err == nil || !strings.Contains(err.Error(), "dsn cannot be combined with ssh") {
		t.Fatalf("expected dsn+ssh error, got %v", err)
	}
}

func TestSSHDefaults(t *testing.T) {
	in := `{"sources":{"r":{"engine":"mysql","host":"db","ssh":{"host":"bastion","user":"deploy","private_key_path":"/k"}}}}`
	cfg, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ssh := cfg.Sources["r"].SSH
	if ssh.Port != 22 {
		t.Errorf("default ssh port = %d", ssh.Port)
	}
	if ssh.KnownHostsPath == "" {
		t.Error("expected default known_hosts path")
	}
	if !cfg.Sources["r"].IsRemote() {
		t.Error("expected remote source")
	}
}
