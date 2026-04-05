/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package network

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

// TLSConfig holds TLS configuration for client or server connections.
type TLSConfig struct {
	// Mode: "none" (insecure), "tls" (one-way), "mtls" (mutual)
	Mode string

	// CertPath: Path to certificate file (public key)
	CertPath string

	// KeyPath: Path to private key file
	KeyPath string

	// CACertPaths: Paths to CA certificate files to verify the remote server.
	CACertPaths []string
}

const (
	TLSModeNone = "none"
	TLSModeTLS  = "tls"
	TLSModeMTLS = "mtls"

	DefaultTLSMinVersion = tls.VersionTLS13
)

// Validate checks that the TLS configuration is internally consistent.
func (c TLSConfig) Validate() error {
	switch c.Mode {
	case "", TLSModeNone:
		// No TLS - no other fields should be set
		if c.CertPath != "" || c.KeyPath != "" || len(c.CACertPaths) > 0 {
			return errors.New("tls mode 'none': cert/key/ca paths should not be set")
		}
		return nil

	case TLSModeTLS:
		// One-way TLS - only CA certs required
		if len(c.CACertPaths) == 0 {
			return errors.New("tls mode 'tls': ca-cert-paths required")
		}
		if c.CertPath != "" || c.KeyPath != "" {
			return errors.New("tls mode 'tls': cert-path and key-path should not be set (use 'mtls' for mutual TLS)")
		}
		return nil

	case TLSModeMTLS:
		// Mutual TLS - all fields required
		if c.CertPath == "" {
			return errors.New("tls mode 'mtls': cert-path required")
		}
		if c.KeyPath == "" {
			return errors.New("tls mode 'mtls': key-path required")
		}
		if len(c.CACertPaths) == 0 {
			return errors.New("tls mode 'mtls': ca-cert-paths required")
		}
		return nil

	default:
		return fmt.Errorf("invalid tls mode '%s': must be 'none', 'tls', or 'mtls'", c.Mode)
	}
}

// LoadClientTLSConfig creates a tls.Config for client connections.
// Returns nil for mode "none" (insecure connection).
func (c TLSConfig) LoadClientTLSConfig(serverName string) (*tls.Config, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}

	switch c.Mode {
	case "", TLSModeNone:
		return nil, nil

	case TLSModeTLS, TLSModeMTLS:
		tlsCfg := &tls.Config{
			MinVersion: DefaultTLSMinVersion,
			ServerName: serverName,
		}

		// Load CA certificates (required for both tls and mtls)
		rootCAs := x509.NewCertPool()
		for _, caPath := range c.CACertPaths {
			caPEM, err := os.ReadFile(caPath)
			if err != nil {
				return nil, fmt.Errorf("failed to read CA cert %s: %w", caPath, err)
			}
			if ok := rootCAs.AppendCertsFromPEM(caPEM); !ok {
				return nil, fmt.Errorf("failed to parse CA cert %s", caPath)
			}
		}
		tlsCfg.RootCAs = rootCAs

		// Load client certificate (only for mtls)
		if c.Mode == TLSModeMTLS {
			certPEM, err := os.ReadFile(c.CertPath)
			if err != nil {
				return nil, fmt.Errorf("failed to read cert %s: %w", c.CertPath, err)
			}
			keyPEM, err := os.ReadFile(c.KeyPath)
			if err != nil {
				return nil, fmt.Errorf("failed to read key %s: %w", c.KeyPath, err)
			}
			cert, err := tls.X509KeyPair(certPEM, keyPEM)
			if err != nil {
				return nil, fmt.Errorf("failed to load client certificate and key: %w", err)
			}
			tlsCfg.Certificates = []tls.Certificate{cert}
		}

		return tlsCfg, nil

	default:
		return nil, fmt.Errorf("invalid tls mode: %s", c.Mode)
	}
}
