package node

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"io"
	"io/ioutil"
	"math/big"
	"os"
	"path"
	"strings"
	"syscall"

	dag "gx/ipfs/QmNUCLv5fmUBuAcwbkt58NQvMcJgd5FPCYV2yNCXq4Wnd6/go-ipfs/merkledag"
	uio "gx/ipfs/QmNUCLv5fmUBuAcwbkt58NQvMcJgd5FPCYV2yNCXq4Wnd6/go-ipfs/unixfs/io"
	mmap "gx/ipfs/QmPXvegq26x982cQjSfbTvSzZXn7GiaMwhhVPHkeTEhrPT/sys/unix"
	cbor "gx/ipfs/QmRiRJhn427YVuufBEHofLreKWNw7P7BWNq86Sb9kzqdbd/go-ipld-cbor"
	"gx/ipfs/QmVmDhyTTUcQXFD1rRQ64fGLMSAoaQvNH3hwuaCFAPq2hy/errors"
	"gx/ipfs/QmcZfnkapfECQGcLZaf9B79NRg7cRa9EnZh4LSbkCzwNvY/go-cid"
	ipld "gx/ipfs/Qme5bWv7wtjUNGsK2BNGVUFPKiuxWrsqrtvYwCLRw8YFES/go-ipld-format"

	drg "github.com/filecoin-project/go-proofs/porep/drgporep"
	"github.com/filecoin-project/go-proofs/porep/drgporep/drgraph"

	"github.com/filecoin-project/go-filecoin/abi"
	"github.com/filecoin-project/go-filecoin/types"
	"github.com/filecoin-project/go-filecoin/util/binpack"
)

func init() {
	cbor.RegisterCborType(PieceInfo{})
}

// ErrPieceTooLarge is an error indicating that a piece cannot be larger than the sector into which it is written.
var ErrPieceTooLarge = errors.New("piece too large for sector")

const sectorSize = 1024
const nodeSize = 16

var noSectorID = big.NewInt(-1)

// SectorDirs describes the methods required to supply sector directories to a SectorBuilder.
type SectorDirs interface {
	StagingDir() string
	SealedDir() string
}

// PieceInfo is information about a filecoin piece
type PieceInfo struct {
	Ref    *cid.Cid
	Size   uint64
	DealID uint64
}

// SectorBuilder manages packing deals into sectors
// maybe this belongs somewhere else as part of a different thing?
type SectorBuilder struct {
	MinerAddr types.Address

	// TODO: information about all sectors needs to be persisted to disk
	CurSector        *Sector
	SealedSectors    []*SealedSector
	G                *drgraph.Graph
	Prover           *drg.Prover
	Packer           binpack.Packer
	publicParameters *PublicParameters

	// OnCommitmentAddedToMempool is called when a sector has been sealed
	// and its commitment added to the message pool.
	OnCommitmentAddedToMempool func(*SealedSector, *cid.Cid, error)

	stagingDir string
	sealedDir  string

	// yada yada don't hold a reference to this here, just take what you need
	nd    *Node
	store *sectorStore
	dserv ipld.DAGService

	sectorSize uint64

	setup func([]byte, *PublicParameters, []byte) ([]byte, error)
}

var _ binpack.Binner = &SectorBuilder{}

// Sector is a filecoin storage sector. A miner fills this up with data, and
// then seals it. The first part I can do, the second part needs to be figured out more.
// Somehow, I turn this piecemap and backing data buffer into something that the chain can verify.
// So something X has to be computed from this that convinces the chain that
// this miner is storing all the deals referenced in the piece map...
type Sector struct {
	Size   uint64
	Free   uint64
	Pieces []*PieceInfo
	ID     int64

	Label    string
	filename string
	file     *os.File
	sealed   *SealedSector
}

var _ binpack.Bin = &Sector{}

// SealedSector is a sector that has been sealed by the PoRep setup process
type SealedSector struct {
	filename    string
	label       string
	sectorLabel string
	merkleRoot  []byte
	pieces      []*PieceInfo
	size        uint64
}

// GetCurrentBin implements Binner.
func (sb *SectorBuilder) GetCurrentBin() binpack.Bin {
	return sb.CurSector
}

// AddItem implements binpack.Binner.
func (sb *SectorBuilder) AddItem(ctx context.Context, item binpack.Item, bin binpack.Bin) error {
	pi := item.(*PieceInfo)
	s := bin.(*Sector)
	root, err := sb.dserv.Get(ctx, pi.Ref)
	if err != nil {
		return err
	}

	r, err := uio.NewDagReader(ctx, root, sb.dserv)
	if err != nil {
		return err
	}

	if err := s.WritePiece(pi, r); err != nil {
		return err
	}

	return nil
}

// CloseBin implements binpack.Binner.
func (sb *SectorBuilder) CloseBin(bin binpack.Bin) {
	go func() {
		sector := bin.(*Sector)
		msgCid, err := sb.SealAndAddCommitmentToMempool(context.Background(), sector)
		if err != nil {
			sb.OnCommitmentAddedToMempool(nil, nil, errors.Wrap(err, "failed to seal and commit sector"))
		}

		// TODO: maybe send these values to a channel instead of calling the
		// callback directly
		sb.OnCommitmentAddedToMempool(sector.sealed, msgCid, nil)
	}()
}

// NewBin implements binpack.Binner.
func (sb *SectorBuilder) NewBin() (binpack.Bin, error) {
	return sb.NewSector()
}

// BinSize implements binpack.Binner.
func (sb *SectorBuilder) BinSize() binpack.Space {
	return binpack.Space(sb.sectorSize)
}

// ItemSize implements binpack.Binner.
func (sb *SectorBuilder) ItemSize(item binpack.Item) binpack.Space {
	return binpack.Space(item.(*PieceInfo).Size)
}

// SpaceAvailable implements binpack.Binner.
func (sb *SectorBuilder) SpaceAvailable(bin binpack.Bin) binpack.Space {
	return binpack.Space(bin.(*Sector).Free)
}

// End binpack.Binner implementation

// NewSector allocates and returns a new Sector with file initialized, along with any error.
func (sb *SectorBuilder) NewSector() (s *Sector, err error) {
	p, label := sb.newSectorPath()
	s = &Sector{
		Size:  sb.sectorSize,
		Free:  sb.sectorSize,
		Label: label,
	}
	err = os.MkdirAll(path.Dir(p), os.ModePerm)
	if err != nil {
		return s, errors.Wrap(err, "failed to create sector directory")
	}
	s.filename = p
	s.file, err = os.Create(p)
	if err != nil {
		return s, errors.Wrap(err, "failed to create sector file")
	}
	return s, s.file.Close()
}

// NewSealedSector allocates and returns a new SealedSector from merkleRoot and a sector. This method moves the sector's
// file into the sealed directory from the staging directory.
func (sb *SectorBuilder) NewSealedSector(merkleRoot []byte, s *Sector) (ss *SealedSector, err error) {
	p, label := sb.newSealedSectorPath()

	if err := os.MkdirAll(path.Dir(p), os.ModePerm); err != nil {
		return nil, errors.Wrap(err, "failed to create sealed sector path")
	}

	// move the now-sealed file out of staging
	if err := os.Rename(s.filename, p); err != nil {
		return nil, errors.Wrap(err, "failed to move file from staging to sealed directory")
	}

	s.filename = ""

	ss = &SealedSector{
		filename:    p,
		label:       label,
		merkleRoot:  merkleRoot,
		pieces:      s.Pieces,
		sectorLabel: s.Label,
		size:        s.Size,
	}

	sb.SealedSectors = append(sb.SealedSectors, ss)

	return ss, nil
}

// InitSectorBuilder creates a new sector builder for the given miner. If a SectorBuilder had previously been created
// for the given miner, we reconstruct it using metadata from the datastore so that the miner can resume its work where
// it left off.
func InitSectorBuilder(nd *Node, minerAddr types.Address, sectorSize int, fs SectorDirs) (*SectorBuilder, error) {
	store := &sectorStore{
		store: nd.Repo.Datastore(),
	}

	g := drgraph.NewDRSample(sectorSize / nodeSize)

	sb := &SectorBuilder{
		dserv:            dag.NewDAGService(nd.Blockservice),
		G:                g,
		MinerAddr:        minerAddr,
		nd:               nd,
		Prover:           drg.NewProver("cats", g, nodeSize),
		publicParameters: makeFilecoinParameters(sectorSize, nodeSize),
		sectorSize:       uint64(sectorSize),
		setup:            proverSetup,
		store:            store,
		stagingDir:       fs.StagingDir(),
		sealedDir:        fs.SealedDir(),
	}

	sb.OnCommitmentAddedToMempool = sb.onCommitmentAddedToMempool

	metadata, err := store.getSectorBuilderMetadata(minerAddr)
	if err == nil {
		sector, err := store.getSector(metadata.CurSectorLabel)
		if err != nil {
			return nil, err
		}
		sb.CurSector = sector

		for _, root := range metadata.SealedSectorMerkleRoots {
			sealed, err := store.getSealedSector(root)
			if err != nil {
				return nil, err
			}
			sb.SealedSectors = append(sb.SealedSectors, sealed)
		}

		np := &binpack.NaivePacker{}
		np.InitWithCurrentBin(sb)
		sb.Packer = np

		return sb, nil
	}

	if err != nil && strings.Contains(err.Error(), "not found") {
		packer, firstBin, err := binpack.NewNaivePacker(sb)
		if err != nil {
			return nil, err
		}
		sb.CurSector = firstBin.(*Sector)
		sb.Packer = packer

		return sb, sb.checkpoint(sb.CurSector)
	}

	return nil, err
}

func (sb *SectorBuilder) onCommitmentAddedToMempool(*SealedSector, *cid.Cid, error) {
	// TODO: wait for commitSector message to be included in a block so that we
	// can update sealed sector metadata with the miner-created SectorID
}

// AddPiece writes the given piece into a sector
func (sb *SectorBuilder) AddPiece(ctx context.Context, pi *PieceInfo) error {
	bin, err := sb.Packer.AddItem(ctx, pi)
	if err == binpack.ErrItemTooLarge {
		return ErrPieceTooLarge
	}
	if err == nil {
		if bin == nil {
			// What does this signify? Could use to signal something.
			panic("no bin returned from Packer.AddItem")
		}
		sb.CurSector = bin.(*Sector)
	}

	// checkpoint after we've added the piece and updated the sector builder's
	// "current sector"
	if err := sb.checkpoint(sb.CurSector); err != nil {
		return err
	}

	return err
}

// Create filecoin parameters (mainly used for testing).
func makeFilecoinParameters(sectorSize int, nodeSize int) *PublicParameters {
	return &PublicParameters{
		graph:     drgraph.NewDRSample(sectorSize / nodeSize),
		blockSize: uint64(nodeSize),
	}
}

// OpenAppend opens and sets sector's file for appending, returning the *os.file.
func (s *Sector) OpenAppend() error {
	f, err := os.OpenFile(s.filename, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	s.file = f
	return nil
}

// openMmap opens a file and maps it into memory.
func openMmap(filename string, fileSize uint64) ([]byte, *os.File, error) {
	if err := os.Truncate(filename, int64(fileSize)); err != nil {
		return nil, nil, errors.Wrap(err, "failed to truncate file")
	}

	file, err := os.OpenFile(filename, os.O_RDWR, 0600)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to open file")
	}

	prot := syscall.PROT_READ | syscall.PROT_WRITE

	data, err := mmap.Mmap(int(file.Fd()), int64(0), int(fileSize), prot, syscall.MAP_SHARED)
	if err != nil {
		return nil, nil, errors.Wrap(err, "failed to mmap file")
	}

	if err := mmap.Madvise(data, syscall.MADV_RANDOM); err != nil {
		return nil, nil, errors.Wrap(err, "failed to madvise mmap")
	}

	return data, file, nil
}

// closeMmap unmaps a memory map and closes its file.
func closeMmap(data []byte, file *os.File) error {
	if err := mmap.Munmap(data); err != nil {
		return errors.Wrap(err, "failed to delete sector's memory map")
	}

	if err := file.Close(); err != nil {
		return errors.Wrap(err, "failed to close sector file")
	}

	return nil
}

// SealAndAddCommitmentToMempool seals the given sector, adds the sealed sector's commitment to the message pool, and
// then returns the CID of the commitment message.
func (sb *SectorBuilder) SealAndAddCommitmentToMempool(ctx context.Context, s *Sector) (*cid.Cid, error) {
	ss, err := sb.Seal(s, sb.MinerAddr, sb.publicParameters)
	if err != nil {
		// Hard to say what to do in this case.
		// Depending on the error, it could be "try again"
		// or 'verify data integrity and try again'
		return nil, errors.Wrap(err, "failed to seal sector")
	}

	s.sealed = ss
	if err := sb.checkpoint(s); err != nil {
		return nil, errors.Wrap(err, "failed to create checkpoint")
	}

	msgCid, err := sb.AddCommitmentToMempool(ctx, ss)
	if err != nil {
		// 'try again'
		// This can fail if the miners owner doesnt have enough funds to pay gas.
		// It can also happen if the miner included a deal in this sector that
		// is already sealed in a different sector.
		return nil, errors.Wrap(err, "failed to seal and add sector commitment")
	}

	return msgCid, nil
}

// AddCommitmentToMempool adds the sealed sector's commitment to the message pool and returns the CID of the commitment
// message.
func (sb *SectorBuilder) AddCommitmentToMempool(ctx context.Context, ss *SealedSector) (*cid.Cid, error) {
	var deals []uint64
	for _, p := range ss.pieces {
		deals = append(deals, p.DealID)
	}

	args, err := abi.ToEncodedValues(
		noSectorID, // NB: we might already know the sector ID from having created it already
		ss.merkleRoot,
		deals,
	)
	if err != nil {
		return nil, errors.Wrap(err, "failed to ABI encode commitSector arguments")
	}

	minerOwner := types.Address{} // TODO: get the miner owner to send this from
	msg := types.NewMessage(minerOwner, sb.MinerAddr, 0, nil, "commitSector", args)

	if err := sb.nd.AddNewMessage(ctx, msg); err != nil {
		return nil, errors.Wrap(err, "pushing out commitSector message failed")
	}

	msgCid, err := msg.Cid()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get commitSector message CID")
	}

	return msgCid, nil
}

// WritePiece writes data from the given reader to the sectors underlying storage
func (s *Sector) WritePiece(pi *PieceInfo, r io.Reader) (finalErr error) {
	if err := s.OpenAppend(); err != nil {
		return err
	}
	// Capture any error from the deferred close as the value to return unless previous error was being returned.
	defer func() {
		err := s.file.Close()
		if err != nil && finalErr == nil {
			finalErr = errors.Wrap(err, "failed to close sector's file after writing piece")
		}
	}()

	n, err := io.Copy(s.file, r)
	if err != nil {
		// TODO: make sure we roll back the state of the file to what it was before we started this write
		// Note, this should be a fault. We likely should already have all the
		// data locally before calling this method. If that ends up being the
		// case, this error signifies disk errors of some kind
		return err
	}

	// TODO: We should be writing this out to a file in the 'staging' area. Once the
	// sector is ready to be sealed, we should mmap it and pass it to the
	// sealing code. The sealing code prefers fairly random access to the data,
	// so using mmap will be the fastest option. We could also provide an
	// interface to just do seeks and reads on the underlying file, but it
	// won't be as optimized

	if uint64(n) != pi.Size {
		// TODO: make sure we roll back the state of the file to what it was before we started this write
		return fmt.Errorf("reported piece size does not match what we read")
	}

	s.Free -= pi.Size
	s.Pieces = append(s.Pieces, pi)

	return nil
}

// PublicParameters is
type PublicParameters struct {
	graph     *drgraph.Graph
	blockSize uint64
}

func proverSetup(minerKey []byte, publicParams *PublicParameters, data []byte) ([]byte, error) {
	prover := drg.NewProver(string(minerKey), publicParams.graph, int(publicParams.blockSize))
	root, err := prover.Setup(NewMemoryReadWriter(data))

	if err != nil {
		return nil, errors.Wrap(err, "prover setup failed")
	}

	return root, nil
}

// Seal runs the PoRep setup process using the given parameters
// address may turn into a secret key
func (sb *SectorBuilder) Seal(s *Sector, minerID types.Address, params *PublicParameters) (_ *SealedSector, finalErr error) {
	data, file, err := openMmap(s.filename, s.Size)
	if err != nil {
		return nil, errors.Wrap(err, "failed to open sector's memory map")
	}

	// Capture any error from the deferred close as the value to return unless
	// previous error was being returned.
	defer func() {
		err := closeMmap(data, file)
		if err != nil && finalErr == nil {
			finalErr = errors.Wrap(err, "failed to close sector's memory map")
		}
	}()

	// AES key must be 16, 24, or 32 bytes. Failure to pad here breaks sealing.
	// TODO how should this key be derived?
	minerKey := make([]byte, 32)
	copy(minerKey[:len(minerID)], minerID[:])

	root, err := sb.setup(minerKey, params, data)
	if err != nil {
		return nil, errors.Wrap(err, "PoRep setup process failed")
	}

	copiedRoot := make([]byte, len(root))
	copy(copiedRoot, root)

	// On some distros (OSX/Darwin), munmap with MAP_SHARED will cause memory
	// to be written back to disk "automatically at some point in the future."
	// To force memory to be written back to the disk, we msync with MS_SYNC
	// before unmapping.
	if err := mmap.Msync(data, syscall.MS_SYNC); err != nil {
		return nil, errors.Wrap(err, "failed to msync sector's data")
	}

	return sb.NewSealedSector(copiedRoot, s)
}

// ReadFile reads the content of a Sector's file into a byte array and returns it along with any error.
func (s *Sector) ReadFile() ([]byte, error) {
	return ioutil.ReadFile(s.filename)
}

// ReadFile reads the content of a SealedSector's file into a byte array and returns it along with any error.
func (ss *SealedSector) ReadFile() ([]byte, error) {
	return ioutil.ReadFile(ss.filename)
}

// newSectorLabel returns a newly-generated, random 32-character base32 string.
func newSectorLabel() string {
	c := 20
	b := make([]byte, c)
	_, err := rand.Read(b)
	if err != nil {
		panic("wtf")
	}
	encoded := base32.StdEncoding.EncodeToString(b)
	return encoded
}

// newSectorPath returns a path to a new (random) filename in the staging directory.
func (sb *SectorBuilder) newSectorPath() (pathname string, label string) {
	label = newSectorLabel()
	pathname = path.Join(sb.stagingDir, label)
	return pathname, label
}

// newSealedSectorPath returns a path to a new (random) filename in the sealed directory.
func (sb *SectorBuilder) newSealedSectorPath() (pathname string, label string) {
	label = newSectorLabel()
	pathname = path.Join(sb.sealedDir, label)
	return pathname, label
}

func merkleString(merkleRoot []byte) string {
	return base32.StdEncoding.EncodeToString(merkleRoot)
}

// MemoryReadWriter implements data.ReadWriter and represents a simple byte slice in memory.
type MemoryReadWriter struct {
	data []byte
}

// NewMemoryReadWriter returns MemoryReadWriter initialized with data.
func NewMemoryReadWriter(data []byte) MemoryReadWriter {
	return MemoryReadWriter{data: data}
}

// DataAt implements data.ReadWriter.
func (mrw MemoryReadWriter) DataAt(offset, length uint64, cb func([]byte) error) error {
	if length == 0 {
		// Define 0 length to mean read/write to end of data.
		return cb(mrw.data[offset:])
	}
	return cb(mrw.data[offset : offset+length])
}
