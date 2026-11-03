package instagram

import (
	"context"
	"fmt"
	"net/url"
)

type Media struct {
	ID            string `json:"id"`
	Caption       string `json:"caption"`
	MediaType     string `json:"media_type"` // IMAGE | VIDEO | CAROUSEL_ALBUM
	MediaURL      string `json:"media_url"`
	ThumbnailURL  string `json:"thumbnail_url"`
	Permalink     string `json:"permalink"`
	Timestamp     string `json:"timestamp"`
	LikeCount     int    `json:"like_count"`
	CommentsCount int    `json:"comments_count"`
}

type Comment struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Username  string `json:"username"`
	Timestamp string `json:"timestamp"`
	LikeCount int    `json:"like_count"`
}

// ListMedia returns the account's most recent posts.
func (m *Manager) ListMedia(ctx context.Context, accountID uint, max int) ([]Media, error) {
	if max <= 0 || max > 200 {
		max = 24
	}
	var resp struct {
		Media []Media `json:"media"`
	}
	err := m.call(ctx, accountID, "/account/medias", map[string]interface{}{"amount": max}, &resp)
	return resp.Media, err
}

// ListComments returns comments on one of the account's posts.
func (m *Manager) ListComments(ctx context.Context, accountID uint, mediaID string, max int) ([]Comment, error) {
	if err := validID(mediaID); err != nil {
		return nil, err
	}
	if max <= 0 || max > 5000 {
		max = 500
	}
	var resp struct {
		Comments []Comment `json:"comments"`
	}
	err := m.call(ctx, accountID, "/account/comments", map[string]interface{}{"media_id": mediaID, "amount": max}, &resp)
	return resp.Comments, err
}

// DeleteComment deletes a comment on one of the account's posts.
func (m *Manager) DeleteComment(ctx context.Context, accountID uint, mediaID, commentID string) error {
	if err := validID(mediaID); err != nil {
		return err
	}
	if err := validID(commentID); err != nil {
		return err
	}
	return m.call(ctx, accountID, "/account/comment/delete", map[string]interface{}{"media_id": mediaID, "comment_id": commentID}, nil)
}

// ReplyComment posts a public reply under a comment and returns its ID.
func (m *Manager) ReplyComment(ctx context.Context, accountID uint, mediaID, commentID, message string) (string, error) {
	if err := validID(mediaID); err != nil {
		return "", err
	}
	if err := validID(commentID); err != nil {
		return "", err
	}
	var resp struct {
		ID string `json:"id"`
	}
	err := m.call(ctx, accountID, "/account/comment/reply", map[string]interface{}{
		"media_id": mediaID, "comment_id": commentID, "text": message,
	}, &resp)
	return resp.ID, err
}

// PublishRequest describes a single post to publish: a file uploaded in the
// panel (Path) or a URL the panel downloads, uploaded to Instagram as the
// account.
type PublishRequest struct {
	MediaType string `json:"media_type"` // "IMAGE" | "REELS"
	URL       string `json:"url"`
	Path      string `json:"-"` // absolute path of an uploaded file
	Caption   string `json:"caption"`
}

func (r PublishRequest) Validate() error {
	if r.MediaType != "IMAGE" && r.MediaType != "REELS" {
		return fmt.Errorf("media_type IMAGE veya REELS olmalı")
	}
	if r.Path == "" {
		u, err := url.Parse(r.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("medya için bir dosya ya da http(s) URL gerekli")
		}
	}
	if len([]rune(r.Caption)) > 2200 {
		return fmt.Errorf("açıklama en fazla 2200 karakter olabilir")
	}
	return nil
}

// Published is a post that went live.
type Published struct {
	ID  string
	URL string // public link, empty if Instagram did not return a code
}

// Publish uploads and publishes one post; progress receives status lines.
func (m *Manager) Publish(ctx context.Context, accountID uint, req PublishRequest, progress func(string)) (Published, error) {
	if err := req.Validate(); err != nil {
		return Published{}, err
	}
	progress("medya Instagram'a yükleniyor")
	params := map[string]interface{}{"media_type": req.MediaType, "caption": req.Caption}
	if req.Path != "" {
		params["path"] = req.Path
	} else {
		params["url"] = req.URL
	}
	var resp struct {
		ID   string `json:"id"`
		Code string `json:"code"`
	}
	if err := m.call(ctx, accountID, "/account/publish", params, &resp); err != nil {
		return Published{}, err
	}
	return Published{ID: resp.ID, URL: PostURL(resp.Code)}, nil
}

// PostURL is the public link of a published post code.
func PostURL(code string) string {
	if code == "" {
		return ""
	}
	return "https://www.instagram.com/p/" + code + "/"
}
