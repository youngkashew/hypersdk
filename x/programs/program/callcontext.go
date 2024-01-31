package program

import (
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/hypersdk/codec"
)

type CallContext struct {
	ProgramID ids.ID
	Caller    codec.Address
	Gas       uint64
}
