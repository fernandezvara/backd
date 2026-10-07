package imaging

import "fmt"

// Fit says how an image is placed in the box of max_width × max_height.
type Fit string

const (
	// Contain fits the image inside the box, keeping its proportions. The default.
	Contain Fit = "contain"
	// Cover fills the box, keeping the proportions and cropping the excess,
	// centered. Needs both dimensions; with one it behaves as Contain.
	Cover Fit = "cover"
	// Stretch fills the box, ignoring the proportions. A missing dimension stays as it is.
	Stretch Fit = "stretch"
)

// Format is an output format.
type Format string

const (
	JPEG Format = "jpeg"
	PNG  Format = "png"
)

// DefaultQuality is the JPEG quality when none is given.
const DefaultQuality = 82

// Params describe one version, with the names of collection.yaml.
type Params struct {
	MaxWidth  int    // box width in pixels; zero is unbounded
	MaxHeight int    // box height in pixels; zero is unbounded
	Fit       Fit    // default Contain
	Quality   int    // 1 to 100, for JPEG; default DefaultQuality
	Format    Format // default: the source's family (jpeg stays jpeg, png and gif become png, webp becomes jpeg unless it has transparency)
	Upscale   bool   // enlarge images smaller than the box; default no
}

// Validate reports a parameter that can't be used, as a ReasonInvalidParameters error.
func (p Params) Validate() error {
	bad := func(format string, a ...any) error {
		return &Error{ReasonInvalidParameters, fmt.Sprintf(format, a...)}
	}
	if p.MaxWidth < 0 || p.MaxHeight < 0 {
		return bad("max_width and max_height can't be negative")
	}
	if p.MaxWidth == 0 && p.MaxHeight == 0 {
		return bad("at least one of max_width and max_height is required")
	}
	switch p.Fit {
	case "", Contain, Cover, Stretch:
	default:
		return bad("fit %q is not contain, cover or stretch", p.Fit)
	}
	if p.Quality < 0 || p.Quality > 100 {
		return bad("quality %d is not between 1 and 100", p.Quality)
	}
	switch p.Format {
	case "", JPEG, PNG:
	default:
		return bad("format %q is not jpeg or png", p.Format)
	}
	return nil
}

// Result is one finished version.
type Result struct {
	Data   []byte
	Type   string // image/jpeg or image/png
	Width  int
	Height int
}
