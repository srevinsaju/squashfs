package squashfs

import (
	"bytes"
	"errors"
	"io"

	"github.com/CalebQ42/squashfs/internal/inode"
)

//FileReader provides a io.Reader interface for files within a squashfs archive
type fileReader struct {
	data         *dataReader
	fil          *FileCore
	fragmentData []byte
	fragged      bool
	fragOnly     bool
	read         int
	FileSize     int //FileSize is the total size of the given file
}

var (
	//ErrPathIsNotFile returns when trying to read from a file, but the given path is NOT a file.
	errPathIsNotFile = errors.New("The given path is not a file")
)

//ReadFile provides a squashfs.FileReader for the file at the given location.
func (f *FileCore) newFileReader() (*fileReader, error) {
	var rdr fileReader
	rdr.fil = f
	if f.in.Type != inode.FileType && f.in.Type != inode.ExtFileType {
		return nil, errPathIsNotFile
	}
	switch f.in.Type {
	case inode.FileType:
		fil := f.in.Info.(inode.File)
		rdr.fragged = fil.Fragmented
		rdr.fragOnly = fil.BlockStart == 0
		rdr.FileSize = int(fil.Size)
	case inode.ExtFileType:
		fil := f.in.Info.(inode.ExtFile)
		rdr.fragged = fil.Fragmented
		rdr.fragOnly = fil.BlockStart == 0
		rdr.FileSize = int(fil.Size)
	}
	var err error
	if rdr.fragged {
		rdr.fragmentData, err = f.r.getFragmentDataFromInode(f.in)
		if err != nil {
			return nil, err
		}
	}
	if !rdr.fragOnly {
		rdr.data, err = f.r.newDataReaderFromInode(f.in)
	}
	return &rdr, nil
}

func (f *fileReader) Read(p []byte) (int, error) {
	if f.fragOnly {
		n, err := bytes.NewBuffer(f.fragmentData[f.read:]).Read(p)
		f.read += n
		if err != nil {
			return n, err
		}
		return n, nil
	}
	var read int
	n, err := f.data.Read(p)
	read += n
	if f.fragged && err == io.EOF {
		if f.fragmentData == nil {
			f.fragmentData, err = f.fil.r.getFragmentDataFromInode(f.fil.in)
		}
		n, err = bytes.NewBuffer(f.fragmentData).Read(p[read:])
		read += n
		if err != nil {
			return read, err
		}
	} else if err != nil {
		return read, err
	}
	return read, nil
}

func (f *fileReader) WriteTo(w io.Writer) (int64, error) {
	if f.fragOnly {
		n, err := w.Write(f.fragmentData)
		return int64(n), err
	}
	if !f.fragged {
		return f.data.WriteTo(w)
	}
	n, err := f.data.WriteTo(w)
	if err != nil {
		return int64(n), err
	}
	nn, err := w.Write(f.fragmentData)
	return int64(nn) + n, err
}
