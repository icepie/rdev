package client

import (
	"runtime"
	"sync"
)

const desktopParallelPixelThreshold = 512 * 512

func parallelDesktopRows(width, height int, work func(y0, y1 int)) {
	if width <= 0 || height <= 0 {
		return
	}
	workers := runtime.GOMAXPROCS(0)
	if workers <= 1 || width*height < desktopParallelPixelThreshold {
		work(0, height)
		return
	}
	if workers > height {
		workers = height
	}
	if workers > 16 {
		workers = 16
	}
	rowsPerWorker := (height + workers - 1) / workers
	var wg sync.WaitGroup
	for y0 := 0; y0 < height; y0 += rowsPerWorker {
		y1 := y0 + rowsPerWorker
		if y1 > height {
			y1 = height
		}
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			work(start, end)
		}(y0, y1)
	}
	wg.Wait()
}
