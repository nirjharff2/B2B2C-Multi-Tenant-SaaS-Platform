package messaging

import (
	"encoding/json"
	"log"

	"order-service/internal/model"

	amqp "github.com/rabbitmq/amqp091-go"
)

type RabbitClient interface {
	PublishNotification(event model.OrderCreatedEvent)
	Close()
}

type rabbitClient struct {
	conn    *amqp.Connection
	channel *amqp.Channel
}

func InitRabbitMQ() RabbitClient {
	conn, err := amqp.Dial("amqp://guest:guest@rabbitmq:5672/")
	if err != nil {
		log.Fatalf("RabbitMQ Connection Error: %v\n", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		log.Fatalf("RabbitMQ Channel Error: %v\n", err)
	}

	_, err = ch.QueueDeclare(
		"order_notifications",
		true,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		log.Fatalf("Queue Declare Error: %v\n", err)
	}

	log.Println("Order Service: RabbitMQ Connected!")
	return &rabbitClient{
		conn:    conn,
		channel: ch,
	}
}

func (r *rabbitClient) PublishNotification(event model.OrderCreatedEvent) {
	body, _ := json.Marshal(event)

	err := r.channel.Publish(
		"",
		"order_notifications",
		false,
		false,
		amqp.Publishing{
			ContentType: "application/json",
			Body:        body,
		},
	)
	if err != nil {
		log.Printf("Failed to publish notification event: %v\n", err)
	} else {
		log.Println("--> RabbitMQ-তে Order Event পাঠানো হয়েছে!")
	}
}

func (r *rabbitClient) Close() {
	if r.channel != nil {
		r.channel.Close()
	}
	if r.conn != nil {
		r.conn.Close()
	}
}