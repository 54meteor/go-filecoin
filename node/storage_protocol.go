package node

import (
	"context"
	"fmt"
	"sync"

	inet "gx/ipfs/QmQSbtGXCyNrj34LWL8EgXyNNYDZ8r3SwQcpW5pPxVhLnM/go-libp2p-net"
	cbor "gx/ipfs/QmV6BQ6fFCf9eFHDuRxvguvqfKLZtZrxthgZvDfRCs4tMN/go-ipld-cbor"
	"gx/ipfs/QmVmDhyTTUcQXFD1rRQ64fGLMSAoaQvNH3hwuaCFAPq2hy/errors"
	ipld "gx/ipfs/QmX5CsuHyVZeTLxgRSYkgLSDQKb9UjE8xnhQzCEJWWWFsC/go-ipld-format"
	"gx/ipfs/QmZFbDTY9jfSBms2MchvYM9oYRbAF19K7Pby47yDBfpPrb/go-cid"
	"gx/ipfs/QmZNkThpqfVXs9GNbexPrfBbXSLNYeKrE7jwFM2oqHbyqN/go-libp2p-protocol"
	unixfs "gx/ipfs/Qmdg2crJzNUF1mLPnLPSCCaDdLDqE4Qrh9QEiDooSYkvuB/go-unixfs"

	dag "gx/ipfs/QmeLG6jF1xvEmHca5Vy4q4EdQWp8Xq9S6EPyZrN9wvSRLC/go-merkledag"

	"github.com/filecoin-project/go-filecoin/address"
	cbu "github.com/filecoin-project/go-filecoin/cborutil"
	"github.com/filecoin-project/go-filecoin/crypto"
	"github.com/filecoin-project/go-filecoin/types"
)

const StorageDealProtocolID = protocol.ID("/fil/storage/mk/1.0.0")       // nolint: golint
const StorageDealQueryProtocolID = protocol.ID("/fil/storage/qry/1.0.0") // nolint: golint

func init() {
	cbor.RegisterCborType(StorageDealProposal{})
	cbor.RegisterCborType(StorageDealResponse{})
	cbor.RegisterCborType(PaymentInfo{})
	cbor.RegisterCborType(ProofInfo{})
	cbor.RegisterCborType(storageDealQueryRequest{})
}

// StorageDealProposal is
type StorageDealProposal struct {
	// PieceRef is the cid of the piece being stored
	PieceRef *cid.Cid

	// Size is the total number of bytes the proposal is asking to store
	Size *types.BytesAmount

	// TotalPrice is the total price that will be paid for the entire storage operation
	TotalPrice *types.AttoFIL

	// Duration is the number of blocks to make a deal for
	Duration uint64

	//Payment PaymentInfo

	//Signature types.Signature
}

// PaymentInfo is
type PaymentInfo struct{}

// StorageDealResponse is
type StorageDealResponse struct {
	// State is the current state of this deal
	State DealState

	// Message is an optional message to add context to any given response
	Message string

	// Proposal is the cid of the StorageDealProposal object this response is for
	Proposal *cid.Cid

	// ProofInfo is a collection of information needed to convince the client that
	// the miner has sealed the data into a sector.
	//ProofInfo *ProofInfo

	// Signature is a signature from the miner over the response
	Signature crypto.Signature
}

// ProofInfo is proof info
type ProofInfo struct {
}

// StorageMiner represents a storage miner
type StorageMiner struct {
	nd *Node

	deals   map[string]*storageDealState
	dealsLk sync.Mutex
}

type storageDealState struct {
	proposal *StorageDealProposal

	state *StorageDealResponse
}

// NewStorageMiner is
func NewStorageMiner(nd *Node) *StorageMiner {
	sm := &StorageMiner{
		nd:    nd,
		deals: make(map[string]*storageDealState),
	}
	nd.Host.SetStreamHandler(StorageDealProtocolID, sm.handleProposalStream)
	nd.Host.SetStreamHandler(StorageDealQueryProtocolID, sm.handleQuery)

	return sm
}

func (sm *StorageMiner) handleProposalStream(s inet.Stream) {
	defer s.Close() // nolint: errcheck

	var proposal StorageDealProposal
	if err := cbu.NewMsgReader(s).ReadMsg(&proposal); err != nil {
		panic(err)
	}

	ctx := context.Background()
	resp, err := sm.ReceiveStorageProposal(ctx, &proposal)
	if err != nil {
		panic(err)
	}

	if err := cbu.NewMsgWriter(s).WriteMsg(resp); err != nil {
		panic(err)
	}
}

// ReceiveStorageProposal is the entry point for the miner storage protocol
func (sm *StorageMiner) ReceiveStorageProposal(ctx context.Context, p *StorageDealProposal) (*StorageDealResponse, error) {
	// TODO: Check signature

	// TODO: check size, duration, totalprice match up with the payment info
	//       and also check that the payment info is valid.
	//       A valid payment info contains enough funds to *us* to cover the totalprice

	// TODO: decide if we want to accept this thingy

	// Payment is valid, everything else checks out, let's accept this proposal
	return sm.acceptProposal(ctx, p)
}

func (sm *StorageMiner) acceptProposal(ctx context.Context, p *StorageDealProposal) (*StorageDealResponse, error) {
	// TODO: we don't really actually want to put this in our general storage
	// but we just want to get its cid, as a way to uniquely track it
	propcid, err := sm.nd.CborStore.Put(ctx, p)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get cid of proposal")
	}

	resp := &StorageDealResponse{
		State:     Accepted,
		Proposal:  propcid,
		Signature: crypto.Signature("signaturrreee"),
	}

	sm.dealsLk.Lock()
	defer sm.dealsLk.Unlock()
	sm.deals[propcid.KeyString()] = &storageDealState{
		proposal: p,
		state:    resp,
	}

	// TODO: use some sort of nicer scheduler
	go sm.processStorageDeal(propcid)

	return resp, nil
}

func (sm *StorageMiner) getStorageDeal(c *cid.Cid) *storageDealState {
	sm.dealsLk.Lock()
	defer sm.dealsLk.Unlock()
	return sm.deals[c.KeyString()]
}

func (sm *StorageMiner) updateDealState(c *cid.Cid, f func(*StorageDealResponse)) {
	sm.dealsLk.Lock()
	defer sm.dealsLk.Unlock()
	f(sm.deals[c.KeyString()].state)
}

func (sm *StorageMiner) processStorageDeal(c *cid.Cid) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := sm.getStorageDeal(c)
	if d.state.State != Accepted {
		// TODO: handle resumption of deal processing across miner restarts
		log.Error("attempted to process an already started deal")
		return
	}

	// 'Receive' the data, this could also be a truck full of hard drives. (TODO: proper abstraction)
	// TODO: this is not a great way to do this. At least use a session
	// Also, this needs to be fetched into a staging area for miners to prepare and seal in data
	if err := dag.FetchGraph(ctx, d.proposal.PieceRef, dag.NewDAGService(sm.nd.Blockservice)); err != nil {
		log.Errorf("failed to fetch data: %s", err)
		sm.updateDealState(c, func(resp *StorageDealResponse) {
			resp.Message = "Transfer failed"
			resp.State = Failed
			// TODO: signature?
		})
		return
	}

	// TODO: add the data to a sector
	sm.updateDealState(c, func(resp *StorageDealResponse) {
		resp.State = Staged
	})

	// TODO: wait for sector to get filled up

	// TODO: seal the data
	sm.updateDealState(c, func(resp *StorageDealResponse) {
		resp.State = Complete
		//resp.ProofInfo = new(ProofInfo)
	})
}

// Query responds to a query for the proposal referenced by the given cid
func (sm *StorageMiner) Query(ctx context.Context, c *cid.Cid) *StorageDealResponse {
	sm.dealsLk.Lock()
	defer sm.dealsLk.Unlock()
	d, ok := sm.deals[c.KeyString()]
	if !ok {
		return &StorageDealResponse{
			State:   Unknown,
			Message: "no such deal",
		}
	}

	return d.state
}

type storageDealQueryRequest struct {
	Cid *cid.Cid
}

func (sm *StorageMiner) handleQuery(s inet.Stream) {
	defer s.Close() // nolint: errcheck

	var q storageDealQueryRequest
	if err := cbu.NewMsgReader(s).ReadMsg(&q); err != nil {
		panic(err)
	}

	ctx := context.Background()
	resp := sm.Query(ctx, q.Cid)

	if err := cbu.NewMsgWriter(s).WriteMsg(resp); err != nil {
		panic(err)
	}
}

// StorageMinerClient is a client interface to the StorageMiner
type StorageMinerClient struct {
	nd *Node

	deals   map[string]*clientStorageDealState
	dealsLk sync.Mutex
}

// NewStorageMinerClient creaters a new storage miner client
func NewStorageMinerClient(nd *Node) *StorageMinerClient {
	return &StorageMinerClient{
		nd:    nd,
		deals: make(map[string]*clientStorageDealState),
	}
}

type clientStorageDealState struct {
	miner     address.Address
	proposal  *StorageDealProposal
	lastState *StorageDealResponse
}

func getFileSize(ctx context.Context, c *cid.Cid, dserv ipld.DAGService) (uint64, error) {
	fnode, err := dserv.Get(ctx, c)
	if err != nil {
		return 0, err
	}
	switch n := fnode.(type) {
	case *dag.ProtoNode:
		return unixfs.DataSize(n.Data())
	case *dag.RawNode:
		return n.Size()
	default:
		return 0, fmt.Errorf("unrecognized node type: %T", fnode)
	}

}

// TryToStoreData needs a better name
func (smc *StorageMinerClient) TryToStoreData(ctx context.Context, miner address.Address, data *cid.Cid, duration uint64, price *types.AttoFIL) (*cid.Cid, error) {
	size, err := getFileSize(ctx, data, dag.NewDAGService(smc.nd.Blockservice))
	if err != nil {
		return nil, err
	}

	proposal := &StorageDealProposal{
		PieceRef:   data,
		Size:       types.NewBytesAmount(size),
		TotalPrice: price,
		Duration:   duration,
		//Payment:    PaymentInfo{},
		//Signature:  nil, // TODO: sign this
	}

	pid, err := smc.nd.Lookup.GetPeerIDByMinerAddress(ctx, miner)
	if err != nil {
		return nil, err
	}

	s, err := smc.nd.Host.NewStream(ctx, pid, StorageDealProtocolID)
	if err != nil {
		return nil, err
	}

	if err := cbu.NewMsgWriter(s).WriteMsg(proposal); err != nil {
		return nil, err
	}

	var response StorageDealResponse
	if err := cbu.NewMsgReader(s).ReadMsg(&response); err != nil {
		return nil, err
	}

	if err := smc.checkDealResponse(ctx, &response); err != nil {
		return nil, err
	}

	// TODO: send the miner the data (currently it gets requested by the miner, out of band)

	if err := smc.addResponseToTracker(&response, miner, proposal); err != nil {
		return nil, err
	}

	return response.Proposal, nil
}

func (smc *StorageMinerClient) addResponseToTracker(resp *StorageDealResponse, miner address.Address, p *StorageDealProposal) error {
	smc.dealsLk.Lock()
	defer smc.dealsLk.Unlock()
	k := resp.Proposal.KeyString()
	_, ok := smc.deals[k]
	if ok {
		return fmt.Errorf("deal in progress with that cid already exists")
	}

	smc.deals[k] = &clientStorageDealState{
		lastState: resp,
		miner:     miner,
		proposal:  p,
	}

	return nil
}

func (smc *StorageMinerClient) checkDealResponse(ctx context.Context, resp *StorageDealResponse) error {
	switch resp.State {
	case Rejected:
		return fmt.Errorf("deal rejected: %s", resp.Message)
	case Failed:
		return fmt.Errorf("deal failed: %s", resp.Message)
	default:
		return fmt.Errorf("invalid proposal response")
	case Accepted:
		return nil
	}
}

func (smc *StorageMinerClient) minerForProposal(c *cid.Cid) (address.Address, error) {
	smc.dealsLk.Lock()
	defer smc.dealsLk.Unlock()
	st, ok := smc.deals[c.KeyString()]
	if !ok {
		return address.Address{}, fmt.Errorf("no such proposal by cid: %s", c)
	}

	return st.miner, nil
}

// Query queries an in-progress proposal
func (smc *StorageMinerClient) Query(ctx context.Context, c *cid.Cid) (*StorageDealResponse, error) {
	mineraddr, err := smc.minerForProposal(c)
	if err != nil {
		return nil, err
	}

	minerpid, err := smc.nd.Lookup.GetPeerIDByMinerAddress(ctx, mineraddr)
	if err != nil {
		return nil, err
	}

	s, err := smc.nd.Host.NewStream(ctx, minerpid, StorageDealQueryProtocolID)
	if err != nil {
		return nil, err
	}

	q := storageDealQueryRequest{c}
	if err := cbu.NewMsgWriter(s).WriteMsg(q); err != nil {
		return nil, err
	}

	var resp StorageDealResponse
	if err := cbu.NewMsgReader(s).ReadMsg(&resp); err != nil {
		return nil, err
	}

	return &resp, nil
}
