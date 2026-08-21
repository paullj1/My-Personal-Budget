package receipt

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"testing"
)

// strip renders a bright rectangle of the given size, rotated by angleDeg, on a
// dark background -- a stand-in for a receipt photographed on a car seat.
func strip(frameW, frameH int, longLen, shortLen float64, angleDeg float64) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, frameW, frameH))
	dark := color.RGBA{28, 28, 30, 255}
	for y := 0; y < frameH; y++ {
		for x := 0; x < frameW; x++ {
			img.Set(x, y, dark)
		}
	}
	theta := angleDeg * math.Pi / 180
	// Long axis direction, and its perpendicular.
	ux, uy := math.Sin(theta), math.Cos(theta)
	vx, vy := math.Cos(theta), -math.Sin(theta)
	cx, cy := float64(frameW)/2, float64(frameH)/2

	paper := color.RGBA{238, 238, 236, 255}
	for y := 0; y < frameH; y++ {
		for x := 0; x < frameW; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			along := dx*ux + dy*uy
			across := dx*vx + dy*vy
			if math.Abs(along) <= longLen/2 && math.Abs(across) <= shortLen/2 {
				img.Set(x, y, paper)
			}
		}
	}
	return img
}

func TestDetectDocumentFindsAnUprightStrip(t *testing.T) {
	img := strip(800, 800, 600, 200, 0)
	rect, info, ok := DetectDocument(img)
	if !ok {
		t.Fatalf("expected detection, got %+v", info)
	}
	if !info.Detected {
		t.Error("info.Detected should be true")
	}
	// Allow slack for the working-resolution downscale and the crop padding.
	if math.Abs(rect.Long-600) > 40 {
		t.Errorf("long side = %.0f, want ~600", rect.Long)
	}
	if math.Abs(rect.Short-200) > 40 {
		t.Errorf("short side = %.0f, want ~200", rect.Short)
	}
	if math.Abs(info.Aspect-3) > 0.3 {
		t.Errorf("aspect = %.2f, want ~3", info.Aspect)
	}
	if info.Fill < 0.85 {
		t.Errorf("fill = %.2f, want a nearly perfect rectangle", info.Fill)
	}
}

func TestDetectDocumentHandlesRotation(t *testing.T) {
	// Rotating calipers should recover the strip's true size at any angle, which
	// is what makes deskewing possible.
	for _, angle := range []float64{-40, -20, -5, 0, 5, 20, 40, 88} {
		img := strip(900, 900, 640, 220, angle)
		rect, info, ok := DetectDocument(img)
		if !ok {
			t.Errorf("angle %.0f: no detection (%s)", angle, info.Reason)
			continue
		}
		if math.Abs(rect.Long-640) > 60 {
			t.Errorf("angle %.0f: long = %.0f, want ~640", angle, rect.Long)
		}
		if math.Abs(rect.Short-220) > 60 {
			t.Errorf("angle %.0f: short = %.0f, want ~220", angle, rect.Short)
		}
	}
}

func TestDetectDocumentOrientationIsDeterministic(t *testing.T) {
	// Rotating calipers give an axis, not a direction. Without pinning the sign the
	// crop comes out upside down half the time, so the long axis must always point
	// down and the cross-axis must be a fixed quarter turn of it.
	for _, angle := range []float64{-30, 0, 30, 75} {
		img := strip(800, 800, 600, 200, angle)
		rect, _, ok := DetectDocument(img)
		if !ok {
			t.Fatalf("angle %.0f: no detection", angle)
		}
		if rect.U.Y < 0 {
			t.Errorf("angle %.0f: long axis points up (U=%.2f,%.2f)", angle, rect.U.X, rect.U.Y)
		}
		// V must be U turned a quarter, which forbids a mirrored crop.
		wantV := point{X: rect.U.Y, Y: -rect.U.X}
		if math.Abs(rect.V.X-wantV.X) > 1e-9 || math.Abs(rect.V.Y-wantV.Y) > 1e-9 {
			t.Errorf("angle %.0f: cross-axis is not a quarter turn of the long axis", angle)
		}
		// Unit axes, or the affine transform would scale wrongly.
		if math.Abs(math.Hypot(rect.U.X, rect.U.Y)-1) > 1e-9 {
			t.Errorf("angle %.0f: long axis is not a unit vector", angle)
		}
		if math.Abs(rect.U.X*rect.V.X+rect.U.Y*rect.V.Y) > 1e-9 {
			t.Errorf("angle %.0f: axes are not perpendicular", angle)
		}
	}
}

// Detection must decline rather than guess: a bad crop destroys data, while
// declining merely forgoes an improvement.
func TestDetectDocumentDeclinesImplausibleShapes(t *testing.T) {
	cases := []struct {
		name string
		img  image.Image
	}{
		{"nearly the whole frame", strip(400, 400, 398, 396, 0)},
		{"a tiny speck", strip(800, 800, 40, 20, 0)},
		{"square-ish region", strip(800, 800, 400, 390, 0)},
		{"uniform image with no edges", image.NewRGBA(image.Rect(0, 0, 400, 400))},
		{"too small to analyse", strip(20, 20, 10, 4, 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, info, ok := DetectDocument(tc.img); ok {
				t.Errorf("expected no detection, got %+v", info)
			} else if info.Reason == "" {
				t.Error("a declined detection should explain itself")
			}
		})
	}
}

func TestNormalizeCropsToTheDocument(t *testing.T) {
	// A tilted strip in a large frame: exactly the real-photo situation.
	img := strip(2000, 1500, 1100, 380, 18)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 92}); err != nil {
		t.Fatalf("encode: %v", err)
	}

	out, info, err := Normalize(buf.Bytes(), 2048)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if !info.Cropped {
		t.Fatalf("expected a crop, got %+v", info.Detect)
	}
	// The receipt's long axis becomes vertical so its text lines come out
	// horizontal, which is the orientation the model reads.
	if info.Height <= info.Width {
		t.Errorf("expected a portrait crop, got %dx%d", info.Width, info.Height)
	}
	gotAspect := float64(info.Height) / float64(info.Width)
	if math.Abs(gotAspect-1100.0/380.0) > 0.4 {
		t.Errorf("crop aspect = %.2f, want ~%.2f", gotAspect, 1100.0/380.0)
	}
	// Cropping away the background is what buys the resolution.
	if info.Width >= 2000 {
		t.Errorf("crop is no narrower than the frame: %dx%d", info.Width, info.Height)
	}
	if _, err := jpeg.Decode(bytes.NewReader(out)); err != nil {
		t.Errorf("output is not decodable JPEG: %v", err)
	}
}

func TestNormalizeFallsBackWhenNothingIsDetected(t *testing.T) {
	// Speckle has no document-shaped region. Normalize must still return a usable
	// image rather than failing or cropping to nonsense.
	img := image.NewRGBA(image.Rect(0, 0, 900, 700))
	for y := 0; y < 700; y++ {
		for x := 0; x < 900; x++ {
			n := uint8(120 + (x*7+y*13)%9)
			img.Set(x, y, color.RGBA{n, n, n, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode: %v", err)
	}

	_, info, err := Normalize(buf.Bytes(), 2048)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if info.Cropped {
		t.Error("expected no crop on an image with no document in it")
	}
	if info.Width == 0 || info.Height == 0 {
		t.Error("expected the original image back")
	}
}

func TestNormalizeCropRespectsMaxEdge(t *testing.T) {
	img := strip(3000, 2400, 2200, 700, 10)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	for _, maxEdge := range []int{1024, 2048} {
		_, info, err := Normalize(buf.Bytes(), maxEdge)
		if err != nil {
			t.Fatalf("Normalize: %v", err)
		}
		if long := max(info.Width, info.Height); long > maxEdge {
			t.Errorf("maxEdge %d: got %dx%d", maxEdge, info.Width, info.Height)
		}
	}
}

func TestOtsuThresholdSplitsBimodalImage(t *testing.T) {
	g := image.NewGray(image.Rect(0, 0, 100, 100))
	for i := range g.Pix {
		if i%2 == 0 {
			g.Pix[i] = 30
		} else {
			g.Pix[i] = 220
		}
	}
	th := otsuThreshold(g)
	if th < 30 || th > 220 {
		t.Errorf("threshold %d should fall between the two modes", th)
	}
	// A flat image has no meaningful split; it must not panic or wrap.
	flat := image.NewGray(image.Rect(0, 0, 10, 10))
	_ = otsuThreshold(flat)
}

func TestConvexHullAndMinAreaRect(t *testing.T) {
	// A square's hull is its corners, and its minimum rectangle is itself.
	pts := []point{{0, 0}, {10, 0}, {10, 10}, {0, 10}, {5, 5}, {3, 7}}
	hull := convexHull(pts)
	if len(hull) != 4 {
		t.Errorf("hull has %d points, want the 4 corners: %v", len(hull), hull)
	}
	rect := minAreaRect(hull)
	if math.Abs(rect.Long-10) > 0.001 || math.Abs(rect.Short-10) > 0.001 {
		t.Errorf("rect = %.3f x %.3f, want 10 x 10", rect.Long, rect.Short)
	}

	// A rectangle rotated 45 degrees: the minimum rectangle must recover its true
	// size rather than the larger axis-aligned bounding box.
	rot := []point{{0, 0}, {30, 30}, {24, 36}, {-6, 6}}
	r2 := minAreaRect(convexHull(rot))
	wantLong := math.Hypot(30, 30)
	wantShort := math.Hypot(6, 6)
	if math.Abs(r2.Long-wantLong) > 0.5 {
		t.Errorf("long = %.2f, want %.2f", r2.Long, wantLong)
	}
	if math.Abs(r2.Short-wantShort) > 0.5 {
		t.Errorf("short = %.2f, want %.2f", r2.Short, wantShort)
	}

	if got := convexHull([]point{{1, 1}, {2, 2}}); len(got) != 2 {
		t.Errorf("degenerate input should pass through, got %v", got)
	}
}

// A strongly banded image still passes the shape and contrast guards, so
// detection is not proof against every non-document input. This test pins the
// known limitation rather than pretending it does not exist: the backstops are
// reconciliation and the review screen, which is why a wrong crop is recoverable.
func TestDetectDocumentCanBeFooledByBrightBands(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 800, 600))
	for y := 0; y < 600; y++ {
		for x := 0; x < 800; x++ {
			img.Set(x, y, color.RGBA{uint8(x % 256), uint8(y % 256), 128, 255})
		}
	}
	_, info, ok := DetectDocument(img)
	if !ok {
		t.Skip("banded image was rejected; the guards are stricter than documented")
	}
	t.Logf("known limitation: banded image detected as a document (%+v)", info)
}
