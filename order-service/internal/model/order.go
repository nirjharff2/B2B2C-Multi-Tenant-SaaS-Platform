package model

type OrderRequest struct {
	UserID string  `json:"user_id"`
	Item   string  `json:"item"`
	Amount float64 `json:"amount"`
}

type User struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type OrderResponse struct {
	OrderID      string  `json:"order_id"`
	CustomerName string  `json:"customer_name"`
	Item         string  `json:"item"`
	Amount       float64 `json:"amount"`
	Status       string  `json:"status"`
}

type OrderCreatedEvent struct {
	OrderID       string  `json:"order_id"`
	CustomerName  string  `json:"customer_name"`
	CustomerEmail string  `json:"customer_email"`
	Item          string  `json:"item"`
	Amount        float64 `json:"amount"`
}
