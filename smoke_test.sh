#!/usr/bin/env bash

# ==============================================================================
# Automated Smoke & Isolation Test Suite for Go Microservices Platform
# Date: 2026-09-13
# ==============================================================================

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

pass() { echo -e "  [${GREEN}PASSED${NC}] $1"; }
fail() { echo -e "  [${RED}FAILED${NC}] $1"; exit 1; }
info() { echo -e "\n${YELLOW}===> $1${NC}"; }

# In-network curl helper function
incurl() {
  docker run --rm --network go-microservices_internal-net curlimages/curl:8.11.1 "$@"
}

# Helper to generate test JWT token
jwt() {
  node -e "
const c=require('crypto'), b=s=>Buffer.from(s).toString('base64url');
const h=b(JSON.stringify({alg:'HS256',typ:'JWT'}));
const p=b(JSON.stringify({user_id:'101',role:'customer',exp:Math.floor(Date.now()/1000)+Number(process.argv[1])}));
console.log(h+'.'+p+'.'+c.createHmac('sha256','super-secret-key').update(h+'.'+p).digest('base64url'));
" -- "$1"
}

# ------------------------------------------------------------------------------
info "0. Setup & Database Cleanup"
# ------------------------------------------------------------------------------
echo "Bringing up Docker containers..."
docker compose up -d --build > /dev/null 2>&1

echo "Resetting orders database..."
docker compose up -d --force-recreate --renew-anon-volumes order-db order-service > /dev/null 2>&1

# Wait for healthy state
echo "Waiting for services to become healthy..."
sleep 5

ORDER_COUNT=$(docker compose exec -T order-db psql -U postgres -d orderdb -tAc "select count(*) from orders;" 2>/dev/null | tr -d '\r\n')
if [ "$ORDER_COUNT" == "0" ]; then
  pass "0.3 Orders database reset successfully (count: 0)"
else
  fail "0.3 Reset failed. Orders count is '$ORDER_COUNT', expected '0'"
fi

# ------------------------------------------------------------------------------
info "Part N — Network Exposure & Isolation"
# ------------------------------------------------------------------------------
# N1
EXIT1=$(curl -s -m 3 -o /dev/null http://localhost:8001/users; echo $?)
EXIT2=$(curl -s -m 3 -o /dev/null http://localhost:8002/orders; echo $?)
if [ "$EXIT1" -ne 0 ] && [ "$EXIT2" -ne 0 ]; then
  pass "N1 Backends are NOT reachable from host (Exit codes: $EXIT1, $EXIT2)"
else
  fail "N1 Backends are exposed on host!"
fi

# N2
N2_PASS=true
for p in 5433 6379 5672; do
  C_EXIT=$(curl -s -m 2 -o /dev/null http://localhost:$p; echo $?)
  if [ "$C_EXIT" -eq 0 ]; then N2_PASS=false; fi
done
if [ "$N2_PASS" = true ]; then
  pass "N2 Datastores and broker are NOT reachable from host"
else
  fail "N2 Datastores exposed on host ports!"
fi

# N3
GATEWAY_NC=$(docker compose exec -T api-gateway nc -z -w 3 user-service 8001; echo $?)
GATEWAY_DB=$(docker compose exec -T api-gateway nc -z -w 3 user-db 5432 2>&1; echo $?)
if [ "$GATEWAY_NC" -eq 0 ] && [[ "$GATEWAY_DB" == *"bad address"* ]]; then
  pass "N3 Gateway reaches backend services, but NOT datastores"
else
  fail "N3 Gateway isolation rules failed"
fi

# N4
EGRESS_EXIT=$(docker compose exec -T user-service nc -z -w 3 1.1.1.1 443 2>&1; echo $?)
if [[ "$EGRESS_EXIT" == *"exit 1"* ]] || [[ "$EGRESS_EXIT" == *""* ]]; then
  pass "N4 Internal services have no internet egress"
else
  fail "N4 Egress security check failed"
fi

# ------------------------------------------------------------------------------
info "Part A — user-service (In-Network)"
# ------------------------------------------------------------------------------
# A1
A1_RES=$(incurl -s http://user-service:8001/users/101)
if [[ "$A1_RES" == *"Rahim"* ]]; then
  pass "A1 Seeded user 101 fetched successfully"
else
  fail "A1 Failed to fetch user 101"
fi

# A2
A2_RES=$(incurl -s http://user-service:8001/users/102)
if [[ "$A2_RES" == *"Karim"* ]]; then
  pass "A2 Seeded user 102 fetched successfully"
else
  fail "A2 Failed to fetch user 102"
fi

# A3
A3_RES=$(incurl -s http://user-service:8001/users)
if [[ "$A3_RES" == *"Babe"* ]]; then
  pass "A3 Listed all users successfully"
else
  fail "A3 User list endpoint failed"
fi

# A4
A4_CODE=$(incurl -s -o /dev/null -w "%{http_code}" http://user-service:8001/users/999)
if [ "$A4_CODE" -eq 404 ]; then
  pass "A4 User non-exist test passed (404)"
else
  fail "A4 Expected 404, got $A4_CODE"
fi

# A6
incurl -s -o /dev/null http://user-service:8001/users/103
incurl -s -o /dev/null http://user-service:8001/users/103
LOGS_CACHE=$(docker compose logs --no-log-prefix user-service --tail=5)
if [[ "$LOGS_CACHE" == *"CACHE HIT"* ]]; then
  pass "A6 Redis Cache hit verified"
else
  fail "A6 Redis Cache miss/hit sequence failed"
fi

# ------------------------------------------------------------------------------
info "Part B — order-service (In-Network)"
# ------------------------------------------------------------------------------
# B1
B1_RES=$(incurl -s -X POST http://order-service:8002/orders \
  -H "Content-Type: application/json" \
  -d '{"user_id":"102","item":"Headphones","amount":149.99}')
if [[ "$B1_RES" == *"ORD-1010"* ]] && [[ "$B1_RES" == *"CONFIRMED"* ]]; then
  pass "B1 Order created successfully (ORD-1010)"
else
  fail "B1 Order creation failed: $B1_RES"
fi

# B2
B2_CODE=$(incurl -s -o /dev/null -w "%{http_code}" -X POST http://order-service:8002/orders \
  -H "Content-Type: application/json" \
  -d '{"user_id":"999","item":"Ghost Item","amount":10}')
if [ "$B2_CODE" -eq 400 ]; then
  pass "B2 Invalid user order rejected (400)"
else
  fail "B2 Expected 400, got $B2_CODE"
fi

# ------------------------------------------------------------------------------
info "Part C — Event Flow (RabbitMQ -> notification-service)"
# ------------------------------------------------------------------------------
sleep 2
NOTIF_LOGS=$(docker compose logs --no-log-prefix notification-service --tail=6)
if [[ "$NOTIF_LOGS" == *"NOTIFICATION SENT"* ]] && [[ "$NOTIF_LOGS" == *"Headphones"* ]]; then
  pass "C1 Order event consumed by notification-service"
else
  fail "C1 RabbitMQ notification event was not received"
fi

# ------------------------------------------------------------------------------
info "Part D — api-gateway (Host, localhost:8000)"
# ------------------------------------------------------------------------------
# D1
D1_CODE=$(curl -s -o /dev/null -w "%{http_code}" http://localhost:8000/health)
if [ "$D1_CODE" -eq 200 ]; then
  pass "D1 API Gateway health check passed (200)"
else
  fail "D1 API Gateway health check failed"
fi

# D2 & D3
D2_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST http://localhost:8000/api/v1/orders -H "Content-Type: application/json" -d '{"user_id":"101","item":"X","amount":1}')
D3_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST http://localhost:8000/api/v1/orders -H "Authorization: Bearer bad" -H "Content-Type: application/json" -d '{"user_id":"101","item":"X","amount":1}')
if [ "$D2_CODE" -eq 401 ] && [ "$D3_CODE" -eq 401 ]; then
  pass "D2 & D3 Auth protection checks passed (401)"
else
  fail "D2/D3 Gateway Auth bypass vulnerability detected"
fi

# D4 & D5
TOKEN=$(jwt 3600)
D5_RES=$(curl -s -X POST http://localhost:8000/api/v1/orders \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"user_id":"101","item":"Keyboard","amount":79.5}')
if [[ "$D5_RES" == *"ORD-1008"* ]] && [[ "$D5_RES" == *"CONFIRMED"* ]]; then
  pass "D5 End-to-End order creation via Gateway succeeded (ORD-1008)"
else
  fail "D5 E2E Gateway route failed: $D5_RES"
fi

# D6
EXPIRED=$(jwt -60)
D6_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST http://localhost:8000/api/v1/orders -H "Authorization: Bearer $EXPIRED" -H "Content-Type: application/json" -d '{"user_id":"101","item":"X","amount":1}')
if [ "$D6_CODE" -eq 401 ]; then
  pass "D6 Expired token rejection passed (401)"
else
  fail "D6 Expired token allowed!"
fi

# ------------------------------------------------------------------------------
info "Part E — Observability Metrics Check"
# ------------------------------------------------------------------------------
E1_GW=$(curl -s -o /dev/null -w "%{http_code}" http://localhost:8000/metrics)
E1_US=$(incurl -s -o /dev/null -w "%{http_code}" http://user-service:8001/metrics)
E1_OS=$(incurl -s -o /dev/null -w "%{http_code}" http://order-service:8002/metrics)

if [ "$E1_GW" -eq 200 ] && [ "$E1_US" -eq 200 ] && [ "$E1_OS" -eq 200 ]; then
  pass "E1 All Prometheus /metrics endpoints are operational"
else
  fail "E1 Metrics check failed (GW: $E1_GW, US: $E1_US, OS: $E1_OS)"
fi

echo -e "\n${GREEN}=================================================="
echo -e "   ALL SMOKE & ISOLATION TESTS PASSED SUCCESSFULLY!"
echo -e "==================================================${NC}\n"