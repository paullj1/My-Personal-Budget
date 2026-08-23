package receipt

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
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

// pngDeclaring builds a PNG of the given dimensions as cheaply as possible: one
// byte per pixel, uniform, so it compresses to almost nothing and encodes fast.
// For tests of the header-size guard, which never decodes the pixels -- pngOf's
// per-pixel RGBA speckle would allocate 192MB to test a 48MP rejection.
func pngDeclaring(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Skipf("could not build a %dx%d fixture: %v", w, h, err)
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

// These fixtures contain no document, so detection declines and the uncropped
// bound applies. The tighter maxEdge is for crops, where the background is gone.
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
		bound := max(tc.maxEdge, UncroppedMaxEdge)
		if info.Cropped {
			bound = tc.maxEdge
		}
		if long > bound {
			t.Errorf("%dx%d -> %dx%d exceeds bound %d (cropped=%v)",
				tc.w, tc.h, info.Width, info.Height, bound, info.Cropped)
		}
		// Guard the bug that pinned width instead of the long edge, silently
		// upscaling portrait sources.
		srcLong := max(tc.w, tc.h)
		if long > srcLong {
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

// A byte-size limit does not bound memory: a highly compressible PNG of a few
// hundred KB can declare hundreds of millions of pixels. The header check rejects
// it before any pixels are allocated.
func TestNormalizeRejectsDecompressionBomb(t *testing.T) {
	// 20000x20000 = 400M pixels, well past MaxPixels, and it compresses tiny.
	huge := image.NewGray(image.Rect(0, 0, 20000, 20000))
	var buf bytes.Buffer
	if err := png.Encode(&buf, huge); err != nil {
		t.Skipf("could not build the fixture: %v", err)
	}
	t.Logf("fixture: %d bytes declaring %d megapixels", buf.Len(), 20000*20000/1_000_000)

	_, _, err := Normalize(buf.Bytes(), DefaultMaxEdge)
	if err == nil {
		t.Fatal("expected an oversized image to be rejected")
	}
	if !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("error should wrap ErrImageTooLarge so the handler can answer 413: %v", err)
	}
}

func TestNormalizeAcceptsPhoneSizedImages(t *testing.T) {
	// What the app actually sends: the browser re-encodes to a 3200px long edge
	// before upload. This is the size that must never be refused.
	if int64(2400)*3200 > MaxPixels {
		t.Errorf("MaxPixels %d rejects a browser-normalized upload", MaxPixels)
	}
	// And a native 12MP phone photo, which is what a direct API caller or the smoke
	// script posts.
	if int64(4032)*3024 > MaxPixels {
		t.Errorf("MaxPixels %d rejects a native 12MP phone photo", MaxPixels)
	}
	_, info, err := Normalize(pngOf(1200, 900), DefaultMaxEdge)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if info.Width == 0 {
		t.Error("expected a normalized image")
	}
}

// A pixel-count limit does not bound the resampler. Above UncroppedMaxEdge the
// uncropped path engages Catmull-Rom, whose separable-pass scratch is about 32
// bytes per source pixel: measured peak RSS was 74MB at 4032x3024 but 559MB at
// 4618x3464, which is the same number of megapixels either side of the cliff. A
// soft memory limit does not help, because that scratch is live rather than
// collectable. Refusing the long edge keeps the expensive path unreachable.
func TestNormalizeRefusesOverlyLongEdges(t *testing.T) {
	// 16MP, inside MaxPixels, but elongated enough to trigger the resampler.
	_, _, err := Normalize(pngDeclaring(t, 4618, 3464), DefaultMaxEdge)
	if err == nil {
		t.Fatal("expected a 4618px long edge to be refused")
	}
	if !errors.Is(err, ErrImageTooLarge) {
		t.Errorf("should wrap ErrImageTooLarge so the handler answers 413, got %v", err)
	}
	if !strings.Contains(err.Error(), "long edge") {
		t.Errorf("error should say which guard rejected it: %v", err)
	}
	// The two guards are independent; neither implies the other.
	if int64(4618)*3464 > MaxPixels {
		t.Error("fixture should be inside MaxPixels, or it tests the wrong guard")
	}
	// A native 12MP photo sits just under the bound and must still pass.
	if 4032 > MaxLongEdge {
		t.Errorf("MaxLongEdge %d rejects a native 12MP phone photo", MaxLongEdge)
	}
}

// The limit deliberately refuses originals from a high-megapixel sensor. This is a
// behaviour change, not an oversight: honouring a 48MP photo at native resolution
// costs ~955MB of transient allocation, which OOM-killed the API on a 1GB host and,
// with no container memory limit set, took a global OOM with it. A 413 naming the
// limit is the better failure. The app is unaffected because the browser resizes
// first; only a direct caller posting an original sees this.
func TestNormalizeRefusesHighMegapixelOriginals(t *testing.T) {
	for _, tc := range []struct {
		name string
		w, h int
	}{
		{"24MP", 5712, 4284},
		{"48MP", 8000, 6000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A uniform gray PNG, not pngOf's speckle: the guard reads only the
			// header, so there is no reason to allocate and encode 48M pixels of
			// incompressible noise to test it.
			_, _, err := Normalize(pngDeclaring(t, tc.w, tc.h), DefaultMaxEdge)
			if err == nil {
				t.Fatalf("expected %s to be refused", tc.name)
			}
			if !errors.Is(err, ErrImageTooLarge) {
				t.Errorf("should wrap ErrImageTooLarge so the handler answers 413, got %v", err)
			}
			// The caller can only act on this if the message says what the limit is.
			if !strings.Contains(err.Error(), "megapixel") {
				t.Errorf("error should name the limit in megapixels: %v", err)
			}
		})
	}
}

// When detection declines, the background is still in the frame, so discarding
// resolution as well leaves the print too small: a Lowe's receipt on brushed
// steel -- where paper and surface merge under a brightness threshold -- read
// nothing at 1536x2048 and read its totals correctly at native 3024x4032.
func TestNormalizeKeepsResolutionWhenNothingWasCropped(t *testing.T) {
	// Speckle has no document-shaped region, so detection declines.
	out, info, err := Normalize(pngOf(3024, 4032), 2048)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if info.Cropped {
		t.Fatal("expected no crop on an image with no document in it")
	}
	if long := max(info.Width, info.Height); long <= 2048 {
		t.Errorf("uncropped image was reduced to %dx%d; the detail is all it has",
			info.Width, info.Height)
	}
	if long := max(info.Width, info.Height); long > UncroppedMaxEdge {
		t.Errorf("uncropped image %dx%d exceeds the safety bound %d", info.Width, info.Height, UncroppedMaxEdge)
	}
	if len(out) == 0 {
		t.Error("expected encoded output")
	}
}

// A crop removed the background, so the tighter bound still applies there.
func TestNormalizeStillBoundsACrop(t *testing.T) {
	img := strip(3000, 2400, 2200, 700, 8)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 92}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	_, info, err := Normalize(buf.Bytes(), 1600)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if !info.Cropped {
		t.Fatalf("expected a crop: %+v", info.Detect)
	}
	if long := max(info.Width, info.Height); long > 1600 {
		t.Errorf("crop is %dx%d, want the long edge bounded to 1600", info.Width, info.Height)
	}
}

// A crop the source could not supply at a legible size is lifted to MinCropEdge.
//
// Upscaling adds no information, so this exists only because the vision encoder
// tiles into fixed patches: a receipt photographed at 1080x1920 crops to 764x1191
// and misread a printed 16.00 as 15.00, which 1.5x fixed for 6 seconds. Raising
// maxEdge cannot help such an image -- the source is exhausted.
func TestExtractRectLiftsSmallCrops(t *testing.T) {
	// Long axis vertical, so U points down and V across.
	small := rotRect{
		Center: point{X: 400, Y: 600},
		U:      point{X: 0, Y: 1},
		V:      point{X: 1, Y: 0},
		Long:   1191,
		Short:  764,
	}
	src := image.NewRGBA(image.Rect(0, 0, 1080, 1920))
	out, err := extractRect(src, small, 2048)
	if err != nil {
		t.Fatalf("extractRect: %v", err)
	}
	got := out.Bounds().Dy()
	if got != MinCropEdge {
		t.Errorf("small crop long edge = %d, want it lifted to %d", got, MinCropEdge)
	}
	// Aspect ratio must survive the lift.
	wantRatio := small.Short / small.Long
	gotRatio := float64(out.Bounds().Dx()) / float64(out.Bounds().Dy())
	if diff := gotRatio - wantRatio; diff > 0.01 || diff < -0.01 {
		t.Errorf("aspect drifted: %.4f vs %.4f", gotRatio, wantRatio)
	}
}

// A crop that is already legible pays nothing: no upscale, no extra prefill.
func TestExtractRectLeavesAdequateCropsAlone(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4032, 3024))
	for _, long := range []float64{MinCropEdge, MinCropEdge + 200, 2048} {
		rect := rotRect{
			Center: point{X: 2000, Y: 1500},
			U:      point{X: 0, Y: 1},
			V:      point{X: 1, Y: 0},
			Long:   long,
			Short:  long / 2.7,
		}
		out, err := extractRect(src, rect, 2048)
		if err != nil {
			t.Fatalf("extractRect(%v): %v", long, err)
		}
		if got := out.Bounds().Dy(); float64(got) > long+1 {
			t.Errorf("crop of %v was upscaled to %d; adequate crops must be untouched", long, got)
		}
	}
}

// maxEdge still wins. It is the operator's bound, so the floor must not push past
// it even when the crop is small.
func TestExtractRectFloorNeverExceedsMaxEdge(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1080, 1920))
	rect := rotRect{
		Center: point{X: 400, Y: 600},
		U:      point{X: 0, Y: 1},
		V:      point{X: 1, Y: 0},
		Long:   1191,
		Short:  764,
	}
	const tight = 1400
	out, err := extractRect(src, rect, tight)
	if err != nil {
		t.Fatalf("extractRect: %v", err)
	}
	if got := out.Bounds().Dy(); got > tight {
		t.Errorf("long edge = %d, must not exceed maxEdge %d", got, tight)
	}
}

// A very small crop is more often a bad detection than a small receipt, so the
// interpolation factor is capped rather than blowing it up to the floor.
func TestExtractRectCapsTheUpscaleFactor(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1080, 1920))
	rect := rotRect{
		Center: point{X: 400, Y: 600},
		U:      point{X: 0, Y: 1},
		V:      point{X: 1, Y: 0},
		Long:   300,
		Short:  120,
	}
	out, err := extractRect(src, rect, 2048)
	if err != nil {
		t.Fatalf("extractRect: %v", err)
	}
	got := float64(out.Bounds().Dy())
	if limit := rect.Long * maxCropUpscale; got > limit+1 {
		t.Errorf("long edge = %.0f, exceeds the %.1fx cap (%.0f)", got, maxCropUpscale, limit)
	}
	if got >= MinCropEdge {
		t.Errorf("a 300px crop reached the floor at %.0f; the factor cap should bind first", got)
	}
}
