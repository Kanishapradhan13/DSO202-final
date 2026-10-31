// Package ledgerx holds the chart of system accounts and posting helpers used by
// services that move money. Only ledger-service changes balances; these helpers
// only build requests for it.
package ledgerx

import ledgerv1 "github.com/dso202/bank-platform/gen/go/bank/ledger/v1"

// System (bank-owned) ledger accounts.
const (
	Cash            = "SYS_CASH"
	LoanPortfolio   = "SYS_LOAN_PORTFOLIO"
	InterestIncome  = "SYS_INTEREST_INCOME"
	PaymentClearing = "SYS_PAYMENT_CLEARING"
	CardSettlement  = "SYS_CARD_SETTLEMENT"
)

// SystemAccounts maps each system account to its display name.
var SystemAccounts = map[string]string{
	Cash:            "Cash in vault",
	LoanPortfolio:   "Loan portfolio",
	InterestIncome:  "Interest income",
	PaymentClearing: "Outgoing payment clearing",
	CardSettlement:  "Card network settlement",
}

// Directions.
const (
	Debit  = "DEBIT"
	Credit = "CREDIT"
)

// Move builds a balanced pair: amount leaves from and arrives at to.
func Move(from, to string, amount int64) []*ledgerv1.Posting {
	return []*ledgerv1.Posting{
		{AccountId: from, Direction: Debit, Amount: amount},
		{AccountId: to, Direction: Credit, Amount: amount},
	}
}

// BalanceAfter finds the resulting balance of accountID in a posted entry.
func BalanceAfter(e *ledgerv1.JournalEntry, accountID string) int64 {
	var out int64
	for _, p := range e.GetPostings() {
		if p.AccountId == accountID {
			out = p.BalanceAfter
		}
	}
	return out
}
