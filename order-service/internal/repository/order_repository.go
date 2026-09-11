package repository

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

type OrderRepository interface {
	SaveOrder(ctx context.Context, orderID, userID, customerName, item string, amount float64, status string) error
}

type orderRepository struct {
	db *pgxpool.Pool
}

func NewOrderRepository(db *pgxpool.Pool) OrderRepository {
	return &orderRepository{db: db}
}

func (r *orderRepository) SaveOrder(ctx context.Context, orderID, userID, customerName, item string, amount float64, status string) error {
	insertQuery := `INSERT INTO orders (order_id, user_id, customer_name, item, amount, status) VALUES ($1, $2, $3, $4, $5, $6)`
	_, err := r.db.Exec(ctx, insertQuery, orderID, userID, customerName, item, amount, status)
	return err
}
