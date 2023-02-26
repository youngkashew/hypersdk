// Copyright (C) 2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package chain

import (
	"github.com/ava-labs/hypersdk/codec"
	"github.com/ava-labs/hypersdk/consts"
)

type Result struct {
	Success     bool
	Units       uint64
	Output      []byte
	WarpMessage []byte
}

func (r *Result) Marshal(p *codec.Packer) {
	p.PackBool(r.Success)
	p.PackUint64(r.Units)
	p.PackBytes(r.Output)
	p.PackBytes(r.WarpMessage)
}

func MarshalResults(src []*Result) ([]byte, error) {
	p := codec.NewWriter(consts.MaxInt) // could be much larger than [NetworkSizeLimit]
	p.PackInt(len(src))
	for _, result := range src {
		result.Marshal(p)
	}
	return p.Bytes(), p.Err()
}

func UnmarshalResult(p *codec.Packer) (*Result, error) {
	result := &Result{
		Success: p.UnpackBool(),
		Units:   p.UnpackUint64(false),
	}
	p.UnpackBytes(consts.MaxInt, false, &result.Output)
	if len(result.Output) == 0 {
		// Enforce object standardization
		result.Output = nil
	}
	p.UnpackBytes(MaxWarpMessageSize, false, &result.WarpMessage)
	if len(result.WarpMessage) == 0 {
		// Enforce object standardization
		result.WarpMessage = nil
	}
	return result, p.Err()
}

func UnmarshalResults(src []byte) ([]*Result, error) {
	p := codec.NewReader(src, consts.MaxInt) // could be much larger than [NetworkSizeLimit]
	items := p.UnpackInt(false)
	results := make([]*Result, items)
	for i := 0; i < items; i++ {
		result, err := UnmarshalResult(p)
		if err != nil {
			return nil, err
		}
		results[i] = result
	}
	if !p.Empty() {
		return nil, ErrInvalidObject
	}
	return results, nil
}
