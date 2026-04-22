/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabric

import (
	"fmt"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/ledger/rwset"
	"github.com/hyperledger/fabric-protos-go-apiv2/ledger/rwset/kvrwset"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-common/protoutil"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"google.golang.org/protobuf/proto"
)

func NewBlockParser(log sdk.Logger) BlockParser {
	return BlockParser{log: log}
}

type BlockParser struct {
	log sdk.Logger
}

func (p BlockParser) Parse(b *common.Block) (blocks.Block, error) {
	block := blocks.Block{
		Number:       b.Header.Number,
		Hash:         protoutil.BlockHeaderHash(b.Header),
		ParentHash:   b.Header.PreviousHash,
		Timestamp:    time.Now().Unix(), // TODO: this not really true, is there a better alternative?
		Transactions: []blocks.Transaction{},
	}

	// txFilter contains the list of transactions and their status.
	if len(b.Metadata.Metadata) <= int(common.BlockMetadataIndex_TRANSACTIONS_FILTER) {
		return block, fmt.Errorf("block metadata missing TRANSACTIONS_FILTER")
	}
	txFilter := b.Metadata.Metadata[common.BlockMetadataIndex_TRANSACTIONS_FILTER]

	// parse transactions.
	for txNum, envBytes := range b.Data.Data {
		env := &common.Envelope{}
		if err := proto.Unmarshal(envBytes, env); err != nil {
			p.log.Warnf("malformed envelope [%d:%d]: %s", block.Number, txNum, err.Error())
			continue
		}

		tx, err := p.ParseTx(env)
		if err != nil {
			p.log.Warnf("malformed tx [%d:%d]: %s", block.Number, txNum, err.Error())
			continue
		}
		if tx != nil {
			// we also include invalid transactions in case a handler needs their content.
			tx.Valid = isValid(txFilter, txNum)
			tx.Number = int64(txNum)
			block.Transactions = append(block.Transactions, *tx)
		}
	}

	return block, nil
}

func isValid(txFilter []byte, txNum int) bool {
	if txNum >= len(txFilter) {
		return false
	}
	return peer.TxValidationCode(txFilter[txNum]) == peer.TxValidationCode_VALID
}

func (BlockParser) ParseTx(env *common.Envelope) (*blocks.Transaction, error) {
	pl := &common.Payload{}
	if err := proto.Unmarshal(env.Payload, pl); err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}
	chdr := &common.ChannelHeader{}
	if err := proto.Unmarshal(pl.Header.ChannelHeader, chdr); err != nil {
		return nil, fmt.Errorf("channel header: %w", err)
	}

	// skip config transactions
	if chdr.Type != int32(common.HeaderType_ENDORSER_TRANSACTION) {
		return nil, nil
	}

	ptx := &peer.Transaction{}
	if err := proto.Unmarshal(pl.Data, ptx); err != nil {
		return nil, fmt.Errorf("transaction: %w", err)
	}

	// Fabric only supports a single action per transaction (the peer validates this too)
	if len(ptx.Actions) != 1 {
		return nil, fmt.Errorf("only one action per transaction is supported, tx contains %d", len(ptx.Actions))
	}
	cap := &peer.ChaincodeActionPayload{}
	if err := proto.Unmarshal(ptx.Actions[0].Payload, cap); err != nil {
		return nil, fmt.Errorf("chaincode action payload: %w", err)
	}

	tx := &blocks.Transaction{
		ID:    chdr.TxId,
		NsRWS: []blocks.NsReadWriteSet{},
	}

	// input args
	cpp := &peer.ChaincodeProposalPayload{}
	if err := proto.Unmarshal(cap.ChaincodeProposalPayload, cpp); err != nil {
		return nil, fmt.Errorf("chaincode proposal payload: %w", err)
	}
	cis := &peer.ChaincodeInvocationSpec{}
	if err := proto.Unmarshal(cpp.Input, cis); err != nil {
		return nil, fmt.Errorf("chaincode invocation spec: %w", err)
	}
	if cis.ChaincodeSpec != nil && cis.ChaincodeSpec.Input != nil {
		tx.InputArgs = cis.ChaincodeSpec.Input.Args
	}

	// events
	prp := &peer.ProposalResponsePayload{}
	if err := proto.Unmarshal(cap.Action.ProposalResponsePayload, prp); err != nil {
		return nil, fmt.Errorf("proposal response payload: %w", err)
	}
	ccAct := &peer.ChaincodeAction{}
	if err := proto.Unmarshal(prp.Extension, ccAct); err != nil {
		return nil, fmt.Errorf("chaincode action: %w", err)
	}
	tx.Events = ccAct.Events

	// read/write set
	txRWSet := &rwset.TxReadWriteSet{}
	if err := proto.Unmarshal(ccAct.Results, txRWSet); err != nil {
		return nil, fmt.Errorf("rwset: %w", err)
	}

	for _, ns := range txRWSet.NsRwset {
		if ns == nil || len(ns.Namespace) == 0 {
			continue
		}

		nsrws := blocks.NsReadWriteSet{
			Namespace: ns.Namespace,
			RWS: blocks.ReadWriteSet{
				Reads:  []blocks.KVRead{},
				Writes: []blocks.KVWrite{},
			},
		}

		kvs := &kvrwset.KVRWSet{}
		if err := proto.Unmarshal(ns.Rwset, kvs); err != nil {
			return tx, fmt.Errorf("kvrwset: %w", err)
		}
		for _, r := range kvs.Reads {
			read := blocks.KVRead{Key: r.Key}
			if r.Version != nil {
				read.Version = &blocks.Version{
					BlockNum: r.Version.BlockNum,
					TxNum:    r.Version.TxNum,
				}
			}
			nsrws.RWS.Reads = append(nsrws.RWS.Reads, read)
		}
		for _, w := range kvs.Writes {
			nsrws.RWS.Writes = append(nsrws.RWS.Writes, blocks.KVWrite{
				Key:      w.Key,
				Value:    w.Value,
				IsDelete: w.IsDelete,
			})
		}

		tx.NsRWS = append(tx.NsRWS, nsrws)
	}

	return tx, nil
}
