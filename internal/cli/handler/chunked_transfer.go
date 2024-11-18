// internal/cli/handler/progress_display.go

package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bnema/gordon/internal/cli"
	"github.com/bnema/gordon/internal/common"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/log"
	"github.com/google/uuid"
	"golang.org/x/exp/rand"
)

// UploadProgress represents the current state of the upload
type UploadProgress struct {
	Completed  int
	Total      int
	Percentage float64
}

// ProgressDisplay manages the upload progress display
type ProgressDisplay struct {
	mu         sync.Mutex
	model      *uploadModel
	program    *tea.Program
	updateChan chan UploadProgress
	done       chan struct{}
	paused     bool
	pauseChan  chan bool
}

type uploadModel struct {
	spinner    spinner.Model
	completed  int
	total      int
	percentage float64
	done       bool
}

// NewProgressDisplay creates a new progress display
func NewProgressDisplay() *ProgressDisplay {
	return &ProgressDisplay{
		updateChan: make(chan UploadProgress),
		done:       make(chan struct{}),
		pauseChan:  make(chan bool),
	}
}

func (pd *ProgressDisplay) Start() {
	pd.mu.Lock()
	defer pd.mu.Unlock()

	pd.model = &uploadModel{
		spinner: spinner.New(
			spinner.WithSpinner(spinner.MiniDot),
			spinner.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("69"))),
		),
	}

	pd.program = tea.NewProgram(pd.model)

	// Run the UI in a goroutine
	go func() {
		if _, err := pd.program.Run(); err != nil {
			fmt.Printf("Error running progress display: %v\n", err)
		}
	}()

	// Handle updates in a separate goroutine
	go pd.handleUpdates()
}

func (pd *ProgressDisplay) Pause() {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	pd.paused = true
	pd.Clear() // Clear the current display
}

func (pd *ProgressDisplay) Resume() {
	pd.mu.Lock()
	defer pd.mu.Unlock()
	pd.paused = false
}

func (pd *ProgressDisplay) handleUpdates() {
	for {
		select {
		case progress := <-pd.updateChan:
			pd.mu.Lock()
			if !pd.paused {
				pd.program.Send(progressMsg{
					completed:  progress.Completed,
					total:      progress.Total,
					percentage: progress.Percentage,
				})
			}
			pd.mu.Unlock()
		case <-pd.done:
			pd.program.Send(doneMsg{})
			return
		}
	}
}

func (pd *ProgressDisplay) Update(completed, total int, percentage float64) {
	pd.updateChan <- UploadProgress{
		Completed:  completed,
		Total:      total,
		Percentage: percentage,
	}
}

func (pd *ProgressDisplay) Stop() {
	close(pd.done)
}

func (pd *ProgressDisplay) Clear() {
	pd.mu.Lock()
	defer pd.mu.Unlock()

	// Send a special clear message
	pd.program.Send(clearMsg{})
	// Wait a brief moment for the clear to take effect
	time.Sleep(50 * time.Millisecond)
}

type progressMsg struct {
	completed  int
	total      int
	percentage float64
}

type doneMsg struct{}

type clearMsg struct{}

func (m *uploadModel) Init() tea.Cmd {
	return m.spinner.Tick
}

func (m *uploadModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case progressMsg:
		m.completed = msg.completed
		m.total = msg.total
		m.percentage = msg.percentage
		return m, m.spinner.Tick
	case doneMsg:
		m.done = true
		return m, tea.Quit
	case clearMsg:
		m.done = true
		return m, tea.Quit
	}
	return m, nil
}

func (m *uploadModel) View() string {
	if m.done {
		return ""
	}

	return fmt.Sprintf("\r%s Uploading image... %d/%d chunks (%.2f%%)",
		m.spinner.View(),
		m.completed,
		m.total,
		m.percentage,
	)
}

type ChunkMetadata = common.ChunkMetadata

type ChunkedClient struct {
	client     *http.Client
	baseURL    string
	token      string
	chunkSize  int64
	app        *cli.App
	progress   *ProgressDisplay
	cancelFunc context.CancelFunc
}

type transferStore struct {
	mu       sync.Mutex
	chunks   map[string]map[int][]byte
	metadata map[string]*ChunkMetadata
	started  map[string]time.Time
}

var clientChunkStore = &transferStore{
	chunks:   make(map[string]map[int][]byte),
	metadata: make(map[string]*ChunkMetadata),
	started:  make(map[string]time.Time),
}

// NewChunkedClient creates a new chunked client
func NewChunkedClient(app *cli.App) *ChunkedClient {
	baseURL := app.Config.Http.BackendURL + "/api"
	token := app.Config.General.Token

	return &ChunkedClient{
		client: &http.Client{
			Timeout: 30 * time.Minute,
		},
		baseURL:   baseURL,
		token:     token,
		chunkSize: 5 * 1024 * 1024,
		app:       app,
		progress:  NewProgressDisplay(),
	}
}

// SendFileAsChunks sends a file as chunks to the specified endpoint
func (c *ChunkedClient) SendFileAsChunks(ctx context.Context, endpoint string, headers http.Header, reader io.Reader, totalSize int64, imageName string) (*Response, error) {
	ctx, cancel := context.WithCancel(ctx)
	c.cancelFunc = cancel
	defer cancel()

	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	// Start progress display
	c.progress.Start()
	defer c.progress.Stop()

	// Handle signals in a goroutine
	go func() {
		select {
		case <-sigChan:
			log.Info("Received interrupt signal, cancelling upload...")
			cancel()
		case <-ctx.Done():
			return
		}
	}()

	transferID := uuid.New().String()
	totalChunks := (int(totalSize) + int(c.chunkSize) - 1) / int(c.chunkSize)
	buf := make([]byte, c.chunkSize)

	// Initialize transfer tracking
	clientChunkStore.mu.Lock()
	clientChunkStore.started[transferID] = time.Now()
	clientChunkStore.mu.Unlock()

	defer cleanupClientTransfer(transferID)

	var lastResponse *Response
	for chunkNum := 0; chunkNum < totalChunks; chunkNum++ {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("upload cancelled")
		default:
			n, err := io.ReadFull(reader, buf)
			if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
				return nil, fmt.Errorf("failed to read chunk %d: %w", chunkNum, err)
			}

			if n == 0 {
				continue
			}

			chunk := buf[:n]
			metadata := common.ChunkMetadata{
				ChunkNumber: chunkNum,
				TotalChunks: totalChunks,
				ChunkSize:   int64(n),
				TotalSize:   totalSize,
				ImageName:   imageName,
				TransferID:  transferID,
			}

			resp, err := c.sendChunkWithRetry(ctx, endpoint, headers, chunk, metadata)
			if err != nil {
				return nil, fmt.Errorf("failed to send chunk %d: %w", chunkNum, err)
			}
			lastResponse = resp

			// Update progress
			c.progress.Update(chunkNum+1, totalChunks, float64(chunkNum+1)/float64(totalChunks)*100)
		}
	}

	if lastResponse != nil {
		c.progress.Clear()
		responseType := determineResponseType(endpoint)
		if err := handleFinalResponse(lastResponse, responseType); err != nil {
			log.Error("Failed to handle final response", "error", err)
		}
	}

	return lastResponse, nil
}

// sendChunkWithRetry sends a single chunk to the server and retries on failure
func (c *ChunkedClient) sendChunkWithRetry(ctx context.Context, endpoint string, headers http.Header, chunk []byte, metadata common.ChunkMetadata) (*Response, error) {
	maxRetries := 3
	backoff := time.Second

	isRetryableError := func(err error) bool {
		if err == nil {
			return false
		}

		if netErr, ok := err.(net.Error); ok {
			return netErr.Timeout()
		}

		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false
		}

		errorStr := err.Error()
		retryableErrors := []string{
			"connection reset by peer",
			"broken pipe",
			"connection refused",
			"no such host",
			"i/o timeout",
			"unexpected EOF",
			"connection timed out",
			"network is unreachable",
		}

		for _, retryableErr := range retryableErrors {
			if strings.Contains(strings.ToLower(errorStr), retryableErr) {
				return true
			}
		}

		return false
	}

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		resp, err := c.sendChunk(ctx, endpoint, headers, chunk, metadata)
		if err == nil {
			return resp, nil
		}

		lastErr = err

		if !isRetryableError(err) {
			return nil, err
		}

		if attempt < maxRetries-1 {
			retryDelay := backoff * time.Duration(attempt+1)
			jitter := time.Duration(rand.Int63n(int64(retryDelay) / 2))

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryDelay + jitter):
				continue
			}
		}
	}

	return nil, fmt.Errorf("max retries exceeded after %d attempts: %w", maxRetries, lastErr)
}

func (c *ChunkedClient) Cancel() {
	if c.cancelFunc != nil {
		c.cancelFunc()
	}
}

// Response handling functions
func determineResponseType(endpoint string) string {
	switch {
	case strings.Contains(endpoint, "/deploy"):
		return "deploy"
	case strings.Contains(endpoint, "/push"):
		return "push"
	default:
		return "unknown"
	}
}

// handleFinalResponse determines the response type and calls the appropriate handler
func handleFinalResponse(resp *Response, responseType string) error {
	if resp == nil || len(resp.Body) == 0 {
		return fmt.Errorf("empty response received")
	}

	switch responseType {
	case "deploy":
		return handleDeployResponse(resp)
	case "push":
		return handlePushResponse(resp)
	default:
		return fmt.Errorf("unknown response type: %s", responseType)
	}
}

// handleDeployResponse handles the response from a deploy operation
func handleDeployResponse(resp *Response) error {
	// If not a conflict, process as normal deploy response
	var finalResp common.DeployResponse
	if err := json.Unmarshal(resp.Body, &finalResp); err != nil {
		return fmt.Errorf("failed to unmarshal deploy response: %w", err)
	}

	// Always log the success message with available information
	logFields := []interface{}{}

	if finalResp.Domain != "" {
		logFields = append(logFields, "domain", finalResp.Domain)
	}
	if finalResp.ContainerID != "" {
		shortContainerID := finalResp.ContainerID
		if len(shortContainerID) > 8 {
			shortContainerID = shortContainerID[:8]
		}
		logFields = append(logFields, "containerID", shortContainerID)
	}
	if finalResp.ContainerName != "" {
		logFields = append(logFields, "containerName", finalResp.ContainerName)
	}

	// If there's a message, use it; otherwise, use a default success message
	message := finalResp.Message
	if message == "" {
		message = "Deployment completed successfully"
	}

	// Ensure we log the result even if there are no additional fields
	if len(logFields) > 0 {
		log.Info(message, logFields...)
	} else {
		log.Info(message)
	}

	return nil
}

// handlePushResponse handles the response from a push operation
func handlePushResponse(resp *Response) error {
	var finalResp common.PushResponse
	if err := json.Unmarshal(resp.Body, &finalResp); err != nil {
		return fmt.Errorf("failed to unmarshal push response: %w", err)
	}
	return nil
}

// sendChunk sends a single chunk to the server
func (c *ChunkedClient) sendChunk(ctx context.Context, endpoint string, headers http.Header, chunk []byte, metadata common.ChunkMetadata) (*Response, error) {
	if err := verifyChunkIntegrity(chunk, metadata); err != nil {
		return nil, fmt.Errorf("chunk integrity check failed: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			resp, err := c.attemptChunkSend(ctx, endpoint, headers, chunk, metadata)
			if err != nil {
				return nil, err
			}
			return resp, nil
		}
	}
}

func (c *ChunkedClient) attemptChunkSend(ctx context.Context, endpoint string, headers http.Header, chunk []byte, metadata common.ChunkMetadata) (*Response, error) {
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal metadata: %w", err)
	}

	url := c.baseURL + endpoint
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(chunk))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Chunk-Metadata", string(metadataJSON))
	req.Header.Set("X-Chunk-Hash", fmt.Sprintf("%x", sha256.Sum256(chunk)))

	for k, v := range headers {
		req.Header[k] = v
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return &Response{
				Http:       resp,
				Body:       body,
				StatusCode: resp.StatusCode,
				Header:     resp.Header,
			}, &common.DeploymentError{
				StatusCode:  resp.StatusCode,
				Message:     "Request failed",
				RawResponse: string(body),
			}
	}

	return &Response{
		Http:       resp,
		Body:       body,
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
	}, nil
}

// verifyChunkIntegrity verifies the integrity of a chunk
func verifyChunkIntegrity(chunk []byte, metadata common.ChunkMetadata) error {
	if len(chunk) == 0 {
		return fmt.Errorf("empty chunk received")
	}

	if int64(len(chunk)) != metadata.ChunkSize {
		return fmt.Errorf("chunk size mismatch: expected %d, got %d",
			metadata.ChunkSize, len(chunk))
	}

	return nil
}

// cleanupClientTransfer cleans up a client transfer
func cleanupClientTransfer(transferID string) {
	clientChunkStore.mu.Lock()
	defer clientChunkStore.mu.Unlock()

	delete(clientChunkStore.chunks, transferID)
	delete(clientChunkStore.metadata, transferID)
	delete(clientChunkStore.started, transferID)
}
