/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package blocks

import (
	"context"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
)

func NewProcessor(parser BlockParser, handlers []BlockHandler) *Processor {
	return &Processor{
		parser:   parser,
		handlers: handlers,
	}
}

type BlockParser interface {
	Parse(*common.Block) (Block, error)
}
type BlockHandler interface {
	Handle(context.Context, Block) error
}

type Processor struct {
	parser   BlockParser
	handlers []BlockHandler
}

// ProcessBlock parses a block and executes the handlers in order. If any handler
// fails, ProcessBlock bubbles up the error.
func (p Processor) ProcessBlock(ctx context.Context, block *common.Block) error {
	b, err := p.parser.Parse(block)
	if err != nil {
		return err // ? TODO: decide on error handling for block parsing.
	}

	for _, h := range p.handlers {
		if err := h.Handle(ctx, b); err != nil {
			return err
		}
	}
	return nil
}
