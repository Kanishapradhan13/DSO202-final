// Package util holds small helpers shared by services: paging, money formatting,
// timestamps and validation errors.
package util

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Page converts a page size and opaque token into limit and offset.
func Page(size int32, token string) (limit, offset int) {
	limit = int(size)
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	if token != "" {
		if b, err := base64.RawURLEncoding.DecodeString(token); err == nil {
			offset, _ = strconv.Atoi(string(b))
		}
	}
	return limit, offset
}

// NextToken returns the token for the next page, or "" when count < limit.
func NextToken(count, limit, offset int) string {
	if count < limit {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset + limit)))
}

// Nu formats minor units (chhertrum) as ngultrum, e.g. 123450 -> "Nu. 1,234.50".
func Nu(minor int64) string {
	neg := minor < 0
	if neg {
		minor = -minor
	}
	whole := strconv.FormatInt(minor/100, 10)
	var b strings.Builder
	for i, c := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	s := fmt.Sprintf("Nu. %s.%02d", b.String(), minor%100)
	if neg {
		return "-" + s
	}
	return s
}

// TS converts a time to a protobuf timestamp (nil-safe).
func TS(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

// TSP converts an optional time to a protobuf timestamp.
func TSP(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}

// Invalid returns an InvalidArgument error.
func Invalid(format string, a ...any) error {
	return status.Errorf(codes.InvalidArgument, format, a...)
}

// NotFound returns a NotFound error.
func NotFound(what string) error { return status.Errorf(codes.NotFound, "%s not found", what) }

// Precondition returns a FailedPrecondition error.
func Precondition(format string, a ...any) error {
	return status.Errorf(codes.FailedPrecondition, format, a...)
}

// Denied returns a PermissionDenied error.
func Denied(format string, a ...any) error {
	return status.Errorf(codes.PermissionDenied, format, a...)
}

// Internal wraps an unexpected error.
func Internal(err error) error {
	if _, ok := status.FromError(err); ok && status.Code(err) != codes.Unknown {
		return err
	}
	return status.Errorf(codes.Internal, "internal error: %v", err)
}

// RandomDigits returns n cryptographically random decimal digits.
func RandomDigits(n int) string {
	var b strings.Builder
	for range n {
		d, _ := rand.Int(rand.Reader, big.NewInt(10))
		b.WriteByte(byte('0' + d.Int64()))
	}
	return b.String()
}

// ParseDate parses YYYY-MM-DD in UTC.
func ParseDate(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", s, time.UTC)
}

// StartOfDay returns midnight UTC for t.
func StartOfDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// MaskTail masks all but the last n characters.
func MaskTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.Repeat("•", len(s)-n) + s[len(s)-n:]
}
