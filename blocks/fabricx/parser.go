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
	"github.com/hyperledger/fabric-x-common/protoutil"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"google.golang.org/protobuf/proto"
)

// NewBlockParser returns a BlockParser that decodes Fabric-X blocks.
func NewBlockParser(log sdk.Logger) BlockParser {
	return BlockParser{log: log}
}

// BlockParser decodes raw Fabric-X block envelopes into the SDK's Block representation.
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
			tx.SetStatus(statusForTx(txFilter, txNum))
			tx.Number = int64(txNum)
			block.Transactions = append(block.Transactions, *tx)
		}
	}

	return block, nil
}

func statusForTx(txFilter []byte, txNum int) (blocks.Status, int32, string) {
	if txNum >= len(txFilter) {
		return blocks.StatusUnknown, 0, "missing tx filter entry"
	}
	return StatusFromCommitterStatus(committerpb.Status(txFilter[txNum]))
}

func (BlockParser) ParseTx(env *common.Envelope) (*blocks.Transaction, error) {
	pl := &common.Payload{}
	if err := proto.Unmarshal(env.Payload, pl); err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}
	if pl.Header == nil {
		return nil, fmt.Errorf("payload has no header")
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

	md := DecodeMetadata(ptx.Metadata)
	tx := &blocks.Transaction{
		ID:        chdr.TxId,
		InputArgs: md.InputArgs,
		Event:     md.Event,
		EventName: md.EventName,
		Payload:   md.Payload,
		NsRWS:     DecodeNamespaces(ptx.Namespaces),
	}

	return tx, nil
}
