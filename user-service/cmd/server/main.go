package main

import (
	"fmt"
	"log"
	"net/http"

	"user-service/internal/config"
	"user-service/internal/handler"
	"user-service/internal/repository"
	"user-service/internal/services"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func main() {
	dbPool := config.InitDB()
	defer dbPool.Close()

	redisClient := config.InitRedis()
	defer redisClient.Close()

	userRepo := repository.NewUserRepository(dbPool)
	userService := services.NewUserService(userRepo, redisClient)
	userHandler := handler.NewUserHandler(userService)

	http.HandleFunc("GET /users", userHandler.GetUsers)
	http.HandleFunc("GET /users/{id}", userHandler.GetUserByID)
	http.Handle("/metrics", promhttp.Handler())

	fmt.Println("User Service 8001 পোর্টে চালু হচ্ছে...")
	log.Fatal(http.ListenAndServe(":8001", nil))
}
