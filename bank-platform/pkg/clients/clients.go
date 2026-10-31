// Package clients dials other services over gRPC. Calls propagate the caller's
// identity and request ID, add the internal-caller marker, carry trace context
// and get a default deadline.
package clients

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	accountv1 "github.com/dso202/bank-platform/gen/go/bank/account/v1"
	auditv1 "github.com/dso202/bank-platform/gen/go/bank/audit/v1"
	authv1 "github.com/dso202/bank-platform/gen/go/bank/auth/v1"
	cardv1 "github.com/dso202/bank-platform/gen/go/bank/card/v1"
	customerv1 "github.com/dso202/bank-platform/gen/go/bank/customer/v1"
	fraudv1 "github.com/dso202/bank-platform/gen/go/bank/fraud/v1"
	kycv1 "github.com/dso202/bank-platform/gen/go/bank/kyc/v1"
	ledgerv1 "github.com/dso202/bank-platform/gen/go/bank/ledger/v1"
	loanv1 "github.com/dso202/bank-platform/gen/go/bank/loan/v1"
	notificationv1 "github.com/dso202/bank-platform/gen/go/bank/notification/v1"
	paymentv1 "github.com/dso202/bank-platform/gen/go/bank/payment/v1"
	statementv1 "github.com/dso202/bank-platform/gen/go/bank/statement/v1"
	transactionv1 "github.com/dso202/bank-platform/gen/go/bank/transaction/v1"
	transferv1 "github.com/dso202/bank-platform/gen/go/bank/transfer/v1"
	"github.com/dso202/bank-platform/pkg/authz"
	"github.com/dso202/bank-platform/pkg/config"
)

// Default addresses match Kubernetes Service / Docker Compose names.
var defaultAddrs = map[string]string{
	"auth":         "auth-service:50051",
	"customer":     "customer-service:50051",
	"kyc":          "kyc-service:50051",
	"account":      "account-service:50051",
	"ledger":       "ledger-service:50051",
	"transaction":  "transaction-service:50051",
	"transfer":     "transfer-service:50051",
	"payment":      "payment-service:50051",
	"card":         "card-service:50051",
	"loan":         "loan-service:50051",
	"fraud":        "fraud-service:50051",
	"audit":        "audit-service:50051",
	"notification": "notification-service:50051",
	"statement":    "statement-service:50051",
}

// Addr returns the address for a service, overridable with <NAME>_ADDR.
func Addr(name string) string {
	return config.Get(strings.ToUpper(name)+"_ADDR", defaultAddrs[name])
}

const retryServiceConfig = `{
  "methodConfig": [{
    "name": [{}],
    "retryPolicy": {
      "maxAttempts": 4,
      "initialBackoff": "0.2s",
      "maxBackoff": "2s",
      "backoffMultiplier": 2,
      "retryableStatusCodes": ["UNAVAILABLE"]
    }
  }]
}`

// Clients lazily dials and caches connections.
type Clients struct {
	caller string
	mu     sync.Mutex
	conns  map[string]*grpc.ClientConn
}

// New returns a client set that identifies itself as caller.
func New(caller string) *Clients {
	return &Clients{caller: caller, conns: map[string]*grpc.ClientConn{}}
}

// Conn returns a connection to the named service.
func (c *Clients) Conn(name string) *grpc.ClientConn {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cc, ok := c.conns[name]; ok {
		return cc
	}
	cc, err := Dial(Addr(name), c.caller)
	if err != nil {
		panic(fmt.Sprintf("dial %s: %v", name, err))
	}
	c.conns[name] = cc
	return cc
}

// Close closes all connections.
func (c *Clients) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cc := range c.conns {
		_ = cc.Close()
	}
}

// Dial creates a client connection with identity propagation.
func Dial(target, caller string) (*grpc.ClientConn, error) {
	return grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		grpc.WithDefaultServiceConfig(retryServiceConfig),
		grpc.WithChainUnaryInterceptor(propagate(caller)),
	)
}

var propagatedKeys = []string{
	authz.MDUserID, authz.MDUserRoles, authz.MDUserEmail, authz.MDUserName,
	authz.MDRequestID, authz.MDClientIP,
}

func propagate(caller string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		in, _ := metadata.FromIncomingContext(ctx)
		out, _ := metadata.FromOutgoingContext(ctx)
		out = out.Copy()
		for _, k := range propagatedKeys {
			if v := in.Get(k); len(v) > 0 && len(out.Get(k)) == 0 {
				out.Set(k, v...)
			}
		}
		out.Set(authz.MDInternalCaller, caller)
		ctx = metadata.NewOutgoingContext(ctx, out)
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func (c *Clients) Auth() authv1.AuthServiceClient { return authv1.NewAuthServiceClient(c.Conn("auth")) }
func (c *Clients) Customer() customerv1.CustomerServiceClient {
	return customerv1.NewCustomerServiceClient(c.Conn("customer"))
}
func (c *Clients) Kyc() kycv1.KycServiceClient { return kycv1.NewKycServiceClient(c.Conn("kyc")) }
func (c *Clients) Account() accountv1.AccountServiceClient {
	return accountv1.NewAccountServiceClient(c.Conn("account"))
}
func (c *Clients) Ledger() ledgerv1.LedgerServiceClient {
	return ledgerv1.NewLedgerServiceClient(c.Conn("ledger"))
}
func (c *Clients) Transaction() transactionv1.TransactionServiceClient {
	return transactionv1.NewTransactionServiceClient(c.Conn("transaction"))
}
func (c *Clients) Transfer() transferv1.TransferServiceClient {
	return transferv1.NewTransferServiceClient(c.Conn("transfer"))
}
func (c *Clients) Payment() paymentv1.PaymentServiceClient {
	return paymentv1.NewPaymentServiceClient(c.Conn("payment"))
}
func (c *Clients) Card() cardv1.CardServiceClient { return cardv1.NewCardServiceClient(c.Conn("card")) }
func (c *Clients) Loan() loanv1.LoanServiceClient { return loanv1.NewLoanServiceClient(c.Conn("loan")) }
func (c *Clients) Fraud() fraudv1.FraudServiceClient {
	return fraudv1.NewFraudServiceClient(c.Conn("fraud"))
}
func (c *Clients) Audit() auditv1.AuditServiceClient {
	return auditv1.NewAuditServiceClient(c.Conn("audit"))
}
func (c *Clients) Notification() notificationv1.NotificationServiceClient {
	return notificationv1.NewNotificationServiceClient(c.Conn("notification"))
}
func (c *Clients) Statement() statementv1.StatementServiceClient {
	return statementv1.NewStatementServiceClient(c.Conn("statement"))
}
