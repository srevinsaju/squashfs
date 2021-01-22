package squashfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/CalebQ42/squashfs/internal/directory"
	"github.com/CalebQ42/squashfs/internal/inode"
)

var (
	//ErrNotDirectory is returned when you're trying to do symlink things with a non-symlink
	errNotSymlink = errors.New("File is not a symlink")
	//ErrNotDirectory is returned when you're trying to do directory things with a non-directory
	errNotDirectory = errors.New("File is not a directory")
	//ErrNotFile is returned when you're trying to do file things with a directory
	errNotFile = errors.New("File is not a file")
	//ErrNotReading is returned when running functions that are only meant to be used when reading a squashfs
	errNotReading = errors.New("Function only supported when reading a squashfs")
	//ErrBrokenSymlink is returned when using ExtractWithOptions with the unbreakSymlink set to true, but the symlink's file cannot be extracted.
	ErrBrokenSymlink = errors.New("Extracted symlink is probably broken")
)

//FileCore contains the core functionality of files within a squashfs archive. This will mainly be used via FileFS or File.
type FileCore struct {
	Parent *FileFS
	r      *Reader //Underlying reader. When writing, will probably be an os.File. When reading this is kept nil UNTIL reading to save memory.
	in     *inode.Inode
	name   string
	dir    string
}

//get a File from a directory.entry
func (r *Reader) newFileFromDirEntry(entry *directory.Entry) (fil *FileCore, err error) {
	fil = new(FileCore)
	fil.in, err = r.getInodeFromEntry(entry)
	if err != nil {
		return nil, err
	}
	fil.name = entry.Name
	fil.r = r
	return
}

//AsFS returns the File as a *FileFS is IsDir
func (f *FileCore) AsFS() (*FileFS, error) {
	if !f.IsDir() {
		return nil, errNotDirectory
	}
	return &FileFS{
		FileCore: f,
	}, nil
}

//ModTime is the time of last modification.
func (f *FileCore) ModTime() time.Time {
	return time.Unix(int64(f.in.Header.ModifiedTime), 0)
}

//Path returns the path of the file within the archive.
func (f *FileCore) Path() string {
	if f.name == "" {
		return f.dir
	}
	return f.dir + "/" + f.name
}

//IsDir returns if the file is a directory.
func (f *FileCore) IsDir() bool {
	return f.in.Type == inode.DirType || f.in.Type == inode.ExtDirType
}

//IsSymlink returns if the file is a symlink.
func (f *FileCore) IsSymlink() bool {
	return f.in.Type == inode.SymType || f.in.Type == inode.ExtSymType
}

//IsFile returns if the file is a file.
func (f *FileCore) IsFile() bool {
	return f.in.Type == inode.FileType || f.in.Type == inode.ExtFileType
}

//SymlinkPath returns the path the symlink is pointing to. If the file ISN'T a symlink, will return an empty string.
//If a path begins with "/" then the symlink is pointing to an absolute path (starting from root, and not a file inside the archive)
func (f *FileCore) SymlinkPath() string {
	switch f.in.Type {
	case inode.SymType:
		return f.in.Info.(inode.Sym).Path
	case inode.ExtSymType:
		return f.in.Info.(inode.ExtSym).Path
	default:
		return ""
	}
}

//GetSymlinkFile returns the file the FileCore is symlinked to.
func (f *FileCore) GetSymlinkFile() (*FileCore, error) {
	if !f.IsSymlink() {
		return nil, errNotSymlink
	}
	target := f.SymlinkPath()
	if target == "" {
		return nil, errors.New("Empty symlink path")
	}
	symFil, err := f.Parent.Open(target)
	if err != nil {
		return nil, err
	}
	return symFil.(*File).FileCore, nil
}

//ExtractTo extracts the file to the given path. This is the same as ExtractWithOptions(path, false, false, os.ModePerm, false).
//Will NOT try to keep symlinks valid, folders extracted will have the permissions set by the squashfs, but the folder to make path will have full permissions (777).
//
//Will try it's best to extract all files, and if any errors come up, they will be appended to the error slice that's returned.
func (f *FileCore) ExtractTo(path string) error {
	return f.ExtractWithOptions(path, false, false, os.ModePerm, false)
}

//ExtractSymlink is similar to ExtractTo, but when it extracts a symlink, it instead extracts the file associated with the symlink in it's place.
//This is the same as ExtractWithOptions(path, true, false, os.ModePerm, false)
func (f *FileCore) ExtractSymlink(path string) error {
	return f.ExtractWithOptions(path, true, false, os.ModePerm, false)
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
func (f *FileCore) ExtractWithOptions(path string, dereferenceSymlink, unbreakSymlink bool, folderPerm os.FileMode, verbose bool) error {
	path = filepath.Clean(path)
	err := os.MkdirAll(path, folderPerm)
	if err != nil {
		return err
	}
	path += "/" + f.name
	file := f.AsFile()
	if f.IsFile() {
		var realFile *os.File
		realFile, err = os.Create(path)
		if os.IsExist(err) {
			err = os.Remove(path)
			if err != nil {
				if verbose {
					log.Println("File exists at", path, "and cannot remove")
					log.Println(err)
				}
				return err
			}
			realFile, err = os.Create(path)
			if err != nil {
				if verbose {
					log.Println("Cannot create file at", path)
					log.Println(err)
				}
				return err
			}
		} else if err != nil {
			if verbose {
				log.Println("Cannot create file at", path)
				log.Println(err)
			}
			return err
		}
		realFile.Chmod(file.mode)
		realFile.Chown(int(f.r.idTable[f.in.UID]), int(f.r.idTable[f.in.GID]))
		_, err = io.Copy(realFile, file)
		if err != nil {
			if verbose {
				log.Println("Error while copying data to", path)
				log.Println(err)
			}
			return err
		}
		return nil
	} else if f.IsDir() {
		err = os.Mkdir(path, file.mode)
		if !os.IsExist(err) && err != nil {
			if verbose {
				log.Println("Error while creating folder", path)
				log.Println(err)
			}
			return err
		}
		var realDir *os.File
		realDir, err = os.Open(path)
		if err != nil {
			if verbose {
				log.Println("Error while opening folder", path)
				log.Println(err)
			}
			return err
		}
		realDir.Chmod(file.mode)
		realDir.Chown(int(f.r.idTable[f.in.UID]), int(f.r.idTable[f.in.GID]))
		var children []fs.DirEntry
		children, err = file.ReadDir(0)
		if err != nil {
			if verbose {
				log.Println("Error while getting children", path)
				log.Println(err)
			}
			return err
		}
		errChan := make(chan error)
		for _, child := range children {
			go func(fil *File) {
				errChan <- fil.ExtractWithOptions(path, dereferenceSymlink, unbreakSymlink, folderPerm, verbose)
			}(child.(*File))
		}
		for range children {
			err = <-errChan
			if err != nil {
				if verbose {
					log.Println("Error while walking", path)
					log.Println(err)
				}
				return err
			}
		}
		return nil
	} else if f.IsSymlink() {
		if dereferenceSymlink {
			var symFil *FileCore
			symFil, err = f.GetSymlinkFile()
			if err != nil {
				symFil.name = f.name
				err = symFil.ExtractWithOptions(filepath.Dir(path), dereferenceSymlink, unbreakSymlink, folderPerm, verbose)
				if err != nil {
					goto symlinkCreate
				}
			}
		}
	symlinkCreate:
		os.Remove(path)
		err = os.Symlink(f.SymlinkPath(), path)
		if err != nil {
			fmt.Println("YO")
			return err
		}
		//TODO: unbreakSymlink
		return nil
	}
	return errors.New("Invalid file type")
}

//ReadDirFromInode returns a fully populated Directory from a given Inode.
//If the given inode is not a directory it returns an error.
func (r *Reader) readDirFromInode(i *inode.Inode) (*directory.Directory, error) {
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
	br, err := r.newMetadataReader(int64(r.super.DirTableStart + uint64(offset)))
	if err != nil {
		return nil, err
	}
	_, err = br.Seek(int64(metaOffset), io.SeekStart)
	if err != nil {
		return nil, err
	}
	dir, err := directory.NewDirectory(br, size)
	if err != nil {
		return dir, err
	}
	return dir, nil
}

//GetInodeFromEntry returns the inode associated with a given directory.Entry
func (r *Reader) getInodeFromEntry(en *directory.Entry) (*inode.Inode, error) {
	br, err := r.newMetadataReader(int64(r.super.InodeTableStart + uint64(en.Header.InodeOffset)))
	if err != nil {
		return nil, err
	}
	_, err = br.Seek(int64(en.Offset), io.SeekStart)
	if err != nil {
		return nil, err
	}
	i, err := inode.ProcessInode(br, r.super.BlockSize)
	if err != nil {
		return nil, err
	}
	return i, nil
}
