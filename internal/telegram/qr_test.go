package telegram

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/gotd/td/telegram/auth/qrlogin"
	"rsc.io/qr"
)

func TestRenderQR(t *testing.T) {
	url := qrlogin.NewToken(make([]byte, 32), 1900000000).URL()
	b, err := renderQR(url)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	code, _ := qr.Encode(url, qr.M)
	if want := (code.Size + 8) * 8; img.Bounds().Dx() != want || img.Bounds().Dy() != want {
		t.Fatalf("size %v, want %d", img.Bounds(), want)
	}
	if r, _, _, _ := img.At(0, 0).RGBA(); r == 0 {
		t.Error("quiet zone corner must be white")
	}
	// The top-left finder pattern module is always black; it must fill its whole 8x8 block.
	for _, p := range [][2]int{{32, 32}, {39, 39}} {
		if r, _, _, _ := img.At(p[0], p[1]).RGBA(); r != 0 {
			t.Errorf("pixel %v should be black", p)
		}
	}
	// The module scaling must cover the image, not just a corner: some black pixel in the far half.
	black := false
	for y := img.Bounds().Dy() / 2; y < img.Bounds().Dy() && !black; y++ {
		for x := img.Bounds().Dx() / 2; x < img.Bounds().Dx(); x++ {
			if r, _, _, _ := img.At(x, y).RGBA(); r == 0 {
				black = true
				break
			}
		}
	}
	if !black {
		t.Error("QR is not spread over the whole image")
	}
}
