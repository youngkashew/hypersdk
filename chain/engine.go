package chain

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"time"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/utils/set"
	"github.com/ava-labs/hypersdk/state"
	"go.uber.org/zap"
)

type engineJob struct {
	parentTimestamp int64
	blk             *StatelessBlock
	chunks          chan *Chunk
}

type output struct {
	view state.View

	startRoot ids.ID
	chunks    []*FilteredChunk
}

// Engine is in charge of orchestrating the execution of
// accepted chunks.
//
// TODO: put in VM?
type Engine struct {
	vm VM

	backlog chan *engineJob

	outputsLock sync.RWMutex
	outputs     map[uint64]*output
}

func NewEngine(vm VM, maxBacklog int) *Engine {
	return &Engine{
		vm: vm,

		backlog: make(chan *engineJob, maxBacklog),

		outputs: make(map[uint64]*output),
	}
}

func (e *Engine) Run(ctx context.Context) {
	log := e.vm.Logger()

	// Get last accepted state
	var parentView state.View
	view, err := e.vm.State()
	if err != nil {
		panic(err)
	}
	parentView = view

	for {
		select {
		case job := <-e.backlog:
			r := e.vm.Rules(job.blk.StatefulBlock.Timestamp)

			// Fetch parent height key and ensure block height is valid
			heightKey := HeightKey(e.vm.StateManager().HeightKey())
			parentHeightRaw, err := parentView.GetValue(ctx, heightKey)
			if err != nil {
				panic(err)
			}
			parentHeight := binary.BigEndian.Uint64(parentHeightRaw)
			if job.blk.Height() != parentHeight+1 {
				panic(ErrInvalidBlockHeight)
			}

			// Compute fees
			feeKey := FeeKey(e.vm.StateManager().FeeKey())
			feeRaw, err := parentView.GetValue(ctx, feeKey)
			if err != nil {
				panic(err)
			}
			parentFeeManager := NewFeeManager(feeRaw)
			feeManager, err := parentFeeManager.ComputeNext(job.parentTimestamp, job.blk.StatefulBlock.Timestamp, r)
			if err != nil {
				panic(err)
			}

			// Process chunks
			p := NewProcessor(e.vm, job.blk.bctx, nil, len(job.blk.AvailableChunks), job.blk.StatefulBlock.Timestamp, parentView, feeManager, r)
			chunks := make([]*Chunk, 0, len(job.blk.AvailableChunks))
			for chunk := range job.chunks {
				if err := p.Add(ctx, len(chunks), chunk); err != nil {
					panic(err)
				}
				chunks[len(chunks)] = chunk
			}
			ts, chunkResults, err := p.Wait()
			if err != nil {
				panic(err)
			}

			// Create FilteredChunks
			filteredChunks := make([]*FilteredChunk, len(chunkResults))
			for i, chunkResult := range chunkResults {
				var (
					chunk = chunks[i]
					cert  = job.blk.AvailableChunks[i]
					txs   = make([]*Transaction, 0, len(chunkResult))

					warpResults set.Bits64
					warpCount   uint
				)
				for j, txResult := range chunkResult {
					if !txResult.Valid {
						continue
					}
					tx := chunk.Txs[j]
					txs = append(txs, tx)
					if tx.WarpMessage != nil {
						if txResult.WarpVerified {
							warpResults.Add(warpCount)
						}
						warpCount++
					}
				}
				filteredChunks[i] = &FilteredChunk{
					Chunk:    cert.Chunk,
					Producer: cert.Producer,

					Txs:         txs,
					WarpResults: warpResults,
				}
			}

			// Update chain metadata
			heightKeyStr := string(heightKey)
			feeKeyStr := string(feeKey)
			keys := make(state.Keys)
			keys.Add(heightKeyStr, state.Write)
			keys.Add(feeKeyStr, state.Write)
			tsv := ts.NewView(keys, map[string][]byte{
				heightKeyStr: parentHeightRaw,
				feeKeyStr:    parentFeeManager.Bytes(),
			})
			if err := tsv.Insert(ctx, heightKey, binary.BigEndian.AppendUint64(nil, job.blk.StatefulBlock.Height)); err != nil {
				panic(err)
			}
			if err := tsv.Insert(ctx, feeKey, feeManager.Bytes()); err != nil {
				panic(err)
			}
			tsv.Commit()

			// Get start root
			startRoot, err := parentView.GetMerkleRoot(ctx)
			if err != nil {
				panic(err)
			}

			// Create new view and kickoff generation
			view, err := ts.ExportMerkleDBView(ctx, e.vm.Tracer(), parentView)
			if err != nil {
				panic(err)
			}
			go func() {
				start := time.Now()
				root, err := view.GetMerkleRoot(ctx)
				if err != nil {
					log.Error("merkle root generation failed", zap.Error(err))
					return
				}
				log.Info("merkle root generated",
					zap.Uint64("height", job.blk.StatefulBlock.Height),
					zap.Stringer("blkID", job.blk.ID()),
					zap.Stringer("root", root),
					zap.Duration("t", time.Since(start)),
				)
			}()

			// Store and update parent view
			e.outputsLock.Lock()
			e.outputs[job.blk.StatefulBlock.Height] = &output{
				view:      view,
				startRoot: startRoot,
				chunks:    filteredChunks,
			}
			e.outputsLock.Unlock()
			parentView = view

			// TODO: persist filtered chunks we finish processing/clear old raw chunks
		case <-ctx.Done():
			return
		}
	}
}

func (e *Engine) Execute(blk *StatelessBlock) {
	// TODO: fetch chunks that don't exist (before start run) -> use a channel for the chunks so can start execution
	chunks := make(chan *Chunk, len(blk.AvailableChunks))

	// Enqueue job
	e.backlog <- &engineJob{
		blk:    blk,
		chunks: chunks,
	}
}

func (e *Engine) Results(height uint64) (ids.ID /* StartRoot */, []ids.ID /* Executed Chunks */, error) {
	// TODO: handle case where never started execution (state sync)
	return ids.ID{}, nil, errors.New("implement me")
}

func (e *Engine) Clear(height uint64) {
	// TODO: clear old tracking as soon as done
}