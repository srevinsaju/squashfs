package squashfs

import (
	"bytes"
	"io"
	"io/fs"
	"path"
	"strings"
)

//FileFS is a wrapper around squashfs.FileCore that satisfies fs.FS.
//Don't initialize on it's own, use File.AsFS().
type FileFS struct {
	*FileCore
}

//Open returns the fs.File at the given name.
func (f *FileFS) Open(name string) (fs.File, error) {
	name = path.Clean(name)
	name = strings.TrimPrefix(name, "/")
	if name == "" || name == "." {
		return f.AsFile(), nil
	}
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{
			Op:   "open",
			Path: name,
			Err:  fs.ErrInvalid,
		}
	}
	dir := path.Dir(name)
	dirs, err := f.ReadDir(dir)
	if err != nil {
		if pathErr, ok := err.(*fs.PathError); ok {
			pathErr.Op = "open"
			return nil, err
		}
		return nil, err
	}
	for i, dir := range dirs {
		if match, _ := path.Match(path.Base(name), dir.Name()); match {
			return dirs[i].(*File), nil
		}
	}
	return nil, &fs.PathError{
		Op:   "open",
		Path: name,
		Err:  fs.ErrNotExist,
	}
}

//ReadDir returns the children of the given directory as a fs.DirEntry slice.
func (f *FileFS) ReadDir(name string) (children []fs.DirEntry, err error) {
	name = path.Clean(name)
	name = strings.TrimPrefix(name, "/")
	if name == "" || name == "." {
		return f.AsFile().ReadDir(-1)
	}
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{
			Op:   "readdir",
			Path: name,
			Err:  fs.ErrInvalid,
		}
	}
	dir, err := f.Open(name)
	if err != nil {
		if pathErr, ok := err.(*fs.PathError); ok {
			pathErr.Op = "open"
			return nil, err
		}
		return nil, &fs.PathError{
			Op:   "readdir",
			Path: name,
			Err:  fs.ErrInvalid,
		}
	}
	return dir.(*File).ReadDir(-1)
}

//ReadFile returns the contents of the file at the given path.
func (f *FileFS) ReadFile(name string) ([]byte, error) {
	tmp, err := f.Open(name)
	if err != nil {
		return nil, &fs.PathError{
			Path: name,
			Err:  err,
		}
	}
	stat, err := tmp.Stat()
	if err != nil {
		if err != nil {
			return nil, &fs.PathError{
				Path: name,
				Err:  err,
			}
		}
	}
	stat.Mode().IsRegular()
	if !stat.Mode().IsRegular() {
		return nil, &fs.PathError{
			Path: name,
			Err:  errNotFile,
		}
	}
	var buf bytes.Buffer
	_, err = io.Copy(&buf, tmp)
	return buf.Bytes(), err
}

//Stat is the same as Open, but returns the file as a fs.FileInfo.
func (f *FileFS) Stat(name string) (fs.FileInfo, error) {
	fil, err := f.Open(name)
	if err != nil {
		if pathErr, ok := err.(*fs.PathError); ok {
			pathErr.Op = "stat"
			pathErr.Path = name
			return nil, pathErr
		}
		return nil, err
	}
	return fil.(*File), nil
}

//Sub returns a new fs.FS at the given directory
func (f *FileFS) Sub(dir string) (fs.FS, error) {
	fil, err := f.Open(dir)
	if err != nil {
		if pathErr, ok := err.(*fs.PathError); ok {
			pathErr.Op = "sub"
			pathErr.Path = dir
			return nil, pathErr
		}
		return nil, err
	}
	return fil.(*File).FileCore.AsFS()
}
