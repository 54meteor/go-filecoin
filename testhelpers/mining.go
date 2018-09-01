package testhelpers

import (
	"gx/ipfs/QmQsErDt8Qgw1XrsXf2BpEzDgGWtB1YLsTAARBup5b6B9W/go-libp2p-peer"

	"github.com/filecoin-project/go-filecoin/abi"
	"github.com/filecoin-project/go-filecoin/address"
	"github.com/filecoin-project/go-filecoin/chain"
	"github.com/filecoin-project/go-filecoin/types"
)

// CreateMinerMessage creates a message to create a miner.
func CreateMinerMessage(from address.Address, nonce uint64, pledge types.BytesAmount, pid peer.ID, collateral *types.AttoFIL) (*chain.Message, error) {
	params, err := abi.ToEncodedValues(&pledge, []byte{}, pid)
	if err != nil {
		return nil, err
	}

	return chain.NewMessage(from, address.StorageMarketAddress, nonce, collateral, "createMiner", params), nil
}

// AddBidMessage creates a message to add a bid.
func AddBidMessage(from address.Address, nonce uint64, price *types.AttoFIL, size *types.BytesAmount) (*chain.Message, error) {
	funds := price.CalculatePrice(size)

	params, err := abi.ToEncodedValues(price, size)
	if err != nil {
		return nil, err
	}

	return chain.NewMessage(from, address.StorageMarketAddress, nonce, funds, "addBid", params), nil
}

// AddAskMessage creates a message to add ask.
func AddAskMessage(miner address.Address, from address.Address, nonce uint64, price *types.AttoFIL, size *types.BytesAmount) (*chain.Message, error) {
	params, err := abi.ToEncodedValues(price, size)
	if err != nil {
		return nil, err
	}

	return chain.NewMessage(from, miner, nonce, types.NewZeroAttoFIL(), "addAsk", params), nil
}

// CommitSectorMessage creates a message to commit a sector.
func CommitSectorMessage(miner, from address.Address, nonce uint64, commR []byte, size *types.BytesAmount) (*chain.Message, error) {
	params, err := abi.ToEncodedValues(commR, size)
	if err != nil {
		return nil, err
	}

	return chain.NewMessage(from, miner, nonce, types.NewZeroAttoFIL(), "commitSector", params), nil
}
