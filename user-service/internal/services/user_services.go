package services

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"user-service/internal/model"
	"user-service/internal/repository"

	"github.com/redis/go-redis/v9"
)

type UserService struct {
	repo  *repository.UserRepository
	redis *redis.Client
}

func NewUserService(repo *repository.UserRepository, redis *redis.Client) *UserService {
	return &UserService{
		repo:  repo,
		redis: redis,
	}
}

func (s *UserService) GetUsers(ctx context.Context) ([]*model.User, error) {
	cacheKey := "users:all"

	// 1. Redis Cache check
	cachedUsers, err := s.redis.Get(ctx, cacheKey).Result()
	if err == nil {
		var users []*model.User
		if err := json.Unmarshal([]byte(cachedUsers), &users); err == nil && users != nil {
			fmt.Println("⚡ [CACHE HIT] All users found in Redis!")
			return users, nil
		}
	}

	// 2. Cache MISS -> Fetch from DB
	fmt.Println("🐢 [CACHE MISS] Fetching all users from PostgreSQL...")
	users, err := s.repo.GetUsers(ctx)
	if err != nil {
		return nil, err
	}

	// 3. Save to Redis
	usersBytes, err := json.Marshal(users)
	if err == nil {
		err = s.redis.Set(ctx, cacheKey, usersBytes, 10*time.Minute).Err()
		if err != nil {
			log.Printf("Failed to cache users list in Redis: %v\n", err)
		} else {
			fmt.Println("💾 All users saved to Redis Cache (Valid for 10 mins)")
		}
	}

	return users, nil
}

func (s *UserService) GetUserByID(ctx context.Context, id string) (*model.User, error) {
	cacheKey := fmt.Sprintf("user:%s", id)

	// 1. Redis Cache check
	cachedUser, err := s.redis.Get(ctx, cacheKey).Result()
	if err == nil {
		fmt.Printf("⚡ [CACHE HIT] User %s found in Redis!\n", id)
		var user model.User
		if err := json.Unmarshal([]byte(cachedUser), &user); err == nil {
			return &user, nil
		}
	}

	// 2. Cache MISS -> Fetch from DB
	fmt.Printf("🐢 [CACHE MISS] Fetching User %s from PostgreSQL...\n", id)
	user, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	// 3. Save to Redis
	userBytes, err := json.Marshal(user)
	if err == nil {
		err = s.redis.Set(ctx, cacheKey, userBytes, 10*time.Minute).Err()
		if err != nil {
			log.Printf("Failed to cache user in Redis: %v\n", err)
		} else {
			fmt.Printf("💾 User %s saved to Redis Cache (Valid for 10 mins)\n", id)
		}
	}

	return user, nil
}
