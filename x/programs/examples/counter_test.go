// Copyright (C) 2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package examples

import (
	"context"
	_ "embed"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/hypersdk/codec"
	"github.com/ava-labs/hypersdk/x/programs/examples/imports/program"
	"github.com/ava-labs/hypersdk/x/programs/examples/imports/pstate"
	"github.com/ava-labs/hypersdk/x/programs/examples/storage"
	"github.com/ava-labs/hypersdk/x/programs/runtime"
)

//go:embed testdata/counter.wasm
var counterProgramBytes []byte

// go test -v -timeout 30s -run ^TestCounterProgram$ github.com/ava-labs/hypersdk/x/programs/examples
func TestCounterProgram(t *testing.T) {
	require := require.New(t)
	db := newTestDB()
	maxUnits := uint64(80000)
	maxUnits := uint64(80000)
	cfg, err := runtime.NewConfigBuilder().Build()
	require.NoError(err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// define supported imports
	supported := runtime.NewSupportedImports()
	supported.Register("state", func() runtime.Import {
		return pstate.New(log, db)
	})
	supported.Register("program", func() runtime.Import {
		return program.New(log, db, cfg)
	})

	rt := runtime.New(log, cfg, supported.Imports())
	err = rt.Initialize(ctx, counterProgramBytes, maxUnits)
	require.NoError(err)

	require.Equal(maxUnits, rt.Meter().GetBalance())

	// simulate create program transaction
	programAddress := codec.CreateAddress(programTypeID, ids.GenerateTestID())
	err = storage.SetProgram(ctx, db, programAddress, counterProgramBytes)
	require.NoError(err)

	programAddressPtr, err := argumentToSmartPtr(programAddress, rt.Memory())
	require.NoError(err)

	// generate alice address
	_, aliceAddress, err := newTestAddress()
	require.NoError(err)

	// write alice's address to stack and get pointer
	alicePtr, err := argumentToSmartPtr(aliceAddress, rt.Memory())
	require.NoError(err)

	// create counter for alice on program 1
	result, err := rt.Call(ctx, "initialize_address", programAddressPtr, alicePtr)
	require.NoError(err)
	require.Equal(int64(1), result[0])

	// validate counter at 0
	result, err = rt.Call(ctx, "get_value", programAddressPtr, alicePtr)
	require.NoError(err)
	require.Equal(int64(0), result[0])

	// initialize second runtime to create second counter program with an empty
	// meter.
	rt2 := runtime.New(log, cfg, supported.Imports())
	err = rt2.Initialize(ctx, counterProgramBytes, runtime.NoUnits)


	require.NoError(err)

	// define max units to transfer to second runtime
	unitsTransfer := uint64(20000)

	// transfer the units from the original runtime to the new runtime before
	// any calls are made.
	_, err = rt.Meter().TransferUnitsTo(rt2.Meter(), unitsTransfer)
	require.NoError(err)

	// simulate creating second program transaction
	program2Address := codec.CreateAddress(programTypeID, ids.GenerateTestID())
	err = storage.SetProgram(ctx, db, program2Address, counterProgramBytes)
	require.NoError(err)

	programAddress2Ptr, err := argumentToSmartPtr(program2Address, rt2.Memory())
	require.NoError(err)

	// write alice's address to stack and get pointer
	alicePtr2, err := argumentToSmartPtr(aliceAddress, rt2.Memory())
	require.NoError(err)

	// initialize counter for alice on runtime 2
	result, err = rt2.Call(ctx, "initialize_address", programAddress2Ptr, alicePtr2)
	require.NoError(err)
	require.Equal(int64(1), result[0])

	// increment alice's counter on program 2 by 10
	incAmount := int64(10)
	incAmountPtr, err := argumentToSmartPtr(incAmount, rt2.Memory())
	require.NoError(err)
	result, err = rt2.Call(ctx, "inc", programAddress2Ptr, alicePtr2, incAmountPtr)
	require.NoError(err)
	require.Equal(int64(1), result[0])

	result, err = rt2.Call(ctx, "get_value", programAddress2Ptr, alicePtr2)
	require.NoError(err)
	require.Equal(incAmount, result[0])

	// stop the runtime to prevent further execution
	rt2.Stop()

	// transfer balance back to original runtime
	_, err = rt2.Meter().TransferUnitsTo(rt.Meter(), rt2.Meter().GetBalance())
	if err != nil {
		log.Error("failed to transfer remaining balance to caller",
			zap.Error(err),
		)
	}

	// increment alice's counter on program 1
	onePtr, err := argumentToSmartPtr(int64(1), rt.Memory())
	require.NoError(err)
	result, err = rt.Call(ctx, "inc", programAddressPtr, alicePtr, onePtr)
	require.NoError(err)
	require.Equal(int64(1), result[0])

	result, err = rt.Call(ctx, "get_value", programAddressPtr, alicePtr)
	require.NoError(err)

	log.Debug("count program 1",
		zap.Int64("alice", result[0]),
	)

	// write program address 2 to stack of program 1
	programAddress2Ptr, err = argumentToSmartPtr(program2Address, rt.Memory())
	require.NoError(err)

	caller := programAddressPtr
	target := programAddress2Ptr
	maxUnitsProgramToProgram := int64(10000)
	maxUnitsProgramToProgramPtr, err := argumentToSmartPtr(maxUnitsProgramToProgram, rt.Memory())
	require.NoError(err)
	maxUnitsProgramToProgramPtr, err := argumentToSmartPtr(maxUnitsProgramToProgram, rt.Memory())
	require.NoError(err)

	// increment alice's counter on program 2
	fivePtr, err := argumentToSmartPtr(int64(5), rt.Memory())
	require.NoError(err)
	result, err = rt.Call(ctx, "inc_external", caller, target, maxUnitsProgramToProgramPtr, alicePtr, fivePtr)
	fivePtr, err := argumentToSmartPtr(int64(5), rt.Memory())
	require.NoError(err)
	result, err = rt.Call(ctx, "inc_external", caller, target, maxUnitsProgramToProgramPtr, alicePtr, fivePtr)
	require.NoError(err)
	require.Equal(int64(1), result[0])

	// expect alice's counter on program 2 to be 15
	result, err = rt.Call(ctx, "get_value_external", caller, target, maxUnitsProgramToProgramPtr, alicePtr)
	result, err = rt.Call(ctx, "get_value_external", caller, target, maxUnitsProgramToProgramPtr, alicePtr)
	require.NoError(err)
	require.Equal(int64(15), result[0])
	require.Greater(rt.Meter().GetBalance(), uint64(0))
}
