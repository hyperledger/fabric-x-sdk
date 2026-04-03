/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"fmt"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-x-common/api/applicationpb"
	"github.com/hyperledger/fabric-x-common/api/committerpb"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"github.com/hyperledger/fabric/protoutil"
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
		}

		tx, err := p.ParseTx(env)
		if err != nil {
			p.log.Warnf("malformed tx [%d:%d]: %s", block.Number, txNum, err.Error())
			continue
		}
		if tx != nil {
			// we also parse invalid transactions in case a handler needs their content.
			tx.Valid = isValid(txFilter, txNum)
			block.Transactions = append(block.Transactions, *tx)
		}
	}

	return block, nil
}

func isValid(txFilter []byte, txNum int) bool {
	if txNum >= len(txFilter) {
		return false
	}
	return committerpb.Status(txFilter[txNum]) == committerpb.Status_COMMITTED
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
	if chdr.Type != int32(common.HeaderType_MESSAGE) {
		return nil, nil
	}

	ptx := &applicationpb.Tx{}
	if err := proto.Unmarshal(pl.Data, ptx); err != nil {
		return nil, fmt.Errorf("transaction: %w", err)
	}

	tx := &blocks.Transaction{
		ID:    chdr.TxId,
		NsRWS: make([]blocks.NsReadWriteSet, len(ptx.Namespaces)),
	}

	// TODO: input and event.

	// read / write set
	for i, ns := range ptx.Namespaces {
		nsrws := blocks.NsReadWriteSet{
			Namespace: ns.NsId,
			RWS: blocks.ReadWriteSet{
				Reads:  []blocks.KVRead{},
				Writes: []blocks.KVWrite{},
			},
		}

		// Fabric-X has no deletion concept: a nil value is stored as NULL in the
		// committer's database and does not remove the key. IsDelete is always false.
		for _, bw := range ns.BlindWrites {
			nsrws.RWS.Writes = append(nsrws.RWS.Writes, blocks.KVWrite{
				Key:   string(bw.Key),
				Value: bw.Value,
			})
		}
		for _, rw := range ns.ReadWrites {
			read := blocks.KVRead{Key: string(rw.Key)}
			// Version nil means "no constraint" (new key / blind-write semantics).
			// Version 0 is a valid MVCC constraint: the key was first written at block 0.
			if rw.Version != nil {
				read.Version = &blocks.Version{
					BlockNum: *rw.Version,
				}
			}
			nsrws.RWS.Reads = append(nsrws.RWS.Reads, read)
			nsrws.RWS.Writes = append(nsrws.RWS.Writes, blocks.KVWrite{
				Key:   string(rw.Key),
				Value: rw.Value,
			})
		}

		tx.NsRWS[i] = nsrws
	}

	return tx, nil
}
