package binlog

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Frame format: [4-byte magic "PLOG"][4-byte uint32 payloadLen][4-byte uint32 crc32][payload bytes (JSON)]
var magic = [4]byte{'P', 'L', 'O', 'G'}

type Logger struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	maxFiles int
	curSize  int64
}

func NewLogger(path string, maxBytes int64, maxFiles int) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	var size int64
	if st, err := os.Stat(path); err == nil {
		size = st.Size()
	}
	return &Logger{
		path:     path,
		maxBytes: maxBytes,
		maxFiles: maxFiles,
		curSize:  size,
	}, nil
}

func (l *Logger) Write(record map[string]any) {
	if _, ok := record["ts"]; !ok {
		record["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return
	}
	frameLen := int64(12 + len(payload))

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.maxBytes > 0 && l.curSize+frameLen >= l.maxBytes {
		l.rotateLocked()
	}

	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	var hdr [12]byte
	copy(hdr[0:4], magic[:])
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(len(payload)))
	binary.LittleEndian.PutUint32(hdr[8:12], crc32.ChecksumIEEE(payload))

	if _, err := f.Write(hdr[:]); err != nil {
		return
	}
	if _, err := f.Write(payload); err != nil {
		return
	}
	l.curSize += frameLen
}

func (l *Logger) rotateLocked() {
	for i := l.maxFiles - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", l.path, i)
		dst := fmt.Sprintf("%s.%d", l.path, i+1)
		if _, err := os.Stat(src); err == nil {
			if i+1 > l.maxFiles {
				_ = os.Remove(src)
			} else {
				_ = os.Rename(src, dst)
			}
		}
	}
	if _, err := os.Stat(l.path); err == nil {
		_ = os.Rename(l.path, fmt.Sprintf("%s.1", l.path))
	}
	l.curSize = 0
}

// ExportJSONL reads a binary log file and writes each valid frame as a JSON line to w.
func ExportJSONL(binPath string, w io.Writer, tail int) (int, error) {
	f, err := os.Open(binPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	defer f.Close()

	var lines [][]byte
	var hdr [12]byte
	for {
		_, err := io.ReadFull(f, hdr[:])
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return 0, err
		}
		if [4]byte(hdr[0:4]) != magic {
			return 0, errors.New("corrupt binary log magic")
		}
		n := binary.LittleEndian.Uint32(hdr[4:8])
		sum := binary.LittleEndian.Uint32(hdr[8:12])
		if n > 16*1024*1024 {
			return 0, errors.New("frame too large")
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(f, buf); err != nil {
			break
		}
		if crc32.ChecksumIEEE(buf) != sum {
			continue
		}
		lines = append(lines, buf)
	}

	start := 0
	if tail > 0 && len(lines) > tail {
		start = len(lines) - tail
	}
	count := 0
	for _, line := range lines[start:] {
		if _, err := w.Write(line); err != nil {
			return count, err
		}
		if _, err := w.Write([]byte("\n")); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}
