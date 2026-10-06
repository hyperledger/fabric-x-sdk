/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// Package queryservice reads world state from the Fabric-X committer's query
// service, so an application can do without a local copy of the state.
package queryservice

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hyperledger/fabric-x-common/api/committerpb"

	"github.com/hyperledger/fabric-x-sdk/network"
)

// Row is one committed key, with its value and MVCC version.
type Row struct {
	Key     []byte
	Value   []byte
	Version uint64
}

// Client reads committed state.
type Client interface {
	// BeginView opens a view: a snapshot of committed state that all reads in
	// it see.
	BeginView(ctx context.Context) (viewID string, err error)
	// GetRows returns the committed rows for keys in namespace ns, as seen by
	// the view, or the latest state if viewID is empty. Keys with no committed
	// value are left out.
	GetRows(ctx context.Context, viewID, ns string, keys [][]byte) ([]Row, error)
	// EndView closes a view.
	EndView(ctx context.Context, viewID string) error
	Close() error
}

// DefaultConnections is a good connection count for NewGRPCClient. Concurrent
// reads on one HTTP/2 connection queue behind each other; a few connections
// spread them.
const DefaultConnections = 8

// callTimeout bounds a single call.
const callTimeout = 5 * time.Second

// GRPCClient reads from the query service, spreading views over its
// connections. A view's calls all go over the connection that opened it, in
// case the address leads to more than one query-service instance.
type GRPCClient struct {
	peers   []*network.Peer
	clients []committerpb.QueryServiceClient
	next    atomic.Uint64
	views   sync.Map // view id -> its client
}

var _ Client = (*GRPCClient)(nil)

// NewGRPCClient opens conns connections to the query service at conf.
func NewGRPCClient(conf network.PeerConf, conns int) (*GRPCClient, error) {
	if conns < 1 {
		return nil, errors.New("queryservice: need at least one connection")
	}
	c := &GRPCClient{}
	for range conns {
		p, err := network.NewPeer(conf)
		if err != nil {
			_ = c.Close()
			return nil, err
		}
		c.peers = append(c.peers, p)
		c.clients = append(c.clients, committerpb.NewQueryServiceClient(p.Connection()))
	}
	return c, nil
}

// BeginView opens a repeatable-read view: a consistent snapshot, taken at the
// view's first read. The query service may share that snapshot with views
// opened shortly before (view-aggregation-window), so keep the window short.
func (c *GRPCClient) BeginView(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	client := c.pick()
	view, err := client.BeginView(ctx, &committerpb.ViewParameters{IsoLevel: committerpb.IsoLevel_REPEATABLE_READ})
	if err != nil {
		return "", err
	}
	c.views.Store(view.GetId(), client)
	return view.GetId(), nil
}

// GetRows reads keys in the view, or the latest state if viewID is empty.
func (c *GRPCClient) GetRows(ctx context.Context, viewID, ns string, keys [][]byte) ([]Row, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	q := &committerpb.Query{Namespaces: []*committerpb.QueryNamespace{{NsId: ns, Keys: keys}}}
	if viewID != "" {
		q.View = &committerpb.View{Id: viewID}
	}
	res, err := c.clientFor(viewID).GetRows(ctx, q)
	if err != nil {
		return nil, err
	}

	var rows []Row
	for _, rns := range res.GetNamespaces() {
		if rns.GetNsId() != ns {
			continue
		}
		for _, r := range rns.GetRows() {
			rows = append(rows, Row{Key: r.GetKey(), Value: r.GetValue(), Version: r.GetVersion()})
		}
	}
	return rows, nil
}

// EndView closes a view.
func (c *GRPCClient) EndView(ctx context.Context, viewID string) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	_, err := c.clientFor(viewID).EndView(ctx, &committerpb.View{Id: viewID})
	c.views.Delete(viewID)
	return err
}

// clientFor returns the client that opened the view, or the next one if there
// is no view.
func (c *GRPCClient) clientFor(viewID string) committerpb.QueryServiceClient {
	if client, ok := c.views.Load(viewID); ok {
		return client.(committerpb.QueryServiceClient)
	}
	return c.pick()
}

// pick returns the next connection's client, round-robin.
func (c *GRPCClient) pick() committerpb.QueryServiceClient {
	return c.clients[c.next.Add(1)%uint64(len(c.clients))]
}

// Close closes all connections.
func (c *GRPCClient) Close() error {
	var errs []error
	for _, p := range c.peers {
		errs = append(errs, p.Close())
	}
	return errors.Join(errs...)
}
