/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabric

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/ledger/rwset"
	"github.com/hyperledger/fabric-protos-go-apiv2/ledger/rwset/kvrwset"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-common/protoutil"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"google.golang.org/protobuf/proto"
)

func buildApplicationEnvelope(t *testing.T, txID string) *common.Envelope {
	t.Helper()
	return buildEnvelope(t, txID, "ns", &kvrwset.KVRWSet{Writes: []*kvrwset.KVWrite{{Key: "k", Value: []byte("v")}}})
}

func buildConfigEnvelope(t *testing.T, txID string) *common.Envelope {
	t.Helper()
	update := buildTypedEnvelope(t, int32(common.HeaderType_CONFIG_UPDATE), txID+"-update", nil)
	return buildTypedEnvelope(t, int32(common.HeaderType_CONFIG), txID, mustMarshal(t, &common.ConfigEnvelope{
		Config: &common.Config{Sequence: 7, ChannelGroup: &common.ConfigGroup{
			Values: map[string]*common.ConfigValue{"example": {Version: 2, Value: []byte("channel-config"), ModPolicy: "Admins"}},
		}},
		LastUpdate: update,
	}))
}

func buildTypedEnvelope(t *testing.T, headerType int32, txID string, data []byte) *common.Envelope {
	t.Helper()
	return &common.Envelope{
		Payload: mustMarshal(t, &common.Payload{
			Header: &common.Header{ChannelHeader: mustMarshal(t, &common.ChannelHeader{Type: headerType, TxId: txID})},
			Data:   data,
		}),
		Signature: []byte("config-signature"),
	}
}

type parserLog struct {
	sdk.NoOpLogger
	infos    []string
	warnings []string
}

func (l *parserLog) Infof(format string, args ...any) {
	l.infos = append(l.infos, fmt.Sprintf(format, args...))
}

func (l *parserLog) Warnf(format string, args ...any) {
	l.warnings = append(l.warnings, fmt.Sprintf(format, args...))
}

func TestParse_ConfigTransaction(t *testing.T) {
	for _, number := range []uint64{0, 42} {
		t.Run(fmt.Sprint(number), func(t *testing.T) {
			env := buildConfigEnvelope(t, "config")
			b := buildBlock(t, number, []*common.Envelope{env}, nil)
			b.Header.PreviousHash = []byte("parent")
			log := &parserLog{}
			block, err := NewBlockParser(log).Parse(b)
			if err != nil {
				t.Fatal(err)
			}
			if !configMatches(t, block.ConfigTransaction, env) {
				t.Fatalf("ConfigTransaction = %v, want config from envelope %v", block.ConfigTransaction, env)
			}
			if block.Transactions == nil || len(block.Transactions) != 0 {
				t.Errorf("Transactions = %v, want nonnil empty slice", block.Transactions)
			}
			if block.Number != number || !bytes.Equal(block.ParentHash, b.Header.PreviousHash) ||
				!bytes.Equal(block.Hash, protoutil.BlockHeaderHash(b.Header)) {
				t.Errorf("block header fields changed: %+v", block)
			}
			if len(log.infos) != 1 || !strings.Contains(log.infos[0], "CONFIG") ||
				!strings.Contains(log.infos[0], fmt.Sprintf("[%d:0]", number)) {
				t.Errorf("info logs = %v, want CONFIG with block and envelope index", log.infos)
			}
			if len(log.warnings) != 0 {
				t.Errorf("unexpected warnings: %v", log.warnings)
			}
		})
	}
}

func TestParse_MalformedConfigFails(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"malformed_envelope", []byte{0xff}, "config envelope"},
		{"malformed_config", []byte{0x0a, 0x01, 0xff}, "config envelope"},
		{"empty_envelope", nil, "missing config"},
		{"last_update_only", mustMarshal(t, &common.ConfigEnvelope{LastUpdate: &common.Envelope{Payload: []byte("update")}}), "missing config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := buildTypedEnvelope(t, int32(common.HeaderType_CONFIG), "bad-config", tc.data)
			b := buildBlock(t, 42, []*common.Envelope{env, buildApplicationEnvelope(t, "after")}, nil)
			log := &parserLog{}
			block, err := NewBlockParser(log).Parse(b)
			if err == nil || !strings.Contains(err.Error(), "CONFIG [42:0]: "+tc.want) {
				t.Fatalf("error = %v, want CONFIG location and %q", err, tc.want)
			}
			if block.ConfigTransaction != nil || len(block.Transactions) != 0 || len(log.infos) != 0 {
				t.Fatalf("malformed CONFIG was accepted or parsing continued: %+v, logs=%v", block, log.infos)
			}
		})
	}
}

func TestParse_ZeroConfigSequence(t *testing.T) {
	env := buildTypedEnvelope(t, int32(common.HeaderType_CONFIG), "genesis", mustMarshal(t, &common.ConfigEnvelope{Config: &common.Config{}}))
	block, err := NewBlockParser(sdk.NoOpLogger{}).Parse(buildBlock(t, 0, []*common.Envelope{env}, nil))
	if err != nil || !configMatches(t, block.ConfigTransaction, env) {
		t.Fatalf("zero-sequence config: block=%+v err=%v", block, err)
	}
}

// configMatches checks the exposed sequence and decoded config, excluding the
// outer envelope and LastUpdate, which are deliberately not part of the SDK API.
func configMatches(t *testing.T, got *blocks.ConfigTransaction, env *common.Envelope) bool {
	t.Helper()
	payload := &common.Payload{}
	if err := proto.Unmarshal(env.Payload, payload); err != nil {
		t.Fatal(err)
	}
	want := &common.ConfigEnvelope{}
	if err := proto.Unmarshal(payload.Data, want); err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Sequence != want.Config.Sequence {
		return false
	}
	decoded := &common.Config{}
	if err := proto.Unmarshal(got.Config, decoded); err != nil {
		t.Fatalf("invalid serialized config: %v", err)
	}
	return proto.Equal(decoded, want.Config)
}

func TestParseTx_ConfigSkipped(t *testing.T) {
	p := NewBlockParser(sdk.NoOpLogger{})
	for _, headerType := range []int32{int32(common.HeaderType_CONFIG), int32(common.HeaderType_CONFIG_UPDATE), 999} {
		t.Run(fmt.Sprint(headerType), func(t *testing.T) {
			tx, err := p.ParseTx(buildTypedEnvelope(t, headerType, "non-app", []byte{0xff}))
			if tx != nil || err != nil {
				t.Fatalf("ParseTx = (%v, %v), want (nil, nil)", tx, err)
			}
		})
	}
}

func TestParse_NonConfigEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name string
		envs []*common.Envelope
		want int
	}{
		{"application", []*common.Envelope{buildApplicationEnvelope(t, "app")}, 1},
		{"config_update", []*common.Envelope{buildTypedEnvelope(t, int32(common.HeaderType_CONFIG_UPDATE), "update", []byte{0xff})}, 0},
		{"unknown", []*common.Envelope{buildTypedEnvelope(t, 999, "unknown", []byte{0xff})}, 0},
		{"empty", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := &parserLog{}
			block, err := NewBlockParser(log).Parse(buildBlock(t, 42, tc.envs, []byte{byte(peer.TxValidationCode_VALID)}))
			if err != nil {
				t.Fatal(err)
			}
			if block.ConfigTransaction != nil || len(block.Transactions) != tc.want {
				t.Fatalf("unexpected block: %+v", block)
			}
			if tc.want == 1 {
				tx := block.Transactions[0]
				if tx.ID != "app" || tx.Number != 0 || tx.Status != blocks.StatusCommitted ||
					len(tx.NsRWS) != 1 || tx.NsRWS[0].Namespace != "ns" ||
					len(tx.NsRWS[0].RWS.Writes) != 1 || string(tx.NsRWS[0].RWS.Writes[0].Value) != "v" {
					t.Errorf("application transaction changed: %+v", tx)
				}
			}
			if len(log.infos) != 0 || len(log.warnings) != 0 {
				t.Errorf("unexpected logs: info=%v warnings=%v", log.infos, log.warnings)
			}
		})
	}
}

func TestParse_ConfigThenApplication(t *testing.T) {
	config := buildConfigEnvelope(t, "config")
	b := buildBlock(t, 42, []*common.Envelope{buildApplicationEnvelope(t, "a"), config, buildApplicationEnvelope(t, "b")}, []byte{byte(peer.TxValidationCode_VALID), byte(peer.TxValidationCode_VALID), byte(peer.TxValidationCode_MVCC_READ_CONFLICT)})
	block, err := NewBlockParser(sdk.NoOpLogger{}).Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if !configMatches(t, block.ConfigTransaction, config) || len(block.Transactions) != 2 {
		t.Fatalf("unexpected block: %+v", block)
	}
	a, btx := block.Transactions[0], block.Transactions[1]
	if a.ID != "a" || a.Number != 0 || a.Status != blocks.StatusCommitted {
		t.Errorf("first transaction changed: %+v", a)
	}
	if btx.ID != "b" || btx.Number != 2 || btx.Status != blocks.StatusMVCCConflict ||
		btx.RawCode != int32(peer.TxValidationCode_MVCC_READ_CONFLICT) || btx.Reason != peer.TxValidationCode_MVCC_READ_CONFLICT.String() {
		t.Errorf("transaction lost original number/status: %+v", btx)
	}
}

func TestParse_MalformedEnvelopeRecovery(t *testing.T) {
	for _, tc := range []struct {
		name string
		bad  []byte
		want string
	}{
		{"envelope", []byte{0xff}, "malformed envelope [42:0]:"},
		{"payload", mustMarshal(t, &common.Envelope{Payload: []byte{0xff}}), "malformed tx [42:0]: payload:"},
		{"missing_header", mustMarshal(t, &common.Envelope{}), "malformed tx [42:0]: payload has no header"},
		{"channel_header", mustMarshal(t, &common.Envelope{Payload: mustMarshal(t, &common.Payload{Header: &common.Header{ChannelHeader: []byte{0xff}}})}), "malformed tx [42:0]: channel header:"},
		{"application_body", mustMarshal(t, buildTypedEnvelope(t, int32(common.HeaderType_ENDORSER_TRANSACTION), "bad-app", []byte{0xff})), "malformed tx [42:0]: transaction:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := buildConfigEnvelope(t, "config")
			b := buildBlock(t, 42, []*common.Envelope{nil, buildApplicationEnvelope(t, "app"), config}, []byte{byte(peer.TxValidationCode_VALID), byte(peer.TxValidationCode_VALID), byte(peer.TxValidationCode_VALID)})
			b.Data.Data[0] = tc.bad
			log := &parserLog{}
			block, err := NewBlockParser(log).Parse(b)
			if err != nil {
				t.Fatal(err)
			}
			if len(block.Transactions) != 1 || block.Transactions[0].Number != 1 ||
				block.Transactions[0].ID != "app" || block.Transactions[0].Status != blocks.StatusCommitted ||
				!configMatches(t, block.ConfigTransaction, config) {
				t.Fatalf("valid envelopes after malformed input were lost or changed: %+v", block)
			}
			if len(log.warnings) != 1 || !strings.HasPrefix(log.warnings[0], tc.want) {
				t.Errorf("warnings = %v, want one starting with %q", log.warnings, tc.want)
			}
			if len(log.infos) != 1 || !strings.Contains(log.infos[0], "[42:2]") {
				t.Errorf("info logs = %v, want CONFIG at [42:2]", log.infos)
			}
		})
	}
}

func TestParse_ConfigMetadata(t *testing.T) {
	p := NewBlockParser(sdk.NoOpLogger{})
	config := buildConfigEnvelope(t, "config")
	t.Run("empty_filter", func(t *testing.T) {
		block, err := p.Parse(buildBlock(t, 42, []*common.Envelope{config}, nil))
		if err != nil || !configMatches(t, block.ConfigTransaction, config) {
			t.Fatalf("empty filter: block=%+v err=%v", block, err)
		}
	})
	t.Run("missing_filter_slot", func(t *testing.T) {
		b := buildBlock(t, 42, []*common.Envelope{config}, nil)
		b.Metadata.Metadata = nil
		_, err := p.Parse(b)
		if err == nil || err.Error() != "block metadata missing TRANSACTIONS_FILTER" {
			t.Fatalf("error = %v, want missing TRANSACTIONS_FILTER", err)
		}
	})
	t.Run("short_filter", func(t *testing.T) {
		b := buildBlock(t, 42, []*common.Envelope{config, buildApplicationEnvelope(t, "app")}, []byte{byte(peer.TxValidationCode_VALID)})
		block, err := p.Parse(b)
		if err != nil {
			t.Fatal(err)
		}
		if !configMatches(t, block.ConfigTransaction, config) || len(block.Transactions) != 1 {
			t.Fatalf("unexpected block: %+v", block)
		}
		tx := block.Transactions[0]
		if tx.Number != 1 || tx.Status != blocks.StatusUnknown || tx.RawCode != 0 || tx.Reason != "missing tx filter entry" {
			t.Errorf("short filter status changed: %+v", tx)
		}
	})
}

func TestParse_EmptyChannelHeader(t *testing.T) {
	env := buildApplicationEnvelope(t, "app")
	payload := &common.Payload{}
	if err := proto.Unmarshal(env.Payload, payload); err != nil {
		t.Fatal(err)
	}
	payload.Header.ChannelHeader = nil
	env.Payload = mustMarshal(t, payload)
	log := &parserLog{}
	block, err := NewBlockParser(log).Parse(buildBlock(t, 42, []*common.Envelope{env}, []byte{byte(peer.TxValidationCode_VALID)}))
	if err != nil {
		t.Fatal(err)
	}
	if block.ConfigTransaction != nil || len(block.Transactions) != 0 {
		t.Fatalf("empty channel header behavior changed: %+v", block)
	}

	if len(log.warnings) != 0 || len(log.infos) != 0 {
		t.Errorf("unexpected logs: info=%v warnings=%v", log.infos, log.warnings)
	}
}

// mustMarshal panics on marshal error (test helper only).
func mustMarshal(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatalf("marshal %T: %v", m, err)
	}
	return b
}

// buildEnvelope builds the deeply nested Fabric envelope for a single
// chaincode action with the given namespace KVRWSet.
func buildEnvelope(t *testing.T, txID, namespace string, kvs *kvrwset.KVRWSet) *common.Envelope {
	t.Helper()

	ccActionBytes := mustMarshal(t, &peer.ChaincodeAction{
		Results: mustMarshal(t, &rwset.TxReadWriteSet{
			NsRwset: []*rwset.NsReadWriteSet{
				{Namespace: namespace, Rwset: mustMarshal(t, kvs)},
			},
		}),
	})

	capBytes := mustMarshal(t, &peer.ChaincodeActionPayload{
		Action: &peer.ChaincodeEndorsedAction{
			ProposalResponsePayload: mustMarshal(t, &peer.ProposalResponsePayload{
				Extension: ccActionBytes,
			}),
		},
	})

	txBytes := mustMarshal(t, &peer.Transaction{
		Actions: []*peer.TransactionAction{
			{Payload: capBytes},
		},
	})

	payloadBytes := mustMarshal(t, &common.Payload{
		Header: &common.Header{
			ChannelHeader: mustMarshal(t, &common.ChannelHeader{TxId: txID, Type: int32(common.HeaderType_ENDORSER_TRANSACTION)}),
		},
		Data: txBytes,
	})

	return &common.Envelope{Payload: payloadBytes}
}

func buildBlock(t *testing.T, blockNum uint64, envelopes []*common.Envelope, txFilter []byte) *common.Block {
	t.Helper()
	data := make([][]byte, len(envelopes))
	for i, env := range envelopes {
		data[i] = mustMarshal(t, env)
	}
	metadata := make([][]byte, int(common.BlockMetadataIndex_TRANSACTIONS_FILTER)+1)
	metadata[common.BlockMetadataIndex_TRANSACTIONS_FILTER] = txFilter
	return &common.Block{
		Header: &common.BlockHeader{Number: blockNum},
		Data:   &common.BlockData{Data: data},
		Metadata: &common.BlockMetadata{
			Metadata: metadata,
		},
	}
}

func TestParse_TxNumber(t *testing.T) {
	kvs := &kvrwset.KVRWSet{
		Writes: []*kvrwset.KVWrite{{Key: "k", Value: []byte("v")}},
	}
	env0 := buildEnvelope(t, "tx0", "ns", kvs)
	env1 := buildEnvelope(t, "tx1", "ns", kvs)
	env2 := buildEnvelope(t, "tx2", "ns", kvs)

	txFilter := []byte{
		byte(peer.TxValidationCode_VALID),
		byte(peer.TxValidationCode_MVCC_READ_CONFLICT),
		byte(peer.TxValidationCode_VALID),
	}
	b := buildBlock(t, 5, []*common.Envelope{env0, env1, env2}, txFilter)

	p := NewBlockParser(sdk.NoOpLogger{})
	block, err := p.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Transactions) != 3 {
		t.Fatalf("expected 3 transactions, got %d", len(block.Transactions))
	}
	for i, tx := range block.Transactions {
		if tx.Number != int64(i) {
			t.Errorf("tx[%d].Number = %d, want %d", i, tx.Number, i)
		}
	}
}

func TestParse_BasicWrite(t *testing.T) {
	kvs := &kvrwset.KVRWSet{
		Writes: []*kvrwset.KVWrite{
			{Key: "k", Value: []byte("v")},
		},
	}
	env := buildEnvelope(t, "txid1", "ns", kvs)

	p := NewBlockParser(sdk.NoOpLogger{})
	btx, err := p.ParseTx(env)
	if err != nil {
		t.Fatal(err)
	}
	if btx.ID != "txid1" {
		t.Errorf("unexpected txID: %q", btx.ID)
	}
	rws := btx.NsRWS[0].RWS
	if len(rws.Writes) != 1 || rws.Writes[0].Key != "k" || string(rws.Writes[0].Value) != "v" {
		t.Errorf("unexpected writes: %+v", rws.Writes)
	}
}

func TestParse_Delete(t *testing.T) {
	kvs := &kvrwset.KVRWSet{
		Writes: []*kvrwset.KVWrite{
			{Key: "d", IsDelete: true},
		},
	}
	env := buildEnvelope(t, "txid2", "ns", kvs)

	p := NewBlockParser(sdk.NoOpLogger{})
	btx, err := p.ParseTx(env)
	if err != nil {
		t.Fatal(err)
	}
	rws := btx.NsRWS[0].RWS
	if len(rws.Writes) != 1 || !rws.Writes[0].IsDelete {
		t.Errorf("expected delete write, got %+v", rws.Writes)
	}
}

func TestParse_ReadWrite(t *testing.T) {
	kvs := &kvrwset.KVRWSet{
		Reads: []*kvrwset.KVRead{
			{Key: "k", Version: &kvrwset.Version{BlockNum: 7}},
		},
		Writes: []*kvrwset.KVWrite{
			{Key: "k", Value: []byte("new")},
		},
	}

	env := buildEnvelope(t, "txid3", "ns", kvs)
	p := NewBlockParser(sdk.NoOpLogger{})

	btx, err := p.ParseTx(env)
	if err != nil {
		t.Fatal(err)
	}
	rws := btx.NsRWS[0].RWS
	if len(rws.Writes) != 1 || rws.Writes[0].Key != "k" || string(rws.Writes[0].Value) != "new" {
		t.Errorf("unexpected writes: %+v", rws.Writes)
	}
	if len(rws.Reads) != 1 || rws.Reads[0].Key != "k" || rws.Reads[0].Version == nil || rws.Reads[0].Version.BlockNum != 7 {
		t.Errorf("unexpected reads: %+v", rws.Reads)
	}
}
