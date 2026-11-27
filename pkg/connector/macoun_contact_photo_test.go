package connector

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/lrhodin/corten-matrix/imessage"
)

// photoJPEG encodes a 64x64 gradient. nudge adds that much to the red channel
// of one 8x8 block in the corner, which is how Google's two encodings of the
// same contact photo differ after decoding.
func photoJPEG(t *testing.T, base uint8, nudge uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{R: base + uint8(x), G: uint8(y * 3), B: 100, A: 255})
		}
	}
	for y := 56; y < 64; y++ {
		for x := 56; x < 64; x++ {
			c := img.RGBAAt(x, y)
			c.R += nudge
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func syncPhoto(c *externalCardDAVClient, photo []byte) {
	c.storeContacts([]*imessage.Contact{{
		FirstName: "Sam",
		Phones:    []string{"+15555550123"},
		Avatar:    photo,
	}})
}

func cachedPhoto(t *testing.T, c *externalCardDAVClient) []byte {
	t.Helper()
	ct, _ := c.GetContactInfo("+15555550123")
	if ct == nil {
		t.Fatal("contact not found")
	}
	return ct.Avatar
}

func TestContactPhotoReencodeKeepsCachedBytes(t *testing.T) {
	first := photoJPEG(t, 10, 0)
	reencoded := photoJPEG(t, 10, 2)
	if bytes.Equal(first, reencoded) {
		t.Fatal("test photos must differ in bytes")
	}
	c := &externalCardDAVClient{}
	syncPhoto(c, first)
	syncPhoto(c, reencoded)
	if !bytes.Equal(cachedPhoto(t, c), first) {
		t.Fatal("a re-encoded copy of the same photo replaced the cached bytes")
	}
}

func TestContactPhotoRealChangeReplacesCachedBytes(t *testing.T) {
	first := photoJPEG(t, 10, 0)
	changed := photoJPEG(t, 120, 0)
	c := &externalCardDAVClient{}
	syncPhoto(c, first)
	syncPhoto(c, changed)
	if !bytes.Equal(cachedPhoto(t, c), changed) {
		t.Fatal("a different photo did not replace the cached bytes")
	}
}

func TestContactPhotoFirstSyncAndUndecodable(t *testing.T) {
	c := &externalCardDAVClient{}
	syncPhoto(c, nil)
	first := photoJPEG(t, 10, 0)
	syncPhoto(c, first)
	if !bytes.Equal(cachedPhoto(t, c), first) {
		t.Fatal("a photo added to a contact without one was not stored")
	}
	junk := []byte("not an image")
	syncPhoto(c, junk)
	if !bytes.Equal(cachedPhoto(t, c), junk) {
		t.Fatal("bytes that cannot be decoded must replace the cache, not be compared")
	}
}
