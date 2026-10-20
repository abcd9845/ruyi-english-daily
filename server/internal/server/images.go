package server

// 作答选图上传（大脑 #57 / PR-I1）。默认关：RUYI_ANSWER_IMAGES_ENABLED=1 且队列开着才生效。
//
//	POST /api/practice/{set}/images     需 cookie；正文 = 一张图片原始字节，
//	                                    Content-Type image/jpeg | image/png，≤ 5 MiB
//	  → 200 {"sha256","bytes","mime"}   之后在 /grade 的 answers[].images 里引用 sha256
//
// 站点先自己校验（大小、魔数、sha256），再用写令牌 PUT 到大脑
// /v1/brains/{slug}/answer-media/{sha256}；密钥只在服务端。上传本身不算一次提交，
// 不占“每题每天 3 次”的额度（额度只在 /grade 计）。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/abcd9845/ruyi-english-daily/server/internal/brain"
)

// AnswerMediaUploader is the brain side of the upload (*brain.Client in production).
type AnswerMediaUploader interface {
	PutAnswerMedia(ctx context.Context, sha, mime string, body []byte) (*brain.AnswerMedia, error)
}

var _ AnswerMediaUploader = (*brain.Client)(nil)

const uploadsTTL = 24 * time.Hour // the brain drops unattached images after 24 h

// Chinese messages shown by the page.
const (
	msgImgTooBig     = "图片太大了：每张不能超过 5MB。请换一张或先压缩。"
	msgImgFormat     = "只支持 JPG 或 PNG 图片。"
	msgImgBroken     = "这张图片打不开或尺寸太大，请换一张。"
	msgImgTooMany    = "每题最多 6 张图片。"
	msgImgTotal      = "这题的图片加起来超过 20MB 了，请删掉几张。"
	msgImgUnknown    = "有图片没有上传成功，请删掉后重新选择。"
	msgImgDaily      = "今天上传的图片已经到上限了，明天再传。"
	msgImgQuota      = "图片存储空间已满，请告诉家长。"
	msgImgBusy       = "上传的人有点多，请过几秒再试。"
	msgImgUnavail    = "图片暂时传不上去，请稍后再试；答案还在。"
	msgImgOff        = "这里暂时不能上传图片。"
	msgImgNeedAnswer = "传了图片的题，请也写一句文字作答（例如“见图”）。"
)

type uploadRec struct {
	bytes int64
	at    time.Time
}

type uploads struct {
	mu sync.Mutex
	m  map[string]uploadRec // session|sha → size
}

func (u *uploads) put(sess, sha string, n int64, now time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.m == nil {
		u.m = map[string]uploadRec{}
	}
	for k, v := range u.m {
		if now.Sub(v.at) > uploadsTTL {
			delete(u.m, k)
		}
	}
	u.m[sess+"|"+sha] = uploadRec{n, now}
}

func (u *uploads) size(sess, sha string, now time.Time) (int64, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	v, ok := u.m[sess+"|"+sha]
	if !ok || now.Sub(v.at) > uploadsTTL {
		return 0, false
	}
	return v.bytes, true
}

func (s *Server) imagesOn() bool { return s.ImagesEnabled && s.Media != nil && s.queueOn() }

func sniffImage(b []byte) string {
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "image/jpeg"
	case len(b) >= 8 && bytes.Equal(b[:8], []byte("\x89PNG\r\n\x1a\n")):
		return "image/png"
	}
	return ""
}

func (s *Server) uploadImage(w http.ResponseWriter, r *http.Request, id string) {
	if !s.imagesOn() {
		writeErr(w, http.StatusNotFound, "images_not_enabled", msgImgOff)
		return
	}
	if _, ok := s.getSet(id); !ok {
		writeErr(w, http.StatusNotFound, "unknown_set", "没有这套题")
		return
	}
	mt := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]))
	if mt != "image/jpeg" && mt != "image/png" {
		writeErr(w, http.StatusUnsupportedMediaType, "unsupported_media_type", msgImgFormat)
		return
	}
	if r.ContentLength > brain.MaxAnswerImageBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large", msgImgTooBig)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, brain.MaxAnswerImageBytes))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large", msgImgTooBig)
			return
		}
		writeErr(w, http.StatusBadRequest, "bad_request", msgImgUnavail)
		return
	}
	if len(body) == 0 {
		writeErr(w, http.StatusBadRequest, "empty_image", msgImgBroken)
		return
	}
	real := sniffImage(body)
	if real == "" {
		writeErr(w, http.StatusUnsupportedMediaType, "unsupported_media_type", msgImgFormat)
		return
	}
	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])
	res, err := s.Media.PutAnswerMedia(r.Context(), sha, real, body)
	if err != nil {
		s.imageErr(w, err)
		return
	}
	s.ups.put(sessionKey(r), sha, int64(len(body)), s.now())
	writeJSON(w, http.StatusOK, map[string]any{"sha256": sha, "bytes": len(body), "mime": real, "created": res.Created})
}

// imageErr maps the brain's upload refusals to page messages (codes only, never brain text).
func (s *Server) imageErr(w http.ResponseWriter, err error) {
	var qe *brain.QueueError
	reason := ""
	retry := 0
	if errors.As(err, &qe) {
		reason, retry = qe.Reason, qe.RetryAfterS
	}
	s.Log.Warn("answer image upload failed", "reason", nz(reason, "transport"))
	if retry > 0 {
		w.Header().Set("Retry-After", fmt.Sprint(retry))
	}
	switch reason {
	case "payload_too_large":
		writeErr(w, http.StatusRequestEntityTooLarge, reason, msgImgTooBig)
	case "unsupported_media_type":
		writeErr(w, http.StatusUnsupportedMediaType, reason, msgImgFormat)
	case "invalid_image", "sha_mismatch":
		writeErr(w, http.StatusUnprocessableEntity, reason, msgImgBroken)
	case "media_daily_quota":
		writeErr(w, http.StatusTooManyRequests, reason, msgImgDaily)
	case "brain_media_quota_exceeded", "media_disk_low":
		writeErr(w, http.StatusConflict, reason, msgImgQuota)
	case "busy":
		writeErr(w, http.StatusServiceUnavailable, reason, msgImgBusy)
	default:
		writeErr(w, http.StatusBadGateway, "brain_unavailable", msgImgUnavail)
	}
}

// checkImages validates answers[].images of a grade request: ≤6 per answer,
// uploaded through this site by this session (last 24 h), together ≤ 20 MB.
// Returns ("", "") when fine, else the error id and message.
func (s *Server) checkImages(r *http.Request, imgs []string) (string, string) {
	if len(imgs) == 0 {
		return "", ""
	}
	if !s.imagesOn() {
		return "images_not_enabled", msgImgOff
	}
	if len(imgs) > brain.MaxAnswerImages {
		return "too_many_images", msgImgTooMany
	}
	seen := map[string]bool{}
	var total int64
	for _, h := range imgs {
		if !brain.ValidSHA256(h) || seen[h] {
			return "bad_images", msgImgUnknown
		}
		seen[h] = true
		n, ok := s.ups.size(sessionKey(r), h, s.now())
		if !ok {
			return "image_not_uploaded", msgImgUnknown
		}
		total += n
	}
	if total > brain.MaxAnswerImagesTotal {
		return "images_total_too_large", msgImgTotal
	}
	return "", ""
}
