package squashfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"github.com/CalebQ42/squashfs/internal/directory"
	"github.com/CalebQ42/squashfs/internal/inode"
)

//File stuff. Re-write of File
type File struct {
	r        *Reader
	in       *inode.Inode
	reader   *fileReader
	parent   *FileFS
	name     string
	entries  []*directory.Entry
	dirsRead int
}

func (r *Reader) newFileFromInode(in *inode.Inode) (fil *File, err error) {
	fil = new(File)
	fil.in = in
	fil.r = r
	if fil.IsRegular() {
		fil.reader, err = r.newFileReader(fil.in)
		if err != nil {
			return nil, err
		}
	} else if fil.IsDir() {
		fil.entries, err = r.readDirFromInode(fil.in)
		if err != nil {
			return nil, err
		}
	}
	return
}

func (r *Reader) newFileFromDirEntry(entry *directory.Entry) (*File, error) {
	br, err := r.newMetadataReader(r.super.InodeTableStart + uint64(entry.InodeOffset))
	if err != nil {
		return nil, err
	}
	_, err = br.Seek(int64(entry.InodeBlockOffset), io.SeekStart)
	if err != nil {
		return nil, err
	}
	in, err := inode.ProcessInode(br, r.super.BlockSize)
	if err != nil {
		return nil, err
	}
	fil, err := r.newFileFromInode(in)
	if err != nil {
		return nil, err
	}
	fil.name = entry.Name
	return fil, nil
}

//AsFS returns the File as a FileFS, which implement fs.FS
func (f *File) AsFS() (*FileFS, error) {
	return &FileFS{
		f: f,
	}, nil
}

//Name the name of the File. Root is named "/"
func (f *File) Name() string {
	return f.name
}

//Size is the size in bytes of the File. 0 if it's not a normal file.
func (f *File) Size() int64 {
	switch f.in.Type {
	case inode.FileType:
		return int64(f.in.Info.(inode.File).Size)
	case inode.ExtFileType:
		return int64(f.in.Info.(inode.ExtFile).Size)
	default:
		return 0
	}
}

//Mode returns the fs.FileMode of the File.
func (f *File) Mode() fs.FileMode {
	var mode fs.FileMode = fs.FileMode(f.in.Permissions)
	if f.IsDir() {
		mode |= fs.ModeDir
	}
	if f.IsSymlink() {
		mode |= fs.ModeSymlink
	}
	return mode
}

//ModTime the last time the given file was modified.
func (f *File) ModTime() time.Time {
	return time.Unix(int64(f.in.ModifiedTime), 0)
}

//IsDir returns whether the given File is a directory
func (f *File) IsDir() bool {
	return f.in.Type == inode.DirType || f.in.Type == inode.ExtDirType
}

//ReadDir reads i fs.DirEntrys from the File. If i <= 0 all entries are returned.
//Subsequesnt calls will return new entries. io.EOF is returned at the end and then it's reset.
func (f *File) ReadDir(i int) ([]fs.DirEntry, error) {
	if !f.IsDir() {
		return nil, errors.New("ReadDir called on a non-directory")
	}
	var beg, end int
	var err error
	if i >= 0 {
		beg, end = 0, len(f.entries)
	} else {
		beg, end = f.dirsRead, f.dirsRead+i
		if end > len(f.entries) {
			err = io.EOF
			end = len(f.entries)
		}
	}
	var out []fs.DirEntry
	for _, ent := range f.entries[beg:end] {
		e := &DirEntry{
			en: ent,
			r:  f.r,
		}
		e.parent = f
		out = append(out, e)
	}
	if err != nil {
		f.dirsRead = 0
	}
	return out, err
}

//IsSymlink returns if the File is a Symlink
func (f *File) IsSymlink() bool {
	return f.in.Type == inode.SymType || f.in.Type == inode.ExtSymType
}

//IsRegular returns if the File is a regular file
func (f *File) IsRegular() bool {
	return f.in.Type == inode.FileType || f.in.Type == inode.ExtFileType
}

//Sys returns the File's underlying reader if IsRegular
func (f *File) Sys() interface{} {
	return f.reader
}

//Stat returns the File cast as a fs.FileInfo
func (f *File) Stat() (fs.FileInfo, error) {
	return f, nil
}

//Read reads bytes into p
func (f *File) Read(p []byte) (int, error) {
	if !f.IsRegular() {
		return 0, io.EOF
	}
	return f.reader.Read(p)
}

//WriteTo writes all data to the io.Writer. This is preferred to using straight Read calls since it's threaded.
func (f *File) WriteTo(w io.Writer) (int64, error) {
	return f.WriteTo(w)
}

//Close nils the underlying reader, but otherwise is useless.
func (f *File) Close() error {
	f.reader = nil
	return nil
}

//ExtractionOptions is ued with ExtractWithOptions to customize a File's extraction.
type ExtractionOptions struct {
	DereferenceSymlink bool
	UnbreakSymlink     bool
	Verbose            bool
	FolderPerm         fs.FileMode //The permissions used when creating the base folder
}

//DefaultExtractionOptions creates a new ExtractionOptions with the default values.
func DefaultExtractionOptions() ExtractionOptions {
	return ExtractionOptions{
		DereferenceSymlink: false,
		UnbreakSymlink:     false,
		Verbose:            false,
		FolderPerm:         fs.ModePerm,
	}
}

//ExtractTo extracts the file with the default extraction options.
func (f *File) ExtractTo(dir string) []error {
	return f.ExtractWithOptions(dir, DefaultExtractionOptions())
}

//ExtractSymlink extracts the file to dir, but replaces symlinks with their target file.
func (f *File) ExtractSymlink(dir string) []error {
	op := DefaultExtractionOptions()
	op.DereferenceSymlink = true
	return f.ExtractWithOptions(dir, op)
}

//ExtractWithOptions will extract the file to the given path, while allowing customization on how it works. ExtractTo is the "default" options.
//Will try it's best to extract all files, and if any errors come up, they will be appended to the error slice that's returned.
//Should only return multiple errors if extracting a folder.
//
//If dereferenceSymlink is set, instead of extracting a symlink, it will extract the file the symlink is pointed to in it's place.
//If both dereferenceSymlink and unbreakSymlink is set, dereferenceSymlink takes precendence.
//
//If unbreakSymlink is set, it will also try to extract the symlink's associated file. WARNING: the symlink's file may have to go up the directory to work.
//If unbreakSymlink is set and the file cannot be extracted, a ErrBrokenSymlink will be appended to the returned error slice.
//
//folderPerm only applies to the folders created to get to path. Folders from the archive are given the correct permissions defined by the archive.
func (f *File) ExtractWithOptions(dir string, options ExtractionOptions) (errs []error) {
	errs = make([]error, 0)
	dir = path.Clean(dir)
	err := os.MkdirAll(dir, options.FolderPerm)
	if err != nil {
		return []error{err}
	}
	switch {
	case f.IsDir():
		if f.name != "/" {
			//TODO: check if folder is present, and if so, try to set it's permission
			err = os.Mkdir(dir+"/"+f.name, os.ModePerm)
			if !os.IsExist(err) && err != nil {
				if options.Verbose {
					fmt.Println("Error while making: ", dir+"/"+f.name)
					fmt.Println(err)
				}
				errs = append(errs, err)
				return
			}
			var fil *os.File
			fil, err = os.Open(dir + "/" + f.name)
			if err != nil {
				if options.Verbose {
					fmt.Println("Error while opening:", dir+"/"+f.name)
					fmt.Println(err)
				}
				errs = append(errs, err)
				return
			}
			fil.Chown(int(f.r.idTable[f.in.Header.UID]), int(f.r.idTable[f.in.Header.GID]))
			//don't mention anything when it fails. Because it fails often. Probably has something to do about uid & gid 0
			err = fil.Chmod(f.Mode())
			if err != nil {
				if options.Verbose {
					fmt.Println("Error while changing owner:", dir+"/"+f.name)
					fmt.Println(err)
				}
				errs = append(errs, err)
			}
		}
		var children []fs.DirEntry
		children, err = f.ReadDir(0)
		if err != nil {
			if options.Verbose {
				fmt.Println("Error getting children for:", f.Name())
				fmt.Println(err)
			}
			errs = append(errs, err)
			return
		}
		finishChan := make(chan []error)
		for _, child := range children {
			go func(child fs.DirEntry) {
				info, infErr := child.Info()
				if err != nil {
					finishChan <- []error{infErr}
				}

				if f.name == "" {
					finishChan <- info.(*File).ExtractWithOptions(dir, options)
				} else {
					finishChan <- info.(*File).ExtractWithOptions(dir+"/"+f.name, options)
				}
			}(child)
		}
		for range children {
			errs = append(errs, (<-finishChan)...)
		}
		return
	case f.IsRegular():
		var fil *os.File
		fil, err = os.Create(dir + "/" + f.name)
		if os.IsExist(err) {
			err = os.Remove(dir + "/" + f.name)
			if err != nil {
				if options.Verbose {
					fmt.Println("Error while making:", dir+"/"+f.name)
					fmt.Println(err)
				}
				errs = append(errs, err)
				return
			}
			fil, err = os.Create(dir + "/" + f.name)
			if err != nil {
				if options.Verbose {
					fmt.Println("Error while making:", dir+"/"+f.name)
					fmt.Println(err)
				}
				errs = append(errs, err)
				return
			}
		} else if err != nil {
			if options.Verbose {
				fmt.Println("Error while making:", dir+"/"+f.name)
				fmt.Println(err)
			}
			errs = append(errs, err)
			return
		} //Since we will be reading from the file
		_, err = io.Copy(fil, f.Sys().(io.Reader))
		if err != nil {
			if options.Verbose {
				fmt.Println("Error while Copying data to:", dir+"/"+f.name)
				fmt.Println(err)
			}
			errs = append(errs, err)
			return
		}
		fil.Chown(int(f.r.idTable[f.in.Header.UID]), int(f.r.idTable[f.in.Header.GID]))
		//don't mention anything when it fails. Because it fails often. Probably has something to do about uid & gid 0
		// if err != nil {
		// 	if verbose {
		// 		fmt.Println("Error while changing owner:", path+"/"+f.Name)
		// 		fmt.Println(err)
		// 	}
		// 	errs = append(errs, err)
		// 	return
		// }
		err = fil.Chmod(f.Mode())
		if err != nil {
			if options.Verbose {
				fmt.Println("Error while setting permissions for:", dir+"/"+f.name)
				fmt.Println(err)
			}
			errs = append(errs, err)
		}
		return
	case f.IsSymlink():
		symPath := f.SymlinkPath()
		if options.DereferenceSymlink {
			fil := f.GetSymlinkFile()
			if fil == nil {
				if options.Verbose {
					fmt.Println("Symlink path(", symPath, ") is outside the archive:"+dir+"/"+f.name)
				}
				return
			}
			fil.name = f.name
			extracSymErrs := fil.ExtractWithOptions(dir, options)
			if len(extracSymErrs) > 0 {
				if options.Verbose {
					fmt.Println("Error(s) while extracting the symlink's file:", dir+"/"+f.name)
					fmt.Println(extracSymErrs)
				}
				errs = append(errs, extracSymErrs...)
			}
			return
		} else if options.UnbreakSymlink {
			fil := f.GetSymlinkFile()
			if fil != nil {
				symPath = dir + "/" + symPath
				paths := strings.Split(symPath, "/")
				extracSymErrs := fil.ExtractWithOptions(strings.Join(paths[:len(paths)-1], "/"), options)
				if len(extracSymErrs) > 0 {
					if options.Verbose {
						fmt.Println("Error(s) while extracting the symlink's file:", dir+"/"+f.name)
						fmt.Println(extracSymErrs)
					}
					errs = append(errs, extracSymErrs...)
				}
			} else {
				if options.Verbose {
					fmt.Println("Symlink path(", symPath, ") is outside the archive:"+dir+"/"+f.name)
				}
				return
			}
		}
		err = os.Symlink(f.SymlinkPath(), dir+"/"+f.name)
		if err != nil {
			if options.Verbose {
				fmt.Println("Error while making symlink:", dir+"/"+f.name)
				fmt.Println(err)
			}
			errs = append(errs, err)
		}
	}
	return
}

//SymlinkPath returns the path the symlink is pointing to. If the file ISN'T a symlink, will return an empty string.
//If a path begins with "/" then the symlink is pointing to an absolute path (starting from root, and not a file inside the archive)
func (f *File) SymlinkPath() string {
	switch f.in.Type {
	case inode.SymType:
		return f.in.Info.(inode.Sym).Path
	case inode.ExtSymType:
		return f.in.Info.(inode.ExtSym).Path
	default:
		return ""
	}
}

//GetSymlinkFile tries to return the squashfs.File associated with the symlink. If the file isn't a symlink
//or the symlink points to a location outside the archive, nil is returned.
func (f *File) GetSymlinkFile() *File {
	if !f.IsSymlink() {
		return nil
	}
	if strings.HasSuffix(f.SymlinkPath(), "/") {
		return nil
	}
	fil, err := f.parent.Open(f.SymlinkPath())
	if err != nil {
		return nil
	}
	return fil.(*File)
}

//GetSymlinkFileRecursive tries to return the squasfs.File associated with the symlink. It will recursively
//try to get the symlink's file. This will return either a non-symlink File, or nil.
func (f *File) GetSymlinkFileRecursive() *File {
	if !f.IsSymlink() {
		return nil
	}
	if strings.HasSuffix(f.SymlinkPath(), "/") {
		return nil
	}
	sym := f
	for {
		sym = sym.GetSymlinkFile()
		if sym == nil {
			return nil
		}
		if !sym.IsSymlink() {
			return sym
		}
	}
}

//DirEntry is an entry directory.Entry that implement fs.DirEntry
type DirEntry struct {
	en     *directory.Entry
	r      *Reader
	parent *File
}

//Name is the name of the file associated with the DirEntry
func (d DirEntry) Name() string {
	return d.en.Name
}

//IsDir returns whether or not the DirEntry represents a directory
func (d DirEntry) IsDir() bool {
	return d.en.Type == inode.DirType
}

//Type returns the fs.FileMode type bits of the file.
func (d DirEntry) Type() fs.FileMode {
	switch d.en.Type {
	case inode.DirType:
		return fs.ModeDir
	case inode.SymType:
		return fs.ModeSymlink
	case inode.SocketType:
		return fs.ModeSocket
	case inode.BlockDevType:
		return fs.ModeDevice
	case inode.CharDevType:
		return fs.ModeCharDevice
	}
	return 0
}

//Info returns the File associated with the DirEntry.
func (d DirEntry) Info() (fs.FileInfo, error) {
	if d.r == nil {
		return nil, errors.New("No squashfs.Reader specified")
	}
	fil, err := d.r.newFileFromDirEntry(d.en)
	if err != nil {
		return nil, err
	}
	if d.parent != nil {
		fil.parent, _ = d.parent.AsFS()
	}
	return fil, nil
}

//ReadDirFromInode returns a fully populated Directory from a given Inode.
//If the given inode is not a directory it returns an error.
func (r *Reader) readDirFromInode(i *inode.Inode) ([]*directory.Entry, error) {
	var offset uint32
	var metaOffset uint16
	var size uint32
	switch i.Type {
	case inode.DirType:
		offset = i.Info.(inode.Dir).DirectoryIndex
		metaOffset = i.Info.(inode.Dir).DirectoryOffset
		size = uint32(i.Info.(inode.Dir).DirectorySize)
	case inode.ExtDirType:
		offset = i.Info.(inode.ExtDir).DirectoryIndex
		metaOffset = i.Info.(inode.ExtDir).DirectoryOffset
		size = i.Info.(inode.ExtDir).DirectorySize
	default:
		return nil, errors.New("Not a directory inode")
	}
	br, err := r.newMetadataReader(r.super.DirTableStart + uint64(offset))
	if err != nil {
		return nil, err
	}
	_, err = br.Seek(int64(metaOffset), io.SeekStart)
	if err != nil {
		return nil, err
	}
	ents, err := directory.NewDirectory(br, size)
	if err != nil {
		return nil, err
	}
	return ents, nil
}
