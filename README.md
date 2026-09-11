# Go Microservices Application

A distributed event-driven microservices architecture built with Go, PostgreSQL, Redis, and RabbitMQ. 

## 🏗 System Architecture

The project consists of three independent microservices interacting via REST endpoints and RabbitMQ message queues:

* **User Service (`:8001`)**: Manages user profiles using PostgreSQL for storage and Redis for caching.
* **Order Service (`:8002`)**: Handles order creation, persists data to PostgreSQL, verifies users via the User Service, and publishes order events to RabbitMQ.
* **Notification Service**: Background consumer that listens to RabbitMQ events and processes notification tasks.

### Infrastructure Components
* **User DB (`PostgreSQL`)**: Host database for `user-service` running on port `5432`.
* **Order DB (`PostgreSQL`)**: Host database for `order-service` running on port `5433`.
* **Redis (`:6379`)**: High-performance caching layer for quick user lookups.
* **RabbitMQ (`:5672`, Management UI: `:15672`)**: Asynchronous event bus and message broker.

---

## 🛠 Prerequisites

Make sure you have the following installed on your machine:
* [Docker Desktop](https://www.docker.com/products/docker-desktop/)
* [Git](https://git-scm.com/)
* `curl` or Postman for testing endpoints

---

## 🚀 Quick Start

### 1. Clone the Repository
```bash
git clone [https://github.com/your-username/go-microservices.git](https://github.com/your-username/go-microservices.git)
cd go-microservices"# Distributed-Delivery-Logistics-Platform" 
