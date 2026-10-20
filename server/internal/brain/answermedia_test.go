package brain

import (
	"strings"
	"testing"
	"time"
)

func TestEnqueueImagesWire(t *testing.T) {
	sha := strings.Repeat("a", 64)
	r := EnqueueRequest{ClientAttemptID: "11111111-1111-4111-8111-111111111111", Items: []EnqueueItem{
		{QRef: "x:1", Answer: "见图", Images: []string{sha}}, {QRef: "x:2", Answer: "a"}}}
	if err := ValidateEnqueue(r); err != nil {
		t.Fatal(err)
	}
	b, _ := encodeEnqueue(r, "ruyi", time.Now())
	if !strings.Contains(string(b), `"images":["`+sha+`"]`) || strings.Count(string(b), `"images"`) != 1 {
		t.Errorf("wire: %s", b)
	}
	r.Items[0].Images = []string{sha, sha, sha, sha, sha, sha, sha}
	if ValidateEnqueue(r) == nil {
		t.Error("7 images must be refused locally")
	}
	r.Items[0].Images = []string{"XYZ"}
	if ValidateEnqueue(r) == nil {
		t.Error("bad sha must be refused locally")
	}
}
