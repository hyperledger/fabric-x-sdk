/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package queryservice_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/hyperledger/fabric-x-sdk/blocks"

	"github.com/hyperledger/fabric-x-sdk/state/queryservice"
)

// fakeClient serves rows from a map and records reads and views.
type fakeClient struct {
	rows      map[string]queryservice.Row // key -> row, for namespace "basic"
	reads     int
	readViews []string
	begun     int
	ended     []string
	closed    bool
}

func (c *fakeClient) BeginView(context.Context) (string, error) {
	c.begun++
	return fmt.Sprintf("view-%d", c.begun), nil
}

func (c *fakeClient) EndView(_ context.Context, viewID string) error {
	c.ended = append(c.ended, viewID)
	return nil
}

func (c *fakeClient) GetRows(_ context.Context, viewID, ns string, keys [][]byte) ([]queryservice.Row, error) {
	c.reads++
	c.readViews = append(c.readViews, viewID)
	var out []queryservice.Row
	for _, k := range keys {
		if r, ok := c.rows[string(k)]; ok && ns == "basic" {
			out = append(out, r)
		}
	}
	return out, nil
}

func (c *fakeClient) Close() error { c.closed = true; return nil }

func TestStoreGet(t *testing.T) {
	client := &fakeClient{rows: map[string]queryservice.Row{"k1": {Key: []byte("k1"), Value: []byte("v1"), Version: 3}}}
	store := queryservice.NewStore(client)

	rec, err := store.Get("basic", "k1")
	if err != nil {
		t.Fatal(err)
	}
	if rec == nil || string(rec.Value) != "v1" || rec.Version != 3 {
		t.Fatalf("Get k1 = %+v, want v1 at version 3", rec)
	}

	rec, err = store.Get("basic", "absent")
	if err != nil || rec != nil {
		t.Fatalf("Get absent = %+v, %v; want nil, nil", rec, err)
	}
	if client.begun != 0 || client.readViews[0] != "" {
		t.Errorf("Get used a view (begun %d, read view %q); a single read needs none", client.begun, client.readViews[0])
	}
}

func TestStoreSnapshotReadsThroughOneView(t *testing.T) {
	client := &fakeClient{rows: map[string]queryservice.Row{"k1": {Key: []byte("k1"), Value: []byte("v1")}}}
	store := queryservice.NewStore(client)

	snap, err := store.NewSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"k1", "k2"} {
		if _, err := snap.Get("basic", key); err != nil {
			t.Fatal(err)
		}
	}
	if err := snap.Close(); err != nil {
		t.Fatal(err)
	}

	if client.begun != 1 {
		t.Fatalf("views begun = %d, want 1", client.begun)
	}
	for i, v := range client.readViews {
		if v != "view-1" {
			t.Errorf("read %d used view %q, want view-1", i, v)
		}
	}
	if len(client.ended) != 1 || client.ended[0] != "view-1" {
		t.Errorf("views ended = %v, want [view-1]", client.ended)
	}
}

func TestStoreSnapshotCachesReads(t *testing.T) {
	client := &fakeClient{rows: map[string]queryservice.Row{"k1": {Key: []byte("k1"), Value: []byte("v1")}}}
	store := queryservice.NewStore(client)

	snap, err := store.NewSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := snap.Get("basic", "k1"); err != nil {
			t.Fatal(err)
		}
		if _, err := snap.Get("basic", "absent"); err != nil {
			t.Fatal(err)
		}
	}
	if client.reads != 2 {
		t.Errorf("client reads = %d, want 2 (one per key)", client.reads)
	}
}

func TestStoreBlockNumber(t *testing.T) {
	store := queryservice.NewStore(&fakeClient{})
	ctx := context.Background()

	store.SetBlockNumber(5)
	for _, tc := range []struct{ deliver, want uint64 }{
		{5, 5},
		{3, 5}, // an older block must not move the height back
		{7, 7},
	} {
		if err := store.Handle(ctx, blocks.Block{Number: tc.deliver}); err != nil {
			t.Fatal(err)
		}
		if got, _ := store.BlockNumber(ctx); got != tc.want {
			t.Errorf("after block %d: BlockNumber = %d, want %d", tc.deliver, got, tc.want)
		}
	}
}

func TestStoreCloseClosesClient(t *testing.T) {
	client := &fakeClient{}
	if err := queryservice.NewStore(client).Close(); err != nil {
		t.Fatal(err)
	}
	if !client.closed {
		t.Error("client not closed")
	}
}
