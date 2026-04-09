/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/hyperledger/fabric-lib-go/common/flogging"
	"github.com/hyperledger/fabric-x-common/common/viperutil"
	"github.com/hyperledger/fabric-x-sdk/identity"
	"github.com/hyperledger/fabric-x-sdk/network"
	nfab "github.com/hyperledger/fabric-x-sdk/network/fabric"
	nfabx "github.com/hyperledger/fabric-x-sdk/network/fabricx"
	"github.com/spf13/cobra"
)

// Config holds all configuration for the endorser client.
type Config struct {
	// ChannelID is the channel to submit to.
	ChannelID string `mapstructure:"channel-id"`

	// Namespace is the chaincode name or Fabric-X namespace to invoke.
	Namespace string `mapstructure:"namespace"`

	// Protocol selects the network protocol: "fabric" or "fabric-x".
	// Defaults to "fabric-x".
	Protocol string `mapstructure:"protocol"`

	// Identity is the MSP identity used for signing the proposal and the transaction.
	Identity *IdentityConfig `mapstructure:"identity"`

	// Endorsers is the list of endorser endpoints, one per organization.
	// Each entry has its own TLS configuration because endorsers run at different orgs.
	Endorsers []ClientConfig `mapstructure:"endorsers"`

	// Orderer is the ordering service endpoint the signed transaction is submitted to.
	// Required for invoke; ignored by query.
	Orderer *ClientConfig `mapstructure:"orderer"`
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
	return net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
}

// txInput is the JSON format for the transaction argument.
// It follows the Fabric peer CLI convention: function name plus arguments.
type txInput struct {
	Function string   `json:"Function"`
	Args     []string `json:"Args"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := &cobra.Command{
		Use:   "client",
		Short: "Client - Example Fabric-X endorser client",
		Long: `Client sends transaction proposals to endorser services and optionally
submits the results to an orderer.

  query  — endorse only; prints the response payload (read-only)
  invoke — endorse and submit to the orderer (write)`,
	}
	root.PersistentFlags().StringP("config", "c", "", "Path to configuration file")

	root.AddCommand(newQueryCmd(), newInvokeCmd())

	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func newQueryCmd() *cobra.Command {
	return &cobra.Command{
		Use:   `query '{"Args":[]}'`,
		Short: "Send a read-only proposal and print the response payload",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, txArgs, err := prepare(cmd, args[0])
			if err != nil {
				return err
			}

			signer, err := identity.SignerFromMSP(cfg.Identity.MSPDir, cfg.Identity.MspID)
			if err != nil {
				return fmt.Errorf("load identity: %w", err)
			}

			ec, err := buildEndorsementClient(cfg, signer)
			if err != nil {
				return err
			}
			defer ec.Close() //nolint:errcheck

			end, err := ec.ExecuteTransaction(cmd.Context(), cfg.Namespace, "1.0", txArgs)
			if err != nil {
				return fmt.Errorf("endorsement failed: %w", err)
			}
			if len(end.Responses) == 0 {
				return nil
			}
			resp := end.Responses[0].Response
			if resp.Status < 200 || resp.Status >= 400 {
				return fmt.Errorf("endorser returned error status %d: %s", resp.Status, resp.Message)
			}
			fmt.Print(string(resp.Payload))
			return nil
		},
	}
}

func newInvokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   `invoke '{"function":"...","Args":[]}'`,
		Short: "Endorse a transaction and submit it to the orderer",
		Long: `Endorse a transaction and submit it to the orderer.
Does not wait for finality. Use the committer's block delivery API if you
need to confirm that the transaction has been committed.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, txArgs, err := prepare(cmd, args[0])
			if err != nil {
				return err
			}
			if cfg.Orderer == nil {
				return fmt.Errorf("orderer is required for invoke")
			}

			signer, err := identity.SignerFromMSP(cfg.Identity.MSPDir, cfg.Identity.MspID)
			if err != nil {
				return fmt.Errorf("load identity: %w", err)
			}

			logger := flogging.MustGetLogger("client")

			ec, err := buildEndorsementClient(cfg, signer)
			if err != nil {
				return err
			}
			defer ec.Close() //nolint:errcheck

			ordererConfs := []network.OrdererConf{toOrdererConf(cfg.Orderer)}
			var submitter *network.FabricSubmitter
			switch cfg.Protocol {
			case "fabric":
				submitter, err = nfab.NewSubmitter(ordererConfs, signer, 0, logger)
			case "fabric-x", "":
				submitter, err = nfabx.NewSubmitter(ordererConfs, signer, 0, logger)
			default:
				return fmt.Errorf("unknown protocol %q: must be \"fabric\" or \"fabric-x\"", cfg.Protocol)
			}
			if err != nil {
				return fmt.Errorf("create submitter: %w", err)
			}
			defer submitter.Close() //nolint:errcheck

			logger.Debugf("sending proposal to %d endorser(s)", len(cfg.Endorsers))
			end, err := ec.ExecuteTransaction(cmd.Context(), cfg.Namespace, "1.0", txArgs)
			if err != nil {
				return fmt.Errorf("endorsement failed: %w", err)
			}
			if len(end.Responses) == 0 {
				return fmt.Errorf("no responses")
			}
			resp := end.Responses[0].Response
			if resp.Status < 200 || resp.Status >= 400 {
				return fmt.Errorf("endorser returned error status %d: %s", resp.Status, resp.Message)
			}

			logger.Debugf("submitting transaction to orderer")
			if err := submitter.Submit(cmd.Context(), end); err != nil {
				return fmt.Errorf("submit failed: %w", err)
			}
			logger.Debugf("transaction submitted")
			fmt.Print(string(resp.Payload))
			return nil
		},
	}
}

// prepare loads config and parses the transaction JSON — shared by query and invoke.
func prepare(cmd *cobra.Command, txJSON string) (Config, [][]byte, error) {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return Config{}, nil, err
	}
	txArgs, err := parseTxArgs(txJSON)
	if err != nil {
		return Config{}, nil, err
	}
	return cfg, txArgs, nil
}

func loadConfig(cmd *cobra.Command) (Config, error) {
	parser := viperutil.New()
	if configFile, _ := cmd.Flags().GetString("config"); configFile != "" {
		f, err := os.Open(configFile)
		if err != nil {
			return Config{}, fmt.Errorf("open config: %w", err)
		}
		defer f.Close()
		if err := parser.ReadConfig(f); err != nil {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
	} else {
		parser.SetConfigName("client")
		parser.AddConfigPaths(".")
		if err := parser.ReadInConfig(); err != nil {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
	}

	var cfg Config
	if err := parser.EnhancedExactUnmarshal(&cfg); err != nil {
		return Config{}, fmt.Errorf("invalid config: %w", err)
	}
	if err := validate(cfg); err != nil {
		return Config{}, fmt.Errorf("invalid config: %w", err)
	}
	return cfg, nil
}

func parseTxArgs(txJSON string) ([][]byte, error) {
	var tx txInput
	if err := json.Unmarshal([]byte(txJSON), &tx); err != nil {
		return nil, fmt.Errorf("invalid transaction JSON: %w", err)
	}
	txArgs := make([][]byte, 0, 1+len(tx.Args))
	if tx.Function != "" {
		txArgs = append(txArgs, []byte(tx.Function))
	}
	for _, a := range tx.Args {
		txArgs = append(txArgs, []byte(a))
	}
	return txArgs, nil
}

func validate(cfg Config) error {
	if cfg.ChannelID == "" {
		return fmt.Errorf("channel-id is required")
	}
	if cfg.Namespace == "" {
		return fmt.Errorf("namespace is required")
	}
	if cfg.Identity == nil {
		return fmt.Errorf("identity is required")
	}
	if len(cfg.Endorsers) == 0 {
		return fmt.Errorf("at least one endorser is required")
	}
	return nil
}

func buildEndorsementClient(cfg Config, signer identity.Signer) (*network.EndorsementClient, error) {
	peerConfs := make([]network.PeerConf, len(cfg.Endorsers))
	for i := range cfg.Endorsers {
		peerConfs[i] = toPeerConf(&cfg.Endorsers[i])
	}
	ec, err := network.NewEndorsementClient(peerConfs, signer, cfg.ChannelID, cfg.Namespace, "1.0")
	if err != nil {
		return nil, fmt.Errorf("create endorsement client: %w", err)
	}
	return ec, nil
}

// toPeerConf converts a connection.ClientConfig to the SDK's PeerConf.
func toPeerConf(cfg *ClientConfig) network.PeerConf {
	return network.PeerConf{
		Address: cfg.Endpoint.Address(),
		TLS: network.TLSConfig{
			Mode:        cfg.TLS.Mode,
			CertPath:    cfg.TLS.CertPath,
			KeyPath:     cfg.TLS.KeyPath,
			CACertPaths: cfg.TLS.CACertPaths,
		},
	}
}

// toOrdererConf converts a connection.ClientConfig to the SDK's OrdererConf.
func toOrdererConf(cfg *ClientConfig) network.OrdererConf {
	return network.OrdererConf{
		Address: cfg.Endpoint.Address(),
		TLS: network.TLSConfig{
			Mode:        cfg.TLS.Mode,
			CertPath:    cfg.TLS.CertPath,
			KeyPath:     cfg.TLS.KeyPath,
			CACertPaths: cfg.TLS.CACertPaths,
		},
	}
}
