package chain

import (
	"bytes"
	"sort"

	cbor "gx/ipfs/QmV6BQ6fFCf9eFHDuRxvguvqfKLZtZrxthgZvDfRCs4tMN/go-ipld-cbor"
	node "gx/ipfs/QmX5CsuHyVZeTLxgRSYkgLSDQKb9UjE8xnhQzCEJWWWFsC/go-ipld-format"
	"gx/ipfs/QmZFbDTY9jfSBms2MchvYM9oYRbAF19K7Pby47yDBfpPrb/go-cid"

	"github.com/filecoin-project/go-filecoin/address"
	"github.com/filecoin-project/go-filecoin/crypto"
	"github.com/filecoin-project/go-filecoin/types"
)

func init() {
	cbor.RegisterCborType(Block{})
}

// Block is a block in the blockchain.
type Block struct {
	// Miner is the address of the miner actor that mined this block.
	Miner address.Address `json:"miner"`

	// Ticket is the winning ticket that was submitted with this block.
	Ticket crypto.Signature `json:"ticket"`

	// Parents is the set of parents this block was based on. Typically one,
	// but can be several in the case where there were multiple winning ticket-
	// holders for an epoch.
	Parents types.SortedCidSet `json:"parents"`

	// ParentWeightNum is the numerator of the aggregate chain weight of the parent set.
	ParentWeightNum types.Uint64 `json:"parentWeightNumerator"`

	// ParentWeightDenom is the denominator of the aggregate chain weight of the parent set
	ParentWeightDenom types.Uint64 `json:"parentWeightDenominator"`

	// Height is the chain height of this block.
	Height types.Uint64 `json:"height"`

	// Nonce is a temporary field used to differentiate blocks for testing
	Nonce types.Uint64 `json:"nonce"`

	// Messages is the set of messages included in this block
	// TODO: should be a merkletree-ish thing
	Messages []*SignedMessage `json:"messages"`

	// StateRoot is a cid pointer to the state tree after application of the
	// transactions state transitions.
	StateRoot *cid.Cid `json:"stateRoot"`

	// MessageReceipts is a set of receipts matching to the sending of the `Messages`.
	MessageReceipts []*MessageReceipt `json:"messageReceipts"`
}

// Cid returns the content id of this block.
func (b *Block) Cid() *cid.Cid {
	// TODO: Cache ToNode() and/or ToNode().Cid(). We should be able to do this efficiently using
	// DeepEquals(), or perhaps our own Equals() interface.
	return b.ToNode().Cid()
}

// IsParentOf returns true if the argument is a parent of the receiver.
func (b Block) IsParentOf(c Block) bool {
	return c.Parents.Has(b.Cid())
}

// ToNode converts the Block to an IPLD node.
func (b *Block) ToNode() node.Node {
	// Use 32 byte / 256 bit digest. TODO pull this out into a constant?
	obj, err := cbor.WrapObject(b, types.DefaultHashFunction, -1)
	if err != nil {
		panic(err)
	}

	return obj
}

// DecodeBlock decodes raw cbor bytes into a Block.
func DecodeBlock(b []byte) (*Block, error) {
	var out Block
	if err := cbor.DecodeInto(b, &out); err != nil {
		return nil, err
	}

	return &out, nil
}

// Score returns the score of this block. Naively this will just return the
// height. But in the future this will return a more sophisticated metric to be
// used in the fork choice rule
// Choosing height as the score gives us the same consensus rules as bitcoin
func (b *Block) Score() uint64 {
	return uint64(b.Height)
}

// Equals returns true if the Block is equal to other.
func (b *Block) Equals(other *Block) bool {
	return b.Cid().Equals(other.Cid())
}

// SortBlocks sorts a slice of blocks in the canonical order (by min tickets)
func SortBlocks(blks []*Block) {
	sort.Slice(blks, func(i, j int) bool {
		return bytes.Compare(blks[i].Ticket, blks[j].Ticket) == -1
	})
}
