package drive

import (
	"context"
	"go-drive/common/drive_util"
	"go-drive/common/errors"
	"go-drive/common/i18n"
	"go-drive/common/task"
	"go-drive/common/types"
	"go-drive/common/utils"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
)

func init() {
	drive_util.RegisterDrive(drive_util.DriveFactoryConfig{
		Type:        "fs",
		DisplayName: i18n.T("drive.fs.name"),
		README:      i18n.T("drive.fs.readme"),
		ConfigForm: []types.FormItem{
			{Field: "path", Label: i18n.T("drive.fs.form.path.label"), Type: "text", Required: true, Description: i18n.T("drive.fs.form.path.description")},
		},
		Factory: drive_util.DriveFactory{Create: NewFsDrive},
	})
}

type FsDrive struct {
	path string
}

type fsFile struct {
	drive *FsDrive
	path  string

	size  int64
	isDir bool

	modTime int64
}

// NewFsDrive creates a file system drive
func NewFsDrive(_ context.Context, config drive_util.DriveConfig,
	driveUtils drive_util.DriveUtils) (types.IDrive, error) {
	path := config["path"]
	if utils.CleanPath(path) == "" {
		return nil, err.NewNotAllowedMessageError(i18n.T("drive.fs.invalid_root_path"))
	}

	localRoot, e := driveUtils.Config.GetLocalFsDir()
	if e != nil {
		return nil, e
	}

	path, e = filepath.Abs(filepath.Join(localRoot, path))
	if e != nil {
		return nil, e
	}
	if exists, _ := utils.FileExists(path); !exists {
		return nil, err.NewNotFoundMessageError(i18n.T("drive.fs.root_path_not_exists"))
	}
	return &FsDrive{path}, nil
}

func (f *FsDrive) newFsFile(path string, file os.FileInfo) (types.IEntry, error) {
	path, e := filepath.Abs(path)
	if e != nil {
		return nil, err.NewNotFoundMessageError(i18n.T("drive.invalid_path"))
	}
	if !strings.HasPrefix(path, f.path) {
		panic("invalid file key")
	}
	path = strings.ReplaceAll(path, "\\", "/")
	path = path[len(f.path):]
	for strings.HasPrefix(path, "/") {
		path = path[1:]
	}
	return &fsFile{
		drive:   f,
		path:    path,
		size:    file.Size(),
		isDir:   file.IsDir(),
		modTime: utils.Millisecond(file.ModTime()),
	}, nil
}

func (f *FsDrive) getPath(path string) string {
	path = filepath.Clean(path)
	return filepath.Join(f.path, path)
}

func (f *FsDrive) isRootPath(path string) bool {
	return filepath.Clean(path) == f.path
}

func (f *FsDrive) Get(_ context.Context, path string) (types.IEntry, error) {
	path = f.getPath(path)
	stat, e := os.Stat(path)
	if os.IsNotExist(e) {
		return nil, err.NewNotFoundError()
	}
	if e != nil {
		return nil, e
	}
	return f.newFsFile(path, stat)
}

func (f *FsDrive) Save(ctx types.TaskCtx, path string, _ int64, override bool, reader io.Reader) (types.IEntry, error) {
	path = f.getPath(path)
	if !override {
		if e := requireFile(path, false); e != nil {
			return nil, e
		}
	}
	file, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0644)
	if e != nil {
		return nil, e
	}
	defer func() { _ = file.Close() }()
	_, e = drive_util.Copy(task.NewProgressCtxWrapper(ctx), file, reader)
	if e != nil {
		return nil, e
	}
	stat, e := file.Stat()
	if e != nil {
		return nil, e
	}
	return f.newFsFile(path, stat)
}

func (f *FsDrive) MakeDir(ctx context.Context, path string) (types.IEntry, error) {
	path = f.getPath(path)
	if exists, _ := utils.FileExists(path); exists {
		return f.Get(ctx, path)
	}
	if e := os.Mkdir(path, 0755); e != nil {
		return nil, e
	}
	stat, e := os.Stat(path)
	if e != nil {
		return nil, e
	}
	return f.newFsFile(path, stat)
}

func (f *FsDrive) Copy(types.TaskCtx, types.IEntry, string, bool) (types.IEntry, error) {
	return nil, err.NewUnsupportedError()
}

func (f *FsDrive) isSelf(entry types.IEntry) bool {
	if fe, ok := entry.(*fsFile); ok {
		return fe.drive == f
	}
	return false
}

func (f *FsDrive) Move(_ types.TaskCtx, from types.IEntry, to string, override bool) (types.IEntry, error) {
	from = drive_util.GetIEntry(from, f.isSelf)
	if from == nil {
		return nil, err.NewUnsupportedError()
	}
	fromPath := f.getPath(from.(*fsFile).path)
	toPath := f.getPath(to)
	if f.isRootPath(fromPath) || f.isRootPath(toPath) {
		return nil, err.NewNotAllowedError()
	}
	if e := requireFile(fromPath, true); e != nil {
		return nil, e
	}
	exists, e := utils.FileExists(toPath)
	if e != nil {
		return nil, e
	}
	if exists {
		if !override {
			return nil, err.NewNotAllowedMessageError(i18n.T("drive.file_exists"))
		}
		if e := f.Delete(task.DummyContext(), to); e != nil {
			return nil, e
		}
	}
	if e := os.Rename(fromPath, toPath); e != nil {
		return nil, e
	}
	stat, e := os.Stat(toPath)
	if e != nil {
		return nil, e
	}
	return f.newFsFile(toPath, stat)
}

func (f *FsDrive) List(_ context.Context, path string) ([]types.IEntry, error) {
	path = f.getPath(path)
	isDir, e := utils.IsDir(path)
	if os.IsNotExist(e) {
		return nil, err.NewNotFoundError()
	}
	if !isDir {
		return nil, err.NewNotAllowedMessageError(i18n.T("drive.fs.cannot_list_file"))
	}
	files, ee := ioutil.ReadDir(path)
	if ee != nil {
		return nil, ee
	}
	entries := make([]types.IEntry, len(files))
	for i, file := range files {
		entry, e := f.newFsFile(filepath.Join(path, file.Name()), file)
		if e != nil {
			return nil, e
		}
		entries[i] = entry
	}
	return entries, nil
}

func (f *FsDrive) Delete(_ types.TaskCtx, path string) error {
	path = f.getPath(path)
	if f.isRootPath(path) {
		return err.NewNotAllowedMessageError(i18n.T("drive.fs.cannot_delete_root"))
	}
	if e := requireFile(path, true); e != nil {
		return e
	}
	return os.RemoveAll(path)
}

func (f *FsDrive) Upload(_ context.Context, path string, size int64,
	override bool, _ types.SM) (*types.DriveUploadConfig, error) {
	path = f.getPath(path)
	if !override {
		if e := requireFile(path, false); e != nil {
			return nil, e
		}
	}
	return types.UseLocalProvider(size), nil
}

func requireFile(path string, requireExists bool) error {
	exists, e := utils.FileExists(path)
	if e != nil {
		return e
	}
	if requireExists && !exists {
		return err.NewNotFoundMessageError(i18n.T("drive.file_not_exists"))
	}
	if !requireExists && exists {
		return err.NewNotAllowedMessageError(i18n.T("drive.file_exists"))
	}
	return nil
}

func (f *FsDrive) Meta(context.Context) types.DriveMeta {
	return types.DriveMeta{CanWrite: true}
}

func (f *fsFile) Path() string {
	return f.path
}

func (f *fsFile) Type() types.EntryType {
	if f.isDir {
		return types.TypeDir
	}
	return types.TypeFile
}

func (f *fsFile) Size() int64 {
	if f.Type().IsDir() {
		return -1
	}
	return f.size
}

func (f *fsFile) Meta() types.EntryMeta {
	return types.EntryMeta{CanRead: true, CanWrite: true}
}

func (f *fsFile) ModTime() int64 {
	return f.modTime
}

func (f *fsFile) Drive() types.IDrive {
	return f.drive
}

func (f *fsFile) Name() string {
	return utils.PathBase(f.path)
}

func (f *fsFile) GetReader(context.Context) (io.ReadCloser, error) {
	if !f.Type().IsFile() {
		return nil, err.NewNotAllowedError()
	}
	path := f.drive.getPath(f.path)
	exists, e := utils.FileExists(path)
	if e != nil {
		return nil, e
	}
	if !exists {
		return nil, err.NewNotFoundMessageError(i18n.T("drive.file_not_exists"))
	}
	return os.Open(path)
}

func (f *fsFile) GetURL(context.Context) (*types.ContentURL, error) {
	return nil, err.NewUnsupportedError()
}
