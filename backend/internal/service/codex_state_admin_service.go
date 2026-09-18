package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const CodexStateDefaultModel = "gpt-6-astra"

type CodexStateAdminService struct {
	accountRepo AccountRepository
	proxyRepo   ProxyRepository
	manager     *CodexStateManager
}

func NewCodexStateAdminService(
	accountRepo AccountRepository,
	proxyRepo ProxyRepository,
	manager *CodexStateManager,
) *CodexStateAdminService {
	return &CodexStateAdminService{
		accountRepo: accountRepo,
		proxyRepo:   proxyRepo,
		manager:     manager,
	}
}

type CodexStateAdminOverview struct {
	DefaultModel string                    `json:"default_model"`
	Accounts     []CodexStateAdminAccount  `json:"accounts"`
	Proxies      []CodexStateAdminProxyDTO `json:"proxies"`
}

type CodexStateAdminAccount struct {
	ID                   int64                  `json:"id"`
	Name                 string                 `json:"name"`
	Status               string                 `json:"status"`
	Schedulable          bool                   `json:"schedulable"`
	Mode                 string                 `json:"mode"`
	AutoMint             bool                   `json:"auto_mint"`
	Concurrency          int                    `json:"concurrency"`
	RefreshBeforeMinutes int                    `json:"refresh_before_minutes"`
	Degraded             bool                   `json:"degraded"`
	NormalProxyID        *int64                 `json:"normal_proxy_id,omitempty"`
	ConfiguredModels     []string               `json:"configured_models"`
	StateProxyIDs        []int64                `json:"state_proxy_ids"`
	Models               []CodexStateAdminModel `json:"models"`
}

type CodexStateAdminModel struct {
	Model      string                    `json:"model"`
	Degraded   bool                      `json:"degraded"`
	Last292At  *time.Time                `json:"last_292_at,omitempty"`
	Last312At  *time.Time                `json:"last_312_at,omitempty"`
	LastMintAt *time.Time                `json:"last_mint_at,omitempty"`
	Current    *CodexStateAdminStateMeta `json:"current,omitempty"`
	Previous   *CodexStateAdminStateMeta `json:"previous,omitempty"`
	MintRun    CodexStateMintRunStatus   `json:"mint_run"`
}

type CodexStateAdminStateMeta struct {
	IssuedAt   time.Time `json:"issued_at"`
	Digest     string    `json:"digest"`
	DecodedLen int       `json:"decoded_length"`
	Blocks     int       `json:"ciphertext_blocks"`
	Length     int       `json:"length"`
	ProxyID    int64     `json:"proxy_id,omitempty"`
	AcquiredAt time.Time `json:"acquired_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	VerifiedAt time.Time `json:"verified_at"`
}

type CodexStateAdminProxyDTO struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Status   string `json:"status"`
}

type CodexStateAdminUpdateInput struct {
	Mode                 *string
	AutoMint             *bool
	Concurrency          *int
	RefreshBeforeMinutes *int
	Models               *[]string
	NormalProxyID        *int64
	StateProxyIDs        *[]int64
}

func (s *CodexStateAdminService) Overview(ctx context.Context) (*CodexStateAdminOverview, error) {
	if s == nil || s.accountRepo == nil {
		return &CodexStateAdminOverview{DefaultModel: CodexStateDefaultModel}, nil
	}
	accounts, err := s.accountRepo.ListAllWithFilters(ctx, PlatformOpenAI, AccountTypeOAuth, "", "", 0, "")
	if err != nil {
		return nil, err
	}
	out := &CodexStateAdminOverview{
		DefaultModel: CodexStateDefaultModel,
		Accounts:     make([]CodexStateAdminAccount, 0, len(accounts)),
	}
	for i := range accounts {
		account := &accounts[i]
		if account == nil || account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth {
			continue
		}
		out.Accounts = append(out.Accounts, s.buildOverviewAccount(ctx, account))
	}
	sort.Slice(out.Accounts, func(i, j int) bool {
		return out.Accounts[i].ID < out.Accounts[j].ID
	})
	if s.proxyRepo != nil {
		proxies, err := s.proxyRepo.ListActive(ctx)
		if err != nil {
			return nil, err
		}
		out.Proxies = make([]CodexStateAdminProxyDTO, 0, len(proxies))
		for i := range proxies {
			proxy := &proxies[i]
			out.Proxies = append(out.Proxies, CodexStateAdminProxyDTO{
				ID:       proxy.ID,
				Name:     proxy.Name,
				Protocol: proxy.Protocol,
				Host:     proxy.Host,
				Port:     proxy.Port,
				Status:   proxy.Status,
			})
		}
		sort.Slice(out.Proxies, func(i, j int) bool {
			return out.Proxies[i].ID < out.Proxies[j].ID
		})
	}
	return out, nil
}

func (s *CodexStateAdminService) buildOverviewAccount(ctx context.Context, account *Account) CodexStateAdminAccount {
	item := CodexStateAdminAccount{
		ID:                   account.ID,
		Name:                 account.Name,
		Status:               account.Status,
		Schedulable:          account.Schedulable,
		Mode:                 codexStateMode(account),
		AutoMint:             codexStateModeAllowsAutoMint(codexStateMode(account)),
		Concurrency:          codexStateMintConcurrency(account),
		RefreshBeforeMinutes: int(codexStateRefreshBefore(account) / time.Minute),
		ConfiguredModels:     codexStateConfiguredModels(account),
		NormalProxyID:        account.ProxyID,
		StateProxyIDs:        codexStateProxyIDs(account),
		Models:               make([]CodexStateAdminModel, 0),
	}
	if account.Extra != nil {
		if degraded, ok := account.Extra[CodexStateDegradedExtraKey].(bool); ok {
			item.Degraded = degraded
		}
	}
	models := codexStateConfiguredModels(account)
	if len(models) == 0 && item.Mode != CodexStateModeOff {
		models = []string{CodexStateDefaultModel}
	}
	for _, model := range models {
		status := s.manager.ModelStatus(account, model)
		entry := CodexStateAdminModel{
			Model:      model,
			Degraded:   status.Degraded,
			Last292At:  status.Last292At,
			Last312At:  status.Last312At,
			LastMintAt: status.LastMintAt,
			MintRun:    s.manager.MintRunStatus(account.ID, model),
		}
		snapshot, snapshotErr := s.manager.Snapshot(ctx, account.ID, model)
		if snapshotErr == nil && snapshot != nil {
			entry.Current = codexStateAdminMeta(snapshot.Current)
			entry.Previous = codexStateAdminMeta(snapshot.Previous)
		}
		if item.Mode == CodexStateModeAuto && (snapshotErr != nil || snapshot == nil || snapshot.Current == nil ||
			snapshot.Current.ExpiresAt.Sub(s.manager.now()) <= codexStateRefreshBefore(account)) {
			s.manager.TriggerMint(account, model)
		}
		item.Models = append(item.Models, entry)
	}
	return item
}

func codexStateConfiguredModels(account *Account) []string {
	if account == nil || account.Extra == nil {
		return nil
	}
	if explicit := normalizeCodexStateModels(anySlice(account.Extra[CodexStateManagedModelsExtraKey])); len(explicit) > 0 {
		return explicit
	}
	raw, ok := account.Extra[CodexStateModelsExtraKey].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	values := make([]any, 0, len(raw))
	for model := range raw {
		values = append(values, model)
	}
	return normalizeCodexStateModels(values)
}

func normalizeCodexStateModels(values []any) []string {
	seen := make(map[string]struct{}, len(values))
	models := make([]string, 0, len(values))
	for _, value := range values {
		model := normalizeCodexStateModel(fmt.Sprint(value))
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}

func codexStateStringsToAny(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func codexStateAdminMeta(entry *CodexTurnStateEntry) *CodexStateAdminStateMeta {
	if entry == nil {
		return nil
	}
	return &CodexStateAdminStateMeta{
		Length:     len(entry.Value),
		ProxyID:    entry.ProxyID,
		AcquiredAt: entry.AcquiredAt,
		ExpiresAt:  entry.ExpiresAt,
		VerifiedAt: entry.VerifiedAt,
		IssuedAt:   entry.IssuedAt,
		Digest:     entry.Digest,
		DecodedLen: entry.DecodedLen,
		Blocks:     entry.Blocks,
	}
}

func (s *CodexStateAdminService) UpdateConfig(
	ctx context.Context,
	accountID int64,
	input CodexStateAdminUpdateInput,
) (*CodexStateAdminAccount, error) {
	if s == nil || s.accountRepo == nil {
		return nil, errors.New("codex state admin service is not configured")
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, ErrAccountNotFound
	}
	if account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth {
		return nil, errors.New("codex state is only supported for OpenAI OAuth accounts")
	}
	if input.NormalProxyID != nil {
		if *input.NormalProxyID > 0 {
			if err := s.validateProxyIDs(ctx, []int64{*input.NormalProxyID}); err != nil {
				return nil, err
			}
			account.ProxyID = input.NormalProxyID
		} else {
			account.ProxyID = nil
		}
	}
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	requestedMode := (*string)(nil)
	if input.Mode != nil {
		mode := strings.ToLower(strings.TrimSpace(*input.Mode))
		switch mode {
		case CodexStateModeOff, CodexStateModeObserve, CodexStateModeManual, CodexStateModeAuto:
		default:
			return nil, errors.New("codex state mode must be off, observe, manual, or auto")
		}
		requestedMode = &mode
	} else if input.AutoMint != nil {
		mode := CodexStateModeOff
		if *input.AutoMint {
			mode = CodexStateModeAuto
		}
		requestedMode = &mode
	}
	if requestedMode != nil {
		account.Extra[CodexStateModeExtraKey] = *requestedMode
		account.Extra[CodexStateAutoMintExtraKey] = *requestedMode == CodexStateModeAuto
	}
	if input.Concurrency != nil {
		if *input.Concurrency < CodexStateConcurrencyMin || *input.Concurrency > CodexStateConcurrencyMax {
			return nil, errors.New("codex state concurrency must be 1")
		}
		account.Extra[CodexStateConcurrencyExtraKey] = *input.Concurrency
	}
	if input.RefreshBeforeMinutes != nil {
		if *input.RefreshBeforeMinutes < CodexStateRefreshBeforeMin ||
			*input.RefreshBeforeMinutes > CodexStateRefreshBeforeMax {
			return nil, errors.New("codex state refresh-before minutes must be between 1 and 60")
		}
		account.Extra[CodexStateRefreshBeforeExtraKey] = *input.RefreshBeforeMinutes
	}
	if input.Models != nil {
		models := normalizeCodexStateModels(codexStateStringsToAny(*input.Models))
		if len(models) == 0 {
			delete(account.Extra, CodexStateManagedModelsExtraKey)
		} else {
			account.Extra[CodexStateManagedModelsExtraKey] = models
		}
	}
	if input.StateProxyIDs != nil {
		ids := normalizeProxyIDs(*input.StateProxyIDs)
		if err := s.validateProxyIDs(ctx, ids); err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			delete(account.Extra, CodexStateProxyIDsExtraKey)
		} else {
			account.Extra[CodexStateProxyIDsExtraKey] = ids
		}
	}
	if err := s.accountRepo.Update(ctx, account); err != nil {
		return nil, err
	}
	if requestedMode != nil && s.manager != nil {
		switch *requestedMode {
		case CodexStateModeAuto:
			s.startAutoMintTakeover(account)
		case CodexStateModeManual:
			s.manager.StopMint(accountID)
		case CodexStateModeObserve, CodexStateModeOff:
			s.manager.ClearAccount(ctx, account)
		}
	}
	if input.RefreshBeforeMinutes != nil && s.manager != nil && codexStateModeAllowsAutoMint(codexStateMode(account)) {
		s.rescheduleAutoMint(account)
	}
	item := s.buildOverviewAccount(ctx, account)
	return &item, nil
}

func (s *CodexStateAdminService) startAutoMintTakeover(account *Account) {
	if s == nil || s.manager == nil || account == nil {
		return
	}
	models := codexStateConfiguredModels(account)
	if len(models) == 0 {
		models = []string{CodexStateDefaultModel}
	}
	for _, model := range models {
		s.manager.TriggerMint(account, model)
	}
}

func (s *CodexStateAdminService) rescheduleAutoMint(account *Account) {
	if s == nil || s.manager == nil || account == nil {
		return
	}
	models := codexStateConfiguredModels(account)
	if len(models) == 0 {
		models = []string{CodexStateDefaultModel}
	}
	for _, model := range models {
		snapshot, err := s.manager.Snapshot(context.Background(), account.ID, model)
		if err != nil || snapshot == nil || snapshot.Current == nil {
			s.manager.TriggerMint(account, model)
			continue
		}
		if snapshot.Current.ExpiresAt.Sub(s.manager.now()) <= codexStateRefreshBefore(account) {
			s.manager.TriggerMint(account, model)
			continue
		}
		s.manager.scheduleRefresh(account, model, snapshot.Current)
	}
}

func (s *CodexStateAdminService) TriggerMint(
	ctx context.Context,
	accountID int64,
	model string,
) error {
	if s == nil || s.accountRepo == nil || s.manager == nil {
		return errors.New("codex state admin service is not configured")
	}
	model = normalizeCodexStateModel(model)
	if model == "" {
		model = CodexStateDefaultModel
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return err
	}
	if account == nil {
		return ErrAccountNotFound
	}
	if account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth {
		return errors.New("codex state is only supported for OpenAI OAuth accounts")
	}
	if !codexStateModeAllowsInjection(codexStateMode(account)) {
		return errors.New("codex state mode must be manual or auto before capture")
	}
	s.manager.TriggerMintForce(account, model)
	return nil
}

func (s *CodexStateAdminService) validateProxyIDs(ctx context.Context, ids []int64) error {
	ids = normalizeProxyIDs(ids)
	if len(ids) == 0 {
		return nil
	}
	if s.proxyRepo == nil {
		return errors.New("proxy repository is not configured")
	}
	proxies, err := s.proxyRepo.ListByIDs(ctx, ids)
	if err != nil {
		return err
	}
	found := make(map[int64]struct{}, len(proxies))
	for i := range proxies {
		found[proxies[i].ID] = struct{}{}
	}
	for _, id := range ids {
		if _, ok := found[id]; !ok {
			return ErrProxyNotFound
		}
	}
	return nil
}

func normalizeProxyIDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
