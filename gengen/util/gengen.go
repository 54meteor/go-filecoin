package gengen

import (
	"context"
	"fmt"
	"io"
	"math/big"
	"strconv"

	"github.com/filecoin-project/go-filecoin/actor/builtin"
	"github.com/filecoin-project/go-filecoin/actor/builtin/account"
	"github.com/filecoin-project/go-filecoin/address"
	"github.com/filecoin-project/go-filecoin/core"
	"github.com/filecoin-project/go-filecoin/crypto"
	"github.com/filecoin-project/go-filecoin/state"
	"github.com/filecoin-project/go-filecoin/types"
	"github.com/filecoin-project/go-filecoin/vm"

	"gx/ipfs/QmQZadYTDF4ud9DdK85PH2vReJRzUM9YfVW4ReB1q2m51p/go-hamt-ipld"
	peer "gx/ipfs/QmQsErDt8Qgw1XrsXf2BpEzDgGWtB1YLsTAARBup5b6B9W/go-libp2p-peer"
	bserv "gx/ipfs/QmTfTKeBhTLjSjxXQsjkF2b1DfZmYEMnknGE2y2gX57C6v/go-blockservice"
	ds "gx/ipfs/QmVG5gxteQNEMhrS8prJSmU2C9rebtFuTd3SYZ5kE3YZ5k/go-datastore"
	cid "gx/ipfs/QmZFbDTY9jfSBms2MchvYM9oYRbAF19K7Pby47yDBfpPrb/go-cid"
	offline "gx/ipfs/QmZxjqR9Qgompju73kakSoUj3rbVndAzky3oCDiBNCxPs1/go-ipfs-exchange-offline"
	"gx/ipfs/QmcQSyreJnxiZ1TCop3s5hjgsggpzCNjrbgqzUoQv4ywEW/go-car"
	blockstore "gx/ipfs/QmcmpX42gtDv1fz24kau4wjS9hfwWj5VexWBKgGnWzsyag/go-ipfs-blockstore"
	dag "gx/ipfs/QmeLG6jF1xvEmHca5Vy4q4EdQWp8Xq9S6EPyZrN9wvSRLC/go-merkledag"
)

// Miner is
type Miner struct {
	// Owner is the name of the key that owns this miner
	// It must be a name of a key from the configs 'Keys' list
	Owner string

	// PeerID is the peer ID to set as the miners owner
	PeerID string

	// Power is the amount of power this miner should start off with
	// TODO: this will get more complicated when we actually have to
	// prove real files
	Power uint64
}

// GenesisCfg is
type GenesisCfg struct {
	// Keys is an array of names of keys. A random key will be generated
	// for each name here.
	Keys []string

	// PreAlloc is a mapping from key names to string values of whole filecoin
	// that will be preallocated to each account
	PreAlloc map[string]string

	// Miners is a list of miners that should be set up at the start of the network
	Miners []Miner
}

// RenderedGenInfo contains information about a genesis block creation
type RenderedGenInfo struct {
	// Keys is the set of keys generated
	Keys map[string]*types.KeyInfo

	// Miners is the list of addresses of miners created
	Miners []RenderedMinerInfo

	// GenesisCid is the cid of the created genesis block
	GenesisCid *cid.Cid
}

// RenderedMinerInfo contains info about a created miner
type RenderedMinerInfo struct {
	// Owner is the key name of the owner of this miner
	Owner string

	// Address is the address generated on-chain for the miner
	Address address.Address

	// Power is the amount of storage power this miner was created with
	Power uint64
}

// GenGen takes the genesis configuration and creates a genesis block that
// matches the description. It writes all chunks to the dagservice, and returns
// the final genesis block.
func GenGen(ctx context.Context, cfg *GenesisCfg, cst *hamt.CborIpldStore, bs blockstore.Blockstore) (*RenderedGenInfo, error) {
	keys, err := genKeys(cfg.Keys)
	if err != nil {
		return nil, err
	}

	st := state.NewEmptyStateTreeWithActors(cst, builtin.Actors)
	storageMap := vm.NewStorageMap(bs)

	if err := core.SetupDefaultActors(ctx, st, storageMap); err != nil {
		return nil, err
	}

	if err := setupPrealloc(st, keys, cfg.PreAlloc); err != nil {
		return nil, err
	}

	miners, err := setupMiners(st, storageMap, keys, cfg.Miners)
	if err != nil {
		return nil, err
	}

	if err := cst.Blocks.AddBlock(types.StorageMarketActorCodeObj); err != nil {
		return nil, err
	}
	if err := cst.Blocks.AddBlock(types.MinerActorCodeObj); err != nil {
		return nil, err
	}
	if err := cst.Blocks.AddBlock(types.AccountActorCodeObj); err != nil {
		return nil, err
	}
	if err := cst.Blocks.AddBlock(types.PaymentBrokerActorCodeObj); err != nil {
		return nil, err
	}

	stateRoot, err := st.Flush(ctx)
	if err != nil {
		return nil, err
	}

	err = storageMap.Flush()
	if err != nil {
		return nil, err
	}

	geneblk := &types.Block{
		StateRoot: stateRoot,
	}

	c, err := cst.Put(ctx, geneblk)
	if err != nil {
		return nil, err
	}

	return &RenderedGenInfo{
		Keys:       keys,
		GenesisCid: c,
		Miners:     miners,
	}, nil
}

func genKeys(cfgkeys []string) (map[string]*types.KeyInfo, error) {
	keys := make(map[string]*types.KeyInfo)
	for _, k := range cfgkeys {
		if _, ok := keys[k]; ok {
			return nil, fmt.Errorf("duplicate key with name: %q", k)
		}
		sk, err := crypto.GenerateKey() // TODO: GenerateKey should return a KeyInfo
		if err != nil {
			return nil, err
		}

		ki := &types.KeyInfo{
			PrivateKey: crypto.ECDSAToBytes(sk),
			Curve:      types.SECP256K1,
		}

		keys[k] = ki
	}

	return keys, nil
}

func setupPrealloc(st state.Tree, keys map[string]*types.KeyInfo, prealloc map[string]string) error {

	for k, v := range prealloc {
		ki, ok := keys[k]
		if !ok {
			return fmt.Errorf("no such key: %q", k)
		}

		addr, err := ki.Address()
		if err != nil {
			return err
		}

		valint, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return err
		}

		act, err := account.NewActor(types.NewAttoFILFromFIL(valint))
		if err != nil {
			return err
		}
		if err := st.SetActor(context.Background(), addr, act); err != nil {
			return err
		}
	}

	netact, err := account.NewActor(types.NewAttoFILFromFIL(10000000000))
	if err != nil {
		return err
	}

	return st.SetActor(context.Background(), address.NetworkAddress, netact)
}

func setupMiners(st state.Tree, sm vm.StorageMap, keys map[string]*types.KeyInfo, miners []Miner) ([]RenderedMinerInfo, error) {
	var minfos []RenderedMinerInfo
	ctx := context.Background()

	for _, m := range miners {
		addr, err := keys[m.Owner].Address()
		if err != nil {
			return nil, err
		}

		var pid peer.ID
		if m.PeerID != "" {
			p, err := peer.IDB58Decode(m.PeerID)
			if err != nil {
				return nil, err
			}
			pid = p
		} else {
			pid = core.RequireRandomPeerID()
		}

		// give collateral to account actor
		_, err = core.ApplyMessageDirect(ctx, st, sm, address.NetworkAddress, addr, types.NewAttoFILFromFIL(100000), "")
		if err != nil {
			return nil, err
		}

		// create miner
		pubkey, err := keys[m.Owner].PublicKey()
		if err != nil {
			return nil, err
		}
		resp, err := core.ApplyMessageDirect(ctx, st, sm, addr, address.StorageMarketAddress, types.NewAttoFILFromFIL(100000), "createMiner", big.NewInt(10000), pubkey, pid)
		if err != nil {
			return nil, err
		}

		// get miner address
		maddr, err := address.NewFromBytes(resp.Receipt.Return[0])
		if err != nil {
			return nil, err
		}

		minfos = append(minfos, RenderedMinerInfo{
			Address: maddr,
			Owner:   m.Owner,
			Power:   m.Power,
		})

		// commit sector to add power

		// TODO: We should get a SectorID from the SectorBuilder instead of
		// hard-coding a value here.
		sectorID := uint64(0)

		for i := 0; uint64(i) < m.Power; i++ {
			commR := []byte(fmt.Sprintf("commR-%d-%s", i, addr))
			commD := []byte(fmt.Sprintf("commD-%d-%s", i, addr))
			_, err = core.ApplyMessageDirect(ctx, st, sm, addr, maddr, types.NewAttoFILFromFIL(0), "commitSector", sectorID, commR, commD)
			if err != nil {
				return nil, err
			}
			sectorID++
		}
	}

	return minfos, nil
}

// GenGenesisCar generates a car for the given genesis configuration
func GenGenesisCar(cfg *GenesisCfg, out io.Writer) (*RenderedGenInfo, error) {
	// TODO: these six lines are ugly. We can do better...
	mds := ds.NewMapDatastore()
	bstore := blockstore.NewBlockstore(mds)
	offl := offline.Exchange(bstore)
	blkserv := bserv.New(bstore, offl)
	cst := &hamt.CborIpldStore{Blocks: blkserv}
	dserv := dag.NewDAGService(blkserv)

	ctx := context.Background()

	info, err := GenGen(ctx, cfg, cst, bstore)
	if err != nil {
		return nil, err
	}

	return info, car.WriteCar(ctx, dserv, []*cid.Cid{info.GenesisCid}, out)
}
