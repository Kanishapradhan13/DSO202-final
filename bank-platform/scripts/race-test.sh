#!/usr/bin/env bash
# Concurrency check: an account holding Nu. 1,000 receives 10 simultaneous
# Nu. 300 transfers. Holds are placed under a row lock, so at most 3 may
# succeed and the balance can never go negative.
set -euo pipefail
GW=${GW:-http://localhost:8080}
R=$RANDOM$RANDOM

call() {
  local args=(-s -X "$1" "$GW$2" -H 'Content-Type: application/json')
  [[ -n "${3:-}" ]] && args+=(-H "Authorization: Bearer $3")
  [[ -n "${4:-}" ]] && args+=(-d "$4")
  [[ -n "${5:-}" ]] && args+=(-H "Idempotency-Key: $5")
  curl "${args[@]}"
}
login() { call POST /v1/auth/login "" "{\"email\":\"$1\",\"password\":\"$2\"}" | jq -r .tokens.access_token; }
OPS=$(login ops@bank.local Bank@12345)

customer() {
  local e="$1.$R@example.com" m c t k
  m=$(call POST /v1/auth/register "" "{\"full_name\":\"$1 Race\",\"email\":\"$e\",\"password\":\"Passw0rd!\"}" | jq -r .message)
  c=$(sed -n 's/.*code: \([0-9]\{6\}\).*/\1/p' <<<"$m")
  call POST /v1/auth/verify-email "" "{\"email\":\"$e\",\"code\":\"$c\"}" >/dev/null
  t=$(login "$e" Passw0rd!)
  call POST /v1/customers/me "$t" '{"full_name":"Race","date_of_birth":"1999-01-01","address":"x","city":"Paro"}' >/dev/null
  k=$(call POST /v1/kyc "$t" '{"document_type":"CID","document_number":"11111111111","date_of_birth":"1999-01-01"}' | jq -r .id)
  call POST "/v1/staff/kyc/$k/review" "$OPS" '{"approve":true}' >/dev/null
  echo "$t"
}

A=$(customer sender)
B=$(customer receiver)
ACC_A=$(call POST /v1/accounts "$A" '{"type":"SAVINGS"}' | jq -r .id)
NUM_B=$(call POST /v1/accounts "$B" '{"type":"SAVINGS"}' | jq -r .account_number)
call POST /v1/staff/transactions/deposit "$OPS" "{\"account_id\":\"$ACC_A\",\"amount\":\"100000\"}" "dep-$R" >/dev/null

echo "Sender holds Nu. 1,000; firing 10 concurrent Nu. 300 transfers…"
for i in $(seq 1 10); do
  call POST /v1/transfers "$A" "{\"from_account_id\":\"$ACC_A\",\"to_account_number\":\"$NUM_B\",\"amount\":\"30000\"}" "race-$R-$i" | jq -r .status &
done | sort | uniq -c
wait
BAL=$(call GET "/v1/accounts/$ACC_A" "$A" | jq -r .balance)
echo "Final sender balance: $BAL chhertrum (expected 10000 = Nu. 100)"
[[ "$BAL" == "10000" ]] && echo "PASS: no overspend" || { echo "FAIL"; exit 1; }
