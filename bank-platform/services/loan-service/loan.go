package main

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	accountv1 "github.com/dso202/bank-platform/gen/go/bank/account/v1"
	ledgerv1 "github.com/dso202/bank-platform/gen/go/bank/ledger/v1"
	loanv1 "github.com/dso202/bank-platform/gen/go/bank/loan/v1"
	transactionv1 "github.com/dso202/bank-platform/gen/go/bank/transaction/v1"
	"github.com/dso202/bank-platform/pkg/authz"
	"github.com/dso202/bank-platform/pkg/clients"
	"github.com/dso202/bank-platform/pkg/events"
	"github.com/dso202/bank-platform/pkg/ledgerx"
	"github.com/dso202/bank-platform/pkg/util"
)

const svcName = "loan-service"

// Loan statuses.
const (
	Applied  = "APPLIED"
	Rejected = "REJECTED"
	Active   = "ACTIVE"
	Closed   = "CLOSED"
)

var loansTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "bank_loans_total", Help: "Loan lifecycle events.",
}, []string{"event", "product"})

// Product is a loan product.
type Product struct {
	ID            string `gorm:"primaryKey"`
	Name          string
	AnnualRateBps int32
	MinAmount     int64
	MaxAmount     int64
	MinTerm       int32
	MaxTerm       int32
	Description   string
}

// Loan is one loan application and, once disbursed, the loan account.
type Loan struct {
	ID                        string `gorm:"primaryKey;type:uuid"`
	UserID                    string `gorm:"type:uuid;index;not null"`
	ApplicantName             string
	ProductID                 string `gorm:"not null"`
	ProductName               string
	Principal                 int64
	AnnualRateBps             int32
	TermMonths                int32
	EMI                       int64
	TotalInterest             int64
	MonthlyIncome             int64
	Status                    string `gorm:"index;not null"`
	DisbursementAccountID     string `gorm:"type:uuid"`
	DisbursementAccountNumber string
	Purpose                   string
	ReviewerID                string
	ReviewerNote              string
	LedgerEntryID             string
	Instalments               []Instalment `gorm:"foreignKey:LoanID"`
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
	DisbursedAt               *time.Time
}

// Instalment is one EMI in a repayment schedule.
type Instalment struct {
	ID           uint64 `gorm:"primaryKey;autoIncrement"`
	LoanID       string `gorm:"type:uuid;not null;uniqueIndex:idx_inst_loan_num"`
	Number       int32  `gorm:"not null;uniqueIndex:idx_inst_loan_num"`
	DueDate      time.Time
	Principal    int64
	Interest     int64
	Amount       int64
	BalanceAfter int64
	Status       string
	LedgerEntryID string
	PaidAt       *time.Time
}

type loanServer struct {
	loanv1.UnimplementedLoanServiceServer
	db *gorm.DB
	cl *clients.Clients
}

// amortize builds an equal-instalment schedule in minor units. Rounding
// differences are absorbed by the final instalment so principal repaid equals
// principal lent exactly.
func amortize(principal int64, rateBps int32, months int32, start time.Time) (emi int64, totalInterest int64, sched []Instalment) {
	r := float64(rateBps) / 10000 / 12
	n := float64(months)
	if r == 0 {
		emi = int64(math.Ceil(float64(principal) / n))
	} else {
		f := math.Pow(1+r, n)
		emi = int64(math.Round(float64(principal) * r * f / (f - 1)))
	}
	bal := principal
	for i := int32(1); i <= months; i++ {
		interest := int64(math.Round(float64(bal) * r))
		pr := emi - interest
		if i == months || pr > bal {
			pr = bal
		}
		bal -= pr
		totalInterest += interest
		sched = append(sched, Instalment{
			Number: i, DueDate: start.AddDate(0, int(i), 0), Principal: pr, Interest: interest,
			Amount: pr + interest, BalanceAfter: bal, Status: "DUE",
		})
	}
	return emi, totalInterest, sched
}

func instProto(in *Instalment) *loanv1.Instalment {
	return &loanv1.Instalment{
		Number: in.Number, DueDate: in.DueDate.Format("2006-01-02"), Principal: in.Principal, Interest: in.Interest,
		Amount: in.Amount, BalanceAfter: in.BalanceAfter, Status: in.Status, PaidAt: util.TSP(in.PaidAt),
	}
}

func toProto(l *Loan, withSchedule bool) *loanv1.Loan {
	out := &loanv1.Loan{
		Id: l.ID, UserId: l.UserID, ApplicantName: l.ApplicantName, ProductId: l.ProductID, ProductName: l.ProductName,
		Principal: l.Principal, AnnualRateBps: l.AnnualRateBps, TermMonths: l.TermMonths, Emi: l.EMI, TotalInterest: l.TotalInterest,
		Status: l.Status, DisbursementAccountId: l.DisbursementAccountID, DisbursementAccountNumber: l.DisbursementAccountNumber,
		Purpose: l.Purpose, ReviewerNote: l.ReviewerNote, LedgerEntryId: l.LedgerEntryID, MonthlyIncome: l.MonthlyIncome,
		CreatedAt: util.TS(l.CreatedAt), DisbursedAt: util.TSP(l.DisbursedAt), OutstandingPrincipal: l.Principal,
	}
	if l.Status == Applied || l.Status == Rejected {
		out.OutstandingPrincipal = 0
	}
	for i := range l.Instalments {
		in := &l.Instalments[i]
		if in.Status == "PAID" {
			out.PaidInstalments++
			out.OutstandingPrincipal = in.BalanceAfter
		} else if out.NextDueDate == "" && l.Status == Active {
			out.NextDueDate, out.NextDueAmount = in.DueDate.Format("2006-01-02"), in.Amount
		}
		if withSchedule {
			out.Schedule = append(out.Schedule, instProto(in))
		}
	}
	if l.Status == Closed {
		out.OutstandingPrincipal = 0
	}
	return out
}

func (s *loanServer) ListProducts(ctx context.Context, _ *loanv1.ListProductsRequest) (*loanv1.ListProductsResponse, error) {
	var ps []Product
	s.db.WithContext(ctx).Order("annual_rate_bps").Find(&ps)
	out := &loanv1.ListProductsResponse{}
	for _, p := range ps {
		out.Products = append(out.Products, &loanv1.LoanProduct{
			Id: p.ID, Name: p.Name, AnnualRateBps: p.AnnualRateBps, MinAmount: p.MinAmount, MaxAmount: p.MaxAmount,
			MinTermMonths: p.MinTerm, MaxTermMonths: p.MaxTerm, Description: p.Description,
		})
	}
	return out, nil
}

func (s *loanServer) product(ctx context.Context, id string, amount int64, term int32) (*Product, error) {
	var p Product
	if err := s.db.WithContext(ctx).First(&p, "id = ?", id).Error; err != nil {
		return nil, util.NotFound("loan product")
	}
	if amount < p.MinAmount || amount > p.MaxAmount {
		return nil, util.Invalid("%s amount must be between %s and %s", p.Name, util.Nu(p.MinAmount), util.Nu(p.MaxAmount))
	}
	if term < p.MinTerm || term > p.MaxTerm {
		return nil, util.Invalid("%s term must be %d to %d months", p.Name, p.MinTerm, p.MaxTerm)
	}
	return &p, nil
}

func (s *loanServer) QuoteLoan(ctx context.Context, req *loanv1.QuoteLoanRequest) (*loanv1.LoanQuote, error) {
	p, err := s.product(ctx, req.ProductId, req.Amount, req.TermMonths)
	if err != nil {
		return nil, err
	}
	emi, interest, sched := amortize(req.Amount, p.AnnualRateBps, req.TermMonths, time.Now().UTC())
	out := &loanv1.LoanQuote{Emi: emi, TotalInterest: interest, TotalPayable: req.Amount + interest, AnnualRateBps: p.AnnualRateBps}
	for i := range sched {
		out.Schedule = append(out.Schedule, instProto(&sched[i]))
	}
	return out, nil
}

func (s *loanServer) ApplyLoan(ctx context.Context, req *loanv1.ApplyLoanRequest) (*loanv1.Loan, error) {
	pr, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	p, err := s.product(ctx, req.ProductId, req.Amount, req.TermMonths)
	if err != nil {
		return nil, err
	}
	acc, err := s.cl.Account().ValidateAccount(ctx, &accountv1.ValidateAccountRequest{AccountId: req.DisbursementAccountId, OwnerUserId: pr.UserID})
	if err != nil {
		return nil, err
	}
	var open int64
	s.db.WithContext(ctx).Model(&Loan{}).Where("user_id = ? AND status IN ?", pr.UserID, []string{Applied, Active}).Count(&open)
	if open >= 3 {
		return nil, util.Precondition("you can have at most 3 open loans or applications")
	}
	if req.MonthlyIncome <= 0 {
		return nil, util.Invalid("monthly income is required")
	}
	emi, interest, _ := amortize(req.Amount, p.AnnualRateBps, req.TermMonths, time.Now().UTC())
	l := Loan{
		ID: uuid.NewString(), UserID: pr.UserID, ApplicantName: acc.HolderName, ProductID: p.ID, ProductName: p.Name,
		Principal: req.Amount, AnnualRateBps: p.AnnualRateBps, TermMonths: req.TermMonths, EMI: emi, TotalInterest: interest,
		MonthlyIncome: req.MonthlyIncome, Status: Applied, DisbursementAccountID: acc.Id, DisbursementAccountNumber: acc.AccountNumber,
		Purpose: strings.TrimSpace(req.Purpose),
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&l).Error; err != nil {
			return err
		}
		if err := events.Notify(tx, pr.UserID, "LOAN_APPLIED", map[string]string{"product": p.Name, "amount": util.Nu(req.Amount)}); err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "loan.apply", "loan", l.ID, "SUCCESS", nil,
			map[string]any{"product": p.ID, "amount": req.Amount, "term": req.TermMonths, "emi": emi})
	})
	if err != nil {
		return nil, util.Internal(err)
	}
	loansTotal.WithLabelValues("applied", p.ID).Inc()
	return toProto(&l, false), nil
}

func (s *loanServer) load(ctx context.Context, id string) (*Loan, error) {
	var l Loan
	if _, err := uuid.Parse(id); err != nil {
		return nil, util.NotFound("loan")
	}
	err := s.db.WithContext(ctx).Preload("Instalments", func(db *gorm.DB) *gorm.DB { return db.Order("number") }).First(&l, "id = ?", id).Error
	if err != nil {
		return nil, util.NotFound("loan")
	}
	return &l, nil
}

func (s *loanServer) ListMyLoans(ctx context.Context, _ *loanv1.ListMyLoansRequest) (*loanv1.ListLoansResponse, error) {
	pr, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	var ls []Loan
	s.db.WithContext(ctx).Preload("Instalments", func(db *gorm.DB) *gorm.DB { return db.Order("number") }).
		Where("user_id = ?", pr.UserID).Order("created_at DESC").Find(&ls)
	out := &loanv1.ListLoansResponse{}
	for i := range ls {
		out.Loans = append(out.Loans, toProto(&ls[i], false))
	}
	return out, nil
}

func (s *loanServer) GetLoan(ctx context.Context, req *loanv1.GetLoanRequest) (*loanv1.Loan, error) {
	pr, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	l, err := s.load(ctx, req.LoanId)
	if err != nil || !pr.CanAccessUser(l.UserID) {
		return nil, util.NotFound("loan")
	}
	return toProto(l, true), nil
}

func (s *loanServer) ListApplications(ctx context.Context, req *loanv1.ListApplicationsRequest) (*loanv1.ListLoansResponse, error) {
	if _, err := authz.RequireRole(ctx, authz.RoleLoanOfficer, authz.RoleAuditor); err != nil {
		return nil, err
	}
	limit, offset := util.Page(req.PageSize, req.PageToken)
	q := s.db.WithContext(ctx).Preload("Instalments", func(db *gorm.DB) *gorm.DB { return db.Order("number") })
	if req.Status != "" {
		q = q.Where("status = ?", req.Status)
	}
	var ls []Loan
	q.Order("created_at DESC").Limit(limit).Offset(offset).Find(&ls)
	out := &loanv1.ListLoansResponse{NextPageToken: util.NextToken(len(ls), limit, offset)}
	for i := range ls {
		out.Loans = append(out.Loans, toProto(&ls[i], false))
	}
	return out, nil
}

// ReviewLoan approves (and disburses) or rejects an application.
func (s *loanServer) ReviewLoan(ctx context.Context, req *loanv1.ReviewLoanRequest) (*loanv1.Loan, error) {
	pr, err := authz.RequireRole(ctx, authz.RoleLoanOfficer)
	if err != nil {
		return nil, err
	}
	l, err := s.load(ctx, req.LoanId)
	if err != nil {
		return nil, err
	}
	if l.Status != Applied {
		return nil, util.Precondition("loan is already %s", strings.ToLower(l.Status))
	}
	if l.UserID == pr.UserID {
		return nil, util.Denied("you cannot review your own loan")
	}
	note := strings.TrimSpace(req.Note)
	if !req.Approve {
		if note == "" {
			return nil, util.Invalid("a note is required when rejecting")
		}
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			res := tx.Model(&Loan{}).Where("id = ? AND status = ?", l.ID, Applied).
				Updates(map[string]any{"status": Rejected, "reviewer_id": pr.UserID, "reviewer_note": note})
			if res.RowsAffected == 0 {
				return util.Precondition("loan was reviewed by someone else")
			}
			if err := events.Notify(tx, l.UserID, "LOAN_REJECTED", map[string]string{"product": l.ProductName, "note": note}); err != nil {
				return err
			}
			return events.Audit(ctx, tx, svcName, "loan.reject", "loan", l.ID, "SUCCESS", map[string]any{"status": Applied}, map[string]any{"status": Rejected, "note": note})
		})
		if err != nil {
			return nil, err
		}
		loansTotal.WithLabelValues("rejected", l.ProductID).Inc()
		l.Status, l.ReviewerNote = Rejected, note
		return toProto(l, false), nil
	}
	// Approve: disburse into the customer's account, then build the schedule.
	if _, err := s.cl.Account().ValidateAccount(ctx, &accountv1.ValidateAccountRequest{AccountId: l.DisbursementAccountID}); err != nil {
		return nil, err
	}
	entry, err := s.cl.Ledger().PostEntries(ctx, &ledgerv1.PostEntriesRequest{
		IdempotencyKey: "loan-disburse:" + l.ID, Description: l.ProductName + " disbursement",
		ReferenceType: "LOAN_DISBURSEMENT", ReferenceId: l.ID, Postings: ledgerx.Move(ledgerx.LoanPortfolio, l.DisbursementAccountID, l.Principal),
	})
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	emi, interest, sched := amortize(l.Principal, l.AnnualRateBps, l.TermMonths, now)
	for i := range sched {
		sched[i].LoanID = l.ID
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&Loan{}).Where("id = ? AND status = ?", l.ID, Applied).Updates(map[string]any{
			"status": Active, "reviewer_id": pr.UserID, "reviewer_note": note, "ledger_entry_id": entry.Id,
			"disbursed_at": now, "emi": emi, "total_interest": interest,
		})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return util.Precondition("loan was reviewed by someone else")
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&sched).Error; err != nil {
			return err
		}
		if err := events.RecordTxn(tx, &transactionv1.RecordTransactionRequest{
			AccountId: l.DisbursementAccountID, AccountNumber: l.DisbursementAccountNumber, UserId: l.UserID, Type: "LOAN_DISBURSEMENT",
			Direction: ledgerx.Credit, Amount: l.Principal, BalanceAfter: ledgerx.BalanceAfter(entry, l.DisbursementAccountID),
			Counterparty: l.ProductName, Description: "Loan disbursement", ReferenceType: "LOAN_DISBURSEMENT", ReferenceId: l.ID, LedgerEntryId: entry.Id,
		}); err != nil {
			return err
		}
		if err := events.Notify(tx, l.UserID, "LOAN_APPROVED", map[string]string{
			"product": l.ProductName, "amount": util.Nu(l.Principal), "emi": util.Nu(emi), "account_number": l.DisbursementAccountNumber,
		}); err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "loan.approve_disburse", "loan", l.ID, "SUCCESS", map[string]any{"status": Applied},
			map[string]any{"status": Active, "ledger_entry_id": entry.Id, "principal": l.Principal})
	})
	if err != nil {
		return nil, err
	}
	loansTotal.WithLabelValues("disbursed", l.ProductID).Inc()
	return s.GetLoan(ctx, &loanv1.GetLoanRequest{LoanId: l.ID})
}

// RepayLoan pays the next due instalment. The ledger idempotency key is
// per-instalment, so a double submit can never pay the same EMI twice.
func (s *loanServer) RepayLoan(ctx context.Context, req *loanv1.RepayLoanRequest) (*loanv1.Loan, error) {
	pr, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	l, err := s.load(ctx, req.LoanId)
	if err != nil || l.UserID != pr.UserID {
		return nil, util.NotFound("loan")
	}
	if l.Status != Active {
		return nil, util.Precondition("loan is %s", strings.ToLower(l.Status))
	}
	var next *Instalment
	for i := range l.Instalments {
		if l.Instalments[i].Status != "PAID" {
			next = &l.Instalments[i]
			break
		}
	}
	if next == nil {
		return nil, util.Precondition("nothing left to repay")
	}
	fromID := req.FromAccountId
	if fromID == "" {
		fromID = l.DisbursementAccountID
	}
	acc, err := s.cl.Account().ValidateAccount(ctx, &accountv1.ValidateAccountRequest{AccountId: fromID, OwnerUserId: pr.UserID, ForDebit: true})
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("loan-repay:%s:%d", l.ID, next.Number)
	if _, err := s.cl.Account().PlaceHold(ctx, &accountv1.PlaceHoldRequest{AccountId: acc.Id, Amount: next.Amount, Reference: key}); err != nil {
		return nil, err
	}
	postings := []*ledgerv1.Posting{
		{AccountId: acc.Id, Direction: ledgerx.Debit, Amount: next.Amount},
		{AccountId: ledgerx.LoanPortfolio, Direction: ledgerx.Credit, Amount: next.Principal},
	}
	if next.Interest > 0 {
		postings = append(postings, &ledgerv1.Posting{AccountId: ledgerx.InterestIncome, Direction: ledgerx.Credit, Amount: next.Interest})
	}
	entry, err := s.cl.Ledger().PostEntries(ctx, &ledgerv1.PostEntriesRequest{
		IdempotencyKey: key, Description: fmt.Sprintf("%s EMI %d/%d", l.ProductName, next.Number, l.TermMonths),
		ReferenceType: "LOAN_REPAYMENT", ReferenceId: fmt.Sprintf("%s:%d", l.ID, next.Number), Postings: postings,
	})
	_, _ = s.cl.Account().ReleaseHold(ctx, &accountv1.ReleaseHoldRequest{Reference: key})
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	last := int(next.Number) == len(l.Instalments)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&Instalment{}).Where("id = ? AND status <> ?", next.ID, "PAID").
			Updates(map[string]any{"status": "PAID", "paid_at": now, "ledger_entry_id": entry.Id})
		if res.RowsAffected == 0 {
			return nil // already recorded by a concurrent request
		}
		if last {
			tx.Model(&Loan{}).Where("id = ?", l.ID).Update("status", Closed)
		}
		if err := events.RecordTxn(tx, &transactionv1.RecordTransactionRequest{
			AccountId: acc.Id, AccountNumber: acc.AccountNumber, UserId: l.UserID, Type: "LOAN_REPAYMENT", Direction: ledgerx.Debit,
			Amount: next.Amount, BalanceAfter: ledgerx.BalanceAfter(entry, acc.Id), Counterparty: l.ProductName,
			Description: fmt.Sprintf("EMI %d of %d", next.Number, l.TermMonths), ReferenceType: "LOAN_REPAYMENT",
			ReferenceId: fmt.Sprintf("%s:%d", l.ID, next.Number), LedgerEntryId: entry.Id,
		}); err != nil {
			return err
		}
		tmpl := "LOAN_REPAYMENT"
		if last {
			tmpl = "LOAN_CLOSED"
		}
		if err := events.Notify(tx, l.UserID, tmpl, map[string]string{
			"product": l.ProductName, "amount": util.Nu(next.Amount), "number": fmt.Sprint(next.Number), "term": fmt.Sprint(l.TermMonths),
		}); err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "loan.repay", "loan", l.ID, "SUCCESS", nil,
			map[string]any{"instalment": next.Number, "amount": next.Amount, "ledger_entry_id": entry.Id})
	})
	if err != nil {
		return nil, util.Internal(err)
	}
	if last {
		loansTotal.WithLabelValues("closed", l.ProductID).Inc()
	}
	return s.GetLoan(ctx, &loanv1.GetLoanRequest{LoanId: l.ID})
}

// dueReminders notifies customers about instalments due within three days, once a day.
func (s *loanServer) dueReminders(ctx context.Context) {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			type row struct {
				LoanID, UserID, ProductName string
				Number                      int32
				Amount                      int64
				DueDate                     time.Time
			}
			var rows []row
			s.db.WithContext(ctx).Table("instalments").
				Select("instalments.loan_id, loans.user_id, loans.product_name, instalments.number, instalments.amount, instalments.due_date").
				Joins("JOIN loans ON loans.id = instalments.loan_id").
				Where("loans.status = ? AND instalments.status = ? AND instalments.due_date BETWEEN ? AND ?",
					Active, "DUE", time.Now().UTC(), time.Now().UTC().Add(72*time.Hour)).Scan(&rows)
			for _, r := range rows {
				_ = s.db.Transaction(func(tx *gorm.DB) error {
					return events.Notify(tx, r.UserID, "LOAN_DUE", map[string]string{
						"product": r.ProductName, "amount": util.Nu(r.Amount), "due": r.DueDate.Format("2 Jan 2006"),
					})
				})
			}
		}
	}
}

