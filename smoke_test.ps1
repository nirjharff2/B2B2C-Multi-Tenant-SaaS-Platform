# ==============================================================================
# Automated Smoke & Isolation Test Suite for Windows PowerShell
# ==============================================================================

$GREEN = "`e[32m"
$RED = "`e[31m"
$YELLOW = "`e[33m"
$NC = "`e[0m"

function Write-Pass($msg) { Write-Host "  [$GREEN`PASSED$NC] $msg" }
function Write-Fail($msg) { Write-Host "  [$RED`FAILED$NC] $msg"; exit 1 }
function Write-Info($msg) { Write-Host "`n$YELLOW===> $msg$NC" }

# Get Project Name from current directory for Docker Network
$PROJECT_NAME = (Get-Item .).Name.ToLower() -replace '[^a-z0-9]', ''
$NETWORK_NAME = "${PROJECT_NAME}_internal-net"

function incurl($argsList) {
    docker run --rm --network $NETWORK_NAME curlimages/curl:8.11.1 $argsList
}

function Get-JwtToken($expSeconds) {
    node -e "
const c=require('crypto'), b=s=>Buffer.from(s).toString('base64url');
const h=b(JSON.stringify({alg:'HS256',typ:'JWT'}));
const p=b(JSON.stringify({user_id:'101',role:'customer',exp:Math.floor(Date.now()/1000)+Number(process.argv[1])}));
console.log(h+'.'+p+'.'+c.createHmac('sha256','super-secret-key').update(h+'.'+p).digest('base64url'));
" -- $expSeconds
}

# ------------------------------------------------------------------------------
Write-Info "0. Setup & Database Cleanup"
# ------------------------------------------------------------------------------
Write-Host "Bringing up Docker containers..."
docker compose up -d --build | Out-Null

Write-Host "Resetting orders database..."
docker compose up -d --force-recreate --renew-anon-volumes order-db order-service | Out-Null

Start-Sleep -Seconds 5

$ORDER_COUNT = (docker compose exec -T order-db psql -U postgres -d orderdb -tAc "select count(*) from orders;" 2>$null).Trim()
if ($ORDER_COUNT -eq "0") {
    Write-Pass "0.3 Orders database reset successfully (count: 0)"
} else {
    Write-Fail "0.3 Reset failed. Orders count is '$ORDER_COUNT', expected '0'"
}

# ------------------------------------------------------------------------------
Write-Info "Part N — Network Exposure & Isolation"
# ------------------------------------------------------------------------------
$EXIT1 = (curl.exe -s -m 3 -o NUL http://localhost:8001/users; echo $LASTEXITCODE)
$EXIT2 = (curl.exe -s -m 3 -o NUL http://localhost:8002/orders; echo $LASTEXITCODE)
if ($EXIT1 -ne 0 -and $EXIT2 -ne 0) {
    Write-Pass "N1 Backends are NOT reachable from host (Exit codes: $EXIT1, $EXIT2)"
} else {
    Write-Fail "N1 Backends are exposed on host!"
}

$N2_PASS = $true
foreach ($p in @(5433, 6379, 5672)) {
    $C_EXIT = (curl.exe -s -m 2 -o NUL http://localhost:$p; echo $LASTEXITCODE)
    if ($C_EXIT -eq 0) { $N2_PASS = $false }
}
if ($N2_PASS) {
    Write-Pass "N2 Datastores and broker are NOT reachable from host"
} else {
    Write-Fail "N2 Datastores exposed on host ports!"
}

# ------------------------------------------------------------------------------
Write-Info "Part A — user-service (In-Network)"
# ------------------------------------------------------------------------------
$A1_RES = incurl @("-s", "http://user-service:8001/users/101")
if ($A1_RES -match "Rahim") {
    Write-Pass "A1 Seeded user 101 fetched successfully"
} else {
    Write-Fail "A1 Failed to fetch user 101"
}

$A3_RES = incurl @("-s", "http://user-service:8001/users")
if ($A3_RES -match "Babe") {
    Write-Pass "A3 Listed all users successfully"
} else {
    Write-Fail "A3 User list endpoint failed"
}

# ------------------------------------------------------------------------------
Write-Info "Part B — order-service (In-Network)"
# ------------------------------------------------------------------------------
$B1_RES = incurl @("-s", "-X", "POST", "http://order-service:8002/orders", "-H", "Content-Type: application/json", "-d", '{"user_id":"102","item":"Headphones","amount":149.99}')
if ($B1_RES -match "ORD-1010" -and $B1_RES -match "CONFIRMED") {
    Write-Pass "B1 Order created successfully (ORD-1010)"
} else {
    Write-Fail "B1 Order creation failed: $B1_RES"
}

# ------------------------------------------------------------------------------
Write-Info "Part C — Event Flow (RabbitMQ -> notification-service)"
# ------------------------------------------------------------------------------
Start-Sleep -Seconds 2
$NOTIF_LOGS = docker compose logs --no-log-prefix notification-service --tail=6
if ($NOTIF_LOGS -match "NOTIFICATION SENT" -and $NOTIF_LOGS -match "Headphones") {
    Write-Pass "C1 Order event consumed by notification-service"
} else {
    Write-Fail "C1 RabbitMQ notification event was not received"
}

# ------------------------------------------------------------------------------
Write-Info "Part D — api-gateway (Host, localhost:8000)"
# ------------------------------------------------------------------------------
$D1_CODE = curl.exe -s -o NUL -w "%{http_code}" http://localhost:8000/health
if ($D1_CODE -eq "200") {
    Write-Pass "D1 API Gateway health check passed (200)"
} else {
    Write-Fail "D1 API Gateway health check failed"
}

$TOKEN = Get-JwtToken 3600
$D5_RES = curl.exe -s -X POST http://localhost:8000/api/v1/orders -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" -d '{"user_id":"101","item":"Keyboard","amount":79.5}'
if ($D5_RES -match "ORD-1008" -and $D5_RES -match "CONFIRMED") {
    Write-Pass "D5 End-to-End order creation via Gateway succeeded (ORD-1008)"
} else {
    Write-Fail "D5 E2E Gateway route failed: $D5_RES"
}

Write-Host "`n$GREEN=================================================="
Write-Host "   ALL SMOKE & ISOLATION TESTS PASSED SUCCESSFULLY!"
Write-Host "==================================================$NC`n"
docker compose down