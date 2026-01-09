package aws

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// ClientManager manages shared AWS clients with connection pooling.
// It should be created once at startup and reused across reconciles.
type ClientManager struct {
	httpClient  *http.Client
	baseConfig  aws.Config
	mu          sync.RWMutex
	initialized bool
}

// NewClientManager creates a new AWS client manager with connection pooling.
// The HTTP client is shared across all AWS API calls to reuse connections.
func NewClientManager() *ClientManager {
	return &ClientManager{
		httpClient: &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 20,
				MaxConnsPerHost:     50,
				IdleConnTimeout:     90 * time.Second,
			},
			Timeout: 30 * time.Second,
		},
	}
}

// Initialize loads the base AWS config. Call this once at startup.
// Safe to call multiple times - only initializes once.
func (m *ClientManager) Initialize(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.initialized {
		return nil
	}

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithHTTPClient(m.httpClient),
	)
	if err != nil {
		return fmt.Errorf("failed to load AWS config: %w", err)
	}

	m.baseConfig = cfg
	m.initialized = true
	return nil
}

// RDSAuthConfig holds the configuration for RDS IAM authentication
type RDSAuthConfig struct {
	// Region is the AWS region (e.g., us-east-1)
	Region string
	// Host is the RDS endpoint (e.g., mydb.123456789012.us-east-1.rds.amazonaws.com)
	Host string
	// Port is the database port (e.g., 5432)
	Port int32
	// Username is the database username (must be IAM-enabled)
	Username string
	// DBName is the database name
	DBName string
	// RoleArn is the IAM role ARN to assume for generating the token
	RoleArn string
	// ExternalID is passed to STS AssumeRole for tenant isolation.
	// Should be set to "{namespace}/{name}" of the DBUpgrade resource.
	// The target role's trust policy must require this ExternalID.
	ExternalID string
}

// GenerateRDSAuthToken generates an IAM authentication token for RDS.
// The token is valid for 15 minutes and can be used as a password.
//
// If ExternalID is provided, it will be passed to STS AssumeRole.
// This provides tenant isolation - the target role's trust policy
// should require the specific ExternalID to prevent cross-tenant access.
func (m *ClientManager) GenerateRDSAuthToken(ctx context.Context, cfg RDSAuthConfig) (string, error) {
	m.mu.RLock()
	if !m.initialized {
		m.mu.RUnlock()
		return "", fmt.Errorf("AWS client manager not initialized - call Initialize() first")
	}
	// Copy base config to avoid mutation
	awsCfg := m.baseConfig.Copy()
	m.mu.RUnlock()

	// Override region if specified
	if cfg.Region != "" {
		awsCfg.Region = cfg.Region
	}

	// If RoleArn is specified, assume the role with ExternalID
	if cfg.RoleArn != "" {
		stsClient := sts.NewFromConfig(awsCfg)
		creds := stscreds.NewAssumeRoleProvider(stsClient, cfg.RoleArn,
			func(o *stscreds.AssumeRoleOptions) {
				// ExternalID provides tenant isolation.
				// The target role's trust policy must require this exact ExternalID.
				if cfg.ExternalID != "" {
					o.ExternalID = aws.String(cfg.ExternalID)
				}
				// Session name for CloudTrail auditing
				o.RoleSessionName = "dbupgrade-operator"
			},
		)
		awsCfg.Credentials = aws.NewCredentialsCache(creds)
	}

	// Generate the authentication token
	endpoint := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	token, err := auth.BuildAuthToken(ctx, endpoint, cfg.Region, cfg.Username, awsCfg.Credentials)
	if err != nil {
		return "", fmt.Errorf("failed to generate RDS auth token: %w", err)
	}

	return token, nil
}

// BuildPostgresConnectionURL builds a PostgreSQL connection URL using IAM auth
func BuildPostgresConnectionURL(cfg RDSAuthConfig, token string) string {
	// URL-encode the token since it contains special characters
	encodedToken := url.QueryEscape(token)
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=require",
		cfg.Username, encodedToken, cfg.Host, cfg.Port, cfg.DBName)
}

// BuildMySQLConnectionURL builds a MySQL connection URL using IAM auth
func BuildMySQLConnectionURL(cfg RDSAuthConfig, token string) string {
	encodedToken := url.QueryEscape(token)
	return fmt.Sprintf("mysql://%s:%s@%s:%d/%s?tls=true",
		cfg.Username, encodedToken, cfg.Host, cfg.Port, cfg.DBName)
}
