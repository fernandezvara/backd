package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"

	xdraw "golang.org/x/image/draw"
)

// render scales a decoded image for one set of parameters. The orientation is
// applied to the small result, never to the full-size image.
func render(src image.Image, info Info, p Params) (*Result, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	swapped := info.Orientation >= 5 && info.Orientation <= 8
	ow, oh := sw, sh // the image as it is meant to be seen
	if swapped {
		ow, oh = sh, sw
	}
	iw, ih, cw, ch := plan(ow, oh, p)

	oriented := orient(scale(src, iw, ih, swapped), info.Orientation)
	if cw != iw || ch != ih {
		oriented = crop(oriented, cw, ch)
	}

	format := p.Format
	if format == "" {
		format = defaultFormat(info.Format, src)
	}
	var buf bytes.Buffer
	if format == PNG {
		if err := (&png.Encoder{}).Encode(&buf, oriented); err != nil {
			return nil, &Error{ReasonDecodeError, "the version can't be encoded"}
		}
		return &Result{Data: buf.Bytes(), Type: "image/png", Width: cw, Height: ch}, nil
	}
	q := p.Quality
	if q == 0 {
		q = DefaultQuality
	}
	if err := jpeg.Encode(&buf, flatten(oriented), &jpeg.Options{Quality: q}); err != nil {
		return nil, &Error{ReasonDecodeError, "the version can't be encoded"}
	}
	return &Result{Data: buf.Bytes(), Type: "image/jpeg", Width: cw, Height: ch}, nil
}

// defaultFormat keeps a source's family: JPEG stays JPEG, PNG and GIF become PNG,
// WebP becomes JPEG unless it has transparency.
func defaultFormat(source string, src image.Image) Format {
	switch source {
	case "png", "gif":
		return PNG
	case "webp":
		if o, ok := src.(interface{ Opaque() bool }); ok && !o.Opaque() {
			return PNG
		}
	}
	return JPEG
}

// plan returns the size the image is scaled to (iw × ih) and the size of the
// result (cw × ch, smaller than iw × ih only for Cover, which crops the excess),
// for an image of w × h as it is meant to be seen.
func plan(w, h int, p Params) (iw, ih, cw, ch int) {
	mw, mh := p.MaxWidth, p.MaxHeight
	fit := p.Fit
	if fit == "" {
		fit = Contain
	}
	if !p.Upscale { // a box larger than the image is the image's own size
		if mw > w {
			mw = w
		}
		if mh > h {
			mh = h
		}
	}
	atLeastOne := func(n int) int {
		if n < 1 {
			return 1
		}
		return n
	}
	switch {
	case fit == Stretch:
		iw, ih = w, h
		if mw > 0 {
			iw = mw
		}
		if mh > 0 {
			ih = mh
		}
		return iw, ih, iw, ih
	case fit == Cover && mw > 0 && mh > 0:
		s := math.Max(float64(mw)/float64(w), float64(mh)/float64(h))
		iw = max(mw, atLeastOne(int(math.Ceil(float64(w)*s))))
		ih = max(mh, atLeastOne(int(math.Ceil(float64(h)*s))))
		return iw, ih, mw, mh
	default: // Contain, and Cover with one dimension
		s := math.Inf(1)
		if mw > 0 {
			s = math.Min(s, float64(mw)/float64(w))
		}
		if mh > 0 {
			s = math.Min(s, float64(mh)/float64(h))
		}
		if !p.Upscale && s > 1 {
			s = 1
		}
		iw = atLeastOne(int(math.Round(float64(w) * s)))
		ih = atLeastOne(int(math.Round(float64(h) * s)))
		return iw, ih, iw, ih
	}
}

// scale resizes src to the size the viewer sees (w × h); when the orientation
// turns the image a quarter, src is scaled to h × w so that turning it gives w × h.
func scale(src image.Image, w, h int, swapped bool) *image.NRGBA {
	tw, th := w, h
	if swapped {
		tw, th = h, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, tw, th))
	if tw == src.Bounds().Dx() && th == src.Bounds().Dy() {
		draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
		return dst
	}
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Src, nil)
	return dst
}

// orient applies an EXIF orientation (1 to 8) so the pixels are upright.
func orient(img *image.NRGBA, o int) *image.NRGBA {
	if o < 2 || o > 8 {
		return img
	}
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	nw, nh := w, h
	if o >= 5 {
		nw, nh = h, w
	}
	out := image.NewNRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2: // mirrored horizontally
				dx, dy = w-1-x, y
			case 3: // rotated 180°
				dx, dy = w-1-x, h-1-y
			case 4: // mirrored vertically
				dx, dy = x, h-1-y
			case 5: // transposed
				dx, dy = y, x
			case 6: // rotated 90° clockwise
				dx, dy = h-1-y, x
			case 7: // transversed
				dx, dy = h-1-y, w-1-x
			case 8: // rotated 90° counter-clockwise
				dx, dy = y, w-1-x
			}
			si := img.PixOffset(x, y)
			di := out.PixOffset(dx, dy)
			copy(out.Pix[di:di+4], img.Pix[si:si+4])
		}
	}
	return out
}

// crop keeps the centered w × h of img.
func crop(img *image.NRGBA, w, h int) *image.NRGBA {
	b := img.Bounds()
	x0 := b.Min.X + (b.Dx()-w)/2
	y0 := b.Min.Y + (b.Dy()-h)/2
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	draw.Draw(out, out.Bounds(), img, image.Pt(x0, y0), draw.Src)
	return out
}

// flatten puts an image with transparency on white, which JPEG can't hold.
func flatten(img *image.NRGBA) image.Image {
	if img.Opaque() {
		return img
	}
	out := image.NewRGBA(img.Bounds())
	draw.Draw(out, out.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(out, out.Bounds(), img, img.Bounds().Min, draw.Over)
	return out
}
