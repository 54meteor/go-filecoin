package node

import (
	"context"

	offline "gx/ipfs/QmS6mo1dPpHdYsVkm27BRZDLxpKBCiJKUH8fHX15XFfMez/go-ipfs-exchange-offline"
	"gx/ipfs/QmVmDhyTTUcQXFD1rRQ64fGLMSAoaQvNH3hwuaCFAPq2hy/errors"
	"gx/ipfs/QmXJkSRxXHeAGmQJENct16anrKZHNECbmUoC7hMuCjLni6/go-hamt-ipld"
	bstore "gx/ipfs/QmadMhXJLHMFjpRmh85XjpmVDkEtQpNYEZNRpWRvYVLrvb/go-ipfs-blockstore"
	ci "gx/ipfs/Qme1knMqwt1hKZbc1BmQFmnm9f36nyQGwXxPGVpVJ9rMK5/go-libp2p-crypto"

	bserv "gx/ipfs/QmSLaAYBSKmPLxKUUh4twAGBCVXuYYriPTZ7FH24MsxSfs/go-blockservice"

	"github.com/filecoin-project/go-filecoin/chain"
	"github.com/filecoin-project/go-filecoin/consensus"
	"github.com/filecoin-project/go-filecoin/core"
	"github.com/filecoin-project/go-filecoin/repo"
	"github.com/filecoin-project/go-filecoin/types"
	"github.com/filecoin-project/go-filecoin/wallet"
)

var ErrLittleBits = errors.New("Bitsize less than 1024 is considered unsafe") // nolint: golint

// Init initializes a filecoin node in the given repo
// TODO: accept options?
//  - configurable genesis block
func Init(ctx context.Context, r repo.Repo, gen core.GenesisInitFunc) error {
	// TODO(ipfs): make the blockstore and blockservice have the same interfaces
	// so that this becomes less painful
	bs := bstore.NewBlockstore(r.Datastore())
	cst := &hamt.CborIpldStore{Blocks: bserv.New(bs, offline.Exchange(bs))}

	consensus := consensus.NewExpected()
	chainStore := chain.NewDefaultStore(cst)
	// cm := core.NewChainManager(r.Datastore(), cst)
	if err := consensus.Genesis(ctx, gen); err != nil {
		return errors.Wrap(err, "failed to initialize genesis")
	}

	sk, err := makePrivateKey(2048)
	if err != nil {
		return errors.Wrap(err, "failed to create nodes private key")
	}

	if err := r.Keystore().Put("self", sk); err != nil {
		return errors.Wrap(err, "failed to store private key")
	}

	// TODO: but behind a config option if this should be generated
	addr, err := newAddress(r)
	if err != nil {
		return errors.Wrap(err, "failed to generate reward address")
	}

	newConfig := r.Config()
	newConfig.Mining.RewardAddress = addr
	if err := r.ReplaceConfig(newConfig); err != nil {
		return errors.Wrap(err, "failed to update config")
	}

	return nil
}

// borrowed from go-ipfs: `repo/config/init.go`
func makePrivateKey(nbits int) (ci.PrivKey, error) {
	if nbits < 1024 {
		return nil, ErrLittleBits
	}

	// create a public private key pair
	sk, _, err := ci.GenerateKeyPair(ci.RSA, nbits)
	if err != nil {
		return nil, err
	}

	return sk, nil
}

func newAddress(r repo.Repo) (types.Address, error) {
	backend, err := wallet.NewDSBackend(r.WalletDatastore())
	if err != nil {
		return types.Address{}, errors.Wrap(err, "failed to set up wallet backend")
	}

	return backend.NewAddress()
}
