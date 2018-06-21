package node

import (
	cbor "gx/ipfs/QmRiRJhn427YVuufBEHofLreKWNw7P7BWNq86Sb9kzqdbd/go-ipld-cbor"
	"gx/ipfs/QmVmDhyTTUcQXFD1rRQ64fGLMSAoaQvNH3hwuaCFAPq2hy/errors"
	ds "gx/ipfs/QmXRKBQA4wXP7xWbFiZsR1GP4HV6wMDQ1aWFxZZ4uBcPX9/go-datastore"

	"github.com/filecoin-project/go-filecoin/repo"
	"github.com/filecoin-project/go-filecoin/types"
)

func init() {
	cbor.RegisterCborType(SectorMetadata{})
	cbor.RegisterCborType(SealedSectorMetadata{})
	cbor.RegisterCborType(SectorBuilderMetadata{})
}

// SectorMetadata represent the persistent metadata associated with a Sector.
type SectorMetadata struct {
	StagingPath string
	Pieces      []*PieceInfo
	Size        uint64
	Free        uint64
	Filename    string
	ID          int64
}

// SealedSectorMetadata represent the persistent metadata associated with a SealedSector.
type SealedSectorMetadata struct {
	Filename    string
	Label       string
	MerkleRoot  []byte
	Pieces      []*PieceInfo
	SectorLabel string
	Size        uint64
}

// SectorBuilderMetadata represent the persistent metadata associated with a SectorBuilder.
type SectorBuilderMetadata struct {
	CurSectorLabel          string
	MinerAddr               types.Address
	SealedSectorMerkleRoots [][]byte
}

// SectorMetadata returns the metadata associated with a Sector.
func (s *Sector) SectorMetadata() *SectorMetadata {
	meta := &SectorMetadata{
		Filename:    s.filename,
		Free:        s.Free,
		Pieces:      s.Pieces,
		Size:        s.Size,
		StagingPath: s.filename,
	}

	return meta
}

// SealedSectorMetadata returns the metadata associated with a SealedSector.
func (ss *SealedSector) SealedSectorMetadata() *SealedSectorMetadata {
	meta := &SealedSectorMetadata{
		Filename:    ss.filename,
		Label:       ss.label,
		MerkleRoot:  ss.merkleRoot,
		Pieces:      ss.pieces,
		SectorLabel: ss.sectorLabel,
		Size:        ss.size,
	}

	return meta
}

// SectorBuilderMetadata returns the metadata associated with a SectorBuilderMetadata.
func (sb *SectorBuilder) SectorBuilderMetadata() *SectorBuilderMetadata {
	meta := SectorBuilderMetadata{
		MinerAddr:               sb.MinerAddr,
		CurSectorLabel:          sb.CurSector.Label,
		SealedSectorMerkleRoots: make([][]byte, len(sb.SealedSectors)),
	}
	for i, sealed := range sb.SealedSectors {
		meta.SealedSectorMerkleRoots[i] = sealed.merkleRoot
	}
	return &meta
}

func metadataKey(label string) ds.Key {
	path := []string{"sectors", "metadata"}
	return ds.KeyWithNamespaces(path).Instance(label)
}

func sealedMetadataKey(merkleRoot []byte) ds.Key {
	path := []string{"sealedSectors", "metadata"}
	return ds.KeyWithNamespaces(path).Instance(merkleString(merkleRoot))
}

func builderMetadataKey(minerAddress types.Address) ds.Key {
	path := []string{"builder", "metadata"}
	return ds.KeyWithNamespaces(path).Instance(minerAddress.String())
}

type sectorStore struct {
	store repo.Datastore
}

// getSealedSector returns the sealed sector with the given merkle root or an error if no match was found.
func (st *sectorStore) getSealedSector(merkleRoot []byte) (*SealedSector, error) {
	metadata, err := st.getSealedSectorMetadata(merkleRoot)
	if err != nil {
		return nil, err
	}

	return &SealedSector{
		filename:    metadata.Filename,
		label:       metadata.Label,
		merkleRoot:  metadata.MerkleRoot,
		pieces:      metadata.Pieces,
		sectorLabel: metadata.SectorLabel,
		size:        metadata.Size,
	}, nil
}

// getSector returns the sector with the given label or an error if no match was found.
func (st *sectorStore) getSector(label string) (*Sector, error) {
	metadata, err := st.getSectorMetadata(label)
	if err != nil {
		return nil, err
	}

	s := &Sector{
		Size:     metadata.Size,
		Free:     metadata.Free,
		Pieces:   metadata.Pieces,
		ID:       metadata.ID,
		Label:    label,
		filename: metadata.Filename,
	}

	if err := s.OpenAppend(); err != nil {
		return nil, errors.Wrap(err, "failed to open sector file")
	}

	return s, nil
}

// getSectorMetadata returns the metadata for a sector with the given label or an error if no match was found.
func (st *sectorStore) getSectorMetadata(label string) (*SectorMetadata, error) {
	key := metadataKey(label)

	data, err := st.store.Get(key)
	if err != nil {
		return nil, err
	}
	var m SectorMetadata
	if err := cbor.DecodeInto(data.([]byte), &m); err != nil {
		return nil, err
	}
	return &m, err
}

// getSealedSectorMetadata returns the metadata for a sealed sector with the given merkle root.
func (st *sectorStore) getSealedSectorMetadata(merkleRoot []byte) (*SealedSectorMetadata, error) {
	key := sealedMetadataKey(merkleRoot)

	data, err := st.store.Get(key)
	if err != nil {
		return nil, err
	}
	var m SealedSectorMetadata
	if err := cbor.DecodeInto(data.([]byte), &m); err != nil {
		return nil, err
	}

	return &m, err
}

// getSectorBuilderMetadata returns the metadata for a miner's SectorBuilder.
func (st *sectorStore) getSectorBuilderMetadata(minerAddr types.Address) (*SectorBuilderMetadata, error) {
	key := builderMetadataKey(minerAddr)

	data, err := st.store.Get(key)
	if err != nil {
		return nil, err
	}
	var m SectorBuilderMetadata
	if err := cbor.DecodeInto(data.([]byte), &m); err != nil {
		return nil, err
	}
	return &m, err
}

func (st *sectorStore) setSectorMetadata(label string, meta *SectorMetadata) error {
	key := metadataKey(label)
	data, err := cbor.DumpObject(meta)
	if err != nil {
		return err
	}
	return st.store.Put(key, data)
}

func (st *sectorStore) deleteSectorMetadata(label string) error {
	key := metadataKey(label)
	return st.store.Delete(key)
}

func (st *sectorStore) setSealedSectorMetadata(merkleRoot []byte, meta *SealedSectorMetadata) error {
	key := sealedMetadataKey(merkleRoot)
	data, err := cbor.DumpObject(meta)
	if err != nil {
		return err
	}
	return st.store.Put(key, data)
}

func (st *sectorStore) setSectorBuilderMetadata(minerAddress types.Address, meta *SectorBuilderMetadata) error {
	key := builderMetadataKey(minerAddress)
	data, err := cbor.DumpObject(meta)
	if err != nil {
		return err
	}
	return st.store.Put(key, data)
}

// TODO: Sealed sector metadata and sector metadata shouldn't exist in the
// datastore at the same time, and sector builder metadata needs to be kept
// in sync with sealed sector metadata (e.g. which sectors are sealed).
// This is the method to enforce these rules. Unfortunately this means that
// we're making more writes to the datastore than we really need to be
// doing. As the SectorBuilder evolves, we will introduce some checks which
// will optimize away redundant writes to the datastore.
func (sb *SectorBuilder) checkpoint(s *Sector) error {
	if err := sb.store.setSectorBuilderMetadata(sb.MinerAddr, sb.SectorBuilderMetadata()); err != nil {
		return errors.Wrap(err, "failed to save builder metadata")
	}

	if s.sealed == nil {
		if err := sb.store.setSectorMetadata(s.Label, s.SectorMetadata()); err != nil {
			return errors.Wrap(err, "failed to save sector metadata")
		}
	} else {
		if err := sb.store.setSealedSectorMetadata(s.sealed.merkleRoot, s.sealed.SealedSectorMetadata()); err != nil {
			return errors.Wrap(err, "failed to save sealed sector metadata")
		}

		if err := sb.store.deleteSectorMetadata(s.Label); err != nil {
			return errors.Wrap(err, "failed to remove sector metadata")
		}
	}

	return nil
}
