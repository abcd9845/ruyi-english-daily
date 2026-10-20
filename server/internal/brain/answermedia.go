package brain

// Answer images (大脑 Issue #57, PR-I1 #81 contract §作答图片). THE WIRE SHAPE OF
// THE UPLOAD LIVES IN THIS FILE.
//
//	M1  PUT {base}/v1/brains/{slug}/answer-media/{sha256}   write token (brain:{slug}:write)
//	    body = the raw jpeg/png bytes, Content-Type image/jpeg|image/png, Content-Length set
//	    201 new | 200 same sha replayed: {"id","sha256","mime","bytes","width","height","expires_at","attached","created"}
//	    413 payload_too_large · 415 unsupported_media_type · 422 invalid_image|sha_mismatch
//	    429 media_daily_quota · 409 brain_media_quota_exceeded · 503 busy|media_disk_low|media_unavailable
//
// The enqueue then references the uploaded images by sha256 (`items[].images`,
// PR-I2, NOT merged yet: see encodeEnqueue). The site never reads answer images
// back and never asks for a ticket.
//
// 联调依据：大脑 docs/media-api.md（PR #82，部署 10db72a3）。入队 items[].images = [{"sha256":…}]。
// 超 5MiB 时 nginx 直接回 HTML 413（无 JSON code），所以错误按状态码兜底映射。
// 大脑允许“只有图无文字”入队；站点暂仍要求有文字作答（grade 不收空作答），见交付说明。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"time"
)

const (
	// MaxAnswerImageBytes is the brain's per-image limit (5 MiB).
	MaxAnswerImageBytes = 5 << 20
	// MaxAnswerImages / MaxAnswerImagesTotal are the per-answer limits checked by the brain at enqueue.
	MaxAnswerImages      = 6
	MaxAnswerImagesTotal = 20 << 20
	// AnswerMediaTimeout bounds one upload (the brain allows 120 s; nginx 60 s).
	AnswerMediaTimeout = 60 * time.Second
)

var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidSHA256 reports a 64-char lowercase hex digest.
func ValidSHA256(s string) bool { return sha256Re.MatchString(s) }

// AnswerMedia is the brain's reply to an upload.
type AnswerMedia struct {
	SHA256  string
	MIME    string
	Bytes   int64
	Created bool
}

// PutAnswerMedia uploads one answer image (M1) with the write token.
// Errors are *QueueError (Reason = the brain's error.code, e.g. media_daily_quota).
func (c *Client) PutAnswerMedia(ctx context.Context, sha, mime string, body []byte) (*AnswerMedia, error) {
	if c.gradeToken == "" {
		return nil, ErrGradeDisabled
	}
	if !ValidSHA256(sha) || (mime != "image/jpeg" && mime != "image/png") || len(body) == 0 || len(body) > MaxAnswerImageBytes {
		return nil, &Error{Kind: KindBadRequest, What: "bad answer image"}
	}
	ctx, cancel := context.WithTimeout(ctx, AnswerMediaTimeout)
	defer cancel()
	u := c.endpoint("/v1/brains/"+c.id()+"/answer-media/"+sha, nil)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(body))
	if err != nil {
		return nil, &Error{Kind: KindUnavailable, What: "bad upstream request"}
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("Authorization", "Bearer "+c.gradeToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "ruyi-site/1")
	req.Header.Set("Content-Type", mime)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, classifyTransport(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxQueueErrLen))
		return nil, parseQueueError(resp.StatusCode, resp.Header, raw)
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var v struct {
		SHA256  string `json:"sha256"`
		MIME    string `json:"mime"`
		Bytes   int64  `json:"bytes"`
		Created bool   `json:"created"`
	}
	if json.Unmarshal(b, &v) != nil || v.SHA256 != sha {
		return nil, &Error{Kind: KindUnavailable, What: "malformed answer-media reply"}
	}
	return &AnswerMedia{SHA256: v.SHA256, MIME: v.MIME, Bytes: v.Bytes, Created: resp.StatusCode == http.StatusCreated}, nil
}
