// notification-service delivers email (SMTP, Mailpit in development), simulated
// SMS and in-app messages from named templates.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/smtp"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	authv1 "github.com/dso202/bank-platform/gen/go/bank/auth/v1"
	notificationv1 "github.com/dso202/bank-platform/gen/go/bank/notification/v1"
	"github.com/dso202/bank-platform/pkg/authz"
	"github.com/dso202/bank-platform/pkg/clients"
	"github.com/dso202/bank-platform/pkg/config"
	"github.com/dso202/bank-platform/pkg/db"
	"github.com/dso202/bank-platform/pkg/logx"
	"github.com/dso202/bank-platform/pkg/server"
	"github.com/dso202/bank-platform/pkg/util"
)

var delivered = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "bank_notifications_total", Help: "Notifications delivered by channel and template.",
}, []string{"channel", "template", "outcome"})

// Notification is an in-app message.
type Notification struct {
	ID        string `gorm:"primaryKey;type:uuid"`
	UserID    string `gorm:"type:uuid;index:idx_notif_user_time"`
	Template  string
	Title     string
	Body      string
	Read      bool `gorm:"index"`
	CreatedAt time.Time `gorm:"index:idx_notif_user_time"`
}

// Delivery logs each send attempt; EventID makes delivery idempotent.
type Delivery struct {
	ID        string `gorm:"primaryKey;type:uuid"`
	EventID   string `gorm:"uniqueIndex:idx_delivery_event_channel"`
	Channel   string `gorm:"uniqueIndex:idx_delivery_event_channel"`
	UserID    string `gorm:"index"`
	Template  string
	Recipient string
	Outcome   string
	Error     string
	CreatedAt time.Time
}

type tmpl struct {
	title, body string
	channels    []string
}

var templates = map[string]tmpl{
	"OTP":                   {"Your verification code", "Your one-time code to {{.purpose}} is {{.code}}. It expires in 10 minutes. Never share this code with anyone, including bank staff.", []string{"EMAIL", "SMS"}},
	"WELCOME":               {"Welcome to CBMP Bank", "Hi {{.name}}, your email is verified. Next: complete your profile and KYC to open an account.", []string{"IN_APP", "EMAIL"}},
	"LOGIN_ALERT":           {"New sign-in", "Your account was signed in to using {{.method}} at {{.time}}. If this wasn't you, change your password now.", []string{"IN_APP", "EMAIL"}},
	"PASSWORD_CHANGED":      {"Password changed", "Your password was changed and other sessions were signed out.", []string{"IN_APP", "EMAIL"}},
	"KYC_SUBMITTED":         {"KYC submitted", "We received your identity documents. An officer will review them shortly.", []string{"IN_APP"}},
	"KYC_APPROVED":          {"KYC approved", "Your identity is verified. You can now open accounts and transact.", []string{"IN_APP", "EMAIL"}},
	"KYC_REJECTED":          {"KYC needs attention", "We could not verify your identity: {{.note}}. Please resubmit.", []string{"IN_APP", "EMAIL"}},
	"ACCOUNT_OPENED":        {"Account opened", "Your {{.type}} account {{.account_number}} is open and ready to use.", []string{"IN_APP", "EMAIL"}},
	"ACCOUNT_FROZEN":        {"Account frozen", "Account {{.account_number}} has been frozen: {{.reason}}. Contact the bank for help.", []string{"IN_APP", "EMAIL", "SMS"}},
	"ACCOUNT_UNFROZEN":      {"Account active again", "Account {{.account_number}} is active again.", []string{"IN_APP"}},
	"DEPOSIT_RECEIVED":      {"Deposit received", "{{.amount}} was deposited to {{.account_number}}. Balance: {{.balance}}.", []string{"IN_APP", "SMS"}},
	"WITHDRAWAL_MADE":       {"Cash withdrawal", "{{.amount}} was withdrawn from {{.account_number}}. Balance: {{.balance}}.", []string{"IN_APP", "SMS"}},
	"TRANSFER_SENT":         {"Transfer sent", "You sent {{.amount}} to {{.to}}. Available balance: {{.balance}}.", []string{"IN_APP", "EMAIL"}},
	"TRANSFER_RECEIVED":     {"Money received", "You received {{.amount}} from {{.from}} into {{.account_number}}.", []string{"IN_APP", "EMAIL", "SMS"}},
	"TRANSFER_FAILED":       {"Transfer failed", "Your transfer of {{.amount}} to {{.to}} failed: {{.reason}}. No money left your account.", []string{"IN_APP"}},
	"TRANSFER_REJECTED":     {"Transfer declined", "Your transfer of {{.amount}} to {{.to}} was declined for security reasons. Contact us if this was you.", []string{"IN_APP", "EMAIL"}},
	"TRANSFER_UNDER_REVIEW": {"Transfer under review", "Your transfer of {{.amount}} to {{.to}} is being reviewed by our security team. The funds are on hold.", []string{"IN_APP", "EMAIL"}},
	"PAYMENT_SUCCESS":       {"Bill paid", "{{.amount}} paid to {{.biller}} (ref {{.reference}}). Confirmation: {{.gateway_ref}}.", []string{"IN_APP", "EMAIL"}},
	"PAYMENT_FAILED":        {"Payment failed", "Your payment of {{.amount}} to {{.biller}} failed: {{.reason}}.", []string{"IN_APP"}},
	"PAYMENT_UNDER_REVIEW":  {"Payment under review", "Your payment of {{.amount}} to {{.biller}} is being reviewed. The funds are on hold.", []string{"IN_APP"}},
	"CARD_ISSUED":           {"Debit card issued", "Your debit card ending {{.last4}} for {{.account_number}} is ready. Activate it and set a PIN to start using it.", []string{"IN_APP", "EMAIL"}},
	"CARD_BLOCKED":          {"Card blocked", "Your card ending {{.last4}} was blocked ({{.reason}}).", []string{"IN_APP", "EMAIL", "SMS"}},
	"CARD_PURCHASE":         {"Card purchase", "{{.amount}} at {{.merchant}} with card ••{{.last4}}. Balance: {{.balance}}.", []string{"IN_APP", "SMS"}},
	"CARD_DECLINED":         {"Card payment declined", "{{.amount}} at {{.merchant}} with card ••{{.last4}} was declined: {{.reason}}.", []string{"IN_APP", "SMS"}},
	"LOAN_APPLIED":          {"Loan application received", "We received your {{.product}} application for {{.amount}}.", []string{"IN_APP"}},
	"LOAN_APPROVED":         {"Loan approved", "Your {{.product}} of {{.amount}} was approved and credited to {{.account_number}}. Monthly EMI: {{.emi}}.", []string{"IN_APP", "EMAIL"}},
	"LOAN_REJECTED":         {"Loan application update", "Your {{.product}} application was not approved: {{.note}}.", []string{"IN_APP", "EMAIL"}},
	"LOAN_REPAYMENT":        {"EMI paid", "EMI {{.number}} of {{.term}} ({{.amount}}) for your {{.product}} is paid.", []string{"IN_APP"}},
	"LOAN_CLOSED":           {"Loan fully repaid", "Congratulations, your {{.product}} is fully repaid and closed.", []string{"IN_APP", "EMAIL"}},
	"LOAN_DUE":              {"EMI due soon", "Your {{.product}} EMI of {{.amount}} is due on {{.due}}.", []string{"IN_APP", "SMS"}},
	"LOW_BALANCE":           {"Low balance", "Account {{.account_number}} balance is {{.balance}}.", []string{"IN_APP"}},
	"STATEMENT_READY":       {"Statement ready", "Your statement for {{.account_number}} ({{.period}}) is ready to download.", []string{"IN_APP", "EMAIL"}},
}

type notifServer struct {
	notificationv1.UnimplementedNotificationServiceServer
	db       *gorm.DB
	cl       *clients.Clients
	smtpAddr string
	from     string
}

func main() {
	cfg := server.Init("notification-service")
	gdb := db.MustOpen(cfg, &Notification{}, &Delivery{})
	if cfg.MigrateOnly {
		return
	}
	cl := clients.New("notification-service")
	defer cl.Close()
	svc := &notifServer{db: gdb, cl: cl, smtpAddr: config.Get("SMTP_ADDR", ""), from: config.Get("SMTP_FROM", "no-reply@cbmp.bank.local")}
	server.Run(server.Service{
		Config:   cfg,
		Register: func(s *grpc.Server) { notificationv1.RegisterNotificationServiceServer(s, svc) },
	})
}

func render(text string, data map[string]string) string {
	t, err := template.New("").Option("missingkey=zero").Parse(text)
	if err != nil {
		return text
	}
	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return text
	}
	return strings.ReplaceAll(b.String(), "<no value>", "")
}

func (s *notifServer) once(ctx context.Context, eventID, channel, userID, tpl, recipient string, send func() error) error {
	if eventID != "" {
		var n int64
		s.db.WithContext(ctx).Model(&Delivery{}).Where("event_id = ? AND channel = ? AND outcome = ?", eventID, channel, "SENT").Count(&n)
		if n > 0 {
			return nil
		}
	}
	err := send()
	d := Delivery{ID: uuid.NewString(), EventID: eventID, Channel: channel, UserID: userID, Template: tpl, Recipient: recipient, Outcome: "SENT"}
	if eventID == "" {
		d.EventID = d.ID
	}
	if err != nil {
		d.Outcome, d.Error = "FAILED", err.Error()
		d.EventID = d.ID // keep failed attempts without blocking a retry
	}
	s.db.WithContext(ctx).Create(&d)
	delivered.WithLabelValues(channel, tpl, d.Outcome).Inc()
	return err
}

func (s *notifServer) Send(ctx context.Context, req *notificationv1.SendRequest) (*notificationv1.SendResponse, error) {
	t, ok := templates[req.Template]
	if !ok {
		return nil, util.Invalid("unknown template %q", req.Template)
	}
	channels := req.Channels
	if len(channels) == 0 {
		channels = t.channels
	}
	title, body := render(t.title, req.Data), render(t.body, req.Data)
	email, phone, name := req.Email, "", ""
	if req.UserId != "" && (email == "" || slices.Contains(channels, "SMS")) {
		if u, err := s.cl.Auth().GetUser(ctx, &authv1.GetUserRequest{UserId: req.UserId}); err == nil {
			if email == "" {
				email = u.Email
			}
			phone, name = u.Phone, u.FullName
		}
	}
	out := &notificationv1.SendResponse{}
	var errs []error
	for _, ch := range channels {
		var err error
		switch ch {
		case "IN_APP":
			if req.UserId == "" {
				continue
			}
			err = s.once(ctx, req.EventId, ch, req.UserId, req.Template, req.UserId, func() error {
				return s.db.WithContext(ctx).Create(&Notification{ID: uuid.NewString(), UserID: req.UserId, Template: req.Template, Title: title, Body: body}).Error
			})
		case "EMAIL":
			if email == "" {
				continue
			}
			err = s.once(ctx, req.EventId, ch, req.UserId, req.Template, logx.MaskEmail(email), func() error {
				return s.sendEmail(email, name, title, body)
			})
		case "SMS":
			if phone == "" {
				continue
			}
			err = s.once(ctx, req.EventId, ch, req.UserId, req.Template, util.MaskTail(phone, 3), func() error {
				// SMS provider is simulated; the message is logged with the number masked.
				slog.InfoContext(ctx, "sms sent (simulated)", "to", util.MaskTail(phone, 3), "template", req.Template)
				return nil
			})
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", ch, err))
		} else {
			out.Delivered = append(out.Delivered, ch)
		}
	}
	if len(errs) > 0 {
		// Returning an error makes the caller's outbox retry; delivered channels are skipped next time.
		return nil, util.Internal(errors.Join(errs...))
	}
	return out, nil
}

func (s *notifServer) sendEmail(to, name, subject, body string) error {
	if s.smtpAddr == "" {
		slog.Info("email skipped (SMTP_ADDR not set)", "to", logx.MaskEmail(to), "subject", subject)
		return nil
	}
	greeting := "Hello"
	if name != "" {
		greeting += " " + name
	}
	htmlBody := fmt.Sprintf(`<div style="font-family:system-ui,sans-serif;max-width:520px;margin:auto;padding:24px;border:1px solid #e5e7eb;border-radius:12px">
<div style="font-weight:700;color:#0f4c81;font-size:18px">CBMP Bank</div>
<h2 style="margin:16px 0 8px;font-size:20px">%s</h2><p>%s,</p><p style="line-height:1.5">%s</p>
<p style="color:#6b7280;font-size:12px;margin-top:24px">This is an automated message from a demonstration system. No real money is involved.</p></div>`,
		html.EscapeString(subject), html.EscapeString(greeting), html.EscapeString(body))
	msg := "From: CBMP Bank <" + s.from + ">\r\nTo: " + to + "\r\nSubject: " + subject +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\nDate: " + time.Now().Format(time.RFC1123Z) + "\r\n\r\n" + htmlBody
	return smtp.SendMail(s.smtpAddr, nil, s.from, []string{to}, []byte(msg))
}

func (s *notifServer) ListMyNotifications(ctx context.Context, req *notificationv1.ListMyNotificationsRequest) (*notificationv1.ListMyNotificationsResponse, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	limit, _ := util.Page(req.PageSize, "")
	q := s.db.WithContext(ctx).Where("user_id = ?", p.UserID)
	if req.UnreadOnly {
		q = q.Where("read = ?", false)
	}
	var ns []Notification
	q.Order("created_at DESC").Limit(limit).Find(&ns)
	out := &notificationv1.ListMyNotificationsResponse{}
	s.db.WithContext(ctx).Model(&Notification{}).Where("user_id = ? AND read = ?", p.UserID, false).Count(&out.UnreadCount)
	for _, n := range ns {
		out.Notifications = append(out.Notifications, &notificationv1.Notification{
			Id: n.ID, UserId: n.UserID, Template: n.Template, Title: n.Title, Body: n.Body, Channel: "IN_APP", Read: n.Read, CreatedAt: util.TS(n.CreatedAt),
		})
	}
	return out, nil
}

func (s *notifServer) MarkRead(ctx context.Context, req *notificationv1.MarkReadRequest) (*notificationv1.MarkReadResponse, error) {
	p, err := authz.RequireUser(ctx)
	if err != nil {
		return nil, err
	}
	q := s.db.WithContext(ctx).Model(&Notification{}).Where("user_id = ? AND read = ?", p.UserID, false)
	if req.NotificationId != "all" {
		q = q.Where("id = ?", req.NotificationId)
	}
	res := q.Update("read", true)
	return &notificationv1.MarkReadResponse{Updated: res.RowsAffected}, nil
}
