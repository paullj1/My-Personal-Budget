package receipt

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// pngOf renders near-uniform speckle: an image with no document-shaped region, so
// detection declines and these tests exercise the geometry fallback path.
//
// A gradient will not do. Its bright bands are rectangular and high-contrast
// enough to clear the detection guards, so the image gets cropped and the test
// ends up measuring detection instead of what it meant to measure.
func pngOf(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// Deterministic pseudo-noise around mid grey: no coherent bright shape.
			n := uint8(120 + (x*7+y*13)%9)
			img.Set(x, y, color.RGBA{n, n, n, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func TestNormalizePreservesOrientation(t *testing.T) {
	// Orientation must be left as photographed. Forcing a frame shape lays the
	// receipt on its side and rotates every line of text, which measurably
	// degrades extraction -- the goal is upright text, not a landscape frame.
	for _, dims := range [][2]int{{500, 1200}, {600, 800}, {800, 600}, {3024, 4032}} {
		out, info, err := Normalize(pngOf(dims[0], dims[1]), DefaultMaxEdge)
		if err != nil {
			t.Fatalf("Normalize(%dx%d): %v", dims[0], dims[1], err)
		}
		if info.Rotated {
			t.Errorf("%dx%d was rotated with no EXIF tag present", dims[0], dims[1])
		}
		portraitIn := dims[1] > dims[0]
		portraitOut := info.Height > info.Width
		if portraitIn != portraitOut {
			t.Errorf("%dx%d changed orientation to %dx%d", dims[0], dims[1], info.Width, info.Height)
		}
		if _, err := jpeg.Decode(bytes.NewReader(out)); err != nil {
			t.Errorf("output is not decodable JPEG: %v", err)
		}
	}
}

func TestNormalizeBoundsTheLongEdge(t *testing.T) {
	cases := []struct{ w, h, maxEdge int }{
		{4032, 3024, 1600},
		{3024, 4032, 1600},
		{800, 600, 1600}, // already small: must not upscale
		{2000, 500, 1000},
	}
	for _, tc := range cases {
		_, info, err := Normalize(pngOf(tc.w, tc.h), tc.maxEdge)
		if err != nil {
			t.Fatalf("Normalize(%dx%d): %v", tc.w, tc.h, err)
		}
		long := max(info.Width, info.Height)
		if long > tc.maxEdge {
			t.Errorf("%dx%d -> %dx%d exceeds maxEdge %d", tc.w, tc.h, info.Width, info.Height, tc.maxEdge)
		}
		// Guard the bug that pinned width instead of the long edge, silently
		// upscaling portrait sources.
		srcLong := max(tc.w, tc.h)
		if srcLong <= tc.maxEdge && long > srcLong {
			t.Errorf("%dx%d was upscaled to %dx%d", tc.w, tc.h, info.Width, info.Height)
		}
	}
}

func TestNormalizePreservesAspectRatio(t *testing.T) {
	_, info, err := Normalize(pngOf(4000, 3000), 1600)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	want := 4000.0 / 3000.0
	got := float64(info.Width) / float64(info.Height)
	if diff := got - want; diff > 0.02 || diff < -0.02 {
		t.Errorf("aspect ratio drifted: %.3f vs %.3f (%dx%d)", got, want, info.Width, info.Height)
	}
}

func TestNormalizeRejectsGarbage(t *testing.T) {
	if _, _, err := Normalize([]byte("not an image"), DefaultMaxEdge); err == nil {
		t.Error("expected an error for non-image input")
	}
	if _, _, err := Normalize(nil, DefaultMaxEdge); err == nil {
		t.Error("expected an error for empty input")
	}
}

// jpegWithOrientation builds a JPEG carrying an EXIF orientation tag, matching
// the structure of the iPhone photo that reversed the orientation logic.
func jpegWithOrientation(w, h, orientation int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// Speckle, not a gradient: see pngOf.
			n := uint8(120 + (x*7+y*13)%9)
			img.Set(x, y, color.RGBA{n, n, n, 255})
		}
	}
	var base bytes.Buffer
	if err := jpeg.Encode(&base, img, &jpeg.Options{Quality: 90}); err != nil {
		panic(err)
	}
	raw := base.Bytes()

	// TIFF header + one IFD entry for tag 0x0112.
	tiff := make([]byte, 0, 26)
	tiff = append(tiff, 'M', 'M', 0, 42)
	tiff = binary.BigEndian.AppendUint32(tiff, 8)
	tiff = binary.BigEndian.AppendUint16(tiff, 1)
	tiff = binary.BigEndian.AppendUint16(tiff, 0x0112)
	tiff = binary.BigEndian.AppendUint16(tiff, 3)
	tiff = binary.BigEndian.AppendUint32(tiff, 1)
	tiff = binary.BigEndian.AppendUint16(tiff, uint16(orientation))
	tiff = append(tiff, 0, 0)
	tiff = binary.BigEndian.AppendUint32(tiff, 0)

	payload := append([]byte("Exif\x00\x00"), tiff...)
	app1 := []byte{0xFF, 0xE1}
	app1 = binary.BigEndian.AppendUint16(app1, uint16(len(payload)+2))
	app1 = append(app1, payload...)

	out := make([]byte, 0, len(raw)+len(app1))
	out = append(out, raw[:2]...) // SOI
	out = append(out, app1...)
	out = append(out, raw[2:]...)
	return out
}

func TestJPEGOrientationParsing(t *testing.T) {
	for _, want := range []int{1, 3, 6, 8} {
		data := jpegWithOrientation(400, 300, want)
		if got := jpegOrientation(data); got != want {
			t.Errorf("jpegOrientation = %d, want %d", got, want)
		}
	}
	// No EXIF at all must default to 1 rather than misreport.
	if got := jpegOrientation(pngOf(10, 10)); got != 1 {
		t.Errorf("expected default orientation 1 for non-JPEG, got %d", got)
	}
}

func TestNormalizeAppliesEXIFOrientation(t *testing.T) {
	// Orientation=6 means stored landscape, displayed portrait -- exactly the test
	// photo. 400x300 stored must come out 300x400, matching what every viewer
	// shows and what the photographer framed.
	_, info, err := Normalize(jpegWithOrientation(400, 300, 6), DefaultMaxEdge)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if !info.Rotated {
		t.Error("expected EXIF orientation to be applied")
	}
	if info.Width != 300 || info.Height != 400 {
		t.Errorf("expected 300x400 after applying EXIF, got %dx%d", info.Width, info.Height)
	}
}

func TestNormalizeEXIFProducesTallImage(t *testing.T) {
	// Stored 1200x500 landscape with Orientation=6 displays as 500x1200, and that
	// is what extraction should see.
	_, info, err := Normalize(jpegWithOrientation(1200, 500, 6), DefaultMaxEdge)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if info.Width != 500 || info.Height != 1200 {
		t.Errorf("expected 500x1200 after applying EXIF, got %dx%d", info.Width, info.Height)
	}
}

func TestRotationsRoundTrip(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	src.Set(0, 0, color.RGBA{1, 0, 0, 255})
	src.Set(2, 1, color.RGBA{2, 0, 0, 255})

	if got := rotate90(src).Bounds(); got.Dx() != 2 || got.Dy() != 3 {
		t.Errorf("rotate90 bounds = %v, want 2x3", got)
	}
	if got := rotate270(src).Bounds(); got.Dx() != 2 || got.Dy() != 3 {
		t.Errorf("rotate270 bounds = %v, want 2x3", got)
	}
	if got := rotate180(src).Bounds(); got.Dx() != 3 || got.Dy() != 2 {
		t.Errorf("rotate180 bounds = %v, want 3x2", got)
	}
	// Four quarter turns is the identity.
	round := rotate90(rotate90(rotate90(rotate90(src))))
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			if round.At(x, y) != src.At(x, y) {
				t.Fatalf("pixel (%d,%d) changed after four rotations", x, y)
			}
		}
	}
	// A quarter turn each way cancels.
	back := rotate270(rotate90(src))
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			if back.At(x, y) != src.At(x, y) {
				t.Fatalf("pixel (%d,%d) changed after opposing rotations", x, y)
			}
		}
	}
}

func TestExifTurnsAndNetRotation(t *testing.T) {
	// Clockwise quarter turns per EXIF value.
	for orientation, want := range map[int]int{1: 0, 2: 0, 3: 2, 4: 2, 5: 1, 6: 1, 7: 3, 8: 3} {
		if got := exifTurns(orientation); got != want {
			t.Errorf("exifTurns(%d) = %d, want %d", orientation, got, want)
		}
	}
	// Four turns is the identity, and out-of-range input is normalized.
	src := pngOf(4, 2)
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := rotateQuarterTurns(img, 4).Bounds(); got.Dx() != 4 || got.Dy() != 2 {
		t.Errorf("4 turns = %v, want the identity", got)
	}
	if got := rotateQuarterTurns(img, -1).Bounds(); got.Dx() != 2 || got.Dy() != 4 {
		t.Errorf("-1 turns = %v, want a quarter turn", got)
	}
}
