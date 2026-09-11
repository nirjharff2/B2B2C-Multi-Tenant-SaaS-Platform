package main

import (
	"encoding/json"
	"fmt"
	"log"

	amqp "github.com/rabbitmq/amqp091-go"
)

type OrderCreatedEvent struct {
	OrderID       string  `json:"order_id"`
	CustomerName  string  `json:"customer_name"`
	CustomerEmail string  `json:"customer_email"`
	Item          string  `json:"item"`
	Amount        float64 `json:"amount"`
}

func main() {
	// RabbitMQ Connection
	// conn, err := amqp.Dial("amqp://guest:guest@localhost:5672/")
	conn, err := amqp.Dial("amqp://guest:guest@rabbitmq:5672/")
	if err != nil {
		log.Fatalf("RabbitMQ Connection Failed: %v", err)
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		log.Fatalf("Failed to open channel: %v", err)
	}
	defer ch.Close()

	// Queue নাম ডিক্লেয়ার করা
	q, err := ch.QueueDeclare(
		"order_notifications",
		true,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		log.Fatalf("Failed to declare queue: %v", err)
	}

	// Message শোনার জন্য Consumer সেটআপ করা
	msgs, err := ch.Consume(
		q.Name, // Queue
		"",     // Consumer
		true,   // Auto-Ack
		false,  // Exclusive
		false,  // No-local
		false,  // No-Wait
		nil,    // Args
	)
	if err != nil {
		log.Fatalf("Failed to register consumer: %v", err)
	}

	forever := make(chan bool)

	go func() {
		for d := range msgs {
			var event OrderCreatedEvent
			err := json.Unmarshal(d.Body, &event)
			if err != nil {
				log.Printf("Error decoding message: %v", err)
				continue
			}

			// ইমেইল বা নোটিফিকেশন পাঠানোর সিমুলেশন
			fmt.Println("\n==================================================")
			fmt.Printf("📧 [NOTIFICATION SENT] Customer: %s (%s)\n", event.CustomerName, event.CustomerEmail)
			fmt.Printf("   Dear %s, your order %s for '%s' (Amount: $%.2f) is CONFIRMED!\n", event.CustomerName, event.OrderID, event.Item, event.Amount)
			fmt.Println("==================================================")
		}
	}()

	fmt.Println(" Notification Service চালু হয়েছে! RabbitMQ ইভেন্টের জন্য অপেক্ষা করছে...")
	<-forever
}
