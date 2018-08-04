package lookup

import (
	"context"

	"gx/ipfs/QmVmDhyTTUcQXFD1rRQ64fGLMSAoaQvNH3hwuaCFAPq2hy/errors"
	"gx/ipfs/QmdVrMn1LhB4ybb8hMVaMLXnA8XRSewMnK6YqXKXoTcRvN/go-libp2p-peer"

	"github.com/filecoin-project/go-filecoin/consensus"
	"github.com/filecoin-project/go-filecoin/core"
	"github.com/filecoin-project/go-filecoin/types"
)

// PeerLookupService provides an interface through which callers look up a miner's libp2p identity by their Filecoin address.
type PeerLookupService interface {
	GetPeerIDByMinerAddress(context.Context, types.Address) (peer.ID, error)
}

// ChainLookupService is a ChainManager-backed implementation of the PeerLookupService interface.
type ChainLookupService struct {
	consensus              consensus.Algorithm
	queryMethodFromAddress types.Address
}

var _ PeerLookupService = &ChainLookupService{}

// NewChainLookupService creates a new ChainLookupService from a ChainManager and a Wallet.
func NewChainLookupService(consensus consensus.Algorithm, queryMethodFromAddr types.Address) *ChainLookupService {
	return &ChainLookupService{
		consensus:              consensus,
		queryMethodFromAddress: queryMethodFromAddr,
	}
}

// GetPeerIDByMinerAddress attempts to get a miner's libp2p identity by loading the actor from the state tree and sending
// it a "getPeerID" message. The MinerActor is currently the only type of actor which has a peer ID.
func (c *ChainLookupService) GetPeerIDByMinerAddress(ctx context.Context, minerAddr types.Address) (peer.ID, error) {
	st, err := c.consensus.LatestState(ctx)
	if err != nil {
		return peer.ID(""), errors.Wrap(err, "failed to load state tree")
	}

	retValue, retCode, err := core.CallQueryMethod(ctx, st, minerAddr, "getPeerID", []byte{}, c.queryMethodFromAddress, nil)
	if err != nil {
		return peer.ID(""), errors.Wrap(err, "failed to query local state tree")
	}

	if retCode != 0 {
		return peer.ID(""), errors.Wrap(err, "non-zero status code from getPeerID")
	}

	pid, err := peer.IDFromBytes(retValue[0])
	if err != nil {
		return peer.ID(""), errors.Wrap(err, "could not decode to peer.ID from message-bytes")
	}

	return pid, nil
}
