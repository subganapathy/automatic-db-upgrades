package aws

import (
	"context"
	"fmt"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

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
}

// GenerateRDSAuthToken generates an IAM authentication token for RDS
// The token is valid for 15 minutes and can be used as a password
func GenerateRDSAuthToken(ctx context.Context, cfg RDSAuthConfig) (string, error) {
	// Load the default AWS config (uses EKS Pod Identity or IRSA)
	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(cfg.Region))
	if err != nil {
		return "", fmt.Errorf("failed to load AWS config: %w", err)
	}

	// If RoleArn is specified, assume the role
	if cfg.RoleArn != "" {
		stsClient := sts.NewFromConfig(awsCfg)
		creds := stscreds.NewAssumeRoleProvider(stsClient, cfg.RoleArn)
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
