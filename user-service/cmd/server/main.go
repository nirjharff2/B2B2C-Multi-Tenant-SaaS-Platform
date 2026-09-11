package main

import (
	"fmt"
	"log"
	"net/http"

	"user-service/internal/config"
	"user-service/internal/handler"
	"user-service/internal/repository"
	"user-service/internal/services"
)

func main() {
	dbPool := config.InitDB()
	defer dbPool.Close()

	redisClient := config.InitRedis()
	defer redisClient.Close()

	userRepo := repository.NewUserRepository(dbPool)
	userService := services.NewUserService(userRepo, redisClient)
	userHandler := handler.NewUserHandler(userService)

	http.HandleFunc("/users/", userHandler.GetUser)

	fmt.Println("User Service 8001 পোর্টে চালু হচ্ছে...")
	log.Fatal(http.ListenAndServe(":8001", nil))
}
