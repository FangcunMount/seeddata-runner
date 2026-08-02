package dailysim

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/FangcunMount/component-base/pkg/log"
	identityv2 "github.com/FangcunMount/iam/v2/api/grpc/iam/identity/v2"
	sdk "github.com/FangcunMount/iam/v2/pkg/sdk"
	sdkerrors "github.com/FangcunMount/iam/v2/pkg/sdk/errors"
	"github.com/FangcunMount/seeddata-runner/internal/scheduler"
)

func newDailySimulationIAMBundle(
	ctx context.Context,
	cfg IAMConfig,
	orgID int64,
) (*dailySimulationIAMBundle, error) {
	if strings.TrimSpace(cfg.GRPC.Address) == "" {
		return nil, fmt.Errorf("daily_simulation requires iam.grpc.address")
	}

	timeout := 15 * time.Second
	if strings.TrimSpace(cfg.GRPC.Timeout) != "" {
		parsed, err := time.ParseDuration(strings.TrimSpace(cfg.GRPC.Timeout))
		if err != nil {
			return nil, fmt.Errorf("invalid iam.grpc.timeout %q: %w", cfg.GRPC.Timeout, err)
		}
		timeout = parsed
	}

	clientCfg := &sdk.Config{
		Endpoint: cfg.GRPC.Address,
		Timeout:  timeout,
	}
	if cfg.GRPC.RetryMax > 0 {
		clientCfg.Retry = &sdk.RetryConfig{
			Enabled:     true,
			MaxAttempts: cfg.GRPC.RetryMax,
		}
	}
	if cfg.GRPC.TLS.Enabled {
		clientCfg.TLS = &sdk.TLSConfig{
			Enabled:            true,
			CACert:             strings.TrimSpace(cfg.GRPC.TLS.CAFile),
			ClientCert:         strings.TrimSpace(cfg.GRPC.TLS.CertFile),
			ClientKey:          strings.TrimSpace(cfg.GRPC.TLS.KeyFile),
			ServerName:         strings.TrimSpace(cfg.GRPC.TLS.ServerName),
			InsecureSkipVerify: cfg.GRPC.TLS.InsecureSkipVerify,
		}
	}

	client, err := sdk.NewClient(ctx, clientCfg)
	if err != nil {
		return nil, fmt.Errorf("create daily simulation iam grpc client: %w", err)
	}
	return &dailySimulationIAMBundle{
		client:   client,
		identity: client.Identity(),
	}, nil
}

func ensureDailySimulationGuardianAccount(
	ctx context.Context,
	deps *dependencies,
	iamBundle *dailySimulationIAMBundle,
	cfg DailySimulationConfig,
	profile dailySimulationProfile,
) (string, string, bool, error) {
	password := normalizeDailySimulationPassword(cfg.UserPassword)
	userID, err := findDailySimulationIAMUser(ctx, iamBundle, profile.GuardianPhone, profile.GuardianEmail)
	if err != nil {
		return "", "", false, err
	}
	if strings.TrimSpace(userID) == "" {
		return "", "", false, fmt.Errorf("daily_simulation guardian provisioning requires iam.mockConsumer.enabled=true with IAM v2; IAM AuthN gRPC no longer exposes password account onboarding")
	}

	loginURL, err := resolveDailySimulationIAMLoginURL(deps.Config.IAM)
	if err != nil {
		return "", "", false, err
	}
	tenantID := resolveDailySimulationTenantID(deps.Config.IAM, deps.Config.Global.OrgID)
	deviceID := fmt.Sprintf("%s-%s-%03d", dailySimulationDeviceIDPrefix, profile.RunDate.Format("20060102"), profile.Index+1)

	token, err := tryDailySimulationGuardianLogin(ctx, loginURL, tenantID, deviceID, profile.GuardianEmail, profile.GuardianPhone, password, deps.Logger)
	if err == nil {
		return userID, token, false, nil
	}

	return "", "", false, fmt.Errorf("login existing guardian %s: %w; IAM v2 password onboarding is only available through iam.mockConsumer REST ensure", profile.GuardianEmail, err)
}

func ensureDailySimulationGuardianMockConsumer(
	ctx context.Context,
	deps *dependencies,
	cfg DailySimulationConfig,
	profile dailySimulationProfile,
) (string, string, bool, error) {
	baseURL, err := resolveDailySimulationIAMBaseURL(deps.Config.IAM)
	if err != nil {
		return "", "", false, err
	}
	endpointPath := resolveDailySimulationIAMMockConsumerEndpointPath(deps.Config.IAM)
	sharedSecret := strings.TrimSpace(deps.Config.IAM.MockConsumer.SharedSecret)
	if sharedSecret == "" {
		return "", "", false, fmt.Errorf("iam.mockConsumer.sharedSecret is required for daily_simulation mock-consumer mode")
	}

	password := normalizeDailySimulationPassword(cfg.UserPassword)
	client := NewAPIClient(baseURL, "", deps.Logger)
	configureDailySimulationMockIAMClient(client)

	ensureResp, err := client.EnsureIAMMockConsumer(ctx, endpointPath, EnsureIAMMockConsumerRequest{
		Name:     profile.GuardianName,
		Phone:    profile.GuardianPhone,
		Email:    profile.GuardianEmail,
		Password: password,
	}, sharedSecret)
	if err != nil {
		return "", "", false, fmt.Errorf("ensure guardian mock-consumer %s: %w", profile.GuardianEmail, err)
	}
	if ensureResp == nil || strings.TrimSpace(ensureResp.UserID) == "" {
		return "", "", false, fmt.Errorf("ensure guardian mock-consumer returned empty user id")
	}

	loginURL, err := resolveDailySimulationIAMLoginURL(deps.Config.IAM)
	if err != nil {
		return "", "", false, err
	}
	// IAM mock-consumer onboarding creates a username identity in the default
	// realm. Password login must therefore omit tenant_id; IAM will default the
	// principal tenant before issuing the token.
	tenantID := ""
	deviceID := fmt.Sprintf("%s-%s-%03d", dailySimulationDeviceIDPrefix, profile.RunDate.Format("20060102"), profile.Index+1)

	token, err := tryDailySimulationGuardianLoginWithRetry(ctx, loginURL, tenantID, deviceID, profile.GuardianEmail, profile.GuardianPhone, password, deps.Logger)
	if err != nil {
		return "", "", false, fmt.Errorf("login guardian %s after ensuring mock-consumer: %w", profile.GuardianEmail, err)
	}
	return strings.TrimSpace(ensureResp.UserID), token, ensureResp.IsNewUser, nil
}

func findDailySimulationIAMUser(
	ctx context.Context,
	iamBundle *dailySimulationIAMBundle,
	phone, email string,
) (string, error) {
	resp, err := iamBundle.identity.SearchUsers(ctx, &identityv2.SearchUsersRequest{
		Phones: []string{normalizePhone(phone)},
		Emails: []string{normalizeEmail(email)},
	})
	if err != nil {
		return "", fmt.Errorf("search iam users by phone/email: %w", err)
	}
	for _, item := range resp.GetUsers() {
		if item == nil {
			continue
		}
		if strings.TrimSpace(item.GetId()) != "" {
			return strings.TrimSpace(item.GetId()), nil
		}
	}
	return "", nil
}

func tryDailySimulationGuardianLogin(
	ctx context.Context,
	loginURL, tenantID, deviceID, email, phone, password string,
	logger log.Logger,
) (string, error) {
	credentials := []string{normalizeEmail(email), normalizePhone(phone)}
	var lastErr error
	for _, username := range credentials {
		if strings.TrimSpace(username) == "" {
			continue
		}
		token, err := fetchTokenFromIAMWithPassword(ctx, loginURL, username, password, tenantID, deviceID, logger)
		if err == nil && strings.TrimSpace(token) != "" {
			return token, nil
		}
		if err == nil {
			err = fmt.Errorf("iam login returned empty token for username %s", username)
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no available guardian login username")
	}
	return "", lastErr
}

func tryDailySimulationGuardianLoginWithRetry(
	ctx context.Context,
	loginURL, tenantID, deviceID, email, phone, password string,
	logger log.Logger,
) (string, error) {
	const maxAttempts = 2

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		token, err := tryDailySimulationGuardianLogin(ctx, loginURL, tenantID, deviceID, email, phone, password, logger)
		if err == nil {
			return token, nil
		}
		lastErr = err
		if attempt+1 >= maxAttempts || !shouldRetryDailySimulationIAMLogin(err) {
			break
		}
		delay := dailySimulationIAMBackoffDelay(attempt, dailySimulationMockIAMRetryMinDelay, dailySimulationMockIAMRetryMaxDelay)
		if waitErr := scheduler.Wait(ctx, delay); waitErr != nil {
			return "", waitErr
		}
	}
	return "", lastErr
}

func shouldRetryDailySimulationIAMLogin(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sdkerrors.ErrRateLimited) ||
		errors.Is(err, sdkerrors.ErrServiceUnavailable) ||
		errors.Is(err, sdkerrors.ErrTimeout) {
		return true
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	if message == "" {
		return false
	}
	return strings.Contains(message, "context deadline exceeded") ||
		strings.Contains(message, "too many requests") ||
		strings.Contains(message, "service unavailable") ||
		strings.Contains(message, "bad gateway") ||
		strings.Contains(message, "gateway timeout") ||
		strings.Contains(message, "status=429") ||
		strings.Contains(message, "status=502") ||
		strings.Contains(message, "status=503") ||
		strings.Contains(message, "status=504")
}

func dailySimulationIAMBackoffDelay(attempt int, minDelay, maxDelay time.Duration) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := minDelay << attempt
	if delay > maxDelay {
		delay = maxDelay
	}
	return delay
}

func configureDailySimulationMockIAMClient(client *APIClient) {
	if client == nil {
		return
	}
	client.SetHTTPTimeout(dailySimulationMockIAMTimeout)
	client.SetRetryConfig(RetryConfig{
		MaxRetries: dailySimulationMockIAMRetryMax,
		MinDelay:   dailySimulationMockIAMRetryMinDelay.String(),
		MaxDelay:   dailySimulationMockIAMRetryMaxDelay.String(),
	})
}

func acquireDailySimulationMockIAMLimiter(ctx context.Context, limiter chan struct{}) (func(), error) {
	if limiter == nil {
		return func() {}, nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case limiter <- struct{}{}:
		return func() {
			select {
			case <-limiter:
			default:
			}
		}, nil
	}
}
