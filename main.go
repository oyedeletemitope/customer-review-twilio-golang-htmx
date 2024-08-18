package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/google/generative-ai-go/genai"
	_ "github.com/mattn/go-sqlite3"
	"github.com/twilio/twilio-go"
	openapi "github.com/twilio/twilio-go/rest/api/v2010"
	"google.golang.org/api/option"
)

var geminiModel *genai.GenerativeModel

func initDB() *sql.DB {
	database, err := sql.Open("sqlite3", "./reviews.db")
	if err != nil {
		panic(err)
	}

	createTableSQL := `CREATE TABLE IF NOT EXISTS reviews (
        "id" INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
        "name" TEXT,
        "rating" INTEGER,
        "description" TEXT
    );`

	if _, err = database.Exec(createTableSQL); err != nil {
		panic(err)
	}

	return database
}

func initGemini() {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		log.Fatal("GEMINI_API_KEY environment variable is not set. Please set it to your Gemini API key.")
	}

	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		log.Fatalf("Failed to create Gemini client: %v", err)
	}
	geminiModel = client.GenerativeModel("gemini-1.5-flash")
}

func analyzeSentiment(description string) (string, error) {
	ctx := context.Background()
	prompt := fmt.Sprintf("Analyze the sentiment of the following review. Respond with only one word: 'Positive', 'Negative', or 'Neutral'.\n\nReview: %s", description)
	resp, err := geminiModel.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		return "", err
	}
	if len(resp.Candidates) == 0 {
		return "", fmt.Errorf("no candidates in response")
	}

	var sentiment string
	for _, part := range resp.Candidates[0].Content.Parts {
		if textPart, ok := part.(genai.Text); ok {
			sentiment += string(textPart)
		}
	}

	if sentiment == "" {
		return "", fmt.Errorf("no text content in response")
	}

	return strings.TrimSpace(sentiment), nil
}

func sendTwilioMessage(name string, rating int) error {
	accountSid := os.Getenv("TWILIO_ACCOUNT_SID")
	authToken := os.Getenv("TWILIO_AUTH_TOKEN")
	fromPhone := os.Getenv("TWILIO_PHONE_NUMBER")
	toPhone := os.Getenv("PRODUCT_OWNER_PHONE_NUMBER")

	client := twilio.NewRestClientWithParams(twilio.ClientParams{
		Username: accountSid,
		Password: authToken,
	})

	message := fmt.Sprintf("%s rated your product a %d star", name, rating)

	params := &openapi.CreateMessageParams{}
	params.SetTo(toPhone)
	params.SetFrom(fromPhone)
	params.SetBody(message)

	_, err := client.Api.CreateMessage(params)
	if err != nil {
		return err
	}

	return nil
}

func submitReview(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			fmt.Println("Form submission received")

			err := r.ParseForm()
			if err != nil {
				fmt.Printf("Error parsing form: %v\n", err)
				http.Error(w, "Error parsing form", http.StatusBadRequest)
				return
			}

			name := r.FormValue("review_name")
			rating, err := strconv.Atoi(r.FormValue("rating"))
			if err != nil {
				fmt.Printf("Invalid rating value: %v\n", err)
				http.Error(w, "Invalid rating value", http.StatusBadRequest)
				return
			}
			description := r.FormValue("review_description")

			fmt.Printf("Received name: %s, rating: %d, description: %s\n", name, rating, description)

			if name == "" || rating == 0 || description == "" {
				fmt.Println("Missing required form fields")
				http.Error(w, "All fields are required", http.StatusBadRequest)
				return
			}

			sentiment, err := analyzeSentiment(description)
			if err != nil {
				fmt.Printf("Error analyzing sentiment: %v\n", err)
				http.Error(w, "Error analyzing sentiment", http.StatusInternalServerError)
				return
			}

			stmt, err := db.Prepare("INSERT INTO reviews (name, rating, description) VALUES (?, ?, ?)")
			if err != nil {
				fmt.Printf("Database preparation error: %v\n", err)
				http.Error(w, "Database error", http.StatusInternalServerError)
				return
			}
			defer stmt.Close()

			_, err = stmt.Exec(name, rating, description)
			if err != nil {
				fmt.Printf("Database execution error: %v\n", err)
				http.Error(w, "Database error", http.StatusInternalServerError)
				return
			}

			fmt.Println("Review submitted successfully")

			// Send SMS to product owner
			err = sendTwilioMessage(name, rating)
			if err != nil {
				fmt.Printf("Error sending SMS: %v\n", err)
			} else {
				fmt.Println("SMS sent successfully")
			}

			var sentimentMessage string
			if sentiment == "Positive" {
				sentimentMessage = "Thank you for the positive feedback!"
			} else if sentiment == "Negative" {
				sentimentMessage = "We are sorry to hear about your experience. We will work on improving it!"
			} else {
				sentimentMessage = "Thank you for your feedback!"
			}

			// Send the response as HTML
			fmt.Fprintf(w, `
                <div class="sentiment-message">
                    <p>%s</p>
                    <a href="/">Submit another review</a>
                </div>
            `, sentimentMessage)
		} else {
			http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
		}
	}
}

func main() {
	db := initDB()
	defer db.Close()

	initGemini()

	// Serve the index.html page
	http.Handle("/", http.FileServer(http.Dir("./static")))

	// Handle form submission at the "/submit-review" endpoint
	http.HandleFunc("/submit-review", submitReview(db))

	fmt.Println("Server started at :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		panic(err)
	}
}
