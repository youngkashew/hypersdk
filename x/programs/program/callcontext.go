package program

import (
	"github.com/ava-labs/avalanchego/ids"
)

type CallContext struct {
	ProgramID ids.ID `json:"program,omitempty" yaml:"program,omitempty"`
}
