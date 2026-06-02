/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/hyperledger/fabric-x-common/api/committerpb"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"github.com/hyperledger/fabric-x-sdk/blocks/fabricx"
	"github.com/hyperledger/fabric-x-sdk/network"
	"github.com/hyperledger/fabric-x-sdk/notification"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
)

func NewPeer(conf network.PeerConf, channel string, signer sdk.Signer) (*Peer, error) {
	peer, err := network.NewPeer(conf)
	if err != nil {
		return nil, err
	}
	return &Peer{Peer: peer, channel: channel, signer: signer}, nil
}

type Peer struct {
	*network.Peer
	channel string
	signer  sdk.Signer
}

func (p *Peer) SubscribeBlocks(ctx context.Context, startBlock uint64, processor network.BlockProcessor) error {
	return p.Peer.SubscribeBlocks(ctx, p.channel, startBlock, p.signer, processor)
}

func (p *Peer) BlockHeight(ctx context.Context) (uint64, error) {
	client := committerpb.NewBlockQueryServiceClient(p.Connection())
	info, err := client.GetBlockchainInfo(ctx, &emptypb.Empty{})
	if err != nil {
		return 0, err
	}
	return info.Height, nil
}

// Notify opens a notification stream to the sidecar and subscribes to transaction
// status events for the IDs sent on txIDs. It blocks until the context is canceled,
// the stream closes, or an error occurs.
func (p *Peer) Notify(ctx context.Context, txIDs <-chan []string, processor *notification.Processor, timeout time.Duration) error {
	stream, err := committerpb.NewNotifierClient(p.Connection()).OpenNotificationStream(ctx)
	if err != nil {
		return fmt.Errorf("open notification stream: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 1)

	go func() {
		if err := notificationSendLoop(ctx, stream, txIDs, timeout); err != nil {
			select {
			case errCh <- err:
				cancel()
			default:
			}
		}
	}()

	go func() {
		if err := notificationReceiveLoop(ctx, stream, processor); err != nil {
			select {
			case errCh <- err:
				cancel()
			default:
			}
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return nil
	}
}

func notificationSendLoop(ctx context.Context, stream committerpb.Notifier_OpenNotificationStreamClient, txIDs <-chan []string, timeout time.Duration) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case batch, ok := <-txIDs:
			if !ok {
				return nil
			}
			if len(batch) == 0 {
				continue
			}
			req := &committerpb.NotificationRequest{
				TxStatusRequest: &committerpb.TxIDsBatch{TxIds: batch},
				Timeout:         durationpb.New(timeout),
			}
			if err := stream.Send(req); err != nil {
				return fmt.Errorf("send request: %w", err)
			}
		}
	}
}

func notificationReceiveLoop(ctx context.Context, stream committerpb.Notifier_OpenNotificationStreamClient, processor *notification.Processor) error {
	for {
		res, err := stream.Recv()
		if err != nil {
			if err == io.EOF || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("recv: %w", err)
		}
		events := convertNotificationResponse(res)
		if len(events) > 0 {
			if err := processor.ProcessStatuses(ctx, events); err != nil {
				// handler errors are non-fatal; continue receiving
				_ = err
			}
		}
	}
}

func convertNotificationResponse(res *committerpb.NotificationResponse) []notification.TxStatusEvent {
	var events []notification.TxStatusEvent
	for _, txStatus := range res.TxStatusEvents {
		events = append(events, notification.TxStatusEvent{
			TxID:     txStatus.Ref.TxId,
			BlockNum: txStatus.Ref.BlockNum,
			TxNum:    txStatus.Ref.TxNum,
			Status:   txStatus.Status,
		})
	}
	for _, txID := range res.TimeoutTxIds {
		events = append(events, notification.TxStatusEvent{
			TxID:   txID,
			Status: committerpb.Status_STATUS_UNSPECIFIED,
		})
	}
	// Note: RejectedTxIds field was added in a later version of fabric-x-common.
	// The current version (v0.1.1-0.20260219094834-26c5a49ed548) only has
	// TxStatusEvents and TimeoutTxIds fields. Rejections will be handled
	// when the SDK upgrades to a newer version of fabric-x-common.
	return events
}

func NewSynchronizer(db network.BlockHeightReader, channel string, conf network.PeerConf, signer sdk.Signer, logger sdk.Logger, handlers ...blocks.BlockHandler) (*network.Synchronizer, error) {
	peer, err := NewPeer(conf, channel, signer)
	if err != nil {
		return nil, err
	}

	return network.NewSynchronizer(
		db,
		peer,
		blocks.NewProcessor(fabricx.NewBlockParser(logger), handlers),
		logger,
	)
}
