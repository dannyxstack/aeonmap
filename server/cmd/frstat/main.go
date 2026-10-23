package main

import (
	"fmt"
	"os"

	"github.com/klauspost/compress/zstd"

	"mapsim/sim"
	"mapsim/store"
)

func main() {
	dir := os.Args[1]
	n := 28969
	r, err := store.Open(dir, n, nil)
	if err != nil {
		panic(err)
	}
	idx := r.Index()
	b, y, _ := r.Frame(int(idx[len(idx)-1].Year))
	pols, _ := sim.DecodeFramePolities(b, n)
	fmt.Println("year", y, "raw", len(b), "cellbytes", sim.FrameCellBytes(n), "polities", len(pols))
	enc, _ := zstd.NewWriter(nil)
	off := 12
	for _, f := range sim.FrameFields {
		seg := b[off : off+f.Bytes*n]
		fmt.Printf("%-8s raw %6d zstd %6d\n", f.ID, len(seg), len(enc.EncodeAll(seg, nil)))
		off += f.Bytes * n
	}
	fmt.Printf("poltable raw %d zstd %d\n", len(b)-off, len(enc.EncodeAll(b[off:], nil)))
	var tot int
	for _, e := range idx {
		tot += int(e.Length)
	}
	fmt.Println("avg compressed frame", tot/len(idx))
}
