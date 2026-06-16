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

// NewPeer dials a Fabric-X committer sidecar and binds it to the given channel and signer.
func NewPeer(conf network.PeerConf, channel string, signer sdk.Signer) (*Peer, error) {
	peer, err := network.NewPeer(conf)
	if err != nil {
		return nil, err
	}
	return &Peer{Peer: peer, channel: channel, signer: signer}, nil
}

// Peer is a channel-bound client for a Fabric-X committer sidecar.
type Peer struct {
	*network.Peer
	channel string
	signer  sdk.Signer
}

// SubscribeBlocks streams blocks from startBlock, invoking processor for each one.
func (p *Peer) SubscribeBlocks(ctx context.Context, startBlock uint64, processor network.BlockProcessor) error {
	return p.Peer.SubscribeBlocks(ctx, p.channel, startBlock, p.signer, processor)
}

// BlockHeight returns the current block height from the committer's BlockQueryService.
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

// StreamAllTransactions opens the sidecar's StreamAllTransactions server-stream and
// delivers each TxEventBatch to processor until the context is cancelled or an error occurs.
func (p *Peer) StreamAllTransactions(ctx context.Context, req *notification.StreamAllRequest, processor notification.AllTxProcessor) error {
	stream, err := committerpb.NewNotifierClient(p.Connection()).StreamAllTransactions(ctx, toProtoStreamAllRequest(req))
	if err != nil {
		return fmt.Errorf("open stream-all-transactions: %w", err)
	}

	for {
		batch, err := stream.Recv()
		if err != nil {
			if err == io.EOF || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("recv: %w", err)
		}
		sdkBatch := convertTxEventBatch(batch)
		if err := processor.ProcessBatch(ctx, sdkBatch); err != nil {
			return fmt.Errorf("process batch: %w", err)
		}
	}
}

func toProtoStreamAllRequest(req *notification.StreamAllRequest) *committerpb.StreamAllRequest {
	if req == nil {
		return &committerpb.StreamAllRequest{}
	}
	return &committerpb.StreamAllRequest{
		FilterNamespaces:     req.FilterNamespaces,
		FilterStatus:         req.FilterStatus,
		IncludeReadWriteSets: req.IncludeReadWriteSets,
		IncludeEndorsements:  req.IncludeEndorsements,
		IncludeMetadata:      req.IncludeMetadata,
	}
}

func convertTxEventBatch(batch *committerpb.TxEventBatch) notification.AllTxBatch {
	events := make([]notification.CommittedTxEvent, len(batch.Events))
	for i, e := range batch.Events {
		events[i] = notification.CommittedTxEvent{
			TxID:         e.Ref.GetTxId(),
			BlockNum:     e.Ref.GetBlockNum(),
			TxNum:        e.Ref.GetTxNum(),
			Status:       e.Status,
			Namespaces:   e.Namespaces,
			Endorsements: e.Endorsements,
			Metadata:     e.Metadata,
		}
	}
	return notification.AllTxBatch{BlockNumber: batch.BlockNumber, Events: events}
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

// NewSynchronizer creates a Fabric-X Synchronizer that streams blocks from the
// sidecar and maintains a local world state. It supports catch-up from any block
// height, automatic reconnection, and liveness/readiness probes.
//
// For a real-time feed of committed transactions without world-state maintenance,
// use notification.AllTxStreamer with the same Peer instead.
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
