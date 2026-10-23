package pulse

import "fmt"

// Notification matches NotifyHub's POST /api/notify contract exactly.
type Notification struct {
	Recipient string `json:"recipient"`
	Channel   string `json:"channel"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
}

// Scenario generates repeatable business traffic, independent of transport.
type Scenario interface {
	Notification(index int) Notification
}

type scenario string

func NewScenario(name string) (Scenario, error) {
	switch name {
	case "normal", "campaign", "duplicates":
		return scenario(name), nil
	default:
		return nil, fmt.Errorf("unknown scenario %q (choose normal, campaign, or duplicates)", name)
	}
}

func (s scenario) Notification(index int) Notification {
	account := 1000 + index%200
	if s == "duplicates" {
		bodies := []string{
			"Invoice 1042 is overdue. Please update your payment details.",
			"Invoice 1042 is overdue. Please update your payment details. Thank you.",
			"INVOICE 1042 IS OVERDUE! Please update your payment details.",
		}
		return Notification{
			Recipient: "billing@example.com", Channel: "email", Subject: "Payment reminder",
			Body: fmt.Sprintf("%s Reference %d.", bodies[index%len(bodies)], 10000+index),
		}
	}
	if s == "campaign" && index%10 != 9 {
		return Notification{
			Recipient: fmt.Sprintf("customer-%d@example.com", account), Channel: "email",
			Subject: "Autumn rewards campaign",
			Body:    fmt.Sprintf("Hello customer %d, your autumn reward is ready. Use offer AUTUMN20 before Friday.", account),
		}
	}

	// Six email, two SMS, and two push notifications per ten requests.
	channel := []string{"email", "email", "sms", "email", "push", "email", "sms", "email", "push", "email"}[index%10]
	if s == "campaign" {
		channel = "push"
		if index%20 == 19 {
			channel = "sms"
		}
	}
	recipient := fmt.Sprintf("customer-%d@example.com", account)
	if channel == "sms" {
		recipient = fmt.Sprintf("+1202555%04d", 100+index%100)
	} else if channel == "push" {
		recipient = fmt.Sprintf("demo-device-%d", account)
	}
	// A broad set of routine messages keeps a short baseline varied.
	templates := []struct{ subject, body string }{
		{"Order confirmation", "Your order %d is confirmed and being prepared."},
		{"Delivery update", "Package %d is on its way to your chosen pickup point."},
		{"Sign-in code", "Your sign-in code is %d. It expires in five minutes."},
		{"Weekly digest", "Here are this week's highlights for account %d."},
		{"Appointment booked", "Your appointment %d is booked for tomorrow afternoon."},
		{"Receipt available", "Receipt %d is now available in your account."},
		{"Password changed", "The password for account %d was changed successfully."},
		{"Stock alert", "Item %d from your wish list is back in stock."},
		{"Support reply", "There is a new reply to support ticket %d."},
		{"Subscription renewed", "Subscription %d has renewed for another month."},
		{"Pickup ready", "Your collection %d is ready at the service desk."},
		{"Profile updated", "Your account %d contact preferences were saved."},
	}
	template := templates[index%len(templates)]
	return Notification{Recipient: recipient, Channel: channel, Subject: template.subject,
		Body: fmt.Sprintf(template.body, 10000+index)}
}
