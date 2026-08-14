// checkcreds validates the Enable Banking credentials in .env with one call:
// it signs a JWT with your app key and asks the API which application it is.
//
//	cp .env.example .env  # fill APP_ID + PRIVATE_KEY
//	go run ./cmd/checkcreds
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/joho/godotenv"

	"fynanz/internal/sync"
)

func main() {
	_ = godotenv.Load()
	baseURL := os.Getenv("ENABLE_BANKING_URL")
	appID := os.Getenv("ENABLE_BANKING_APP_ID")
	keyPath := os.Getenv("ENABLE_BANKING_PRIVATE_KEY")
	switch {
	case baseURL == "":
		log.Fatal("ENABLE_BANKING_URL not set (default in .env.example)")
	case appID == "":
		log.Fatal("ENABLE_BANKING_APP_ID not set")
	case keyPath == "":
		log.Fatal("ENABLE_BANKING_PRIVATE_KEY not set")
	}

	key, err := loadKey(keyPath)
	if err != nil {
		log.Fatalf("private key: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	app, err := sync.NewClient(baseURL, appID, key).GetApplication(ctx)
	if err != nil {
		log.Fatalf("API rejected the JWT: %v", err)
	}
	fmt.Printf("OK — authenticated as %q\n", app.Name)
	fmt.Printf("  kid (app id) : %s\n", app.Kid)
	fmt.Printf("  environment  : %s\n", app.Environment)
	fmt.Printf("  redirect urls: %v\n", app.RedirectURLs)
	if len(app.RedirectURLs) > 0 {
		fmt.Printf("  (your ENABLE_BANKING_REDIRECT_URL must be one of these)\n")
	}
	if !app.Active {
		fmt.Println("  ⚠ app is not active — check the control panel")
	}
}
