/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/hyperledger/fabric-lib-go/common/flogging"
	"github.com/hyperledger/fabric-x-committer/utils/connection"
	"github.com/hyperledger/fabric-x-common/common/viperutil"
	"github.com/hyperledger/fabric-x-sdk/example/endorser/config"
	"github.com/hyperledger/fabric-x-sdk/example/endorser/service"
	"github.com/spf13/cobra"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd := &cobra.Command{
		Use:   "endorser",
		Short: "Endorser - Example Fabric or Fabric-X endorser service",
		Long: `Endorser exposes the Fabric ProcessProposal endpoint and synchronizes its
world state with a committing peer. It can create endorsed read/write sets for either
Fabric or Fabric-X. This enables an alternative deployment model to chaincode for Fabric,
and make is possible to add chaincode-like functionality to Fabric-X networks.`,
		RunE: run,
	}

	cmd.Flags().StringP("config", "c", "", "Path to configuration file")
	cmd.Flags().String("log-level", "INFO", "Log level (DEBUG, INFO, WARNING, ERROR)")
	cmd.MarkFlagRequired("config")

	if err := cmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(cmd *cobra.Command, args []string) error {
	// Load configuration
	parser := viperutil.New()
	parser.SetConfigName("endorser")
	configFile, _ := cmd.Flags().GetString("config")
	f, err := os.Open(configFile)
	if err != nil {
		return fmt.Errorf("failed to open config: %w", err)
	}
	if err := parser.ReadConfig(f); err != nil {
		f.Close()
		return fmt.Errorf("failed to read config: %w", err)
	}
	f.Close()

	// Parse and validate
	var cfg config.Config
	if err := parser.EnhancedExactUnmarshal(&cfg); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	// Setup logging
	if cmd.Flags().Changed("log-level") {
		logLevel, _ := cmd.Flags().GetString("log-level")
		cfg.Logging.LogSpec = logLevel
	}
	flogging.Init(cfg.Logging)
	logger := flogging.MustGetLogger("main")
	logger.Infof("starting endorser", parser.ConfigFileUsed())

	// Create service
	executors := map[string]service.Executor{
		"basic": SampleExecutor{},
		"two":   SampleExecutor{},
	}
	svcCfg := service.ServiceConfig{
		ChannelID: cfg.ChannelID,
		Protocol:  cfg.Protocol,
		Committer: cfg.Committer.ToPeerConf(),
		DBConnStr: cfg.Database.ConnStr,
	}
	svc, err := service.New(svcCfg, cfg.Identity.MSPDir, cfg.Identity.MspID, executors, flogging.MustGetLogger("endorser"))
	if err != nil {
		return fmt.Errorf("failed to create service: %w", err)
	}

	// Start service
	// This handles:
	// - gRPC server creation with TLS
	// - Network listener binding
	// - Service registration
	// - Background task execution
	// - Graceful shutdown on context cancellation
	if err := connection.StartService(cmd.Context(), svc, cfg.Server); err != nil {
		return fmt.Errorf("service failed: %w", err)
	}

	logger.Info("service shutdown complete")
	return nil
}
