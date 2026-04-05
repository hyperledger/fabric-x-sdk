/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package network

import (
	"strings"
	"testing"
)

func TestTLSConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  TLSConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "empty mode (none)",
			config:  TLSConfig{},
			wantErr: false,
		},
		{
			name:    "explicit none mode",
			config:  TLSConfig{Mode: TLSModeNone},
			wantErr: false,
		},
		{
			name: "none mode with paths set",
			config: TLSConfig{
				Mode:        TLSModeNone,
				CACertPaths: []string{"/path/to/ca.crt"},
			},
			wantErr: true,
			errMsg:  "should not be set",
		},
		{
			name: "none mode with cert/key set",
			config: TLSConfig{
				Mode:     TLSModeNone,
				CertPath: "/path/to/cert.crt",
				KeyPath:  "/path/to/key.key",
			},
			wantErr: true,
			errMsg:  "should not be set",
		},
		{
			name: "tls mode with CA",
			config: TLSConfig{
				Mode:        TLSModeTLS,
				CACertPaths: []string{"/path/to/ca.crt"},
			},
			wantErr: false,
		},
		{
			name: "tls mode with multiple CAs",
			config: TLSConfig{
				Mode:        TLSModeTLS,
				CACertPaths: []string{"/path/to/ca1.crt", "/path/to/ca2.crt"},
			},
			wantErr: false,
		},
		{
			name: "tls mode without CA",
			config: TLSConfig{
				Mode: TLSModeTLS,
			},
			wantErr: true,
			errMsg:  "ca-cert-paths required",
		},
		{
			name: "tls mode with client cert (should be mtls)",
			config: TLSConfig{
				Mode:        TLSModeTLS,
				CertPath:    "/path/to/cert.crt",
				KeyPath:     "/path/to/key.key",
				CACertPaths: []string{"/path/to/ca.crt"},
			},
			wantErr: true,
			errMsg:  "should not be set",
		},
		{
			name: "mtls mode complete",
			config: TLSConfig{
				Mode:        TLSModeMTLS,
				CertPath:    "/path/to/cert.crt",
				KeyPath:     "/path/to/key.key",
				CACertPaths: []string{"/path/to/ca.crt"},
			},
			wantErr: false,
		},
		{
			name: "mtls mode with multiple CAs",
			config: TLSConfig{
				Mode:        TLSModeMTLS,
				CertPath:    "/path/to/cert.crt",
				KeyPath:     "/path/to/key.key",
				CACertPaths: []string{"/path/to/ca1.crt", "/path/to/ca2.crt"},
			},
			wantErr: false,
		},
		{
			name: "mtls mode missing cert",
			config: TLSConfig{
				Mode:        TLSModeMTLS,
				KeyPath:     "/path/to/key.key",
				CACertPaths: []string{"/path/to/ca.crt"},
			},
			wantErr: true,
			errMsg:  "cert-path required",
		},
		{
			name: "mtls mode missing key",
			config: TLSConfig{
				Mode:        TLSModeMTLS,
				CertPath:    "/path/to/cert.crt",
				CACertPaths: []string{"/path/to/ca.crt"},
			},
			wantErr: true,
			errMsg:  "key-path required",
		},
		{
			name: "mtls mode missing CA",
			config: TLSConfig{
				Mode:     TLSModeMTLS,
				CertPath: "/path/to/cert.crt",
				KeyPath:  "/path/to/key.key",
			},
			wantErr: true,
			errMsg:  "ca-cert-paths required",
		},
		{
			name:    "invalid mode",
			config:  TLSConfig{Mode: "invalid"},
			wantErr: true,
			errMsg:  "invalid tls mode",
		},
		{
			name:    "typo in mode",
			config:  TLSConfig{Mode: "mtsl"},
			wantErr: true,
			errMsg:  "invalid tls mode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.errMsg)
				}
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Fatalf("expected error containing %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
			}
		})
	}
}

func TestTLSConfigLoadClientTLSConfig(t *testing.T) {
	t.Run("none mode returns nil", func(t *testing.T) {
		cfg := TLSConfig{Mode: TLSModeNone}
		tlsCfg, err := cfg.LoadClientTLSConfig("example.com")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if tlsCfg != nil {
			t.Fatalf("expected nil TLS config, got %v", tlsCfg)
		}
	})

	t.Run("empty mode returns nil", func(t *testing.T) {
		cfg := TLSConfig{}
		tlsCfg, err := cfg.LoadClientTLSConfig("example.com")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if tlsCfg != nil {
			t.Fatalf("expected nil TLS config, got %v", tlsCfg)
		}
	})

	t.Run("invalid config returns error", func(t *testing.T) {
		cfg := TLSConfig{Mode: TLSModeTLS} // missing CA paths
		_, err := cfg.LoadClientTLSConfig("example.com")
		if err == nil {
			t.Fatal("expected error containing \"ca-cert-paths required\", got nil")
		}
		if !strings.Contains(err.Error(), "ca-cert-paths required") {
			t.Fatalf("expected error containing \"ca-cert-paths required\", got %q", err.Error())
		}
	})

	t.Run("invalid mode returns error", func(t *testing.T) {
		cfg := TLSConfig{Mode: "invalid"}
		_, err := cfg.LoadClientTLSConfig("example.com")
		if err == nil {
			t.Fatal("expected error containing \"invalid tls mode\", got nil")
		}
		if !strings.Contains(err.Error(), "invalid tls mode") {
			t.Fatalf("expected error containing \"invalid tls mode\", got %q", err.Error())
		}
	})

	t.Run("file not found returns error", func(t *testing.T) {
		cfg := TLSConfig{
			Mode:        TLSModeTLS,
			CACertPaths: []string{"/nonexistent/ca.crt"},
		}
		_, err := cfg.LoadClientTLSConfig("example.com")
		if err == nil {
			t.Fatal("expected error containing \"failed to read CA cert\", got nil")
		}
		if !strings.Contains(err.Error(), "failed to read CA cert") {
			t.Fatalf("expected error containing \"failed to read CA cert\", got %q", err.Error())
		}
	})

	t.Run("tls mode loads successfully with real files", func(t *testing.T) {
		cfg := TLSConfig{
			Mode:        TLSModeTLS,
			CACertPaths: []string{"fixtures/ca.crt"},
		}
		tlsCfg, err := cfg.LoadClientTLSConfig("example.com")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if tlsCfg == nil {
			t.Fatal("expected non-nil TLS config")
		}
		if tlsCfg.ServerName != "example.com" {
			t.Fatalf("expected ServerName to be \"example.com\", got %q", tlsCfg.ServerName)
		}
		if tlsCfg.RootCAs == nil {
			t.Fatal("expected RootCAs to be set")
		}
	})

	t.Run("tls mode with multiple CAs loads successfully", func(t *testing.T) {
		cfg := TLSConfig{
			Mode:        TLSModeTLS,
			CACertPaths: []string{"fixtures/ca.crt", "fixtures/ca.crt"}, // same CA twice for testing
		}
		tlsCfg, err := cfg.LoadClientTLSConfig("example.com")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if tlsCfg == nil {
			t.Fatal("expected non-nil TLS config")
		}
		if tlsCfg.RootCAs == nil {
			t.Fatal("expected RootCAs to be set")
		}
	})

	t.Run("mtls mode loads successfully with real files", func(t *testing.T) {
		cfg := TLSConfig{
			Mode:        TLSModeMTLS,
			CertPath:    "fixtures/client.crt",
			KeyPath:     "fixtures/client.key",
			CACertPaths: []string{"fixtures/ca.crt"},
		}
		tlsCfg, err := cfg.LoadClientTLSConfig("example.com")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if tlsCfg == nil {
			t.Fatal("expected non-nil TLS config")
		}
		if tlsCfg.ServerName != "example.com" {
			t.Fatalf("expected ServerName to be \"example.com\", got %q", tlsCfg.ServerName)
		}
		if tlsCfg.RootCAs == nil {
			t.Fatal("expected RootCAs to be set")
		}
		if len(tlsCfg.Certificates) != 1 {
			t.Fatalf("expected 1 client certificate, got %d", len(tlsCfg.Certificates))
		}
	})

	t.Run("mtls mode with invalid cert returns error", func(t *testing.T) {
		cfg := TLSConfig{
			Mode:        TLSModeMTLS,
			CertPath:    "fixtures/ca.crt", // wrong file - CA cert instead of client cert
			KeyPath:     "fixtures/client.key",
			CACertPaths: []string{"fixtures/ca.crt"},
		}
		_, err := cfg.LoadClientTLSConfig("example.com")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "failed to load client certificate and key") {
			t.Fatalf("expected error containing \"failed to load client certificate and key\", got %q", err.Error())
		}
	})

	t.Run("mtls mode with missing key returns error", func(t *testing.T) {
		cfg := TLSConfig{
			Mode:        TLSModeMTLS,
			CertPath:    "fixtures/client.crt",
			KeyPath:     "/nonexistent/key.key",
			CACertPaths: []string{"fixtures/ca.crt"},
		}
		_, err := cfg.LoadClientTLSConfig("example.com")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "failed to read key") {
			t.Fatalf("expected error containing \"failed to read key\", got %q", err.Error())
		}
	})
}
