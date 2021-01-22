package squashfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
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
func (f *FileCore) ExtractTo(path string) []error {
	return f.ExtractWithOptions(path, false, false, os.ModePerm, false)
}

//ExtractSymlink is similar to ExtractTo, but when it extracts a symlink, it instead extracts the file associated with the symlink in it's place.
//This is the same as ExtractWithOptions(path, true, false, os.ModePerm, false)
func (f *FileCore) ExtractSymlink(path string) []error {
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
func (f *FileCore) ExtractWithOptions(path string, dereferenceSymlink, unbreakSymlink bool, folderPerm os.FileMode, verbose bool) (errs []error) {
	errs = make([]error, 0)
	err := os.MkdirAll(path, folderPerm)
	if err != nil {
		return []error{err}
	}
	fFile := f.AsFile()
	switch {
	case f.IsDir():
		if f.name != "" {
			//TODO: check if folder is present, and if so, try to set it's permission
			err = os.Mkdir(path+"/"+f.name, os.ModePerm)
			if err != nil {
				if verbose {
					fmt.Println("Error while making: ", path+"/"+f.name)
					fmt.Println(err)
				}
				errs = append(errs, err)
				return
			}
			var fil *os.File
			fil, err = os.Open(path + "/" + f.name)
			if err != nil {
				if verbose {
					fmt.Println("Error while opening:", path+"/"+f.name)
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
			// }
			err = fil.Chmod(fFile.Mode())
			if err != nil {
				if verbose {
					fmt.Println("Error while changing owner:", path+"/"+f.name)
					fmt.Println(err)
				}
				errs = append(errs, err)
			}
		}
		var children []fs.DirEntry
		children, err = fFile.ReadDir(-1)
		if err != nil {
			if verbose {
				fmt.Println("Error getting children for:", f.Path())
				fmt.Println(err)
			}
			errs = append(errs, err)
			return
		}
		finishChan := make(chan []error)
		for _, child := range children {
			go func(child *File) {
				if f.name == "" {
					finishChan <- child.ExtractWithOptions(path, dereferenceSymlink, unbreakSymlink, folderPerm, verbose)
				} else {
					finishChan <- child.ExtractWithOptions(path+"/"+f.name, dereferenceSymlink, unbreakSymlink, folderPerm, verbose)
				}
			}(child.(*File))
		}
		for range children {
			errs = append(errs, (<-finishChan)...)
		}
		return
	case f.IsFile():
		var fil *os.File
		fil, err = os.Create(path + "/" + f.name)
		if os.IsExist(err) {
			err = os.Remove(path + "/" + f.name)
			if err != nil {
				if verbose {
					fmt.Println("Error while making:", path+"/"+f.name)
					fmt.Println(err)
				}
				errs = append(errs, err)
				return
			}
			fil, err = os.Create(path + "/" + f.name)
			if err != nil {
				if verbose {
					fmt.Println("Error while making:", path+"/"+f.name)
					fmt.Println(err)
				}
				errs = append(errs, err)
				return
			}
		} else if err != nil {
			if verbose {
				fmt.Println("Error while making:", path+"/"+f.name)
				fmt.Println(err)
			}
			errs = append(errs, err)
			return
		} //Since we will be reading from the file
		_, err = io.Copy(fil, fFile)
		if err != nil {
			if verbose {
				fmt.Println("Error while Copying data to:", path+"/"+f.name)
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
		err = fil.Chmod(fFile.Mode())
		if err != nil {
			if verbose {
				fmt.Println("Error while setting permissions for:", path+"/"+f.name)
				fmt.Println(err)
			}
			errs = append(errs, err)
		}
		return
	case f.IsSymlink():
		var fil *FileCore
		symPath := f.SymlinkPath()
		if dereferenceSymlink {
			fil, err = f.GetSymlinkFile()
			if err != nil {
				if verbose {
					fmt.Println("Symlink path(", symPath, ") is invalid:"+path+"/"+f.name)
					fmt.Println(err)
				}
				errs = append(errs, err)
			}
			fil.name = f.name
			extracSymErrs := fil.ExtractWithOptions(path, dereferenceSymlink, unbreakSymlink, folderPerm, verbose)
			if len(extracSymErrs) > 0 {
				if verbose {
					fmt.Println("Error(s) while extracting the symlink's file:", path+"/"+f.name)
					fmt.Println(extracSymErrs)
				}
				errs = append(errs, extracSymErrs...)
			}
			return
		} else if unbreakSymlink {
			fil, err = f.GetSymlinkFile()
			if fil != nil {
				symPath = path + "/" + symPath
				paths := strings.Split(symPath, "/")
				extracSymErrs := fil.ExtractWithOptions(strings.Join(paths[:len(paths)-1], "/"), dereferenceSymlink, unbreakSymlink, folderPerm, verbose)
				if len(extracSymErrs) > 0 {
					if verbose {
						fmt.Println("Error(s) while extracting the symlink's file:", path+"/"+f.name)
						fmt.Println(extracSymErrs)
					}
					errs = append(errs, extracSymErrs...)
				}
			} else {
				if verbose {
					fmt.Println("Symlink path(", symPath, ") is outside the archive:"+path+"/"+f.name)
					fmt.Println(err)
				}
				errs = append(errs, err)
			}
		}
		err = os.Symlink(f.SymlinkPath(), path+"/"+f.name)
		if err != nil {
			if verbose {
				fmt.Println("Error while making symlink:", path+"/"+f.name)
				fmt.Println(err)
			}
			errs = append(errs, err)
		}
	}
	return
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
