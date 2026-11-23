package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// ChunkSize is the upload.getFile request size. Resume offsets must be multiples of it.
const ChunkSize = 1 << 20

// MediaRef identifies a file by where it was posted. The file location itself is resolved just
// before downloading, because a stored file_reference expires.
type MediaRef struct {
	Channel   Channel
	OwnerKind string // "post" | "comment"
	OwnerID   int64  // message id (post id in the channel, or comment id in the discussion group)
	PostID    int64  // the post (equals OwnerID for posts)
	TGMediaID int64
}

// Fetch writes the file to w starting at byte offset (a multiple of ChunkSize).
func (s Source) Fetch(ctx context.Context, ref MediaRef, offset int64, w io.Writer) error {
	loc, err := s.locate(ctx, ref)
	if err != nil {
		return err
	}
	expiredRetries := 2
	for {
		res, err := s.API.UploadGetFile(ctx, &tg.UploadGetFileRequest{Location: loc, Offset: offset, Limit: ChunkSize})
		if tgerr.Is(err, "FILE_REFERENCE_EXPIRED") && expiredRetries > 0 { // long download outlived the reference
			expiredRetries--
			if loc, err = s.locate(ctx, ref); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("get file at %d: %w", offset, err)
		}
		f, ok := res.(*tg.UploadFile)
		if !ok {
			return fmt.Errorf("unsupported file response %T (CDN redirect)", res)
		}
		if _, err := w.Write(f.Bytes); err != nil {
			return err
		}
		offset += int64(len(f.Bytes))
		if len(f.Bytes) < ChunkSize { // a short (or empty) chunk is the end of the file
			return nil
		}
	}
}

// locate re-reads the message and builds a fresh file location for the media.
func (s Source) locate(ctx context.Context, ref MediaRef) (tg.InputFileLocationClass, error) {
	peer := &tg.InputPeerChannel{ChannelID: ref.Channel.ID, AccessHash: ref.Channel.AccessHash}
	var msgs []tg.MessageClass
	switch ref.OwnerKind {
	case "post":
		res, err := s.API.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: ref.Channel.ID, AccessHash: ref.Channel.AccessHash},
			ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: int(ref.OwnerID)}},
		})
		if err != nil {
			return nil, fmt.Errorf("locate post %d: %w", ref.OwnerID, err)
		}
		if mod, ok := res.AsModified(); ok {
			msgs = mod.GetMessages()
		}
	case "comment":
		// offset_id is exclusive, so id+1 with limit 1 returns exactly the comment.
		res, err := s.API.MessagesGetReplies(ctx, &tg.MessagesGetRepliesRequest{
			Peer: peer, MsgID: int(ref.PostID), OffsetID: int(ref.OwnerID) + 1, Limit: 1,
		})
		if err != nil {
			return nil, fmt.Errorf("locate comment %d: %w", ref.OwnerID, err)
		}
		if mod, ok := res.AsModified(); ok {
			msgs = mod.GetMessages()
		}
	default:
		return nil, fmt.Errorf("unknown owner kind %q", ref.OwnerKind)
	}
	for _, m := range msgs {
		if msg, ok := m.(*tg.Message); ok && int64(msg.ID) == ref.OwnerID {
			if loc, ok := locationFrom(msg.Media, ref.TGMediaID); ok {
				return loc, nil
			}
		}
	}
	return nil, errors.New("message or its media is gone")
}

// locationFrom builds the download location for the media with the given id (largest photo size).
func locationFrom(mm tg.MessageMediaClass, id int64) (tg.InputFileLocationClass, bool) {
	switch v := mm.(type) {
	case *tg.MessageMediaPhoto:
		if ph, ok := v.Photo.AsNotEmpty(); ok && ph.ID == id {
			return &tg.InputPhotoFileLocation{
				ID: ph.ID, AccessHash: ph.AccessHash, FileReference: ph.FileReference, ThumbSize: largestType(ph.Sizes),
			}, true
		}
	case *tg.MessageMediaDocument:
		if d, ok := v.Document.AsNotEmpty(); ok && d.ID == id {
			return &tg.InputDocumentFileLocation{ID: d.ID, AccessHash: d.AccessHash, FileReference: d.FileReference}, true
		}
	}
	return nil, false
}

// largestType returns the Type letter of the biggest photo size; it must agree with largestSize.
func largestType(sizes []tg.PhotoSizeClass) string {
	var best int64 = -1
	typ := ""
	for _, s := range sizes {
		var n int64
		var t string
		switch s := s.(type) {
		case *tg.PhotoSize:
			n, t = int64(s.Size), s.Type
		case *tg.PhotoSizeProgressive:
			for _, x := range s.Sizes {
				n = max64(n, int64(x))
			}
			t = s.Type
		default:
			continue
		}
		if n > best {
			best, typ = n, t
		}
	}
	return typ
}
