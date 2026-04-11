/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package config

import (
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/hyperledger/fabric-lib-go/common/flogging"
	"github.com/hyperledger/fabric-x-committer/utils/connection"
	"github.com/hyperledger/fabric-x-sdk/network"
)

// Config holds all configuration for the endorser
type Config struct {
	// ChannelID is the channel to connect to.
	ChannelID string `mapstructure:"channel-id"`

	// Server is the main gRPC server configuration with TLS, rate limiting, etc.
	Server *connection.ServerConfig `mapstructure:"server"`

	// Committer is a client connection to a committing peer (sidecar in case of fabric-x).
	Committer ClientConfig `mapstructure:"committer,omitempty"`

	// Identity is optional Fabric MSP identity configuration
	Identity *IdentityConfig `mapstructure:"identity,omitempty"`

	// Logging configuration
	Logging flogging.Config `mapstructure:"logging"`

	// Protocol selects the network protocol: "fabric" or "fabric-x". Defaults to "fabric-x".
	Protocol string `mapstructure:"protocol"`

	// Database configures the database. For now we only support sqlite.
	Database DatabaseConfig `mapstructure:"database"`
}

// IdentityConfig defines the component's MSP.
type IdentityConfig struct {
	// MspID indicates to which MSP this client belongs to.
	MspID  string `mapstructure:"msp-id" yaml:"msp-id"`
	MSPDir string `mapstructure:"msp-dir" yaml:"msp-dir"`
}

// ClientConfig contains a single endpoint, TLS config, and retry profile.
type ClientConfig struct {
	Endpoint *Endpoint `mapstructure:"endpoint"  yaml:"endpoint"`
	TLS      TLSConfig `mapstructure:"tls"       yaml:"tls"`
}

// ToPeerConf converts a ClientConfig to the SDK's PeerConf.
func (c ClientConfig) ToPeerConf() network.PeerConf {
	return network.PeerConf{
		Address: c.Endpoint.Address(),
		TLS: network.TLSConfig{
			Mode:        c.TLS.Mode,
			CertPath:    c.TLS.CertPath,
			KeyPath:     c.TLS.KeyPath,
			CACertPaths: c.TLS.CACertPaths,
		},
	}
}

// ToOrdererConf converts a ClientConfig to the SDK's OrdererConf.
func (c ClientConfig) ToOrdererConf() network.OrdererConf {
	return network.OrdererConf{
		Address: c.Endpoint.Address(),
		TLS: network.TLSConfig{
			Mode:        c.TLS.Mode,
			CertPath:    c.TLS.CertPath,
			KeyPath:     c.TLS.KeyPath,
			CACertPaths: c.TLS.CACertPaths,
		},
	}
}

// TLSConfig holds the TLS options and certificate paths
// used for secure communication between servers and clients.
// Credentials are built based on the configuration mode.
// For example, If only server-side TLS is required, the certificate pool (certPool) is not built (for a server),
// since the relevant certificates paths are defined in the YAML according to the selected mode.
type TLSConfig struct {
	Mode string `mapstructure:"mode"`
	// CertPath is the path to the certificate file (public key).
	CertPath string `mapstructure:"cert-path"`
	// KeyPath is the path to the key file (private key).
	KeyPath     string   `mapstructure:"key-path"`
	CACertPaths []string `mapstructure:"ca-cert-paths"`
}

// Endpoint describes a remote endpoint.
type Endpoint struct {
	Host string `mapstructure:"host" json:"host,omitempty" yaml:"host,omitempty"`
	Port int    `mapstructure:"port" json:"port,omitempty" yaml:"port,omitempty"`
}

// Address returns a string representation of the endpoint's address.
func (e *Endpoint) Address() string {
	// JoinHostPort defaults to ipv6 for localhost,
	// which is not always wanted.
	if e.Host == "localhost" {
		return fmt.Sprintf("%s:%d", e.Host, e.Port)
	}
	return net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
}

// DatabaseConfig is the configuration for the database.
type DatabaseConfig struct {
	ConnStr string `mapstructure:"connection-string"`
}

func (cfg Config) Validate() error {
	var errs []error

	if cfg.Database.ConnStr == "" {
		errs = append(errs, errors.New("database.connection-string is required"))
	}
	if cfg.ChannelID == "" {
		errs = append(errs, errors.New("channel-id is required"))
	}
	if p := cfg.Protocol; p != "" && p != "fabric" && p != "fabric-x" {
		errs = append(errs, errors.New("protocol must be either fabric or fabric-x"))
	}
	if cfg.Committer.Endpoint == nil {
		errs = append(errs, errors.New("committer.endpoint is required"))
	}

	return errors.Join(errs...)
}
