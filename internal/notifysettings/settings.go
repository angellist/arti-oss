// Package notifysettings decides, at send time, whether a Slack notification
// may go out to a particular person.
//
// A notification is a message to one person, so that person decides whether to
// receive it. One deployment-wide switch sits above their choices, for an admin
// to stop everything at once: notifications used to be governed only by whether
// a bot token was configured, so on 2026-09-04 the only way to stop one that was
// firing wrongly was to roll the service back.
package notifysettings

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/angellist/arti-oss/gen/sqlc"
)

// Category names one kind of notification. The value is the stored key, so
// renaming one silently resets everybody to the default; add a new one instead.
type Category string

const (
	// Master is the deployment-wide switch. Off here means nothing is sent to
	// anyone, whatever they chose. It belongs to an admin, not to a person.
	Master Category = "notifications.slack.enabled"
	// Comments is the @mention DM on a comment thread.
	Comments Category = "notifications.slack.comments"
	// CredentialMint is the DM sent when a key is created under someone's name.
	CredentialMint Category = "notifications.slack.credential_mint"
	// CredentialNewSource is the DM sent when a key answers from a new network.
	CredentialNewSource Category = "notifications.slack.credential_new_source"
	// OwnerTransfer fires when someone hands you a document. Acquiring
	// authority over a document without being told is the case it exists for.
	OwnerTransfer Category = "notifications.slack.owner_transfer"
)

// All is every category, master first.
var All = []Category{Master, Comments, CredentialMint, CredentialNewSource, OwnerTransfer}

// Personal is what a person chooses for themselves.
var Personal = []Category{Comments, CredentialMint, CredentialNewSource, OwnerTransfer}

// ErrMasterIsNotPersonal rejects an attempt to set the deployment switch as if
// it were somebody's own preference.
var ErrMasterIsNotPersonal = errors.New("the deployment switch is not a personal setting")

// Label is what a settings page calls a category.
func (c Category) Label() string {
	switch c {
	case Master:
		return "Slack notifications (whole deployment)"
	case Comments:
		return "Comment mentions"
	case CredentialMint:
		return "An API key is created under my name"
	case CredentialNewSource:
		return "One of my keys is used from a new network"
	case OwnerTransfer:
		return "Someone hands me a document"
	}
	return string(c)
}

// Description is the line under a category on the settings page.
func (c Category) Description() string {
	switch c {
	case Comments:
		return "Someone mentions you, replies to your thread, or resolves it."
	case CredentialMint:
		return "Every key also appears in Settings → API Keys."
	case CredentialNewSource:
		return "Every source is listed against the key in Settings → API Keys."
	case OwnerTransfer:
		return "You become responsible for its access, comments and sharing."
	}
	return ""
}

// defaultEnabled is what a category does for someone who has never touched it.
//
// Comment mentions are on, because they have been since they shipped and people
// rely on them to learn they were mentioned; turning them off in a deploy would
// be a regression nobody asked for. The credential categories are off, because
// they are new, they were wrong the first time, and a notification of that kind
// should be something a person chose. Owner transfer is on despite being new
// because it is not a stream: it fires once, names one person, and tells them
// they are now answerable for a document. Off by default would ship a
// notification that mostly does not arrive. The master is on, so the deployment
// switch is only ever something an admin reaches for deliberately.
func (c Category) defaultEnabled() bool {
	switch c {
	case Master, Comments, OwnerTransfer:
		return true
	default:
		return false
	}
}

// IsPersonal reports whether a person may set this category for themselves.
func (c Category) IsPersonal() bool { return c != Master }

type store interface {
	ListAppSettings(ctx context.Context) ([]sqlc.AppSetting, error)
	UpsertAppSetting(ctx context.Context, arg sqlc.UpsertAppSettingParams) error
	ListUserNotificationSettings(ctx context.Context) ([]sqlc.ListUserNotificationSettingsRow, error)
	UpsertUserNotificationSetting(ctx context.Context, arg sqlc.UpsertUserNotificationSettingParams) error
}

// cacheTTL bounds how long a flipped switch takes to be obeyed. Short, because
// someone turning a noisy notification off is usually doing it in a hurry; long
// enough that fanning a comment out to a thread's participants is not a query
// per recipient.
const cacheTTL = 15 * time.Second

// Settings answers "may this be sent to this person", and lets people change
// the answer for themselves.
type Settings struct {
	store  store
	logger *slog.Logger

	mu      sync.RWMutex
	global  map[Category]bool
	perUser map[string]map[Category]bool
	fetched time.Time
}

func New(s store, logger *slog.Logger) *Settings {
	if logger == nil {
		logger = slog.Default()
	}
	return &Settings{store: s, logger: logger}
}

// EnabledFor reports whether this category may be sent to recipient right now:
// the deployment switch is on, and that person has not turned the category off
// (or has turned on one that starts off). An unreadable settings table answers
// false — silence is the safe direction, and a notification nobody can stop is
// what this exists to prevent.
func (s *Settings) EnabledFor(ctx context.Context, recipient string, c Category) bool {
	global, users, err := s.load(ctx)
	if err != nil {
		s.logger.Warn("notification settings unreadable; sending nothing", "err", err)
		return false
	}
	if !global[Master] {
		return false
	}
	if c == Master {
		return true
	}
	if choice, ok := users[normalize(recipient)][c]; ok {
		return choice
	}
	return c.defaultEnabled()
}

// MasterEnabled reports the deployment-wide switch.
func (s *Settings) MasterEnabled(ctx context.Context) (bool, error) {
	global, _, err := s.load(ctx)
	if err != nil {
		return false, err
	}
	return global[Master], nil
}

// SetMaster writes the deployment-wide switch. The caller enforces that only an
// admin reaches this, and is recorded as the actor.
func (s *Settings) SetMaster(ctx context.Context, enabled bool, actor string) error {
	if err := s.store.UpsertAppSetting(ctx, sqlc.UpsertAppSettingParams{
		Key: string(Master), Enabled: enabled, UpdatedBy: actor,
	}); err != nil {
		return err
	}
	s.invalidate()
	s.logger.Info("deployment notification switch changed", "enabled", enabled, "by", actor)
	return nil
}

// CurrentFor returns one person's own categories, for their settings page.
func (s *Settings) CurrentFor(ctx context.Context, recipient string) (map[Category]bool, error) {
	_, users, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[Category]bool, len(Personal))
	for _, c := range Personal {
		if choice, ok := users[normalize(recipient)][c]; ok {
			out[c] = choice
			continue
		}
		out[c] = c.defaultEnabled()
	}
	return out, nil
}

// SetFor writes one person's choice about one category.
func (s *Settings) SetFor(ctx context.Context, recipient string, c Category, enabled bool) error {
	if !c.IsPersonal() {
		return ErrMasterIsNotPersonal
	}
	if err := s.store.UpsertUserNotificationSetting(ctx, sqlc.UpsertUserNotificationSettingParams{
		UserEmail: normalize(recipient), Key: string(c), Enabled: enabled,
	}); err != nil {
		return err
	}
	s.invalidate()
	return nil
}

func (s *Settings) invalidate() {
	s.mu.Lock()
	s.fetched = time.Time{}
	s.mu.Unlock()
}

// load returns the deployment switches and everyone's choices, cached together.
// Both tables are read whole: user_notification_settings holds a row only for
// someone who changed something, so it stays far smaller than the set of people
// a comment thread fans out to, and one read serves the whole fan-out.
func (s *Settings) load(ctx context.Context) (map[Category]bool, map[string]map[Category]bool, error) {
	s.mu.RLock()
	if time.Since(s.fetched) < cacheTTL && s.global != nil {
		g, u := s.global, s.perUser
		s.mu.RUnlock()
		return g, u, nil
	}
	s.mu.RUnlock()

	appRows, err := s.store.ListAppSettings(ctx)
	if err != nil {
		return nil, nil, err
	}
	userRows, err := s.store.ListUserNotificationSettings(ctx)
	if err != nil {
		return nil, nil, err
	}

	global := map[Category]bool{Master: Master.defaultEnabled()}
	for _, row := range appRows {
		global[Category(row.Key)] = row.Enabled
	}
	perUser := make(map[string]map[Category]bool)
	for _, row := range userRows {
		email := normalize(row.UserEmail)
		if perUser[email] == nil {
			perUser[email] = make(map[Category]bool, len(Personal))
		}
		perUser[email][Category(row.Key)] = row.Enabled
	}

	s.mu.Lock()
	s.global, s.perUser, s.fetched = global, perUser, time.Now()
	s.mu.Unlock()
	return global, perUser, nil
}

func normalize(email string) string { return strings.ToLower(strings.TrimSpace(email)) }
