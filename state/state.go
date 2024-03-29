// Copyright (C) 2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package state

import (
	"context"
	"time"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/utils/maybe"
	"github.com/ava-labs/hypersdk/smap"
)

type Metrics interface {
	RecordTrieNodeChanges(int)
	RecordTrieValueChanges(int)
	RecordTrieSkippedValueChanges(int)
	RecordWaitTrie(time.Duration)
	RecordWaitRoot(time.Duration)
}

type Immutable interface {
	GetValue(ctx context.Context, key []byte) (value []byte, err error)
}

type Mutable interface {
	Immutable

	Insert(ctx context.Context, key []byte, value []byte) error
	Remove(ctx context.Context, key []byte) error
}

type Database interface {
	Mutable

	GetValues(ctx context.Context, keys [][]byte) (values [][]byte, errs []error)

	Update(ctx context.Context, ops *smap.SMap[maybe.Maybe[[]byte]]) int
	PrepareCommit(ctx context.Context) func(context.Context, Metrics) (ids.ID, error)
}
