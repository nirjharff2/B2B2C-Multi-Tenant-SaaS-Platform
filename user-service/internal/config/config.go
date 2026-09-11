package config

import (
	"context"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type Config struct {
	DBPool      *pgxpool.Pool
	RedisClient *redis.Client
}

func InitDB() *pgxpool.Pool {
	connStr := "postgres://postgres:secret@user-db:5432/userdb?sslmode=disable"

	dbPool, err := pgxpool.New(context.Background(), connStr)
	if err != nil {
		log.Fatalf("Database Connection Error: %v\n", err)
	}

	query := `
	CREATE TABLE IF NOT EXISTS users (
		id VARCHAR(50) PRIMARY KEY,
		name VARCHAR(100),
		email VARCHAR(100)
	);`
	_, err = dbPool.Exec(context.Background(), query)
	if err != nil {
		log.Fatalf("Failed to create table: %v\n", err)
	}

	seedQuery := `
	INSERT INTO users (id, name, email) 
	VALUES ('101', 'Rahim', 'rahim@example.com'), ('102', 'Karim', 'karim@example.com')
	ON CONFLICT (id) DO NOTHING;`
	dbPool.Exec(context.Background(), seedQuery)

	fmt.Println("User Service: PostgreSQL Connected!")
	return dbPool
}

func InitRedis() *redis.Client {
	redisClient := redis.NewClient(&redis.Options{
		Addr:     "redis:6379",
		Password: "",
		DB:       0,
	})

	_, err := redisClient.Ping(context.Background()).Result()
	if err != nil {
		log.Fatalf("Redis Connection Error: %v\n", err)
	}

	fmt.Println("User Service: Redis Cache Connected!")
	return redisClient
}
