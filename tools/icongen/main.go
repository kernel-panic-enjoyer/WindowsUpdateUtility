package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

type point struct {
	x float64
	y float64
}

type rgba struct {
	r uint8
	g uint8
	b uint8
	a uint8
}

var iconSizes = []int{16, 24, 32, 48, 64, 128, 256}

func main() {
	root := filepath.Join("internal", "updater", "assets")
	must(os.MkdirAll(root, 0o755))
	base := renderIcon(1024)
	must(writePNG(filepath.Join(root, "app-icon.png"), base))
	must(writeICO(filepath.Join(root, "app.ico"), base, iconSizes))
}

func renderIcon(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	drawRoundedRect(img, 24, 24, float64(size-24), float64(size-24), 210, func(x, y, w float64) rgba {
		top := rgba{22, 41, 61, 255}
		bottom := rgba{5, 14, 24, 255}
		mix := y / w
		return lerp(top, bottom, mix)
	})
	drawGlow(img, point{float64(size) * 0.68, float64(size) * 0.28}, float64(size)*0.5, rgba{47, 160, 255, 130})
	drawGlow(img, point{float64(size) * 0.35, float64(size) * 0.75}, float64(size)*0.45, rgba{50, 231, 201, 110})

	drawPackageTile(img, size, 0.18, 0.62, 0.18, rgba{34, 62, 83, 238}, rgba{74, 183, 255, 255})
	drawPackageTile(img, size, 0.64, 0.62, 0.18, rgba{29, 56, 78, 238}, rgba{64, 227, 198, 255})
	drawPackageTile(img, size, 0.41, 0.70, 0.18, rgba{38, 73, 94, 238}, rgba{255, 190, 84, 255})

	shield := []point{
		{0.50, 0.13},
		{0.74, 0.24},
		{0.70, 0.58},
		{0.50, 0.82},
		{0.30, 0.58},
		{0.26, 0.24},
	}
	scalePoints(shield, float64(size))
	fillPolygon(img, shield, func(x, y float64) rgba {
		return lerp(rgba{80, 178, 255, 255}, rgba{28, 108, 224, 255}, y/float64(size))
	})
	strokePolygon(img, shield, rgba{160, 229, 255, 230}, float64(size)*0.026)

	inner := []point{
		{0.50, 0.22},
		{0.65, 0.29},
		{0.62, 0.53},
		{0.50, 0.68},
		{0.38, 0.53},
		{0.35, 0.29},
	}
	scalePoints(inner, float64(size))
	fillPolygon(img, inner, func(x, y float64) rgba {
		return lerp(rgba{20, 43, 72, 135}, rgba{11, 26, 46, 80}, y/float64(size))
	})

	drawArc(img, point{float64(size) * 0.50, float64(size) * 0.49}, float64(size)*0.22, -28, 226, rgba{225, 253, 255, 255}, float64(size)*0.052)
	drawArrowHead(img, size, rgba{225, 253, 255, 255})
	drawCheck(img, size, rgba{92, 245, 211, 255})
	drawHighlight(img, size)
	clipRoundedRect(img, 24, 24, float64(size-24), float64(size-24), 210)
	return img
}

func drawPackageTile(img *image.RGBA, size int, x, y, s float64, fill rgba, accent rgba) {
	left := float64(size) * x
	top := float64(size) * y
	w := float64(size) * s
	drawRoundedRect(img, left, top, left+w, top+w, w*0.16, func(px, py, width float64) rgba {
		return fill
	})
	strokeRoundedRect(img, left, top, left+w, top+w, w*0.16, accent, w*0.035)
	drawLine(img, point{left + w*0.18, top + w*0.38}, point{left + w*0.82, top + w*0.38}, accent, w*0.045)
	drawLine(img, point{left + w*0.5, top + w*0.08}, point{left + w*0.5, top + w*0.38}, accent, w*0.04)
}

func drawArrowHead(img *image.RGBA, size int, c rgba) {
	points := []point{
		{0.69, 0.39},
		{0.81, 0.43},
		{0.72, 0.52},
	}
	scalePoints(points, float64(size))
	fillPolygon(img, points, func(x, y float64) rgba { return c })
}

func drawCheck(img *image.RGBA, size int, c rgba) {
	drawLine(img, point{float64(size) * 0.39, float64(size) * 0.52}, point{float64(size) * 0.48, float64(size) * 0.61}, c, float64(size)*0.052)
	drawLine(img, point{float64(size) * 0.48, float64(size) * 0.61}, point{float64(size) * 0.64, float64(size) * 0.40}, c, float64(size)*0.052)
}

func drawHighlight(img *image.RGBA, size int) {
	drawLine(img, point{float64(size) * 0.38, float64(size) * 0.22}, point{float64(size) * 0.53, float64(size) * 0.16}, rgba{255, 255, 255, 80}, float64(size)*0.018)
	drawLine(img, point{float64(size) * 0.32, float64(size) * 0.30}, point{float64(size) * 0.31, float64(size) * 0.50}, rgba{255, 255, 255, 52}, float64(size)*0.016)
}

func scalePoints(points []point, scale float64) {
	for i := range points {
		points[i].x *= scale
		points[i].y *= scale
	}
}

func drawRoundedRect(img *image.RGBA, minX, minY, maxX, maxY, radius float64, shader func(x, y, w float64) rgba) {
	for y := int(math.Floor(minY)); y < int(math.Ceil(maxY)); y++ {
		for x := int(math.Floor(minX)); x < int(math.Ceil(maxX)); x++ {
			if roundedRectContains(float64(x)+0.5, float64(y)+0.5, minX, minY, maxX, maxY, radius) {
				setBlend(img, x, y, shader(float64(x), float64(y), maxY-minY))
			}
		}
	}
}

func strokeRoundedRect(img *image.RGBA, minX, minY, maxX, maxY, radius float64, c rgba, width float64) {
	for y := int(math.Floor(minY - width)); y < int(math.Ceil(maxY+width)); y++ {
		for x := int(math.Floor(minX - width)); x < int(math.Ceil(maxX+width)); x++ {
			px := float64(x) + 0.5
			py := float64(y) + 0.5
			if roundedRectContains(px, py, minX-width, minY-width, maxX+width, maxY+width, radius+width) &&
				!roundedRectContains(px, py, minX+width, minY+width, maxX-width, maxY-width, math.Max(0, radius-width)) {
				setBlend(img, x, y, c)
			}
		}
	}
}

func clipRoundedRect(img *image.RGBA, minX, minY, maxX, maxY, radius float64) {
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if !roundedRectContains(float64(x)+0.5, float64(y)+0.5, minX, minY, maxX, maxY, radius) {
				img.SetRGBA(x, y, color.RGBA{})
			}
		}
	}
}

func roundedRectContains(x, y, minX, minY, maxX, maxY, radius float64) bool {
	if x < minX || x > maxX || y < minY || y > maxY {
		return false
	}
	cx := math.Min(math.Max(x, minX+radius), maxX-radius)
	cy := math.Min(math.Max(y, minY+radius), maxY-radius)
	return math.Hypot(x-cx, y-cy) <= radius
}

func fillPolygon(img *image.RGBA, points []point, shader func(x, y float64) rgba) {
	minX, minY, maxX, maxY := bounds(points)
	for y := int(minY); y <= int(maxY); y++ {
		for x := int(minX); x <= int(maxX); x++ {
			if pointInPolygon(point{float64(x) + 0.5, float64(y) + 0.5}, points) {
				setBlend(img, x, y, shader(float64(x), float64(y)))
			}
		}
	}
}

func strokePolygon(img *image.RGBA, points []point, c rgba, width float64) {
	for i := range points {
		drawLine(img, points[i], points[(i+1)%len(points)], c, width)
	}
}

func drawArc(img *image.RGBA, center point, radius float64, startDeg, endDeg float64, c rgba, width float64) {
	steps := int(math.Max(64, radius*1.7))
	prev := point{}
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		deg := startDeg + (endDeg-startDeg)*t
		rad := deg * math.Pi / 180
		p := point{center.x + math.Cos(rad)*radius, center.y + math.Sin(rad)*radius}
		if i > 0 {
			drawLine(img, prev, p, c, width)
		}
		prev = p
	}
}

func drawLine(img *image.RGBA, a, b point, c rgba, width float64) {
	minX := int(math.Floor(math.Min(a.x, b.x) - width))
	maxX := int(math.Ceil(math.Max(a.x, b.x) + width))
	minY := int(math.Floor(math.Min(a.y, b.y) - width))
	maxY := int(math.Ceil(math.Max(a.y, b.y) + width))
	half := width / 2
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			d := distanceToSegment(point{float64(x) + 0.5, float64(y) + 0.5}, a, b)
			if d <= half {
				alpha := 1.0
				if d > half-1 {
					alpha = math.Max(0, half-d)
				}
				cc := c
				cc.a = uint8(float64(cc.a) * alpha)
				setBlend(img, x, y, cc)
			}
		}
	}
}

func drawGlow(img *image.RGBA, center point, radius float64, c rgba) {
	minX := int(math.Floor(center.x - radius))
	maxX := int(math.Ceil(center.x + radius))
	minY := int(math.Floor(center.y - radius))
	maxY := int(math.Ceil(center.y + radius))
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			dist := math.Hypot(float64(x)-center.x, float64(y)-center.y)
			if dist > radius {
				continue
			}
			t := 1 - dist/radius
			cc := c
			cc.a = uint8(float64(c.a) * t * t)
			setBlend(img, x, y, cc)
		}
	}
}

func bounds(points []point) (float64, float64, float64, float64) {
	minX, minY := points[0].x, points[0].y
	maxX, maxY := minX, minY
	for _, p := range points[1:] {
		minX = math.Min(minX, p.x)
		minY = math.Min(minY, p.y)
		maxX = math.Max(maxX, p.x)
		maxY = math.Max(maxY, p.y)
	}
	return minX, minY, maxX, maxY
}

func pointInPolygon(p point, polygon []point) bool {
	inside := false
	j := len(polygon) - 1
	for i := range polygon {
		pi := polygon[i]
		pj := polygon[j]
		if ((pi.y > p.y) != (pj.y > p.y)) &&
			(p.x < (pj.x-pi.x)*(p.y-pi.y)/(pj.y-pi.y)+pi.x) {
			inside = !inside
		}
		j = i
	}
	return inside
}

func distanceToSegment(p, a, b point) float64 {
	dx := b.x - a.x
	dy := b.y - a.y
	if dx == 0 && dy == 0 {
		return math.Hypot(p.x-a.x, p.y-a.y)
	}
	t := ((p.x-a.x)*dx + (p.y-a.y)*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(p.x-(a.x+t*dx), p.y-(a.y+t*dy))
}

func lerp(a, b rgba, t float64) rgba {
	t = math.Max(0, math.Min(1, t))
	return rgba{
		r: uint8(float64(a.r)*(1-t) + float64(b.r)*t),
		g: uint8(float64(a.g)*(1-t) + float64(b.g)*t),
		b: uint8(float64(a.b)*(1-t) + float64(b.b)*t),
		a: uint8(float64(a.a)*(1-t) + float64(b.a)*t),
	}
}

func setBlend(img *image.RGBA, x, y int, c rgba) {
	if !(image.Point{X: x, Y: y}.In(img.Bounds())) || c.a == 0 {
		return
	}
	dst := img.RGBAAt(x, y)
	alpha := float64(c.a) / 255
	inv := 1 - alpha
	outA := alpha + float64(dst.A)/255*inv
	if outA == 0 {
		img.SetRGBA(x, y, color.RGBA{})
		return
	}
	img.SetRGBA(x, y, color.RGBA{
		R: uint8((float64(c.r)*alpha + float64(dst.R)*float64(dst.A)/255*inv) / outA),
		G: uint8((float64(c.g)*alpha + float64(dst.G)*float64(dst.A)/255*inv) / outA),
		B: uint8((float64(c.b)*alpha + float64(dst.B)*float64(dst.A)/255*inv) / outA),
		A: uint8(outA * 255),
	})
}

func resize(src *image.RGBA, size int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	scaleX := float64(src.Bounds().Dx()) / float64(size)
	scaleY := float64(src.Bounds().Dy()) / float64(size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			sx0 := int(float64(x) * scaleX)
			sy0 := int(float64(y) * scaleY)
			sx1 := int(float64(x+1) * scaleX)
			sy1 := int(float64(y+1) * scaleY)
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			if sy1 <= sy0 {
				sy1 = sy0 + 1
			}
			var r, g, b, a uint64
			var n uint64
			for sy := sy0; sy < sy1 && sy < src.Bounds().Dy(); sy++ {
				for sx := sx0; sx < sx1 && sx < src.Bounds().Dx(); sx++ {
					c := src.RGBAAt(sx, sy)
					r += uint64(c.R)
					g += uint64(c.G)
					b += uint64(c.B)
					a += uint64(c.A)
					n++
				}
			}
			if n > 0 {
				dst.SetRGBA(x, y, color.RGBA{uint8(r / n), uint8(g / n), uint8(b / n), uint8(a / n)})
			}
		}
	}
	return dst
}

func writePNG(path string, img image.Image) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return png.Encode(file, img)
}

func writeICO(path string, base *image.RGBA, sizes []int) error {
	type entry struct {
		size int
		data []byte
	}
	entries := make([]entry, 0, len(sizes))
	for _, size := range sizes {
		var buf bytes.Buffer
		must(png.Encode(&buf, resize(base, size)))
		entries = append(entries, entry{size: size, data: buf.Bytes()})
	}

	var out bytes.Buffer
	must(binary.Write(&out, binary.LittleEndian, uint16(0)))
	must(binary.Write(&out, binary.LittleEndian, uint16(1)))
	must(binary.Write(&out, binary.LittleEndian, uint16(len(entries))))
	offset := uint32(6 + len(entries)*16)
	for _, item := range entries {
		width := byte(item.size)
		if item.size == 256 {
			width = 0
		}
		out.WriteByte(width)
		out.WriteByte(width)
		out.WriteByte(0)
		out.WriteByte(0)
		must(binary.Write(&out, binary.LittleEndian, uint16(1)))
		must(binary.Write(&out, binary.LittleEndian, uint16(32)))
		must(binary.Write(&out, binary.LittleEndian, uint32(len(item.data))))
		must(binary.Write(&out, binary.LittleEndian, offset))
		offset += uint32(len(item.data))
	}
	for _, item := range entries {
		out.Write(item.data)
	}
	return os.WriteFile(path, out.Bytes(), 0o644)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
