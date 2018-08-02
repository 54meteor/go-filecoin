package node

import (
	"bytes"
	"context"
	"io/ioutil"
	"os"
	"sync"
	"testing"

	"gx/ipfs/QmYVNvtQkeZ6AKSwDrjQTs432QtL6umrrK41EBq3cu7iSP/go-cid"
	dag "gx/ipfs/QmfGzdovkTAhGni3Wfg2fTEtNxhpwWSyAJWW2cC1pWM9TS/go-merkledag"

	"github.com/filecoin-project/go-filecoin/repo"
	"github.com/filecoin-project/go-filecoin/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var sectorDirsForTest = &repo.MemRepo{}
var testSectorSize = 64

func TestSimple(t *testing.T) {
	require := require.New(t)
	nd := MakeOfflineNode(t)
	sb := requireSectorBuilder(require, nd, testSectorSize)
	sector, err := sb.NewSector()
	require.NoError(err)

	d1Data := []byte("hello world")
	d1 := &PieceInfo{
		DealID: 5,
		Size:   uint64(len(d1Data)),
	}

	if err := sector.WritePiece(d1, bytes.NewReader(d1Data)); err != nil {
		t.Fatal(err)
	}

	ag := types.NewAddressForTestGetter()
	_, err = sb.Seal(sector, ag(), makeFilecoinParameters(testSectorSize, nodeSize))
	if err != nil {
		t.Fatal(err)
	}
}

func requireSectorBuilder(require *require.Assertions, nd *Node, sectorSize int) *SectorBuilder {
	sb, err := InitSectorBuilder(nd, types.MakeTestAddress("bar"), sectorSize, sectorDirsForTest)
	require.NoError(err)

	sb.publicParameters = makeFilecoinParameters(sectorSize, nodeSize)
	sb.setup = func(minerKey []byte, parameters *PublicParameters, data []byte) ([]byte, error) {
		reverseBytes(data)
		return data, nil
	}

	require.NoError(err)
	return sb
}

func requirePieceInfo(require *require.Assertions, nd *Node, bytes []byte) *PieceInfo {
	data := dag.NewRawNode(bytes)
	err := nd.Blockservice.AddBlock(data)
	require.NoError(err)
	return &PieceInfo{
		Ref:    data.Cid(),
		Size:   uint64(len(bytes)),
		DealID: 0, // FIXME parameterize
	}
}

func TestSectorBuilder(t *testing.T) {
	defer sectorDirsForTest.CleanupSectorDirs()
	assert := assert.New(t)
	require := require.New(t)
	ctx := context.Background()

	fname := newSectorLabel()
	assert.Len(fname, 32) // Sanity check, nothing more.

	nd := MakeOfflineNode(t)

	var sealingWg sync.WaitGroup
	var sealingErr error
	sealingWg.Add(1)

	sb := requireSectorBuilder(require, nd, testSectorSize)
	sector := sb.CurSector

	sb.OnCommitmentAddedToMempool = func(ss *SealedSector, msgCid *cid.Cid, err error) {
		if ss.sectorLabel == sector.Label {
			sealingErr = err
			sealingWg.Done()
		}
	}

	requireAddPiece := func(s string) {
		err := sb.AddPiece(ctx, requirePieceInfo(require, nd, []byte(s)))
		assert.NoError(err)
	}

	metadataMustMatch(require, sb, sb.CurSector, 0)

	// New paths are in the right places.
	stagingPath, _ := sb.newSectorPath()
	sealedPath, _ := sb.newSealedSectorPath()
	assert.Contains(stagingPath, sb.stagingDir)
	assert.Contains(sealedPath, sb.sealedDir)

	// New paths are generated each time.
	stpath2, _ := sb.newSectorPath()
	sepath2, _ := sb.newSealedSectorPath()
	assert.NotEqual(stagingPath, stpath2)
	assert.NotEqual(sealedPath, sepath2)

	metadataMustMatch(require, sb, sb.CurSector, 0)
	text := "What's our vector, sector?" // len(text) = 26
	requireAddPiece(text)
	assert.Equal(sector, sb.CurSector)
	all := text

	metadataMustMatch(require, sb, sector, 1)

	d := requireReadAll(require, sector)
	assert.Equal(all, string(d))
	assert.Nil(sector.sealed)

	text2 := "We have clearance, Clarence." // len(text2) = 28
	requireAddPiece(text2)
	assert.Equal(sector, sb.CurSector)
	all += text2

	d2 := requireReadAll(require, sector)
	assert.Equal(all, string(d2))
	assert.Nil(sector.sealed)

	// persisted and calculated metadata match.
	metadataMustMatch(require, sb, sector, 2)

	text3 := "I'm too sexy for this sector." // len(text3) = 29
	requireAddPiece(text3)

	// wait for sector sealing to complete
	sealingWg.Wait()
	require.NoError(sealingErr)

	assert.NotEqual(sector, sb.CurSector)

	// persisted and calculated metadata match after a sector is sealed.
	metadataMustMatch(require, sb, sector, 2)

	newSector := sb.CurSector
	d4 := requireReadAll(require, newSector)
	metadataMustMatch(require, sb, newSector, 1)

	assert.Equal(text3, d4)
	sealed := sector.sealed
	assert.NotNil(sealed)
	assert.Nil(newSector.sealed)

	assert.Equal(sealed.sectorLabel, sector.Label)
	assert.Equal(sealed.pieces, sector.Pieces)
	assert.Equal(sealed.size, sector.Size)
	sealedData, err := sealed.ReadFile()
	assert.NoError(err)

	// Create a padded copy of 'all' and reverse it -- which is what our bogus setup function does.
	bytes := make([]byte, testSectorSize)
	copy(bytes, all)
	reverseBytes(bytes)

	assert.Equal(string(bytes), string(sealedData))

	meta := sb.CurSector.SectorMetadata()
	assert.Len(meta.Pieces, 1)
	assert.Equal(uint64(testSectorSize), meta.Size)
	assert.Equal(testSectorSize-len(text3), int(meta.Free))

	text4 := "I am text, and I am long. My reach exceeds my grasp exceeds exceeds my allotted space."
	err = sb.AddPiece(ctx, requirePieceInfo(require, nd, []byte(text4)))
	assert.EqualError(err, ErrPieceTooLarge.Error())
}

func TestSectorBuilderMetadata(t *testing.T) {
	t.Run("creating datastore keys", func(t *testing.T) {
		assert := assert.New(t)

		fname := newSectorLabel()
		assert.Len(fname, 32) // Sanity check, nothing more.

		label := "SECTORFILENAMEWHATEVER"

		k := metadataKey(label).String()
		// Don't accidentally test Datastore namespacing implementation.
		assert.Contains(k, "sectors")
		assert.Contains(k, "metadata")
		assert.Contains(k, label)

		merkleRoot := ([]byte)("someMerkleRootLOL")
		k2 := sealedMetadataKey(merkleRoot).String()
		// Don't accidentally test Datastore namespacing implementation.
		assert.Contains(k2, "sealedSectors")
		assert.Contains(k2, "metadata")
		assert.Contains(k2, merkleString(merkleRoot))
	})

	t.Run("sealing sector moves metadata", func(t *testing.T) {
		require := require.New(t)

		ctx := context.Background()

		nd := MakeOfflineNode(t)

		var sealingWg sync.WaitGroup
		var sealingErr error
		sealingWg.Add(1)

		bytesA := make([]byte, 10+(sectorSize/2))
		bytesB := make([]byte, (sectorSize/2)-10)

		sb := requireSectorBuilder(require, nd, sectorSize)
		sector := sb.CurSector

		sb.OnCommitmentAddedToMempool = func(ss *SealedSector, msgCid *cid.Cid, err error) {
			if ss.sectorLabel == sector.Label {
				sealingErr = err
				sealingWg.Done()
			}
		}

		sb.AddPiece(ctx, requirePieceInfo(require, nd, bytesA))
		sectormeta, err := sb.store.getSectorMetadata(sector.Label)
		require.NoError(err)
		require.NotNil(sectormeta)

		sb.AddPiece(ctx, requirePieceInfo(require, nd, bytesB))

		// wait for sector sealing to complete
		sealingWg.Wait()
		require.NoError(sealingErr)

		_, err = sb.store.getSectorMetadata(sector.Label)
		require.Error(err)
		require.Contains(err.Error(), "not found")

		sealedmeta, err := sb.store.getSealedSectorMetadata(sector.sealed.merkleRoot)
		require.NoError(err)
		require.NotNil(sealedmeta)

		require.Equal(sector.Size, sealedmeta.Size)
		require.Equal(len(sector.Pieces), len(sealedmeta.Pieces))
		for i := 0; i < len(sector.Pieces); i++ {
			pieceInfoMustEqual(t, sector.Pieces[i], sealedmeta.Pieces[i])
		}
	})
}

func TestSectorStore(t *testing.T) {
	t.Run("it loads a persisted sector", func(t *testing.T) {
		require := require.New(t)

		ctx := context.Background()

		nd := MakeOfflineNode(t)

		bytesA := make([]byte, 10+(sectorSize/2))

		sb := requireSectorBuilder(require, nd, sectorSize)
		sector := sb.CurSector

		sb.AddPiece(ctx, requirePieceInfo(require, nd, bytesA))

		loaded, err := sb.store.getSector(sector.Label)
		require.NoError(err)

		sectorsMustEqual(t, sector, loaded)
	})

	t.Run("it loads a persisted, sealed sector", func(t *testing.T) {
		require := require.New(t)

		ctx := context.Background()

		nd := MakeOfflineNode(t)

		var sealingWg sync.WaitGroup
		var sealingErr error
		sealingWg.Add(1)

		bytesA := make([]byte, 10+(sectorSize/2))
		bytesB := make([]byte, (sectorSize/2)-10)

		sb := requireSectorBuilder(require, nd, sectorSize)
		sector := sb.CurSector

		sb.OnCommitmentAddedToMempool = func(ss *SealedSector, msgCid *cid.Cid, err error) {
			if ss.sectorLabel == sector.Label {
				sealingErr = err
				sealingWg.Done()
			}
		}

		sb.AddPiece(ctx, requirePieceInfo(require, nd, bytesA))
		sb.AddPiece(ctx, requirePieceInfo(require, nd, bytesB))

		// wait for sector sealing to complete
		sealingWg.Wait()
		require.NoError(sealingErr)

		require.Equal(1, len(sb.SealedSectors))
		sealedSector := sb.SealedSectors[0]

		loaded, err := sb.store.getSealedSector(sealedSector.merkleRoot)
		require.NoError(err)
		sealedSectorsMustEqual(t, sealedSector, loaded)
	})
}

func TestInitializesSectorBuilderFromPersistedState(t *testing.T) {
	require := require.New(t)

	ctx := context.Background()

	nd := MakeOfflineNode(t)

	var sealingWg sync.WaitGroup
	var sealingErr error
	sealingWg.Add(1)

	bytesA := make([]byte, 10+(sectorSize/2))
	bytesB := make([]byte, (sectorSize/2)-10)

	sbA := requireSectorBuilder(require, nd, sectorSize)
	sector := sbA.CurSector

	sbA.OnCommitmentAddedToMempool = func(ss *SealedSector, msgCid *cid.Cid, err error) {
		if ss.sectorLabel == sector.Label {
			sealingErr = err
			sealingWg.Done()
		}
	}

	sbA.AddPiece(ctx, requirePieceInfo(require, nd, bytesA))

	// sector builder B should have the same state as sector builder A
	sbB := requireSectorBuilder(require, nd, sectorSize)

	// can't compare sectors with Equal(s1, s2) because their "file" fields will differ
	sectorBuildersMustEqual(t, sbA, sbB)

	// trigger sealing by adding a second piece
	sbA.AddPiece(ctx, requirePieceInfo(require, nd, bytesB))

	// wait for sealing to complete
	sealingWg.Wait()
	require.NoError(sealingErr)

	// sector builder C should have the same state as sector builder A
	sbC := requireSectorBuilder(require, nd, sectorSize)

	sectorBuildersMustEqual(t, sbA, sbC)
}

func TestTruncatesUnsealedSectorOnDiskIfMismatch(t *testing.T) {
	t.Run("it truncates the file if file size > metadata size", func(t *testing.T) {
		require := require.New(t)

		ctx := context.Background()

		nd := MakeOfflineNode(t)

		sbA, err := InitSectorBuilder(nd, nd.RewardAddress(), sectorSize, sectorDirsForTest)
		require.NoError(err)

		sbA.AddPiece(ctx, requirePieceInfo(require, nd, make([]byte, 10)))
		sbA.AddPiece(ctx, requirePieceInfo(require, nd, make([]byte, 20)))
		sbA.AddPiece(ctx, requirePieceInfo(require, nd, make([]byte, 50)))

		metaA, err := sbA.store.getSectorMetadata(sbA.CurSector.Label)
		require.NoError(err)

		infoA, err := os.Stat(metaA.Filename)
		require.NoError(err)

		// size of file on disk should match what we've persisted as metadata
		require.Equal(int(metaA.Size-metaA.Free), int(infoA.Size()))

		// perform an out-of-band write to the file (replaces its contents)
		ioutil.WriteFile(metaA.Filename, make([]byte, 90), 0600)

		// initialize a new sector builder (simulates the node restarting)
		sbB, err := InitSectorBuilder(nd, nd.RewardAddress(), sectorSize, sectorDirsForTest)
		require.NoError(err)

		metaB, err := sbB.store.getSectorMetadata(sbB.CurSector.Label)
		require.NoError(err)

		infoB, err := os.Stat(metaB.Filename)
		require.NoError(err)

		// ensure that the file was truncated to match metadata
		require.Equal(int(metaB.Size-metaB.Free), int(infoB.Size()))
		require.Equal(int(infoA.Size()), int(infoB.Size()))
	})

	t.Run("it truncates the metadata if file size < metadata size", func(t *testing.T) {
		require := require.New(t)

		ctx := context.Background()

		nd := MakeOfflineNode(t)

		sbA, err := InitSectorBuilder(nd, nd.RewardAddress(), sectorSize, sectorDirsForTest)
		require.NoError(err)

		sbA.AddPiece(ctx, requirePieceInfo(require, nd, make([]byte, 10)))
		sbA.AddPiece(ctx, requirePieceInfo(require, nd, make([]byte, 20)))
		sbA.AddPiece(ctx, requirePieceInfo(require, nd, make([]byte, 50)))

		metaA, err := sbA.store.getSectorMetadata(sbA.CurSector.Label)
		require.NoError(err)

		// truncate the file such that its size < sum(size-of-pieces)
		require.NoError(os.Truncate(metaA.Filename, int64(40)))

		// initialize final sector builder
		sbB, err := InitSectorBuilder(nd, nd.RewardAddress(), sectorSize, sectorDirsForTest)
		require.NoError(err)

		metaB, err := sbA.store.getSectorMetadata(sbB.CurSector.Label)
		require.NoError(err)

		infoB, err := os.Stat(metaB.Filename)
		require.NoError(err)

		// ensure metadata was truncated
		require.Equal(2, len(metaB.Pieces))
		require.Equal(30, int(metaB.Size-metaB.Free))

		// ensure that the file was truncated to match metadata
		require.Equal(int(metaB.Size-metaB.Free), int(infoB.Size()))
	})
}

func metadataMustMatch(require *require.Assertions, sb *SectorBuilder, sector *Sector, pieces int) {
	sealed := sector.sealed
	if sealed != nil {
		sealedMeta := sealed.SealedSectorMetadata()
		sealedMetaPersisted, err := sb.store.getSealedSectorMetadata(sealed.merkleRoot)
		require.NoError(err)
		require.Equal(sealedMeta, sealedMetaPersisted)
	} else {
		meta := sector.SectorMetadata()
		require.Len(meta.Pieces, pieces)

		// persisted and calculated metadata match.
		metaPersisted, err := sb.store.getSectorMetadata(sector.Label)
		require.NoError(err)
		require.Equal(metaPersisted, meta)
	}

	builderMeta := sb.SectorBuilderMetadata()
	builderMetaPersisted, err := sb.store.getSectorBuilderMetadata(sb.MinerAddr)
	require.NoError(err)
	require.Equal(builderMeta, builderMetaPersisted)
}

func pieceInfoMustEqual(t *testing.T, p1 *PieceInfo, p2 *PieceInfo) {
	if p1.Size != p2.Size {
		t.Fatalf("p1.Size(%d) != p2.Size(%d)\n", p1.Size, p2.Size)
	}

	if p1.DealID != p2.DealID {
		t.Fatalf("p1.DealID(%d) != p2.DealID(%d)\n", p1.DealID, p2.DealID)
	}

	if !p1.Ref.Equals(p2.Ref) {
		t.Fatalf("p1.Ref(%s) != p2.Ref(%s)\n", p1.Ref.String(), p2.Ref.String())
	}
}

func sectorBuildersMustEqual(t *testing.T, sb1 *SectorBuilder, sb2 *SectorBuilder) {
	require := require.New(t)

	require.Equal(sb1.MinerAddr, sb2.MinerAddr)
	require.Equal(sb1.sealedDir, sb2.sealedDir)
	require.Equal(sb1.sectorSize, sb2.sectorSize)
	require.Equal(sb1.stagingDir, sb2.stagingDir)

	sectorsMustEqual(t, sb1.CurSector, sb2.CurSector)

	require.Equal(len(sb1.SealedSectors), len(sb2.SealedSectors))
	for i := 0; i < len(sb1.SealedSectors); i++ {
		sealedSectorsMustEqual(t, sb1.SealedSectors[i], sb2.SealedSectors[i])
	}
}

func sealedSectorsMustEqual(t *testing.T, ss1 *SealedSector, ss2 *SealedSector) {
	require := require.New(t)

	if ss1 == nil && ss2 == nil {
		return
	}

	require.Equal(ss1.filename, ss2.filename)
	require.Equal(ss1.label, ss2.label)
	require.Equal(ss1.sectorLabel, ss2.sectorLabel)
	require.Equal(ss1.size, ss2.size)
	require.True(bytes.Equal(ss1.merkleRoot, ss2.merkleRoot))

	require.Equal(len(ss1.pieces), len(ss2.pieces))
	for i := 0; i < len(ss1.pieces); i++ {
		pieceInfoMustEqual(t, ss1.pieces[i], ss2.pieces[i])
	}
}

func sectorsMustEqual(t *testing.T, s1 *Sector, s2 *Sector) {
	require := require.New(t)

	require.Equal(s1.filename, s2.filename)
	require.Equal(s1.Free, s2.Free)
	require.Equal(s1.ID, s2.ID)
	require.Equal(s1.Label, s2.Label)
	require.Equal(s1.Size, s2.Size)

	sealedSectorsMustEqual(t, s1.sealed, s2.sealed)

	require.Equal(len(s1.Pieces), len(s2.Pieces))
	for i := 0; i < len(s1.Pieces); i++ {
		pieceInfoMustEqual(t, s1.Pieces[i], s2.Pieces[i])
	}
}

func requireReadAll(require *require.Assertions, sector *Sector) string {
	data, err := sector.ReadFile()
	require.NoError(err)

	return string(data)
}

func reverseBytes(s []byte) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}
