// customer-service owns customer profiles and CIF numbers.
package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	customerv1 "github.com/dso202/bank-platform/gen/go/bank/customer/v1"
	"github.com/dso202/bank-platform/pkg/authz"
	"github.com/dso202/bank-platform/pkg/clients"
	"github.com/dso202/bank-platform/pkg/db"
	"github.com/dso202/bank-platform/pkg/events"
	"github.com/dso202/bank-platform/pkg/server"
	"github.com/dso202/bank-platform/pkg/util"
)

const svcName = "customer-service"

// Customer is a retail customer profile.
type Customer struct {
	ID          string `gorm:"primaryKey;type:uuid"`
	UserID      string `gorm:"type:uuid;uniqueIndex;not null"`
	CIF         string `gorm:"uniqueIndex;not null"`
	FullName    string `gorm:"not null;index"`
	Email       string `gorm:"index"`
	Phone       string
	DateOfBirth string
	Address     string
	City        string
	Occupation  string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type customerServer struct {
	customerv1.UnimplementedCustomerServiceServer
	db *gorm.DB
}

func main() {
	cfg := server.Init(svcName)
	gdb := db.MustOpen(cfg, &Customer{})
	if cfg.MigrateOnly {
		return
	}
	cl := clients.New(svcName)
	defer cl.Close()
	svc := &customerServer{db: gdb}
	server.Run(server.Service{
		Config:   cfg,
		Register: func(s *grpc.Server) { customerv1.RegisterCustomerServiceServer(s, svc) },
		Workers:  []server.Worker{events.Dispatcher(gdb, cl).Run},
	})
}

func toProto(c *Customer) *customerv1.Customer {
	return &customerv1.Customer{
		Id: c.ID, UserId: c.UserID, Cif: c.CIF, FullName: c.FullName, Email: c.Email, Phone: c.Phone,
		DateOfBirth: c.DateOfBirth, Address: c.Address, City: c.City, Occupation: c.Occupation,
		CreatedAt: util.TS(c.CreatedAt), UpdatedAt: util.TS(c.UpdatedAt),
	}
}

func validDOB(dob string) error {
	t, err := util.ParseDate(dob)
	if err != nil {
		return util.Invalid("date_of_birth must be YYYY-MM-DD")
	}
	if age := time.Since(t).Hours() / 24 / 365.25; age < 18 || age > 120 {
		return util.Invalid("customer must be at least 18 years old")
	}
	return nil
}

func (s *customerServer) CreateProfile(ctx context.Context, req *customerv1.CreateProfileRequest) (*customerv1.Customer, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.FullName)
	if name == "" {
		name = p.Name
	}
	if name == "" {
		return nil, util.Invalid("full_name is required")
	}
	if err := validDOB(req.DateOfBirth); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Address) == "" || strings.TrimSpace(req.City) == "" {
		return nil, util.Invalid("address and city are required")
	}
	c := Customer{
		ID: uuid.NewString(), UserID: p.UserID, CIF: "CIF" + util.RandomDigits(9), FullName: name, Email: p.Email,
		Phone: strings.TrimSpace(req.Phone), DateOfBirth: req.DateOfBirth, Address: strings.TrimSpace(req.Address),
		City: strings.TrimSpace(req.City), Occupation: strings.TrimSpace(req.Occupation),
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&c).Error; err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "customer.create", "customer", c.ID, "SUCCESS", nil, map[string]any{"cif": c.CIF})
	})
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return nil, status.Error(codes.AlreadyExists, "profile already exists")
	}
	if err != nil {
		return nil, util.Internal(err)
	}
	return toProto(&c), nil
}

func (s *customerServer) byUser(ctx context.Context, userID string) (*Customer, error) {
	var c Customer
	if err := s.db.WithContext(ctx).First(&c, "user_id = ?", userID).Error; err != nil {
		return nil, util.NotFound("customer profile")
	}
	return &c, nil
}

func (s *customerServer) GetMyProfile(ctx context.Context, _ *customerv1.GetMyProfileRequest) (*customerv1.Customer, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.byUser(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	return toProto(c), nil
}

func (s *customerServer) UpdateMyProfile(ctx context.Context, req *customerv1.UpdateMyProfileRequest) (*customerv1.Customer, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.byUser(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	before := *c
	if v := strings.TrimSpace(req.Phone); v != "" {
		c.Phone = v
	}
	if v := strings.TrimSpace(req.Address); v != "" {
		c.Address = v
	}
	if v := strings.TrimSpace(req.City); v != "" {
		c.City = v
	}
	if v := strings.TrimSpace(req.Occupation); v != "" {
		c.Occupation = v
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(c).Error; err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "customer.update", "customer", c.ID, "SUCCESS",
			map[string]any{"phone": before.Phone, "address": before.Address, "city": before.City, "occupation": before.Occupation},
			map[string]any{"phone": c.Phone, "address": c.Address, "city": c.City, "occupation": c.Occupation})
	})
	if err != nil {
		return nil, util.Internal(err)
	}
	return toProto(c), nil
}

func (s *customerServer) SearchCustomers(ctx context.Context, req *customerv1.SearchCustomersRequest) (*customerv1.SearchCustomersResponse, error) {
	if _, err := authz.RequireRole(ctx, authz.StaffRoles...); err != nil {
		return nil, err
	}
	limit, offset := util.Page(req.PageSize, req.PageToken)
	q := s.db.WithContext(ctx).Model(&Customer{})
	if v := strings.TrimSpace(req.Query); v != "" {
		like := "%" + strings.ToLower(v) + "%"
		q = q.Where("LOWER(full_name) LIKE ? OR LOWER(email) LIKE ? OR LOWER(cif) LIKE ? OR phone LIKE ?", like, like, like, like)
	}
	var cs []Customer
	if err := q.Order("created_at DESC").Limit(limit).Offset(offset).Find(&cs).Error; err != nil {
		return nil, util.Internal(err)
	}
	out := &customerv1.SearchCustomersResponse{NextPageToken: util.NextToken(len(cs), limit, offset)}
	for i := range cs {
		out.Customers = append(out.Customers, toProto(&cs[i]))
	}
	return out, nil
}

func (s *customerServer) GetCustomer(ctx context.Context, req *customerv1.GetCustomerRequest) (*customerv1.Customer, error) {
	if _, err := authz.RequireRole(ctx, authz.StaffRoles...); err != nil {
		return nil, err
	}
	var c Customer
	if err := s.db.WithContext(ctx).First(&c, "id = ?", req.CustomerId).Error; err != nil {
		return nil, util.NotFound("customer")
	}
	return toProto(&c), nil
}

func (s *customerServer) GetCustomerByUser(ctx context.Context, req *customerv1.GetCustomerByUserRequest) (*customerv1.Customer, error) {
	c, err := s.byUser(ctx, req.UserId)
	if err != nil {
		return nil, err
	}
	return toProto(c), nil
}
