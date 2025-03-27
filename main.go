package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/jackc/pgx/stdlib" // postgres driver
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/nats-io/nats.go"
)

// --- MCP Tools (Existing & New) ---

type ToolName string

const (
	ADD                    ToolName = "add"
	LONG_RUNNING_OPERATION ToolName = "longRunningOperation"
)

type PromptName string

const (
	SIMPLE  PromptName = "simple_prompt"
	COMPLEX PromptName = "complex_prompt"
)

// PostgreSQL query type constants
const (
	StatementTypeNoExplainCheck = ""
	StatementTypeSelect         = "SELECT"
	StatementTypeInsert         = "INSERT"
	StatementTypeUpdate         = "UPDATE"
	StatementTypeDelete         = "DELETE"
)

// --- Global variables for PostgreSQL connection and NATS URL ---
// Now we only use pgdsn for connection.
var (
	pgDSN            string
	readOnly         bool
	withExplainCheck bool
	DB               *sqlx.DB

	// natsURL is provided via a flag and used as default for natsPublish tool.
	natsURL string
)

// --- MCP Server Creation ---

func NewMCPServer() *server.MCPServer {

	hooks := &server.Hooks{}

	hooks.AddBeforeAny(func(id any, method mcp.MCPMethod, message any) {
		fmt.Printf("beforeAny: %s, %v, %v\n", method, id, message)
	})
	hooks.AddOnSuccess(func(id any, method mcp.MCPMethod, message any, result any) {
		fmt.Printf("onSuccess: %s, %v, %v, %v\n", method, id, message, result)
	})
	hooks.AddOnError(func(id any, method mcp.MCPMethod, message any, err error) {
		fmt.Printf("onError: %s, %v, %v, %v\n", method, id, message, err)
	})
	hooks.AddBeforeInitialize(func(id any, message *mcp.InitializeRequest) {
		fmt.Printf("beforeInitialize: %v, %v\n", id, message)
	})
	hooks.AddAfterInitialize(func(id any, message *mcp.InitializeRequest, result *mcp.InitializeResult) {
		fmt.Printf("afterInitialize: %v, %v, %v\n", id, message, result)
	})
	hooks.AddAfterCallTool(func(id any, message *mcp.CallToolRequest, result *mcp.CallToolResult) {
		fmt.Printf("afterCallTool: %v, %v, %v\n", id, message, result)
	})
	hooks.AddBeforeCallTool(func(id any, message *mcp.CallToolRequest) {
		fmt.Printf("beforeCallTool: %v, %v\n", id, message)
	})

	mcpServer := server.NewMCPServer(
		"example-servers/everything",
		"1.0.0",
		server.WithResourceCapabilities(true, true),
		server.WithPromptCapabilities(true),
		server.WithLogging(),
		server.WithHooks(hooks),
	)

	// Existing resource & prompt registrations
	mcpServer.AddResource(mcp.NewResource("test://static/resource",
		"Static Resource",
		mcp.WithMIMEType("text/plain"),
	), handleReadResource)
	mcpServer.AddResourceTemplate(
		mcp.NewResourceTemplate(
			"test://dynamic/resource/{id}",
			"Dynamic Resource",
		),
		handleResourceTemplate,
	)
	mcpServer.AddPrompt(mcp.NewPrompt(string(SIMPLE),
		mcp.WithPromptDescription("A simple prompt"),
	), handleSimplePrompt)
	mcpServer.AddPrompt(mcp.NewPrompt(string(COMPLEX),
		mcp.WithPromptDescription("A complex prompt"),
		mcp.WithArgument("temperature",
			mcp.ArgumentDescription("The temperature parameter for generation"),
			mcp.RequiredArgument(),
		),
		mcp.WithArgument("style",
			mcp.ArgumentDescription("The style to use for the response"),
			mcp.RequiredArgument(),
		),
	), handleComplexPrompt)

	// Register only the needed tools
	mcpServer.AddTool(mcp.NewTool(string(ADD),
		mcp.WithDescription("Adds two numbers"),
		mcp.WithNumber("a",
			mcp.Description("First number"),
			mcp.Required(),
		),
		mcp.WithNumber("b",
			mcp.Description("Second number"),
			mcp.Required(),
		),
	), handleAddTool)
	mcpServer.AddTool(mcp.NewTool(
		string(LONG_RUNNING_OPERATION),
		mcp.WithDescription("Demonstrates a long running operation with progress updates"),
		mcp.WithNumber("duration",
			mcp.Description("Duration of the operation in seconds"),
			mcp.DefaultNumber(10),
		),
		mcp.WithNumber("steps",
			mcp.Description("Number of steps in the operation"),
			mcp.DefaultNumber(5),
		),
	), handleLongRunningOperationTool)
	mcpServer.AddTool(mcp.NewTool("natsPublish",
		mcp.WithDescription("Publishes a message to a NATS channel"),
		// Set the default value for "url" from the natsURL flag.
		mcp.WithString("url",
			mcp.Description("The NATS server URL"),
			mcp.DefaultString(natsURL),
			mcp.Required(),
		),
		mcp.WithString("channel",
			mcp.Description("The channel to publish to"),
			mcp.Required(),
		),
		mcp.WithString("message",
			mcp.Description("The message to publish"),
			mcp.Required(),
		),
	), handleNATSPublishTool)

	// --- PostgreSQL Tools ---
	mcpServer.AddTool(mcp.NewTool("list_database",
		mcp.WithDescription("List all databases in the POSTGRES server"),
	), handleListDatabaseTool)
	mcpServer.AddTool(mcp.NewTool("list_table",
		mcp.WithDescription("List all tables in the POSTGRES server"),
	), handleListTableTool)
	mcpServer.AddTool(mcp.NewTool("desc_table",
		mcp.WithDescription("Describe the structure of a table"),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The name of the table to describe"),
		),
	), handleDescTableTool)
	mcpServer.AddTool(mcp.NewTool("read_query",
		mcp.WithDescription("Execute a read-only SQL query. Call `desc_table` first if necessary"),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("The SQL query to execute"),
		),
	), handleReadQueryTool)

	mcpServer.AddNotificationHandler("notification", handleNotification)

	return mcpServer
}

// --- Resource & Prompt Handlers ---

func handleReadResource(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	return []mcp.ResourceContents{
		mcp.TextResourceContents{
			URI:      "test://static/resource",
			MIMEType: "text/plain",
			Text:     "This is a sample resource",
		},
	}, nil
}

func handleResourceTemplate(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	return []mcp.ResourceContents{
		mcp.TextResourceContents{
			URI:      request.Params.URI,
			MIMEType: "text/plain",
			Text:     "This is a sample resource",
		},
	}, nil
}

func handleSimplePrompt(ctx context.Context, request mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	return &mcp.GetPromptResult{
		Description: "A simple prompt without arguments",
		Messages: []mcp.PromptMessage{
			{
				Role: mcp.RoleUser,
				Content: mcp.TextContent{
					Type: "text",
					Text: "This is a simple prompt without arguments.",
				},
			},
		},
	}, nil
}

func handleComplexPrompt(ctx context.Context, request mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	arguments := request.Params.Arguments
	return &mcp.GetPromptResult{
		Description: "A complex prompt with arguments",
		Messages: []mcp.PromptMessage{
			{
				Role: mcp.RoleUser,
				Content: mcp.TextContent{
					Type: "text",
					Text: fmt.Sprintf("This is a complex prompt with arguments: temperature=%s, style=%s",
						arguments["temperature"], arguments["style"]),
				},
			},
			{
				Role: mcp.RoleAssistant,
				Content: mcp.TextContent{
					Type: "text",
					Text: "I understand. You've provided a complex prompt with temperature and style arguments. How would you like me to proceed?",
				},
			},
		},
	}, nil
}

// --- Tool Handlers ---

func handleAddTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	arguments := request.Params.Arguments
	a, ok1 := arguments["a"].(float64)
	b, ok2 := arguments["b"].(float64)
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("invalid number arguments")
	}
	sum := a + b
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{
				Type: "text",
				Text: fmt.Sprintf("The sum of %f and %f is %f.", a, b, sum),
			},
		},
	}, nil
}

func handleLongRunningOperationTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	arguments := request.Params.Arguments
	progressToken := request.Params.Meta.ProgressToken
	duration, _ := arguments["duration"].(float64)
	steps, _ := arguments["steps"].(float64)
	stepDuration := duration / steps
	srv := server.ServerFromContext(ctx)

	for i := 1; i <= int(steps); i++ {
		time.Sleep(time.Duration(stepDuration * float64(time.Second)))
		if progressToken != nil {
			srv.SendNotificationToClient(
				ctx,
				"notifications/progress",
				map[string]interface{}{
					"progress":      i,
					"total":         int(steps),
					"progressToken": progressToken,
				},
			)
		}
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{
				Type: "text",
				Text: fmt.Sprintf("Long running operation completed. Duration: %f seconds, Steps: %d.", duration, int(steps)),
			},
		},
	}, nil
}

func handleNATSPublishTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	arguments := request.Params.Arguments
	url, ok := arguments["url"].(string)
	if !ok || url == "" {
		// If no URL is provided in the tool arguments, fall back to the global flag value.
		url = natsURL
		if url == "" {
			return nil, fmt.Errorf("NATS URL must be provided")
		}
	}
	channel, ok := arguments["channel"].(string)
	if !ok {
		return nil, fmt.Errorf("invalid channel argument")
	}
	message, ok := arguments["message"].(string)
	if !ok {
		return nil, fmt.Errorf("invalid message argument")
	}

	err := natsPublish(url, channel, message)
	if err != nil {
		return nil, fmt.Errorf("failed to publish message: %w", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{
				Type: "text",
				Text: fmt.Sprintf("Message published to channel %s", channel),
			},
		},
	}, nil
}

func natsPublish(url, channel, msg string) error {
	opts := []nats.Option{nats.Name("NATS Publisher")}
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return err
	}
	defer nc.Close()

	err = nc.Publish(channel, []byte(msg))
	if err != nil {
		return err
	}

	err = nc.Flush()
	if err != nil {
		return err
	}

	if err = nc.LastError(); err != nil {
		return err
	}
	log.Printf("Published [%s] : '%s'\n", channel, msg)
	return nil
}

// --- PostgreSQL Tool Handlers ---

func handleListDatabaseTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	result, err := HandleQuery("SELECT datname FROM pg_database WHERE datistemplate = false;", StatementTypeNoExplainCheck)
	if err != nil {
		return newToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(result), nil
}

func handleListTableTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	result, err := HandleQuery("SELECT table_schema, table_name FROM information_schema.tables ORDER BY table_schema, table_name;", StatementTypeNoExplainCheck)
	if err != nil {
		return newToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(result), nil
}

func handleDescTableTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name, ok := request.Params.Arguments["name"].(string)
	if !ok {
		return newToolResultError("invalid table name"), nil
	}
	result, err := HandleDescTable(name)
	if err != nil {
		return newToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(result), nil
}

func handleReadQueryTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	query, ok := request.Params.Arguments["query"].(string)
	if !ok {
		return newToolResultError("invalid query"), nil
	}
	result, err := HandleQuery(query, StatementTypeSelect)
	if err != nil {
		return newToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(result), nil
}

// --- PostgreSQL Helper Functions ---

func GetDB() (*sqlx.DB, error) {
	if DB != nil {
		return DB, nil
	}
	if len(pgDSN) == 0 {
		return nil, fmt.Errorf("pgdsn option must be provided")
	}
	db, err := sqlx.Connect("pgx", pgDSN)
	if err != nil {
		return nil, fmt.Errorf("failed to establish database connection: %v", err)
	}
	DB = db
	return DB, nil
}

func HandleQuery(query, expect string) (string, error) {
	result, headers, err := DoQuery(query, expect)
	if err != nil {
		return "", err
	}
	s, err := MapToCSV(result, headers)
	if err != nil {
		return "", err
	}
	return s, nil
}

func DoQuery(query, expect string) ([]map[string]interface{}, []string, error) {
	db, err := GetDB()
	if err != nil {
		return nil, nil, err
	}

	if len(expect) > 0 {
		if err := HandleExplain(query, expect); err != nil {
			return nil, nil, err
		}
	}

	rows, err := db.Queryx(query)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}

	result := []map[string]interface{}{}
	for rows.Next() {
		row, err := rows.SliceScan()
		if err != nil {
			return nil, nil, err
		}
		resultRow := map[string]interface{}{}
		for i, col := range cols {
			switch v := row[i].(type) {
			case []byte:
				resultRow[col] = string(v)
			default:
				resultRow[col] = v
			}
		}
		result = append(result, resultRow)
	}

	return result, cols, nil
}

func HandleExec(query, expect string) (string, error) {
	db, err := GetDB()
	if err != nil {
		return "", err
	}

	if len(expect) > 0 {
		if err := HandleExplain(query, expect); err != nil {
			return "", err
		}
	}

	result, err := db.Exec(query)
	if err != nil {
		return "", err
	}

	ra, err := result.RowsAffected()
	if err != nil {
		return "", err
	}

	switch expect {
	case StatementTypeInsert:
		li, err := result.LastInsertId()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d rows affected, last insert id: %d", ra, li), nil
	default:
		return fmt.Sprintf("%d rows affected", ra), nil
	}
}

func HandleExplain(query, expect string) error {
	if !withExplainCheck {
		return nil
	}

	db, err := GetDB()
	if err != nil {
		return err
	}

	rows, err := db.Queryx(fmt.Sprintf("EXPLAIN %s", query))
	if err != nil {
		return err
	}
	defer rows.Close()

	type ExplainResult struct {
		Id           *string `db:"id"`
		SelectType   *string `db:"select_type"`
		Table        *string `db:"table"`
		Partitions   *string `db:"partitions"`
		Type         *string `db:"type"`
		PossibleKeys *string `db:"possible_keys"`
		Key          *string `db:"key"`
		KeyLen       *string `db:"key_len"`
		Ref          *string `db:"ref"`
		Rows         *string `db:"rows"`
		Filtered     *string `db:"filtered"`
		Extra        *string `db:"Extra"`
	}

	result := []ExplainResult{}
	for rows.Next() {
		var row ExplainResult
		if err := rows.StructScan(&row); err != nil {
			return err
		}
		result = append(result, row)
	}

	if len(result) != 1 {
		return fmt.Errorf("unable to check query plan, denied")
	}

	match := false
	switch expect {
	case StatementTypeInsert, StatementTypeUpdate, StatementTypeDelete:
		if result[0].SelectType != nil && *result[0].SelectType == expect {
			match = true
		}
	default:
		// For SELECT queries, ensure the select_type is not one of the write types.
		match = true
		for _, typ := range []string{StatementTypeInsert, StatementTypeUpdate, StatementTypeDelete} {
			if result[0].SelectType != nil && *result[0].SelectType == typ {
				match = false
				break
			}
		}
	}

	if !match {
		return fmt.Errorf("query plan does not match expected pattern, denied")
	}

	return nil
}

func HandleDescTable(name string) (string, error) {
	db, err := GetDB()
	if err != nil {
		return "", err
	}

	query := fmt.Sprintf("SHOW CREATE TABLE %s", name)
	rows, err := db.Queryx(query)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	type ShowCreateTableResult struct {
		Table       string `db:"Table"`
		CreateTable string `db:"Create Table"`
	}

	results := []ShowCreateTableResult{}
	for rows.Next() {
		var row ShowCreateTableResult
		if err := rows.StructScan(&row); err != nil {
			return "", err
		}
		results = append(results, row)
	}

	if len(results) == 0 {
		return "", fmt.Errorf("table %s does not exist", name)
	}

	return results[0].CreateTable, nil
}

func MapToCSV(m []map[string]interface{}, headers []string) (string, error) {
	var csvBuf strings.Builder
	writer := csv.NewWriter(&csvBuf)

	if err := writer.Write(headers); err != nil {
		return "", fmt.Errorf("failed to write headers: %v", err)
	}

	for _, item := range m {
		row := make([]string, len(headers))
		for i, header := range headers {
			value, exists := item[header]
			if !exists {
				return "", fmt.Errorf("key '%s' not found in map", header)
			}
			row[i] = fmt.Sprintf("%v", value)
		}
		if err := writer.Write(row); err != nil {
			return "", fmt.Errorf("failed to write row: %v", err)
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", fmt.Errorf("error flushing CSV writer: %v", err)
	}

	return csvBuf.String(), nil
}

// --- Notification Handler ---

func handleNotification(ctx context.Context, notification mcp.JSONRPCNotification) {
	log.Printf("Received notification: %s", notification.Method)
}

// --- Main Function ---

func main() {
	var transport, port, baseURL string
	var omitPort bool

	// MCP server flags
	flag.StringVar(&transport, "transport", "stdio", "Transport type (stdio or sse)")
	flag.StringVar(&port, "port", "3001", "Port to run the MCP server on.")
	flag.StringVar(&baseURL, "baseurl", "http://localhost", "Base URL for the server.")
	flag.BoolVar(&omitPort, "omitPort", false, "Do not append port to base URL (useful when served via a domain).")

	// PostgreSQL flag (only pgdsn is used)
	flag.StringVar(&pgDSN, "pgdsn", "", "POSTGRES DSN")
	flag.BoolVar(&readOnly, "read-only", false, "Enable read-only mode for Postgres queries")
	flag.BoolVar(&withExplainCheck, "with-explain-check", false, "Check query plan with `EXPLAIN` before executing for Postgres queries")

	// NATS flag for default URL for natsPublish tool.
	flag.StringVar(&natsURL, "natsurl", "", "NATS server URL")
	
	flag.Parse()

	mcpServer := NewMCPServer()

	// Choose transport
	if transport == "sse" {
		var fullBaseURL string
		if omitPort {
			fullBaseURL = baseURL
		} else {
			fullBaseURL = baseURL + ":" + port
		}
		sseServer := server.NewSSEServer(mcpServer, server.WithBaseURL(fullBaseURL))
		log.Printf("SSE server listening on %s", fullBaseURL)
		if err := sseServer.Start(":" + port); err != nil {
			log.Fatalf("Server error: %v", err)
		}
	} else {
		if err := server.ServeStdio(mcpServer); err != nil {
			log.Fatalf("Server error: %v", err)
		}
	}
}

// --- Helper: Wrap error into a tool result ---

func newToolResultError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{
				Type: "text",
				Text: message,
			},
		},
	}
}

