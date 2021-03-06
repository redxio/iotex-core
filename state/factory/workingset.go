// Copyright (c) 2019 IoTeX Foundation
// This is an alpha (internal) release and is not suitable for production. This source code is provided 'as is' and no
// warranties are given as to title or non-infringement, merchantability or fitness for purpose and, to the extent
// permitted by law, all liability for your use of the code is disclaimed. This source code is governed by Apache
// License 2.0 that can be found in the LICENSE file.

package factory

import (
	"context"
	"fmt"

	"github.com/iotexproject/go-pkgs/hash"
	"github.com/iotexproject/iotex-address/address"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/iotexproject/iotex-core/action"
	"github.com/iotexproject/iotex-core/action/protocol"
	"github.com/iotexproject/iotex-core/db"
	"github.com/iotexproject/iotex-core/db/batch"
	"github.com/iotexproject/iotex-core/db/trie"
	"github.com/iotexproject/iotex-core/pkg/util/byteutil"
	"github.com/iotexproject/iotex-core/state"
)

var (
	stateDBMtc = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "iotex_state_db",
			Help: "IoTeX State DB",
		},
		[]string{"type"},
	)
	dbBatchSizelMtc = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "iotex_db_batch_size",
			Help: "DB batch size",
		},
		[]string{},
	)
)

func init() {
	prometheus.MustRegister(stateDBMtc)
	prometheus.MustRegister(dbBatchSizelMtc)
}

type (
	// WorkingSet defines an interface for working set of states changes
	WorkingSet interface {
		protocol.StateManager
		// states and actions
		RunAction(context.Context, action.SealedEnvelope) (*action.Receipt, error)
		RunActions(context.Context, []action.SealedEnvelope) ([]*action.Receipt, error)
		Finalize() error
		Commit() error
		RootHash() ([]byte, error)
		Digest() (hash.Hash256, error)
		Version() uint64
	}

	// workingSet implements WorkingSet interface, tracks pending changes to account/contract in local cache
	workingSet struct {
		finalized   bool
		blockHeight uint64
		accountTrie trie.Trie      // global account state trie
		trieRoots   map[int][]byte // root of trie at time of snapshot
		flusher     db.KVStoreFlusher
	}
)

// newWorkingSet creates a new working set
func newWorkingSet(
	height uint64,
	kv db.KVStore,
	root []byte,
	opts ...db.KVStoreFlusherOption,
) (WorkingSet, error) {
	flusher, err := db.NewKVStoreFlusher(kv, batch.NewCachedBatch(), opts...)
	if err != nil {
		return nil, err
	}

	dbForTrie, err := db.NewKVStoreForTrie(AccountTrieNamespace, flusher.KVStoreWithBuffer())
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate state tire db")
	}
	tr, err := trie.NewTrie(trie.KVStoreOption(dbForTrie), trie.RootHashOption(root[:]))
	if err != nil {
		return nil, errors.Wrap(err, "failed to generate state trie from config")
	}

	return &workingSet{
		accountTrie: tr,
		finalized:   false,
		blockHeight: height,
		trieRoots:   make(map[int][]byte),
		flusher:     flusher,
	}, tr.Start(context.Background())
}

// RootHash returns the hash of the root node of the accountTrie
func (ws *workingSet) RootHash() ([]byte, error) {
	if !ws.finalized {
		return nil, errors.Errorf("working set has not been finalized")
	}
	return ws.accountTrie.RootHash(), nil
}

// Digest returns the delta state digest
func (ws *workingSet) Digest() (hash.Hash256, error) {
	if !ws.finalized {
		return hash.ZeroHash256, errors.New("workingset has not been finalized yet")
	}
	return hash.Hash256b(ws.flusher.SerializeQueue()), nil
}

// Version returns the Version of this working set
func (ws *workingSet) Version() uint64 {
	return ws.blockHeight
}

// Height returns the Height of the block being worked on
func (ws *workingSet) Height() (uint64, error) {
	return ws.blockHeight, nil
}

// RunActions runs actions in the block and track pending changes in working set
func (ws *workingSet) RunActions(
	ctx context.Context,
	elps []action.SealedEnvelope,
) ([]*action.Receipt, error) {
	// Handle actions
	receipts := make([]*action.Receipt, 0)
	for _, elp := range elps {
		receipt, err := ws.runAction(ctx, elp)
		if err != nil {
			return nil, errors.Wrap(err, "error when run action")
		}
		if receipt != nil {
			receipts = append(receipts, receipt)
		}
	}

	return receipts, nil
}

func (ws *workingSet) RunAction(
	ctx context.Context,
	elp action.SealedEnvelope,
) (*action.Receipt, error) {
	return ws.runAction(ctx, elp)
}

func (ws *workingSet) runAction(
	ctx context.Context,
	elp action.SealedEnvelope,
) (*action.Receipt, error) {
	if ws.finalized {
		return nil, errors.Errorf("cannot run action on a finalized working set")
	}
	// Handle action
	var actionCtx protocol.ActionCtx
	blkCtx := protocol.MustGetBlockCtx(ctx)
	bcCtx := protocol.MustGetBlockchainCtx(ctx)
	if blkCtx.BlockHeight != ws.blockHeight {
		return nil, errors.Errorf(
			"invalid block height %d, %d expected",
			blkCtx.BlockHeight,
			ws.blockHeight,
		)
	}
	caller, err := address.FromBytes(elp.SrcPubkey().Hash())
	if err != nil {
		return nil, err
	}
	actionCtx.Caller = caller
	actionCtx.ActionHash = elp.Hash()
	actionCtx.GasPrice = elp.GasPrice()
	intrinsicGas, err := elp.IntrinsicGas()
	if err != nil {
		return nil, err
	}
	actionCtx.IntrinsicGas = intrinsicGas
	actionCtx.Nonce = elp.Nonce()

	ctx = protocol.WithActionCtx(ctx, actionCtx)
	if bcCtx.Registry == nil {
		return nil, nil
	}
	for _, actionHandler := range bcCtx.Registry.All() {
		receipt, err := actionHandler.Handle(ctx, elp.Action(), ws)
		if err != nil {
			return nil, errors.Wrapf(
				err,
				"error when action %x (nonce: %d) from %s mutates states",
				elp.Hash(),
				elp.Nonce(),
				caller.String(),
			)
		}
		if receipt != nil {
			return receipt, nil
		}
	}
	return nil, nil
}

// Finalize runs action in the block and track pending changes in working set
func (ws *workingSet) Finalize() error {
	if ws.finalized {
		return errors.New("Cannot finalize a working set twice")
	}
	ws.finalized = true
	// Persist current chain Height
	h := byteutil.Uint64ToBytes(ws.blockHeight)
	ws.flusher.KVStoreWithBuffer().MustPut(AccountKVNamespace, []byte(CurrentHeightKey), h)
	// Persist accountTrie's root hash
	rootHash := ws.accountTrie.RootHash()
	ws.flusher.KVStoreWithBuffer().MustPut(AccountTrieNamespace, []byte(AccountTrieRootKey), rootHash)
	// Persist the historical accountTrie's root hash
	ws.flusher.KVStoreWithBuffer().MustPut(
		AccountTrieNamespace,
		[]byte(fmt.Sprintf("%s-%d", AccountTrieRootKey, ws.blockHeight)),
		rootHash,
	)

	return nil
}

func (ws *workingSet) Snapshot() int {
	s := ws.flusher.KVStoreWithBuffer().Snapshot()
	ws.trieRoots[s] = ws.accountTrie.RootHash()
	return s
}

func (ws *workingSet) Revert(snapshot int) error {
	if err := ws.flusher.KVStoreWithBuffer().Revert(snapshot); err != nil {
		return err
	}
	root, ok := ws.trieRoots[snapshot]
	if !ok {
		// this should not happen, b/c we save the trie root on a successful return of Snapshot(), but check anyway
		return errors.Wrapf(trie.ErrInvalidTrie, "failed to get trie root for snapshot = %d", snapshot)
	}
	return ws.accountTrie.SetRootHash(root[:])
}

// Commit persists all changes in RunActions() into the DB
func (ws *workingSet) Commit() error {
	// Commit all changes in a batch
	dbBatchSizelMtc.WithLabelValues().Set(float64(ws.flusher.KVStoreWithBuffer().Size()))
	if err := ws.flusher.Flush(); err != nil {
		return errors.Wrap(err, "failed to Commit all changes to underlying DB in a batch")
	}
	ws.clear()
	return nil
}

// GetDB returns the underlying DB for account/contract storage
func (ws *workingSet) GetDB() db.KVStore {
	return ws.flusher.KVStoreWithBuffer()
}

// State pulls a state from DB
func (ws *workingSet) State(s interface{}, opts ...protocol.StateOption) (uint64, error) {
	cfg, err := protocol.CreateStateConfig(opts...)
	if err != nil {
		return 0, err
	}
	if cfg.AtHeight {
		return 0, ErrNotSupported
	}

	stateDBMtc.WithLabelValues("get").Inc()
	mstate, err := ws.accountTrie.Get(cfg.Key)
	if errors.Cause(err) == trie.ErrNotExist {
		return 0, errors.Wrapf(state.ErrStateNotExist, "addrHash = %x", cfg.Key)
	}
	if err != nil {
		return 0, errors.Wrapf(err, "failed to get account of %x", cfg.Key)
	}
	return ws.blockHeight, state.Deserialize(s, mstate)
}

// PutState puts a state into DB
func (ws *workingSet) PutState(s interface{}, opts ...protocol.StateOption) (uint64, error) {
	stateDBMtc.WithLabelValues("put").Inc()
	cfg, err := protocol.CreateStateConfig(opts...)
	if err != nil {
		return 0, err
	}
	ss, err := state.Serialize(s)
	if err != nil {
		return 0, errors.Wrapf(err, "failed to convert account %v to bytes", s)
	}
	ws.flusher.KVStoreWithBuffer().MustPut(AccountKVNamespace, cfg.Key, ss)

	return ws.blockHeight, ws.accountTrie.Upsert(cfg.Key, ss)
}

// DelState deletes a state from DB
func (ws *workingSet) DelState(opts ...protocol.StateOption) (uint64, error) {
	cfg, err := protocol.CreateStateConfig(opts...)
	if err != nil {
		return 0, err
	}
	ws.flusher.KVStoreWithBuffer().MustDelete(AccountKVNamespace, cfg.Key)

	return ws.blockHeight, ws.accountTrie.Delete(cfg.Key)
}

// clearCache removes all local changes after committing to trie
func (ws *workingSet) clear() {
	ws.trieRoots = nil
	ws.trieRoots = make(map[int][]byte)
}
