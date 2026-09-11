package service

import (
	"context"
	"fmt"

	"order-service/internal/client"
	"order-service/internal/messaging"
	"order-service/internal/model"
	"order-service/internal/repository"
)

type OrderService interface {
	CreateOrder(ctx context.Context, req model.OrderRequest) (*model.OrderResponse, error)
}

type orderService struct {
	repo         repository.OrderRepository
	userClient   client.UserClient
	rabbitClient messaging.RabbitClient
}

func NewOrderService(repo repository.OrderRepository, userClient client.UserClient, rabbitClient messaging.RabbitClient) OrderService {
	return &orderService{
		repo:         repo,
		userClient:   userClient,
		rabbitClient: rabbitClient,
	}
}

func (s *orderService) CreateOrder(ctx context.Context, req model.OrderRequest) (*model.OrderResponse, error) {
	// 1. Get user details from user client
	user, err := s.userClient.GetUserByID(req.UserID)
	if err != nil {
		return nil, err
	}

	orderID := fmt.Sprintf("ORD-%d", 1000+len(req.Item))

	// 2. Save order to database
	err = s.repo.SaveOrder(ctx, orderID, req.UserID, user.Name, req.Item, req.Amount, "CONFIRMED")
	if err != nil {
		return nil, err
	}

	// 3. Publish RabbitMQ event asynchronously
	event := model.OrderCreatedEvent{
		OrderID:       orderID,
		CustomerName:  user.Name,
		CustomerEmail: user.Email,
		Item:          req.Item,
		Amount:        req.Amount,
	}
	go s.rabbitClient.PublishNotification(event)

	return &model.OrderResponse{
		OrderID:      orderID,
		CustomerName: user.Name,
		Item:         req.Item,
		Amount:       req.Amount,
		Status:       "CONFIRMED",
	}, nil
}
