// Copyright (C) 2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package chain

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ava-labs/avalanchego/database"
	smblock "github.com/ava-labs/avalanchego/snow/engine/snowman/block"
	"github.com/ava-labs/avalanchego/utils/logging"
	"github.com/ava-labs/avalanchego/utils/math"
	"github.com/ava-labs/avalanchego/utils/set"
	"go.opentelemetry.io/otel/attribute"
	"go.uber.org/zap"

	"github.com/ava-labs/hypersdk/keys"
	"github.com/ava-labs/hypersdk/tstate"
)

const (
	maxViewPreallocation = 10_000

	// TODO: make these tunable
	streamBatch             = 256
	streamPrefetchThreshold = streamBatch / 2
	stopBuildingThreshold   = 2_048 // units
)

var errBlockFull = errors.New("block full")

func HandlePreExecute(log logging.Logger, err error) bool {
	switch {
	case errors.Is(err, ErrInsufficientPrice):
		return false
	case errors.Is(err, ErrTimestampTooEarly):
		return true
	case errors.Is(err, ErrTimestampTooLate):
		return false
	case errors.Is(err, ErrInvalidBalance):
		return false
	case errors.Is(err, ErrAuthNotActivated):
		return false
	case errors.Is(err, ErrAuthFailed):
		return false
	case errors.Is(err, ErrActionNotActivated):
		return false
	default:
		// If unknown error, drop
		log.Warn("unknown PreExecute error", zap.Error(err))
		return false
	}
}

func BuildBlock(
	ctx context.Context,
	vm VM,
	parent *StatelessBlock,
	blockContext *smblock.Context,
) (*StatelessBlock, error) {
	ctx, span := vm.Tracer().Start(ctx, "chain.BuildBlock")
	defer span.End()
	log := vm.Logger()

	// We don't need to fetch the [VerifyContext] because
	// we will always have a block to build on.

	// Select next timestamp
	nextTime := time.Now().UnixMilli()
	r := vm.Rules(nextTime)
	if nextTime < parent.Tmstmp+r.GetMinBlockGap() {
		log.Warn("block building failed", zap.Error(ErrTimestampTooEarly))
		return nil, ErrTimestampTooEarly
	}
	b := NewBlock(vm, parent, nextTime)

	// Fetch view where we will apply block state transitions
	//
	// If the parent block is not yet verified, we will attempt to
	// execute it.
	mempoolSize := vm.Mempool().Len(ctx)
	changesEstimate := math.Min(mempoolSize, maxViewPreallocation)
	parentView, err := parent.View(ctx, true)
	if err != nil {
		log.Warn("block building failed: couldn't get parent db", zap.Error(err))
		return nil, err
	}

	// Compute next unit prices to use
	feeKey := FeeKey(vm.StateManager().FeeKey())
	feeRaw, err := parentView.GetValue(ctx, feeKey)
	if err != nil {
		return nil, err
	}
	parentFeeManager := NewFeeManager(feeRaw)
	feeManager, err := parentFeeManager.ComputeNext(parent.Tmstmp, nextTime, r)
	if err != nil {
		return nil, err
	}
	maxUnits := r.GetMaxBlockUnits()
	targetUnits := r.GetWindowTargetUnits()

	ts := tstate.New(changesEstimate, vm.GetPrefetchPathBatch())
	state, err := vm.State()
	if err != nil {
		return nil, err
	}
	var (
		oldestAllowed = nextTime - r.GetValidityWindow()

		mempool = vm.Mempool()

		txsAttempted = 0
		results      = []*Result{}
		warpCount    = 0

		vdrState = vm.ValidatorState()
		sm       = vm.StateManager()

		start = time.Now()

		// restorable txs after block attempt finishes
		restorable = []*Transaction{}

		// alreadyFetched contains keys already fetched from state that can be
		// used during prefetching.
		alreadyFetched = map[string]*fetchData{}

		// prepareStreamLock ensures we don't overwrite stream prefetching spawned
		// asynchronously.
		prepareStreamLock sync.Mutex
	)

	// Batch fetch items from mempool to unblock incoming RPC/Gossip traffic
	mempool.StartStreaming(ctx)
	b.Txs = []*Transaction{}
	usedKeys := set.NewSet[string](0) // prefetch map for transactions in block
	for time.Since(start) < vm.GetTargetBuildDuration() {
		prepareStreamLock.Lock()
		txs := mempool.Stream(ctx, streamBatch)
		prepareStreamLock.Unlock()
		if len(txs) == 0 {
			b.vm.RecordClearedMempool()
			break
		}

		// Prefetch all transactions
		//
		// TODO: unify logic with https://github.com/ava-labs/hypersdk/blob/4e10b911c3cd88e0ccd8d9de5210515b1d3a3ac4/chain/processor.go#L44-L79
		var (
			readyTxs  = make(chan *txData, len(txs))
			stopIndex = -1
			execErr   error
		)
		go func() {
			ctx, prefetchKeysSpan := vm.Tracer().Start(ctx, "chain.BuildBlock.PrefetchKeys")
			defer prefetchKeysSpan.End()
			defer close(readyTxs)

			for i, tx := range txs {
				if execErr != nil {
					stopIndex = i
					return
				}

				// Once we get part way through a prefetching job, we start
				// to prepare for the next stream.
				if i == streamPrefetchThreshold {
					prepareStreamLock.Lock()
					go func() {
						mempool.PrepareStream(ctx, streamBatch)
						prepareStreamLock.Unlock()
					}()
				}

				// Prefetch all values from state
				storage := map[string][]byte{}
				stateKeys, err := tx.StateKeys(sm)
				if err != nil {
					// Drop bad transaction and continue
					//
					// This should not happen because we check this before
					// adding a transaction to the mempool.
					continue
				}
				for k := range stateKeys {
					if v, ok := alreadyFetched[k]; ok {
						if v.exists {
							storage[k] = v.v
						}
						continue
					}
					v, err := parentView.GetValue(ctx, []byte(k))
					if errors.Is(err, database.ErrNotFound) {
						alreadyFetched[k] = &fetchData{nil, false, 0}
						continue
					} else if err != nil {
						// This can happen if the underlying view changes (if we are
						// verifying a block that can never be accepted).
						execErr = err
						stopIndex = i
						return
					}
					numChunks, ok := keys.NumChunks(v)
					if !ok {
						// Drop bad transaction and continue
						//
						// This should not happen because we check this before
						// adding a transaction to the mempool.
						continue
					}
					alreadyFetched[k] = &fetchData{v, true, numChunks}
					storage[k] = v
				}
				readyTxs <- &txData{tx, storage, nil, nil}
			}
		}()

		// Perform a batch repeat check while we are waiting for state prefetching
		dup, err := parent.IsRepeat(ctx, oldestAllowed, txs, set.NewBits(), false)
		if err != nil {
			execErr = err
		}

		// Execute transactions as they become ready
		ctx, executeSpan := vm.Tracer().Start(ctx, "chain.BuildBlock.Execute")
		txIndex := 0
		for nextTxData := range readyTxs {
			txsAttempted++
			next := nextTxData.tx
			if execErr != nil {
				restorable = append(restorable, next)
				continue
			}

			// Skip if tx is a duplicate
			if dup.Contains(txIndex) {
				continue
			}
			txIndex++

			// Ensure we can process if transaction includes a warp message
			if next.WarpMessage != nil && blockContext == nil {
				log.Info(
					"dropping pending warp message because no context provided",
					zap.Stringer("txID", next.ID()),
				)
				restorable = append(restorable, next)
				continue
			}

			// Skip warp message if at max
			if next.WarpMessage != nil && warpCount == MaxWarpMessages {
				log.Info(
					"dropping pending warp message because already have MaxWarpMessages",
					zap.Stringer("txID", next.ID()),
				)
				restorable = append(restorable, next)
				continue
			}

			// Ensure we have room
			nextUnits, err := next.MaxUnits(sm, r)
			if err != nil {
				// Should never happen
				log.Warn(
					"skipping tx: invalid max units",
					zap.Error(err),
				)
				continue
			}
			if ok, dimension := feeManager.CanConsume(nextUnits, maxUnits); !ok {
				log.Debug(
					"skipping tx: too many units",
					zap.Int("dimension", int(dimension)),
					zap.Uint64("tx", nextUnits[dimension]),
					zap.Uint64("block units", feeManager.LastConsumed(dimension)),
					zap.Uint64("max block units", maxUnits[dimension]),
				)
				restorable = append(restorable, next)

				// If we are above the target for the dimension we can't consume, we will
				// stop building. This prevents a full mempool iteration looking for the
				// "perfect fit".
				if feeManager.LastConsumed(dimension) >= targetUnits[dimension] {
					execErr = errBlockFull
				}
				continue
			}

			// Populate required transaction state and restrict which keys can be used
			txStart := ts.OpIndex()
			stateKeys, err := next.StateKeys(sm)
			if err != nil {
				// This should not happen because we check this before
				// adding a transaction to the mempool.
				log.Warn(
					"skipping tx: invalid stateKeys",
					zap.Error(err),
				)
				continue
			}
			ts.SetScope(ctx, stateKeys, nextTxData.storage)

			// PreExecute next to see if it is fit
			authCUs, err := next.PreExecute(ctx, feeManager, sm, r, ts, nextTime)
			if err != nil {
				ts.Rollback(ctx, txStart)
				if HandlePreExecute(log, err) {
					restorable = append(restorable, next)
				}
				continue
			}

			// Verify warp message, if it exists
			//
			// We don't drop invalid warp messages because we must collect fees for
			// the work the sender made us do (otherwise this would be a DoS).
			//
			// We wait as long as possible to verify the signature to ensure we don't
			// spend unnecessary time on an invalid tx.
			var warpErr error
			if next.WarpMessage != nil {
				// We do not check the validity of [SourceChainID] because a VM could send
				// itself a message to trigger a chain upgrade.
				allowed, num, denom := r.GetWarpConfig(next.WarpMessage.SourceChainID)
				if allowed {
					warpErr = next.WarpMessage.Signature.Verify(
						ctx, &next.WarpMessage.UnsignedMessage, r.NetworkID(),
						vdrState, blockContext.PChainHeight, num, denom,
					)
				} else {
					warpErr = ErrDisabledChainID
				}
				if warpErr != nil {
					log.Warn(
						"warp verification failed",
						zap.Stringer("txID", next.ID()),
						zap.Error(warpErr),
					)
				}
			}

			// If execution works, keep moving forward with new state
			//
			// Note, these calculations must match block verification exactly
			// otherwise they will produce a different state root.
			coldReads := map[string]uint16{}
			warmReads := map[string]uint16{}
			var invalidStateKeys bool
			for k := range stateKeys {
				v := nextTxData.storage[k]
				numChunks, ok := keys.NumChunks(v)
				if !ok {
					invalidStateKeys = true
					break
				}
				if usedKeys.Contains(k) {
					warmReads[k] = numChunks
					continue
				}
				coldReads[k] = numChunks
			}
			if invalidStateKeys {
				// This should not happen because we check this before
				// adding a transaction to the mempool.
				log.Warn("invalid tx: invalid state keys")
				continue
			}
			result, err := next.Execute(
				ctx,
				feeManager,
				authCUs,
				coldReads,
				warmReads,
				sm,
				r,
				ts,
				nextTime,
				next.WarpMessage != nil && warpErr == nil,
			)
			if err != nil {
				// Returning an error here should be avoided at all costs (can be a DoS). Rather,
				// all units for the transaction should be consumed and a fee should be charged.
				log.Warn("unexpected post-execution error", zap.Error(err))
				restorable = append(restorable, next)
				execErr = err
				continue
			}

			// Update block with new transaction
			b.Txs = append(b.Txs, next)
			usedKeys.Add(stateKeys.List()...)
			if err := feeManager.Consume(result.Consumed); err != nil {
				execErr = err
				continue
			}
			results = append(results, result)
			if next.WarpMessage != nil {
				if warpErr == nil {
					// Add a bit if the warp message was verified
					b.WarpResults.Add(uint(warpCount))
				}
				warpCount++
			}

			// Prefetch path of modified keys
			if modifiedKeys := ts.FlushModifiedKeys(false); len(modifiedKeys) > 0 {
				pctx, prefetchPathsSpan := vm.Tracer().Start(ctx, "chain.BuildBlock.PrefetchPaths")
				prefetchPathsSpan.SetAttributes(
					attribute.Int("keys", len(modifiedKeys)),
					attribute.Bool("force", false),
				)
				go func() {
					defer prefetchPathsSpan.End()

					// It is ok if these do not finish by the time root generation begins...
					//
					// If the paths of all keys are already in memory, this is a no-op.
					if err := state.PrefetchPaths(pctx, modifiedKeys); err != nil {
						vm.Logger().Warn("unable to prefetch paths", zap.Error(err))
					}
				}()
			}
		}
		executeSpan.End()

		// Handle execution result
		if execErr != nil {
			if stopIndex >= 0 {
				// If we stopped prefetching, make sure to add those txs back
				restorable = append(restorable, txs[stopIndex:]...)
			}
			if !errors.Is(execErr, errBlockFull) {
				// Wait for stream preparation to finish to make
				// sure all transactions are returned to the mempool.
				go func() {
					prepareStreamLock.Lock() // we never need to unlock this as it will not be used after this
					restored := mempool.FinishStreaming(ctx, append(b.Txs, restorable...))
					b.vm.Logger().Debug("transactions restored to mempool", zap.Int("count", restored))
				}()
				b.vm.Logger().Warn("build failed", zap.Error(execErr))
				return nil, execErr
			}

			// Prefetch path of modified keys
			if modifiedKeys := ts.FlushModifiedKeys(true); len(modifiedKeys) > 0 {
				pctx, prefetchPathsSpan := vm.Tracer().Start(ctx, "chain.BuildBlock.PrefetchPaths")
				prefetchPathsSpan.SetAttributes(
					attribute.Int("keys", len(modifiedKeys)),
					attribute.Bool("force", true),
				)
				go func() {
					defer prefetchPathsSpan.End()

					// It is ok if these do not finish by the time root generation begins...
					//
					// If the paths of all keys are already in memory, this is a no-op.
					if err := state.PrefetchPaths(pctx, modifiedKeys); err != nil {
						vm.Logger().Warn("unable to prefetch paths", zap.Error(err))
					}
				}()
			}
			break
		}
	}

	// Wait for stream preparation to finish to make
	// sure all transactions are returned to the mempool.
	go func() {
		prepareStreamLock.Lock()
		restored := mempool.FinishStreaming(ctx, restorable)
		b.vm.Logger().Debug("transactions restored to mempool", zap.Int("count", restored))
	}()

	// Update tracking metrics
	span.SetAttributes(
		attribute.Int("attempted", txsAttempted),
		attribute.Int("added", len(b.Txs)),
	)
	if time.Since(start) > b.vm.GetTargetBuildDuration() {
		b.vm.RecordBuildCapped()
	}

	// Perform basic validity checks to make sure the block is well-formatted
	if len(b.Txs) == 0 {
		if nextTime < parent.Tmstmp+r.GetMinEmptyBlockGap() {
			return nil, fmt.Errorf("%w: allowed in %d ms", ErrNoTxs, parent.Tmstmp+r.GetMinEmptyBlockGap()-nextTime)
		}
		vm.RecordEmptyBlockBuilt()
	}

	// Update chain metadata
	heightKey := HeightKey(sm.HeightKey())
	heightKeyStr := string(heightKey)
	timestampKey := TimestampKey(b.vm.StateManager().TimestampKey())
	timestampKeyStr := string(timestampKey)
	feeKeyStr := string(feeKey)
	ts.SetScope(ctx, set.Of(heightKeyStr, timestampKeyStr, feeKeyStr), map[string][]byte{
		heightKeyStr:    binary.BigEndian.AppendUint64(nil, parent.Hght),
		timestampKeyStr: binary.BigEndian.AppendUint64(nil, uint64(parent.Tmstmp)),
		feeKeyStr:       parentFeeManager.Bytes(),
	})
	if err := ts.Insert(ctx, heightKey, binary.BigEndian.AppendUint64(nil, b.Hght)); err != nil {
		return nil, fmt.Errorf("%w: unable to insert height", err)
	}
	if err := ts.Insert(ctx, timestampKey, binary.BigEndian.AppendUint64(nil, uint64(b.Tmstmp))); err != nil {
		return nil, fmt.Errorf("%w: unable to insert timestamp", err)
	}
	if err := ts.Insert(ctx, feeKey, feeManager.Bytes()); err != nil {
		return nil, fmt.Errorf("%w: unable to insert fees", err)
	}

	// Fetch [parentView] root as late as possible to allow
	// for async processing to complete
	root, err := parentView.GetMerkleRoot(ctx)
	if err != nil {
		return nil, err
	}
	b.StateRoot = root

	// Get view from [tstate] after writing all changed keys
	view, err := ts.CreateView(ctx, parentView, vm.Tracer())
	if err != nil {
		return nil, err
	}

	// Compute block hash and marshaled representation
	if err := b.initializeBuilt(ctx, view, results, feeManager); err != nil {
		return nil, err
	}

	// Kickoff root generation
	go func() {
		start := time.Now()
		root, err := view.GetMerkleRoot(ctx)
		if err != nil {
			log.Error("merkle root generation failed", zap.Error(err))
			return
		}
		log.Info("merkle root generated",
			zap.Uint64("height", b.Hght),
			zap.Stringer("blkID", b.ID()),
			zap.Stringer("root", root),
		)
		b.vm.RecordRootCalculated(time.Since(start))
	}()

	log.Info(
		"built block",
		zap.Bool("context", blockContext != nil),
		zap.Uint64("hght", b.Hght),
		zap.Int("attempted", txsAttempted),
		zap.Int("added", len(b.Txs)),
		zap.Int("state changes", ts.PendingChanges()),
		zap.Int("state operations", ts.OpIndex()),
		zap.Int64("parent (t)", parent.Tmstmp),
		zap.Int64("block (t)", b.Tmstmp),
	)
	return b, nil
}
