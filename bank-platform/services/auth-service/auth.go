package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	authv1 "github.com/dso202/bank-platform/gen/go/bank/auth/v1"
	"github.com/dso202/bank-platform/pkg/authz"
	"github.com/dso202/bank-platform/pkg/events"
	"github.com/dso202/bank-platform/pkg/logx"
	"github.com/dso202/bank-platform/pkg/util"
)

const svcName = "auth-service"

// User statuses.
const (
	StatusPending  = "PENDING_VERIFICATION"
	StatusActive   = "ACTIVE"
	StatusDisabled = "DISABLED"
)

// OTP purposes.
const (
	PurposeVerifyEmail = "VERIFY_EMAIL"
	PurposeMFA         = "MFA"
)

const (
	maxFailedLogins = 5
	lockDuration    = 15 * time.Minute
	otpTTL          = 10 * time.Minute
	maxOTPAttempts  = 5
)

var logins = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "bank_auth_logins_total", Help: "Login attempts by outcome.",
}, []string{"outcome"})

// User is an identity with credentials and roles.
type User struct {
	ID            string `gorm:"primaryKey;type:uuid"`
	Email         string `gorm:"uniqueIndex;not null"`
	Phone         string
	FullName      string `gorm:"not null"`
	PasswordHash  string `gorm:"not null"`
	Roles         string `gorm:"not null"` // comma-separated
	Status        string `gorm:"not null;index"`
	EmailVerified bool
	MfaEnabled    bool
	FailedLogins  int
	LockedUntil   *time.Time
	LastLoginAt   *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (u *User) roleList() []string { return strings.Split(u.Roles, ",") }

// OTP is a one-time code for email verification or MFA. Only hashes are stored.
type OTP struct {
	ID            string `gorm:"primaryKey;type:uuid"`
	UserID        string `gorm:"type:uuid;index;not null"`
	Purpose       string `gorm:"not null"`
	CodeHash      string `gorm:"not null"`
	ChallengeHash string `gorm:"index"`
	Attempts      int
	ExpiresAt     time.Time
	ConsumedAt    *time.Time
	CreatedAt     time.Time
}

// RefreshToken is a rotating, revocable session token. Only hashes are stored.
type RefreshToken struct {
	ID         string `gorm:"primaryKey;type:uuid"`
	UserID     string `gorm:"type:uuid;index;not null"`
	TokenHash  string `gorm:"uniqueIndex;not null"`
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	ReplacedBy string
	CreatedAt  time.Time
}

type authServer struct {
	authv1.UnimplementedAuthServiceServer
	db         *gorm.DB
	keys       *keySet
	accessTTL  time.Duration
	refreshTTL time.Duration
	exposeOTP  bool
}

func toProto(u *User) *authv1.User {
	return &authv1.User{
		Id: u.ID, Email: u.Email, Phone: u.Phone, FullName: u.FullName, Roles: u.roleList(),
		Status: u.Status, EmailVerified: u.EmailVerified, MfaEnabled: u.MfaEnabled,
		CreatedAt: util.TS(u.CreatedAt), LastLoginAt: util.TSP(u.LastLoginAt),
	}
}

func normEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func validatePassword(pw string) error {
	if len(pw) < 8 {
		return util.Invalid("password must be at least 8 characters")
	}
	var letter, digit bool
	for _, r := range pw {
		letter = letter || unicode.IsLetter(r)
		digit = digit || unicode.IsDigit(r)
	}
	if !letter || !digit {
		return util.Invalid("password must contain letters and digits")
	}
	return nil
}

func (s *authServer) seedStaff(ctx context.Context, password string) {
	staff := []struct{ email, name, roles string }{
		{"admin@bank.local", "System Administrator", "admin"},
		{"ops@bank.local", "Operations Officer", "ops_officer"},
		{"loans@bank.local", "Loan Officer", "loan_officer"},
		{"fraud@bank.local", "Fraud Analyst", "fraud_analyst"},
		{"auditor@bank.local", "Compliance Auditor", "auditor"},
	}
	for _, st := range staff {
		var n int64
		s.db.WithContext(ctx).Model(&User{}).Where("email = ?", st.email).Count(&n)
		if n > 0 {
			continue
		}
		u := User{ID: uuid.NewString(), Email: st.email, FullName: st.name, PasswordHash: hashPassword(password),
			Roles: st.roles, Status: StatusActive, EmailVerified: true}
		if err := s.db.WithContext(ctx).Create(&u).Error; err != nil {
			slog.Warn("seed staff user", "email", st.email, "err", err)
		} else {
			slog.Info("seeded staff user", "email", st.email, "roles", st.roles)
		}
	}
}

// issueOTP stores a new code and queues the email. It returns the plain code.
func (s *authServer) issueOTP(tx *gorm.DB, u *User, purpose, challenge string) (string, error) {
	tx.Model(&OTP{}).Where("user_id = ? AND purpose = ? AND consumed_at IS NULL", u.ID, purpose).
		Update("consumed_at", time.Now().UTC())
	code := otpCode()
	o := OTP{ID: uuid.NewString(), UserID: u.ID, Purpose: purpose, CodeHash: sha(code), ExpiresAt: time.Now().UTC().Add(otpTTL)}
	if challenge != "" {
		o.ChallengeHash = sha(challenge)
	}
	if err := tx.Create(&o).Error; err != nil {
		return "", err
	}
	label := "verify your email"
	if purpose == PurposeMFA {
		label = "sign in"
	}
	err := events.NotifyEmail(tx, u.ID, u.Email, "OTP", map[string]string{"code": code, "purpose": label, "name": u.FullName})
	return code, err
}

func (s *authServer) devHint(code string) string {
	if s.exposeOTP {
		return fmt.Sprintf(" (dev mode code: %s)", code)
	}
	return ""
}

// consumeOTP validates a code; it counts attempts and burns the code on success.
func (s *authServer) consumeOTP(tx *gorm.DB, o *OTP, code string) error {
	if o.ConsumedAt != nil || time.Now().After(o.ExpiresAt) {
		return util.Precondition("code expired, request a new one")
	}
	if o.Attempts >= maxOTPAttempts {
		return util.Precondition("too many attempts, request a new code")
	}
	if sha(strings.TrimSpace(code)) != o.CodeHash {
		// Count the attempt outside the caller's transaction, which rolls back on error.
		s.db.Model(&OTP{}).Where("id = ?", o.ID).Update("attempts", gorm.Expr("attempts + 1"))
		return status.Error(codes.InvalidArgument, "incorrect code")
	}
	now := time.Now().UTC()
	return tx.Model(o).Update("consumed_at", now).Error
}

func (s *authServer) Register(ctx context.Context, req *authv1.RegisterRequest) (*authv1.RegisterResponse, error) {
	email := normEmail(req.Email)
	if _, err := mail.ParseAddress(email); err != nil {
		return nil, util.Invalid("a valid email is required")
	}
	if strings.TrimSpace(req.FullName) == "" {
		return nil, util.Invalid("full name is required")
	}
	if err := validatePassword(req.Password); err != nil {
		return nil, err
	}
	u := User{
		ID: uuid.NewString(), Email: email, Phone: strings.TrimSpace(req.Phone), FullName: strings.TrimSpace(req.FullName),
		PasswordHash: hashPassword(req.Password), Roles: authz.RoleCustomer, Status: StatusPending,
	}
	var code string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&u).Error; err != nil {
			return err
		}
		var err error
		if code, err = s.issueOTP(tx, &u, PurposeVerifyEmail, ""); err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "user.register", "user", u.ID, "SUCCESS", nil, map[string]any{"email": logx.MaskEmail(email)})
	})
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return nil, status.Error(codes.AlreadyExists, "an account with this email already exists")
	}
	if err != nil {
		return nil, util.Internal(err)
	}
	return &authv1.RegisterResponse{UserId: u.ID, Message: "Verification code sent to " + logx.MaskEmail(email) + s.devHint(code)}, nil
}

func (s *authServer) VerifyEmail(ctx context.Context, req *authv1.VerifyEmailRequest) (*authv1.VerifyEmailResponse, error) {
	var u User
	if err := s.db.WithContext(ctx).First(&u, "email = ?", normEmail(req.Email)).Error; err != nil {
		return nil, status.Error(codes.InvalidArgument, "incorrect code")
	}
	if u.EmailVerified {
		return &authv1.VerifyEmailResponse{Verified: true}, nil
	}
	var o OTP
	if err := s.db.WithContext(ctx).Where("user_id = ? AND purpose = ? AND consumed_at IS NULL", u.ID, PurposeVerifyEmail).
		Order("created_at DESC").First(&o).Error; err != nil {
		return nil, util.Precondition("no pending code, request a new one")
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.consumeOTP(tx, &o, req.Code); err != nil {
			return err
		}
		if err := tx.Model(&u).Updates(map[string]any{"email_verified": true, "status": StatusActive}).Error; err != nil {
			return err
		}
		if err := events.Notify(tx, u.ID, "WELCOME", map[string]string{"name": u.FullName}); err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "user.verify_email", "user", u.ID, "SUCCESS", nil, nil)
	})
	if err != nil {
		return nil, err
	}
	return &authv1.VerifyEmailResponse{Verified: true}, nil
}

func (s *authServer) ResendCode(ctx context.Context, req *authv1.ResendCodeRequest) (*authv1.ResendCodeResponse, error) {
	generic := "If the account exists and is unverified, a new code has been sent"
	var u User
	if err := s.db.WithContext(ctx).First(&u, "email = ?", normEmail(req.Email)).Error; err != nil || u.EmailVerified {
		return &authv1.ResendCodeResponse{Message: generic}, nil
	}
	var code string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		code, err = s.issueOTP(tx, &u, PurposeVerifyEmail, "")
		return err
	})
	if err != nil {
		return nil, util.Internal(err)
	}
	return &authv1.ResendCodeResponse{Message: generic + s.devHint(code)}, nil
}

func (s *authServer) Login(ctx context.Context, req *authv1.LoginRequest) (*authv1.LoginResponse, error) {
	invalid := status.Error(codes.Unauthenticated, "invalid email or password")
	var u User
	if err := s.db.WithContext(ctx).First(&u, "email = ?", normEmail(req.Email)).Error; err != nil {
		hashPassword(req.Password) // keep timing similar for unknown users
		logins.WithLabelValues("unknown_user").Inc()
		return nil, invalid
	}
	if u.LockedUntil != nil && time.Now().Before(*u.LockedUntil) {
		logins.WithLabelValues("locked").Inc()
		return nil, status.Errorf(codes.PermissionDenied, "account locked after repeated failures; try again after %s UTC", u.LockedUntil.Format("15:04"))
	}
	if !checkPassword(u.PasswordHash, req.Password) {
		updates := map[string]any{"failed_logins": u.FailedLogins + 1}
		if u.FailedLogins+1 >= maxFailedLogins {
			t := time.Now().UTC().Add(lockDuration)
			updates["locked_until"], updates["failed_logins"] = t, 0
		}
		_ = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			tx.Model(&u).Updates(updates)
			return events.Audit(ctx, tx, svcName, "user.login", "user", u.ID, "FAILURE", nil, map[string]any{"reason": "bad_password"})
		})
		logins.WithLabelValues("bad_password").Inc()
		return nil, invalid
	}
	switch u.Status {
	case StatusPending:
		return nil, status.Error(codes.FailedPrecondition, "email not verified")
	case StatusDisabled:
		return nil, status.Error(codes.PermissionDenied, "account disabled; contact the bank")
	}
	if u.MfaEnabled {
		challenge := randomToken()
		var code string
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var err error
			code, err = s.issueOTP(tx, &u, PurposeMFA, challenge)
			return err
		})
		if err != nil {
			return nil, util.Internal(err)
		}
		logins.WithLabelValues("mfa_challenge").Inc()
		resp := &authv1.LoginResponse{MfaRequired: true, MfaToken: challenge}
		if s.exposeOTP {
			slog.InfoContext(ctx, "dev mode MFA code", "user_id", u.ID, "code", code)
		}
		return resp, nil
	}
	return s.completeLogin(ctx, &u, "password")
}

func (s *authServer) VerifyMfa(ctx context.Context, req *authv1.VerifyMfaRequest) (*authv1.LoginResponse, error) {
	var o OTP
	if err := s.db.WithContext(ctx).Where("challenge_hash = ? AND purpose = ?", sha(req.MfaToken), PurposeMFA).First(&o).Error; err != nil {
		return nil, status.Error(codes.Unauthenticated, "sign-in session expired, start again")
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return s.consumeOTP(tx, &o, req.Code) }); err != nil {
		return nil, err
	}
	var u User
	if err := s.db.WithContext(ctx).First(&u, "id = ?", o.UserID).Error; err != nil {
		return nil, util.NotFound("user")
	}
	return s.completeLogin(ctx, &u, "password+otp")
}

func (s *authServer) completeLogin(ctx context.Context, u *User, method string) (*authv1.LoginResponse, error) {
	now := time.Now().UTC()
	var pair *authv1.TokenPair
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		if pair, err = s.newSession(tx, u); err != nil {
			return err
		}
		tx.Model(u).Updates(map[string]any{"failed_logins": 0, "locked_until": nil, "last_login_at": now})
		if err := events.Notify(tx, u.ID, "LOGIN_ALERT", map[string]string{"method": method, "time": now.Format(time.RFC1123)}); err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "user.login", "user", u.ID, "SUCCESS", nil, map[string]any{"method": method})
	})
	if err != nil {
		return nil, util.Internal(err)
	}
	u.LastLoginAt = &now
	logins.WithLabelValues("success").Inc()
	return &authv1.LoginResponse{Tokens: pair, User: toProto(u)}, nil
}

func (s *authServer) newSession(tx *gorm.DB, u *User) (*authv1.TokenPair, error) {
	access, err := s.keys.sign(u, s.accessTTL)
	if err != nil {
		return nil, err
	}
	refresh := randomToken()
	rt := RefreshToken{ID: uuid.NewString(), UserID: u.ID, TokenHash: sha(refresh), ExpiresAt: time.Now().UTC().Add(s.refreshTTL)}
	if err := tx.Create(&rt).Error; err != nil {
		return nil, err
	}
	return &authv1.TokenPair{AccessToken: access, RefreshToken: refresh, ExpiresIn: int64(s.accessTTL.Seconds()), TokenType: "Bearer"}, nil
}

// Refresh rotates the refresh token. Reusing an already-rotated token revokes
// every session of that user (token theft detection).
func (s *authServer) Refresh(ctx context.Context, req *authv1.RefreshRequest) (*authv1.LoginResponse, error) {
	expired := status.Error(codes.Unauthenticated, "session expired, sign in again")
	var rt RefreshToken
	if err := s.db.WithContext(ctx).First(&rt, "token_hash = ?", sha(req.RefreshToken)).Error; err != nil {
		return nil, expired
	}
	now := time.Now().UTC()
	if rt.RevokedAt != nil {
		if rt.ReplacedBy != "" {
			s.db.WithContext(ctx).Model(&RefreshToken{}).Where("user_id = ? AND revoked_at IS NULL", rt.UserID).Update("revoked_at", now)
			slog.WarnContext(ctx, "refresh token reuse detected; all sessions revoked", "user_id", rt.UserID)
		}
		return nil, expired
	}
	if now.After(rt.ExpiresAt) {
		return nil, expired
	}
	var u User
	if err := s.db.WithContext(ctx).First(&u, "id = ?", rt.UserID).Error; err != nil || u.Status != StatusActive {
		return nil, expired
	}
	var pair *authv1.TokenPair
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		if pair, err = s.newSession(tx, &u); err != nil {
			return err
		}
		res := tx.Model(&RefreshToken{}).Where("id = ? AND revoked_at IS NULL", rt.ID).
			Updates(map[string]any{"revoked_at": now, "replaced_by": sha(pair.RefreshToken)})
		if res.RowsAffected == 0 {
			return expired
		}
		return res.Error
	})
	if err != nil {
		return nil, err
	}
	return &authv1.LoginResponse{Tokens: pair, User: toProto(&u)}, nil
}

func (s *authServer) Logout(ctx context.Context, req *authv1.LogoutRequest) (*authv1.LogoutResponse, error) {
	if req.RefreshToken != "" {
		s.db.WithContext(ctx).Model(&RefreshToken{}).Where("token_hash = ? AND revoked_at IS NULL", sha(req.RefreshToken)).
			Update("revoked_at", time.Now().UTC())
	}
	return &authv1.LogoutResponse{}, nil
}

func (s *authServer) currentUser(ctx context.Context) (*User, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	var u User
	if err := s.db.WithContext(ctx).First(&u, "id = ?", p.UserID).Error; err != nil {
		return nil, util.NotFound("user")
	}
	return &u, nil
}

func (s *authServer) GetMe(ctx context.Context, _ *authv1.GetMeRequest) (*authv1.User, error) {
	u, err := s.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	return toProto(u), nil
}

func (s *authServer) SetMfa(ctx context.Context, req *authv1.SetMfaRequest) (*authv1.User, error) {
	u, err := s.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(u).Update("mfa_enabled", req.Enabled).Error; err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "user.set_mfa", "user", u.ID, "SUCCESS", nil, map[string]any{"enabled": req.Enabled})
	})
	if err != nil {
		return nil, util.Internal(err)
	}
	u.MfaEnabled = req.Enabled
	return toProto(u), nil
}

func (s *authServer) ChangePassword(ctx context.Context, req *authv1.ChangePasswordRequest) (*authv1.ChangePasswordResponse, error) {
	u, err := s.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	if !checkPassword(u.PasswordHash, req.CurrentPassword) {
		return nil, status.Error(codes.InvalidArgument, "current password is incorrect")
	}
	if err := validatePassword(req.NewPassword); err != nil {
		return nil, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(u).Update("password_hash", hashPassword(req.NewPassword)).Error; err != nil {
			return err
		}
		// Sign out every other session.
		tx.Model(&RefreshToken{}).Where("user_id = ? AND revoked_at IS NULL", u.ID).Update("revoked_at", time.Now().UTC())
		if err := events.Notify(tx, u.ID, "PASSWORD_CHANGED", nil); err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "user.change_password", "user", u.ID, "SUCCESS", nil, nil)
	})
	if err != nil {
		return nil, util.Internal(err)
	}
	return &authv1.ChangePasswordResponse{}, nil
}

func (s *authServer) GetJwks(context.Context, *authv1.GetJwksRequest) (*authv1.GetJwksResponse, error) {
	return &authv1.GetJwksResponse{Keys: []*authv1.Jwk{s.keys.jwk()}}, nil
}

func (s *authServer) CreateStaffUser(ctx context.Context, req *authv1.CreateStaffUserRequest) (*authv1.User, error) {
	if _, err := authz.RequireRole(ctx, authz.RoleAdmin); err != nil {
		return nil, err
	}
	email := normEmail(req.Email)
	if _, err := mail.ParseAddress(email); err != nil {
		return nil, util.Invalid("a valid email is required")
	}
	if err := validatePassword(req.Password); err != nil {
		return nil, err
	}
	if len(req.Roles) == 0 {
		return nil, util.Invalid("at least one role is required")
	}
	for _, r := range req.Roles {
		if !slices.Contains(authz.AllRoles, r) {
			return nil, util.Invalid("unknown role %q", r)
		}
	}
	u := User{ID: uuid.NewString(), Email: email, Phone: req.Phone, FullName: req.FullName, PasswordHash: hashPassword(req.Password),
		Roles: strings.Join(req.Roles, ","), Status: StatusActive, EmailVerified: true}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&u).Error; err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "user.create_staff", "user", u.ID, "SUCCESS", nil, map[string]any{"roles": req.Roles})
	})
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return nil, status.Error(codes.AlreadyExists, "email already in use")
	}
	if err != nil {
		return nil, util.Internal(err)
	}
	return toProto(&u), nil
}

func (s *authServer) ListUsers(ctx context.Context, req *authv1.ListUsersRequest) (*authv1.ListUsersResponse, error) {
	if _, err := authz.RequireRole(ctx, authz.RoleAdmin); err != nil {
		return nil, err
	}
	limit, offset := util.Page(req.PageSize, req.PageToken)
	q := s.db.WithContext(ctx).Model(&User{})
	if req.Query != "" {
		like := "%" + strings.ToLower(req.Query) + "%"
		q = q.Where("LOWER(email) LIKE ? OR LOWER(full_name) LIKE ?", like, like)
	}
	if req.Role != "" {
		q = q.Where("(',' || roles || ',') LIKE ?", "%,"+req.Role+",%")
	}
	var users []User
	if err := q.Order("created_at DESC").Limit(limit).Offset(offset).Find(&users).Error; err != nil {
		return nil, util.Internal(err)
	}
	out := &authv1.ListUsersResponse{NextPageToken: util.NextToken(len(users), limit, offset)}
	for i := range users {
		out.Users = append(out.Users, toProto(&users[i]))
	}
	return out, nil
}

func (s *authServer) SetUserStatus(ctx context.Context, req *authv1.SetUserStatusRequest) (*authv1.User, error) {
	p, err := authz.RequireRole(ctx, authz.RoleAdmin)
	if err != nil {
		return nil, err
	}
	if req.Status != StatusActive && req.Status != StatusDisabled {
		return nil, util.Invalid("status must be ACTIVE or DISABLED")
	}
	if req.UserId == p.UserID {
		return nil, util.Precondition("you cannot change your own status")
	}
	var u User
	if err := s.db.WithContext(ctx).First(&u, "id = ?", req.UserId).Error; err != nil {
		return nil, util.NotFound("user")
	}
	before := u.Status
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&u).Update("status", req.Status).Error; err != nil {
			return err
		}
		if req.Status == StatusDisabled {
			tx.Model(&RefreshToken{}).Where("user_id = ? AND revoked_at IS NULL", u.ID).Update("revoked_at", time.Now().UTC())
		}
		return events.Audit(ctx, tx, svcName, "user.set_status", "user", u.ID, "SUCCESS",
			map[string]any{"status": before}, map[string]any{"status": req.Status})
	})
	if err != nil {
		return nil, util.Internal(err)
	}
	u.Status = req.Status
	return toProto(&u), nil
}

func (s *authServer) GetUser(ctx context.Context, req *authv1.GetUserRequest) (*authv1.User, error) {
	var u User
	if err := s.db.WithContext(ctx).First(&u, "id = ?", req.UserId).Error; err != nil {
		return nil, util.NotFound("user")
	}
	return toProto(&u), nil
}

// cleanup deletes expired OTPs and refresh tokens.
func (s *authServer) cleanup(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cutoff := time.Now().UTC().Add(-24 * time.Hour)
			s.db.Where("expires_at < ?", cutoff).Delete(&OTP{})
			s.db.Where("expires_at < ?", cutoff).Delete(&RefreshToken{})
		}
	}
}
