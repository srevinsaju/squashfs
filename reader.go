package squashfs

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"time"

	"github.com/CalebQ42/squashfs/internal/compression"
	"github.com/CalebQ42/squashfs/internal/inode"
)

const (
	magic uint32 = 0x73717368
)

var (
	//ErrNoMagic is returned if the magic number in the superblock isn't correct.
	errNoMagic = errors.New("Magic number doesn't match. Either isn't a squashfs or corrupted")
	//ErrIncompatibleCompression is returned if the compression type in the superblock doesn't work.
	errIncompatibleCompression = errors.New("Compression type unsupported")
	//ErrCompressorOptions is returned if compressor options is present. It's not currently supported.
	errCompressorOptions = errors.New("Compressor options is not currently supported")
	//ErrOptions is returned when compression options that I haven't tested is set. When this is returned, the Reader is also returned.
	ErrOptions = errors.New("Possibly incompatible compressor options")
)

//Reader processes and reads a squashfs archive.
type Reader struct {
	*FileFS      //root directory
	r            *io.SectionReader
	decompressor compression.Decompressor
	fragOffsets  []uint64
	idTable      []uint32
	super        superblock
	flags        SuperblockFlags
}

//NewSquashfsReader returns a new squashfs.Reader from an io.ReaderAt
func NewSquashfsReader(r io.ReaderAt) (*Reader, error) {
	var rdr Reader
	err := binary.Read(io.NewSectionReader(r, 0, 96), binary.LittleEndian, &rdr.super)
	if err != nil {
		return nil, err
	}
	if rdr.super.Magic != magic {
		return nil, errNoMagic
	}
	if rdr.super.BlockLog != uint16(math.Log2(float64(rdr.super.BlockSize))) {
		return nil, errors.New("BlockSize and BlockLog doesn't match. The archive is probably corrupt")
	}
	rdr.r = io.NewSectionReader(r, 0, int64(rdr.super.BytesUsed))
	_, err = rdr.r.Seek(96, io.SeekStart)
	if err != nil {
		return nil, err
	}
	hasUnsupportedOptions := false
	rdr.flags = rdr.super.GetFlags()
	if rdr.flags.compressorOptions {
		switch rdr.super.CompressionType {
		case GzipCompression:
			var gzip *compression.Gzip
			gzip, err = compression.NewGzipCompressorWithOptions(rdr.r)
			if err != nil {
				return nil, err
			}
			if gzip.HasCustomWindow || gzip.HasStrategies {
				hasUnsupportedOptions = true
			}
			rdr.decompressor = gzip
		case XzCompression:
			var xz *compression.Xz
			xz, err = compression.NewXzCompressorWithOptions(rdr.r)
			if err != nil {
				return nil, err
			}
			if xz.HasFilters {
				return nil, errors.New("XZ compression options has filters. These are not yet supported")
			}
			rdr.decompressor = xz
		case Lz4Compression:
			var lz4 *compression.Lz4
			lz4, err = compression.NewLz4CompressorWithOptions(rdr.r)
			if err != nil {
				return nil, err
			}
			rdr.decompressor = lz4
		case ZstdCompression:
			var zstd *compression.Zstd
			zstd, err = compression.NewZstdCompressorWithOptions(rdr.r)
			if err != nil {
				return nil, err
			}
			rdr.decompressor = zstd
		default:
			return nil, errIncompatibleCompression
		}
	} else {
		switch rdr.super.CompressionType {
		case GzipCompression:
			rdr.decompressor = &compression.Gzip{}
		case LzmaCompression:
			rdr.decompressor = &compression.Lzma{}
		case XzCompression:
			rdr.decompressor = &compression.Xz{}
		case Lz4Compression:
			rdr.decompressor = &compression.Lz4{}
		case ZstdCompression:
			rdr.decompressor = &compression.Zstd{}
		default:
			//TODO: all compression types.
			return nil, errIncompatibleCompression
		}
	}
	fragBlocks := int(math.Ceil(float64(rdr.super.FragCount) / 512))
	rdr.fragOffsets = make([]uint64, fragBlocks)
	if fragBlocks > 0 {
		_, err = rdr.r.Seek(int64(rdr.super.FragTableStart), io.SeekStart)
		if err != nil {
			return nil, err
		}
		err = binary.Read(rdr.r, binary.LittleEndian, &rdr.fragOffsets)
		if err != nil {
			return nil, err
		}
	}
	offsets := rdr.super.IDCount / 2048
	if rdr.super.IDCount%2048 > 0 {
		offsets++
	}
	blockOffsets := make([]uint64, offsets)
	_, err = rdr.r.Seek(int64(rdr.super.IDTableStart), io.SeekStart)
	if err != nil {
		return nil, err
	}
	err = binary.Read(rdr.r, binary.LittleEndian, &blockOffsets)
	if err != nil {
		return nil, err
	}
	unread := rdr.super.IDCount
	for i := 0; i < len(blockOffsets); i++ {
		var idRdr io.Reader
		idRdr, err = rdr.newMetadataReader(blockOffsets[i])
		if err != nil {
			return nil, err
		}
		var read uint16
		if unread > 2048 {
			read = 2048
		} else {
			read = unread
		}
		tmpSlice := make([]uint32, read)
		err = binary.Read(idRdr, binary.LittleEndian, &tmpSlice)
		if err != nil {
			return nil, err
		}
		rdr.idTable = append(rdr.idTable, tmpSlice...)
		unread -= read
	}
	rootInReader, err := rdr.newMetadataReaderFromInodeRef(rdr.super.RootInodeRef)
	if err != nil {
		return nil, err
	}
	rootIn, err := inode.ProcessInode(rootInReader, rdr.super.BlockSize)
	if err != nil {
		return nil, err
	}
	rootFil, err := rdr.newFileFromInode(rootIn)
	if err != nil {
		return nil, err
	}
	rdr.FileFS, err = rootFil.AsFS()
	if err != nil {
		return nil, err
	}
	if hasUnsupportedOptions {
		return &rdr, ErrOptions
	}
	return &rdr, nil
}

//ModTime is the last time the file was modified/created.
func (r *Reader) ModTime() time.Time {
	return time.Unix(int64(r.super.CreationTime), 0)
}
