// simtest：不开界面的快速演化，用于调参。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"mapsim/sim"
	"mapsim/world"
)

func main() {
	dir := flag.String("world", "../cache/dev", "预处理输出目录")
	years := flag.Int("years", 300, "")
	every := flag.Int("every", 25, "")
	pj := flag.String("params", "{}", "覆盖参数(JSON)")
	flag.Parse()
	w, err := world.Load(*dir)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	p := sim.DefaultParams()
	if err := json.Unmarshal([]byte(*pj), &p); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	t0 := time.Now()
	s := sim.NewState(w, p)
	fmt.Printf("land=%d S=%.0f 人/单位 init=%.1fs\n", len(w.LandIdx), s.PeoplePerUnit, time.Since(t0).Seconds())
	t0 = time.Now()
	prev := s.TotalCnt
	for y := 0; y < *years; y++ {
		s.Step()
		if (y+1)%*every == 0 {
			m := s.Metrics()
			d := s.TotalCnt
			m.Conquests, m.Rebellions, m.Wars, m.Annexations = d.Conquests-prev.Conquests, d.Rebellions-prev.Rebellions, d.Wars-prev.Wars, d.Annexations-prev.Annexations
			m.Splits, m.Defects = d.SuccessionSplits-prev.SuccessionSplits, d.Defects-prev.Defects
			prev = d
			fmt.Printf("y=%4d fam=%6d pop=%6.1fM roots=%6d pol10=%4d largest=%.3f top5=%.3f depth=%.2f conq=%4d reb=%4d wars=%3d annex=%2d succ=%2d def=%4d  %.1fms/yr\n",
				m.Year, m.Families, m.Population/1e6, m.Roots, m.Polities10, m.Largest, m.Top5, m.MeanDepth,
				m.Conquests, m.Rebellions, m.Wars, m.Annexations, m.Splits, m.Defects,
				float64(time.Since(t0).Milliseconds())/float64(y+1))
		}
	}
}
