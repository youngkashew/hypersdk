// Copyright (C) 2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package host

import (
	"fmt"
	"testing"

	"github.com/ava-labs/avalanchego/utils/logging"
	"github.com/ava-labs/hypersdk/x/programs/engine"
	"github.com/ava-labs/hypersdk/x/programs/program"
	"github.com/bytecodealliance/wasmtime-go/v14"
	"github.com/stretchr/testify/require"
)

func TestLinkMissingImport(t *testing.T) {
	require := require.New(t)

	wasm, err := wasmtime.Wat2Wasm(`
	(module
      (import "env" "alert" (func $alert (param i32)))
    )	
	`)
	require.NoError(err)
	eng := engine.New(engine.NewConfig())
	mod, err := eng.CompileModule(wasm)
	require.NoError(err)
	store := engine.NewStore(eng, engine.NewStoreConfig(10))
	link, err := newTestLink(store, NoSupportedImports)
	require.NoError(err)
	_, err = link.Instantiate(store, mod, nil)
	require.ErrorIs(err, ErrMissingImportModule)
}

func TestLinkImport(t *testing.T) {
	require := require.New(t)

	wasm, err := wasmtime.Wat2Wasm(`
	(module
      (import "env" "one" (func $one (param i64) (result i64)))
    )	
	`)
	require.NoError(err)

	tests := []struct {
		name,
		module,
		errMsg string
		fn interface{}
	}{
		{
			name:   "happy path",
			module: "env",
			fn:     func(int64) int64 { return 0 },
		},
		{
			name:   "missing module",
			module: "oops",
			fn:     func() {},
			errMsg: "failed to find import module: env",
		},
		{
			name:   "invalid module function signature",
			module: "env",
			fn:     func(int64) int32 { return 0 },
			errMsg: "function types incompatible",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			imports := NewImportsBuilder()
			imports.Register(tt.module, func() Import {
				return newTestImport(tt.module, "CustomWrap", tt.fn)
			})
			eng := engine.New(engine.NewConfig())
			mod, err := eng.CompileModule(wasm)
			require.NoError(err)
			store := engine.NewStore(eng, engine.NewStoreConfig(10))
			require.NoError(err)
			link, err := newTestLink(store, imports.Build())
			require.NoError(err)
			_, err = link.Instantiate(store, mod, nil)
			if tt.errMsg != "" {
				require.ErrorContains(err, tt.errMsg) // can't use ErrorIs because the error message is not owned by us.
				return
			}
			require.NoError(err)
		})
	}

}

// go test -v -benchmem -run=^$ -bench ^BenchmarkInstantiate$ github.com/ava-labs/hypersdk/x/programs/host -memprofile benchvset.mem -cpuprofile benchvset.cpu
func BenchmarkInstantiate(b *testing.B) {
	require := require.New(b)
	imports := NewImportsBuilder()
	imports.Register("env", func() Import {
		return newTestImport("env", "Wrap", nil)
	})
	wasm, err := wasmtime.Wat2Wasm(`
	(module
	  (import "env" "one" (func $one (param i64) (result i64)))
	  (import "env" "two" (func $two (param i64) (param i64) (result i64)))
	)	
	`)
	require.NoError(err)
	eng := engine.New(engine.NewConfig())
	require.NoError(err)
	mod, err := eng.CompileModule(wasm)
	require.NoError(err)
	b.Run("benchmark_funcWrap", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			store := engine.NewStore(eng, engine.NewStoreConfig(10))
			link, err := newTestLink(store, imports.Build())
			require.NoError(err)
			_, err = link.Instantiate(store, mod, nil)
			require.NoError(err)
		}
	})
	imports = NewImportsBuilder()
	imports.Register("env", func() Import {
		return newTestImport("env", "Int64Fn", nil)
	})
	b.Run("benchmark_funcInt64", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			store := engine.NewStore(eng, engine.NewStoreConfig(10))
			link, err := newTestLink(store, imports.Build())
			require.NoError(err)
			_, err = link.Instantiate(store, mod, nil)
			require.NoError(err)
		}
	})
}

func newTestLink(store *engine.Store, supported SupportedImports) (*Link, error) {
	meter, err := program.NewMeter(store, program.NoUnits)
	if err != nil {
		return nil, err
	}
	return NewLink(logging.NoLog{}, store.Engine(), supported, meter, false), nil
}

type testImport struct {
	module   string
	linkType string
	fn       interface{}
}

func newTestImport(module, linkType string, fn interface{}) *testImport {
	return &testImport{
		module:   module,
		linkType: linkType,
		fn:       fn,
	}
}

func (i *testImport) Name() string {
	return i.module
}

func (i *testImport) Register(link *Link) error {
	switch i.linkType {
	case "Int64Fn":
		if err := link.RegisterOneParamInt64Fn(i.module, "one", testOneParamFn); err != nil {
			return err
		}
		if err := link.RegisterTwoParamInt64Fn(i.module, "two", testTwoParamFn); err != nil {
			return err
		}
	case "Wrap":
		if err := link.RegisterFuncWrap(i.module, "one", testOneParamFnWrap); err != nil {
			return err
		}
		if err := link.RegisterFuncWrap(i.module, "two", testTwoParamFnWrap); err != nil {
			return err
		}
	case "CustomWrap":
		if err := link.RegisterFuncWrap(i.module, "one", i.fn); err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid link type: %s", i.linkType)
	}

	return nil
}

func testOneParamFn(caller *program.Caller, p1 int64) (*program.Val, error) {
	return nil, nil
}

func testTwoParamFn(caller *program.Caller, p1, p2 int64) (*program.Val, error) {
	return nil, nil
}

func testOneParamFnWrap(caller *wasmtime.Caller, p1 int64) int64 {
	return 0
}

func testTwoParamFnWrap(caller *wasmtime.Caller, p1, p2 int64) int64 {
	return 0
}
