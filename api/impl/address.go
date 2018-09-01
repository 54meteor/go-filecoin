package impl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"gx/ipfs/QmQsErDt8Qgw1XrsXf2BpEzDgGWtB1YLsTAARBup5b6B9W/go-libp2p-peer"
	"gx/ipfs/QmSP88ryZkHSRn1fnngAaV2Vcn63WUJzAavnRM9CVdU1Ky/go-ipfs-cmdkit/files"
	"gx/ipfs/QmVmDhyTTUcQXFD1rRQ64fGLMSAoaQvNH3hwuaCFAPq2hy/errors"

	"github.com/filecoin-project/go-filecoin/address"
	"github.com/filecoin-project/go-filecoin/api"
	"github.com/filecoin-project/go-filecoin/crypto"
	"github.com/filecoin-project/go-filecoin/state"
	"github.com/filecoin-project/go-filecoin/types"
	"github.com/filecoin-project/go-filecoin/wallet"
)

type nodeAddress struct {
	api *nodeAPI

	addrs *nodeAddrs
}

func newNodeAddress(api *nodeAPI) *nodeAddress {
	return &nodeAddress{
		api:   api,
		addrs: newNodeAddrs(api),
	}
}

func (api *nodeAddress) Addrs() api.Addrs {
	return api.addrs
}

func (api *nodeAddress) Balance(ctx context.Context, addr address.Address) (*types.AttoFIL, error) {
	fcn := api.api.node
	ts := fcn.ChainMgr.GetHeaviestTipSet()
	if len(ts) == 0 {
		return types.ZeroAttoFIL, ErrHeaviestTipSetNotFound
	}

	tree, err := fcn.ChainMgr.State(ctx, ts.ToSlice())
	if err != nil {
		return types.ZeroAttoFIL, err
	}

	act, err := tree.GetActor(ctx, addr)
	if err != nil {
		if state.IsActorNotFoundError(err) {
			// if the account doesn't exit, the balance should be zero
			return types.NewAttoFILFromFIL(0), nil
		}

		return types.ZeroAttoFIL, err
	}

	return act.Balance, nil
}

type nodeAddrs struct {
	api *nodeAPI
}

func newNodeAddrs(api *nodeAPI) *nodeAddrs {
	return &nodeAddrs{api: api}
}

func (api *nodeAddrs) New(ctx context.Context) (address.Address, error) {
	return api.api.node.NewAddress()
}

func (api *nodeAddrs) Ls(ctx context.Context) ([]address.Address, error) {
	return api.api.node.Wallet.Addresses(), nil
}

func (api *nodeAddrs) Lookup(ctx context.Context, addr address.Address) (peer.ID, error) {
	id, err := api.api.node.Lookup.GetPeerIDByMinerAddress(ctx, addr)
	if err != nil {
		return peer.ID(""), errors.Wrapf(err, "failed to find miner with address %s", addr.String())
	}

	return id, nil
}

func (api *nodeAddress) Import(ctx context.Context, f files.File) ([]address.Address, error) {
	nd := api.api.node

	kinfos, err := parseKeyInfos(f)
	if err != nil {
		return nil, err
	}

	dsb := nd.Wallet.Backends(wallet.DSBackendType)
	if len(dsb) != 1 {
		return nil, fmt.Errorf("expected exactly one datastore wallet backend")
	}

	imp, ok := dsb[0].(wallet.Importer)
	if !ok {
		return nil, fmt.Errorf("datastore backend wallets should implement importer")
	}

	var out []address.Address
	for _, ki := range kinfos {
		if err := imp.ImportKey(ki); err != nil {
			return nil, err
		}

		a, err := ki.Address()
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func (api *nodeAddress) Export(ctx context.Context, addrs []address.Address) ([]*crypto.KeyInfo, error) {
	nd := api.api.node

	out := make([]*crypto.KeyInfo, len(addrs))
	for i, addr := range addrs {
		bck, err := nd.Wallet.Find(addr)
		if err != nil {
			return nil, err
		}

		ki, err := bck.GetKeyInfo(addr)
		if err != nil {
			return nil, err
		}
		out[i] = ki
	}

	return out, nil
}

func parseKeyInfos(f files.File) ([]*crypto.KeyInfo, error) {
	var kinfos []*crypto.KeyInfo
	for {
		fi, err := f.NextFile()
		switch err {
		case io.EOF:
			return kinfos, nil
		default:
			return nil, err
		case nil:
		}

		var ki crypto.KeyInfo
		if err := json.NewDecoder(fi).Decode(&ki); err != nil {
			return nil, err
		}

		kinfos = append(kinfos, &ki)
	}
}
