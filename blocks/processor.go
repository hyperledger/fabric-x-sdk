/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package blocks

import (
	"context"
	"fmt"

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
		return fmt.Errorf("parse: %w", err)
	}

	for _, h := range p.handlers {
		if err := h.Handle(ctx, b); err != nil {
			return fmt.Errorf("handle: %w", err)
		}
	}
	return nil
}
