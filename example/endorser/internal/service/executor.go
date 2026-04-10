/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/hyperledger/fabric-x-sdk/blocks"
	"github.com/hyperledger/fabric-x-sdk/endorsement"
	"github.com/hyperledger/fabric-x-sdk/state"
)

// SampleExecutor is a very basic getter and setter that
// uses the SimulationStore to generate read/write sets.
type SampleExecutor struct {
	db                state.ReadStore
	monotonicVersions bool
}

func (e SampleExecutor) Execute(ctx context.Context, inv endorsement.Invocation) (endorsement.ExecutionResult, error) {
	state, err := state.NewSimulationStore(ctx, e.db, inv.CCID.Name, 0, e.monotonicVersions)
	if err != nil {
		return endorsement.ExecutionResult{}, errors.New("failed creating store")
	}

	switch string(inv.Args[0]) {
	case "get":
		if len(inv.Args) < 2 {
			return endorsement.BadRequest("usage: get [key]"), nil
		}

		res, err := state.GetState(string(inv.Args[1]))
		if err != nil {
			return endorsement.ExecutionResult{}, fmt.Errorf("db: %w", err)
		}
		return endorsement.Success(blocks.ReadWriteSet{}, nil, res), nil
	case "set":
		if len(inv.Args) < 3 {
			return endorsement.BadRequest("usage: set [key] [value]"), nil
		}
		rws := blocks.ReadWriteSet{Writes: []blocks.KVWrite{
			{
				Key:   string(inv.Args[1]),
				Value: inv.Args[2],
			},
		}}
		return endorsement.Success(rws, nil, fmt.Appendf(nil, "%s=%s", string(inv.Args[1]), string(inv.Args[2]))), nil
	default:
		return endorsement.BadRequest("usage: get [key] || set [key] [value]"), nil
	}
}
