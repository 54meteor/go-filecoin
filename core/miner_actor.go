package core

import (
	"fmt"
	"math/big"

	cbor "gx/ipfs/QmRVSCwQtW1rjHCay9NqKXDwbtKTgDcN4iY7PrpSqfKM5D/go-ipld-cbor"

	"github.com/filecoin-project/go-filecoin/abi"
	"github.com/filecoin-project/go-filecoin/types"
)

func init() {
	cbor.RegisterCborType(MinerStorage{})
}

// MinerActor is the miner actor
type MinerActor struct{}

// Sector is the on-chain representation of a sector
type Sector struct {
	CommR []byte
	Deals []uint64
}

// MinerStorage is the miner actors storage
type MinerStorage struct {
	Owner types.Address

	// Pledge is amount the space being offered up by this miner
	// TODO: maybe minimum granularity is more than 1 byte?
	PledgeBytes *types.BytesAmount

	// Collateral is the total amount of filecoin being held as collateral for
	// the miners pledge
	Collateral *types.TokenAmount

	Sectors []*Sector

	LockedStorage *types.BytesAmount // LockedStorage is the amount of the miner's storage that is used.
	Power         *types.BytesAmount
}

// NewStorage returns an empty MinerStorage struct
func (ma *MinerActor) NewStorage() interface{} {
	return &MinerStorage{}
}

var _ ExecutableActor = (*MinerActor)(nil)

// NewMinerActor returns a new miner actor
func NewMinerActor(owner types.Address, pledge *types.BytesAmount, coll *types.TokenAmount) (*types.Actor, error) {
	st := &MinerStorage{
		Owner:         owner,
		PledgeBytes:   pledge,
		Collateral:    coll,
		LockedStorage: types.NewBytesAmount(0),
	}

	storageBytes, err := MarshalStorage(st)
	if err != nil {
		return nil, err
	}

	return types.NewActorWithMemory(types.MinerActorCodeCid, nil, storageBytes), nil
}

var minerExports = Exports{
	"addAsk": &FunctionSignature{
		Params: []abi.Type{abi.TokenAmount, abi.BytesAmount},
		Return: []abi.Type{abi.Integer},
	},
	"getOwner": &FunctionSignature{
		Params: nil,
		Return: []abi.Type{abi.Address},
	},
	"addDealsToSector": &FunctionSignature{
		Params: []abi.Type{abi.Integer, abi.UintArray},
		Return: []abi.Type{abi.Integer},
	},
	"commitSector": &FunctionSignature{
		Params: []abi.Type{abi.Integer, abi.Bytes, abi.UintArray},
		Return: []abi.Type{abi.Integer},
	},
}

// Exports returns the miner actors exported functions
func (ma *MinerActor) Exports() Exports {
	return minerExports
}

// ErrCallerUnauthorized signals an unauthorized caller.
var ErrCallerUnauthorized = newRevertError("not authorized to call the method")

// ErrInsufficientPledge signals insufficient pledge for what you are trying to do.
var ErrInsufficientPledge = newRevertError("not enough pledged")

// AddAsk adds an ask via this miner to the storage markets orderbook
func (ma *MinerActor) AddAsk(ctx *VMContext, price *types.TokenAmount, size *types.BytesAmount) (*big.Int, uint8,
	error) {
	var mstore MinerStorage
	out, err := WithStorage(ctx, &mstore, func() (interface{}, error) {
		if ctx.Message().From != mstore.Owner {
			// TODO This should probably return a non-zero exit code instead of an error.
			return nil, ErrCallerUnauthorized
		}

		// compute locked storage + new ask
		total := mstore.LockedStorage.Add(size)

		if total.GreaterThan(mstore.PledgeBytes) {
			// TODO This should probably return a non-zero exit code instead of an error.88
			return nil, ErrInsufficientPledge
		}

		mstore.LockedStorage = total

		// TODO: kinda feels weird that I can't get a real type back here
		out, err := ctx.Send(StorageMarketAddress, "addAsk", nil, []interface{}{price, size})
		if err != nil {
			return nil, err
		}

		askID, err := abi.Deserialize(out.ReturnBytes(), abi.Integer)
		if err != nil {
			return nil, faultErrorWrap(err, "error deserializing")
		}

		return askID.Val, nil
	})
	if err != nil {
		return nil, 1, err
	}

	askID, ok := out.(*big.Int)
	if !ok {
		return nil, 1, newRevertErrorf("expected an Integer return value from call, but got %T instead", out)
	}

	fmt.Printf("addAsk %s\n", askID)
	return askID, 0, nil
}

// GetOwner returns the miners owner
func (ma *MinerActor) GetOwner(ctx *VMContext) (types.Address, uint8, error) {
	var mstore MinerStorage
	out, err := WithStorage(ctx, &mstore, func() (interface{}, error) {
		return mstore.Owner, nil
	})
	if err != nil {
		return types.Address{}, 1, err
	}

	a, ok := out.(types.Address)
	if !ok {
		return types.Address{}, 1, newFaultErrorf("expected an Address return value from call, but got %T instead", out)
	}

	return a, 0, nil
}

// AddDealsToSector adds deals to a sector. If the sectorID given is -1, a new
// sector ID is allocated. The sector ID that deals are added to is returned
func (ma *MinerActor) AddDealsToSector(ctx *VMContext, sectorID int64, deals []uint64) (*big.Int, uint8,
	error) {
	var mstore MinerStorage
	out, err := WithStorage(ctx, &mstore, func() (interface{}, error) {
		return mstore.upsertDealsToSector(sectorID, deals)
	})
	if err != nil {
		return nil, 1, err
	}

	secIDout, ok := out.(int64)
	if !ok {
		return nil, 1, newRevertError("expected an int64")
	}

	return big.NewInt(secIDout), 0, nil
}

func (mstore *MinerStorage) upsertDealsToSector(sectorID int64, deals []uint64) (int64, error) {
	if sectorID == -1 {
		sectorID = int64(len(mstore.Sectors))
		mstore.Sectors = append(mstore.Sectors, new(Sector))
	}
	if sectorID >= int64(len(mstore.Sectors)) {
		return 0, newRevertError("sectorID out of range")
	}
	sector := mstore.Sectors[sectorID]
	if sector.CommR != nil {
		return 0, newRevertError("can't add deals to committed sector")
	}

	sector.Deals = append(sector.Deals, deals...)
	return sectorID, nil
}

// CommitSector adds a commitment to the specified sector
// if sectorID is -1, a new sector will be allocated.
// if passing an existing sector ID, any deals given here will be added to the
// deals already added to that sector
func (ma *MinerActor) CommitSector(ctx *VMContext, sectorID int64, commR []byte, deals []uint64) (*big.Int, uint8, error) {
	var mstore MinerStorage
	out, err := WithStorage(ctx, &mstore, func() (interface{}, error) {
		if len(deals) != 0 {
			sid, err := mstore.upsertDealsToSector(sectorID, deals)
			if err != nil {
				return nil, err
			}
			sectorID = sid
		}

		sector := mstore.Sectors[sectorID]
		if sector.CommR != nil {
			return nil, newRevertError("sector already committed")
		}

		resp, err := ctx.Send(StorageMarketAddress, "commitDeals", nil, []interface{}{sector.Deals})
		if err != nil {
			return nil, err
		}

		sector.CommR = commR
		power := types.NewBytesAmountFromBytes(resp.ReturnBytes())
		mstore.Power = mstore.Power.Add(power)

		return nil, nil
	})
	if err != nil {
		return nil, 1, err
	}

	secIDout, ok := out.(int64)
	if !ok {
		return nil, 1, newRevertError("expected an int64")
	}

	return big.NewInt(secIDout), 0, nil
}
