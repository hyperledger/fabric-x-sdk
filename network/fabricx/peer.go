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
		FilterStatus:         toProtoFilterStatus(req.FilterStatus),
		IncludeReadWriteSets: req.IncludeReadWriteSets,
		IncludeEndorsements:  req.IncludeEndorsements,
		IncludeMetadata:      req.IncludeMetadata,
	}
}

// toProtoFilterStatus expands the coarse, protocol-neutral filter statuses into
// the concrete sidecar status codes they cover. StatusEndorsementPolicyFailure
// has no distinct Fabric-X code of its own so it expands to the same code as
// StatusInvalidSignature. StatusUnrecognized is dropped.
func toProtoFilterStatus(statuses []blocks.Status) []committerpb.Status {
	if len(statuses) == 0 {
		return nil
	}
	var out []committerpb.Status
	for _, s := range statuses {
		switch s {
		case blocks.StatusCommitted:
			out = append(out, committerpb.Status_COMMITTED)
		case blocks.StatusInvalidSignature, blocks.StatusEndorsementPolicyFailure:
			out = append(out, committerpb.Status_ABORTED_SIGNATURE_INVALID)
		case blocks.StatusMVCCConflict:
			out = append(out, committerpb.Status_ABORTED_MVCC_CONFLICT)
		case blocks.StatusDuplicateTxID:
			out = append(out, committerpb.Status_REJECTED_DUPLICATE_TX_ID)
		case blocks.StatusMalformed:
			// Sidecar codes >= 101 are the MALFORMED_* family (100 is the
			// duplicate-tx-id rejection, handled above as its own status).
			for code := range committerpb.Status_name {
				if code >= 101 {
					out = append(out, committerpb.Status(code))
				}
			}
		case blocks.StatusUnknown:
			out = append(out, committerpb.Status_STATUS_UNSPECIFIED)
			// StatusUnrecognized has no corresponding sidecar code to filter by.
		}
	}
	return out
}

func convertTxEventBatch(batch *committerpb.TxEventBatch) notification.AllTxBatch {
	events := make([]notification.CommittedTxEvent, len(batch.Events))
	for i, e := range batch.Events {
		md := fabricx.DecodeMetadata(e.Metadata)
		events[i] = notification.CommittedTxEvent{
			Transaction: blocks.Transaction{
				ID:        e.Ref.GetTxId(),
				Number:    int64(e.Ref.GetTxNum()),
				InputArgs: md.InputArgs,
				Event:     md.Event,
				EventName: md.EventName,
				Payload:   md.Payload,
				NsRWS:     fabricx.DecodeNamespaces(e.Namespaces),
			},
			BlockNum:     e.Ref.GetBlockNum(),
			Endorsements: e.Endorsements,
		}
		events[i].SetStatus(fabricx.StatusFromCommitterStatus(e.Status))
	}
	return notification.AllTxBatch{BlockNumber: batch.BlockNumber, Events: events}
}

// convertTxStatuses maps sidecar TxStatus records onto neutral TxStatusEvents.
func convertTxStatuses(statuses []*committerpb.TxStatus) []notification.TxStatusEvent {
	events := make([]notification.TxStatusEvent, 0, len(statuses))
	for _, s := range statuses {
		status, rawCode, reason := fabricx.StatusFromCommitterStatus(s.Status)
		events = append(events, notification.TxStatusEvent{
			TxID:     s.Ref.GetTxId(),
			BlockNum: s.Ref.GetBlockNum(),
			TxNum:    s.Ref.GetTxNum(),
			Status:   status,
			RawCode:  rawCode,
			Reason:   reason,
		})
	}
	return events
}

func convertNotificationResponse(res *committerpb.NotificationResponse) []notification.TxStatusEvent {
	events := convertTxStatuses(res.TxStatusEvents)
	for _, txID := range res.TimeoutTxIds {
		events = append(events, notification.TxStatusEvent{
			TxID:   txID,
			Status: blocks.StatusUnknown,
		})
	}
	// TODO: RejectedTxIds field was added in a later version of fabric-x-common.
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
