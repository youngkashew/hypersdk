// Copyright (C) 2023, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package rpc

import (
	"context"
	"strings"

	"github.com/ava-labs/avalanchego/ids"

	"github.com/ava-labs/hypersdk/chain"
	"github.com/ava-labs/hypersdk/examples/litevm/consts"
	"github.com/ava-labs/hypersdk/examples/litevm/genesis"
	_ "github.com/ava-labs/hypersdk/examples/litevm/registry" // ensure registry populated
	"github.com/ava-labs/hypersdk/examples/litevm/storage"
	"github.com/ava-labs/hypersdk/requester"
	"github.com/ava-labs/hypersdk/rpc"
	"github.com/ava-labs/hypersdk/utils"
)

type JSONRPCClient struct {
	requester *requester.EndpointRequester

	chainID ids.ID
	g       *genesis.Genesis
}

// New creates a new client object.
func NewJSONRPCClient(uri string, chainID ids.ID) *JSONRPCClient {
	uri = strings.TrimSuffix(uri, "/")
	uri += JSONRPCEndpoint
	req := requester.New(uri, consts.Name)
	return &JSONRPCClient{req, chainID, nil}
}

func (cli *JSONRPCClient) Genesis(ctx context.Context) (*genesis.Genesis, error) {
	if cli.g != nil {
		return cli.g, nil
	}

	resp := new(GenesisReply)
	err := cli.requester.SendRequest(
		ctx,
		"genesis",
		nil,
		resp,
	)
	if err != nil {
		return nil, err
	}
	cli.g = resp.Genesis
	return resp.Genesis, nil
}

func (cli *JSONRPCClient) Tx(ctx context.Context, id ids.ID) (bool, bool, int64, error) {
	resp := new(TxReply)
	err := cli.requester.SendRequest(
		ctx,
		"tx",
		&TxArgs{TxID: id},
		resp,
	)
	switch {
	// We use string parsing here because the JSON-RPC library we use may not
	// allows us to perform errors.Is.
	case err != nil && strings.Contains(err.Error(), ErrTxNotFound.Error()):
		return false, false, -1, nil
	case err != nil:
		return false, false, -1, err
	}
	return true, resp.Success, resp.Timestamp, nil
}

func (cli *JSONRPCClient) Balance(ctx context.Context, addr string) (uint64, error) {
	resp := new(BalanceReply)
	err := cli.requester.SendRequest(
		ctx,
		"balance",
		&BalanceArgs{
			Address: addr,
		},
		resp,
	)
	return resp.Amount, err
}

func (cli *JSONRPCClient) WaitForBalance(
	ctx context.Context,
	addr string,
	min uint64,
) error {
	return rpc.Wait(ctx, func(ctx context.Context) (bool, error) {
		balance, err := cli.Balance(ctx, addr)
		if err != nil {
			return false, err
		}
		shouldExit := balance >= min
		if !shouldExit {
			utils.Outf(
				"{{yellow}}waiting for %s balance: %s{{/}}\n",
				utils.FormatBalance(min),
				addr,
			)
		}
		return shouldExit, nil
	})
}

func (cli *JSONRPCClient) WaitForTransaction(ctx context.Context, txID ids.ID) (bool, error) {
	var success bool
	if err := rpc.Wait(ctx, func(ctx context.Context) (bool, error) {
		found, isuccess, _, err := cli.Tx(ctx, txID)
		if err != nil {
			return false, err
		}
		success = isuccess
		return found, nil
	}); err != nil {
		return false, err
	}
	return success, nil
}

var _ chain.Parser = (*Parser)(nil)

type Parser struct {
	chainID ids.ID
	genesis *genesis.Genesis
}

func (p *Parser) ChainID() ids.ID {
	return p.chainID
}

func (p *Parser) Rules(t int64) chain.Rules {
	return p.genesis.Rules(t)
}

func (*Parser) Registry() (chain.ActionRegistry, chain.AuthRegistry) {
	return consts.ActionRegistry, consts.AuthRegistry
}

func (*Parser) StateManager() chain.StateManager {
	return &storage.StateManager{}
}

func (cli *JSONRPCClient) Parser(ctx context.Context) (chain.Parser, error) {
	g, err := cli.Genesis(ctx)
	if err != nil {
		return nil, err
	}
	return &Parser{cli.chainID, g}, nil
}
