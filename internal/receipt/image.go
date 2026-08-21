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

	jpegQuality = 85
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
	if !info.Cropped && (w > maxEdge || h > maxEdge) {
		img = downscale(img, maxEdge)
		info.Downscaled = true
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

// ErrImageTooLarge is returned before decoding, so an oversized upload cannot
// force a large allocation.
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
