package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// RateLimitTestConfig holds configuration for the rate limit test
type RateLimitTestConfig struct {
	URL               string
	ConcurrentReqs    int
	MaxBatches        int
	BatchDelay        time.Duration
	WaitAfterJail     time.Duration
	TestIP            string
	PrintDetailedResp bool
}

// Result represents the result of a single HTTP request
type Result struct {
	ID           int
	StatusCode   int
	RateLimitIP  string
	ResponseTime time.Duration
}

// runConcurrentRateLimitTest executes a rate limiting test with the given configuration
func runConcurrentRateLimitTest(config RateLimitTestConfig) {
	fmt.Printf("Starting concurrent rate limit test at %v\n", time.Now().Format(time.RFC1123))
	fmt.Printf("URL: %s\n", config.URL)
	fmt.Printf("Concurrent requests per batch: %d\n", config.ConcurrentReqs)
	fmt.Printf("Maximum batches: %d\n", config.MaxBatches)
	fmt.Printf("Simulated client IP: %s\n", config.TestIP)
	fmt.Println("========================================================")

	hitLimit := false
	for batch := 1; batch <= config.MaxBatches && !hitLimit; batch++ {
		fmt.Printf("Sending batch %d with %d concurrent requests...\n", batch, config.ConcurrentReqs)

		results := sendBatch(config, batch)
		hitLimit = processBatchResults(results, batch, config.PrintDetailedResp)

		if hitLimit {
			fmt.Println("========================================================")
			fmt.Printf("Success! Rate limit triggered in batch %d\n", batch)
			break
		}

		if batch < config.MaxBatches {
			fmt.Printf("Waiting %v seconds before next batch...\n", config.BatchDelay.Seconds())
			time.Sleep(config.BatchDelay)
		}
	}

	if !hitLimit {
		fmt.Println("========================================================")
		fmt.Printf("Did not trigger rate limit after %d batches of %d requests\n", config.MaxBatches, config.ConcurrentReqs)
		fmt.Println("You may need to increase the number of concurrent requests or batches")
		return
	}

	// Test if we're still rate limited after a short pause
	fmt.Println("Waiting 5 seconds and trying again to confirm we're still rate limited...")
	time.Sleep(5 * time.Second)

	status, _ := sendSingleRequest(config.URL, config.TestIP)
	if status == 429 {
		fmt.Println("Still rate limited after 5 seconds as expected")
	} else {
		fmt.Printf("No longer rate limited after 5 seconds (HTTP %d)\n", status)
	}

	// Wait for jail time to expire
	fmt.Println("========================================================")
	fmt.Printf("Waiting for jail time to expire (%v seconds)...\n", config.WaitAfterJail.Seconds())
	time.Sleep(config.WaitAfterJail)

	// Try again after waiting
	status, _ = sendSingleRequest(config.URL, config.TestIP)
	if status != 429 {
		fmt.Printf("Successfully released from jail after waiting (HTTP %d)\n", status)
	} else {
		fmt.Printf("Still rate limited after waiting %v seconds\n", config.WaitAfterJail.Seconds())
		fmt.Println("You might need to wait longer or reset the rate limiter")
	}

	fmt.Println("========================================================")
	fmt.Printf("Test completed at %v\n", time.Now().Format(time.RFC1123))
}

// sendBatch sends a batch of concurrent requests
func sendBatch(config RateLimitTestConfig, batchNum int) []Result {
	var wg sync.WaitGroup
	results := make([]Result, config.ConcurrentReqs)

	for i := 0; i < config.ConcurrentReqs; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			start := time.Now()
			statusCode, rateLimitIP := sendSingleRequest(config.URL, config.TestIP)
			responseTime := time.Since(start)

			results[id-1] = Result{
				ID:           id,
				StatusCode:   statusCode,
				RateLimitIP:  rateLimitIP,
				ResponseTime: responseTime,
			}
		}(i + 1)
	}

	wg.Wait()
	return results
}

// sendSingleRequest sends a single HTTP request and returns the status code and rate limit header
func sendSingleRequest(url, testIP string) (int, string) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		console.Debug("Error creating request:", err)
		return 0, ""
	}

	// Add headers to simulate client IP
	if testIP != "" {
		req.Header.Set("X-Forwarded-For", testIP)
		req.Header.Set("X-Real-IP", testIP)
	}

	resp, err := client.Do(req)
	if err != nil {
		console.Debug("Error sending request:", err)
		return 0, ""
	}
	defer resp.Body.Close()

	// Get the rate limit header
	rateLimitIP := resp.Header.Get("X-Rate-Limit-IP")

	return resp.StatusCode, rateLimitIP
}

// processBatchResults processes and displays the results from a batch of requests
func processBatchResults(results []Result, batchNum int, printDetailedResp bool) bool {
	statusCounts := make(map[int]int)
	hitLimit := false

	fmt.Printf("Results for batch %d:\n", batchNum)

	// Display detailed results if requested
	if printDetailedResp {
		for _, result := range results {
			fmt.Printf("  Request %d: Status %d, Rate-Limited IP: %s (took %v)\n",
				result.ID, result.StatusCode, result.RateLimitIP, result.ResponseTime)
		}
	}

	// Count the different status codes
	for _, result := range results {
		statusCounts[result.StatusCode]++
		if result.StatusCode == 429 {
			hitLimit = true
		}
	}

	// Display summary
	fmt.Println("  Summary:")
	for status, count := range statusCounts {
		fmt.Printf("    Status %d: %d requests\n", status, count)
	}

	return hitLimit
}

// Simplified console.Debug for logging debug messages
var console = struct {
	Debug func(v ...interface{})
}{
	Debug: func(v ...interface{}) {
		fmt.Fprintln(os.Stderr, append([]interface{}{"DEBUG:"}, v...)...)
	},
}

// Main entry point for the script
func main() {
	// Define command-line flags
	urlFlag := flag.String("url", "", "URL to test")
	concurrentFlag := flag.Int("concurrent", 15, "Number of concurrent requests per batch")
	batchesFlag := flag.Int("batches", 3, "Maximum number of batches to send")
	batchDelayFlag := flag.Float64("batch-delay", 0.5, "Delay between batches in seconds")
	waitJailFlag := flag.Float64("wait-jail", 15, "Time to wait for jail expiry in seconds")
	ipFlag := flag.String("ip", "127.0.0.1", "Simulated client IP")
	verboseFlag := flag.Bool("verbose", false, "Print detailed response information")

	flag.Parse()

	// Check for required URL
	if *urlFlag == "" {
		// Try to get URL from environment variable
		*urlFlag = os.Getenv("TEST_BACKEND_URL")
		if *urlFlag == "" {
			fmt.Println("Error: URL is required. Provide with -url flag or TEST_BACKEND_URL environment variable")
			flag.Usage()
			os.Exit(1)
		}
	}

	config := RateLimitTestConfig{
		URL:               *urlFlag,
		ConcurrentReqs:    *concurrentFlag,
		MaxBatches:        *batchesFlag,
		BatchDelay:        time.Duration(*batchDelayFlag * float64(time.Second)),
		WaitAfterJail:     time.Duration(*waitJailFlag * float64(time.Second)),
		TestIP:            *ipFlag,
		PrintDetailedResp: *verboseFlag,
	}

	runConcurrentRateLimitTest(config)
}
