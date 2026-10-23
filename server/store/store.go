// Package store 保存与读取回放数据：压缩帧、索引、指标、事件、政权表、检查点。
package store

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/klauspost/compress/zstd"

	"mapsim/sim"
	"mapsim/world"
)

const KeyEvery = 20 // 每 20 帧(100 年)一个关键帧

type IndexEntry struct {
	Year   int32
	Offset int64
	Length int32
	Key    bool
}

type RunMeta struct {
	ID            string           `json:"id"`
	Params        sim.Params       `json:"params"`
	Constraints   *sim.Constraints `json:"constraints,omitempty"`
	World         string           `json:"world"`   // 预处理输出目录(缓存目录，决定格子数据)
	WorldID       string           `json:"worldId"` // 平台中的世界项目
	Tenant        string           `json:"tenant"`
	Key           string           `json:"key,omitempty"` // 复现键：世界缓存 + 引擎版本 + 参数 + 约束
	EngineVersion string           `json:"engineVersion"`
	PeoplePerUnit float32          `json:"peoplePerUnit"`
	ComputedUntil int              `json:"computedUntil"`
	FrameEvery    int              `json:"frameEvery"`
	CkptEvery     int              `json:"ckptEvery"`
	Done          bool             `json:"done"`
	Failed        string           `json:"failed,omitempty"`
	Created       string           `json:"created"`
}

// Writer 追加写帧。只在模拟 goroutine 中使用。
type Writer struct {
	dir     string
	fb      *os.File
	fi      *os.File
	fm      *os.File
	fe      *os.File
	enc     *zstd.Encoder
	prev    []byte
	n       int
	off     int64
	cells   int
	Meta    RunMeta
	mu      sync.Mutex
	index   []IndexEntry
	metrics []sim.Metrics
}

func Create(dir string, meta RunMeta, cells int) (*Writer, error) {
	if err := os.MkdirAll(filepath.Join(dir, "ckpt"), 0o755); err != nil {
		return nil, err
	}
	w := &Writer{dir: dir, Meta: meta, cells: cells}
	var err error
	open := func(name string) *os.File {
		if err != nil {
			return nil
		}
		var f *os.File
		f, err = os.Create(filepath.Join(dir, name))
		return f
	}
	w.fb, w.fi, w.fm, w.fe = open("frames.bin"), open("frames.idx"), open("metrics.jsonl"), open("events.jsonl")
	if err != nil {
		return nil, err
	}
	w.enc, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	return w, w.SaveMeta()
}

func (w *Writer) SaveMeta() error {
	b, _ := json.MarshalIndent(w.Meta, "", " ")
	tmp := filepath.Join(w.dir, "run.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(w.dir, "run.json"))
}

// AddFrame：关键帧直接压缩；其余帧的格子部分与上一帧逐字节异或后压缩(变化少→大量 0)。
func (w *Writer) AddFrame(year int, raw []byte) error {
	key := w.n%KeyEvery == 0
	cb := sim.FrameCellBytes(w.cells)
	// 慢变字段：非刷新年份沿用上一帧的值
	if year%sim.SlowEvery != 0 && len(w.prev) >= cb && len(raw) >= cb {
		for id := range sim.SlowFields {
			off, n := sim.FieldOffset(id, w.cells)
			copy(raw[off:off+n], w.prev[off:off+n])
		}
	}
	payload := raw
	if !key && len(w.prev) >= cb && len(raw) >= cb {
		payload = make([]byte, len(raw))
		copy(payload, raw)
		for i := 12; i < cb; i++ {
			payload[i] ^= w.prev[i]
		}
	}
	comp := w.enc.EncodeAll(payload, nil)
	if _, err := w.fb.Write(comp); err != nil {
		return err
	}
	e := IndexEntry{Year: int32(year), Offset: w.off, Length: int32(len(comp)), Key: key}
	if err := binary.Write(w.fi, binary.LittleEndian, e); err != nil {
		return err
	}
	w.off += int64(len(comp))
	w.prev = raw
	w.n++
	w.mu.Lock()
	w.index = append(w.index, e)
	w.mu.Unlock()
	return nil
}

func (w *Writer) AddMetrics(m sim.Metrics) error {
	b, _ := json.Marshal(m)
	w.mu.Lock()
	w.metrics = append(w.metrics, m)
	w.mu.Unlock()
	_, err := w.fm.Write(append(b, '\n'))
	return err
}

func (w *Writer) AddEvents(ev []sim.Event) error {
	for _, e := range ev {
		b, _ := json.Marshal(e)
		if _, err := w.fe.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return nil
}

func (w *Writer) SavePolities(p map[int64]*sim.PolityInfo) error {
	list := make([]*sim.PolityInfo, 0, len(p))
	for _, v := range p {
		list = append(list, v)
	}
	sort.Slice(list, func(a, b int) bool { return list[a].UID < list[b].UID })
	b, _ := json.Marshal(list)
	tmp := filepath.Join(w.dir, "polities.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(w.dir, "polities.json"))
}

func (w *Writer) Sync() {
	w.fb.Sync()
	w.fi.Sync()
	w.fm.Sync()
	w.fe.Sync()
}

func (w *Writer) Close() error {
	w.Sync()
	for _, f := range []*os.File{w.fb, w.fi, w.fm, w.fe} {
		f.Close()
	}
	return w.SaveMeta()
}

func (w *Writer) Index() []IndexEntry {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]IndexEntry(nil), w.index...)
}

func (w *Writer) Metrics() []sim.Metrics {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]sim.Metrics(nil), w.metrics...)
}

// ---- 读取 ----

type Reader struct {
	Dir   string
	Meta  RunMeta
	cells int
	dec   *zstd.Decoder
	mu    sync.Mutex
	cache map[int][]byte // 帧序号 → 原始帧(只缓存关键帧链附近)
	index []IndexEntry
	// 写入中的 run 由 live 提供最新索引
	live func() []IndexEntry
}

func Open(dir string, cells int, live func() []IndexEntry) (*Reader, error) {
	b, err := os.ReadFile(filepath.Join(dir, "run.json"))
	if err != nil {
		return nil, err
	}
	r := &Reader{Dir: dir, cells: cells, cache: map[int][]byte{}, live: live}
	if err := json.Unmarshal(b, &r.Meta); err != nil {
		return nil, err
	}
	r.dec, _ = zstd.NewReader(nil)
	if live == nil {
		if err := r.loadIndex(); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (r *Reader) loadIndex() error {
	b, err := os.ReadFile(filepath.Join(r.Dir, "frames.idx"))
	if err != nil {
		return err
	}
	var idx []IndexEntry
	rd := bytes.NewReader(b)
	for {
		var e IndexEntry
		if err := binary.Read(rd, binary.LittleEndian, &e); err != nil {
			break
		}
		idx = append(idx, e)
	}
	r.index = idx
	return nil
}

func (r *Reader) Index() []IndexEntry {
	if r.live != nil {
		return r.live()
	}
	return r.index
}

// Frame 返回某年(取不晚于该年的最近一帧)的原始帧。
func (r *Reader) Frame(year int) ([]byte, int, error) {
	idx := r.Index()
	if len(idx) == 0 {
		return nil, 0, fmt.Errorf("还没有帧")
	}
	k := sort.Search(len(idx), func(i int) bool { return int(idx[i].Year) > year }) - 1
	if k < 0 {
		k = 0
	}
	b, err := r.frameAt(idx, k)
	return b, int(idx[k].Year), err
}

func (r *Reader) frameAt(idx []IndexEntry, k int) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if b, ok := r.cache[k]; ok {
		return b, nil
	}
	// 从最近关键帧(或已缓存帧)向后解码
	start := k
	for start > 0 && !idx[start].Key {
		if _, ok := r.cache[start-1]; ok {
			break
		}
		start--
	}
	f, err := os.Open(filepath.Join(r.Dir, "frames.bin"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var prev []byte
	if start > 0 && !idx[start].Key {
		prev = r.cache[start-1]
	}
	cb := sim.FrameCellBytes(r.cells)
	for j := start; j <= k; j++ {
		comp := make([]byte, idx[j].Length)
		if _, err := f.ReadAt(comp, idx[j].Offset); err != nil {
			return nil, err
		}
		raw, err := r.dec.DecodeAll(comp, nil)
		if err != nil {
			return nil, err
		}
		if !idx[j].Key && prev != nil && len(raw) >= cb && len(prev) >= cb {
			for i := 12; i < cb; i++ {
				raw[i] ^= prev[i]
			}
		}
		prev = raw
		if len(r.cache) > 64 {
			r.cache = map[int][]byte{}
		}
		r.cache[j] = raw
	}
	return prev, nil
}

func ReadMetrics(dir string) ([]sim.Metrics, error) {
	f, err := os.Open(filepath.Join(dir, "metrics.jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []sim.Metrics
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m sim.Metrics
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			out = append(out, m)
		}
	}
	return out, nil
}

func ReadEvents(dir string, minYear, maxYear int) ([]sim.Event, error) {
	f, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []sim.Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e sim.Event
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Year >= minYear && e.Year <= maxYear {
			out = append(out, e)
		}
	}
	return out, nil
}

// ---- 检查点 ----

func SaveCheckpoint(dir string, s *sim.State) error {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(s.Snapshot()); err != nil {
		return err
	}
	enc, _ := zstd.NewWriter(nil)
	b := enc.EncodeAll(buf.Bytes(), nil)
	name := filepath.Join(dir, "ckpt", fmt.Sprintf("%06d.bin.zst", s.Year))
	return os.WriteFile(name, b, 0o644)
}

// LoadCheckpoint 读取不晚于 year 的最近检查点。
func LoadCheckpoint(dir string, year int, w *world.World) (*sim.State, error) {
	ents, err := os.ReadDir(filepath.Join(dir, "ckpt"))
	if err != nil {
		return nil, err
	}
	best := -1
	var bestName string
	for _, e := range ents {
		var y int
		if _, err := fmt.Sscanf(e.Name(), "%06d.bin.zst", &y); err == nil && y <= year && y > best {
			best, bestName = y, e.Name()
		}
	}
	if best < 0 {
		return nil, fmt.Errorf("没有不晚于 %d 年的检查点", year)
	}
	b, err := os.ReadFile(filepath.Join(dir, "ckpt", bestName))
	if err != nil {
		return nil, err
	}
	dec, _ := zstd.NewReader(nil)
	raw, err := dec.DecodeAll(b, nil)
	if err != nil {
		return nil, err
	}
	var sn sim.Snapshot
	if err := gob.NewDecoder(io.Reader(bytes.NewReader(raw))).Decode(&sn); err != nil {
		return nil, err
	}
	return sim.Restore(&sn, w), nil
}
