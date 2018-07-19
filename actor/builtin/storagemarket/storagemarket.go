package storagemarket

import (
	"bytes"
	"fmt"
	"math/big"

	cbor "gx/ipfs/QmRiRJhn427YVuufBEHofLreKWNw7P7BWNq86Sb9kzqdbd/go-ipld-cbor"
	"gx/ipfs/QmZoWKhxUmZ2seW4BzX6fJkNR8hh9PsGModr7q171yq2SS/go-libp2p-peer"
	"gx/ipfs/QmcZfnkapfECQGcLZaf9B79NRg7cRa9EnZh4LSbkCzwNvY/go-cid"

	// TODO remove once an interface is decided on
	"gx/ipfs/QmZp3eKdYQHHAneECmeK6HhiMwTPufmjC8DuuaGKv3unvx/blake2b-simd"

	"github.com/filecoin-project/go-filecoin/abi"
	"github.com/filecoin-project/go-filecoin/actor"
	"github.com/filecoin-project/go-filecoin/actor/builtin/miner"
	"github.com/filecoin-project/go-filecoin/crypto"
	"github.com/filecoin-project/go-filecoin/exec"
	"github.com/filecoin-project/go-filecoin/types"
	"github.com/filecoin-project/go-filecoin/vm/errors"
)

// MinimumPledge is the minimum amount of space a user can pledge
var MinimumPledge = types.NewBytesAmount(10000)

const (
	// ErrPledgeTooLow is the error code for a pledge under the MinimumPledge
	ErrPledgeTooLow = 33
	// ErrUnknownMiner indicates a pledge under the MinimumPledge
	ErrUnknownMiner = 34
	// ErrUnknownAsk indicates the ask for a deal could not be found
	ErrUnknownAsk = 35
	// ErrUnknownBid indicates the bid for a deal could not be found
	ErrUnknownBid = 36
	// ErrAskOwnerNotFound indicates the owner of an ask could not be found
	ErrAskOwnerNotFound = 37
	// ErrNotBidOwner indicates the sender is not the owner of the bid
	ErrNotBidOwner = 38
	// ErrInsufficientSpace indicates the bid to too big for the ask
	ErrInsufficientSpace = 39
	// ErrInvalidSignature indicates the signature is invalid
	ErrInvalidSignature = 40
	// ErrUnknownDeal indicates the deal id is not found
	ErrUnknownDeal = 41
	// ErrNotDealOwner indicates someone other than the deal owner tried to commit
	ErrNotDealOwner = 42
	// ErrDealCommitted indicates the deal is already committed
	ErrDealCommitted = 43
	// ErrInsufficientBidFunds indicates the value of the bid message is less than the price of the space
	ErrInsufficientBidFunds = 44
)

// Errors map error codes to revert errors this actor may return
var Errors = map[uint8]error{
	ErrPledgeTooLow:         errors.NewCodedRevertErrorf(ErrPledgeTooLow, "pledge must be at least %s bytes", MinimumPledge),
	ErrUnknownMiner:         errors.NewCodedRevertErrorf(ErrUnknownMiner, "unknown miner"),
	ErrUnknownAsk:           errors.NewCodedRevertErrorf(ErrUnknownAsk, "ask id not found"),
	ErrUnknownBid:           errors.NewCodedRevertErrorf(ErrUnknownBid, "bid id not found"),
	ErrAskOwnerNotFound:     errors.NewCodedRevertErrorf(ErrAskOwnerNotFound, "cannot create a deal for someone elses ask"),
	ErrNotBidOwner:          errors.NewCodedRevertErrorf(ErrNotBidOwner, "ask id not found"),
	ErrInsufficientSpace:    errors.NewCodedRevertErrorf(ErrNotBidOwner, "not enough space in ask for bid"),
	ErrInvalidSignature:     errors.NewCodedRevertErrorf(ErrInvalidSignature, "signature failed to validate"),
	ErrUnknownDeal:          errors.NewCodedRevertErrorf(ErrUnknownDeal, "unknown deal id"),
	ErrNotDealOwner:         errors.NewCodedRevertErrorf(ErrNotDealOwner, "miner tried to commit with someone elses deal"),
	ErrDealCommitted:        errors.NewCodedRevertErrorf(ErrDealCommitted, "deal already committed"),
	ErrInsufficientBidFunds: errors.NewCodedRevertErrorf(ErrInsufficientBidFunds, "must send price * size funds to create bid"),
}

func init() {
	cbor.RegisterCborType(Storage{})
	cbor.RegisterCborType(struct{}{})
	cbor.RegisterCborType(Filemap{})
}

// Actor implements the filecoin storage market. It is responsible
// for starting up new miners, adding bids, asks and deals. It also exposes the
// power table used to drive filecoin consensus.
type Actor struct{}

// Storage is the storage markets storage
type Storage struct {
	Miners types.AddrSet

	Orderbook *Orderbook

	Filemap *Filemap

	TotalCommittedStorage *types.BytesAmount
}

// NewStorage returns an empty StorageMarketStorage struct
func (sma *Actor) NewStorage() interface{} {
	return &Storage{}
}

var _ exec.ExecutableActor = (*Actor)(nil)

// NewActor returns a new storage market actor
func NewActor() (*types.Actor, error) {
	initStorage := &Storage{
		Miners: make(types.AddrSet),
		Orderbook: &Orderbook{
			Asks: make(AskSet),
			Bids: make(BidSet),
		},
		Filemap: &Filemap{
			Files: make(map[string][]uint64),
		},
	}
	storageBytes, err := actor.MarshalStorage(initStorage)
	if err != nil {
		return nil, err
	}
	return types.NewActorWithMemory(types.StorageMarketActorCodeCid, nil, storageBytes), nil
}

// Exports returns the actors exports
func (sma *Actor) Exports() exec.Exports {
	return storageMarketExports
}

var storageMarketExports = exec.Exports{
	"createMiner": &exec.FunctionSignature{
		Params: []abi.Type{abi.BytesAmount, abi.Bytes, abi.PeerID},
		Return: []abi.Type{abi.Address},
	},
	"addAsk": &exec.FunctionSignature{
		Params: []abi.Type{abi.AttoFIL, abi.BytesAmount},
		Return: []abi.Type{abi.Integer},
	},
	"addBid": &exec.FunctionSignature{
		Params: []abi.Type{abi.AttoFIL, abi.BytesAmount},
		Return: []abi.Type{abi.Integer},
	},
	"addDeal": &exec.FunctionSignature{
		Params: []abi.Type{abi.Integer, abi.Integer, abi.Bytes, abi.Bytes},
		Return: []abi.Type{abi.Integer},
	},
	"commitDeals": &exec.FunctionSignature{
		Params: []abi.Type{abi.UintArray},
		Return: []abi.Type{abi.Integer},
	},
}

// CreateMiner creates a new miner with the a pledge of the given size. The
// miners collateral is set by the value in the message.
func (sma *Actor) CreateMiner(ctx exec.VMContext, pledge *types.BytesAmount, publicKey []byte, pid peer.ID) (types.Address, uint8, error) {
	var storage Storage
	ret, err := actor.WithStorage(ctx, &storage, func() (interface{}, error) {
		if pledge.LessThan(MinimumPledge) {
			// TODO This should probably return a non-zero exit code instead of an error.
			return nil, Errors[ErrPledgeTooLow]
		}

		// 'CreateNewActor' (should likely be a method on the vmcontext)
		addr, err := ctx.AddressForNewActor()
		if err != nil {
			return nil, errors.FaultErrorWrap(err, "could not get address for new actor")
		}

		minerActor, err := miner.NewActor(ctx.Message().From, publicKey, pledge, pid, ctx.Message().Value)
		if err != nil {
			if !errors.ShouldRevert(err) {
				// TODO? From an Actor's perspective this (and other stuff) should probably
				// never fail. It should call into the vmcontext to do this and the vm context
				// should "throw" to a higher level handler if there's a system fault. It would
				// simplify the actor code.
				err = errors.FaultErrorWrap(err, "could not get a new miner actor")
			}
			return nil, err
		}

		if err := ctx.TEMPCreateActor(addr, minerActor); err != nil {
			return nil, errors.FaultErrorWrap(err, "could not set miner actor in CreateMiner")
		}
		// -- end --

		_, _, err = ctx.Send(addr, "", ctx.Message().Value, nil)
		if err != nil {
			return nil, err
		}

		storage.Miners[addr] = struct{}{}
		return addr, nil
	})
	if err != nil {
		return types.Address{}, errors.CodeError(err), err
	}

	return ret.(types.Address), 0, nil
}

// AddAsk adds an ask order to the orderbook. Must be called by a miner created
// by this storage market actor
func (sma *Actor) AddAsk(ctx exec.VMContext, price *types.AttoFIL, size *types.BytesAmount) (*big.Int, uint8,
	error) {
	var storage Storage
	ret, err := actor.WithStorage(ctx, &storage, func() (interface{}, error) {
		// method must be called by a miner that was created by this storage market actor
		miner := ctx.Message().From

		_, ok := storage.Miners[miner]
		if !ok {
			return nil, Errors[ErrUnknownMiner]
		}

		askID := storage.Orderbook.NextAskID
		storage.Orderbook.NextAskID++

		storage.Orderbook.Asks[askID] = &Ask{
			ID:    askID,
			Price: price,
			Size:  size,
			Owner: miner,
		}

		return big.NewInt(0).SetUint64(askID), nil
	})
	if err != nil {
		return nil, errors.CodeError(err), err
	}

	askID, ok := ret.(*big.Int)
	if !ok {
		return nil, 1, errors.NewRevertErrorf("expected *big.Int to be returned, but got %T instead", ret)
	}

	return askID, 0, nil
}

// AddBid adds a bid order to the orderbook. Can be called by anyone. The
// message must contain the appropriate amount of funds to be locked up for the
// bid.
func (sma *Actor) AddBid(ctx exec.VMContext, price *types.AttoFIL, size *types.BytesAmount) (*big.Int, uint8, error) {
	var storage Storage
	ret, err := actor.WithStorage(ctx, &storage, func() (interface{}, error) {
		lockedFunds := price.CalculatePrice(size)
		if ctx.Message().Value.LessThan(lockedFunds) {
			return nil, Errors[ErrInsufficientBidFunds]
		}

		bidID := storage.Orderbook.NextBidID
		storage.Orderbook.NextBidID++

		storage.Orderbook.Bids[bidID] = &Bid{
			ID:    bidID,
			Price: price,
			Size:  size,
			Owner: ctx.Message().From,
		}

		return big.NewInt(0).SetUint64(bidID), nil
	})
	if err != nil {
		return nil, errors.CodeError(err), err
	}

	bidID, ok := ret.(*big.Int)
	if !ok {
		return nil, 1, errors.NewRevertErrorf("expected *big.Int to be returned, but got %T instead", ret)
	}

	return bidID, 0, nil
}

func RecoverAddress(askID, bidID *big.Int, dataRef, sig []byte) (types.Address, error) {
	data, err := cid.Cast(dataRef)
	if err != nil {
		return types.Address{}, err
	}
	// recreate the deal
	deal := &Deal{
		Ask:     askID.Uint64(),
		Bid:     bidID.Uint64(),
		DataRef: data,
	}

	// as bytes
	bd, err := deal.Marshal()
	if err != nil {
		return types.Address{}, err
	}
	// TODO this should be handled by a utility
	dealHash := blake2b.Sum256(bd)

	// get a public key
	maybePk, err := crypto.Ecrecover(dealHash[:], sig)
	if err != nil {
		return types.Address{}, err
	}

	maybeAddrHash, err := types.AddressHash(maybePk)
	if err != nil {
		return types.Address{}, err
	}
	// return address associated with public key
	return types.NewMainnetAddress(maybeAddrHash), nil
}

// AddDeal creates a deal from the given ask and bid
// It must always called by the owner of the miner in the ask
func (sma *Actor) AddDeal(ctx exec.VMContext, askID, bidID *big.Int, bidOwnerSig []byte, refb []byte) (*big.Int, uint8, error) {
	ref, err := cid.Cast(refb)
	if err != nil {
		return nil, 1, errors.NewRevertErrorf("'ref' input was not a valid cid: %s", err)
	}

	var storage Storage
	ret, err := actor.WithStorage(ctx, &storage, func() (interface{}, error) {
		// TODO: askset is a map from uint64, our input is a big int.
		ask, ok := storage.Orderbook.Asks[askID.Uint64()]
		if !ok {
			return nil, Errors[ErrUnknownAsk]
		}

		bid, ok := storage.Orderbook.Bids[bidID.Uint64()]
		if !ok {
			return nil, Errors[ErrUnknownBid]
		}

		mown, ret, err := ctx.Send(ask.Owner, "getOwner", nil, nil)
		if err != nil {
			return nil, err
		}
		if ret != 0 {
			return nil, Errors[ErrAskOwnerNotFound]
		}

		if !bytes.Equal(ctx.Message().From.Bytes(), mown[0]) {
			return nil, Errors[ErrNotBidOwner]
		}

		if ask.Size.LessThan(bid.Size) {
			return nil, Errors[ErrInsufficientSpace]
		}

		maybeBidOwner, err := RecoverAddress(askID, bidID, refb, bidOwnerSig)
		if err != nil {
			panic(err)
			return nil, Errors[ErrInvalidSignature]
		}
		// TODO: real signature check and stuff
		if !bytes.Equal(bid.Owner.Bytes(), maybeBidOwner.Bytes()) {
			return nil, Errors[ErrInvalidSignature]
		}

		// mark bid as used (note: bid is a pointer)
		bid.Used = true

		// subtract used space from add
		ask.Size = ask.Size.Sub(bid.Size)

		d := &Deal{
			// Expiry:  ???
			DataRef: ref,
			Ask:     askID.Uint64(),
			Bid:     bidID.Uint64(),
		}

		dealID := uint64(len(storage.Filemap.Deals))
		ks := ref.KeyString()
		deals := storage.Filemap.Files[ks]
		storage.Filemap.Files[ks] = append(deals, dealID)
		storage.Filemap.Deals = append(storage.Filemap.Deals, d)

		return big.NewInt(int64(dealID)), nil
	})
	if err != nil {
		return nil, errors.CodeError(err), err
	}

	dealID, ok := ret.(*big.Int)
	if !ok {
		return nil, 1, fmt.Errorf("expected *big.Int to be returned, but got %T instead", ret)
	}

	return dealID, 0, nil
}

// CommitDeals marks the given deals as committed, counts up the total space
// occupied by those deals, updates the total storage count, and returns the
// total size of these deals.
func (sma *Actor) CommitDeals(ctx exec.VMContext, deals []uint64) (*types.BytesAmount, uint8, error) {
	var storage Storage
	ret, err := actor.WithStorage(ctx, &storage, func() (interface{}, error) {
		totalSize := types.NewBytesAmount(0)
		for _, d := range deals {
			if d >= uint64(len(storage.Filemap.Deals)) {
				return nil, Errors[ErrUnknownDeal]
			}

			deal := storage.Filemap.Deals[d]
			ask := storage.Orderbook.Asks[deal.Ask]

			// make sure that the miner actor who owns the asks calls this
			if ask.Owner != ctx.Message().From {
				return nil, Errors[ErrNotDealOwner]
			}

			if deal.Committed {
				return nil, Errors[ErrDealCommitted]
			}

			deal.Committed = true

			// TODO: Check that deal has not expired

			bid := storage.Orderbook.Bids[deal.Bid]

			// NB: if we allow for deals to be made at sizes other than the bid
			// size, this will need to be changed.
			totalSize = totalSize.Add(bid.Size)
		}

		// update the total data stored by the network
		storage.TotalCommittedStorage = storage.TotalCommittedStorage.Add(totalSize)

		return totalSize, nil
	})
	if err != nil {
		return nil, errors.CodeError(err), err
	}

	count, ok := ret.(*types.BytesAmount)
	if !ok {
		return nil, 1, fmt.Errorf("expected *BytesAmount to be returned, but got %T instead", ret)
	}

	return count, 0, nil
}
