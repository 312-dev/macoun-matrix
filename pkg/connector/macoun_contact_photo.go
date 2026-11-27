package connector

import (
	"bytes"
	"image"
	_ "image/jpeg" // decoders for samePhoto
	_ "image/png"
	"strings"

	"github.com/lrhodin/corten-matrix/imessage"
)

// Google's CardDAV serves some contact photos in two encodings that alternate
// between requests: the same picture, differing in a few bytes and, decoded,
// by a level or two in one 8x8 block. The ghost avatar ID is a hash of the
// photo bytes, so without this every switch reads as a new photo and re-sets
// the avatar in each of the contact's rooms. keepEquivalentPhotos holds on to
// the cached bytes when the fresh ones are the same picture.

// Limits for treating two decoded photos as the same picture. A re-encode
// differs by a level or two in a handful of pixels; a new photo differs by far
// more than either limit.
const (
	samePhotoMaxMeanDiff  = 1.0 // average per-channel difference, 0-255 scale
	samePhotoMaxPixelDiff = 24  // largest single per-channel difference
)

// keepEquivalentPhotos gives each fresh contact the previous sync's photo
// bytes when its new photo is the same picture. old looks a contact up in the
// previous cache.
func keepEquivalentPhotos(fresh []*imessage.Contact, old func(*imessage.Contact) *imessage.Contact) int {
	kept := 0
	for _, ct := range fresh {
		if len(ct.Avatar) == 0 {
			continue
		}
		prev := old(ct)
		if prev == nil || len(prev.Avatar) == 0 || bytes.Equal(prev.Avatar, ct.Avatar) {
			continue
		}
		if samePhoto(prev.Avatar, ct.Avatar) {
			ct.Avatar = prev.Avatar
			kept++
		}
	}
	return kept
}

// previousContact finds ct in the previous sync's indexes, by phone first and
// then by email, the same order GetContactInfo resolves a handle.
func previousContact(byPhone, byEmail map[string]*imessage.Contact, ct *imessage.Contact) *imessage.Contact {
	for _, phone := range ct.Phones {
		for _, suffix := range phoneSuffixes(phone) {
			if o, ok := byPhone[suffix]; ok {
				return o
			}
		}
	}
	for _, email := range ct.Emails {
		if o, ok := byEmail[strings.ToLower(email)]; ok {
			return o
		}
	}
	return nil
}

// samePhoto reports whether two encoded images show the same picture. Images
// that fail to decode or differ in size are never the same.
func samePhoto(a, b []byte) bool {
	imgA, _, errA := image.Decode(bytes.NewReader(a))
	imgB, _, errB := image.Decode(bytes.NewReader(b))
	if errA != nil || errB != nil {
		return false
	}
	ra, rb := imgA.Bounds(), imgB.Bounds()
	if ra.Dx() != rb.Dx() || ra.Dy() != rb.Dy() || ra.Empty() {
		return false
	}
	var total uint64
	for y := 0; y < ra.Dy(); y++ {
		for x := 0; x < ra.Dx(); x++ {
			ar, ag, ab, _ := imgA.At(ra.Min.X+x, ra.Min.Y+y).RGBA()
			br, bg, bb, _ := imgB.At(rb.Min.X+x, rb.Min.Y+y).RGBA()
			for _, d := range [3]uint32{absDiff(ar, br), absDiff(ag, bg), absDiff(ab, bb)} {
				d >>= 8 // 16-bit color to 0-255
				if d > samePhotoMaxPixelDiff {
					return false
				}
				total += uint64(d)
			}
		}
	}
	mean := float64(total) / float64(ra.Dx()*ra.Dy()*3)
	return mean <= samePhotoMaxMeanDiff
}

func absDiff(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}
