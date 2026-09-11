package config

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
)

func InitDB() *pgxpool.Pool {
	connStr := "postgres://postgres:secret@order-db:5432/orderdb?sslmode=disable"
	dbPool, err := pgxpool.New(context.Background(), connStr)
	if err != nil {
		log.Fatalf("Database Connection Error: %v\n", err)
	}

	query := `
	CREATE TABLE IF NOT EXISTS orders (
		id SERIAL PRIMARY KEY,
		order_id VARCHAR(50) UNIQUE NOT NULL,
		user_id VARCHAR(50) NOT NULL,
		customer_name VARCHAR(100),
		item VARCHAR(100),
		amount NUMERIC(10, 2),
		status VARCHAR(50)
	);`

	_, err = dbPool.Exec(context.Background(), query)
	if err != nil {
		log.Fatalf("Failed to create orders table: %v\n", err)
	}

	log.Println("Order Service: PostgreSQL Connected & Table Ready!")
	return dbPool
}
