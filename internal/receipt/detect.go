package receipt

import (
	"image"
	"math"
	"sort"
)

// Deterministic document detection.
//
// A phone photo of a receipt is mostly background: on the reference photo the
// receipt covers well under half the frame, so bounding the long edge spends most
// of the pixel budget on a car seat and leaves the print too small to read.
// Cropping to the paper and deskewing it multiplies the effective resolution of
// the text at the same token cost, and yields the receipt's true long axis so
// orientation stops being a guess about frame shape.
//
// The approach is plain image processing, no model: Otsu threshold to separate
// bright paper from a darker background, largest connected component, convex
// hull, then a minimum-area rectangle via rotating calipers.
//
// It is deliberately fail-safe. Every stage has a plausibility guard, and any
// failure returns "not detected" so the caller falls back to the whole frame. A
// wrong crop loses data permanently; declining to crop only forgoes an
// improvement.

const (
	// Analysis runs on a small copy: detection needs shape, not detail.
	detectWorkingEdge = 640

	// The paper must be a real but not total part of the frame. Below this it is
	// probably a highlight; above it, there is no background to crop away.
	minAreaFraction = 0.04
	maxAreaFraction = 0.97

	// Receipts are long strips. Anything squarer is more likely a table top or a
	// window, and anything thinner is probably a glare streak.
	minRectAspect = 1.15
	maxRectAspect = 25.0

	// The component must actually fill its own bounding rectangle, or it is some
	// irregular bright region rather than a sheet of paper.
	minRectFill = 0.55

	// A small margin keeps the outermost printed characters off the crop edge.
	cropPadFraction = 0.012

	// The candidate must be markedly brighter than its surroundings. Without this,
	// any smoothly textured image offers up a bright band that clears the shape
	// guards -- a window, a laptop screen, a gradient -- and gets cropped as though
	// it were paper. Paper on a darker surface separates far more strongly than 24
	// levels, so this rejects texture without rejecting real receipts.
	minContrast = 24.0

	// A rectangle covering essentially the whole frame leaves no background to
	// compare against, so its contrast is undefined rather than low. Measured on a
	// Lowe's receipt shot on brushed steel: the bright component ran to every edge,
	// rectContrast sampled zero outside pixels and dutifully returned 0, which the
	// contrast guard then reported as "not bright enough". Naming the real problem
	// keeps that from being diagnosed as a lighting fault.
	maxRectCoverage = 0.98

	// Second pass. Paper is locally smooth; brushed metal, foliage, woodgrain and
	// fabric are not. On the steel-countertop photo the paper and the counter were
	// within ~16 grey levels of each other -- Otsu merged them into one component
	// that filled the frame -- but their local variance differed sharply, and
	// requiring smoothness recovered the receipt's true outline.
	maxSmoothStdDev = 10.0
	smoothRadius    = 2

	// The second pass may accept less separation because smoothness has already
	// established the candidate is paper-like, doing the work contrast does in the
	// first pass. It is not a blanket loosening: this runs only after the first
	// pass has declined, where the alternative is not cropping at all.
	minContrastSmooth = 12.0
)

// DetectInfo reports what detection concluded, so the result is explainable and
// a bad crop is diagnosable from the API response alone.
type DetectInfo struct {
	Detected     bool    `json:"detected"`
	AreaFraction float64 `json:"area_fraction,omitempty"`
	Aspect       float64 `json:"aspect,omitempty"`
	AngleDegrees float64 `json:"angle_degrees,omitempty"`
	Fill         float64 `json:"fill,omitempty"`
	Contrast     float64 `json:"contrast,omitempty"`
	// RectCoverage is the candidate rectangle's share of the frame. A value near
	// 1 means there was no background left to measure contrast against.
	RectCoverage float64 `json:"rect_coverage,omitempty"`
	// Smooth reports that the crop came from the texture-based second pass, so a
	// surprising result can be traced to the pass that produced it.
	Smooth bool   `json:"smooth,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type point struct{ X, Y float64 }

// rotRect is a rectangle at an arbitrary angle: a centre, two perpendicular unit
// axes, and the extent along each.
type rotRect struct {
	Center point
	U      point // unit vector along Long
	V      point // unit vector along Short, perpendicular to U
	Long   float64
	Short  float64
}

// DetectDocument locates the receipt in an image and returns its oriented
// rectangle. ok is false whenever the result should not be trusted.
func DetectDocument(img image.Image) (rotRect, DetectInfo, bool) {
	b := img.Bounds()
	if b.Dx() < 32 || b.Dy() < 32 {
		return rotRect{}, DetectInfo{Reason: "image too small to analyse"}, false
	}

	gray, scale := grayDownscale(img, detectWorkingEdge)
	gb := gray.Bounds()
	total := gb.Dx() * gb.Dy()

	threshold := otsuThreshold(gray)
	bright := make([]bool, total)
	for i, v := range gray.Pix {
		bright[i] = v > threshold
	}

	// First pass: brightness alone, which is what a receipt on a darker surface
	// needs and nothing more.
	rect, info, ok := evaluateMask(gray, bright, minContrast)
	if ok {
		return finalizeRect(rect, info, scale, b)
	}

	// Second pass: brightness and smoothness together. Only reached when the
	// first pass declined, so the fallback it replaces is the uncropped frame.
	sd := localStdDev(gray, smoothRadius)
	smooth := make([]bool, total)
	for i := range bright {
		smooth[i] = bright[i] && sd[i] < maxSmoothStdDev
	}
	if rect2, info2, ok2 := evaluateMask(gray, smooth, minContrastSmooth); ok2 {
		info2.Smooth = true
		return finalizeRect(rect2, info2, scale, b)
	}

	// Report the first pass's reason: it describes the primary path, and the
	// second pass only ever runs after it has already failed.
	return rotRect{}, info, false
}

// evaluateMask turns one binary mask into a candidate rectangle and applies every
// plausibility guard. Splitting it out lets the two passes share exactly the same
// shape reasoning, so they cannot drift apart.
func evaluateMask(gray *image.Gray, mask []bool, contrastFloor float64) (rotRect, DetectInfo, bool) {
	gb := gray.Bounds()
	total := gb.Dx() * gb.Dy()

	pts, area := largestComponent(mask, gb.Dx(), gb.Dy())
	if len(pts) < 16 {
		return rotRect{}, DetectInfo{Reason: "no bright region found"}, false
	}

	info := DetectInfo{AreaFraction: float64(area) / float64(total)}
	if info.AreaFraction < minAreaFraction {
		info.Reason = "bright region too small to be the receipt"
		return rotRect{}, info, false
	}
	if info.AreaFraction > maxAreaFraction {
		info.Reason = "bright region fills the frame; nothing to crop"
		return rotRect{}, info, false
	}

	hull := convexHull(pts)
	if len(hull) < 3 {
		info.Reason = "degenerate outline"
		return rotRect{}, info, false
	}
	rect := minAreaRect(hull)
	if rect.Long <= 0 || rect.Short <= 0 {
		info.Reason = "degenerate rectangle"
		return rotRect{}, info, false
	}

	info.Aspect = rect.Long / rect.Short
	info.Fill = float64(area) / (rect.Long * rect.Short)
	info.RectCoverage = (rect.Long * rect.Short) / float64(total)
	info.AngleDegrees = math.Atan2(rect.U.Y, rect.U.X) * 180 / math.Pi

	if info.Aspect < minRectAspect || info.Aspect > maxRectAspect {
		info.Reason = "shape does not look like a receipt"
		return rotRect{}, info, false
	}
	if info.Fill < minRectFill {
		info.Reason = "bright region is not rectangular"
		return rotRect{}, info, false
	}
	// Checked before contrast: with no background left, contrast is undefined,
	// and reporting it as "not bright enough" sends you looking at the lighting.
	if info.RectCoverage > maxRectCoverage {
		info.Reason = "candidate rectangle fills the frame; nothing to crop"
		return rotRect{}, info, false
	}

	info.Contrast = rectContrast(gray, rect)
	if info.Contrast < contrastFloor {
		info.Reason = "candidate is not bright enough against its surroundings"
		return rotRect{}, info, false
	}
	return rect, info, true
}

// finalizeRect maps a rectangle from analysis coordinates back to the original
// image, with a little breathing room so the outermost characters are not clipped.
func finalizeRect(rect rotRect, info DetectInfo, scale float64, b image.Rectangle) (rotRect, DetectInfo, bool) {
	inv := 1 / scale
	rect.Center = point{
		X: rect.Center.X*inv + float64(b.Min.X),
		Y: rect.Center.Y*inv + float64(b.Min.Y),
	}
	rect.Long = rect.Long * inv * (1 + cropPadFraction*2)
	rect.Short = rect.Short * inv * (1 + cropPadFraction*2)

	info.Detected = true
	return rect, info, true
}

// localStdDev measures texture in a small window around each pixel. Paper reads
// low even where it is printed; a brushed or woven surface reads high.
//
// Computed with a summed-area table so the cost does not grow with the window:
// the naive form is O(r^2) per pixel and this runs on every scan.
func localStdDev(g *image.Gray, radius int) []float64 {
	gb := g.Bounds()
	w, h := gb.Dx(), gb.Dy()

	// Integral images of v and v^2, offset by one row/column so the sum over any
	// rectangle is four lookups.
	sum := make([]float64, (w+1)*(h+1))
	sumSq := make([]float64, (w+1)*(h+1))
	for y := 0; y < h; y++ {
		var rowSum, rowSumSq float64
		for x := 0; x < w; x++ {
			v := float64(g.Pix[y*g.Stride+x])
			rowSum += v
			rowSumSq += v * v
			sum[(y+1)*(w+1)+x+1] = sum[y*(w+1)+x+1] + rowSum
			sumSq[(y+1)*(w+1)+x+1] = sumSq[y*(w+1)+x+1] + rowSumSq
		}
	}
	boxSum := func(t []float64, x0, y0, x1, y1 int) float64 {
		return t[y1*(w+1)+x1] - t[y0*(w+1)+x1] - t[y1*(w+1)+x0] + t[y0*(w+1)+x0]
	}

	out := make([]float64, w*h)
	for y := 0; y < h; y++ {
		y0 := max(0, y-radius)
		y1 := min(h, y+radius+1)
		for x := 0; x < w; x++ {
			x0 := max(0, x-radius)
			x1 := min(w, x+radius+1)
			n := float64((x1 - x0) * (y1 - y0))
			mean := boxSum(sum, x0, y0, x1, y1) / n
			variance := boxSum(sumSq, x0, y0, x1, y1)/n - mean*mean
			out[y*w+x] = math.Sqrt(math.Max(0, variance))
		}
	}
	return out
}

// grayDownscale produces a small 8-bit copy for analysis and the scale factor
// that maps analysis coordinates back to the original.
func grayDownscale(img image.Image, maxEdge int) (*image.Gray, float64) {
	b := img.Bounds()
	scale := math.Min(1, float64(maxEdge)/float64(max(b.Dx(), b.Dy())))
	w := max(1, int(float64(b.Dx())*scale))
	h := max(1, int(float64(b.Dy())*scale))

	dst := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		sy := b.Min.Y + int(float64(y)/scale)
		if sy >= b.Max.Y {
			sy = b.Max.Y - 1
		}
		for x := 0; x < w; x++ {
			sx := b.Min.X + int(float64(x)/scale)
			if sx >= b.Max.X {
				sx = b.Max.X - 1
			}
			r, g, bl, _ := img.At(sx, sy).RGBA()
			// Rec. 601 luma, which tracks perceived paper brightness well.
			lum := (299*uint32(r>>8) + 587*uint32(g>>8) + 114*uint32(bl>>8)) / 1000
			dst.Pix[y*dst.Stride+x] = uint8(lum)
		}
	}
	return dst, scale
}

// otsuThreshold finds the intensity that best separates the histogram into two
// classes. Paper against a darker surface is close to the ideal case for it.
func otsuThreshold(g *image.Gray) uint8 {
	var hist [256]int
	for _, v := range g.Pix {
		hist[v]++
	}
	total := len(g.Pix)
	if total == 0 {
		return 128
	}

	var sum float64
	for i, c := range hist {
		sum += float64(i) * float64(c)
	}

	var (
		sumB, wB   float64
		best       float64
		bestThresh uint8 = 128
	)
	for t := 0; t < 256; t++ {
		wB += float64(hist[t])
		if wB == 0 {
			continue
		}
		wF := float64(total) - wB
		if wF == 0 {
			break
		}
		sumB += float64(t) * float64(hist[t])
		mB := sumB / wB
		mF := (sum - sumB) / wF
		between := wB * wF * (mB - mF) * (mB - mF)
		if between > best {
			best, bestThresh = between, uint8(t)
		}
	}
	return bestThresh
}

// largestComponent returns the boundary points of the biggest 4-connected run of
// set pixels, plus its area. Only boundary points are kept: the hull needs the
// outline, and holding every interior pixel of a 12MP photo is wasteful.
func largestComponent(mask []bool, w, h int) ([]point, int) {
	labels := make([]int32, len(mask))
	var (
		bestPts  []point
		bestArea int
		queue    []int32
		label    int32
	)

	for start := range mask {
		if !mask[start] || labels[start] != 0 {
			continue
		}
		label++
		labels[start] = label
		queue = append(queue[:0], int32(start))
		area := 0
		var pts []point

		for qi := 0; qi < len(queue); qi++ {
			idx := int(queue[qi])
			area++
			x, y := idx%w, idx/w

			edge := x == 0 || y == 0 || x == w-1 || y == h-1
			for _, n := range [4]int{idx - 1, idx + 1, idx - w, idx + w} {
				// Guard the row wrap that makes -1/+1 neighbours unsafe.
				if n < 0 || n >= len(mask) {
					edge = true
					continue
				}
				if (n == idx-1 && x == 0) || (n == idx+1 && x == w-1) {
					edge = true
					continue
				}
				if !mask[n] {
					edge = true
					continue
				}
				if labels[n] == 0 {
					labels[n] = label
					queue = append(queue, int32(n))
				}
			}
			if edge {
				pts = append(pts, point{X: float64(x), Y: float64(y)})
			}
		}

		if area > bestArea {
			bestArea, bestPts = area, pts
		}
	}
	return bestPts, bestArea
}

// convexHull returns the hull in counter-clockwise order (Andrew's monotone
// chain).
func convexHull(pts []point) []point {
	if len(pts) < 3 {
		return pts
	}
	sorted := make([]point, len(pts))
	copy(sorted, pts)
	sortPoints(sorted)

	cross := func(o, a, b point) float64 {
		return (a.X-o.X)*(b.Y-o.Y) - (a.Y-o.Y)*(b.X-o.X)
	}
	build := func(in []point) []point {
		var out []point
		for _, p := range in {
			for len(out) >= 2 && cross(out[len(out)-2], out[len(out)-1], p) <= 0 {
				out = out[:len(out)-1]
			}
			out = append(out, p)
		}
		return out
	}

	lower := build(sorted)
	rev := make([]point, len(sorted))
	for i, p := range sorted {
		rev[len(sorted)-1-i] = p
	}
	upper := build(rev)
	return append(lower[:len(lower)-1], upper[:len(upper)-1]...)
}

// sortPoints orders boundary points for the monotone chain.
//
// This must not be a quadratic sort. A textured surface -- blinds, a grille, a
// railing -- can alias into one connected component with hundreds of thousands of
// boundary points, and the shape guards only reject it *after* the hull is built.
// An insertion sort here cost 7.5s of CPU per request on such an image, which is
// trivially repeatable by any caller.
func sortPoints(p []point) {
	sort.Slice(p, func(i, j int) bool {
		if p[i].X != p[j].X {
			return p[i].X < p[j].X
		}
		return p[i].Y < p[j].Y
	})
}

// minAreaRect finds the smallest enclosing rectangle by rotating calipers: the
// optimum always has one side flush with a hull edge.
func minAreaRect(hull []point) rotRect {
	best := rotRect{}
	bestArea := math.Inf(1)

	for i := range hull {
		a := hull[i]
		b := hull[(i+1)%len(hull)]
		ex, ey := b.X-a.X, b.Y-a.Y
		length := math.Hypot(ex, ey)
		if length < 1e-9 {
			continue
		}
		ux, uy := ex/length, ey/length
		vx, vy := -uy, ux

		minU, maxU := math.Inf(1), math.Inf(-1)
		minV, maxV := math.Inf(1), math.Inf(-1)
		for _, p := range hull {
			du := p.X*ux + p.Y*uy
			dv := p.X*vx + p.Y*vy
			minU, maxU = math.Min(minU, du), math.Max(maxU, du)
			minV, maxV = math.Min(minV, dv), math.Max(maxV, dv)
		}
		w, h := maxU-minU, maxV-minV
		area := w * h
		if area >= bestArea {
			continue
		}

		midU, midV := (minU+maxU)/2, (minV+maxV)/2
		bestArea = area
		center := point{X: midU*ux + midV*vx, Y: midU*uy + midV*vy}
		// Name the axes so Long is always the longer side.
		if w >= h {
			best = rotRect{Center: center, U: point{X: ux, Y: uy}, Long: w, Short: h}
		} else {
			best = rotRect{Center: center, U: point{X: vx, Y: vy}, Long: h, Short: w}
		}
		best = orientRect(best)
	}
	return best
}

// orientRect fixes the 180-degree ambiguity that rotating calipers leaves behind.
//
// Calipers yield an axis, not a direction, so the long axis is equally likely to
// point up or down and the crop comes out upside down half the time. Resolving it
// from image content (ascender/descender asymmetry, say) would be fragile and
// language-specific. Instead inherit the photograph's own sense of down: people
// frame a receipt with its top toward the top of the frame, so pointing the long
// axis downward reproduces how they held it.
//
// The cross-axis is then a fixed quarter turn of the long axis rather than an
// independent choice, which keeps the handedness consistent and makes a mirrored
// crop impossible.
func orientRect(r rotRect) rotRect {
	if r.U.Y < 0 || (r.U.Y == 0 && r.U.X < 0) {
		r.U = point{X: -r.U.X, Y: -r.U.Y}
	}
	r.V = point{X: r.U.Y, Y: -r.U.X}
	return r
}

// rectContrast measures how much brighter the candidate is than everything around
// it, which is what separates a sheet of paper from mere texture.
func rectContrast(g *image.Gray, rect rotRect) float64 {
	b := g.Bounds()
	var inSum, outSum float64
	var inN, outN int

	halfLong, halfShort := rect.Long/2, rect.Short/2
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dx := float64(x) - rect.Center.X
			dy := float64(y) - rect.Center.Y
			along := math.Abs(dx*rect.U.X + dy*rect.U.Y)
			across := math.Abs(dx*rect.V.X + dy*rect.V.Y)
			v := float64(g.Pix[(y-b.Min.Y)*g.Stride+(x-b.Min.X)])
			if along <= halfLong && across <= halfShort {
				inSum += v
				inN++
			} else {
				outSum += v
				outN++
			}
		}
	}
	if inN == 0 || outN == 0 {
		return 0
	}
	return inSum/float64(inN) - outSum/float64(outN)
}
