// Package authz carries the caller identity that the API gateway derives from a
// verified JWT. The gateway is the only component that sets these metadata keys;
// it never forwards client-supplied values for them.
package authz

import (
	"context"
	"slices"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Metadata keys propagated between gateway and services.
const (
	MDUserID         = "x-user-id"
	MDUserRoles      = "x-user-roles"
	MDUserEmail      = "x-user-email"
	MDUserName       = "x-user-name"
	MDRequestID      = "x-request-id"
	MDClientIP       = "x-client-ip"
	MDIdempotencyKey = "idempotency-key"
	MDInternalCaller = "x-internal-caller"
)

// Roles.
const (
	RoleCustomer     = "customer"
	RoleOpsOfficer   = "ops_officer"
	RoleLoanOfficer  = "loan_officer"
	RoleFraudAnalyst = "fraud_analyst"
	RoleAuditor      = "auditor"
	RoleAdmin        = "admin"
)

// AllRoles lists every role auth-service may grant.
var AllRoles = []string{RoleCustomer, RoleOpsOfficer, RoleLoanOfficer, RoleFraudAnalyst, RoleAuditor, RoleAdmin}

// StaffRoles are roles that grant back-office access.
var StaffRoles = []string{RoleOpsOfficer, RoleLoanOfficer, RoleFraudAnalyst, RoleAuditor, RoleAdmin}

// SystemUserID identifies background work done by a service on its own behalf.
const SystemUserID = "system"

// Principal is the authenticated caller of a request.
type Principal struct {
	UserID    string
	Email     string
	Name      string
	Roles     []string
	RequestID string
	ClientIP  string
	// Internal is the name of the calling service for service-to-service calls.
	Internal string
}

func first(md metadata.MD, key string) string {
	if v := md.Get(key); len(v) > 0 {
		return v[0]
	}
	return ""
}

// FromContext reads the principal from incoming gRPC metadata.
func FromContext(ctx context.Context) Principal {
	md, _ := metadata.FromIncomingContext(ctx)
	p := Principal{
		UserID:    first(md, MDUserID),
		Email:     first(md, MDUserEmail),
		Name:      first(md, MDUserName),
		RequestID: first(md, MDRequestID),
		ClientIP:  first(md, MDClientIP),
		Internal:  first(md, MDInternalCaller),
	}
	if r := first(md, MDUserRoles); r != "" {
		p.Roles = strings.Split(r, ",")
	}
	return p
}

// HasRole reports whether the principal holds any of roles. Admin holds every role.
func (p Principal) HasRole(roles ...string) bool {
	if slices.Contains(p.Roles, RoleAdmin) {
		return true
	}
	for _, r := range roles {
		if slices.Contains(p.Roles, r) {
			return true
		}
	}
	return false
}

// IsStaff reports whether the principal has any back-office role.
func (p Principal) IsStaff() bool { return p.HasRole(StaffRoles...) }

// IsSystem reports whether the call is a service acting on its own behalf.
func (p Principal) IsSystem() bool { return p.UserID == SystemUserID && p.Internal != "" }

// RequireUser returns the principal or Unauthenticated when nobody is signed in.
func RequireUser(ctx context.Context) (Principal, error) {
	p := FromContext(ctx)
	if p.UserID == "" {
		return p, status.Error(codes.Unauthenticated, "authentication required")
	}
	return p, nil
}

// RequireRole returns the principal or PermissionDenied when it lacks every role.
func RequireRole(ctx context.Context, roles ...string) (Principal, error) {
	p, err := RequireUser(ctx)
	if err != nil {
		return p, err
	}
	if p.IsSystem() || p.HasRole(roles...) {
		return p, nil
	}
	return p, status.Errorf(codes.PermissionDenied, "requires role: %s", strings.Join(roles, " or "))
}

// CanAccessUser reports whether the principal may act on data owned by ownerID.
func (p Principal) CanAccessUser(ownerID string) bool {
	return p.UserID == ownerID || p.IsStaff() || p.IsSystem()
}

// IdempotencyKey returns the client's Idempotency-Key header, if any.
func IdempotencyKey(ctx context.Context) string {
	md, _ := metadata.FromIncomingContext(ctx)
	return first(md, MDIdempotencyKey)
}

// SystemContext returns a context for background work, carrying the system identity.
func SystemContext(ctx context.Context) context.Context {
	return metadata.NewIncomingContext(ctx, metadata.Pairs(MDUserID, SystemUserID, MDUserRoles, RoleAdmin))
}
