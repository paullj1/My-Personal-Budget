package payroll

import (
	"database/sql/driver"
	"errors"
	"testing"
	"time"
)

func TestIsBadConn(t *testing.T) {
	if isBadConn(nil) {
		t.Fatalf("expected nil to be false")
	}
	if !isBadConn(driver.ErrBadConn) {
		t.Fatalf("expected ErrBadConn to be true")
	}
	if !isBadConn(errors.New("bad connection")) {
		t.Fatalf("expected substring match to be true")
	}
	if isBadConn(errors.New("other error")) {
		t.Fatalf("expected unrelated error to be false")
	}
}

func TestNextMonthStart(t *testing.T) {
	now := time.Date(2024, time.March, 15, 12, 30, 0, 0, time.UTC)
	next := nextMonthStart(now)
	expected := time.Date(2024, time.April, 1, 0, 0, 0, 0, time.UTC)
	if !next.Equal(expected) {
		t.Fatalf("expected %s, got %s", expected, next)
	}
}
