package telegram

import (
	"testing"

	"github.com/gotd/td/tg"
)

func TestLocationFrom(t *testing.T) {
	photo := &tg.MessageMediaPhoto{Photo: &tg.Photo{ID: 5, AccessHash: 1, FileReference: []byte{9}, Sizes: []tg.PhotoSizeClass{
		&tg.PhotoSize{Type: "m", Size: 100},
		&tg.PhotoSizeProgressive{Type: "y", Sizes: []int{500, 2000}},
		&tg.PhotoSizeProgressive{Type: "x", Sizes: []int{300}},
	}}}
	loc, ok := locationFrom(photo, 5)
	pl, isPhoto := loc.(*tg.InputPhotoFileLocation)
	if !ok || !isPhoto || pl.ThumbSize != "y" || pl.FileReference[0] != 9 {
		t.Fatalf("photo: %+v ok=%v", loc, ok)
	}
	if got := largestSize(photo.Photo.(*tg.Photo).Sizes); got != 2000 {
		t.Errorf("largestSize = %d, must match the chosen type's size", got)
	}
	if _, ok := locationFrom(photo, 6); ok {
		t.Error("different media id must not match")
	}

	doc := &tg.MessageMediaDocument{Document: &tg.Document{ID: 7, AccessHash: 2, FileReference: []byte{1}}}
	if loc, ok := locationFrom(doc, 7); !ok || loc.(*tg.InputDocumentFileLocation).ID != 7 {
		t.Errorf("document: %+v ok=%v", loc, ok)
	}
}
