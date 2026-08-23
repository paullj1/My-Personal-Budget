package receipt

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"math"

	"golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
)

// Normalization constants, all established by measurement on real receipts.
// See docs/receipt-scan-design.md §3.4 and §3.5.
const (
	// DefaultMaxEdge bounds the long edge.
	//
	// 2048 rather than 1600: a receipt usually occupies only part of the frame, so
	// bounding the long edge at 1600 leaves its print too small to read reliably.
	// Measured on the same photo, 1600x2134 extracted all four items exactly while
	// 1200x1600 collapsed them into one bogus line. Going above 2048 is free
	// accuracy-wise but not latency-wise: 2048 and native 4032 both hit the same
	// vision-token ceiling, so 2048 is the cheapest point that still reads.
	DefaultMaxEdge = 2048

	// The crop is the model's only input, and it has already been through one lossy
	// encode in the browser. Re-encoding it at 85 measurably destroyed legibility on
	// a dim receipt: at 85 the model transcribed 15 of 30 lines, at 95 it read 25.
	// The extra ~100KB costs nothing next to a wrong extraction.
	jpegQuality = 95

	// UncroppedMaxEdge bounds an image we could not crop.
	//
	// The normal bound exists to stop the pixel budget going on background. When
	// detection declines -- a receipt on a bright surface defeats the brightness
	// threshold, since paper and brushed steel merge into one region -- the
	// background is still there, so throwing away resolution as well leaves the
	// print too small to read: a Lowe's receipt extracted nothing at 1536x2048 and
	// read correctly at its native 3024x4032. Vision tokens plateau around 4k
	// regardless, so the larger bound costs prefill time rather than tokens.
	UncroppedMaxEdge = 4096

	// MaxPixels caps the decoded image. A byte-length limit is not enough: a
	// heavily compressed PNG a few hundred KB in size can decode to hundreds of
	// millions of pixels and exhaust memory.
	//
	// This is a memory bound, not a taste one, so it is set from measurement. The
	// pipeline allocates several full-frame buffers -- decode, then RGBA copies at
	// 4 bytes per pixel for the rotate and downscale steps -- so cost scales with
	// pixel count at roughly 13MB per megapixel:
	//
	//	 8MP (browser upload)     82MB allocated
	//	12MP (native iPhone)     162MB
	//	48MP                     955MB
	//	59MP (the old limit)    1086MB, driving process Sys to 1845MB
	//
	// The old 60M limit was chosen to cover a 48MP sensor, which cannot be honoured
	// at native resolution on a 1GB host: it OOM-killed the API twice in production
	// on 2026-08-22, and because the container had no memory limit the kernel fired
	// a global OOM that endangered every other service on the box.
	//
	// 16M is ~215MB worst case. It leaves better than 2x headroom over what the app
	// actually sends, since the browser re-encodes to a 3200px long edge (7.68MP)
	// before upload, and still accepts a native 12MP phone photo whole. A direct API
	// caller posting a 24MP or 48MP original is now refused with a 413 naming this
	// limit, which is the honest answer -- previously it took the host down. Raising
	// it means downscaling right after decode, before the RGBA copies, rather than
	// simply moving this number up.
	MaxPixels = 16_000_000

	// MaxLongEdge caps the source's long edge, and exists because a pixel-count
	// limit alone does not bound memory.
	//
	// Above UncroppedMaxEdge the uncropped path calls downscale, and x/image's
	// Catmull-Rom resampler allocates a separable-pass scratch buffer of
	// dst_width x src_height x 4 channels of float64 -- roughly 32 bytes per source
	// pixel, which at 16MP is over 500MB. Measured peak RSS jumps from 74MB at
	// 4032x3024 to 559MB at 4618x3464 for exactly this reason, and a soft memory
	// limit barely dents it (507MB) because the scratch is live, not garbage.
	//
	// So the cost is not proportional to pixels; it is a cliff at the point the
	// resampler engages. Lowering MaxPixels cannot fix that, since the scratch
	// scales with whatever the limit allows. Refusing to enter the expensive path
	// can, and costs nothing real: 4096 is already the most this pipeline will keep
	// on the long edge, the browser re-encodes to 3200 before upload, and a native
	// 12MP photo is 4032. Only an unusually elongated original -- a panorama, or a
	// direct caller's 8000x2000 -- is refused, and it gets a 413 saying why.
	MaxLongEdge = UncroppedMaxEdge
)

// NormalizeInfo reports what was done, so callers can log or surface it.
type NormalizeInfo struct {
	SrcWidth   int        `json:"src_width"`
	SrcHeight  int        `json:"src_height"`
	Width      int        `json:"width"`
	Height     int        `json:"height"`
	Rotated    bool       `json:"rotated"`
	Downscaled bool       `json:"downscaled"`
	Cropped    bool       `json:"cropped"`
	Format     string     `json:"format"`
	Detect     DetectInfo `json:"detect"`
}

// Normalize prepares a photo for extraction: it honours EXIF orientation so the
// text is upright, and bounds the long edge.
//
// The browser normally does this before upload; this is the backstop for direct
// API callers.
func Normalize(data []byte, maxEdge int) ([]byte, NormalizeInfo, error) {
	if maxEdge <= 0 {
		maxEdge = DefaultMaxEdge
	}
	// Check the declared dimensions before decoding: DecodeConfig reads only the
	// header, so an oversized image is rejected without allocating its pixels.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, NormalizeInfo{}, fmt.Errorf("decode image header: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, NormalizeInfo{}, fmt.Errorf("image has no dimensions")
	}
	if pixels := int64(cfg.Width) * int64(cfg.Height); pixels > MaxPixels {
		return nil, NormalizeInfo{}, fmt.Errorf("%w: %dx%d is %d megapixels, limit is %d",
			ErrImageTooLarge, cfg.Width, cfg.Height, pixels/1_000_000, MaxPixels/1_000_000)
	}
	// Both guards are needed: the pixel count bounds the decode, this bounds the
	// resampler's scratch buffer, and neither implies the other.
	if long := max(cfg.Width, cfg.Height); long > MaxLongEdge {
		return nil, NormalizeInfo{}, fmt.Errorf("%w: %dx%d has a %dpx long edge, limit is %d",
			ErrImageTooLarge, cfg.Width, cfg.Height, long, MaxLongEdge)
	}

	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, NormalizeInfo{}, fmt.Errorf("decode image: %w", err)
	}

	info := NormalizeInfo{
		SrcWidth:  img.Bounds().Dx(),
		SrcHeight: img.Bounds().Dy(),
		Format:    format,
	}

	// Apply EXIF orientation, and nothing more.
	//
	// The goal is upright text, not a particular frame shape. A receipt's lines run
	// across its narrow axis, so forcing the frame to landscape lays the receipt on
	// its side and leaves every line of text rotated 90 degrees -- which measurably
	// degrades extraction. Honouring EXIF alone reproduces what the photographer
	// saw, which is the orientation that reads best.
	turns := 0
	if format == "jpeg" {
		turns = exifTurns(jpegOrientation(data))
	}
	if turns != 0 {
		img = rotateQuarterTurns(img, turns)
		info.Rotated = true
	}
	// Crop to the paper when we can find it. This is where most of the accuracy
	// comes from: the receipt occupies a fraction of the frame, so cropping spends
	// the whole pixel budget on print instead of background, and deskewing makes
	// the text lines axis-aligned.
	if rect, detect, ok := DetectDocument(img); ok {
		if out, err := extractRect(img, rect, maxEdge); err == nil {
			img = out
			info.Cropped = true
			info.Downscaled = true
		}
		info.Detect = detect
	} else {
		info.Detect = detect
	}

	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if !info.Cropped {
		// Nothing was cropped away, so keep the detail instead.
		bound := max(maxEdge, UncroppedMaxEdge)
		if w > bound || h > bound {
			img = downscale(img, bound)
			info.Downscaled = true
		}
	}

	info.Width = img.Bounds().Dx()
	info.Height = img.Bounds().Dy()

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, info, fmt.Errorf("encode image: %w", err)
	}
	return buf.Bytes(), info, nil
}

// jpegOrientation reads EXIF tag 0x0112. The Go standard library does not parse
// EXIF, and both raw SOF markers and ffprobe report pre-rotation dimensions, so
// this is the only way to know which way is up. Returns 1 when absent.
func jpegOrientation(data []byte) int {
	const defaultOrientation = 1
	// Walk JPEG segments looking for APP1/Exif.
	for i := 2; i+4 < len(data); {
		if data[i] != 0xFF {
			i++
			continue
		}
		marker := data[i+1]
		if marker == 0xD8 || marker == 0xD9 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2
			continue
		}
		segLen := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if segLen < 2 || i+2+segLen > len(data) {
			return defaultOrientation
		}
		if marker == 0xE1 {
			seg := data[i+4 : i+2+segLen]
			if o, ok := exifOrientation(seg); ok {
				return o
			}
		}
		i += 2 + segLen
	}
	return defaultOrientation
}

func exifOrientation(seg []byte) (int, bool) {
	if len(seg) < 14 || !bytes.HasPrefix(seg, []byte("Exif\x00\x00")) {
		return 0, false
	}
	tiff := seg[6:]
	var bo binary.ByteOrder
	switch {
	case bytes.HasPrefix(tiff, []byte("MM")):
		bo = binary.BigEndian
	case bytes.HasPrefix(tiff, []byte("II")):
		bo = binary.LittleEndian
	default:
		return 0, false
	}
	if len(tiff) < 8 {
		return 0, false
	}
	offset := int(bo.Uint32(tiff[4:8]))
	if offset+2 > len(tiff) {
		return 0, false
	}
	count := int(bo.Uint16(tiff[offset : offset+2]))
	for n := 0; n < count; n++ {
		entry := offset + 2 + n*12
		if entry+12 > len(tiff) {
			return 0, false
		}
		if bo.Uint16(tiff[entry:entry+2]) == 0x0112 {
			v := int(bo.Uint16(tiff[entry+8 : entry+10]))
			if v >= 1 && v <= 8 {
				return v, true
			}
			return 0, false
		}
	}
	return 0, false
}

// exifTurns maps an EXIF orientation to clockwise quarter turns. Mirrored values
// (2, 4, 5, 7) are vanishingly rare from phone cameras and are reduced to their
// rotation, so text stays readable rather than being left sideways.
func exifTurns(orientation int) int {
	switch orientation {
	case 3, 4:
		return 2
	case 5, 6:
		return 1
	case 7, 8:
		return 3
	default:
		return 0
	}
}

// rotateQuarterTurns rotates clockwise by turns*90 degrees in a single pass.
func rotateQuarterTurns(img image.Image, turns int) image.Image {
	switch ((turns % 4) + 4) % 4 {
	case 1:
		return rotate90(img)
	case 2:
		return rotate180(img)
	case 3:
		return rotate270(img)
	default:
		return img
	}
}

// rotate90 turns the image a quarter turn clockwise.
func rotate90(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dy(), b.Dx()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.Set(b.Max.Y-1-y, x-b.Min.X, src.At(x, y))
		}
	}
	return dst
}

// rotate270 turns the image a quarter turn counter-clockwise, which is how a
// receipt photographed in portrait becomes readable landscape.
func rotate270(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dy(), b.Dx()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.Set(y-b.Min.Y, b.Max.X-1-x, src.At(x, y))
		}
	}
	return dst
}

func rotate180(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.Set(b.Max.X-1-x, b.Max.Y-1-y, src.At(x, y))
		}
	}
	return dst
}

// downscale resamples to fit inside maxEdge using Catmull-Rom.
//
// The filter choice is load-bearing, not cosmetic. Box averaging blurs the
// one-pixel strokes of thermal receipt print at a 2.5x reduction, and the model
// then misreads prices -- measured against the same photo, where a sharper
// resample extracted cleanly and a box filter did not.
func downscale(src image.Image, maxEdge int) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= 0 || sh <= 0 {
		return src
	}
	scale := float64(maxEdge) / float64(max(sw, sh))
	dw := max(1, int(float64(sw)*scale))
	dh := max(1, int(float64(sh)*scale))

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst
}

// ErrImageTooLarge means the image's declared dimensions exceed MaxPixels. It is
// reported from the header alone, before any pixels are allocated.
var ErrImageTooLarge = errors.New("image too large")

// extractRect crops, deskews and scales in a single resample.
//
// One pass rather than three: each separate rotate/crop/scale would resample the
// pixels again and compound the blurring that already costs price-reading
// accuracy on thermal print.
//
// The receipt's long axis becomes the output's vertical axis, so its lines of
// text -- which run across the narrow axis -- come out horizontal. That is the
// orientation the model reads best, and here it is derived from the detected
// geometry rather than guessed from the frame's shape.
func extractRect(src image.Image, rect rotRect, maxEdge int) (image.Image, error) {
	if rect.Long <= 1 || rect.Short <= 1 {
		return nil, fmt.Errorf("degenerate crop rectangle")
	}

	scale := math.Min(1, float64(maxEdge)/rect.Long)
	outW := max(1, int(rect.Short*scale))
	outH := max(1, int(rect.Long*scale))

	// Map source pixels into the output: across-axis (V) to x, along-axis (U) to y.
	sx := float64(outW) / rect.Short
	sy := float64(outH) / rect.Long
	a := sx * rect.V.X
	b := sx * rect.V.Y
	d := sy * rect.U.X
	e := sy * rect.U.Y
	m := f64.Aff3{
		a, b, float64(outW)/2 - (a*rect.Center.X + b*rect.Center.Y),
		d, e, float64(outH)/2 - (d*rect.Center.X + e*rect.Center.Y),
	}

	dst := image.NewRGBA(image.Rect(0, 0, outW, outH))
	draw.CatmullRom.Transform(dst, m, src, src.Bounds(), draw.Src, nil)
	return dst, nil
}
