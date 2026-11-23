// Package pvf reads the locally supplied S9A1 resource archive.
package pvf

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
)

var le = binary.LittleEndian

const (
	headerSize     = 48
	signature      = 0x69706B6E
	fileItemSize   = 24
	groupItemSize  = 8
	regularBudget  = 1 << 20
	metadataBudget = 256 << 20
	chunkLimit     = 256 << 20
)

type entry struct {
	name, directory, chunk, offset, size, kind int32
}

type Archive struct {
	lifetime   sync.RWMutex
	file       *os.File
	filename   string
	info       os.FileInfo
	prefixes   [][]byte
	client100  bool
	format     string
	strA, strW []byte
	items      []entry
	groups     []int32
	bodyOffset int64
	index      map[uint64]int32
	collisions map[uint64][]int32
	chunks     chunkCache
	localeMu   sync.Mutex
	locales    map[int]map[string]string
}

// Open accepts the protected NKPI and Client100 NKPI layouts supported by the
// original reader. The profile is required for the wrapped variant.
func Open(path, profile string) (*Archive, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	f, err := openSource(path)
	if err != nil {
		return nil, err
	}
	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	a := &Archive{file: f, filename: path, info: stat, locales: make(map[int]map[string]string)}
	a.chunks.init()
	if err = a.load(profile); err != nil {
		f.Close()
		return nil, err
	}
	return a, nil
}

func (a *Archive) Close() error {
	a.lifetime.Lock()
	defer a.lifetime.Unlock()
	if a.file == nil {
		return nil
	}
	err := a.file.Close()
	a.file = nil
	a.prefixes, a.items, a.groups, a.strA, a.strW = nil, nil, nil, nil, nil
	a.index, a.collisions, a.locales = nil, nil, nil
	a.chunks.clear()
	return err
}

// Format identifies the on-disk archive branch.
func (a *Archive) Format() string { return a.format }

// PhysicalIndex exposes the read order of an entry for bulk callers. The chunk
// and offset are archive-relative and are stable for the lifetime of a handle.
func (a *Archive) PhysicalIndex(path string) (chunk, offset int, ok bool) {
	a.lifetime.RLock()
	defer a.lifetime.RUnlock()
	i := a.find(path)
	if i < 0 {
		return 0, 0, false
	}
	item := a.items[i]
	return int(item.chunk), int(item.offset), true
}

func (a *Archive) checkSource() error {
	if a.file == nil {
		return os.ErrClosed
	}
	if stableSource {
		return nil
	}
	current, err := os.Stat(a.filename)
	if err != nil || !os.SameFile(a.info, current) || current.Size() != a.info.Size() || !current.ModTime().Equal(a.info.ModTime()) {
		return errors.New("PVF changed while open; reopen the resource archive")
	}
	return nil
}

func (a *Archive) load(profile string) error {
	buf, err := a.readRange(0, headerSize)
	if err != nil {
		return fmt.Errorf("PVF header: %w", err)
	}
	header := bytes.Clone(buf)
	protected("hEAd", header, 0x269EC3)
	a.format = "protected_nkpi"
	if le.Uint32(header) != signature {
		a.client100, a.format = true, "client100_nkpi"
		copy(header, buf)
		protected("iNfO", header, 0x269EC3)
		if le.Uint32(header) != signature {
			a.prefixes, err = envelopePrefixes(a.file, a.info.Size(), a.filename, profile)
			if err != nil {
				return err
			}
			copy(header, a.prefixes[0])
			protected("iNfO", header, 0x269EC3)
			if le.Uint32(header) != signature {
				return errors.New("unsupported Client100 NKPI signature")
			}
			a.format = "client100_sk_nkpi"
		}
	}
	count, bodySize, groupCount := i32(header[24:]), i32(header[32:]), i32(header[36:])
	hashSize, nameSize := i32(header[40:]), i32(header[44:])
	if count < 0 || bodySize < 0 || groupCount < 0 || hashSize < 0 || nameSize < 0 ||
		int64(count)*fileItemSize > metadataBudget || int64(groupCount)*groupItemSize > metadataBudget || nameSize > metadataBudget {
		return errors.New("invalid PVF header fields")
	}
	nameOffset := int64(headerSize) + int64(count)*fileItemSize + int64(hashSize)
	groupOffset := nameOffset + int64(nameSize)
	a.bodyOffset = groupOffset + int64(groupCount)*groupItemSize
	if a.bodyOffset > a.info.Size() {
		return errors.New("PVF directory extends beyond the file")
	}
	if err = a.loadStringPools(nameOffset, int(nameSize)); err != nil {
		return err
	}
	groups, err := a.readRange(groupOffset, int(groupCount)*groupItemSize)
	if err != nil {
		return err
	}
	protected(a.key("grpi", "Gidx"), groups, 0x269EC3)
	a.groups = make([]int32, groupCount)
	for i := range a.groups {
		a.groups[i] = i32(groups[i*groupItemSize:])
	}
	table, err := a.readRange(headerSize, int(count)*fileItemSize)
	if err != nil {
		return err
	}
	a.items = make([]entry, count)
	a.index = make(map[uint64]int32, count)
	a.collisions = make(map[uint64][]int32)
	for i := range a.items {
		b := table[i*fileItemSize:]
		a.items[i] = entry{i32(b), i32(b[4:]), i32(b[8:]), i32(b[12:]), i32(b[16:]), i32(b[20:])}
		path := a.path(i)
		if path == "" {
			continue
		}
		hash := hashPath(path)
		first, exists := a.index[hash]
		if !exists {
			a.index[hash] = int32(i)
			continue
		}
		if !strings.EqualFold(a.path(int(first)), path) {
			a.collisions[hash] = append(a.collisions[hash], int32(i))
		}
	}
	return a.checkSource()
}

func (a *Archive) key(older, client100 string) string {
	if a.client100 {
		return client100
	}
	return older
}

func (a *Archive) loadStringPools(offset int64, size int) error {
	if size < 16 {
		return nil
	}
	b, err := a.readRange(offset, size)
	if err != nil {
		return err
	}
	pos := 8
	read := func(key string, mask uint32) []byte {
		if pos+8 > len(b) {
			return nil
		}
		n := int(int32(le.Uint32(b[pos:]) ^ mask))
		pos += 8
		if n <= 0 || n > len(b)-pos {
			return nil
		}
		enc := b[pos : pos+n]
		pos += n
		protected(key, enc, 0x269EC9)
		dec, _ := inflate(enc, metadataBudget)
		return dec
	}
	if a.client100 {
		a.strA, a.strW = read("stAs", 0xE7ADF7EA), read("stWs", 0xB8DEA7AC)
	} else {
		a.strA, a.strW = read("StRa", 0xAA74472E), read("StRw", 0x9A82F037)
	}
	return nil
}

func (a *Archive) readRange(offset int64, size int) ([]byte, error) {
	if offset < 0 || size < 0 || offset > a.info.Size()-int64(size) {
		return nil, errors.New("PVF read range extends beyond the file")
	}
	b := make([]byte, size)
	if _, err := a.file.ReadAt(b, offset); err != nil {
		return nil, err
	}
	if size != 0 && len(a.prefixes) != 0 {
		end := offset + int64(size)
		for i := offset / regionSize; i <= (end-1)/regionSize; i++ {
			start := i * regionSize
			left, right := max(offset, start), min(end, start+prefixSize)
			if left < right {
				copy(b[left-offset:right-offset], a.prefixes[i][left-start:right-start])
			}
		}
	}
	return b, nil
}

func i32(b []byte) int32 { return int32(le.Uint32(b)) }

func protected(key string, b []byte, magic uint32) {
	seed := 0x339E9711*uint32(key[0]) + 0x393*(uint32(key[3])+0x393*(uint32(key[2])+0x393*uint32(key[1])))
	for len(b) >= 4 {
		first := 0x343FD*seed + magic
		seed = 0x343FD*first + magic
		le.PutUint32(b, le.Uint32(b)^(first&0xFFFF0000|seed>>16))
		b = b[4:]
	}
	if len(b) != 0 {
		first := 0x343FD*seed + magic
		second := 0x343FD*first + magic
		k := first&0xFFFF0000 | second>>16
		for i := range b {
			b[i] ^= byte(k >> (8 * i))
		}
	}
}

func inflate(b []byte, maximum int64) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, maximum+1))
	if err == nil && int64(len(out)) > maximum {
		err = errors.New("inflated PVF block exceeds read budget")
	}
	return out, err
}

func (a *Archive) loadChunk(idx int32) ([]byte, error) {
	if idx < 0 || int(idx) >= len(a.groups) {
		return nil, errors.New("invalid PVF chunk index")
	}
	var prev int32
	if idx > 0 {
		prev = a.groups[idx-1]
	}
	curr := a.groups[idx]
	if prev < 0 || curr <= prev {
		return nil, errors.New("invalid PVF chunk interval")
	}
	b, err := a.readRange(a.bodyOffset+int64(prev), int(curr-prev))
	if err != nil {
		return nil, err
	}
	protected(a.key("bODy", "mAIn"), b, 0x269EC3)
	return inflate(b, chunkLimit)
}

func (a *Archive) raw(index int, maximum int) ([]byte, error) {
	e := a.items[index]
	if maximum < 0 || e.size < 0 || int(e.size) > maximum {
		return nil, errors.New("PVF file exceeds read budget")
	}
	chunk, err := a.chunks.get(e.chunk, a.loadChunk)
	if err != nil {
		return nil, err
	}
	end := int64(e.offset) + int64(e.size)
	if e.offset < 0 || end > int64(len(chunk)) {
		return nil, errors.New("invalid PVF entry boundaries")
	}
	return bytes.Clone(chunk[e.offset:end]), a.checkSource()
}

func (a *Archive) Read(path string, maximum int) ([]byte, error) {
	a.lifetime.RLock()
	defer a.lifetime.RUnlock()
	if maximum < 0 || maximum > regularBudget {
		return nil, errors.New("PVF read budget must be at most 1 MiB")
	}
	if err := a.checkSource(); err != nil {
		return nil, err
	}
	i, err := a.entry(path)
	if err != nil {
		return nil, err
	}
	return a.raw(i, maximum)
}

func (a *Archive) entry(path string) (int, error) {
	i := a.find(path)
	if i < 0 {
		return -1, &os.PathError{Op: "read PVF", Path: path, Err: os.ErrNotExist}
	}
	return i, nil
}

func (a *Archive) path(i int) string {
	row := a.items[i]
	name, dir := a.resolve(row.name), a.resolve(row.directory)
	if dir == "" {
		return NormalizePath(name)
	}
	return NormalizePath(dir + "/" + name)
}

// Paths returns a snapshot in original directory order, skipping empty names.
func (a *Archive) Paths() []string {
	a.lifetime.RLock()
	defer a.lifetime.RUnlock()
	out := make([]string, 0, len(a.items))
	for i := range a.items {
		if path := a.path(i); path != "" {
			out = append(out, path)
		}
	}
	return out
}

func hashPath(path string) uint64 {
	h := uint64(14695981039346656037)
	for _, r := range path {
		h = (h ^ uint64(unicode.ToUpper(r))) * 1099511628211
	}
	return h
}

func (a *Archive) find(path string) int {
	path = NormalizePath(path)
	hash := hashPath(path)
	first, found := a.index[hash]
	if !found || path == "" {
		return -1
	}
	if strings.EqualFold(a.path(int(first)), path) {
		return int(first)
	}
	for _, idx := range a.collisions[hash] {
		if strings.EqualFold(a.path(int(idx)), path) {
			return int(idx)
		}
	}
	return -1
}

func NormalizePath(path string) string {
	path = strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	for strings.HasPrefix(path, "./") || strings.HasPrefix(path, "/") {
		path = strings.TrimPrefix(strings.TrimPrefix(path, "./"), "/")
	}
	if path != "." && path != ".." && !strings.HasSuffix(path, "/") && !strings.HasSuffix(path, "/.") && !strings.HasSuffix(path, "/..") &&
		!strings.Contains(path, "//") && !strings.Contains(path, "/./") && !strings.Contains(path, "/../") {
		return path
	}
	parts := make([]string, 0, strings.Count(path, "/")+1)
	for part := range strings.SplitSeq(path, "/") {
		switch part {
		case "", ".":
		case "..":
			if len(parts) != 0 {
				parts = parts[:len(parts)-1]
			}
		default:
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "/")
}
