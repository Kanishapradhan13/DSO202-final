// kyc-service captures identity documents, runs automated checks against a
// mocked ID-verification provider and records officer decisions.
package main

import (
	"context"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	customerv1 "github.com/dso202/bank-platform/gen/go/bank/customer/v1"
	kycv1 "github.com/dso202/bank-platform/gen/go/bank/kyc/v1"
	"github.com/dso202/bank-platform/pkg/authz"
	"github.com/dso202/bank-platform/pkg/clients"
	"github.com/dso202/bank-platform/pkg/db"
	"github.com/dso202/bank-platform/pkg/events"
	"github.com/dso202/bank-platform/pkg/server"
	"github.com/dso202/bank-platform/pkg/util"
)

const svcName = "kyc-service"

// KYC statuses.
const (
	NotSubmitted = "NOT_SUBMITTED"
	Pending      = "PENDING"
	Approved     = "APPROVED"
	Rejected     = "REJECTED"
)

const maxDocBytes = 2 << 20

// Case is one KYC submission. A customer may resubmit after rejection.
type Case struct {
	ID             string `gorm:"primaryKey;type:uuid"`
	UserID         string `gorm:"type:uuid;index;not null"`
	CustomerID     string
	FullName       string
	DocumentType   string
	DocumentNumber string
	DateOfBirth    string
	Status         string `gorm:"index;not null"`
	RiskTier       string
	AutoChecks     string // newline-separated
	ReviewerID     string
	ReviewNote     string
	DocumentImage  []byte
	DocumentMime   string
	SubmittedAt    time.Time `gorm:"index"`
	ReviewedAt     *time.Time
}

func (Case) TableName() string { return "kyc_cases" }

type kycServer struct {
	kycv1.UnimplementedKycServiceServer
	db *gorm.DB
	cl *clients.Clients
}

func main() {
	cfg := server.Init(svcName)
	gdb := db.MustOpen(cfg, &Case{})
	if cfg.MigrateOnly {
		return
	}
	cl := clients.New(svcName)
	defer cl.Close()
	svc := &kycServer{db: gdb, cl: cl}
	server.Run(server.Service{
		Config:   cfg,
		Register: func(s *grpc.Server) { kycv1.RegisterKycServiceServer(s, svc) },
		Workers:  []server.Worker{events.Dispatcher(gdb, cl).Run},
	})
}

func toProto(c *Case, withImage bool) *kycv1.KycCase {
	out := &kycv1.KycCase{
		Id: c.ID, UserId: c.UserID, CustomerId: c.CustomerID, FullName: c.FullName, DocumentType: c.DocumentType,
		DocumentNumber: c.DocumentNumber, DateOfBirth: c.DateOfBirth, Status: c.Status, RiskTier: c.RiskTier,
		ReviewerId: c.ReviewerID, ReviewNote: c.ReviewNote, HasDocument: len(c.DocumentImage) > 0,
		DocumentMime: c.DocumentMime, SubmittedAt: util.TS(c.SubmittedAt), ReviewedAt: util.TSP(c.ReviewedAt),
	}
	if c.AutoChecks != "" {
		out.AutoChecks = strings.Split(c.AutoChecks, "\n")
	}
	if withImage {
		out.DocumentImage = c.DocumentImage
	} else {
		out.DocumentNumber = util.MaskTail(c.DocumentNumber, 4)
	}
	return out
}

var (
	cidRe      = regexp.MustCompile(`^\d{11}$`)
	passportRe = regexp.MustCompile(`^[A-Z0-9]{6,9}$`)
)

// autoCheck mocks an external ID-verification provider: format checks, date of
// birth match and a simple risk tier.
func autoCheck(docType, docNumber, dob, profileDOB string, hasImage bool) (checks []string, tier string) {
	tier = "LOW"
	switch docType {
	case "CID":
		if cidRe.MatchString(docNumber) {
			checks = append(checks, "PASS: CID number format (11 digits)")
		} else {
			checks = append(checks, "FAIL: CID number must be 11 digits")
			tier = "HIGH"
		}
	case "PASSPORT", "WORK_PERMIT":
		if passportRe.MatchString(docNumber) {
			checks = append(checks, "PASS: document number format")
		} else {
			checks = append(checks, "FAIL: document number format")
			tier = "HIGH"
		}
		if tier == "LOW" {
			tier = "MEDIUM"
			checks = append(checks, "INFO: foreign document, enhanced due diligence")
		}
	}
	if profileDOB != "" && dob == profileDOB {
		checks = append(checks, "PASS: date of birth matches profile")
	} else {
		checks = append(checks, "WARN: date of birth does not match profile")
		if tier == "LOW" {
			tier = "MEDIUM"
		}
	}
	if hasImage {
		checks = append(checks, "PASS: document image provided")
	} else {
		checks = append(checks, "WARN: no document image")
	}
	checks = append(checks, "PASS: sanctions/PEP screening (mock provider): no match")
	return checks, tier
}

func (s *kycServer) latest(ctx context.Context, userID string) (*Case, error) {
	var c Case
	err := s.db.WithContext(ctx).Where("user_id = ?", userID).Order("submitted_at DESC").First(&c).Error
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *kycServer) SubmitKyc(ctx context.Context, req *kycv1.SubmitKycRequest) (*kycv1.KycCase, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	docType := strings.ToUpper(strings.TrimSpace(req.DocumentType))
	if docType != "CID" && docType != "PASSPORT" && docType != "WORK_PERMIT" {
		return nil, util.Invalid("document_type must be CID, PASSPORT or WORK_PERMIT")
	}
	docNumber := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(req.DocumentNumber), " ", ""))
	if docNumber == "" {
		return nil, util.Invalid("document_number is required")
	}
	if _, err := util.ParseDate(req.DateOfBirth); err != nil {
		return nil, util.Invalid("date_of_birth must be YYYY-MM-DD")
	}
	if len(req.DocumentImage) > maxDocBytes {
		return nil, util.Invalid("document image must be under 2 MB")
	}
	if prev, err := s.latest(ctx, p.UserID); err == nil && (prev.Status == Pending || prev.Status == Approved) {
		return nil, util.Precondition("KYC is already %s", strings.ToLower(prev.Status))
	}
	cust, err := s.cl.Customer().GetCustomerByUser(ctx, &customerv1.GetCustomerByUserRequest{UserId: p.UserID})
	if err != nil {
		return nil, util.Precondition("create your customer profile before submitting KYC")
	}
	checks, tier := autoCheck(docType, docNumber, req.DateOfBirth, cust.DateOfBirth, len(req.DocumentImage) > 0)
	c := Case{
		ID: uuid.NewString(), UserID: p.UserID, CustomerID: cust.Id, FullName: cust.FullName, DocumentType: docType,
		DocumentNumber: docNumber, DateOfBirth: req.DateOfBirth, Status: Pending, RiskTier: tier,
		AutoChecks: strings.Join(checks, "\n"), DocumentImage: req.DocumentImage, DocumentMime: req.DocumentMime,
		SubmittedAt: time.Now().UTC(),
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&c).Error; err != nil {
			return err
		}
		if err := events.Notify(tx, p.UserID, "KYC_SUBMITTED", nil); err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "kyc.submit", "kyc_case", c.ID, "SUCCESS", nil,
			map[string]any{"document_type": docType, "risk_tier": tier})
	})
	if err != nil {
		return nil, util.Internal(err)
	}
	return toProto(&c, false), nil
}

func (s *kycServer) GetMyKyc(ctx context.Context, _ *kycv1.GetMyKycRequest) (*kycv1.KycCase, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.latest(ctx, p.UserID)
	if err != nil {
		return &kycv1.KycCase{UserId: p.UserID, Status: NotSubmitted}, nil
	}
	return toProto(c, false), nil
}

func (s *kycServer) GetKycStatus(ctx context.Context, req *kycv1.GetKycStatusRequest) (*kycv1.KycCase, error) {
	c, err := s.latest(ctx, req.UserId)
	if err != nil {
		return &kycv1.KycCase{UserId: req.UserId, Status: NotSubmitted}, nil
	}
	return toProto(c, false), nil
}

func (s *kycServer) ListKycCases(ctx context.Context, req *kycv1.ListKycCasesRequest) (*kycv1.ListKycCasesResponse, error) {
	if _, err := authz.RequireRole(ctx, authz.RoleOpsOfficer, authz.RoleLoanOfficer, authz.RoleAuditor); err != nil {
		return nil, err
	}
	limit, offset := util.Page(req.PageSize, req.PageToken)
	q := s.db.WithContext(ctx).Model(&Case{}).Omit("document_image")
	if req.Status != "" {
		q = q.Where("status = ?", req.Status)
	}
	var cs []Case
	if err := q.Order("submitted_at DESC").Limit(limit).Offset(offset).Find(&cs).Error; err != nil {
		return nil, util.Internal(err)
	}
	out := &kycv1.ListKycCasesResponse{NextPageToken: util.NextToken(len(cs), limit, offset)}
	for i := range cs {
		out.Cases = append(out.Cases, toProto(&cs[i], false))
	}
	return out, nil
}

func (s *kycServer) GetKycCase(ctx context.Context, req *kycv1.GetKycCaseRequest) (*kycv1.KycCase, error) {
	if _, err := authz.RequireRole(ctx, authz.RoleOpsOfficer, authz.RoleLoanOfficer, authz.RoleAuditor); err != nil {
		return nil, err
	}
	var c Case
	if err := s.db.WithContext(ctx).First(&c, "id = ?", req.CaseId).Error; err != nil {
		return nil, util.NotFound("KYC case")
	}
	return toProto(&c, true), nil
}

func (s *kycServer) ReviewKyc(ctx context.Context, req *kycv1.ReviewKycRequest) (*kycv1.KycCase, error) {
	p, err := authz.RequireRole(ctx, authz.RoleOpsOfficer)
	if err != nil {
		return nil, err
	}
	var c Case
	if err := s.db.WithContext(ctx).First(&c, "id = ?", req.CaseId).Error; err != nil {
		return nil, util.NotFound("KYC case")
	}
	if c.Status != Pending {
		return nil, util.Precondition("case is already %s", strings.ToLower(c.Status))
	}
	if c.UserID == p.UserID {
		return nil, util.Denied("you cannot review your own KYC")
	}
	if !req.Approve && strings.TrimSpace(req.Note) == "" {
		return nil, util.Invalid("a note is required when rejecting")
	}
	now := time.Now().UTC()
	c.Status, c.ReviewerID, c.ReviewNote, c.ReviewedAt = Rejected, p.UserID, strings.TrimSpace(req.Note), &now
	tmpl := "KYC_REJECTED"
	if req.Approve {
		c.Status, tmpl = Approved, "KYC_APPROVED"
	}
	if t := strings.ToUpper(req.RiskTier); t == "LOW" || t == "MEDIUM" || t == "HIGH" {
		c.RiskTier = t
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&Case{}).Where("id = ? AND status = ?", c.ID, Pending).Updates(map[string]any{
			"status": c.Status, "reviewer_id": c.ReviewerID, "review_note": c.ReviewNote, "reviewed_at": now, "risk_tier": c.RiskTier,
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return util.Precondition("case was reviewed by someone else")
		}
		if err := events.Notify(tx, c.UserID, tmpl, map[string]string{"note": c.ReviewNote}); err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "kyc.review", "kyc_case", c.ID, "SUCCESS",
			map[string]any{"status": Pending}, map[string]any{"status": c.Status, "risk_tier": c.RiskTier, "note": c.ReviewNote})
	})
	if err != nil {
		return nil, err
	}
	return toProto(&c, false), nil
}
