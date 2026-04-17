package app

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steipete/wacli/internal/store"
	"github.com/steipete/wacli/internal/wa"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// SendTextResult is what both the in-process and IPC-delegated send paths
// return after a successful text send.
type SendTextResult struct {
	MsgID    string
	ChatJID  string
	ChatName string
}

// SendFileResult is what both the in-process and IPC-delegated send paths
// return after a successful file send.
type SendFileResult struct {
	MsgID     string
	ChatJID   string
	ChatName  string
	Filename  string
	MimeType  string
	MediaType string
}

// SendTextAndRecord sends a WhatsApp text message and records the outbound
// row in the local DB exactly once. Used by both `wacli send text`
// (in-process) and the daemon's IPC handler.
func (a *App) SendTextAndRecord(ctx context.Context, to types.JID, message string) (SendTextResult, error) {
	if a.wa == nil {
		return SendTextResult{}, fmt.Errorf("whatsapp client not initialized")
	}
	msgID, err := a.wa.SendText(ctx, to, message)
	if err != nil {
		return SendTextResult{}, err
	}

	now := time.Now().UTC()
	chatName := a.wa.ResolveChatName(ctx, to, "")
	kind := chatKind(to)
	_ = a.db.UpsertChat(to.String(), kind, chatName, now)
	_ = a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:    to.String(),
		ChatName:   chatName,
		MsgID:      string(msgID),
		SenderJID:  "",
		SenderName: "me",
		Timestamp:  now,
		FromMe:     true,
		Text:       message,
	})

	return SendTextResult{
		MsgID:    string(msgID),
		ChatJID:  to.String(),
		ChatName: chatName,
	}, nil
}

// SendFileAndRecord uploads a file, sends the appropriate media/document
// message, and records the outbound row in the local DB. Used by both
// `wacli send file` (in-process) and the daemon's IPC handler.
func (a *App) SendFileAndRecord(ctx context.Context, to types.JID, filePath, filename, caption, mimeOverride string) (SendFileResult, error) {
	if a.wa == nil {
		return SendFileResult{}, fmt.Errorf("whatsapp client not initialized")
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return SendFileResult{}, err
	}

	name := strings.TrimSpace(filename)
	if name == "" {
		name = filepath.Base(filePath)
	}
	mimeType := strings.TrimSpace(mimeOverride)
	if mimeType == "" {
		// Use filePath for MIME detection, not the display name override.
		mimeType = mime.TypeByExtension(strings.ToLower(filepath.Ext(filePath)))
	}
	if mimeType == "" {
		sniff := data
		if len(sniff) > 512 {
			sniff = sniff[:512]
		}
		mimeType = http.DetectContentType(sniff)
	}

	mediaType := "document"
	uploadType, _ := wa.MediaTypeFromString("document")
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		mediaType = "image"
		uploadType, _ = wa.MediaTypeFromString("image")
	case strings.HasPrefix(mimeType, "video/"):
		mediaType = "video"
		uploadType, _ = wa.MediaTypeFromString("video")
	case strings.HasPrefix(mimeType, "audio/"):
		mediaType = "audio"
		uploadType, _ = wa.MediaTypeFromString("audio")
	}

	up, err := a.wa.Upload(ctx, data, uploadType)
	if err != nil {
		return SendFileResult{}, err
	}

	now := time.Now().UTC()
	msg := &waProto.Message{}

	switch mediaType {
	case "image":
		msg.ImageMessage = &waProto.ImageMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
			Caption:       proto.String(caption),
		}
	case "video":
		msg.VideoMessage = &waProto.VideoMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
			Caption:       proto.String(caption),
		}
	case "audio":
		msg.AudioMessage = &waProto.AudioMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
			PTT:           proto.Bool(false),
		}
	default:
		msg.DocumentMessage = &waProto.DocumentMessage{
			URL:           proto.String(up.URL),
			DirectPath:    proto.String(up.DirectPath),
			MediaKey:      up.MediaKey,
			FileEncSHA256: up.FileEncSHA256,
			FileSHA256:    up.FileSHA256,
			FileLength:    proto.Uint64(up.FileLength),
			Mimetype:      proto.String(mimeType),
			FileName:      proto.String(name),
			Caption:       proto.String(caption),
			Title:         proto.String(name),
		}
	}

	id, err := a.wa.SendProtoMessage(ctx, to, msg)
	if err != nil {
		return SendFileResult{}, err
	}

	chatName := a.wa.ResolveChatName(ctx, to, "")
	kind := chatKind(to)
	_ = a.db.UpsertChat(to.String(), kind, chatName, now)
	_ = a.db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:       to.String(),
		ChatName:      chatName,
		MsgID:         string(id),
		SenderJID:     "",
		SenderName:    "me",
		Timestamp:     now,
		FromMe:        true,
		Text:          caption,
		MediaType:     mediaType,
		MediaCaption:  caption,
		Filename:      name,
		MimeType:      mimeType,
		DirectPath:    up.DirectPath,
		MediaKey:      up.MediaKey,
		FileSHA256:    up.FileSHA256,
		FileEncSHA256: up.FileEncSHA256,
		FileLength:    up.FileLength,
	})

	return SendFileResult{
		MsgID:     string(id),
		ChatJID:   to.String(),
		ChatName:  chatName,
		Filename:  name,
		MimeType:  mimeType,
		MediaType: mediaType,
	}, nil
}
