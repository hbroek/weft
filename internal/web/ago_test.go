package web

import (
	"testing"
	"time"
)

func TestStampUsesTheServersZone(t *testing.T) {
	old := time.Local
	time.Local = time.FixedZone("test", -4*3600)
	defer func() { time.Local = old }()
	utc := time.Date(2026, 10, 22, 20, 10, 0, 0, time.UTC)
	if got := stamp(utc); got != "22 Oct 2026 16:10" {
		t.Errorf("stamp(utc) = %q", got)
	}
	if got := stampAt(utc); got != "22 Oct 2026 at 16:10" {
		t.Errorf("stampAt(utc) = %q", got)
	}
	if stamp(utc) != stamp(utc.In(time.Local)) {
		t.Errorf("the same moment reads differently by the zone it carries")
	}
}
