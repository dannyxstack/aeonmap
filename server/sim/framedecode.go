package sim

import (
	"encoding/binary"
	"fmt"
	"math"
)

// FrameData 是解码后的帧(全网格数组，非陆地格为 0)。
type FrameData struct {
	Year  int
	Pop   []float32 // 人
	Fam   []uint16
	DMode []uint8
	Tribe []uint16 // 政权表下标(0 无，PolScattered 零散)
	Top1  []uint16
	Top2  []uint16
	Share []uint8
	Pols  []FramePolity // 下标 k 对应 Pols[k-1]
}

func PolityName(uid int64) string { return polityName(uid) }

// DecodeFrame 解码 BuildFrame 生成的帧；landIdx 为世界的陆地格下标，n 为全网格格数。
func DecodeFrame(b []byte, landIdx []int32, n int) (*FrameData, error) {
	if len(b) < 12 || string(b[:4]) != "MSF1" {
		return nil, fmt.Errorf("帧格式错误")
	}
	nl := int(binary.LittleEndian.Uint32(b[8:]))
	if nl != len(landIdx) {
		return nil, fmt.Errorf("帧格数 %d 与世界 %d 不符", nl, len(landIdx))
	}
	if len(b) < FrameCellBytes(nl) {
		return nil, fmt.Errorf("帧截断")
	}
	fd := &FrameData{Year: int(binary.LittleEndian.Uint32(b[4:])), Pop: make([]float32, n), Fam: make([]uint16, n),
		DMode: make([]uint8, n), Tribe: make([]uint16, n), Top1: make([]uint16, n), Top2: make([]uint16, n), Share: make([]uint8, n)}
	u16 := func(id string, dst []uint16) {
		off, _ := FieldOffset(id, nl)
		for k, c := range landIdx {
			dst[c] = binary.LittleEndian.Uint16(b[off+2*k:])
		}
	}
	u8 := func(id string, dst []uint8) {
		off, _ := FieldOffset(id, nl)
		for k, c := range landIdx {
			dst[c] = b[off+k]
		}
	}
	pq := make([]uint16, n)
	u16("pop", pq)
	for _, c := range landIdx {
		fd.Pop[c] = float32(math.Pow(10, float64(pq[c])/10000) - 1)
	}
	u16("fam", fd.Fam)
	u8("dmode", fd.DMode)
	u16("tribe", fd.Tribe)
	u16("top1", fd.Top1)
	u16("top2", fd.Top2)
	u8("share", fd.Share)
	pols, err := DecodeFramePolities(b, nl)
	if err != nil {
		return nil, err
	}
	fd.Pols = pols
	return fd, nil
}

// PolUID 把帧中的政权下标转换为 UID(0 无/零散)。
func (fd *FrameData) PolUID(k uint16) int64 {
	if k == 0 || k == PolScattered || int(k) > len(fd.Pols) {
		return 0
	}
	return fd.Pols[k-1].UID
}
