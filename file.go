package squashfs

import (
	"io"
	"io/fs"

	"github.com/CalebQ42/squashfs/internal/directory"
	"github.com/CalebQ42/squashfs/internal/inode"
)

//File represents a file within a squashfs archive. Implements fs.File and fs.DirEntry
type File struct {
	*FileCore
	rdr       *fileReader
	directory *directory.Directory
	dirIndex  int
	mode      fs.FileMode
}

//AsFile returns the file as a File.
func (f *FileCore) AsFile() *File {
	fil := File{
		FileCore: f,
		mode:     fs.FileMode(f.in.Permissions),
	}
	if fil.IsSymlink() {
		fil.mode |= fs.ModeSymlink
	}
	if fil.IsDir() {
		fil.mode |= fs.ModeDir
		var err error
		fil.directory, err = fil.r.readDirFromInode(fil.in)
		if err != nil {
			fil.directory = nil
		}
	}
	if fil.IsFile() {
		rdr, err := f.newFileReader()
		if err == nil {
			fil.rdr = rdr
		}
	}
	return &fil
}

//Name is the file's name (not including path)
func (f *File) Name() string {
	return f.name
}

//Mode returns the fs.FileMode of the squashfs.File
func (f *File) Mode() fs.FileMode {
	return f.mode
}

//Type is the same as File.Mode().Type(). Here to satisfy fs.DirEntry
func (f *File) Type() fs.FileMode {
	return f.mode.Type()
}

//Info returns itself as a fs.FileInfo. Here to satisfy fs.DirEntry
func (f *File) Info() (fs.FileInfo, error) {
	return f, nil
}

//Stat returns itself as a fs.FileInfo. Here to satisfy fs.File
func (f *File) Stat() (fs.FileInfo, error) {
	return f, nil
}

//Size returns the size of the file if IsFile. Otherwise, returns 0.
func (f *File) Size() int64 {
	switch f.in.Type {
	case inode.ExtFileType:
		return int64(f.in.Info.(inode.ExtFile).Size)
	case inode.FileType:
		return int64(f.in.Info.(inode.File).Size)
	default:
		return 0
	}
}

//Sys returns the file's underlying reader that implements io.Reader and io.WriteTo
func (f *File) Sys() interface{} {
	return f.rdr
}

//ReadDir reads n
func (f *File) ReadDir(n int) (out []fs.DirEntry, err error) {
	if !f.IsDir() || f.directory == nil {
		return nil, errNotDirectory
	}
	var eof bool
	var beg int
	var end int
	if n <= 0 {
		beg = 0
		end = len(f.directory.Entries)
	} else {
		beg = f.dirIndex
		if beg+n > len(f.directory.Entries) {
			end = len(f.directory.Entries)
			eof = true
		} else {
			end = beg + n
		}
		f.dirIndex = end
	}
	var fil *FileCore
	for i, entry := range f.directory.Entries[beg:end] {
		fil, err = f.r.newFileFromDirEntry(entry)
		if err != nil {
			f.dirIndex = i
			return
		}
		out = append(out, fil.AsFile())
	}
	if eof {
		err = io.EOF
	}
	return
}

//Read reads data into p
func (f *File) Read(p []byte) (int, error) {
	if f.rdr == nil {
		return 0, fs.ErrClosed
	}
	return f.rdr.Read(p)
}

//WriteTo writes all data to the given writer
func (f *File) WriteTo(w io.Writer) (int64, error) {
	if f.rdr == nil {
		return 0, fs.ErrClosed
	}
	return f.rdr.WriteTo(w)
}

//Close closes the squsahfs File's underlying reader (it just sets it to nil)
func (f *File) Close() error {
	if f.rdr == nil {
		return fs.ErrClosed
	}
	f.rdr = nil
	return nil
}
