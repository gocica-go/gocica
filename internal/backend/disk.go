package backend

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	v1 "github.com/mazrean/gocica/internal/proto/gocica/v1"
	"google.golang.org/protobuf/proto"
)

var _ Backend = &Disk{}

type Disk struct {
	rootPath        string
	emptyFilePath   string
	actionMapLocker sync.RWMutex
	actionMap       map[string]*v1.IndexEntry
}

func NewDisk(dir string, isMemMode bool) (*Disk, error) {
	err := os.MkdirAll(dir, 0755)
	if err != nil {
		return nil, fmt.Errorf("failed to create root directory: %w", err)
	}

	emptyFilePath := filepath.Join(dir, "r-empty-file")
	f, err := os.Create(emptyFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create empty file: %w", err)
	}
	err = f.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to close empty file: %w", err)
	}

	disk := &Disk{
		rootPath:      dir,
		emptyFilePath: emptyFilePath,
	}
	if isMemMode {
		disk.actionMap = make(map[string]*v1.IndexEntry)
	}

	return disk, nil
}

func (d *Disk) Get(ctx context.Context, actionID string) (string, *MetaData, error) {
	var indexEntry *v1.IndexEntry
	if d.actionMap == nil {
		buf, err := os.ReadFile(d.actionFilePath(actionID))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return "", nil, nil
			}
			return "", nil, fmt.Errorf("open action file: %w", err)
		}

		indexEntry = &v1.IndexEntry{}
		if err := proto.Unmarshal(buf, indexEntry); err != nil {
			return "", nil, fmt.Errorf("unmarshal index entry: %w", err)
		}
	} else {
		var ok bool
		func() {
			d.actionMapLocker.RLock()
			defer d.actionMapLocker.RUnlock()
			indexEntry, ok = d.actionMap[actionID]
		}()
		if !ok {
			return "", nil, nil
		}
	}

	return indexEntry.DiskPath, &MetaData{
		OutputID: indexEntry.OutputId,
		Size:     indexEntry.Size,
		Timenano: indexEntry.Timenano,
	}, nil
}

func (d *Disk) Put(ctx context.Context, actionID, outputID string, size int64, body io.Reader) (string, error) {
	var outputFilePath string
	if size == 0 {
		outputFilePath = d.emptyFilePath
	} else {
		outputFilePath = d.objectFilePath(outputID)
		_, err := d.writeAtomic(outputFilePath, body)
		if err != nil {
			return "", fmt.Errorf("write output file: %w", err)
		}
	}

	indexEntry := &v1.IndexEntry{
		DiskPath: outputFilePath,
		OutputId: outputID,
		Size:     size,
		Timenano: time.Now().UnixNano(),
	}
	if d.actionMap == nil {
		buf, err := proto.Marshal(indexEntry)
		if err != nil {
			return "", fmt.Errorf("marshal index entry: %w", err)
		}

		actionFilePath := d.actionFilePath(actionID)
		_, err = d.writeAtomic(actionFilePath, bytes.NewReader(buf))
		if err != nil {
			return "", fmt.Errorf("write action file: %w", err)
		}
	} else {
		func() {
			d.actionMapLocker.Lock()
			defer d.actionMapLocker.Unlock()
			d.actionMap[actionID] = indexEntry
		}()
	}

	return outputFilePath, nil
}

func (d *Disk) objectFilePath(id string) string {
	return filepath.Join(d.rootPath, fmt.Sprintf("o-%s", d.encodeID(id)))
}

func (d *Disk) actionFilePath(id string) string {
	return filepath.Join(d.rootPath, fmt.Sprintf("a-%s", d.encodeID(id)))
}

func (*Disk) encodeID(id string) string {
	return strings.ReplaceAll(id, "/", "-")
}

func (d *Disk) writeTemp(dest string, r io.Reader) (path string, size int64, err error) {
	f, err := os.CreateTemp(filepath.Dir(dest), filepath.Base(dest)+".*")
	if err != nil {
		err = fmt.Errorf("create temp file: %w", err)
		return
	}
	defer func() {
		deferErr := f.Close()
		if deferErr != nil {
			deferErr = fmt.Errorf("close temp file: %w", deferErr)
			if err == nil {
				err = deferErr
			} else {
				err = errors.Join(err, deferErr)
			}
		}

		if err != nil {
			deferErr = os.Remove(f.Name())
			if deferErr != nil {
				deferErr = fmt.Errorf("remove temp file: %w", deferErr)
				err = errors.Join(err, deferErr)
			}
		}
	}()
	path = f.Name()

	size, err = io.Copy(f, r)
	if err != nil {
		err = fmt.Errorf("write temp file: %w", err)
		return
	}

	return
}

func (d *Disk) writeAtomic(dest string, r io.Reader) (size int64, err error) {
	var tmpPath string
	tmpPath, size, err = d.writeTemp(dest, r)
	if err != nil {
		err = fmt.Errorf("write temp file: %w", err)
		return
	}
	defer func() {
		deferErr := os.Remove(tmpPath)
		if deferErr != nil && !os.IsNotExist(deferErr) {
			deferErr = fmt.Errorf("remove temp file: %w", deferErr)
			if err == nil {
				err = deferErr
			} else {
				err = errors.Join(err, deferErr)
			}
		}
	}()

	if err = os.Rename(tmpPath, dest); err != nil {
		err = fmt.Errorf("rename temp file: %w", err)
		return
	}

	return
}

func (d *Disk) Close() error {
	if d.actionMap != nil {
		d.actionMap = nil
	}

	return nil
}
