package client

import (
	"image"
	"image/color"
	"runtime"
	"testing"
)

func TestParallelDesktopRowsCoversRows(t *testing.T) {
	var rows [17]int
	parallelDesktopRows(4096, len(rows), func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			rows[y]++
		}
	})
	for y, count := range rows {
		if count != 1 {
			t.Fatalf("row %d processed %d times", y, count)
		}
	}
}

func TestResizeDesktopFrameMatchesSerialNearestNeighbor(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 96, 64))
	for y := 0; y < src.Bounds().Dy(); y++ {
		for x := 0; x < src.Bounds().Dx(); x++ {
			src.SetRGBA(x, y, color.RGBA{R: byte(x * 3), G: byte(y * 5), B: byte(x + y), A: 255})
		}
	}
	got := resizeDesktopFrame(src, 32, 32)
	wantSize := scaledDimension(src.Bounds().Dx(), src.Bounds().Dy(), 32, 32)
	if got.Bounds().Dx() != wantSize.X || got.Bounds().Dy() != wantSize.Y {
		t.Fatalf("resized to %v, want %v", got.Bounds().Size(), wantSize)
	}
	for y := 0; y < got.Bounds().Dy(); y++ {
		for x := 0; x < got.Bounds().Dx(); x++ {
			sourceX := x * src.Bounds().Dx() / got.Bounds().Dx()
			sourceY := y * src.Bounds().Dy() / got.Bounds().Dy()
			if got.RGBAAt(x, y) != src.RGBAAt(sourceX, sourceY) {
				t.Fatalf("pixel %d,%d = %v, want %v", x, y, got.RGBAAt(x, y), src.RGBAAt(sourceX, sourceY))
			}
		}
	}
}

func BenchmarkResizeDesktopFrame(b *testing.B) {
	src := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
	for i := range src.Pix {
		src.Pix[i] = byte(i)
	}
	runtime.GOMAXPROCS(runtime.NumCPU())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = resizeDesktopFrame(src, 1600, 1000)
	}
}
