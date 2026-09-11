package main

import (
	"fmt"
	"log"
	"net/http"

	"order-service/internal/client"
	"order-service/internal/config"
	"order-service/internal/handler"
	"order-service/internal/messaging"
	"order-service/internal/repository"
	"order-service/internal/service"
)

func main() {
	// Initialize Database Pool
	dbPool := config.InitDB()
	defer dbPool.Close()

	// Initialize RabbitMQ
	rabbitClient := messaging.InitRabbitMQ()
	defer rabbitClient.Close()

	// Dependency Injection Setup
	userClient := client.NewUserClient("http://user-service:8001")
	orderRepo := repository.NewOrderRepository(dbPool)
	orderService := service.NewOrderService(orderRepo, userClient, rabbitClient)
	orderHandler := handler.NewOrderHandler(orderService)

	http.HandleFunc("/orders", orderHandler.CreateOrderHandler)

	fmt.Println("Order Service 8002 পোর্টে চালু হচ্ছে...")
	log.Fatal(http.ListenAndServe(":8002", nil))
}
