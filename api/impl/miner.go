package impl

import (
	"context"
	"math/big"

	"gx/ipfs/QmR8BauakNcBa3RbE4nbQu76PDiJgoQgz8AJdhJuiU4TAw/go-cid"
	"gx/ipfs/QmVmDhyTTUcQXFD1rRQ64fGLMSAoaQvNH3hwuaCFAPq2hy/errors"
	"gx/ipfs/QmcqU6QUDSXprb1518vYDGczrTJTyGwLG9eUa5iNX4xUtS/go-libp2p-peer"

	"github.com/filecoin-project/go-filecoin/address"
	"github.com/filecoin-project/go-filecoin/api2"
	"github.com/filecoin-project/go-filecoin/types"
)

type nodeMiner struct {
	api  *nodeAPI
	API2 api2.Filecoin
}

func newNodeMiner(api *nodeAPI, api2 api2.Filecoin) *nodeMiner {
	return &nodeMiner{api: api, API2: api2}
}

func (nm *nodeMiner) Create(ctx context.Context, fromAddr address.Address, pledge uint64, pid peer.ID, collateral *types.AttoFIL) (address.Address, error) {
	nd := nm.api.node

	if err := setDefaultFromAddr(&fromAddr, nd); err != nil {
		return address.Address{}, err
	}

	if pid == "" {
		pid = nd.Host().ID()
	}

	res, err := nd.CreateMiner(ctx, fromAddr, pledge, pid, collateral)
	if err != nil {
		return address.Address{}, errors.Wrap(err, "Could not create miner. Please consult the documentation to setup your wallet and genesis block correctly")
	}

	return *res, nil
}

func (nm *nodeMiner) UpdatePeerID(ctx context.Context, fromAddr, minerAddr address.Address, newPid peer.ID) (cid.Cid, error) {
	return nm.API2.MessageSend(
		ctx,
		fromAddr,
		minerAddr,
		nil,
		"updatePeerID",
		newPid,
	)
}

func (nm *nodeMiner) AddAsk(ctx context.Context, fromAddr, minerAddr address.Address, price *types.AttoFIL, expiry *big.Int) (cid.Cid, error) {
	return nm.API2.MessageSend(
		ctx,
		fromAddr,
		minerAddr,
		nil,
		"addAsk",
		price,
		expiry,
	)
}

func (nm *nodeMiner) GetOwner(ctx context.Context, minerAddr address.Address) (address.Address, error) {
	bytes, _, err := nm.api.Message().Query(
		ctx,
		address.Address{},
		minerAddr,
		"getOwner",
	)
	if err != nil {
		return address.Address{}, err
	}

	return address.NewFromBytes(bytes[0])
}

func (nm *nodeMiner) GetPower(ctx context.Context, minerAddr address.Address) (*big.Int, error) {
	bytes, _, err := nm.api.Message().Query(
		ctx,
		address.Address{},
		minerAddr,
		"getPower",
	)
	if err != nil {
		return nil, err
	}

	power := big.NewInt(0).SetBytes(bytes[0])

	return power, nil
}

func (nm *nodeMiner) GetPledge(ctx context.Context, minerAddr address.Address) (*big.Int, error) {
	bytes, _, err := nm.api.Message().Query(
		ctx,
		address.Address{},
		minerAddr,
		"getPledge",
	)
	if err != nil {
		return nil, err
	}

	power := big.NewInt(0).SetBytes(bytes[0])

	return power, nil
}

func (nm *nodeMiner) GetTotalPower(ctx context.Context) (*big.Int, error) {
	bytes, _, err := nm.api.Message().Query(
		ctx,
		address.Address{},
		address.StorageMarketAddress,
		"getTotalStorage",
	)
	if err != nil {
		return nil, err
	}

	power := big.NewInt(0).SetBytes(bytes[0])

	return power, nil
}
