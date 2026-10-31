package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"
	"github.com/google/uuid"
	"google.golang.org/genproto/googleapis/api/httpbody"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	accountv1 "github.com/dso202/bank-platform/gen/go/bank/account/v1"
	ledgerv1 "github.com/dso202/bank-platform/gen/go/bank/ledger/v1"
	statementv1 "github.com/dso202/bank-platform/gen/go/bank/statement/v1"
	"github.com/dso202/bank-platform/pkg/authz"
	"github.com/dso202/bank-platform/pkg/clients"
	"github.com/dso202/bank-platform/pkg/events"
	"github.com/dso202/bank-platform/pkg/ledgerx"
	"github.com/dso202/bank-platform/pkg/util"
)

const svcName = "statement-service"

// Statement is a generated statement with its rendered files.
type Statement struct {
	ID             string `gorm:"primaryKey;type:uuid"`
	AccountID      string `gorm:"type:uuid;index;not null"`
	AccountNumber  string
	UserID         string `gorm:"type:uuid;index"`
	HolderName     string
	PeriodFrom     string
	PeriodTo       string
	OpeningBalance int64
	ClosingBalance int64
	TotalCredits   int64
	TotalDebits    int64
	EntryCount     int32
	Reconciled     bool
	Kind           string
	PDF            []byte
	CSV            []byte
	CreatedAt      time.Time `gorm:"index"`
}

type statementServer struct {
	statementv1.UnimplementedStatementServiceServer
	db *gorm.DB
	cl *clients.Clients
}

func toProto(s *Statement) *statementv1.Statement {
	return &statementv1.Statement{
		Id: s.ID, AccountId: s.AccountID, AccountNumber: s.AccountNumber, UserId: s.UserID, HolderName: s.HolderName,
		PeriodFrom: s.PeriodFrom, PeriodTo: s.PeriodTo, OpeningBalance: s.OpeningBalance, ClosingBalance: s.ClosingBalance,
		TotalCredits: s.TotalCredits, TotalDebits: s.TotalDebits, EntryCount: s.EntryCount, Reconciled: s.Reconciled,
		Kind: s.Kind, CreatedAt: util.TS(s.CreatedAt),
	}
}

func (s *statementServer) GenerateStatement(ctx context.Context, req *statementv1.GenerateStatementRequest) (*statementv1.Statement, error) {
	if _, err := authz.RequireUser(ctx); err != nil {
		return nil, err
	}
	from, err := util.ParseDate(req.From)
	if err != nil {
		return nil, util.Invalid("from must be YYYY-MM-DD")
	}
	to, err := util.ParseDate(req.To)
	if err != nil {
		return nil, util.Invalid("to must be YYYY-MM-DD")
	}
	if to.Before(from) {
		return nil, util.Invalid("to must not be before from")
	}
	if to.Sub(from) > 366*24*time.Hour {
		return nil, util.Invalid("a statement can cover at most one year")
	}
	// account-service enforces ownership.
	acc, err := s.cl.Account().GetAccount(ctx, &accountv1.GetAccountRequest{AccountId: req.AccountId})
	if err != nil {
		return nil, err
	}
	st, err := s.build(ctx, acc, from, to, "ON_DEMAND")
	if err != nil {
		return nil, err
	}
	return toProto(st), nil
}

// build pulls postings from the ledger and renders the statement. It is
// reconciled when opening + credits - debits equals the ledger's own balance
// at the end of the period.
func (s *statementServer) build(ctx context.Context, acc *accountv1.Account, from, to time.Time, kind string) (*Statement, error) {
	end := to.AddDate(0, 0, 1)
	ledger := s.cl.Ledger()
	opening, err := ledger.GetBalanceAt(ctx, &ledgerv1.GetBalanceAtRequest{AccountId: acc.Id, At: timestamppb.New(from)})
	if err != nil {
		return nil, err
	}
	closingLedger, err := ledger.GetBalanceAt(ctx, &ledgerv1.GetBalanceAtRequest{AccountId: acc.Id, At: timestamppb.New(end)})
	if err != nil {
		return nil, err
	}
	ps, err := ledger.ListAccountPostings(ctx, &ledgerv1.ListAccountPostingsRequest{AccountId: acc.Id, From: timestamppb.New(from), To: timestamppb.New(end)})
	if err != nil {
		return nil, err
	}
	st := &Statement{
		ID: uuid.NewString(), AccountID: acc.Id, AccountNumber: acc.AccountNumber, UserID: acc.UserId, HolderName: acc.HolderName,
		PeriodFrom: from.Format("2006-01-02"), PeriodTo: to.Format("2006-01-02"), OpeningBalance: opening.Balance,
		EntryCount: int32(len(ps.Postings)), Kind: kind, CreatedAt: time.Now().UTC(),
	}
	for _, p := range ps.Postings {
		if p.Direction == ledgerx.Credit {
			st.TotalCredits += p.Amount
		} else {
			st.TotalDebits += p.Amount
		}
	}
	st.ClosingBalance = st.OpeningBalance + st.TotalCredits - st.TotalDebits
	st.Reconciled = st.ClosingBalance == closingLedger.Balance
	st.CSV = renderCSV(st, ps.Postings)
	st.PDF, err = renderPDF(st, acc, ps.Postings)
	if err != nil {
		return nil, util.Internal(err)
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(st).Error; err != nil {
			return err
		}
		if err := events.Notify(tx, acc.UserId, "STATEMENT_READY", map[string]string{
			"account_number": acc.AccountNumber, "period": st.PeriodFrom + " to " + st.PeriodTo,
		}); err != nil {
			return err
		}
		return events.Audit(ctx, tx, svcName, "statement.generate", "statement", st.ID, "SUCCESS", nil,
			map[string]any{"account_id": acc.Id, "period": st.PeriodFrom + "/" + st.PeriodTo, "reconciled": st.Reconciled})
	})
	if err != nil {
		return nil, util.Internal(err)
	}
	return st, nil
}

func amount(v int64) string { return fmt.Sprintf("%d.%02d", v/100, abs(v%100)) }

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func renderCSV(st *Statement, ps []*ledgerv1.AccountPosting) []byte {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	_ = w.Write([]string{"date", "description", "reference", "debit", "credit", "balance"})
	_ = w.Write([]string{st.PeriodFrom, "Opening balance", "", "", "", amount(st.OpeningBalance)})
	for _, p := range ps {
		debit, credit := "", ""
		if p.Direction == ledgerx.Credit {
			credit = amount(p.Amount)
		} else {
			debit = amount(p.Amount)
		}
		_ = w.Write([]string{p.CreatedAt.AsTime().Format("2006-01-02 15:04"), p.Description, p.ReferenceType + ":" + p.ReferenceId, debit, credit, amount(p.BalanceAfter)})
	}
	_ = w.Write([]string{st.PeriodTo, "Closing balance", "", amount(st.TotalDebits), amount(st.TotalCredits), amount(st.ClosingBalance)})
	w.Flush()
	return b.Bytes()
}

func renderPDF(st *Statement, acc *accountv1.Account, ps []*ledgerv1.AccountPosting) ([]byte, error) {
	pdf := fpdf.New("P", "mm", "A4", "")
	tr := pdf.UnicodeTranslatorFromDescriptor("")
	pdf.SetTitle("Account statement "+st.AccountNumber, true)
	pdf.SetAutoPageBreak(true, 18)
	pdf.SetFooterFunc(func() {
		pdf.SetY(-12)
		pdf.SetFont("Helvetica", "", 7)
		pdf.SetTextColor(120, 120, 120)
		pdf.CellFormat(0, 5, tr(fmt.Sprintf("CBMP Bank - demonstration system, no real money.  Statement %s  -  page %d", st.ID[:8], pdf.PageNo())), "", 0, "C", false, 0, "")
	})
	pdf.AddPage()
	pdf.SetFillColor(15, 76, 129)
	pdf.Rect(0, 0, 210, 28, "F")
	pdf.SetTextColor(255, 255, 255)
	pdf.SetFont("Helvetica", "B", 18)
	pdf.SetXY(14, 8)
	pdf.Cell(0, 8, "CBMP Bank")
	pdf.SetFont("Helvetica", "", 10)
	pdf.SetXY(14, 16)
	pdf.Cell(0, 6, "Account statement")
	pdf.SetTextColor(30, 30, 30)
	pdf.SetXY(14, 36)
	pdf.SetFont("Helvetica", "B", 11)
	pdf.Cell(100, 6, tr(st.HolderName))
	pdf.SetFont("Helvetica", "", 9)
	pdf.SetXY(120, 36)
	pdf.MultiCell(76, 5, tr(fmt.Sprintf("Account: %s (%s)\nPeriod: %s to %s\nGenerated: %s UTC",
		st.AccountNumber, titleCase(acc.Type), st.PeriodFrom, st.PeriodTo, st.CreatedAt.Format("2006-01-02 15:04"))), "", "R", false)

	// Summary boxes.
	y := 56.0
	boxes := []struct{ label, value string }{
		{"Opening balance", util.Nu(st.OpeningBalance)}, {"Money in", util.Nu(st.TotalCredits)},
		{"Money out", util.Nu(st.TotalDebits)}, {"Closing balance", util.Nu(st.ClosingBalance)},
	}
	for i, b := range boxes {
		x := 14 + float64(i)*46
		pdf.SetFillColor(243, 246, 250)
		pdf.Rect(x, y, 43, 16, "F")
		pdf.SetXY(x+3, y+2)
		pdf.SetFont("Helvetica", "", 7)
		pdf.SetTextColor(100, 100, 100)
		pdf.Cell(37, 4, b.label)
		pdf.SetXY(x+3, y+7)
		pdf.SetFont("Helvetica", "B", 10)
		pdf.SetTextColor(20, 20, 20)
		pdf.Cell(37, 6, b.value)
	}

	pdf.SetXY(14, y+22)
	cols := []float64{28, 78, 24, 24, 28}
	head := []string{"Date", "Description", "Debit", "Credit", "Balance"}
	pdf.SetFont("Helvetica", "B", 8)
	pdf.SetFillColor(15, 76, 129)
	pdf.SetTextColor(255, 255, 255)
	for i, h := range head {
		align := "L"
		if i >= 2 {
			align = "R"
		}
		pdf.CellFormat(cols[i], 7, h, "", 0, align, true, 0, "")
	}
	pdf.Ln(-1)
	pdf.SetFont("Helvetica", "", 8)
	pdf.SetTextColor(30, 30, 30)
	row := func(i int, date, desc, debit, credit, bal string) {
		fill := i%2 == 1
		pdf.SetFillColor(248, 250, 252)
		if len(desc) > 52 {
			desc = desc[:50] + "..."
		}
		pdf.SetX(14)
		pdf.CellFormat(cols[0], 6, date, "", 0, "L", fill, 0, "")
		pdf.CellFormat(cols[1], 6, tr(desc), "", 0, "L", fill, 0, "")
		pdf.CellFormat(cols[2], 6, debit, "", 0, "R", fill, 0, "")
		pdf.CellFormat(cols[3], 6, credit, "", 0, "R", fill, 0, "")
		pdf.CellFormat(cols[4], 6, bal, "", 0, "R", fill, 0, "")
		pdf.Ln(-1)
	}
	row(0, st.PeriodFrom, "Opening balance", "", "", amount(st.OpeningBalance))
	for i, p := range ps {
		debit, credit := "", ""
		if p.Direction == ledgerx.Credit {
			credit = amount(p.Amount)
		} else {
			debit = amount(p.Amount)
		}
		row(i+1, p.CreatedAt.AsTime().Format("2006-01-02 15:04"), p.Description, debit, credit, amount(p.BalanceAfter))
	}
	pdf.SetFont("Helvetica", "B", 8)
	row(len(ps)+1, st.PeriodTo, "Closing balance", amount(st.TotalDebits), amount(st.TotalCredits), amount(st.ClosingBalance))
	pdf.Ln(4)
	pdf.SetX(14)
	pdf.SetFont("Helvetica", "", 8)
	recon := "Reconciled with the ledger: closing balance matches ledger-service."
	if !st.Reconciled {
		pdf.SetTextColor(180, 30, 30)
		recon = "NOT reconciled: closing balance differs from the ledger. Contact the bank."
	}
	pdf.Cell(0, 5, recon)
	var b bytes.Buffer
	if err := pdf.Output(&b); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func (s *statementServer) ListStatements(ctx context.Context, req *statementv1.ListStatementsRequest) (*statementv1.ListStatementsResponse, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	var sts []Statement
	s.db.WithContext(ctx).Omit("pdf", "csv").Where("account_id = ?", req.AccountId).Order("created_at DESC").Limit(50).Find(&sts)
	out := &statementv1.ListStatementsResponse{}
	for i := range sts {
		if !p.CanAccessUser(sts[i].UserID) {
			return nil, util.NotFound("account")
		}
		out.Statements = append(out.Statements, toProto(&sts[i]))
	}
	return out, nil
}

func (s *statementServer) DownloadStatement(ctx context.Context, req *statementv1.DownloadStatementRequest) (*httpbody.HttpBody, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	var st Statement
	if _, err := uuid.Parse(req.StatementId); err != nil {
		return nil, util.NotFound("statement")
	}
	if err := s.db.WithContext(ctx).First(&st, "id = ?", req.StatementId).Error; err != nil || !p.CanAccessUser(st.UserID) {
		return nil, util.NotFound("statement")
	}
	if strings.EqualFold(req.Format, "csv") {
		return &httpbody.HttpBody{ContentType: "text/csv; charset=utf-8", Data: st.CSV}, nil
	}
	return &httpbody.HttpBody{ContentType: "application/pdf", Data: st.PDF}, nil
}

func (s *statementServer) RunMonthly(ctx context.Context, req *statementv1.RunMonthlyRequest) (*statementv1.RunMonthlyResponse, error) {
	if _, err := authz.RequireRole(ctx, authz.RoleAdmin); err != nil {
		return nil, err
	}
	y, m := req.Year, req.Month
	if y == 0 || m < 1 || m > 12 {
		prev := time.Now().UTC().AddDate(0, -1, 0)
		y, m = int32(prev.Year()), int32(prev.Month())
	}
	return s.runMonthly(ctx, y, m)
}

func (s *statementServer) runMonthly(ctx context.Context, year, month int32) (*statementv1.RunMonthlyResponse, error) {
	from := time.Date(int(year), time.Month(month), 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, -1)
	ids, err := s.cl.Account().ListAllAccountIds(ctx, &accountv1.ListAllAccountIdsRequest{})
	if err != nil {
		return nil, err
	}
	out := &statementv1.RunMonthlyResponse{}
	for _, id := range ids.AccountIds {
		var n int64
		s.db.WithContext(ctx).Model(&Statement{}).Where("account_id = ? AND kind = ? AND period_from = ?", id, "MONTHLY", from.Format("2006-01-02")).Count(&n)
		if n > 0 {
			out.Skipped++
			continue
		}
		acc, err := s.cl.Account().GetAccount(ctx, &accountv1.GetAccountRequest{AccountId: id})
		if err != nil || acc.CreatedAt.AsTime().After(to.AddDate(0, 0, 1)) {
			out.Skipped++
			continue
		}
		if _, err := s.build(ctx, acc, from, to, "MONTHLY"); err != nil {
			out.Failed++
			continue
		}
		out.Generated++
	}
	return out, nil
}


func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + strings.ToLower(s[1:])
}
