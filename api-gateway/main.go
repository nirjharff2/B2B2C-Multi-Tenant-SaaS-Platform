package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

type Config struct {
	Port               string
	JWTSecret          []byte
	UserServiceURL     string
	OrderServiceURL    string
	NotificationSvcURL string
}

func getEnv(key, fallback string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return fallback
}

func loadConfig() *Config {
	return &Config{
		Port:               getEnv("PORT", "8000"),
		JWTSecret:          []byte(getEnv("JWT_SECRET", "super-secret-key")),
		UserServiceURL:     getEnv("USER_SERVICE_URL", "http://user-service:8001"),
		OrderServiceURL:    getEnv("ORDER_SERVICE_URL", "http://order-service:8002"),
		NotificationSvcURL: getEnv("NOTIFICATION_SERVICE_URL", "http://notification-service:8003"),
	}
}

// ReverseProxy forwards incoming HTTP requests to downstream microservices
func ReverseProxy(targetURL string) gin.HandlerFunc {
	remote, err := url.Parse(targetURL)
	if err != nil {
		log.Fatalf("Invalid target URL for proxy: %v", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(remote)

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = remote.Host
	}

	return func(c *gin.Context) {
		proxy.ServeHTTP(c.Writer, c.Request)
	}
}

// JWTAuthMiddleware validates the Bearer token and injects user identity into headers
func JWTAuthMiddleware(secret []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authorization header required"})
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid authorization format"})
			return
		}

		tokenString := parts[1]
		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return secret, nil
		})

		if err != nil || !token.Valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
			return
		}

		if claims, ok := token.Claims.(jwt.MapClaims); ok {
			if userID, exists := claims["user_id"]; exists {
				c.Request.Header.Set("X-User-ID", fmt.Sprintf("%v", userID))
			}
			if role, exists := claims["role"]; exists {
				c.Request.Header.Set("X-User-Role", fmt.Sprintf("%v", role))
			}
		}

		c.Next()
	}
}

func CORSMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE, PATCH")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

func main() {
	cfg := loadConfig()

	router := gin.Default()
	router.Use(CORSMiddleware())

	// Health check for gateway container
	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "API Gateway Healthy"})
	})

	userProxy := ReverseProxy(cfg.UserServiceURL)
	orderProxy := ReverseProxy(cfg.OrderServiceURL)
	notificationProxy := ReverseProxy(cfg.NotificationSvcURL)

	api := router.Group("/api/v1")
	{
		// 1. Public Auth routes (bypasses JWT validation)
		api.POST("/users/register", userProxy)
		api.POST("/users/login", userProxy)

		// 2. Protected Routes (requires valid JWT)
		protected := api.Group("")
		protected.Use(JWTAuthMiddleware(cfg.JWTSecret))
		{
			// Specific User routes to avoid wildcard conflicts with /register & /login
			protected.GET("/users/profile", userProxy)
			protected.PUT("/users/profile", userProxy)

			// Order Service wildcard routes
			protected.Any("/orders", orderProxy)
			protected.Any("/orders/*path", orderProxy)

			// Notification Service wildcard routes
			protected.Any("/notifications", notificationProxy)
			protected.Any("/notifications/*path", notificationProxy)
		}
	}

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: router,
	}

	go func() {
		log.Printf("API Gateway running on port %s...", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Gateway server error: %s\n", err)
		}
	}()

	// Graceful Shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down API Gateway...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal("Gateway forced to shutdown:", err)
	}

	log.Println("API Gateway stopped cleanly")
}
