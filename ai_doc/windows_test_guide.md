Here is the easiest way to run and test everything from Windows CMD.

1. Open CMD in the project folder
cd C:\path\to\your\go-microservices

Check:

dir

You should see things such as:

docker-compose.yml
api-gateway
user-service
order-service
notification-service
...

Make sure Docker Desktop is running.

2. Build and start everything

Run:

docker compose up -d --build

Then:

docker compose ps

You want the containers to be Up.

Your expected architecture is:

                    HOST
                     |
                  :8000
                     |
               API Gateway
                /       \
               /         \
       user-service    order-service
          :8001           :8002
             \             /
              \           /
               internal-net
                    |
          ---------------------
          |         |         |
       user-db    order-db   RabbitMQ
                    |
             notification-service

Importantly, only the gateway and RabbitMQ management UI should be exposed to your host. The user and order services should not be accessible through localhost:8001 / 8002.

3. First test: container/network setup

Run:

docker compose ps

Look at the PORTS column.

You should see something similar to:

api-gateway       0.0.0.0:8000->8000/tcp
rabbitmq          0.0.0.0:15672->15672/tcp

But not:

user-service      0.0.0.0:8001->8001/tcp
order-service     0.0.0.0:8002->8002/tcp
user-db           0.0.0.0:5432->5432/tcp
order-db          0.0.0.0:5433->5432/tcp
redis             0.0.0.0:6379->6379/tcp

That is your first security check.

4. Reset the order database

The test guide specifically says to do this before rerunning the order tests because order IDs aren't actually unique.

Run:

docker compose up -d --force-recreate --renew-anon-volumes order-db order-service

Then:

docker compose exec -T order-db psql -U postgres -d orderdb -tAc "select count(*) from orders;"

Expected:

0
5. Test Gateway

This is the easiest part because port 8000 is exposed.

Test 1 — Health
curl -s -w "\nHTTP:%%{http_code}\n" http://localhost:8000/health

Expected:

{"status":"API Gateway Healthy"}
HTTP:200

The guide identifies this as D1.

6. Test JWT protection
No token
curl -s -w "\nHTTP:%%{http_code}\n" -X POST http://localhost:8000/api/v1/orders -H "Content-Type: application/json" -d "{\"user_id\":\"101\",\"item\":\"X\",\"amount\":1}"

Expected:

{"error":"Authorization header required"}
HTTP:401
Fake token
curl -s -w "\nHTTP:%%{http_code}\n" -X POST http://localhost:8000/api/v1/orders -H "Authorization: Bearer not-a-real-token" -H "Content-Type: application/json" -d "{\"user_id\":\"101\",\"item\":\"X\",\"amount\":1}"

Expected:

{"error":"Invalid or expired token"}
HTTP:401

These correspond to D2 and D3.

7. Generate a valid JWT in CMD

The guide uses a Bash function for this, but don't use that in CMD.

You can run Node directly.

First create a valid token:

node -e "const c=require('crypto'),b=s=>Buffer.from(s).toString('base64url');const h=b(JSON.stringify({alg:'HS256',typ:'JWT'}));const p=b(JSON.stringify({user_id:'101',role:'customer',exp:Math.floor(Date.now()/1000)+3600}));console.log(h+'.'+p+'.'+c.createHmac('sha256','super-secret-key').update(h+'.'+p).digest('base64url'));"

Copy the output.

It will look like:

eyJhbGciOiJIUzI1NiIs...

Then:

set TOKEN=PASTE_YOUR_TOKEN_HERE

Check:

echo %TOKEN%
8. Test complete order flow through Gateway

This is the most important test in your document.

Run:

curl -s -w "\nHTTP:%%{http_code}\n" -X POST http://localhost:8000/api/v1/orders -H "Authorization: Bearer %TOKEN%" -H "Content-Type: application/json" -d "{\"user_id\":\"101\",\"item\":\"Keyboard\",\"amount\":79.5}"

Expected:

{"order_id":"ORD-1008","customer_name":"Rahim","item":"Keyboard","amount":79.5,"status":"CONFIRMED"}
HTTP:201

This single test verifies:

Client
  ↓
JWT authentication
  ↓
API Gateway
  ↓
/api/v1 rewrite
  ↓
order-service
  ↓
user-service
  ↓
PostgreSQL
  ↓
RabbitMQ
  ↓
notification-service

The guide explicitly calls D5 the most important end-to-end test.

9. Test expired JWT

Generate an expired token:

node -e "const c=require('crypto'),b=s=>Buffer.from(s).toString('base64url');const h=b(JSON.stringify({alg:'HS256',typ:'JWT'}));const p=b(JSON.stringify({user_id:'101',role:'customer',exp:Math.floor(Date.now()/1000)-60}));console.log(h+'.'+p+'.'+c.createHmac('sha256','super-secret-key').update(h+'.'+p).digest('base64url'));"

Copy it:

set EXPIRED=PASTE_EXPIRED_TOKEN_HERE

Then:

curl -s -w "\nHTTP:%%{http_code}\n" -X POST http://localhost:8000/api/v1/orders -H "Authorization: Bearer %EXPIRED%" -H "Content-Type: application/json" -d "{\"user_id\":\"101\",\"item\":\"X\",\"amount\":1}"

Expected:

{"error":"Invalid or expired token"}
HTTP:401

10. Test that backend ports are actually blocked

From CMD on Windows:

curl -s -m 3 http://localhost:8001/users

and:

curl -s -m 3 http://localhost:8002/orders

These should fail to connect.

That's intentional.

The architecture says user-service:8001 and order-service:8002 should only be reachable from the Docker network, not directly from your computer.

11. Test user-service internally

Because you're using CMD, don't use the Bash incurl function.

Instead run:

docker run --rm --network go-microservices_internal-net curlimages/curl:8.11.1 -s -w "\nHTTP:%%{http_code}\n" http://user-service:8001/users/101

Expected:

{"id":"101","name":"Rahim","email":"rahim@example.com"}
HTTP:200
User 102
docker run --rm --network go-microservices_internal-net curlimages/curl:8.11.1 -s -w "\nHTTP:%%{http_code}\n" http://user-service:8001/users/102

Expected:

{"id":"102","name":"Karim","email":"karim@example.com"}
HTTP:200
All users
docker run --rm --network go-microservices_internal-net curlimages/curl:8.11.1 -s -w "\nHTTP:%%{http_code}\n" http://user-service:8001/users

Expected: 3 users and HTTP:200.

12. Test user not found
docker run --rm --network go-microservices_internal-net curlimages/curl:8.11.1 -s -w "\nHTTP:%%{http_code}\n" http://user-service:8001/users/999

Expected:

{"message":"User not found"}
HTTP:404
13. Test Redis cache

Run the same request twice:

docker run --rm --network go-microservices_internal-net curlimages/curl:8.11.1 -s http://user-service:8001/users/103

Run again:

docker run --rm --network go-microservices_internal-net curlimages/curl:8.11.1 -s http://user-service:8001/users/103

Then:

docker compose logs --no-log-prefix user-service --tail=10

You should see something like:

[CACHE MISS] Fetching User 103 from PostgreSQL...
User 103 saved to Redis Cache
[CACHE HIT] User 103 found in Redis!

That proves the Redis cache is actually being used.

14. Test order-service internally
Valid order
docker run --rm --network go-microservices_internal-net curlimages/curl:8.11.1 -s -w "\nHTTP:%%{http_code}\n" -X POST http://order-service:8002/orders -H "Content-Type: application/json" -d "{\"user_id\":\"102\",\"item\":\"Headphones\",\"amount\":149.99}"

Expected:

{"order_id":"ORD-1010","customer_name":"Karim","item":"Headphones","amount":149.99,"status":"CONFIRMED"}
HTTP:201

Invalid user
docker run --rm --network go-microservices_internal-net curlimages/curl:8.11.1 -s -w "\nHTTP:%%{http_code}\n" -X POST http://order-service:8002/orders -H "Content-Type: application/json" -d "{\"user_id\":\"999\",\"item\":\"Ghost Item\",\"amount\":10}"

Expected:

user not found or service unavailable
HTTP:400
15. Test malformed JSON
docker run --rm --network go-microservices_internal-net curlimages/curl:8.11.1 -s -w "\nHTTP:%%{http_code}\n" -X POST http://order-service:8002/orders -H "Content-Type: application/json" -d "not-json"

Expected:

Invalid Body
HTTP:400
16. Test RabbitMQ notification

After successfully creating an order, run:

docker compose logs --no-log-prefix notification-service --tail=10

You should see the notification for the order, e.g.:

[NOTIFICATION SENT] Customer: Karim
Dear Karim, your order ORD-1010 for 'Headphones' ...

This proves:

order-service
      ↓
RabbitMQ
      ↓
notification-service

17. Open RabbitMQ UI

In your browser:

RabbitMQ Management UI

Login:

Username: guest
Password: guest

Then:

Queues
   ↓
order_notifications

You want:

Consumers: 1
Messages: 0

consumers=1 means the notification service is connected, and messages=0 means there is no backlog.

18. Test the network isolation properly
Gateway → user-service should work
docker compose exec -T api-gateway nc -z -w 3 user-service 8001

Should return successfully.

Gateway → database should NOT work
docker compose exec -T api-gateway nc -z -w 3 user-db 5432

Should fail.

Gateway → Redis should NOT work
docker compose exec -T api-gateway nc -z -w 3 redis 6379

Should fail.

This verifies that the gateway can access the application layer but cannot directly access the data tier.

19. Test no internet access from internal services
docker compose exec -T user-service nc -z -w 3 1.1.1.1 443

Expected failure.

Your internal networks are intentionally configured with no outbound route.

20. Test monitoring

If you have docker-compose.monitoring.yml, run:

docker compose -f docker-compose.yml -f docker-compose.monitoring.yml up -d

Then:

docker compose -f docker-compose.yml -f docker-compose.monitoring.yml ps

Wait around 20 seconds.

Then open:

Prometheus

and:

Grafana

Grafana credentials from your guide:

Username: admin
Password: admin

21. Test metrics

Gateway:

curl http://localhost:8000/metrics

Should return metrics.

For the backend services, use the Docker network:

docker run --rm --network go-microservices_internal-net curlimages/curl:8.11.1 -s -o /dev/null -w "HTTP:%%{http_code}\n" http://user-service:8001/metrics

and:

docker run --rm --network go-microservices_internal-net curlimages/curl:8.11.1 -s -o /dev/null -w "HTTP:%%{http_code}\n" http://order-service:8002/metrics

Expected:

HTTP:200
HTTP:200

22. Finally, check logs

If anything fails:

docker compose logs api-gateway
docker compose logs user-service
docker compose logs order-service
docker compose logs notification-service

Or everything:

docker compose logs --tail=100

Check container health:

docker compose ps
Your complete PASS checklist

You can use this as your final verification:

[ ] docker compose up -d --build
[ ] All containers Up
[ ] user-db healthy
[ ] order-db healthy
[ ] rabbitmq healthy

NETWORK
[ ] localhost:8001 blocked
[ ] localhost:8002 blocked
[ ] database ports blocked
[ ] Redis port blocked
[ ] RabbitMQ AMQP port blocked
[ ] gateway → user-service works
[ ] gateway → database blocked
[ ] gateway → Redis blocked
[ ] user-service → internet blocked

USER SERVICE
[ ] GET user 101 → 200
[ ] GET user 102 → 200
[ ] GET users → 200 / 3 users
[ ] GET user 999 → 404
[ ] Redis MISS → HIT

ORDER SERVICE
[ ] Valid order → 201
[ ] Invalid user → 400
[ ] Invalid JSON → 400
[ ] GET /orders → 405

RABBITMQ
[ ] Order event published
[ ] notification-service consumes event
[ ] consumers = 1
[ ] messages = 0

API GATEWAY
[ ] /health → 200
[ ] No JWT → 401
[ ] Fake JWT → 401
[ ] Valid JWT → 201
[ ] Expired JWT → 401

MONITORING
[ ] gateway /metrics → 200
[ ] user-service /metrics → 200
[ ] order-service /metrics → 200
[ ] Prometheus targets → UP
[ ] Grafana → 200

This matches the pass/fail checklist in your test document.