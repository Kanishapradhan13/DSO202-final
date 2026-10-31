#!/usr/bin/env bash
# End-to-end journey through the API gateway (Planning doc §6 "core journey"):
# register → verify OTP → profile → KYC → officer approves → open account →
# deposit → transfer (+ idempotent replay) → fraud review → bill payment →
# card purchase → loan → statement → audit chain + trial balance.
set -euo pipefail
GW=${GW:-http://localhost:8080}
PASS=0
FAIL=0
RUN=$RANDOM$RANDOM

green() { printf '\033[32m%s\033[0m\n' "$*"; }
red() { printf '\033[31m%s\033[0m\n' "$*"; }
check() { # check "label" actual expected
  if [[ "$2" == "$3" ]]; then green "  ✓ $1"; PASS=$((PASS + 1)); else red "  ✗ $1 (got '$2', want '$3')"; FAIL=$((FAIL + 1)); fi
}

# call METHOD PATH [TOKEN] [BODY] [IDEMPOTENCY_KEY]
call() {
  local args=(-s -X "$1" "$GW$2" -H 'Content-Type: application/json')
  [[ -n "${3:-}" ]] && args+=(-H "Authorization: Bearer $3")
  [[ -n "${4:-}" ]] && args+=(-d "$4")
  [[ -n "${5:-}" ]] && args+=(-H "Idempotency-Key: $5")
  curl "${args[@]}"
}

login() { call POST /v1/auth/login "" "{\"email\":\"$1\",\"password\":\"$2\"}" | jq -r '.tokens.access_token'; }

new_customer() { # new_customer NAME EMAIL -> prints token
  local name=$1 email=$2
  local msg code
  msg=$(call POST /v1/auth/register "" "{\"full_name\":\"$name\",\"email\":\"$email\",\"phone\":\"17$RANDOM$RANDOM\",\"password\":\"Passw0rd!\"}" | jq -r .message)
  code=$(sed -n 's/.*code: \([0-9]\{6\}\).*/\1/p' <<<"$msg")
  call POST /v1/auth/verify-email "" "{\"email\":\"$email\",\"code\":\"$code\"}" >/dev/null
  login "$email" "Passw0rd!"
}

onboard() { # onboard TOKEN NAME -> prints account id
  local tok=$1 name=$2
  call POST /v1/customers/me "$tok" "{\"full_name\":\"$name\",\"date_of_birth\":\"2000-05-14\",\"address\":\"Changzamtog\",\"city\":\"Thimphu\"}" >/dev/null
  local case_id
  case_id=$(call POST /v1/kyc "$tok" "{\"document_type\":\"CID\",\"document_number\":\"1$(printf '%010d' $RANDOM$RANDOM | cut -c1-10)\",\"date_of_birth\":\"2000-05-14\"}" | jq -r .id)
  call POST "/v1/staff/kyc/$case_id/review" "$OPS" '{"approve":true,"note":"verified"}' >/dev/null
  call POST /v1/accounts "$tok" '{"type":"SAVINGS","nickname":"Main"}' | jq -r .id
}

balance() { call GET "/v1/accounts/$2" "$1" | jq -r .available_balance; }

echo "▶ Staff sign-in"
OPS=$(login ops@bank.local Bank@12345)
FRAUD=$(login fraud@bank.local Bank@12345)
LOANS=$(login loans@bank.local Bank@12345)
AUDITOR=$(login auditor@bank.local Bank@12345)
check "staff tokens issued" "$([[ ${#OPS} -gt 100 && ${#AUDITOR} -gt 100 ]] && echo yes)" "yes"

echo "▶ Onboarding two customers (register → OTP → profile → KYC → account)"
A=$(new_customer "Pema Wangmo" "pema.$RUN@example.com")
B=$(new_customer "Karma Dorji" "karma.$RUN@example.com")
check "customer A signed in" "$([[ ${#A} -gt 100 ]] && echo yes)" "yes"
ACC_A=$(onboard "$A" "Pema Wangmo")
ACC_B=$(onboard "$B" "Karma Dorji")
NUM_B=$(call GET "/v1/accounts/$ACC_B" "$B" | jq -r .account_number)
check "accounts opened" "$([[ $ACC_A =~ ^[0-9a-f-]{36}$ && $ACC_B =~ ^[0-9a-f-]{36}$ ]] && echo yes)" "yes"
check "customer cannot open account before KYC (gate)" \
  "$(call POST /v1/accounts "$(new_customer "No Kyc" "nokyc.$RUN@example.com")" '{"type":"SAVINGS"}' | jq -r .status)" "FailedPrecondition"

echo "▶ Teller deposit"
call POST /v1/staff/transactions/deposit "$OPS" "{\"account_id\":\"$ACC_A\",\"amount\":\"30000000\"}" "dep-$RUN" >/dev/null
check "A balance Nu. 300,000" "$(balance "$A" "$ACC_A")" "30000000"
check "customer cannot use teller API" "$(call POST /v1/staff/transactions/deposit "$A" "{\"account_id\":\"$ACC_A\",\"amount\":\"1\"}" "x-$RUN" | jq -r .code)" "403"

echo "▶ Transfer saga + idempotency"
KEY="tr-$RUN"
T1=$(call POST /v1/transfers "$A" "{\"from_account_id\":\"$ACC_A\",\"to_account_number\":\"$NUM_B\",\"amount\":\"250000\",\"description\":\"Lunch\"}" "$KEY")
check "transfer completed" "$(jq -r .status <<<"$T1")" "COMPLETED"
T2=$(call POST /v1/transfers "$A" "{\"from_account_id\":\"$ACC_A\",\"to_account_number\":\"$NUM_B\",\"amount\":\"250000\",\"description\":\"Lunch\"}" "$KEY")
check "replay returns same transfer" "$(jq -r .id <<<"$T2")" "$(jq -r .id <<<"$T1")"
check "B received exactly once" "$(balance "$B" "$ACC_B")" "250000"
check "A debited exactly once" "$(balance "$A" "$ACC_A")" "29750000"
check "insufficient funds rejected" "$(call POST /v1/transfers "$B" "{\"from_account_id\":\"$ACC_B\",\"to_account_number\":\"$(call GET "/v1/accounts/$ACC_A" "$A" | jq -r .account_number)\",\"amount\":\"99999999\"}" "ins-$RUN" | jq -r .status)" "FAILED"
check "B cannot read A's account" "$(call GET "/v1/accounts/$ACC_A" "$B" | jq -r .code)" "404"

echo "▶ Fraud review (large transfer is held, analyst releases it)"
T3=$(call POST /v1/transfers "$A" "{\"from_account_id\":\"$ACC_A\",\"to_account_number\":\"$NUM_B\",\"amount\":\"12000000\"}" "big-$RUN")
check "large transfer held for review" "$(jq -r .status <<<"$T3")" "PENDING_REVIEW"
check "funds on hold while reviewed" "$(call GET "/v1/accounts/$ACC_A" "$A" | jq -r .held_amount)" "12000000"
ALERT=$(call GET "/v1/staff/fraud/alerts?status=OPEN&page_size=100" "$FRAUD" | jq -r --arg id "$(jq -r .id <<<"$T3")" '.alerts[] | select(.reference_id==$id) | .id')
call POST "/v1/staff/fraud/alerts/$ALERT/decide" "$FRAUD" '{"approve":true,"note":"confirmed with customer"}' >/dev/null
check "released transfer completed" "$(call GET "/v1/transfers/$(jq -r .id <<<"$T3")" "$A" | jq -r .status)" "COMPLETED"
check "hold released" "$(call GET "/v1/accounts/$ACC_A" "$A" | jq -r .held_amount)" "0"

echo "▶ Bill payments"
P1=$(call POST /v1/payments "$A" "{\"from_account_id\":\"$ACC_A\",\"biller_id\":\"electricity\",\"customer_reference\":\"100234567\",\"amount\":\"150000\"}" "pay-$RUN")
check "bill paid" "$(jq -r .status <<<"$P1")" "SUCCESS"
BEFORE=$(balance "$A" "$ACC_A")
P2=$(call POST /v1/payments "$A" "{\"from_account_id\":\"$ACC_A\",\"biller_id\":\"water\",\"customer_reference\":\"FAIL-1\",\"amount\":\"10000\"}" "payf-$RUN")
check "declined bill payment fails cleanly" "$(jq -r .status <<<"$P2")" "FAILED"
check "no money lost on failed payment" "$(balance "$A" "$ACC_A")" "$BEFORE"

echo "▶ Debit card"
CARD=$(call POST /v1/cards "$A" "{\"account_id\":\"$ACC_A\"}" | jq -r .id)
call POST "/v1/cards/$CARD/activate" "$A" '{"pin":"4826"}' >/dev/null
AUTH=$(call POST "/v1/cards/$CARD/simulate-purchase" "$A" '{"merchant":"Clock Tower Cafe","amount":"45000","channel":"POS","pin":"4826"}' "card-$RUN")
check "card purchase approved" "$(jq -r .status <<<"$AUTH")" "APPROVED"
check "wrong PIN declined" "$(call POST "/v1/cards/$CARD/simulate-purchase" "$A" '{"merchant":"X","amount":"100","channel":"POS","pin":"0000"}' | jq -r .status)" "DECLINED"

echo "▶ Loan: apply → approve & disburse → repay"
LOAN=$(call POST /v1/loans "$B" "{\"product_id\":\"personal\",\"amount\":\"5000000\",\"term_months\":12,\"disbursement_account_id\":\"$ACC_B\",\"monthly_income\":\"6000000\",\"purpose\":\"Laptop\"}" | jq -r .id)
call POST "/v1/staff/loans/$LOAN/review" "$LOANS" '{"approve":true,"note":"good affordability"}' >/dev/null
check "loan active" "$(call GET "/v1/loans/$LOAN" "$B" | jq -r .status)" "ACTIVE"
R=$(call POST "/v1/loans/$LOAN/repay" "$B" "{\"from_account_id\":\"$ACC_B\"}")
check "first EMI paid" "$(jq -r .paid_instalments <<<"$R")" "1"

echo "▶ Statement"
ST=$(call POST "/v1/accounts/$ACC_A/statements" "$A" "{\"from\":\"$(date -u -v-1d +%F 2>/dev/null || date -u -d yesterday +%F)\",\"to\":\"$(date -u +%F)\"}")
check "statement reconciles to ledger" "$(jq -r .reconciled <<<"$ST")" "true"
check "PDF downloads" "$(curl -s "$GW/v1/statements/$(jq -r .id <<<"$ST")/download?format=pdf" -H "Authorization: Bearer $A" | head -c 4)" "%PDF"

echo "▶ Integrity"
sleep 2 # let outboxes deliver audit events
TB=$(call GET /v1/staff/ledger/trial-balance "$AUDITOR")
check "ledger balanced (debits = credits)" "$(jq -r .balanced <<<"$TB")" "true"
check "audit hash chain valid" "$(call GET /v1/staff/audit/verify "$AUDITOR" | jq -r .valid)" "true"
check "transfer recorded in history" "$(call GET "/v1/accounts/$ACC_B/transactions" "$B" | jq -r '[.transactions[] | select(.type=="TRANSFER_IN")] | length')" "2"
check "B notified of incoming money" "$(call GET /v1/notifications "$B" | jq -r '[.notifications[] | select(.template=="TRANSFER_RECEIVED")] | length')" "2"
check "internal RPC not exposed" "$(call POST /v1/ledger/post "$A" '{}' | jq -r .code)" "404"

echo
if [[ $FAIL -eq 0 ]]; then green "All $PASS checks passed"; else red "$FAIL failed, $PASS passed"; exit 1; fi
