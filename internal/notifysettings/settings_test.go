package notifysettings

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/angellist/arti-oss/gen/sqlc"
)

type fakeStore struct {
	app      []sqlc.AppSetting
	users    []sqlc.ListUserNotificationSettingsRow
	listErr  error
	appWrite []sqlc.UpsertAppSettingParams
	usrWrite []sqlc.UpsertUserNotificationSettingParams
}

func (f *fakeStore) ListAppSettings(context.Context) ([]sqlc.AppSetting, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.app, nil
}

func (f *fakeStore) UpsertAppSetting(_ context.Context, arg sqlc.UpsertAppSettingParams) error {
	f.appWrite = append(f.appWrite, arg)
	f.app = append(f.app, sqlc.AppSetting{Key: arg.Key, Enabled: arg.Enabled})
	return nil
}

func (f *fakeStore) ListUserNotificationSettings(context.Context) ([]sqlc.ListUserNotificationSettingsRow, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.users, nil
}

func (f *fakeStore) UpsertUserNotificationSetting(_ context.Context, arg sqlc.UpsertUserNotificationSettingParams) error {
	f.usrWrite = append(f.usrWrite, arg)
	f.users = append(f.users, sqlc.ListUserNotificationSettingsRow{
		UserEmail: arg.UserEmail, Key: arg.Key, Enabled: arg.Enabled,
	})
	return nil
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

const alice = "alice@example.com"

// Nothing is seeded, so an empty table is the shipped policy: comment mentions
// keep working, because taking them away would be a regression nobody asked
// for, and the credential notifications stay off until a person chooses them.
func TestDefaultsWithNothingStored(t *testing.T) {
	s := New(&fakeStore{}, discardLogger())
	ctx := context.Background()

	if !s.EnabledFor(ctx, alice, Comments) {
		t.Error("comment mentions are off by default; they have always been sent and nobody asked to stop them")
	}
	for _, c := range []Category{CredentialMint, CredentialNewSource} {
		if s.EnabledFor(ctx, alice, c) {
			t.Errorf("%s is on by default; credential notifications must be opted into", c)
		}
	}
}

// The whole point of moving this off an admin page: one person's choice is
// theirs, and must not reach anybody else.
func TestOnePersonsChoiceDoesNotAffectAnother(t *testing.T) {
	st := &fakeStore{users: []sqlc.ListUserNotificationSettingsRow{
		{UserEmail: alice, Key: string(Comments), Enabled: false},
		{UserEmail: alice, Key: string(CredentialNewSource), Enabled: true},
	}}
	s := New(st, discardLogger())
	ctx := context.Background()

	if s.EnabledFor(ctx, alice, Comments) {
		t.Error("alice still receives comment mentions after switching them off")
	}
	if !s.EnabledFor(ctx, "bob@example.com", Comments) {
		t.Error("bob lost comment mentions because alice turned hers off")
	}
	if !s.EnabledFor(ctx, alice, CredentialNewSource) {
		t.Error("alice opted into new-network alerts and is not getting them")
	}
	if s.EnabledFor(ctx, "bob@example.com", CredentialNewSource) {
		t.Error("bob was opted in by alice's choice")
	}
}

// An email that differs only in case is the same person.
func TestRecipientMatchIgnoresCase(t *testing.T) {
	st := &fakeStore{users: []sqlc.ListUserNotificationSettingsRow{
		{UserEmail: alice, Key: string(Comments), Enabled: false},
	}}
	s := New(st, discardLogger())

	if s.EnabledFor(context.Background(), "Alice@Example.com", Comments) {
		t.Error("a mixed-case address missed the person's own setting")
	}
}

// The deployment switch is the one lever that stops everything at once, which
// is what was missing when a misfiring notification could only be stopped by
// rolling the service back.
func TestDeploymentSwitchOffSilencesEveryone(t *testing.T) {
	st := &fakeStore{
		app:   []sqlc.AppSetting{{Key: string(Master), Enabled: false}},
		users: []sqlc.ListUserNotificationSettingsRow{{UserEmail: alice, Key: string(CredentialMint), Enabled: true}},
	}
	s := New(st, discardLogger())
	ctx := context.Background()

	if s.EnabledFor(ctx, alice, Comments) || s.EnabledFor(ctx, alice, CredentialMint) {
		t.Error("something was sent with the deployment switch off")
	}
}

// Turning the deployment switch on returns everyone to their own choices; it
// does not opt anyone into a category they never asked for.
func TestDeploymentSwitchOnRestoresPersonalChoices(t *testing.T) {
	st := &fakeStore{app: []sqlc.AppSetting{{Key: string(Master), Enabled: true}}}
	s := New(st, discardLogger())
	ctx := context.Background()

	if !s.EnabledFor(ctx, alice, Comments) {
		t.Error("comment mentions off with the deployment switch on and no personal choice")
	}
	if s.EnabledFor(ctx, alice, CredentialNewSource) {
		t.Error("a credential notification sent because the deployment switch is on")
	}
}

// A database that cannot answer must not be read as permission to send.
func TestUnreadableSettingsSendNothing(t *testing.T) {
	s := New(&fakeStore{listErr: errors.New("connection refused")}, discardLogger())

	if s.EnabledFor(context.Background(), alice, Comments) {
		t.Error("sent while the settings were unreadable; silence is the safe direction")
	}
}

// Someone turning a notification off is usually in a hurry, so the change must
// not wait out the cache.
func TestSetTakesEffectImmediately(t *testing.T) {
	st := &fakeStore{}
	s := New(st, discardLogger())
	ctx := context.Background()
	if !s.EnabledFor(ctx, alice, Comments) {
		t.Fatal("comments should start enabled here")
	}

	if err := s.SetFor(ctx, alice, Comments, false); err != nil {
		t.Fatalf("set: %v", err)
	}

	if s.EnabledFor(ctx, alice, Comments) {
		t.Error("still sending after being switched off; the cache outlived the change")
	}
	if len(st.usrWrite) != 1 || st.usrWrite[0].UserEmail != alice {
		t.Errorf("writes = %+v, want one row for that person", st.usrWrite)
	}
}

// The deployment switch is not something a person can set for themselves, or
// one user could silence everyone.
func TestMasterIsNotAPersonalSetting(t *testing.T) {
	s := New(&fakeStore{}, discardLogger())

	if err := s.SetFor(context.Background(), alice, Master, false); !errors.Is(err, ErrMasterIsNotPersonal) {
		t.Fatalf("err = %v, want ErrMasterIsNotPersonal", err)
	}
}

// CurrentFor drives the person's own page, so it must show their choices and
// leave the deployment switch out of them.
func TestCurrentForShowsOnlyPersonalCategories(t *testing.T) {
	st := &fakeStore{users: []sqlc.ListUserNotificationSettingsRow{
		{UserEmail: alice, Key: string(CredentialMint), Enabled: true},
	}}
	s := New(st, discardLogger())

	mine, err := s.CurrentFor(context.Background(), alice)
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if len(mine) != len(Personal) {
		t.Fatalf("categories = %d, want %d", len(mine), len(Personal))
	}
	if _, ok := mine[Master]; ok {
		t.Error("the deployment switch appeared among a person's own settings")
	}
	if !mine[CredentialMint] || !mine[Comments] || mine[CredentialNewSource] {
		t.Errorf("state = %+v, want the stored choice plus the defaults", mine)
	}
}

// The master's own setter records who changed it, since it affects everyone.
func TestSetMasterRecordsTheActor(t *testing.T) {
	st := &fakeStore{}
	s := New(st, discardLogger())

	if err := s.SetMaster(context.Background(), false, "admin@example.com"); err != nil {
		t.Fatalf("set master: %v", err)
	}
	if len(st.appWrite) != 1 || st.appWrite[0].UpdatedBy != "admin@example.com" {
		t.Fatalf("writes = %+v, want one recording the admin", st.appWrite)
	}
	if enabled, _ := s.MasterEnabled(context.Background()); enabled {
		t.Error("master still reads as on after being switched off")
	}
}
