package client

import (
	"encoding/json"
	"fmt"
	"net/http"

	"order-service/internal/model"
)

type UserClient interface {
	GetUserByID(userID string) (*model.User, error)
}

type userClient struct {
	baseURL string
}

func NewUserClient(baseURL string) UserClient {
	return &userClient{baseURL: baseURL}
}

func (c *userClient) GetUserByID(userID string) (*model.User, error) {
	userServiceURL := fmt.Sprintf("%s/users/%s", c.baseURL, userID)
	resp, err := http.Get(userServiceURL)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("user not found or service unavailable")
	}
	defer resp.Body.Close()

	var user model.User
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}

	return &user, nil
}
