/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"context"
	"fmt"

	"github.com/hyperledger/fabric-x-sdk/endorsement"
	"github.com/hyperledger/fabric-x-sdk/example/endorser/service"
)

// SampleExecutor is a very basic getter and setter that
// uses the SimulationStore to generate read/write sets.
type SampleExecutor struct{}

// Execute implements the service.Executor inteface.
func (SampleExecutor) Execute(ctx context.Context, newStore service.StoreFactory, inv endorsement.Invocation) (endorsement.ExecutionResult, error) {
	store, err := newStore(0)
	if err != nil {
		return endorsement.ExecutionResult{}, fmt.Errorf("simulation store: %w", err)
	}
	switch string(inv.Args[0]) {
	case "get":
		if len(inv.Args) < 2 {
			return endorsement.BadRequest("usage: get [key]"), nil
		}
		res, err := store.GetState(string(inv.Args[1]))
		if err != nil {
			return endorsement.ExecutionResult{}, fmt.Errorf("db: %w", err)
		}
		return endorsement.Success(store.Result(), nil, res), nil
	case "set":
		if len(inv.Args) < 3 {
			return endorsement.BadRequest("usage: set [key] [value]"), nil
		}
		if err := store.PutState(string(inv.Args[1]), inv.Args[2]); err != nil {
			return endorsement.ExecutionResult{}, fmt.Errorf("db: %w", err)
		}
		return endorsement.Success(store.Result(), nil, fmt.Appendf(nil, "%s=%s", string(inv.Args[1]), string(inv.Args[2]))), nil
	default:
		return endorsement.BadRequest("usage: get [key] || set [key] [value]"), nil
	}
}
